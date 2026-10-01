package chainledger

import (
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
)

// buildGraph constructs a graph from name -> parents pairs, asserting success.
func buildGraph(t *testing.T, pairs ...struct {
	name    string
	parents []string
}) map[string]*Lineage {
	t.Helper()
	graph := map[string]*Lineage{}
	for _, p := range pairs {
		if err := Register(graph, Dataset{Name: p.name}, p.parents); err != nil {
			t.Fatalf("buildGraph: Register(%q, %v) unexpected error: %v", p.name, p.parents, err)
		}
	}
	return graph
}

// P is a shorthand for building name/parents pairs in test tables.
func P(name string, parents ...string) struct {
	name    string
	parents []string
} {
	return struct {
		name    string
		parents []string
	}{name, parents}
}

func planOf(changes ...PlanChange) Plan {
	return Plan{Changes: changes}
}

func change(name string, upstreams ...string) PlanChange {
	return PlanChange{Name: name, Upstreams: upstreams}
}

// TestBatchMutualSwapSucceeds is the headline scenario: originally B depends
// on A. Submitting "A depends on B, B becomes root" must succeed because the
// legality is judged on the graph AFTER all replacements.
func TestBatchMutualSwapSucceeds(t *testing.T) {
	graph := buildGraph(t, P("A"), P("B", "A"))
	plan := planOf(change("A", "B"), change("B"))

	report, err := PreviewBatch(graph, plan)
	if err != nil {
		t.Fatalf("PreviewBatch unexpected error: %v", err)
	}
	if got := graph["A"].Parents; len(got) != 0 {
		t.Fatalf("preview mutated graph: A.Parents = %v", got)
	}

	wantChanged := []string{"A", "B"}
	if !reflect.DeepEqual(report.ChangedDatasets, wantChanged) {
		t.Errorf("ChangedDatasets = %v, want %v", report.ChangedDatasets, wantChanged)
	}
	if len(report.NewDatasets) != 0 {
		t.Errorf("NewDatasets = %v, want empty", report.NewDatasets)
	}
	wantAdded := []Relation{{Upstream: "B", Downstream: "A"}}
	wantRemoved := []Relation{{Upstream: "A", Downstream: "B"}}
	if !reflect.DeepEqual(report.AddedRelations, wantAdded) {
		t.Errorf("AddedRelations = %v, want %v", report.AddedRelations, wantAdded)
	}
	if !reflect.DeepEqual(report.RemovedRelations, wantRemoved) {
		t.Errorf("RemovedRelations = %v, want %v", report.RemovedRelations, wantRemoved)
	}
	// No downstream is affected: the swap only repoints A and B, and neither
	// has other dependents.
	if len(report.AffectedDownstreams) != 0 {
		t.Errorf("AffectedDownstreams = %v, want empty", report.AffectedDownstreams)
	}

	// Apply and verify the final shape: A depends on B, B is a root.
	applied, err := ApplyBatch(graph, plan)
	if err != nil {
		t.Fatalf("ApplyBatch unexpected error: %v", err)
	}
	if !reflect.DeepEqual(applied, report) {
		t.Errorf("apply report differs from preview:\n apply=%#v\n preview=%#v", applied, report)
	}
	if got := graph["A"].Parents; !reflect.DeepEqual(got, []string{"B"}) {
		t.Errorf("A.Parents = %v, want [B]", got)
	}
	if got := graph["B"].Parents; len(got) != 0 {
		t.Errorf("B.Parents = %v, want empty (root)", got)
	}
	if got := graph["B"].Children; !reflect.DeepEqual(got, []string{"A"}) {
		t.Errorf("B.Children = %v, want [A]", got)
	}
	if got := graph["A"].Children; len(got) != 0 {
		t.Errorf("A.Children = %v, want empty", got)
	}
	if roots := Roots(graph); !reflect.DeepEqual(roots, []string{"B"}) {
		t.Errorf("Roots = %v, want [B]", roots)
	}
}

