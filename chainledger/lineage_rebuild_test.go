package chainledger

import (
	"reflect"
	"testing"
)

// This file pins the single relationship rule shared by graph reads
// (UnmarshalGraphFile/ParseSnapshot via lineageFromAdjacency) and batch
// applies (ApplyBatch via applyAdjacency): both materialize lineage through
// materializeLineage, so every dataset's direct upstreams correspond to those
// upstreams' direct downstreams, lists are sorted and duplicate-free, removed
// nodes leave no residual edges, and surviving node records keep exposing the
// updated relationships through the pointers callers already hold.

// assertLineageConsistent checks the parent/child correspondence of every
// node: parents are sorted/deduped, every parent lists the node exactly once,
// and no child list references a missing dataset or repeats a name.
func assertLineageConsistent(t *testing.T, graph map[string]*Lineage) {
	t.Helper()
	for name, entry := range graph {
		if entry == nil {
			t.Errorf("graph[%q] is nil", name)
			continue
		}
		if got := uniqueSorted(entry.Parents); !reflect.DeepEqual(got, entry.Parents) {
			t.Errorf("%s.Parents = %v, want sorted and duplicate-free", name, entry.Parents)
		}
		if got := uniqueSorted(entry.Children); !reflect.DeepEqual(got, entry.Children) {
			t.Errorf("%s.Children = %v, want sorted and duplicate-free", name, entry.Children)
		}
		for _, parent := range entry.Parents {
			p, ok := graph[parent]
			if !ok {
				t.Errorf("%s lists upstream %q which is absent from the graph", name, parent)
				continue
			}
			count := 0
			for _, child := range p.Children {
				if child == name {
					count++
				}
			}
			if count != 1 {
				t.Errorf("upstream %q lists downstream %s %d times, want exactly once", parent, name, count)
			}
		}
		for _, child := range entry.Children {
			c, ok := graph[child]
			if !ok {
				t.Errorf("%s lists child %q which is absent from the graph", name, child)
				continue
			}
			found := false
			for _, parent := range c.Parents {
				if parent == name {
					found = true
				}
			}
			if !found {
				t.Errorf("%s lists child %q, but %s does not list it back as an upstream", name, child, child)
			}
		}
	}
}

// TestReadGraphMaterializesCorrespondingRelations covers the read side of the
// shared rule: after reading a legal graph, A is root, B depends on A, C
// depends on B, and D is a separate root — parent and child lists correspond.
func TestReadGraphMaterializesCorrespondingRelations(t *testing.T) {
	data := []byte(`{
  "datasets": [
    {"name": "A", "upstreams": []},
    {"name": "B", "upstreams": ["A"]},
    {"name": "C", "upstreams": ["B"]},
    {"name": "D", "upstreams": []}
  ]
}`)
	graph, err := UnmarshalGraphFile(data)
	if err != nil {
		t.Fatalf("UnmarshalGraphFile unexpected error: %v", err)
	}
	assertLineageConsistent(t, graph)
	if got := graph["A"].Children; !reflect.DeepEqual(got, []string{"B"}) {
		t.Errorf("A.Children = %v, want [B]", got)
	}
	if got := graph["B"].Parents; !reflect.DeepEqual(got, []string{"A"}) {
		t.Errorf("B.Parents = %v, want [A]", got)
	}
	if got := graph["B"].Children; !reflect.DeepEqual(got, []string{"C"}) {
		t.Errorf("B.Children = %v, want [C]", got)
	}
	if got := graph["C"].Parents; !reflect.DeepEqual(got, []string{"B"}) {
		t.Errorf("C.Parents = %v, want [B]", got)
	}
	if got := graph["D"].Children; len(got) != 0 {
		t.Errorf("D.Children = %v, want empty", got)
	}
}

// TestApplyRepointUpstreamsFollowsSharedRule is the headline example: A is
// root, B depends on A, C depends on B, plus root D. After replacing B's
// upstreams with D, A no longer lists B, D lists B once, B still lists C, and
// C still depends on B.
func TestApplyRepointUpstreamsFollowsSharedRule(t *testing.T) {
	graph := buildGraph(t, P("A"), P("D"), P("B", "A"), P("C", "B"))
	heldB := graph["B"]
	heldC := graph["C"]

	if _, err := ApplyBatch(graph, planOf(change("B", "D"))); err != nil {
		t.Fatalf("ApplyBatch unexpected error: %v", err)
	}
	assertLineageConsistent(t, graph)

	if got := graph["A"].Children; len(got) != 0 {
		t.Errorf("A.Children = %v, want empty (replaced edge dissolved)", got)
	}
	if got := graph["D"].Children; !reflect.DeepEqual(got, []string{"B"}) {
		t.Errorf("D.Children = %v, want [B] exactly once", got)
	}
	if got := graph["B"].Parents; !reflect.DeepEqual(got, []string{"D"}) {
		t.Errorf("B.Parents = %v, want [D]", got)
	}
	if got := graph["B"].Children; !reflect.DeepEqual(got, []string{"C"}) {
		t.Errorf("B.Children = %v, want [C] (untouched downstream kept)", got)
	}
	if got := graph["C"].Parents; !reflect.DeepEqual(got, []string{"B"}) {
		t.Errorf("C.Parents = %v, want [B]", got)
	}

	// Records the Go caller already held expose the updated relationships.
	if heldB.Parents == nil || !reflect.DeepEqual(heldB.Parents, []string{"D"}) {
		t.Errorf("held B record Parents = %v, want updated [D]", heldB.Parents)
	}
	if !reflect.DeepEqual(heldB.Children, []string{"C"}) {
		t.Errorf("held B record Children = %v, want retained [C]", heldB.Children)
	}
	if !reflect.DeepEqual(heldC.Parents, []string{"B"}) {
		t.Errorf("held C record Parents = %v, want unchanged [B]", heldC.Parents)
	}
}

