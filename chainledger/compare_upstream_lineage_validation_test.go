package chainledger

import (
	"reflect"
	"strings"
	"testing"
)

// A target missing from both valid documents is reported against the
// before-change document: presence is checked before first, after second, and
// the whole comparison fails with a zero-value result — no partial lists.
func TestCompareUpstreamLineageTargetMissingFromBothDocuments(t *testing.T) {
	before := compareDoc(t, []string{"s", "m"}, [][2]string{{"s", "m"}})
	after := compareDoc(t, []string{"s", "n"}, [][2]string{{"s", "n"}})

	diff, err := CompareUpstreamLineage(before, after, "orders_daily")
	if err == nil {
		t.Fatal("expected error when target is missing from both documents")
	}
	msg := err.Error()
	if !strings.Contains(msg, "orders_daily") {
		t.Errorf("error %q must name the target's full name", msg)
	}
	if !strings.Contains(msg, "before-change") {
		t.Errorf("error %q must identify the before-change document", msg)
	}
	if strings.Contains(msg, "after-change") {
		t.Errorf("error %q must not blame the after-change document", msg)
	}
	if diff.Added != nil || diff.Removed != nil {
		t.Errorf("failed comparison must return the zero result, got %+v", diff)
	}
}

// The documents around a missing target may be any valid lineage: other
// registered datasets with their dependencies, or an empty lineage whose
// nodes and edges are both empty arrays. An empty lineage is a valid document,
// so the failure must be the target-not-registered one, not a document
// rejection.
func TestCompareUpstreamLineageTargetMissingWithValidSurroundings(t *testing.T) {
	emptyLineage := `{"nodes":[],"edges":[]}`
	withT := compareDoc(t,
		[]string{"s", "m", "orders_daily"},
		[][2]string{{"s", "m"}, {"m", "orders_daily"}})

	diff, err := CompareUpstreamLineage(emptyLineage, withT, "orders_daily")
	if err == nil {
		t.Fatal("expected error when the before document is an empty lineage")
	} else if msg := err.Error(); !strings.Contains(msg, "orders_daily") || !strings.Contains(msg, "before-change") {
		t.Fatalf("error %q must be the target-not-registered failure naming the before-change document", msg)
	} else if diff.Added != nil || diff.Removed != nil {
		t.Fatalf("failed comparison must return the zero result, got %+v", diff)
	}

	diff, err = CompareUpstreamLineage(withT, emptyLineage, "orders_daily")
	if err == nil {
		t.Fatal("expected error when the after document is an empty lineage")
	} else if msg := err.Error(); !strings.Contains(msg, "orders_daily") || !strings.Contains(msg, "after-change") {
		t.Fatalf("error %q must be the target-not-registered failure naming the after-change document", msg)
	} else if diff.Added != nil || diff.Removed != nil {
		t.Fatalf("failed comparison must return the zero result, got %+v", diff)
	}
}

