package chainledger

import (
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
)

// TestBatchDeleteRepointDownstream is the headline scenario: A<-B<-C. Delete A
// and repoint B at the newly registered D. The batch succeeds; C (unadjusted)
// is still listed as an affected downstream.
func TestBatchDeleteRepointDownstream(t *testing.T) {
	graph := buildGraph(t, P("A"), P("B", "A"), P("C", "B"))
	plan := Plan{
		Changes:  []PlanChange{change("D"), change("B", "D")},
		Removals: []string{"A"},
	}
	report, err := PreviewBatch(graph, plan)
	if err != nil {
		t.Fatalf("PreviewBatch unexpected error: %v", err)
	}
	wantRemoved := []string{"A"}
	if !reflect.DeepEqual(report.RemovedDatasets, wantRemoved) {
		t.Errorf("RemovedDatasets = %v, want %v", report.RemovedDatasets, wantRemoved)
	}
	wantRemovedRels := []Relation{{Upstream: "A", Downstream: "B"}}
	if !reflect.DeepEqual(report.RemovedRelations, wantRemovedRels) {
		t.Errorf("RemovedRelations = %v, want %v", report.RemovedRelations, wantRemovedRels)
	}
	wantAdded := []Relation{{Upstream: "D", Downstream: "B"}}
	if !reflect.DeepEqual(report.AddedRelations, wantAdded) {
		t.Errorf("AddedRelations = %v, want %v", report.AddedRelations, wantAdded)
	}
	// C is downstream of B and is not itself adjusted, so it is affected.
	wantAffected := []string{"C"}
	if !reflect.DeepEqual(report.AffectedDownstreams, wantAffected) {
		t.Errorf("AffectedDownstreams = %v, want %v", report.AffectedDownstreams, wantAffected)
	}
	// A must be gone from the final graph; B now depends on D only.
	for _, ds := range report.FinalGraph.Datasets {
		if ds.Name == "A" {
			t.Errorf("A still present in final graph: %+v", report.FinalGraph.Datasets)
		}
		if ds.Name == "B" && !reflect.DeepEqual(ds.Upstreams, []string{"D"}) {
			t.Errorf("B.Upstreams = %v, want [D]", ds.Upstreams)
		}
	}
	if _, err := ApplyBatch(graph, plan); err != nil {
		t.Fatalf("ApplyBatch unexpected error: %v", err)
	}
	if _, ok := graph["A"]; ok {
		t.Errorf("A still in graph after apply")
	}
	if got := graph["B"].Parents; !reflect.DeepEqual(got, []string{"D"}) {
		t.Errorf("B.Parents = %v, want [D]", got)
	}
}

// TestBatchDeleteOnlyRejectsRetainedReferrer: delete A only while B still
// references A. The batch must fail, naming both the referrer (B) and the
// referenced name (A); B must not silently become a root.
func TestBatchDeleteOnlyRejectsRetainedReferrer(t *testing.T) {
	graph := buildGraph(t, P("A"), P("B", "A"), P("C", "B"))
	plan := Plan{Removals: []string{"A"}}
	_, err := PreviewBatch(graph, plan)
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("err = %v, want ErrNotFound", err)
	}
	if !strings.Contains(err.Error(), "B") || !strings.Contains(err.Error(), "A") {
		t.Errorf("error %q must name referrer B and referenced A", err)
	}
	// Graph is untouched on rejection.
	if _, ok := graph["A"]; !ok {
		t.Errorf("A was removed from graph on rejection")
	}
	if got := graph["B"].Parents; !reflect.DeepEqual(got, []string{"A"}) {
		t.Errorf("B.Parents = %v, want [A] (B must not silently become root)", got)
	}
}

