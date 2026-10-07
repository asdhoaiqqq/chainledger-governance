package chainledger

import (
	"errors"
	"reflect"
	"strings"
	"testing"
)

// sharedNodeGraph builds the headline aliasing defect: datasets A and B point
// at the SAME *Lineage record (no upstreams), while R is an independent root
// with its own record.
func sharedNodeGraph() (graph map[string]*Lineage, shared *Lineage) {
	shared = &Lineage{Dataset: "A"}
	return map[string]*Lineage{
		"A": shared,
		"B": shared,
		"R": {Dataset: "R"},
	}, shared
}

// graphSnapshot captures everything a rejection must preserve: the name set,
// the node pointer behind each name, and each node's fields with their
// contents, order, and duplicates.
func graphSnapshot(graph map[string]*Lineage) map[string]Lineage {
	snap := make(map[string]Lineage, len(graph))
	for name, entry := range graph {
		if entry == nil {
			continue
		}
		snap[name] = Lineage{
			Dataset:  entry.Dataset,
			Parents:  append([]string(nil), entry.Parents...),
			Children: append([]string(nil), entry.Children...),
		}
	}
	return snap
}

// assertGraphUnchanged fails if the graph's name set, node pointers, or node
// contents differ from the snapshot taken before the call.
func assertGraphUnchanged(t *testing.T, graph map[string]*Lineage, before map[string]Lineage, ptrs map[string]*Lineage) {
	t.Helper()
	if len(graph) != len(before) {
		t.Fatalf("graph size = %d, want %d (datasets added or removed)", len(graph), len(before))
	}
	for name, want := range before {
		entry, ok := graph[name]
		if !ok {
			t.Fatalf("dataset %q was deleted from the graph", name)
		}
		if entry != ptrs[name] {
			t.Errorf("dataset %q: node pointer was replaced", name)
		}
		got := Lineage{
			Dataset:  entry.Dataset,
			Parents:  append([]string(nil), entry.Parents...),
			Children: append([]string(nil), entry.Children...),
		}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("dataset %q: node = %+v, want %+v", name, got, want)
		}
	}
}

func ptrMap(graph map[string]*Lineage) map[string]*Lineage {
	ptrs := make(map[string]*Lineage, len(graph))
	for name, entry := range graph {
		ptrs[name] = entry
	}
	return ptrs
}

// TestBatchSharedNodeRejected is the headline scenario: A and B share one
// record, R is an independent root, and the plan only repoints B's upstreams
// to R. Both preview and apply must refuse the graph instead of rewriting A
// through B's record or leaving B without its new upstream.
func TestBatchSharedNodeRejected(t *testing.T) {
	plan := planOf(change("B", "R"))

	t.Run("preview", func(t *testing.T) {
		graph, _ := sharedNodeGraph()
		before := graphSnapshot(graph)
		ptrs := ptrMap(graph)

		report, err := PreviewBatch(graph, plan)
		if !errors.Is(err, ErrInvalidArgument) {
			t.Fatalf("err = %v, want ErrInvalidArgument", err)
		}
		if report != nil {
			t.Errorf("report = %+v, want nil on rejection", report)
		}
		for _, name := range []string{`"A"`, `"B"`} {
			if !strings.Contains(err.Error(), name) {
				t.Errorf("error %q does not name dataset %s", err, name)
			}
		}
		assertGraphUnchanged(t, graph, before, ptrs)
	})

	t.Run("apply", func(t *testing.T) {
		graph, _ := sharedNodeGraph()
		before := graphSnapshot(graph)
		ptrs := ptrMap(graph)

		report, err := ApplyBatch(graph, plan)
		if !errors.Is(err, ErrInvalidArgument) {
			t.Fatalf("err = %v, want ErrInvalidArgument", err)
		}
		if report != nil {
			t.Errorf("report = %+v, want nil on rejection", report)
		}
		for _, name := range []string{`"A"`, `"B"`} {
			if !strings.Contains(err.Error(), name) {
				t.Errorf("error %q does not name dataset %s", err, name)
			}
		}
		assertGraphUnchanged(t, graph, before, ptrs)
	})

	t.Run("validate graph agrees", func(t *testing.T) {
		graph, _ := sharedNodeGraph()
		err := ValidateGraph(graph)
		if !errors.Is(err, ErrInvalidArgument) {
			t.Fatalf("ValidateGraph err = %v, want ErrInvalidArgument", err)
		}
		for _, name := range []string{`"A"`, `"B"`} {
			if !strings.Contains(err.Error(), name) {
				t.Errorf("error %q does not name dataset %s", err, name)
			}
		}
	})
}