// TestApplyMakeRootKeepsDownstreamFollowsSharedRule verifies that turning B
// into a root through a batch does not let the unified rebuild discard C: A
// drops B, B lists no parents but still lists C.
func TestApplyMakeRootKeepsDownstreamFollowsSharedRule(t *testing.T) {
	graph := buildGraph(t, P("A"), P("D"), P("B", "A"), P("C", "B"))

	if _, err := ApplyBatch(graph, planOf(change("B"))); err != nil {
		t.Fatalf("ApplyBatch unexpected error: %v", err)
	}
	assertLineageConsistent(t, graph)

	if got := graph["B"].Parents; len(got) != 0 {
		t.Errorf("B.Parents = %v, want empty (root)", got)
	}
	if got := graph["B"].Children; !reflect.DeepEqual(got, []string{"C"}) {
		t.Errorf("B.Children = %v, want [C] (downstream survives becoming root)", got)
	}
	if got := graph["A"].Children; len(got) != 0 {
		t.Errorf("A.Children = %v, want empty", got)
	}
	if got := graph["C"].Parents; !reflect.DeepEqual(got, []string{"B"}) {
		t.Errorf("C.Parents = %v, want [B]", got)
	}
	if got := Roots(graph); !reflect.DeepEqual(got, []string{"A", "B", "D"}) {
		t.Errorf("Roots = %v, want [A B D]", got)
	}
}

// TestApplyRemovalLeavesNoResidualEdges verifies a deleted dataset disappears
// completely: no node retains it and no retained parent keeps listing it.
func TestApplyRemovalLeavesNoResidualEdges(t *testing.T) {
	graph := buildGraph(t, P("A"), P("B", "A"), P("C", "B"), P("D"))
	heldC := graph["C"]

	plan := Plan{
		Changes:  []PlanChange{change("B", "D")},
		Removals: []string{"A"},
	}
	if _, err := ApplyBatch(graph, plan); err != nil {
		t.Fatalf("ApplyBatch unexpected error: %v", err)
	}
	assertLineageConsistent(t, graph)

	if _, ok := graph["A"]; ok {
		t.Errorf("removed dataset A still present in the graph")
	}
	for name, entry := range graph {
		for _, parent := range entry.Parents {
			if parent == "A" {
				t.Errorf("retained dataset %s still references removed A", name)
			}
		}
		for _, child := range entry.Children {
			if child == "A" {
				t.Errorf("retained dataset %s still lists removed A as a child", name)
			}
		}
	}
	if got := graph["B"].Parents; !reflect.DeepEqual(got, []string{"D"}) {
		t.Errorf("B.Parents = %v, want [D]", got)
	}
	// The held record for a surviving dataset reflects the rebuild.
	if !reflect.DeepEqual(heldC.Parents, []string{"B"}) {
		t.Errorf("held C record Parents = %v, want [B]", heldC.Parents)
	}
}

// TestApplyDeleteAllThenAddKeepsGraphLegal exercises the empty-graph boundary
// through the same materialization: deleting every dataset leaves a usable
// empty graph, and a later registration through a batch works normally.
func TestApplyDeleteAllThenAddKeepsGraphLegal(t *testing.T) {
	graph := buildGraph(t, P("A"), P("B", "A"))

	if _, err := ApplyBatch(graph, Plan{Removals: []string{"A", "B"}}); err != nil {
		t.Fatalf("ApplyBatch delete-all unexpected error: %v", err)
	}
	if len(graph) != 0 {
		t.Fatalf("graph = %v, want empty", graph)
	}
	assertLineageConsistent(t, graph)

	if _, err := ApplyBatch(graph, planOf(change("Z"))); err != nil {
		t.Fatalf("ApplyBatch add-after-delete unexpected error: %v", err)
	}
	assertLineageConsistent(t, graph)
	if got := graph["Z"].Parents; len(got) != 0 {
		t.Errorf("Z.Parents = %v, want empty", got)
	}
	if got := Roots(graph); !reflect.DeepEqual(got, []string{"Z"}) {
		t.Errorf("Roots = %v, want [Z]", got)
	}
}