// A document with a missing, null or wrongly typed array is invalid, not
// "valid but without the target": the failure must be the document rejection,
// never the target-not-registered error, even though the target appears
// nowhere in such a document.
func TestCompareUpstreamLineageMalformedArraysAreNotUnregisteredTarget(t *testing.T) {
	// A valid document registering only the target, with an explicit empty
	// edges array — compareDoc would marshal a nil edges slice as null.
	withT := `{"nodes":["orders_daily"],"edges":[]}`

	cases := []struct {
		name string
		doc  string
	}{
		{"null nodes", `{"nodes":null,"edges":[]}`},
		{"null edges", `{"nodes":[],"edges":null}`},
		{"missing edges", `{"nodes":["orders_daily"]}`},
		{"missing nodes", `{"edges":[]}`},
		{"nodes not an array", `{"nodes":"orders_daily","edges":[]}`},
		{"edges not an array", `{"nodes":[],"edges":{}}`},
	}
	for _, tc := range cases {
		t.Run(tc.name+" before", func(t *testing.T) {
			diff, err := CompareUpstreamLineage(tc.doc, withT, "orders_daily")
			if err == nil {
				t.Fatal("expected document rejection")
			}
			msg := err.Error()
			if !strings.HasPrefix(msg, "before-change") {
				t.Errorf("error %q must be the before-change document rejection", msg)
			}
			if strings.Contains(msg, "not found") {
				t.Errorf("error %q must not be the target-not-registered failure", msg)
			}
			if diff.Added != nil || diff.Removed != nil {
				t.Errorf("failed comparison must return the zero result, got %+v", diff)
			}
		})
		t.Run(tc.name+" after", func(t *testing.T) {
			diff, err := CompareUpstreamLineage(withT, tc.doc, "orders_daily")
			if err == nil {
				t.Fatal("expected document rejection")
			}
			msg := err.Error()
			if !strings.HasPrefix(msg, "after-change") {
				t.Errorf("error %q must be the after-change document rejection", msg)
			}
			if strings.Contains(msg, "not found") {
				t.Errorf("error %q must not be the target-not-registered failure", msg)
			}
			if diff.Added != nil || diff.Removed != nil {
				t.Errorf("failed comparison must return the zero result, got %+v", diff)
			}
		})
	}
}

// Document validation covers the whole lineage, not just the target's
// upstream: a cycle among datasets the target never reaches still rejects the
// document, and document rejection is reported before target presence. A
// before document that is valid but lacks the target, compared against an
// after document carrying an unrelated dependency cycle, fails on the
// after-change cycle — not on the before document missing the target.
// Swapping the documents flips the reported side.
func TestCompareUpstreamLineageValidatesWholeDocumentBeforeTargetPresence(t *testing.T) {
	// Valid, contains no "orders_daily".
	withoutT := compareDoc(t,
		[]string{"s", "m"},
		[][2]string{{"s", "m"}})
	// The cycle loop_a -> loop_b -> loop_a is unrelated to anything the target
	// could reach; the target is absent here too.
	cyclic := compareDoc(t,
		[]string{"s", "m", "loop_a", "loop_b"},
		[][2]string{{"s", "m"}, {"loop_a", "loop_b"}, {"loop_b", "loop_a"}})

	diff, err := CompareUpstreamLineage(withoutT, cyclic, "orders_daily")
	if err == nil {
		t.Fatal("expected the cyclic after document to be rejected")
	}
	msg := err.Error()
	if !strings.HasPrefix(msg, "after-change") {
		t.Errorf("error %q must identify the after-change document", msg)
	}
	if !strings.Contains(msg, "cycle") {
		t.Errorf("error %q must keep the cycle reason", msg)
	}
	if !strings.Contains(msg, "loop_a") || !strings.Contains(msg, "loop_b") {
		t.Errorf("error %q must name the datasets on the cycle", msg)
	}
	if strings.Contains(msg, "not found") {
		t.Errorf("error %q must not be the target-not-registered failure", msg)
	}
	if diff.Added != nil || diff.Removed != nil {
		t.Errorf("failed comparison must return the zero result, got %+v", diff)
	}

	diff, err = CompareUpstreamLineage(cyclic, withoutT, "orders_daily")
	if err == nil {
		t.Fatal("expected the cyclic before document to be rejected")
	}
	msg = err.Error()
	if !strings.HasPrefix(msg, "before-change") {
		t.Errorf("error %q must identify the before-change document", msg)
	}
	if !strings.Contains(msg, "cycle") {
		t.Errorf("error %q must keep the cycle reason", msg)
	}
	if !strings.Contains(msg, "loop_a") || !strings.Contains(msg, "loop_b") {
		t.Errorf("error %q must name the datasets on the cycle", msg)
	}
	if diff.Added != nil || diff.Removed != nil {
		t.Errorf("failed comparison must return the zero result, got %+v", diff)
	}
}