// TestBatchDeleteDownstreamAlsoListed: delete A and B together (B references
// A, and both are explicitly listed for deletion). The batch succeeds with no
// dangling references because no retained dataset references a deleted name.
func TestBatchDeleteDownstreamAlsoListed(t *testing.T) {
	graph := buildGraph(t, P("A"), P("B", "A"))
	plan := Plan{Removals: []string{"A", "B"}}
	report, err := PreviewBatch(graph, plan)
	if err != nil {
		t.Fatalf("PreviewBatch unexpected error: %v", err)
	}
	wantRemoved := []string{"A", "B"}
	if !reflect.DeepEqual(report.RemovedDatasets, wantRemoved) {
		t.Errorf("RemovedDatasets = %v, want %v", report.RemovedDatasets, wantRemoved)
	}
	wantRemovedRels := []Relation{{Upstream: "A", Downstream: "B"}}
	if !reflect.DeepEqual(report.RemovedRelations, wantRemovedRels) {
		t.Errorf("RemovedRelations = %v, want %v", report.RemovedRelations, wantRemovedRels)
	}
	if len(report.FinalGraph.Datasets) != 0 {
		t.Errorf("FinalGraph = %v, want empty", report.FinalGraph.Datasets)
	}
	if _, err := ApplyBatch(graph, plan); err != nil {
		t.Fatalf("ApplyBatch unexpected error: %v", err)
	}
	if len(graph) != 0 {
		t.Errorf("graph not empty after delete: %v", graph)
	}
}

// TestBatchDeleteAll: delete every dataset. The result is a legal empty graph.
func TestBatchDeleteAll(t *testing.T) {
	graph := buildGraph(t, P("A"), P("B", "A"), P("C", "B"))
	plan := Plan{Removals: []string{"A", "B", "C"}}
	report, err := PreviewBatch(graph, plan)
	if err != nil {
		t.Fatalf("PreviewBatch unexpected error: %v", err)
	}
	wantRemoved := []string{"A", "B", "C"}
	if !reflect.DeepEqual(report.RemovedDatasets, wantRemoved) {
		t.Errorf("RemovedDatasets = %v, want %v", report.RemovedDatasets, wantRemoved)
	}
	if len(report.FinalGraph.Datasets) != 0 {
		t.Errorf("FinalGraph = %v, want empty", report.FinalGraph.Datasets)
	}
	if _, err := ApplyBatch(graph, plan); err != nil {
		t.Fatalf("ApplyBatch unexpected error: %v", err)
	}
	if len(graph) != 0 {
		t.Errorf("graph not empty after delete-all: %v", graph)
	}
}

// TestBatchDeleteNonExistentIsNoOp: removing a name that does not currently
// exist is a no-op. A successful plan can be applied again with all change and
// impact lists empty.
func TestBatchDeleteNonExistentIsNoOp(t *testing.T) {
	graph := buildGraph(t, P("A"), P("B", "A"))
	plan := Plan{Removals: []string{"ghost"}}
	report, err := PreviewBatch(graph, plan)
	if err != nil {
		t.Fatalf("PreviewBatch unexpected error: %v", err)
	}
	if len(report.RemovedDatasets) != 0 {
		t.Errorf("RemovedDatasets = %v, want empty", report.RemovedDatasets)
	}
	if len(report.ChangedDatasets) != 0 || len(report.AddedRelations) != 0 ||
		len(report.RemovedRelations) != 0 || len(report.AffectedDownstreams) != 0 {
		t.Errorf("no-op removal produced non-empty diff: %+v", report)
	}
	// Apply once, then again: the second apply is a no-op with empty lists.
	if _, err := ApplyBatch(graph, plan); err != nil {
		t.Fatalf("first ApplyBatch unexpected error: %v", err)
	}
	report, err = ApplyBatch(graph, plan)
	if err != nil {
		t.Fatalf("second ApplyBatch unexpected error: %v", err)
	}
	if len(report.RemovedDatasets) != 0 || len(report.ChangedDatasets) != 0 ||
		len(report.AddedRelations) != 0 || len(report.RemovedRelations) != 0 ||
		len(report.AffectedDownstreams) != 0 {
		t.Errorf("re-apply produced non-empty diff: %+v", report)
	}
}