// TestBatchRejectsCyclesAfterReplacement verifies that a final graph with a
// self-dependency or an indirect cycle is rejected, and that the rejection
// names the datasets involved.
func TestBatchRejectsCyclesAfterReplacement(t *testing.T) {
	t.Run("self dependency", func(t *testing.T) {
		graph := buildGraph(t, P("A"))
		plan := planOf(change("A", "A"))
		_, err := PreviewBatch(graph, plan)
		if !errors.Is(err, ErrCycle) {
			t.Fatalf("err = %v, want ErrCycle", err)
		}
		if !strings.Contains(err.Error(), "A") {
			t.Errorf("error %q does not name dataset A", err)
		}
	})

	t.Run("indirect cycle through swap", func(t *testing.T) {
		// A<-B. Plan: A depends on B, B depends on A. Final: A<->B, a cycle.
		graph := buildGraph(t, P("A"), P("B", "A"))
		plan := planOf(change("A", "B"), change("B", "A"))
		_, err := PreviewBatch(graph, plan)
		if !errors.Is(err, ErrCycle) {
			t.Fatalf("err = %v, want ErrCycle", err)
		}
		for _, name := range []string{"A", "B"} {
			if !strings.Contains(err.Error(), name) {
				t.Errorf("error %q does not name dataset %s", err, name)
			}
		}
	})

	t.Run("new dataset forming a cycle", func(t *testing.T) {
		graph := buildGraph(t, P("A"))
		plan := planOf(change("A", "B"), change("B", "A"))
		_, err := PreviewBatch(graph, plan)
		if !errors.Is(err, ErrCycle) {
			t.Fatalf("err = %v, want ErrCycle", err)
		}
	})

	t.Run("rejection leaves graph unchanged", func(t *testing.T) {
		graph := buildGraph(t, P("A"), P("B", "A"))
		snapshot := dump(graph)
		plan := planOf(change("A", "B"), change("B", "A"))
		if _, err := ApplyBatch(graph, plan); !errors.Is(err, ErrCycle) {
			t.Fatalf("err = %v, want ErrCycle", err)
		}
		if got := dump(graph); got != snapshot {
			t.Fatalf("ApplyBatch mutated graph on rejection:\n got %s\nwant %s", got, snapshot)
		}
	})
}

// TestBatchNewDatasetsReferenceEachOther verifies that a batch can register
// new datasets that depend on each other, regardless of plan order.
func TestBatchNewDatasetsReferenceEachOther(t *testing.T) {
	graph := buildGraph(t, P("A"), P("B", "A"))
	// Plan is deliberately shuffled: E appears before D, D before C.
	plan := planOf(change("E", "D"), change("D", "C"), change("C"))
	report, err := PreviewBatch(graph, plan)
	if err != nil {
		t.Fatalf("PreviewBatch unexpected error: %v", err)
	}
	wantNew := []string{"C", "D", "E"}
	if !reflect.DeepEqual(report.NewDatasets, wantNew) {
		t.Errorf("NewDatasets = %v, want %v", report.NewDatasets, wantNew)
	}
	if len(report.ChangedDatasets) != 0 {
		t.Errorf("ChangedDatasets = %v, want empty", report.ChangedDatasets)
	}
	wantAdded := []Relation{
		{Upstream: "C", Downstream: "D"},
		{Upstream: "D", Downstream: "E"},
	}
	if !reflect.DeepEqual(report.AddedRelations, wantAdded) {
		t.Errorf("AddedRelations = %v, want %v", report.AddedRelations, wantAdded)
	}
	// No downstream of A or B is affected; the new chain is independent.
	if len(report.AffectedDownstreams) != 0 {
		t.Errorf("AffectedDownstreams = %v, want empty", report.AffectedDownstreams)
	}

	if _, err := ApplyBatch(graph, plan); err != nil {
		t.Fatalf("ApplyBatch unexpected error: %v", err)
	}
	if got := graph["D"].Parents; !reflect.DeepEqual(got, []string{"C"}) {
		t.Errorf("D.Parents = %v, want [C]", got)
	}
	if got := graph["E"].Parents; !reflect.DeepEqual(got, []string{"D"}) {
		t.Errorf("E.Parents = %v, want [D]", got)
	}
	if roots := Roots(graph); !reflect.DeepEqual(roots, []string{"A", "C"}) {
		t.Errorf("Roots = %v, want [A C]", roots)
	}
}

