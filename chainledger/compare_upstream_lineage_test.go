package chainledger

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

// compareDoc builds a lineage document from names and directed dependencies,
// so tests can freely reorder or repeat nodes and edges without hand-writing
// JSON.
func compareDoc(t *testing.T, nodes []string, edges [][2]string) string {
	t.Helper()
	type edge struct {
		From string `json:"from"`
		To   string `json:"to"`
	}
	doc := struct {
		Nodes []string `json:"nodes"`
		Edges []edge   `json:"edges"`
	}{Nodes: nodes}
	for _, e := range edges {
		doc.Edges = append(doc.Edges, edge{From: e[0], To: e[1]})
	}
	data, err := json.Marshal(doc)
	if err != nil {
		t.Fatalf("marshal document: %v", err)
	}
	return string(data)
}

func depPairs(deps []Dependency) [][2]string {
	pairs := make([][2]string, len(deps))
	for i, d := range deps {
		pairs[i] = [2]string{d.From, d.To}
	}
	return pairs
}

// Identical documents report no change as a success with two non-nil empty
// lists, and datasets outside the target's derivation (an independent node and
// the target's downstream) never enter either list.
func TestCompareUpstreamLineageNoChange(t *testing.T) {
	before := compareDoc(t,
		[]string{"s", "m", "t", "d", "lone"},
		[][2]string{{"s", "m"}, {"m", "t"}, {"t", "d"}})
	after := before

	diff, err := CompareUpstreamLineage(before, after, "t")
	if err != nil {
		t.Fatalf("CompareUpstreamLineage: %v", err)
	}
	if diff.Added == nil || diff.Removed == nil {
		t.Fatalf("no-change result must hold non-nil empty lists, got %+v", diff)
	}
	if len(diff.Added) != 0 || len(diff.Removed) != 0 {
		t.Fatalf("expected no change, got added=%v removed=%v", diff.Added, diff.Removed)
	}
}

// A target with no upstreams in both documents compares cleanly even while
// unrelated lineage elsewhere changes between the two documents.
func TestCompareUpstreamLineageIgnoresUnrelatedChanges(t *testing.T) {
	before := compareDoc(t, []string{"t", "x", "y"}, [][2]string{{"x", "y"}})
	// t stays a root: the changed x/y/z chain is independent, and t gains a
	// downstream, which must never enter its upstream comparison.
	after := compareDoc(t,
		[]string{"t", "x", "y", "z", "down"},
		[][2]string{{"x", "y"}, {"y", "z"}, {"t", "down"}})

	diff, err := CompareUpstreamLineage(before, after, "t")
	if err != nil {
		t.Fatalf("CompareUpstreamLineage: %v", err)
	}
	if len(diff.Added) != 0 || len(diff.Removed) != 0 {
		t.Fatalf("root target should see no change, got added=%v removed=%v",
			depPairs(diff.Added), depPairs(diff.Removed))
	}
}

// Direct dependencies added and removed at the target are reported with the
// exports' from-upstream/to-derived direction.
func TestCompareUpstreamLineageDirectAddAndRemove(t *testing.T) {
	before := compareDoc(t,
		[]string{"keep", "gone", "soon", "t"},
		[][2]string{{"keep", "t"}, {"gone", "t"}})
	after := compareDoc(t,
		[]string{"keep", "gone", "soon", "t"},
		[][2]string{{"keep", "t"}, {"soon", "t"}})

	diff, err := CompareUpstreamLineage(before, after, "t")
	if err != nil {
		t.Fatalf("CompareUpstreamLineage: %v", err)
	}
	if got, want := depPairs(diff.Added), [][2]string{{"soon", "t"}}; !reflect.DeepEqual(got, want) {
		t.Errorf("Added = %v, want %v", got, want)
	}
	if got, want := depPairs(diff.Removed), [][2]string{{"gone", "t"}}; !reflect.DeepEqual(got, want) {
		t.Errorf("Removed = %v, want %v", got, want)
	}
}