// TestBatchSharedNodeEmptyPlanRejected: a plan with no adjustments at all
// cannot make an aliased graph pass — the defect is in the original graph.
func TestBatchSharedNodeEmptyPlanRejected(t *testing.T) {
	graph, _ := sharedNodeGraph()
	before := graphSnapshot(graph)
	ptrs := ptrMap(graph)

	if _, err := PreviewBatch(graph, Plan{}); !errors.Is(err, ErrInvalidArgument) {
		t.Fatalf("preview err = %v, want ErrInvalidArgument", err)
	}
	if _, err := ApplyBatch(graph, Plan{}); !errors.Is(err, ErrInvalidArgument) {
		t.Fatalf("apply err = %v, want ErrInvalidArgument", err)
	}
	assertGraphUnchanged(t, graph, before, ptrs)
}

// TestBatchSharedNodeRemovalStillRejected: a plan that deletes one of the
// aliased names must not be executed first with the graph then judged legal —
// the original graph is invalid, the deletion never happens, and both names
// keep their records.
func TestBatchSharedNodeRemovalStillRejected(t *testing.T) {
	graph, shared := sharedNodeGraph()
	before := graphSnapshot(graph)
	ptrs := ptrMap(graph)
	plan := Plan{Removals: []string{"B"}}

	if _, err := PreviewBatch(graph, plan); !errors.Is(err, ErrInvalidArgument) {
		t.Fatalf("preview err = %v, want ErrInvalidArgument", err)
	}
	if _, err := ApplyBatch(graph, plan); !errors.Is(err, ErrInvalidArgument) {
		t.Fatalf("apply err = %v, want ErrInvalidArgument", err)
	}
	if graph["A"] != shared || graph["B"] != shared {
		t.Fatalf("aliased records were replaced or deleted: A=%p B=%p shared=%p", graph["A"], graph["B"], shared)
	}
	assertGraphUnchanged(t, graph, before, ptrs)
}

// TestBatchSharedNodePlanUntouched: the submitted plan must come through a
// rejection exactly as the caller built it.
func TestBatchSharedNodePlanUntouched(t *testing.T) {
	graph, _ := sharedNodeGraph()
	plan := Plan{
		Changes:  []PlanChange{{Name: "B", Upstreams: []string{"R", "R"}}},
		Removals: []string{"Z"},
	}
	want := Plan{
		Changes:  []PlanChange{{Name: "B", Upstreams: []string{"R", "R"}}},
		Removals: []string{"Z"},
	}
	if _, err := ApplyBatch(graph, plan); !errors.Is(err, ErrInvalidArgument) {
		t.Fatalf("err = %v, want ErrInvalidArgument", err)
	}
	if !reflect.DeepEqual(plan, want) {
		t.Errorf("plan = %+v, want %+v (plan was rewritten)", plan, want)
	}
}

// TestBatchSharedNodeReportedPairIsDeterministic: with several aliased
// records, and with one record referenced by three names, the reported pair
// is the earliest one in name byte order, stable across runs regardless of
// map iteration order.
func TestBatchSharedNodeReportedPairIsDeterministic(t *testing.T) {
	build := func() map[string]*Lineage {
		sharedABC := &Lineage{Dataset: "A"}
		sharedYZ := &Lineage{Dataset: "Y"}
		return map[string]*Lineage{
			// One record shared by three names: the pair is (A, B), the two
			// smallest names, never (A, C) or (B, C).
			"C": sharedABC,
			"A": sharedABC,
			"B": sharedABC,
			// A second shared record whose names all sort later.
			"Z": sharedYZ,
			"Y": sharedYZ,
			"R": {Dataset: "R"},
		}
	}
	for i := 0; i < 50; i++ {
		graph := build()
		err := ValidateGraph(graph)
		if !errors.Is(err, ErrInvalidArgument) {
			t.Fatalf("err = %v, want ErrInvalidArgument", err)
		}
		msg := err.Error()
		if !strings.Contains(msg, `"A"`) || !strings.Contains(msg, `"B"`) {
			t.Fatalf("error %q does not report the pair (A, B)", msg)
		}
		if strings.Contains(msg, `"C"`) || strings.Contains(msg, `"Y"`) || strings.Contains(msg, `"Z"`) {
			t.Fatalf("error %q names a dataset outside the earliest pair", msg)
		}
	}
}