// TestBatchEmptyPlanAndIdempotentReApply verifies that an empty plan and a
// repeated application of the same plan both succeed with empty change and
// impact lists.
func TestBatchEmptyPlanAndIdempotentReApply(t *testing.T) {
	graph := buildGraph(t, P("A"), P("B", "A"), P("C", "B"))
	plan := planOf(change("B", "A")) // identical to current state

	report, err := PreviewBatch(graph, plan)
	if err != nil {
		t.Fatalf("PreviewBatch unexpected error: %v", err)
	}
	if len(report.NewDatasets) != 0 || len(report.ChangedDatasets) != 0 ||
		len(report.AddedRelations) != 0 || len(report.RemovedRelations) != 0 ||
		len(report.AffectedDownstreams) != 0 {
		t.Errorf("identical plan produced non-empty diff: %+v", report)
	}

	// Empty plan.
	empty := planOf()
	report, err = PreviewBatch(graph, empty)
	if err != nil {
		t.Fatalf("empty plan unexpected error: %v", err)
	}
	if len(report.ChangedDatasets) != 0 || len(report.AddedRelations) != 0 ||
		len(report.RemovedRelations) != 0 || len(report.AffectedDownstreams) != 0 {
		t.Errorf("empty plan produced non-empty diff: %+v", report)
	}

	// Apply once, then again: the second apply is a no-op.
	if _, err := ApplyBatch(graph, plan); err != nil {
		t.Fatalf("first ApplyBatch unexpected error: %v", err)
	}
	snapshot := dump(graph)
	report, err = ApplyBatch(graph, plan)
	if err != nil {
		t.Fatalf("second ApplyBatch unexpected error: %v", err)
	}
	if got := dump(graph); got != snapshot {
		t.Fatalf("re-apply mutated graph:\n got %s\nwant %s", got, snapshot)
	}
	if len(report.ChangedDatasets) != 0 || len(report.AddedRelations) != 0 ||
		len(report.RemovedRelations) != 0 || len(report.AffectedDownstreams) != 0 {
		t.Errorf("re-apply produced non-empty diff: %+v", report)
	}
}

// TestBatchOrderAndDuplicateUpstreamInvariance verifies that changing only the
// input order or repeating an upstream does not count as a change.
func TestBatchOrderAndDuplicateUpstreamInvariance(t *testing.T) {
	graph := buildGraph(t, P("A"), P("B"), P("C", "A", "B"))
	// Same set, different order and duplicates.
	plan := planOf(change("C", "B", "A", "A", "B"))
	report, err := PreviewBatch(graph, plan)
	if err != nil {
		t.Fatalf("PreviewBatch unexpected error: %v", err)
	}
	if len(report.ChangedDatasets) != 0 {
		t.Errorf("order/duplicate-only change counted as a real change: %v", report.ChangedDatasets)
	}
	if len(report.AddedRelations) != 0 || len(report.RemovedRelations) != 0 {
		t.Errorf("order/duplicate-only change produced a relation diff: +%v -%v", report.AddedRelations, report.RemovedRelations)
	}
}

