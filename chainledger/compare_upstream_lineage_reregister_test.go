package chainledger

import (
	"reflect"
	"strings"
	"testing"
)

// These tests guard the upstream-lineage comparison across an in-place
// replacement of one intermediate dataset's direct upstreams: the lineage is
// registered through the Go library, the target's complete upstream document
// is saved with ExportUpstreamLineage before and after the change, and
// CompareUpstreamLineage reports the added and removed dependencies between
// the two saved documents.

// compareReregisterScenario builds the shared-source lineage: oldroot derives
// the old source old, and old feeds TWO intermediate datasets m1 and m2 that
// both feed the target t, so old reaches t through two branches. The new
// source new is already registered with its own ancestor branch — the diamond
// newroot -> newmid -> new plus the direct newroot -> new — disjoint from
// old's branch. side is derived from old but cannot reach t, down is t's
// downstream, and lone stands alone; none of them ever takes part in t's
// derivation.
func compareReregisterScenario(t *testing.T) map[string]*Lineage {
	t.Helper()
	return buildRegisteredGraph(t, [][]string{
		{"newroot"},
		{"newmid", "newroot"},
		{"new", "newroot", "newmid"},
		{"oldroot"},
		{"old", "oldroot"},
		{"m1", "old"},
		{"m2", "old"},
		{"t", "m1", "m2"},
		{"down", "t"},
		{"side", "old"},
		{"lone"},
	})
}

// assertCompareDiff compares the two saved documents for target and requires
// exactly the given added and removed dependencies, in result order. Both
// lists must be non-nil even when the other holds entries.
func assertCompareDiff(t *testing.T, before, after, target string, wantAdded, wantRemoved [][2]string) {
	t.Helper()
	diff, err := CompareUpstreamLineage(before, after, target)
	if err != nil {
		t.Fatalf("CompareUpstreamLineage: %v", err)
	}
	if diff.Added == nil || diff.Removed == nil {
		t.Fatalf("comparison lists must be non-nil, got %+v", diff)
	}
	if got := depPairs(diff.Added); !reflect.DeepEqual(got, wantAdded) {
		t.Errorf("Added = %v, want %v", got, wantAdded)
	}
	if got := depPairs(diff.Removed); !reflect.DeepEqual(got, wantRemoved) {
		t.Errorf("Removed = %v, want %v", got, wantRemoved)
	}
}

// Replacing m1's direct upstream old with the already-registered new source
// while old still reaches t through m2: the comparison must list new -> m1
// and every ancestor dependency the new source brings into t's scope
// (newmid -> new, newroot -> new, newroot -> newmid — the converging diamond
// reported once per dependency, ordered by From then To), and only old -> m1
// as removed. old's own ancestor edge oldroot -> old and the retained branch
// edge old -> m2 stay in scope through m2 and must not be misreported as
// removed; the unchanged m1 -> t and m2 -> t appear in neither list.
func TestCompareUpstreamLineageReregisterKeepsSharedSource(t *testing.T) {
	graph := compareReregisterScenario(t)
	assertConsistent(t, graph)

	before := mustExport(t, graph, "t")

	// Swap only m1's direct upstream; t's own registration is untouched.
	mustRegister(t, graph, "m1", "new")
	assertConsistent(t, graph)
	assertEntry(t, graph, "m1", []string{"new"}, []string{"t"})
	assertEntry(t, graph, "t", []string{"m1", "m2"}, []string{"down"})
	// old lost only the m1 reverse edge; it still derives m2 and side.
	assertEntry(t, graph, "old", []string{"oldroot"}, []string{"m2", "side"})

	after := mustExport(t, graph, "t")

	wantAdded := [][2]string{
		{"new", "m1"},
		{"newmid", "new"},
		{"newroot", "new"},
		{"newroot", "newmid"},
	}
	wantRemoved := [][2]string{{"old", "m1"}}
	assertCompareDiff(t, before, after, "t", wantAdded, wantRemoved)

	// The dependencies that must NOT surface as removed: old still
	// participates in t's derivation through the m2 branch, so its ancestor
	// edge and the retained branch edge stay common to both scopes.
	diff, err := CompareUpstreamLineage(before, after, "t")
	if err != nil {
		t.Fatalf("CompareUpstreamLineage: %v", err)
	}
	for _, kept := range [][2]string{{"oldroot", "old"}, {"old", "m2"}, {"m1", "t"}, {"m2", "t"}} {
		for _, d := range diff.Removed {
			if d.From == kept[0] && d.To == kept[1] {
				t.Errorf("%s -> %s is still in scope through the m2 branch and must not be removed", kept[0], kept[1])
			}
		}
	}
}

