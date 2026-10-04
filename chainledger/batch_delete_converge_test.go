package chainledger

import (
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
)

// buildConvergeGraph builds the shared-intermediate scenario:
//
//	R, S are roots; M depends on R; B and C both depend on M; H depends on
//	B and C (a convergence); T depends on H. U and V form a disconnected
//	island: V depends on U.
func buildConvergeGraph(t *testing.T) map[string]*Lineage {
	t.Helper()
	return buildGraph(t,
		P("R"), P("S"),
		P("M", "R"),
		P("B", "M"), P("C", "M"),
		P("H", "B", "C"),
		P("T", "H"),
		P("U"), P("V", "U"),
	)
}

// convergePlan deletes the shared intermediate M and B, repoints C at R, and
// repoints H at C and S, all in one batch.
func convergePlan() Plan {
	return Plan{
		Changes:  []PlanChange{change("C", "R"), change("H", "C", "S")},
		Removals: []string{"M", "B"},
	}
}

// TestBatchDeleteSharedIntermediateConverge is the headline regression
// scenario: a shared intermediate dataset (M) and one of its downstreams (B)
// are deleted while the retained downstreams (C, H) are repointed in the same
// batch. The converged downstream H and its dependent T survive; the
// disconnected island U<-V is untouched; the final graph contains no M or B
// and no upstream reference to them.
func TestBatchDeleteSharedIntermediateConverge(t *testing.T) {
	graph := buildConvergeGraph(t)
	before := dump(graph)
	plan := convergePlan()

	report, err := PreviewBatch(graph, plan)
	if err != nil {
		t.Fatalf("PreviewBatch unexpected error: %v", err)
	}

	// Preview is read-only: nodes and both directions of every relation keep
	// their submitted state.
	if got := dump(graph); got != before {
		t.Fatalf("preview mutated graph:\n got %s\nwant %s", got, before)
	}

	// The report distinguishes actual removals, direct upstream changes, and
	// the indirectly affected downstream.
	if want := []string{"B", "M"}; !reflect.DeepEqual(report.RemovedDatasets, want) {
		t.Errorf("RemovedDatasets = %v, want %v", report.RemovedDatasets, want)
	}
	if want := []string{"C", "H"}; !reflect.DeepEqual(report.ChangedDatasets, want) {
		t.Errorf("ChangedDatasets = %v, want %v", report.ChangedDatasets, want)
	}
	// No dataset is registered: the list must be present and empty, never
	// omitted or null.
	if report.NewDatasets == nil || len(report.NewDatasets) != 0 {
		t.Errorf("NewDatasets = %v, want empty non-nil list", report.NewDatasets)
	}
	reportJSON, err := json.Marshal(report)
	if err != nil {
		t.Fatalf("json.Marshal report unexpected error: %v", err)
	}
	if !strings.Contains(string(reportJSON), `"newDatasets":[]`) {
		t.Errorf("report JSON = %s, want newDatasets encoded as []", reportJSON)
	}

	// T is the only affected downstream, listed exactly once; H is directly
	// adjusted and must not leak into the impact list.
	if want := []string{"T"}; !reflect.DeepEqual(report.AffectedDownstreams, want) {
		t.Errorf("AffectedDownstreams = %v, want %v (T once, H excluded)", report.AffectedDownstreams, want)
	}

	// Removed relations are exactly the direct edges that disappear: R->M,
	// M->B, M->C, B->H (sorted by upstream then downstream).
	wantRemoved := []Relation{
		{Upstream: "B", Downstream: "H"},
		{Upstream: "M", Downstream: "B"},
		{Upstream: "M", Downstream: "C"},
		{Upstream: "R", Downstream: "M"},
	}
	if !reflect.DeepEqual(report.RemovedRelations, wantRemoved) {
		t.Errorf("RemovedRelations = %v, want %v", report.RemovedRelations, wantRemoved)
	}
	// Added relations are exactly the new direct edges R->C and S->H. C->H
	// and H->T survive unchanged, and reachability (R->H, S->T, ...) must
	// not be reported as a new direct relation.
	wantAdded := []Relation{
		{Upstream: "R", Downstream: "C"},
		{Upstream: "S", Downstream: "H"},
	}
	if !reflect.DeepEqual(report.AddedRelations, wantAdded) {
		t.Errorf("AddedRelations = %v, want %v", report.AddedRelations, wantAdded)
	}

	// The final graph keeps C, H, R, S, T, U, V with their new direct
	// upstreams; M and B are gone and nothing references them.
	wantFinal := []GraphDataset{
		{Name: "C", Upstreams: []string{"R"}},
		{Name: "H", Upstreams: []string{"C", "S"}},
		{Name: "R", Upstreams: []string{}},
		{Name: "S", Upstreams: []string{}},
		{Name: "T", Upstreams: []string{"H"}},
		{Name: "U", Upstreams: []string{}},
		{Name: "V", Upstreams: []string{"U"}},
	}
	if !reflect.DeepEqual(report.FinalGraph.Datasets, wantFinal) {
		t.Errorf("FinalGraph.Datasets = %+v, want %+v", report.FinalGraph.Datasets, wantFinal)
	}

	// Apply produces the same report and commits exactly the previewed final
	// graph.
	applied, err := ApplyBatch(graph, plan)
	if err != nil {
		t.Fatalf("ApplyBatch unexpected error: %v", err)
	}
	if !reflect.DeepEqual(applied, report) {
		t.Fatalf("apply report differs from preview:\n apply=%#v\n preview=%#v", applied, report)
	}
	graphJSON, err := MarshalGraphFile(graph)
	if err != nil {
		t.Fatalf("MarshalGraphFile unexpected error: %v", err)
	}
	finalJSON, err := json.MarshalIndent(report.FinalGraph, "", "  ")
	if err != nil {
		t.Fatalf("json.MarshalIndent final graph unexpected error: %v", err)
	}
	if string(graphJSON) != string(finalJSON) {
		t.Fatalf("applied graph differs from previewed final graph:\n applied %s\n previewed %s", graphJSON, finalJSON)
	}

	// Spot-check the committed graph in both directions: M and B are gone, H
	// and T survive with the new shape, and the U<-V island is untouched.
	for _, name := range []string{"M", "B"} {
		if _, ok := graph[name]; ok {
			t.Errorf("%s still in graph after apply", name)
		}
	}
	for name, entry := range graph {
		for _, parent := range entry.Parents {
			if parent == "M" || parent == "B" {
				t.Errorf("%s still references deleted upstream %s", name, parent)
			}
		}
	}
	if got := graph["C"].Parents; !reflect.DeepEqual(got, []string{"R"}) {
		t.Errorf("C.Parents = %v, want [R]", got)
	}
	if got := graph["H"].Parents; !reflect.DeepEqual(got, []string{"C", "S"}) {
		t.Errorf("H.Parents = %v, want [C S]", got)
	}
	if got := graph["H"].Children; !reflect.DeepEqual(got, []string{"T"}) {
		t.Errorf("H.Children = %v, want [T]", got)
	}
	if got := graph["T"].Parents; !reflect.DeepEqual(got, []string{"H"}) {
		t.Errorf("T.Parents = %v, want [H]", got)
	}
	if got := graph["U"].Children; !reflect.DeepEqual(got, []string{"V"}) {
		t.Errorf("U.Children = %v, want [V] (island must be untouched)", got)
	}
	if got := graph["V"].Parents; !reflect.DeepEqual(got, []string{"U"}) {
		t.Errorf("V.Parents = %v, want [U] (island must be untouched)", got)
	}
}