// TestBatchRejectsInvalidPlan verifies that the whole batch is rejected when a
// dataset is declared twice, a name is empty, or an upstream does not exist in
// the final graph.
func TestBatchRejectsInvalidPlan(t *testing.T) {
	t.Run("duplicate declaration", func(t *testing.T) {
		graph := buildGraph(t, P("A"))
		plan := planOf(change("A"), change("A", "B"))
		_, err := PreviewBatch(graph, plan)
		if !errors.Is(err, ErrInvalidArgument) {
			t.Fatalf("err = %v, want ErrInvalidArgument", err)
		}
		if !strings.Contains(err.Error(), "A") {
			t.Errorf("error %q does not name dataset A", err)
		}
	})

	t.Run("empty name", func(t *testing.T) {
		graph := buildGraph(t, P("A"))
		plan := planOf(change(""))
		_, err := PreviewBatch(graph, plan)
		if !errors.Is(err, ErrInvalidArgument) {
			t.Fatalf("err = %v, want ErrInvalidArgument", err)
		}
	})

	t.Run("missing final upstream", func(t *testing.T) {
		graph := buildGraph(t, P("A"))
		plan := planOf(change("A", "ghost"))
		_, err := PreviewBatch(graph, plan)
		if !errors.Is(err, ErrNotFound) {
			t.Fatalf("err = %v, want ErrNotFound", err)
		}
		if !strings.Contains(err.Error(), "ghost") {
			t.Errorf("error %q does not name the missing upstream ghost", err)
		}
	})

	t.Run("upstream missing even though declared later in plan", func(t *testing.T) {
		// B is declared but the plan references a name that is never declared
		// and never existed in the original graph.
		graph := buildGraph(t, P("A"))
		plan := planOf(change("A", "B"), change("B", "nope"))
		_, err := PreviewBatch(graph, plan)
		if !errors.Is(err, ErrNotFound) {
			t.Fatalf("err = %v, want ErrNotFound", err)
		}
	})
}

// TestBatchRejectsInvalidOriginalGraph verifies that a plan cannot paper over
// pre-existing corruption in the input graph.
func TestBatchRejectsInvalidOriginalGraph(t *testing.T) {
	empty := planOf()

	t.Run("nil node", func(t *testing.T) {
		graph := map[string]*Lineage{"A": nil}
		_, err := PreviewBatch(graph, empty)
		if !errors.Is(err, ErrInvalidArgument) {
			t.Fatalf("err = %v, want ErrInvalidArgument", err)
		}
	})

	t.Run("empty name", func(t *testing.T) {
		graph := map[string]*Lineage{"": {Dataset: ""}}
		_, err := PreviewBatch(graph, empty)
		if !errors.Is(err, ErrInvalidArgument) {
			t.Fatalf("err = %v, want ErrInvalidArgument", err)
		}
	})

	t.Run("missing upstream", func(t *testing.T) {
		graph := map[string]*Lineage{"A": {Dataset: "A", Parents: []string{"ghost"}}}
		_, err := PreviewBatch(graph, empty)
		if !errors.Is(err, ErrNotFound) {
			t.Fatalf("err = %v, want ErrNotFound", err)
		}
	})

	t.Run("cycle", func(t *testing.T) {
		graph := map[string]*Lineage{
			"A": {Dataset: "A", Parents: []string{"B"}},
			"B": {Dataset: "B", Parents: []string{"A"}},
		}
		_, err := PreviewBatch(graph, empty)
		if !errors.Is(err, ErrCycle) {
			t.Fatalf("err = %v, want ErrCycle", err)
		}
	})
}

// TestBatchEmptyGraphAllowsNewDatasets verifies that an empty graph accepts a
// batch that registers new datasets.
func TestBatchEmptyGraphAllowsNewDatasets(t *testing.T) {
	graph := map[string]*Lineage{}
	plan := planOf(change("A"), change("B", "A"))
	report, err := PreviewBatch(graph, plan)
	if err != nil {
		t.Fatalf("PreviewBatch unexpected error: %v", err)
	}
	wantNew := []string{"A", "B"}
	if !reflect.DeepEqual(report.NewDatasets, wantNew) {
		t.Errorf("NewDatasets = %v, want %v", report.NewDatasets, wantNew)
	}
	if len(report.ChangedDatasets) != 0 {
		t.Errorf("ChangedDatasets = %v, want empty", report.ChangedDatasets)
	}
	if _, err := ApplyBatch(graph, plan); err != nil {
		t.Fatalf("ApplyBatch unexpected error: %v", err)
	}
	if roots := Roots(graph); !reflect.DeepEqual(roots, []string{"A"}) {
		t.Errorf("Roots = %v, want [A]", roots)
	}
}