// The target reaches s both directly and through m. Removing only the longer
// route's edge m -> t must still show, and because that deletion detaches the
// whole m segment from the target's derivation, s -> m — which belonged to the
// before scope — is removed as well. Nothing is added.
func TestCompareUpstreamLineageLongRouteEdgeDeletion(t *testing.T) {
	before := compareDoc(t,
		[]string{"s", "m", "t"},
		[][2]string{{"s", "t"}, {"s", "m"}, {"m", "t"}})
	after := compareDoc(t,
		[]string{"s", "m", "t"},
		[][2]string{{"s", "t"}, {"s", "m"}})

	diff, err := CompareUpstreamLineage(before, after, "t")
	if err != nil {
		t.Fatalf("CompareUpstreamLineage: %v", err)
	}
	if len(diff.Added) != 0 {
		t.Errorf("Added = %v, want none", depPairs(diff.Added))
	}
	wantRemoved := [][2]string{{"m", "t"}, {"s", "m"}}
	if got := depPairs(diff.Removed); !reflect.DeepEqual(got, wantRemoved) {
		t.Errorf("Removed = %v, want %v", got, wantRemoved)
	}
}

// Deleting only the direct s -> t edge keeps the longer route intact, so just
// the direct relationship is removed and the route's edges stay common.
func TestCompareUpstreamLineageDirectEdgeDeletionKeepsRoute(t *testing.T) {
	before := compareDoc(t,
		[]string{"s", "m", "t"},
		[][2]string{{"s", "t"}, {"s", "m"}, {"m", "t"}})
	after := compareDoc(t,
		[]string{"s", "m", "t"},
		[][2]string{{"s", "m"}, {"m", "t"}})

	diff, err := CompareUpstreamLineage(before, after, "t")
	if err != nil {
		t.Fatalf("CompareUpstreamLineage: %v", err)
	}
	if got, want := depPairs(diff.Removed), [][2]string{{"s", "t"}}; !reflect.DeepEqual(got, want) {
		t.Errorf("Removed = %v, want %v", got, want)
	}
	if len(diff.Added) != 0 {
		t.Errorf("Added = %v, want none", depPairs(diff.Added))
	}
}

// The target's direct upstreams do not change — it still derives from m — but
// m's own source swaps from old to new. The actual changed dependencies show,
// proving the comparison is over the full upstream scope rather than the
// target's direct upstream list.
func TestCompareUpstreamLineageAncestorSourceSwap(t *testing.T) {
	before := compareDoc(t,
		[]string{"old", "new", "m", "t"},
		[][2]string{{"m", "t"}, {"old", "m"}})
	after := compareDoc(t,
		[]string{"old", "new", "m", "t"},
		[][2]string{{"m", "t"}, {"new", "m"}})

	diff, err := CompareUpstreamLineage(before, after, "t")
	if err != nil {
		t.Fatalf("CompareUpstreamLineage: %v", err)
	}
	if got, want := depPairs(diff.Added), [][2]string{{"new", "m"}}; !reflect.DeepEqual(got, want) {
		t.Errorf("Added = %v, want %v", got, want)
	}
	if got, want := depPairs(diff.Removed), [][2]string{{"old", "m"}}; !reflect.DeepEqual(got, want) {
		t.Errorf("Removed = %v, want %v", got, want)
	}
}

// A new multi-hop branch appearing is reported edge by edge, and results are
// ordered by From then To in Go string order regardless of document order.
func TestCompareUpstreamLineageNewBranchOrdering(t *testing.T) {
	// Before: a -> t only. After adds b -> mid -> t. Nodes and edges are listed
	// shuffled and partly repeated in the after document.
	before := compareDoc(t,
		[]string{"a", "b", "mid", "t"},
		[][2]string{{"a", "t"}})
	after := `{"edges":[{"from":"mid","to":"t"},{"from":"b","to":"mid"},{"from":"a","to":"t"},{"from":"b","to":"mid"}],` +
		`"nodes":["t","a","mid","b","a"]}`

	diff, err := CompareUpstreamLineage(before, after, "t")
	if err != nil {
		t.Fatalf("CompareUpstreamLineage: %v", err)
	}
	wantAdded := [][2]string{{"b", "mid"}, {"mid", "t"}}
	if got := depPairs(diff.Added); !reflect.DeepEqual(got, wantAdded) {
		t.Errorf("Added = %v, want %v", got, wantAdded)
	}
	if len(diff.Removed) != 0 {
		t.Errorf("Removed = %v, want none", depPairs(diff.Removed))
	}
}

