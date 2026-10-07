package chainledger

import (
	"errors"
	"reflect"
	"strings"
	"testing"
)

// sharedNodeGraph builds the headline aliasing scenario: A and B point to the
// same lineage record (no upstreams), R is an independent root.
func sharedNodeGraph() (map[string]*Lineage, *Lineage) {
	shared := &Lineage{Dataset: "A"}
	graph := map[string]*Lineage{
		"A": shared,
		"B": shared,
		"R": {Dataset: "R"},
	}
	return graph, shared
}

// TestSharedNodePreviewRejected: two dataset names aliasing one record must
// reject preview with ErrInvalidArgument, naming both datasets, even though
// the plan itself would be legal on a normal graph.
func TestSharedNodePreviewRejected(t *testing.T) {
	graph, _ := sharedNodeGraph()
	plan := planOf(change("B", "R"))

	report, err := PreviewBatch(graph, plan)
	if err == nil {
		t.Fatalf("PreviewBatch succeeded with report %+v, want ErrInvalidArgument", report)
	}
	if !errors.Is(err, ErrInvalidArgument) {
		t.Fatalf("PreviewBatch err = %v, want errors.Is ErrInvalidArgument", err)
	}
	if !strings.Contains(err.Error(), `"A"`) || !strings.Contains(err.Error(), `"B"`) {
		t.Fatalf("error %q must name both aliased datasets A and B", err)
	}
	if report != nil {
		t.Fatalf("PreviewBatch returned a report alongside the rejection: %+v", report)
	}
}

// TestSharedNodeApplyRejected: apply must reject the aliased graph and leave
// both the graph and the plan exactly as the caller submitted them.
func TestSharedNodeApplyRejected(t *testing.T) {
	graph, shared := sharedNodeGraph()
	plan := planOf(change("B", "R"))

	report, err := ApplyBatch(graph, plan)
	if err == nil {
		t.Fatalf("ApplyBatch succeeded with report %+v, want ErrInvalidArgument", report)
	}
	if !errors.Is(err, ErrInvalidArgument) {
		t.Fatalf("ApplyBatch err = %v, want errors.Is ErrInvalidArgument", err)
	}
	if report != nil {
		t.Fatalf("ApplyBatch returned a report alongside the rejection: %+v", report)
	}

	// The graph must be untouched: same keys, same node pointers, same names
	// and parent/child lists (contents, order, duplicates).
	if len(graph) != 3 {
		t.Fatalf("graph size = %d, want 3 (no dataset added or deleted)", len(graph))
	}
	if graph["A"] != shared || graph["B"] != shared {
		t.Fatalf("aliased node pointers were replaced: A=%p B=%p shared=%p", graph["A"], graph["B"], shared)
	}
	if shared.Dataset != "A" || len(shared.Parents) != 0 || len(shared.Children) != 0 {
		t.Fatalf("shared node mutated: %+v", shared)
	}
	if r := graph["R"]; r.Dataset != "R" || len(r.Parents) != 0 || len(r.Children) != 0 {
		t.Fatalf("R node mutated: %+v", r)
	}

	// The plan must be untouched too.
	wantPlan := planOf(change("B", "R"))
	if !reflect.DeepEqual(plan, wantPlan) {
		t.Fatalf("plan mutated: %+v, want %+v", plan, wantPlan)
	}
}

// TestSharedNodeValidateGraph: ValidateGraph reaches the same conclusion on
// the same original graph.
func TestSharedNodeValidateGraph(t *testing.T) {
	graph, _ := sharedNodeGraph()
	err := ValidateGraph(graph)
	if err == nil {
		t.Fatal("ValidateGraph succeeded, want ErrInvalidArgument")
	}
	if !errors.Is(err, ErrInvalidArgument) {
		t.Fatalf("ValidateGraph err = %v, want errors.Is ErrInvalidArgument", err)
	}
	if !strings.Contains(err.Error(), `"A"`) || !strings.Contains(err.Error(), `"B"`) {
		t.Fatalf("error %q must name both aliased datasets A and B", err)
	}
}

// TestSharedNodeEmptyPlanRejected: a plan with no adjustments at all cannot
// launder the aliased graph into a success report.
func TestSharedNodeEmptyPlanRejected(t *testing.T) {
	graph, _ := sharedNodeGraph()
	if report, err := PreviewBatch(graph, Plan{}); !errors.Is(err, ErrInvalidArgument) {
		t.Fatalf("PreviewBatch with empty plan = (%+v, %v), want ErrInvalidArgument", report, err)
	}
	if report, err := ApplyBatch(graph, Plan{}); !errors.Is(err, ErrInvalidArgument) {
		t.Fatalf("ApplyBatch with empty plan = (%+v, %v), want ErrInvalidArgument", report, err)
	}
}

// TestSharedNodeRemovalPlanRejected: deleting one of the aliased names does
// not make the original graph legal — the graph is judged as it is, before
// any removal is applied.
func TestSharedNodeRemovalPlanRejected(t *testing.T) {
	graph, shared := sharedNodeGraph()
	plan := Plan{Removals: []string{"B"}}

	if report, err := PreviewBatch(graph, plan); !errors.Is(err, ErrInvalidArgument) {
		t.Fatalf("PreviewBatch = (%+v, %v), want ErrInvalidArgument", report, err)
	}
	if report, err := ApplyBatch(graph, plan); !errors.Is(err, ErrInvalidArgument) {
		t.Fatalf("ApplyBatch = (%+v, %v), want ErrInvalidArgument", report, err)
	}
	// The removal must not have been carried out before the rejection.
	if graph["B"] != shared {
		t.Fatalf("B was deleted or replaced before the rejection: graph[B]=%p shared=%p", graph["B"], shared)
	}
}