// TestBatchSharedNodeLaterPairWhenEarlierNamesAreIndependent: when the
// alphabetically smallest names each have their own record, the reported pair
// comes from the shared record that sorts first among the actual conflicts.
func TestBatchSharedNodeLaterPairWhenEarlierNamesAreIndependent(t *testing.T) {
	sharedBC := &Lineage{Dataset: "B"}
	sharedEF := &Lineage{Dataset: "E"}
	graph := map[string]*Lineage{
		"A": {Dataset: "A"},
		"B": sharedBC,
		"C": sharedBC,
		"E": sharedEF,
		"F": sharedEF,
	}
	err := ValidateGraph(graph)
	if !errors.Is(err, ErrInvalidArgument) {
		t.Fatalf("err = %v, want ErrInvalidArgument", err)
	}
	msg := err.Error()
	if !strings.Contains(msg, `"B"`) || !strings.Contains(msg, `"C"`) {
		t.Fatalf("error %q does not report the pair (B, C)", msg)
	}
}

// TestBatchDistinctNodesWithIdenticalFieldsAreLegal: two records that happen
// to carry exactly the same fields are not a conflict — only pointer sharing
// is. The batch runs normally on such a graph.
func TestBatchDistinctNodesWithIdenticalFieldsAreLegal(t *testing.T) {
	graph := map[string]*Lineage{
		"A": {Dataset: "A", Parents: []string{"R"}, Children: []string{}},
		"B": {Dataset: "B", Parents: []string{"R"}, Children: []string{}},
		"R": {Dataset: "R", Children: []string{"A", "B"}},
	}
	if err := ValidateGraph(graph); err != nil {
		t.Fatalf("ValidateGraph unexpected error: %v", err)
	}
	report, err := ApplyBatch(graph, planOf(change("B", "R")))
	if err != nil {
		t.Fatalf("ApplyBatch unexpected error: %v", err)
	}
	if len(report.ChangedDatasets) != 0 {
		t.Errorf("ChangedDatasets = %v, want empty (B already had upstream R)", report.ChangedDatasets)
	}
}

// TestBatchDistinctNodesSharingUpstreamSliceAreLegal: two independent records
// whose parent lists happen to share one backing slice are still a normal
// graph — the aliasing rule judges node pointers only.
func TestBatchDistinctNodesSharingUpstreamSliceAreLegal(t *testing.T) {
	parents := []string{"R"}
	graph := map[string]*Lineage{
		"A": {Dataset: "A", Parents: parents},
		"B": {Dataset: "B", Parents: parents},
		"R": {Dataset: "R"},
	}
	if err := ValidateGraph(graph); err != nil {
		t.Fatalf("ValidateGraph unexpected error: %v", err)
	}
	if _, err := PreviewBatch(graph, planOf(change("B"))); err != nil {
		t.Fatalf("PreviewBatch unexpected error: %v", err)
	}
}

// TestBatchNormalGraphStillAppliesThroughHeldPointers: a normal graph keeps
// the existing behavior — a batch replaces upstreams wholesale, preserves
// downstreams, and node pointers the caller already holds expose the new
// relationships after apply.
func TestBatchNormalGraphStillAppliesThroughHeldPointers(t *testing.T) {
	graph := buildGraph(t, P("R"), P("A"), P("B"), P("D", "B"))
	bPtr := graph["B"]

	report, err := ApplyBatch(graph, planOf(change("B", "R")))
	if err != nil {
		t.Fatalf("ApplyBatch unexpected error: %v", err)
	}
	if !reflect.DeepEqual(report.ChangedDatasets, []string{"B"}) {
		t.Errorf("ChangedDatasets = %v, want [B]", report.ChangedDatasets)
	}
	if graph["B"] != bPtr {
		t.Fatalf("B's node pointer was replaced by apply")
	}
	if !reflect.DeepEqual(bPtr.Parents, []string{"R"}) {
		t.Errorf("B.Parents = %v, want [R] read through the held pointer", bPtr.Parents)
	}
	if !reflect.DeepEqual(bPtr.Children, []string{"D"}) {
		t.Errorf("B.Children = %v, want [D] (downstream preserved)", bPtr.Children)
	}
	if !reflect.DeepEqual(graph["R"].Children, []string{"B"}) {
		t.Errorf("R.Children = %v, want [B]", graph["R"].Children)
	}
}