// TestBatchDeleteRejectsInvalidRemovals: empty names, duplicate names, and a
// name appearing in both changes and removals all reject the whole batch.
func TestBatchDeleteRejectsInvalidRemovals(t *testing.T) {
	t.Run("empty name", func(t *testing.T) {
		graph := buildGraph(t, P("A"))
		plan := Plan{Removals: []string{""}}
		_, err := PreviewBatch(graph, plan)
		if !errors.Is(err, ErrInvalidArgument) {
			t.Fatalf("err = %v, want ErrInvalidArgument", err)
		}
	})
	t.Run("duplicate name", func(t *testing.T) {
		graph := buildGraph(t, P("A"))
		plan := Plan{Removals: []string{"A", "A"}}
		_, err := PreviewBatch(graph, plan)
		if !errors.Is(err, ErrInvalidArgument) {
			t.Fatalf("err = %v, want ErrInvalidArgument", err)
		}
		if !strings.Contains(err.Error(), "A") {
			t.Errorf("error %q does not name A", err)
		}
	})
	t.Run("overlap with changes", func(t *testing.T) {
		graph := buildGraph(t, P("A"))
		plan := Plan{
			Changes:  []PlanChange{change("A")},
			Removals: []string{"A"},
		}
		_, err := PreviewBatch(graph, plan)
		if !errors.Is(err, ErrInvalidArgument) {
			t.Fatalf("err = %v, want ErrInvalidArgument", err)
		}
		if !strings.Contains(err.Error(), "A") {
			t.Errorf("error %q does not name A", err)
		}
	})
}

// TestBatchDeleteDoesNotPaperOverCorruptGraph: the original graph is corrupt
// (A<->B cycle). Even though the plan deletes A, the batch must fail because a
// problem node cannot be deleted to make a corrupt original graph pass.
func TestBatchDeleteDoesNotPaperOverCorruptGraph(t *testing.T) {
	graph := map[string]*Lineage{
		"A": {Dataset: "A", Parents: []string{"B"}},
		"B": {Dataset: "B", Parents: []string{"A"}},
	}
	plan := Plan{Removals: []string{"A"}}
	_, err := PreviewBatch(graph, plan)
	if !errors.Is(err, ErrCycle) {
		t.Fatalf("err = %v, want ErrCycle (corrupt original graph)", err)
	}
}

// TestBatchDeleteOnlyPlanWorks: a plan containing only removals (no changes)
// works when the deleted datasets have no retained referrers.
func TestBatchDeleteOnlyPlanWorks(t *testing.T) {
	// Deleting B (a leaf) from A<-B succeeds.
	graph := buildGraph(t, P("A"), P("B", "A"))
	plan := Plan{Removals: []string{"B"}}
	report, err := PreviewBatch(graph, plan)
	if err != nil {
		t.Fatalf("PreviewBatch unexpected error: %v", err)
	}
	wantRemoved := []string{"B"}
	if !reflect.DeepEqual(report.RemovedDatasets, wantRemoved) {
		t.Errorf("RemovedDatasets = %v, want %v", report.RemovedDatasets, wantRemoved)
	}
	// Deleting A (referenced by B) fails.
	graph2 := buildGraph(t, P("A"), P("B", "A"))
	plan2 := Plan{Removals: []string{"A"}}
	_, err = PreviewBatch(graph2, plan2)
	if err == nil {
		t.Fatalf("expected error deleting referenced A, got nil")
	}
}

// TestBatchDeletePreservesNames: name casing and spaces are preserved in
// removals, and the same name with different casing is a different name.
func TestBatchDeletePreservesNames(t *testing.T) {
	graph := buildGraph(t, P(" Raw-Blocks "), P("RAW-BLOCKS", " Raw-Blocks "))
	// Removing "RAW-BLOCKS" (different casing from " Raw-Blocks ") deletes
	// only that dataset; " Raw-Blocks " is retained.
	plan := Plan{Removals: []string{"RAW-BLOCKS"}}
	report, err := PreviewBatch(graph, plan)
	if err != nil {
		t.Fatalf("PreviewBatch unexpected error: %v", err)
	}
	wantRemoved := []string{"RAW-BLOCKS"}
	if !reflect.DeepEqual(report.RemovedDatasets, wantRemoved) {
		t.Errorf("RemovedDatasets = %v, want %v", report.RemovedDatasets, wantRemoved)
	}
	if len(report.FinalGraph.Datasets) != 1 || report.FinalGraph.Datasets[0].Name != " Raw-Blocks " {
		t.Errorf("FinalGraph = %v, want only [ Raw-Blocks ]", report.FinalGraph.Datasets)
	}
}