// TestBatchDeleteSharedIntermediateMissingRepointRejects: the plan deletes M
// and B and legally repoints C, but forgets to adjust H, which still
// references the deleted B. Preview and apply must both reject the whole
// batch naming H and B, return no success report, and leave every dataset,
// direct upstream, and derived downstream exactly as submitted — C's legal
// repoint must not take effect either.
func TestBatchDeleteSharedIntermediateMissingRepointRejects(t *testing.T) {
	plan := Plan{
		Changes:  []PlanChange{change("C", "R")},
		Removals: []string{"M", "B"},
	}

	t.Run("preview", func(t *testing.T) {
		graph := buildConvergeGraph(t)
		before := dump(graph)
		report, err := PreviewBatch(graph, plan)
		if !errors.Is(err, ErrNotFound) {
			t.Fatalf("err = %v, want ErrNotFound", err)
		}
		if report != nil {
			t.Errorf("rejected preview returned a success report: %+v", report)
		}
		if !strings.Contains(err.Error(), "H") || !strings.Contains(err.Error(), "B") {
			t.Errorf("error %q must name referrer H and deleted B", err)
		}
		if got := dump(graph); got != before {
			t.Fatalf("preview mutated graph on rejection:\n got %s\nwant %s", got, before)
		}
	})

	t.Run("apply", func(t *testing.T) {
		graph := buildConvergeGraph(t)
		before := dump(graph)
		report, err := ApplyBatch(graph, plan)
		if !errors.Is(err, ErrNotFound) {
			t.Fatalf("err = %v, want ErrNotFound", err)
		}
		if report != nil {
			t.Errorf("rejected apply returned a success report: %+v", report)
		}
		if !strings.Contains(err.Error(), "H") || !strings.Contains(err.Error(), "B") {
			t.Errorf("error %q must name referrer H and deleted B", err)
		}
		// The whole batch is atomic: even C's legal repoint must not be
		// visible, and M, B, and every parent/child link stay as submitted.
		if got := dump(graph); got != before {
			t.Fatalf("apply mutated graph on rejection:\n got %s\nwant %s", got, before)
		}
		if got := graph["C"].Parents; !reflect.DeepEqual(got, []string{"M"}) {
			t.Errorf("C.Parents = %v, want [M] (legal repoint must not take effect)", got)
		}
		if got := graph["H"].Parents; !reflect.DeepEqual(got, []string{"B", "C"}) {
			t.Errorf("H.Parents = %v, want [B C]", got)
		}
		if got := graph["M"].Children; !reflect.DeepEqual(got, []string{"B", "C"}) {
			t.Errorf("M.Children = %v, want [B C]", got)
		}
		if got := graph["B"].Children; !reflect.DeepEqual(got, []string{"H"}) {
			t.Errorf("B.Children = %v, want [H]", got)
		}
	})
}

// TestBatchDeleteSharedIntermediateDeterministic: permuting graph records,
// change records, removal order, and upstream order — or listing one of H's
// upstreams twice — changes neither the result semantics nor the output
// bytes; the existing name and relation ordering rules still govern.
func TestBatchDeleteSharedIntermediateDeterministic(t *testing.T) {
	plan1 := convergePlan()
	plan2 := Plan{
		Changes:  []PlanChange{change("H", "S", "C", "S"), change("C", "R")},
		Removals: []string{"B", "M"},
	}

	build2 := func() map[string]*Lineage {
		// Same graph, different record and upstream order (upstreams still
		// registered before their dependents, as Register requires).
		return buildGraph(t,
			P("U"), P("V", "U"),
			P("S"), P("R"),
			P("M", "R"),
			P("C", "M"), P("B", "M"),
			P("H", "C", "B"),
			P("T", "H"),
		)
	}

	g1 := buildConvergeGraph(t)
	r1, err := ApplyBatch(g1, plan1)
	if err != nil {
		t.Fatalf("ApplyBatch plan1 unexpected error: %v", err)
	}
	g2 := build2()
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
