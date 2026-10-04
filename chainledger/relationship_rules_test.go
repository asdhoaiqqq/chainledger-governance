package chainledger

import (
	"reflect"
	"sort"
	"testing"
)

// This file pins the shared relationship-maintenance rule consolidated from
// the graph reader and the batch apply path: after a legal graph is read and
// after a batch is applied, every dataset's direct upstreams and each
// upstream's direct downstreams must correspond, and every name list must be
// sorted by name byte order and duplicate-free.

// assertCorrespondence checks the bidirectional invariant on a graph:
//
//   - child lists parent  <=>  parent lists child (every pair corresponds);
//   - every Parents and Children list is sorted and duplicate-free;
//   - every Lineage.Dataset matches its map key.
func assertCorrespondence(t *testing.T, graph map[string]*Lineage) {
	t.Helper()
	for name, entry := range graph {
		if entry == nil {
			t.Errorf("dataset %q has a nil lineage node", name)
			continue
		}
		if entry.Dataset != name {
			t.Errorf("graph[%q].Dataset = %q, want %q", name, entry.Dataset, name)
		}
		if !sortedUnique(entry.Parents) {
			t.Errorf("%s.Parents = %v, want sorted, duplicate-free", name, entry.Parents)
		}
		if !sortedUnique(entry.Children) {
			t.Errorf("%s.Children = %v, want sorted, duplicate-free", name, entry.Children)
		}
		// child lists parent  =>  parent lists child
		for _, parent := range entry.Parents {
			p, ok := graph[parent]
			if !ok || p == nil {
				t.Errorf("%s lists upstream %q which is not in the graph", name, parent)
				continue
			}
			if index := sort.SearchStrings(p.Children, name); index >= len(p.Children) || p.Children[index] != name {
				t.Errorf("%s lists upstream %q, but %s.Children = %v does not list %s",
					name, parent, parent, p.Children, name)
			}
		}
		// parent lists child  =>  child lists parent
		for _, child := range entry.Children {
			c, ok := graph[child]
			if !ok || c == nil {
				t.Errorf("%s lists downstream %q which is not in the graph", name, child)
				continue
			}
			if index := sort.SearchStrings(c.Parents, name); index >= len(c.Parents) || c.Parents[index] != name {
				t.Errorf("%s lists downstream %q, but %s.Parents = %v does not list %s",
					name, child, child, c.Parents, name)
			}
		}
	}
}

func sortedUnique(names []string) bool {
	for i := 1; i < len(names); i++ {
		if names[i-1] >= names[i] {
			return false
		}
	}
	return true
}

// TestReaderRebuildsCorrespondingRelationships: reading a legal graph yields
// mutually corresponding, sorted, duplicate-free parent/child lists.
func TestReaderRebuildsCorrespondingRelationships(t *testing.T) {
	const doc = `{"datasets":[
		{"name":"C","upstreams":["B","B"]},
		{"name":"A","upstreams":[]},
		{"name":"D","upstreams":[]},
		{"name":"B","upstreams":["A"]}
	]}`
	graph, err := UnmarshalGraphFile([]byte(doc))
	if err != nil {
		t.Fatalf("UnmarshalGraphFile unexpected error: %v", err)
	}
	assertCorrespondence(t, graph)

	want := map[string]struct{ parents, children []string }{
		"A": {parents: nil, children: []string{"B"}},
		"B": {parents: []string{"A"}, children: []string{"C"}},
		"C": {parents: []string{"B"}, children: nil},
		"D": {parents: nil, children: nil},
	}
	for name, w := range want {
		if got := graph[name].Parents; !reflect.DeepEqual(got, w.parents) {
			t.Errorf("%s.Parents = %v, want %v", name, got, w.parents)
		}
		if got := graph[name].Children; !reflect.DeepEqual(got, w.children) {
			t.Errorf("%s.Children = %v, want %v", name, got, w.children)
		}
	}
}

