package chainledger

import (
	"reflect"
	"testing"
)

// This file pins the contract that the batch report (preview/apply) and the
// snapshot comparison answer the question "which datasets are new, removed,
// or directly changed between the old graph and the new graph" with one and
// the same rule: both are computed by diffDatasetNodes, so a batch and the
// comparison of the snapshots taken around it must agree on all three lists.
//
// The two reports keep their distinct remaining meanings: only the batch
// report lists affected downstreams, and only the comparison lists root
// source changes. Unifying the direct-change classification must not move a
// dataset between those lists.

// consistencyScenario builds the headline transition: A is a root, B depends
// directly on A, C depends directly on B. The batch deletes A, registers the
// new root D, and repoints B onto D; C is not mentioned by the plan.
func consistencyScenario(t *testing.T) (map[string]*Lineage, Plan) {
	t.Helper()
	graph := buildGraph(t, P("A"), P("B", "A"), P("C", "B"))
	plan := Plan{
		Changes:  []PlanChange{change("D"), change("B", "D")},
		Removals: []string{"A"},
	}
	return graph, plan
}

// TestBatchAndCompareAgreeOnNodeClassification runs the headline transition
// through apply and through a snapshot comparison of the graphs before and
// after, and requires both reports to classify the nodes identically: new D,
// removed A, changed B — and never C, whose direct upstreams are untouched.
func TestBatchAndCompareAgreeOnNodeClassification(t *testing.T) {
	graph, plan := consistencyScenario(t)

	before, err := BuildSnapshot(graph)
	if err != nil {
		t.Fatalf("BuildSnapshot before: %v", err)
	}
	batch, err := ApplyBatch(graph, plan)
	if err != nil {
		t.Fatalf("ApplyBatch unexpected error: %v", err)
	}
	after, err := BuildSnapshot(graph)
	if err != nil {
		t.Fatalf("BuildSnapshot after: %v", err)
	}
	compare := CompareSnapshots(before, after)

	if got, want := batch.NewDatasets, []string{"D"}; !reflect.DeepEqual(got, want) {
		t.Errorf("batch NewDatasets = %v, want %v", got, want)
	}
	if got, want := batch.RemovedDatasets, []string{"A"}; !reflect.DeepEqual(got, want) {
		t.Errorf("batch RemovedDatasets = %v, want %v", got, want)
	}
	if got, want := batch.ChangedDatasets, []string{"B"}; !reflect.DeepEqual(got, want) {
		t.Errorf("batch ChangedDatasets = %v, want %v", got, want)
	}

	// The comparison of the two versions must reach the same three lists.
	if !reflect.DeepEqual(compare.NewDatasets, batch.NewDatasets) {
		t.Errorf("compare NewDatasets = %v, batch reported %v", compare.NewDatasets, batch.NewDatasets)
	}
	if !reflect.DeepEqual(compare.RemovedDatasets, batch.RemovedDatasets) {
		t.Errorf("compare RemovedDatasets = %v, batch reported %v", compare.RemovedDatasets, batch.RemovedDatasets)
	}
	if !reflect.DeepEqual(compare.ChangedDatasets, batch.ChangedDatasets) {
		t.Errorf("compare ChangedDatasets = %v, batch reported %v", compare.ChangedDatasets, batch.ChangedDatasets)
	}

	// C keeps its direct upstream B, so it is not a direct change in either
	// report — but the batch still lists it as an affected downstream, and the
	// comparison still reports its root source moving A -> D alongside B's.
	if got, want := batch.AffectedDownstreams, []string{"C"}; !reflect.DeepEqual(got, want) {
		t.Errorf("batch AffectedDownstreams = %v, want %v", got, want)
	}
	wantRootChanges := []RootSourceChange{
		{Dataset: "B", OldRoots: []string{"A"}, NewRoots: []string{"D"}},
		{Dataset: "C", OldRoots: []string{"A"}, NewRoots: []string{"D"}},
	}
	if got := compare.RootSourceChanges; !reflect.DeepEqual(got, wantRootChanges) {
		t.Errorf("compare RootSourceChanges = %v, want %v", got, wantRootChanges)
	}
	if containsString(compare.ChangedDatasets, "C") {
		t.Errorf("C must not be a direct change in the comparison: %v", compare.ChangedDatasets)
	}
}