// When each document has its own document-level problem, the before document
// is validated first and its rejection is the one reported.
func TestCompareUpstreamLineageBothDocumentsInvalidReportsBefore(t *testing.T) {
	dangling := compareDoc(t,
		[]string{"orders_daily"},
		[][2]string{{"ghost", "orders_daily"}})
	cyclic := compareDoc(t,
		[]string{"loop_a", "loop_b"},
		[][2]string{{"loop_a", "loop_b"}, {"loop_b", "loop_a"}})

	diff, err := CompareUpstreamLineage(dangling, cyclic, "orders_daily")
	if err == nil {
		t.Fatal("expected the before document's problem to be reported")
	}
	msg := err.Error()
	if !strings.HasPrefix(msg, "before-change") {
		t.Errorf("error %q must be the before-change document rejection", msg)
	}
	if !strings.Contains(msg, "ghost") {
		t.Errorf("error %q must keep the before document's own reason", msg)
	}
	if strings.Contains(msg, "cycle") {
		t.Errorf("error %q must not report the after document's cycle", msg)
	}
	if diff.Added != nil || diff.Removed != nil {
		t.Errorf("failed comparison must return the zero result, got %+v", diff)
	}
}

// A target registered in both valid documents but deriving from nothing
// compares successfully: Added and Removed are non-nil empty lists. Legal
// changes to the target's downstream or to independent lineage between the
// two documents must not turn that success into a target-missing failure.
func TestCompareUpstreamLineageRegisteredTargetWithoutUpstreams(t *testing.T) {
	before := compareDoc(t,
		[]string{"orders_daily", "x", "y"},
		[][2]string{{"x", "y"}})
	// The target gains a downstream and the independent x/y chain is rewired;
	// neither touches the target's (empty) upstream scope.
	after := compareDoc(t,
		[]string{"orders_daily", "report", "x", "y", "z"},
		[][2]string{{"orders_daily", "report"}, {"x", "z"}, {"z", "y"}})

	diff, err := CompareUpstreamLineage(before, after, "orders_daily")
	if err != nil {
		t.Fatalf("CompareUpstreamLineage: %v", err)
	}
	if diff.Added == nil || diff.Removed == nil {
		t.Fatalf("successful comparison must return non-nil lists, got %+v", diff)
	}
	if len(diff.Added) != 0 || len(diff.Removed) != 0 {
		t.Fatalf("target without upstreams must see no change, got added=%v removed=%v",
			depPairs(diff.Added), depPairs(diff.Removed))
	}
}

// A successful comparison leaves both lists non-nil and empty even when the
// target's only change is downstream; and the result is the zero value only
// on failure. Pin the success/failure result shapes side by side.
func TestCompareUpstreamLineageResultShapeSuccessVsFailure(t *testing.T) {
	before := compareDoc(t,
		[]string{"s", "orders_daily"},
		[][2]string{{"s", "orders_daily"}})
	after := compareDoc(t,
		[]string{"s", "orders_daily", "dashboard"},
		[][2]string{{"s", "orders_daily"}, {"orders_daily", "dashboard"}})

	ok, err := CompareUpstreamLineage(before, after, "orders_daily")
	if err != nil {
		t.Fatalf("CompareUpstreamLineage: %v", err)
	}
	if ok.Added == nil || ok.Removed == nil || len(ok.Added) != 0 || len(ok.Removed) != 0 {
		t.Fatalf("unchanged upstream scope must succeed with non-nil empty lists, got %+v", ok)
	}

	bad, err := CompareUpstreamLineage(before, `{"nodes":[],"edges":[]}`, "orders_daily")
	if err == nil {
		t.Fatal("expected target-not-registered failure")
	}
	if !reflect.DeepEqual(bad, UpstreamLineageDiff{}) {
		t.Fatalf("failed comparison must return the zero UpstreamLineageDiff, got %+v", bad)
	}
}