// Both lists sort by From first and then To; duplicate edges in the documents
// never duplicate an entry.
func TestCompareUpstreamLineageSortsByFromThenTo(t *testing.T) {
	before := compareDoc(t,
		[]string{"z", "a", "m", "t"},
		[][2]string{{"z", "t"}, {"z", "m"}, {"m", "t"}})
	after := compareDoc(t,
		[]string{"z", "a", "m", "t"},
		[][2]string{
			{"z", "t"}, {"z", "m"}, {"m", "t"}, // survivors
			{"a", "t"}, {"a", "m"}, // added, both From a: order on To
		})

	diff, err := CompareUpstreamLineage(before, after, "t")
	if err != nil {
		t.Fatalf("CompareUpstreamLineage: %v", err)
	}
	wantAdded := [][2]string{{"a", "m"}, {"a", "t"}}
	if got := depPairs(diff.Added); !reflect.DeepEqual(got, wantAdded) {
		t.Errorf("Added = %v, want %v", got, wantAdded)
	}
}

// Names compare by their exact decoded value: case and surrounding spaces
// matter, so a relationship renamed only by case or whitespace is a real
// remove/add pair.
func TestCompareUpstreamLineageExactNameMatching(t *testing.T) {
	before := compareDoc(t, []string{" mid ", "t"}, [][2]string{{" mid ", "t"}})
	after := compareDoc(t, []string{" mid ", "Mid", "t"}, [][2]string{{"Mid", "t"}})

	diff, err := CompareUpstreamLineage(before, after, "t")
	if err != nil {
		t.Fatalf("CompareUpstreamLineage: %v", err)
	}
	if got, want := depPairs(diff.Added), [][2]string{{"Mid", "t"}}; !reflect.DeepEqual(got, want) {
		t.Errorf("Added = %v, want %v", got, want)
	}
	if got, want := depPairs(diff.Removed), [][2]string{{" mid ", "t"}}; !reflect.DeepEqual(got, want) {
		t.Errorf("Removed = %v, want %v", got, want)
	}
}

// An empty target is rejected without either document being inspected first:
// even malformed documents yield the missing-target error.
func TestCompareUpstreamLineageEmptyTarget(t *testing.T) {
	_, err := CompareUpstreamLineage("not json", "also not json", "")
	if err == nil || err.Error() != "target dataset name is required" {
		t.Fatalf("error = %v, want target dataset name is required", err)
	}
}

// A target missing from either valid document fails the whole comparison with
// the target named and the offending document identified, and no partial
// lists.
func TestCompareUpstreamLineageTargetNotRegistered(t *testing.T) {
	withT := compareDoc(t, []string{"s", "t"}, [][2]string{{"s", "t"}})
	// A valid document that simply does not register t: explicit empty arrays,
	// since a null edges field would make the document itself invalid and the
	// failure would be the document rejection, not the missing target.
	withoutT := `{"nodes":["s"],"edges":[]}`

	if diff, err := CompareUpstreamLineage(withoutT, withT, "t"); err == nil {
		t.Fatal("expected error when target missing from before document")
	} else {
		msg := err.Error()
		if !strings.Contains(msg, `"t"`) && !strings.Contains(msg, " t ") && !strings.HasSuffix(msg, " t") {
			t.Fatalf("error %q must name the target", msg)
		}
		if !strings.Contains(msg, "before-change") {
			t.Fatalf("error %q must identify the before-change document", msg)
		}
		if strings.Contains(msg, "rejected") {
			t.Fatalf("error %q must be the target-not-registered failure, not a document rejection", msg)
		}
		if diff.Added != nil || diff.Removed != nil {
			t.Fatalf("failed comparison must return the zero result, got %+v", diff)
		}
	}

	if diff, err := CompareUpstreamLineage(withT, withoutT, "t"); err == nil {
		t.Fatal("expected error when target missing from after document")
	} else {
		msg := err.Error()
		if !strings.Contains(msg, `"t"`) && !strings.Contains(msg, " t ") && !strings.HasSuffix(msg, " t") {
			t.Fatalf("error %q must name the target", msg)
		}
		if !strings.Contains(msg, "after-change") {
			t.Fatalf("error %q must identify the after-change document", msg)
		}
		if strings.Contains(msg, "rejected") {
			t.Fatalf("error %q must be the target-not-registered failure, not a document rejection", msg)
		}
		if diff.Added != nil || diff.Removed != nil {
			t.Fatalf("failed comparison must return the zero result, got %+v", diff)
		}
	}
}