// TestApplyRepointSharesRelationshipRule is the headline scenario: roots A and
// D, B depends on A, C depends on B. After replacing B's upstreams with D, A no
// longer lists B, D lists B exactly once, B still lists C, and C still depends
// on B — and the applied graph satisfies the same correspondence rule a read
// enforces.
func TestApplyRepointSharesRelationshipRule(t *testing.T) {
	graph := buildGraph(t, P("A"), P("D"), P("B", "A"), P("C", "B"))

	if _, err := ApplyBatch(graph, planOf(change("B", "D"))); err != nil {
		t.Fatalf("ApplyBatch unexpected error: %v", err)
	}
	assertCorrespondence(t, graph)

	if got := graph["A"].Children; len(got) != 0 {
		t.Errorf("A.Children = %v, want empty (replaced edge leaves the old upstream)", got)
	}
	if got := graph["D"].Children; !reflect.DeepEqual(got, []string{"B"}) {
		t.Errorf("D.Children = %v, want [B] registered once", got)
	}
	if got := graph["B"].Parents; !reflect.DeepEqual(got, []string{"D"}) {
		t.Errorf("B.Parents = %v, want [D]", got)
	}
	if got := graph["B"].Children; !reflect.DeepEqual(got, []string{"C"}) {
		t.Errorf("B.Children = %v, want [C] (unadjusted downstream retained)", got)
	}
	if got := graph["C"].Parents; !reflect.DeepEqual(got, []string{"B"}) {
		t.Errorf("C.Parents = %v, want [B]", got)
	}

	// An applied graph and a graph freshly read from its export are the same
	// graph: reader and apply share one relationship-maintenance rule.
	data, err := MarshalGraphFile(graph)
	if err != nil {
		t.Fatalf("MarshalGraphFile unexpected error: %v", err)
	}
	reread, err := UnmarshalGraphFile(data)
	if err != nil {
		t.Fatalf("UnmarshalGraphFile unexpected error: %v", err)
	}
	if !reflect.DeepEqual(reread, graph) {
		t.Fatalf("applied graph and reread graph differ:\n got %#v\nwant %#v", reread, graph)
	}
}

// TestApplyMakeRootKeepsDownstream: turning B into a root must dissolve the
// A->B edge but retain the B->C downstream, under the same unified rebuild.
func TestApplyMakeRootKeepsDownstream(t *testing.T) {
	graph := buildGraph(t, P("A"), P("D"), P("B", "A"), P("C", "B"))

	if _, err := ApplyBatch(graph, planOf(change("B"))); err != nil {
		t.Fatalf("ApplyBatch unexpected error: %v", err)
	}
	assertCorrespondence(t, graph)

	if got := graph["B"].Parents; len(got) != 0 {
		t.Errorf("B.Parents = %v, want empty (root)", got)
	}
	if got := graph["B"].Children; !reflect.DeepEqual(got, []string{"C"}) {
		t.Errorf("B.Children = %v, want [C] (downstream must not be lost on becoming root)", got)
	}
	if got := graph["A"].Children; len(got) != 0 {
		t.Errorf("A.Children = %v, want empty", got)
	}
	if got := graph["C"].Parents; !reflect.DeepEqual(got, []string{"B"}) {
		t.Errorf("C.Parents = %v, want [B]", got)
	}
}

// TestApplyRemovalLeavesNoResidualEdges: deleted records leave no residual
// relationship in retained nodes, and deleting everything yields a legal empty
// graph.
func TestApplyRemovalLeavesNoResidualEdges(t *testing.T) {
	graph := buildGraph(t, P("A"), P("B", "A"), P("C", "B"))
	plan := Plan{Removals: []string{"A", "B", "C"}}

	report, err := PreviewBatch(graph, plan)
	if err != nil {
		t.Fatalf("PreviewBatch unexpected error: %v", err)
	}
	if len(report.FinalGraph.Datasets) != 0 {
		t.Errorf("preview FinalGraph.Datasets = %v, want empty", report.FinalGraph.Datasets)
	}

	if _, err := ApplyBatch(graph, plan); err != nil {
		t.Fatalf("ApplyBatch unexpected error: %v", err)
	}
	assertCorrespondence(t, graph)
	if len(graph) != 0 {
		t.Errorf("graph = %v, want empty after deleting every dataset", graph)
	}

	// The empty graph stays legal and round-trips as [].
	data, err := MarshalGraphFile(graph)
	if err != nil {
		t.Fatalf("MarshalGraphFile empty unexpected error: %v", err)
	}
	if got := string(data); got != "{\n  \"datasets\": []\n}" {
		t.Errorf("empty graph export = %q, want datasets []", got)
	}
}