// TestBatchPreservesNames verifies that name casing and spaces are preserved.
func TestBatchPreservesNames(t *testing.T) {
	graph := map[string]*Lineage{}
	plan := planOf(change(" Raw-Blocks "), change("RAW-BLOCKS", " Raw-Blocks "))
	if _, err := ApplyBatch(graph, plan); err != nil {
		t.Fatalf("ApplyBatch unexpected error: %v", err)
	}
	if got := graph["RAW-BLOCKS"].Parents; !reflect.DeepEqual(got, []string{" Raw-Blocks "}) {
		t.Fatalf("name was trimmed or case-folded: %v", got)
	}
}

// TestBatchAffectedDownstreams verifies the impact computation: it starts from
// new or changed datasets, follows downstream edges in BOTH the original and
// the final graphs, excludes seeds, and dedupes.
func TestBatchAffectedDownstreams(t *testing.T) {
	t.Run("downstream of changed dataset", func(t *testing.T) {
		// A<-B<-C. B's upstreams change to D (new). C is downstream of B and
		// is affected. D is new (a seed).
		graph := buildGraph(t, P("A"), P("B", "A"), P("C", "B"))
		plan := planOf(change("D"), change("B", "D"))
		report, err := PreviewBatch(graph, plan)
		if err != nil {
			t.Fatalf("PreviewBatch unexpected error: %v", err)
		}
		wantAffected := []string{"C"}
		if !reflect.DeepEqual(report.AffectedDownstreams, wantAffected) {
			t.Errorf("AffectedDownstreams = %v, want %v", report.AffectedDownstreams, wantAffected)
		}
	})

	t.Run("deleted edge still counts from original graph", func(t *testing.T) {
		// A<-B<-C. B becomes a root (edge A-B removed). C is still downstream
		// of B in the final graph, but the impact also follows the original
		// graph: B's children in the original include C. Either way C is
		// affected. The key case is when a removed edge hides a dependent in
		// the final graph.
		//
		// A<-B, A<-C (B and C both depend on A). Plan: B becomes root. In the
		// final graph B has no children, but in the original graph B's children
		// are empty too. The real test: a dataset that was downstream of B in
		// the original graph only.
		//
		// Construct: A<-B<-C, and C also depends on A. Plan: B becomes root.
		// Final: B root, C depends on A. C is downstream of B in the original
		// graph (B->C) but not in the final graph. C must still be affected
		// because the impact follows the original graph too.
		graph := buildGraph(t, P("A"), P("B", "A"), P("C", "B", "A"))
		plan := planOf(change("B"))
		report, err := PreviewBatch(graph, plan)
		if err != nil {
			t.Fatalf("PreviewBatch unexpected error: %v", err)
		}
		wantAffected := []string{"C"}
		if !reflect.DeepEqual(report.AffectedDownstreams, wantAffected) {
			t.Errorf("AffectedDownstreams = %v, want %v (original-graph dependent must still count)", report.AffectedDownstreams, wantAffected)
		}
	})

	t.Run("seeds excluded and paths deduped", func(t *testing.T) {
		// A<-B<-C and A<-C. B changes to root. C is downstream of B (original)
		// and downstream of A (unchanged). C is affected once, not twice.
		graph := buildGraph(t, P("A"), P("B", "A"), P("C", "B", "A"))
		plan := planOf(change("B"))
		report, err := PreviewBatch(graph, plan)
		if err != nil {
			t.Fatalf("PreviewBatch unexpected error: %v", err)
		}
		count := 0
		for _, name := range report.AffectedDownstreams {
			if name == "C" {
				count++
			}
		}
		if count != 1 {
			t.Errorf("C counted %d times in AffectedDownstreams %v, want once", count, report.AffectedDownstreams)
		}
		// B is a seed (changed) and must not appear in affected.
		for _, name := range report.AffectedDownstreams {
			if name == "B" {
				t.Errorf("seed B appears in AffectedDownstreams %v, must be excluded", report.AffectedDownstreams)
			}
		}
	})

	t.Run("new dataset with downstream", func(t *testing.T) {
		// A is new. B (existing) now depends on A, so B is a changed seed. C
		// depends on B and is unchanged, so C is affected (reachable from the
		// new dataset A via B in the final graph).
		graph := buildGraph(t, P("B"), P("C", "B"))
		plan := planOf(change("A"), change("B", "A"))
		report, err := PreviewBatch(graph, plan)
		if err != nil {
			t.Fatalf("PreviewBatch unexpected error: %v", err)
		}
		wantAffected := []string{"C"}
		if !reflect.DeepEqual(report.AffectedDownstreams, wantAffected) {
			t.Errorf("AffectedDownstreams = %v, want %v", report.AffectedDownstreams, wantAffected)
		}
	})
}