// When the old source participates in t's derivation ONLY through the
// replaced intermediate, the same replacement detaches its whole ancestor
// segment: old -> m1, old -> oldmid, oldmid -> m1, oldroot -> old and
// oldroot -> oldmid are all listed as removed — each exactly once even though
// oldmid is reachable from oldroot two ways — while those registrations and
// edges remain in the graph. The unrelated keep -> t and the unchanged
// m1 -> t stay common and appear in neither list.
func TestCompareUpstreamLineageReregisterDetachesWholeSegment(t *testing.T) {
	graph := buildRegisteredGraph(t, [][]string{
		{"oldroot"},
		{"old", "oldroot"},
		{"oldmid", "oldroot", "old"}, // converging branches inside the old segment
		{"m1", "old", "oldmid"},
		{"keep"},
		{"newroot"},
		{"new", "newroot"},
		{"t", "m1", "keep"},
	})
	assertConsistent(t, graph)

	before := mustExport(t, graph, "t")

	mustRegister(t, graph, "m1", "new")
	assertConsistent(t, graph)

	after := mustExport(t, graph, "t")

	wantAdded := [][2]string{
		{"new", "m1"},
		{"newroot", "new"},
	}
	wantRemoved := [][2]string{
		{"old", "m1"},
		{"old", "oldmid"},
		{"oldmid", "m1"},
		{"oldroot", "old"},
		{"oldroot", "oldmid"},
	}
	assertCompareDiff(t, before, after, "t", wantAdded, wantRemoved)

	// The detached segment keeps its registrations and its internal edges in
	// the graph; it only left t's upstream scope.
	assertEntry(t, graph, "old", []string{"oldroot"}, []string{"oldmid"})
	assertEntry(t, graph, "oldmid", []string{"oldroot", "old"}, nil)
	assertEntry(t, graph, "oldroot", nil, []string{"old", "oldmid"})
	assertConsistent(t, graph)
}

// A replacement request that mixes a legal new source with a parent that
// would close a cycle — the target t itself, which is downstream of m1 — is
// rejected with an error naming the cycle through t, and the whole graph is
// left exactly as it was: no partial application of the legal parent. The
// target's lineage document saved afterwards is identical to the pre-request
// one, and comparing the two succeeds with two non-nil empty lists.
func TestCompareUpstreamLineageRejectedReregisterComparesNoChange(t *testing.T) {
	graph := compareReregisterScenario(t)
	assertConsistent(t, graph)

	exportBefore := mustExport(t, graph, "t")
	snapshot := snapshotExportGraph(graph)

	err := Register(graph, Dataset{Name: "m1"}, []string{"new", "t"})
	if err == nil {
		t.Fatal("expected cycle rejection when the replacement includes the target")
	}
	if msg := err.Error(); !strings.Contains(msg, "cycle") || !strings.Contains(msg, "t") {
		t.Fatalf("error %q must state the cycle through the target t", msg)
	}

	// No partial effect: m1 never gained new nor lost old, and every node,
	// edge and list order is exactly as before the request.
	assertGraphUnchanged(t, snapshot, graph)
	assertEntry(t, graph, "m1", []string{"old"}, []string{"t"})
	assertEntry(t, graph, "new", []string{"newroot", "newmid"}, nil)
	assertEntry(t, graph, "old", []string{"oldroot"}, []string{"m1", "m2", "side"})
	assertConsistent(t, graph)

	exportAfter := mustExport(t, graph, "t")
	if exportAfter != exportBefore {
		t.Errorf("export changed after rejected re-register:\nbefore: %s\nafter:  %s",
			exportBefore, exportAfter)
	}

	diff, err := CompareUpstreamLineage(exportBefore, exportAfter, "t")
	assertCompareNoChange(t, diff, err)
}
