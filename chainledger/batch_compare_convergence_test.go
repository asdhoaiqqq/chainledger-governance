package chainledger

import (
	"reflect"
	"testing"
)

// This file pins the convergence contract between the two reports that
// classify dataset-level changes for the same old -> new transition:
//
//   - the batch report (PreviewBatch / ApplyBatch) for a plan that turns one
//     graph into another, and
//   - the snapshot comparison (CompareSnapshots) of the two versions,
//
// must agree on newDatasets, removedDatasets, and changedDatasets, because
// both are computed by the single shared rule in diffDatasets. The reports
// keep their distinct remaining meanings: the batch report's
// affectedDownstreams and the comparison's rootSourceChanges answer different
// questions and are not unified.

// applyToFreshGraph applies plan to a fresh copy of the old graph and returns
// snapshots of both versions, so the batch report and the snapshot comparison
// describe exactly the same transition.
func applyToFreshGraph(t *testing.T, oldGraph map[string]*Lineage, plan Plan) (oldSnap, newSnap *SnapshotFile, batchReport *BatchReport) {
	t.Helper()
	oldSnap, err := BuildSnapshot(oldGraph)
	if err != nil {
		t.Fatalf("BuildSnapshot old graph unexpected error: %v", err)
	}
	// Rebuild the same graph from its canonical form so the apply cannot
	// alias the snapshot's input.
	fresh, err := UnmarshalGraphFile(mustMarshalGraph(t, oldGraph))
	if err != nil {
		t.Fatalf("re-read old graph unexpected error: %v", err)
	}
	batchReport, err = ApplyBatch(fresh, plan)
	if err != nil {
		t.Fatalf("ApplyBatch unexpected error: %v", err)
	}
	newSnap, err = BuildSnapshot(fresh)
	if err != nil {
		t.Fatalf("BuildSnapshot new graph unexpected error: %v", err)
	}
	return oldSnap, newSnap, batchReport
}

func mustMarshalGraph(t *testing.T, graph map[string]*Lineage) []byte {
	t.Helper()
	data, err := MarshalGraphFile(graph)
	if err != nil {
		t.Fatalf("MarshalGraphFile unexpected error: %v", err)
	}
	return data
}

// assertSameDatasetClassification fails unless the batch report and the
// compare report list exactly the same new, removed, and changed datasets.
func assertSameDatasetClassification(t *testing.T, batch *BatchReport, compare *CompareReport) {
	t.Helper()
	if !reflect.DeepEqual(batch.NewDatasets, compare.NewDatasets) {
		t.Errorf("new datasets disagree: batch %v, compare %v", batch.NewDatasets, compare.NewDatasets)
	}
	if !reflect.DeepEqual(batch.RemovedDatasets, compare.RemovedDatasets) {
		t.Errorf("removed datasets disagree: batch %v, compare %v", batch.RemovedDatasets, compare.RemovedDatasets)
	}
	if !reflect.DeepEqual(batch.ChangedDatasets, compare.ChangedDatasets) {
		t.Errorf("changed datasets disagree: batch %v, compare %v", batch.ChangedDatasets, compare.ChangedDatasets)
	}
}

// TestBatchAndCompareAgreeOnDeleteRepoint is the headline convergence case:
// the old graph has root A with B depending on A and C depending on B; the
// batch deletes A, registers the new root D, and repoints B onto D while C is
// left alone. Both reports must list exactly new [D], removed [A], changed
// [B] — C is downstream impact, never a direct change. The reports keep their
// own extras: the batch report lists C as an affected downstream, and the
// comparison reports B and C switching root source A -> D.
func TestBatchAndCompareAgreeOnDeleteRepoint(t *testing.T) {
	oldGraph := buildGraph(t, P("A"), P("B", "A"), P("C", "B"))
	plan := Plan{
		Changes:  []PlanChange{change("D"), change("B", "D")},
		Removals: []string{"A"},
	}

	// Preview against the pristine graph, then apply to a fresh copy so the
	// comparison covers the identical transition.
	preview, err := PreviewBatch(oldGraph, plan)
	if err != nil {
		t.Fatalf("PreviewBatch unexpected error: %v", err)
	}
	oldSnap, newSnap, applied := applyToFreshGraph(t, oldGraph, plan)
	if !reflect.DeepEqual(preview, applied) {
		t.Errorf("preview and apply reports differ:\n preview=%#v\n applied=%#v", preview, applied)
	}

	compare := CompareSnapshots(oldSnap, newSnap)

	if got, want := applied.NewDatasets, []string{"D"}; !reflect.DeepEqual(got, want) {
		t.Errorf("batch NewDatasets = %v, want %v", got, want)
	}
	if got, want := applied.RemovedDatasets, []string{"A"}; !reflect.DeepEqual(got, want) {
		t.Errorf("batch RemovedDatasets = %v, want %v", got, want)
	}
	if got, want := applied.ChangedDatasets, []string{"B"}; !reflect.DeepEqual(got, want) {
		t.Errorf("batch ChangedDatasets = %v, want %v", got, want)
	}
	assertSameDatasetClassification(t, applied, compare)

	// C keeps its direct upstream B, so it is not a direct change in either
	// report; it is downstream impact in the batch report and a root-source
	// change in the comparison.
	if containsString(compare.ChangedDatasets, "C") {
		t.Errorf("compare ChangedDatasets = %v, C must not be a direct change", compare.ChangedDatasets)
	}
	if got, want := applied.AffectedDownstreams, []string{"C"}; !reflect.DeepEqual(got, want) {
		t.Errorf("batch AffectedDownstreams = %v, want %v", got, want)
	}
	wantRootChanges := []RootSourceChange{
		{Dataset: "B", OldRoots: []string{"A"}, NewRoots: []string{"D"}},
		{Dataset: "C", OldRoots: []string{"A"}, NewRoots: []string{"D"}},
	}
	if got := compare.RootSourceChanges; !reflect.DeepEqual(got, wantRootChanges) {
		t.Errorf("compare RootSourceChanges = %v, want %v", got, wantRootChanges)
	}
}