// TestSharedNodeReportedPairIsStable: with several sharing groups and one
// record aliased by three names, the reported pair is the lexicographically
// smallest pair of names in byte order, regardless of map iteration order.
func TestSharedNodeReportedPairIsStable(t *testing.T) {
	xy := &Lineage{Dataset: "X"}
	abc := &Lineage{Dataset: "c"}
	graph := map[string]*Lineage{
		"Y": xy,
		"X": xy,
		"a": abc,
		"c": abc,
		"b": abc,
		"Z": {Dataset: "Z"},
	}
	// Groups: {X,Y} and {a,b,c}. Candidates are ("X","Y") and ("a","b");
	// "X" < "a" in byte order, so ("X","Y") wins.
	for i := 0; i < 50; i++ {
		err := ValidateGraph(graph)
		if !errors.Is(err, ErrInvalidArgument) {
			t.Fatalf("ValidateGraph err = %v, want errors.Is ErrInvalidArgument", err)
		}
		if !strings.Contains(err.Error(), `"X"`) || !strings.Contains(err.Error(), `"Y"`) {
			t.Fatalf("iteration %d: error %q must name the smallest pair X and Y", i, err)
		}
		if strings.Contains(err.Error(), `"a"`) || strings.Contains(err.Error(), `"b"`) {
			t.Fatalf("iteration %d: error %q must not name the larger pair", i, err)
		}
	}
}

// TestSharedNodeThreeAliasesReportedPair: one record aliased by three names
// reports the two smallest names.
func TestSharedNodeThreeAliasesReportedPair(t *testing.T) {
	node := &Lineage{Dataset: "b"}
	graph := map[string]*Lineage{"c": node, "a": node, "b": node}
	err := ValidateGraph(graph)
	if !errors.Is(err, ErrInvalidArgument) {
		t.Fatalf("ValidateGraph err = %v, want errors.Is ErrInvalidArgument", err)
	}
	if !strings.Contains(err.Error(), `"a"`) || !strings.Contains(err.Error(), `"b"`) {
		t.Fatalf("error %q must name the two smallest aliases a and b", err)
	}
}

// TestDistinctNodesWithIdenticalFieldsAreLegal: two independent nodes whose
// fields happen to be identical are not a conflict — only pointer identity is
// judged. Names keep their case and surrounding whitespace verbatim.
func TestDistinctNodesWithIdenticalFieldsAreLegal(t *testing.T) {
	upstreams := []string{"R"}
	graph := map[string]*Lineage{
		"R": {Dataset: "R"},
		"A": {Dataset: "A", Parents: upstreams},
		"B": {Dataset: "B", Parents: upstreams}, // same backing slice as A
	}
	if err := ValidateGraph(graph); err != nil {
		t.Fatalf("ValidateGraph unexpected error: %v", err)
	}
	report, err := ApplyBatch(graph, planOf(change("B", "R")))
	if err != nil {
		t.Fatalf("ApplyBatch unexpected error: %v", err)
	}
	if len(report.ChangedDatasets) != 0 {
		t.Fatalf("ChangedDatasets = %v, want empty (B already had upstream R)", report.ChangedDatasets)
	}
}

// TestNormalGraphBatchStillWorks: a graph with one record per name keeps the
// existing behavior — wholesale upstream replacement, downstream preserved,
// preview read-only, and held node pointers observing the applied relations.
func TestNormalGraphBatchStillWorks(t *testing.T) {
	graph := buildGraph(t, P("R"), P("A"), P("B"), P("D", "B"))
	aNode := graph["A"]

	preview, err := PreviewBatch(graph, planOf(change("B", "R")))
	if err != nil {
		t.Fatalf("PreviewBatch unexpected error: %v", err)
	}
	if got := graph["B"].Parents; len(got) != 0 {
		t.Fatalf("preview mutated graph: B.Parents = %v", got)
	}
	if !reflect.DeepEqual(preview.ChangedDatasets, []string{"B"}) {
		t.Fatalf("preview ChangedDatasets = %v, want [B]", preview.ChangedDatasets)
	}

	report, err := ApplyBatch(graph, planOf(change("B", "R")))
	if err != nil {
		t.Fatalf("ApplyBatch unexpected error: %v", err)
	}
	if !reflect.DeepEqual(report, preview) {
		t.Fatalf("apply report %+v differs from preview %+v", report, preview)
	}
	if got := graph["B"].Parents; !reflect.DeepEqual(got, []string{"R"}) {
		t.Fatalf("B.Parents = %v, want [R]", got)
	}
	// Downstream of B is preserved, and a pointer held before the batch sees
	// the updated graph.
	if got := graph["D"].Parents; !reflect.DeepEqual(got, []string{"B"}) {
		t.Fatalf("D.Parents = %v, want [B]", got)
	}
	if graph["A"] != aNode {
		t.Fatalf("surviving node A was replaced: %p != %p", graph["A"], aNode)
	}
	if got := aNode.Parents; len(got) != 0 {
		t.Fatalf("held A node Parents = %v, want empty", got)
	}
}