// TestBatchDeterministicOutput verifies that the same current graph and plan
// produce byte-identical success output even when records and upstreams are in
// a different order.
func TestBatchDeterministicOutput(t *testing.T) {
	build := func() map[string]*Lineage {
		return buildGraph(t, P("A"), P("B", "A"), P("C", "B"))
	}
	// Plan 1: shuffled order, shuffled upstreams with duplicates.
	plan1 := planOf(
		change("D", "C"),
		change("B"),
		change("C", "A", "A"),
	)
	// Plan 2: different record order, different upstream order, no duplicates.
	plan2 := planOf(
		change("C", "A"),
		change("D", "C"),
		change("B"),
	)

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

	// Reports must also be identical.
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

// TestBatchPreviewApplyConsistency verifies that preview and apply produce the
// same report for the same current graph and plan.
func TestBatchPreviewApplyConsistency(t *testing.T) {
	graph := buildGraph(t, P("A"), P("B", "A"), P("C", "B"))
	plan := planOf(change("B"), change("C", "A"))

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

// TestBatchJSONRoundTrip verifies that a graph marshaled to JSON and parsed
// back is equivalent, and that empty lists encode as [].
func TestBatchJSONRoundTrip(t *testing.T) {
	graph := buildGraph(t, P("A"), P("B", "A"))
	data, err := MarshalGraphFile(graph)
	if err != nil {
		t.Fatalf("MarshalGraphFile unexpected error: %v", err)
	}
	parsed, err := UnmarshalGraphFile(data)
	if err != nil {
		t.Fatalf("UnmarshalGraphFile unexpected error: %v", err)
	}
	if !reflect.DeepEqual(parsed, graph) {
		t.Fatalf("round-trip mismatch:\n got %#v\nwant %#v", parsed, graph)
	}

	// Empty graph encodes datasets as [].
	empty := map[string]*Lineage{}
	data, err = MarshalGraphFile(empty)
	if err != nil {
		t.Fatalf("MarshalGraphFile empty unexpected error: %v", err)
	}
	if !strings.Contains(string(data), `"datasets": []`) {
		t.Errorf("empty graph JSON = %s, want datasets: []", data)
	}
}

// TestBatchUnmarshalRejectsInvalidGraphJSON verifies that UnmarshalGraphFile
// rejects structurally invalid graphs.
func TestBatchUnmarshalRejectsInvalidGraphJSON(t *testing.T) {
	cases := map[string]string{
		"empty name":      `{"datasets":[{"name":"","upstreams":[]}]}`,
		"duplicate":       `{"datasets":[{"name":"A","upstreams":[]},{"name":"A","upstreams":[]}]}`,
		"missing upstream": `{"datasets":[{"name":"A","upstreams":["ghost"]}]}`,
		"cycle":           `{"datasets":[{"name":"A","upstreams":["B"]},{"name":"B","upstreams":["A"]}]}`,
		"invalid json":    `{not json`,
	}
	for name, data := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := UnmarshalGraphFile([]byte(data))
			if err == nil {
				t.Fatalf("expected error for %s, got nil", name)
			}
		})
	}
}