// TestBatchAndCompareAgreeOnNoOpAndRegistration covers the two plan shapes
// that must NOT manufacture a change: naming an existing dataset while
// restating its current upstreams (even shuffled and duplicated) is a no-op,
// and registering a new dataset that already carries upstreams is only a new
// dataset, never an upstream change. Both reports must agree on that.
func TestBatchAndCompareAgreeOnNoOpAndRegistration(t *testing.T) {
	graph := buildGraph(t, P("A"), P("B", "A"), P("C", "B"))
	plan := Plan{
		Changes: []PlanChange{
			change("B", "A", "A"), // restates the current upstreams: no change
			change("X", "B"),      // new dataset with an upstream: new only
			change("C", "B", "B"), // restates, duplicates ignored: no change
		},
	}

	before, err := BuildSnapshot(graph)
	if err != nil {
		t.Fatalf("BuildSnapshot before: %v", err)
	}
	batch, err := ApplyBatch(graph, plan)
	if err != nil {
		t.Fatalf("ApplyBatch unexpected error: %v", err)
	}
	after, err := BuildSnapshot(graph)
	if err != nil {
		t.Fatalf("BuildSnapshot after: %v", err)
	}
	compare := CompareSnapshots(before, after)

	if got, want := batch.NewDatasets, []string{"X"}; !reflect.DeepEqual(got, want) {
		t.Errorf("batch NewDatasets = %v, want %v", got, want)
	}
	if len(batch.ChangedDatasets) != 0 {
		t.Errorf("batch ChangedDatasets = %v, want [] (restated upstreams are not a change)", batch.ChangedDatasets)
	}
	if len(batch.RemovedDatasets) != 0 {
		t.Errorf("batch RemovedDatasets = %v, want []", batch.RemovedDatasets)
	}
	if !reflect.DeepEqual(compare.NewDatasets, batch.NewDatasets) {
		t.Errorf("compare NewDatasets = %v, batch reported %v", compare.NewDatasets, batch.NewDatasets)
	}
	if !reflect.DeepEqual(compare.ChangedDatasets, batch.ChangedDatasets) {
		t.Errorf("compare ChangedDatasets = %v, batch reported %v", compare.ChangedDatasets, batch.ChangedDatasets)
	}
	if !reflect.DeepEqual(compare.RemovedDatasets, batch.RemovedDatasets) {
		t.Errorf("compare RemovedDatasets = %v, batch reported %v", compare.RemovedDatasets, batch.RemovedDatasets)
	}
}

// TestBatchAndCompareAgreeOnEmptyTransition pins the empty boundary: applying
// an empty plan and comparing the two identical versions both report three
// empty (but non-nil) lists, as does comparing an empty graph with itself.
func TestBatchAndCompareAgreeOnEmptyTransition(t *testing.T) {
	graph := buildGraph(t, P("A"), P("B", "A"))

	before, err := BuildSnapshot(graph)
	if err != nil {
		t.Fatalf("BuildSnapshot before: %v", err)
	}
	batch, err := ApplyBatch(graph, Plan{})
	if err != nil {
		t.Fatalf("ApplyBatch empty plan unexpected error: %v", err)
	}
	after, err := BuildSnapshot(graph)
	if err != nil {
		t.Fatalf("BuildSnapshot after: %v", err)
	}
	compare := CompareSnapshots(before, after)

	for name, list := range map[string][]string{
		"batch NewDatasets":     batch.NewDatasets,
		"batch RemovedDatasets": batch.RemovedDatasets,
		"batch ChangedDatasets": batch.ChangedDatasets,
		"compare NewDatasets":   compare.NewDatasets,
		"compare Removed":       compare.RemovedDatasets,
		"compare Changed":       compare.ChangedDatasets,
	} {
		if list == nil || len(list) != 0 {
			t.Errorf("%s = %v, want non-nil empty list", name, list)
		}
	}

	// An empty graph compared with itself has no changes either.
	emptySnap, err := BuildSnapshot(map[string]*Lineage{})
	if err != nil {
		t.Fatalf("BuildSnapshot empty graph: %v", err)
	}
	emptyCompare := CompareSnapshots(emptySnap, emptySnap)
	if len(emptyCompare.NewDatasets)+len(emptyCompare.RemovedDatasets)+len(emptyCompare.ChangedDatasets) != 0 {
		t.Errorf("empty-vs-empty comparison = %+v, want no node changes", emptyCompare)
	}
}