// TestBatchAndCompareAgreeOnNoOpAndBoundaryCases pins the shared rule's
// boundaries on one transition: re-declaring an existing dataset with its
// current upstreams (even shuffled and duplicated) is NOT a change; a newly
// registered dataset is new even though it arrives with upstreams; clearing a
// dataset's upstreams makes it a root and is a change, not a removal; and a
// removal that names no existing dataset is a no-op.
func TestBatchAndCompareAgreeOnNoOpAndBoundaryCases(t *testing.T) {
	// Roots A, F; B depends on A; C depends on B.
	oldGraph := buildGraph(t, P("A"), P("F"), P("B", "A"), P("C", "B"))
	plan := Plan{
		Changes: []PlanChange{
			change("B", "A", "A"), // same upstream set as before: no change
			change("E", "F"),      // registered with an upstream: new, not changed
			change("C"),           // upstreams cleared: changed, not removed
		},
		Removals: []string{"ghost"}, // does not exist: no-op, not reported
	}

	if _, err := PreviewBatch(oldGraph, plan); err != nil {
		t.Fatalf("PreviewBatch unexpected error: %v", err)
	}
	oldSnap, newSnap, applied := applyToFreshGraph(t, oldGraph, plan)
	compare := CompareSnapshots(oldSnap, newSnap)

	if got, want := applied.NewDatasets, []string{"E"}; !reflect.DeepEqual(got, want) {
		t.Errorf("batch NewDatasets = %v, want %v", got, want)
	}
	if got, want := applied.RemovedDatasets, []string{}; !reflect.DeepEqual(got, want) {
		t.Errorf("batch RemovedDatasets = %v, want %v (removing a missing name is a no-op)", got, want)
	}
	if got, want := applied.ChangedDatasets, []string{"C"}; !reflect.DeepEqual(got, want) {
		t.Errorf("batch ChangedDatasets = %v, want %v (a no-op re-declaration is not a change)", got, want)
	}
	assertSameDatasetClassification(t, applied, compare)

	// The no-op re-declaration of B must not surface anywhere as a change,
	// and B keeps root source A through its retained upstream.
	if containsString(compare.ChangedDatasets, "B") {
		t.Errorf("compare ChangedDatasets = %v, B's upstreams are unchanged", compare.ChangedDatasets)
	}
	// C became a root: it is its own source now, so the comparison reports
	// its root source changing A -> C, while the batch report counts C's
	// downstream impact only through the seed rules (C is itself a seed).
	wantRootChanges := []RootSourceChange{
		{Dataset: "C", OldRoots: []string{"A"}, NewRoots: []string{"C"}},
	}
	if got := compare.RootSourceChanges; !reflect.DeepEqual(got, wantRootChanges) {
		t.Errorf("compare RootSourceChanges = %v, want %v", got, wantRootChanges)
	}
}

// TestBatchAndCompareAgreeAcrossShuffledSemantics feeds the same transition
// through record order, upstream order, and duplicate-upstream noise and
// requires both reports to stay byte-identical to their canonical runs, so
// the shared classification cannot drift on inputs that differ only in
// spelling.
func TestBatchAndCompareAgreeAcrossShuffledSemantics(t *testing.T) {
	oldGraph := buildGraph(t, P("A"), P("B", "A"), P("C", "B"))
	plan := Plan{
		Changes:  []PlanChange{change("D"), change("B", "D", "D")},
		Removals: []string{"A"},
	}

	_, _, canonical := applyToFreshGraph(t, oldGraph, plan)
	canonicalCompare := CompareSnapshots(
		mustBuildSnapshot(t, oldGraph),
		mustBuildSnapshot(t, buildGraph(t, P("D"), P("B", "D"), P("C", "B"))),
	)

	// Same plan semantics spelled differently: change records reordered, the
	// duplicate upstream dropped.
	shuffledPlan := Plan{
		Changes:  []PlanChange{change("B", "D"), change("D")},
		Removals: []string{"A"},
	}
	_, _, shuffled := applyToFreshGraph(t, oldGraph, shuffledPlan)
	if !reflect.DeepEqual(canonical, shuffled) {
		t.Errorf("equivalent plans produced different batch reports:\n canonical=%#v\n shuffled=%#v", canonical, shuffled)
	}

	// Same final graph semantics spelled differently: record order and
	// duplicate upstreams in the rebuilt graph file.
	shuffledFinal, err := UnmarshalGraphFile([]byte(`{"datasets":[
		{"name":"C","upstreams":["B"]},
		{"name":"B","upstreams":["D","D"]},
		{"name":"D","upstreams":[]}
	]}`))
	if err != nil {
		t.Fatalf("re-read shuffled final graph unexpected error: %v", err)
	}
	shuffledCompare := CompareSnapshots(mustBuildSnapshot(t, oldGraph), mustBuildSnapshot(t, shuffledFinal))
	if !reflect.DeepEqual(canonicalCompare, shuffledCompare) {
		t.Errorf("equivalent graphs produced different compare reports:\n canonical=%#v\n shuffled=%#v", canonicalCompare, shuffledCompare)
	}
	assertSameDatasetClassification(t, shuffled, shuffledCompare)
}

func mustBuildSnapshot(t *testing.T, graph map[string]*Lineage) *SnapshotFile {
	t.Helper()
	snap, err := BuildSnapshot(graph)
	if err != nil {
		t.Fatalf("BuildSnapshot unexpected error: %v", err)
	}
	return snap
}