// A document that fails import validation is rejected as a whole; the error
// states which document (before or after) was rejected and why, and no partial
// difference comes back.
func TestCompareUpstreamLineageRejectsInvalidDocuments(t *testing.T) {
	good := compareDoc(t, []string{"s", "t"}, [][2]string{{"s", "t"}})

	cyclic := compareDoc(t,
		[]string{"a", "b", "t"},
		[][2]string{{"a", "b"}, {"b", "a"}})
	dangling := compareDoc(t,
		[]string{"t"},
		[][2]string{{"ghost", "t"}})

	if _, err := CompareUpstreamLineage(cyclic, good, "t"); err == nil {
		t.Fatal("expected rejection of cyclic before document")
	} else if msg := err.Error(); !strings.Contains(msg, "before-change") || !strings.Contains(msg, "cycle") {
		t.Fatalf("error %q must identify the before-change document and its cycle", msg)
	}

	if _, err := CompareUpstreamLineage(good, cyclic, "t"); err == nil {
		t.Fatal("expected rejection of cyclic after document")
	} else if msg := err.Error(); !strings.Contains(msg, "after-change") || !strings.Contains(msg, "cycle") {
		t.Fatalf("error %q must identify the after-change document and its cycle", msg)
	}

	if _, err := CompareUpstreamLineage(good, dangling, "t"); err == nil {
		t.Fatal("expected rejection of after document with a missing edge endpoint")
	} else if msg := err.Error(); !strings.Contains(msg, "after-change") || !strings.Contains(msg, "ghost") {
		t.Fatalf("error %q must identify the after-change document and the ghost endpoint", msg)
	}
}

// When both documents are invalid, the before document is validated first and
// its rejection is the one reported. Document validation also precedes target
// presence checks.
func TestCompareUpstreamLineageValidatesBeforeFirst(t *testing.T) {
	cyclic := compareDoc(t, []string{"a"}, [][2]string{{"a", "a"}})
	// Target also absent from both documents; document rejection must win.
	if _, err := CompareUpstreamLineage(cyclic, cyclic, "ghost"); err == nil {
		t.Fatal("expected before-document rejection")
	} else if msg := err.Error(); !strings.HasPrefix(msg, "before-change") {
		t.Fatalf("error %q must be the before-document rejection", msg)
	}
}

// The comparison only builds throwaway graphs and never mutates or depends on
// the caller's own graph: passing documents round-trips through import with no
// shared state, and calling twice yields identical results.
func TestCompareUpstreamLineageDeterministic(t *testing.T) {
	before := compareDoc(t,
		[]string{"o", "n", "m", "t"},
		[][2]string{{"o", "m"}, {"m", "t"}})
	after := compareDoc(t,
		[]string{"o", "n", "m", "t"},
		[][2]string{{"n", "m"}, {"m", "t"}})

	first, err := CompareUpstreamLineage(before, after, "t")
	if err != nil {
		t.Fatalf("first comparison: %v", err)
	}
	second, err := CompareUpstreamLineage(before, after, "t")
	if err != nil {
		t.Fatalf("second comparison: %v", err)
	}
	if !reflect.DeepEqual(first, second) {
		t.Fatalf("comparisons differ:\n%+v\n%+v", first, second)
	}
	if got, want := depPairs(first.Added), [][2]string{{"n", "m"}}; !reflect.DeepEqual(got, want) {
		t.Errorf("Added = %v, want %v", got, want)
	}
	if got, want := depPairs(first.Removed), [][2]string{{"o", "m"}}; !reflect.DeepEqual(got, want) {
		t.Errorf("Removed = %v, want %v", got, want)
	}
}