// TestBatchDeleteDeterministic: the same semantics with different record order
// produces byte-identical graph and report output.
func TestBatchDeleteDeterministic(t *testing.T) {
	build := func() map[string]*Lineage {
		return buildGraph(t, P("A"), P("B", "A"), P("C", "B"))
	}
	plan1 := Plan{
		Changes:  []PlanChange{change("D"), change("B", "D")},
		Removals: []string{"A"},
	}
	plan2 := Plan{
		Changes:  []PlanChange{change("B", "D"), change("D")},
		Removals: []string{"A"},
	}
	g1 := build()
	r1, err := ApplyBatch(g1, plan1)
	if err != nil {
		t.Fatalf("ApplyBatch plan1 unexpected error: %v", err)
	}
	g2 := build()
	r2, err := ApplyBatch(g2, plan2)
	if err != nil {
		t.Fatalf("ApplyBatch plan2 unexpected error: %v", err)
	}
	b1, err := MarshalGraphFile(g1)
	if err != nil {
		t.Fatalf("MarshalGraphFile g1 unexpected error: %v", err)
	}
	b2, err := MarshalGraphFile(g2)
	if err != nil {
		t.Fatalf("MarshalGraphFile g2 unexpected error: %v", err)
	}
	if string(b1) != string(b2) {
		t.Fatalf("graph bytes differ:\n--- plan1 ---\n%s\n--- plan2 ---\n%s", b1, b2)
	}
	rb1, err := json.Marshal(r1)
	if err != nil {
		t.Fatalf("json.Marshal r1 unexpected error: %v", err)
	}
	rb2, err := json.Marshal(r2)
	if err != nil {
		t.Fatalf("json.Marshal r2 unexpected error: %v", err)
	}
	if string(rb1) != string(rb2) {
		t.Fatalf("report bytes differ:\n--- plan1 ---\n%s\n--- plan2 ---\n%s", rb1, rb2)
	}
}

// TestBatchDeletePreviewApplyConsistency: preview and apply produce the same
// report for the same graph and plan with removals.
func TestBatchDeletePreviewApplyConsistency(t *testing.T) {
	graph := buildGraph(t, P("A"), P("B", "A"), P("C", "B"))
	plan := Plan{
		Changes:  []PlanChange{change("D"), change("B", "D")},
		Removals: []string{"A"},
	}
	preview, err := PreviewBatch(graph, plan)
	if err != nil {
		t.Fatalf("PreviewBatch unexpected error: %v", err)
	}
	applied, err := ApplyBatch(graph, plan)
	if err != nil {
		t.Fatalf("ApplyBatch unexpected error: %v", err)
	}
	if !reflect.DeepEqual(preview, applied) {
		t.Fatalf("preview and apply reports differ:\n preview=%#v\n applied=%#v", preview, applied)
	}
}

// TestBatchDeleteEmptyPlanAndRemovals: an empty plan and an empty removals
// array both succeed with empty lists.
func TestBatchDeleteEmptyPlanAndRemovals(t *testing.T) {
	graph := buildGraph(t, P("A"), P("B", "A"))
	for _, plan := range []Plan{
		{},
		{Removals: []string{}},
		{Changes: []PlanChange{}, Removals: []string{}},
	} {
		report, err := PreviewBatch(graph, plan)
		if err != nil {
			t.Fatalf("PreviewBatch %+v unexpected error: %v", plan, err)
		}
		if len(report.RemovedDatasets) != 0 || len(report.NewDatasets) != 0 ||
			len(report.ChangedDatasets) != 0 || len(report.AddedRelations) != 0 ||
			len(report.RemovedRelations) != 0 || len(report.AffectedDownstreams) != 0 {
			t.Errorf("plan %+v produced non-empty diff: %+v", plan, report)
		}
	}
}
