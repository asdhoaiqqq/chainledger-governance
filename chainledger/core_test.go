package chainledger

import (
	"reflect"
	"testing"
)

func register(t *testing.T, graph map[string]*Lineage, name string, parents []string) {
	t.Helper()
	if err := Register(graph, Dataset{Name: name}, parents); err != nil {
		t.Fatalf("Register(%q, %v): %v", name, parents, err)
	}
}

func TestRegisterLinksBothDirections(t *testing.T) {
	graph := map[string]*Lineage{}
	register(t, graph, "A", nil)
	register(t, graph, "B", []string{"A"})
	register(t, graph, "C", []string{"B"})

	assertParents(t, graph, "B", []string{"A"})
	assertParents(t, graph, "C", []string{"B"})
	assertChildren(t, graph, "A", []string{"B"})
	assertChildren(t, graph, "B", []string{"C"})
	assertChildren(t, graph, "C", nil)
}

func TestReregisterReplacesDirectUpstreams(t *testing.T) {
	graph := map[string]*Lineage{}
	register(t, graph, "A", nil)
	register(t, graph, "B", []string{"A"})
	register(t, graph, "C", []string{"B"})
	register(t, graph, "D", nil)

	// B moves from A to D: old edge A-B is removed, new edge D-B is added,
	// and C still depends on B.
	register(t, graph, "B", []string{"D"})

	assertParents(t, graph, "B", []string{"D"})
	assertParents(t, graph, "C", []string{"B"})
	assertChildren(t, graph, "A", nil)
	assertChildren(t, graph, "D", []string{"B"})
	assertChildren(t, graph, "B", []string{"C"})
}

func TestReregisterWithEmptyParentsBecomesRoot(t *testing.T) {
	graph := map[string]*Lineage{}
	register(t, graph, "A", nil)
	register(t, graph, "B", []string{"A"})
	register(t, graph, "C", []string{"B"})

	register(t, graph, "B", nil)

	assertParents(t, graph, "B", nil)
	assertChildren(t, graph, "A", nil)
	assertChildren(t, graph, "B", []string{"C"})
	if got := Roots(graph); !reflect.DeepEqual(got, []string{"A", "B"}) {
		t.Fatalf("Roots() = %v, want [A B]", got)
	}
}

func TestRepeatedRegistrationIsIdempotent(t *testing.T) {
	graph := map[string]*Lineage{}
	register(t, graph, "A", nil)
	register(t, graph, "B", []string{"A"})
	register(t, graph, "C", []string{"B"})

	before := snapshot(graph)
	register(t, graph, "B", []string{"A"})
	if after := snapshot(graph); !reflect.DeepEqual(before, after) {
		t.Fatalf("repeated registration changed lineage:\nbefore: %+v\nafter:  %+v", before, after)
	}
}

func TestDuplicateParentsAndOrderIndependence(t *testing.T) {
	graph := map[string]*Lineage{}
	register(t, graph, "A", nil)
	register(t, graph, "B", nil)
	register(t, graph, "C", []string{"B", "A", "B", "A"})

	assertParents(t, graph, "C", []string{"A", "B"})
	assertChildren(t, graph, "A", []string{"C"})
	assertChildren(t, graph, "B", []string{"C"})

	graph2 := map[string]*Lineage{}
	register(t, graph2, "A", nil)
	register(t, graph2, "B", nil)
	register(t, graph2, "C", []string{"A", "B"})
	if !reflect.DeepEqual(snapshot(graph), snapshot(graph2)) {
		t.Fatal("different parent orders produced different lineage")
	}
}

func TestParentsAndChildrenAreSortedAndUnique(t *testing.T) {
	graph := map[string]*Lineage{}
	register(t, graph, "root-a", nil)
	register(t, graph, "root-b", nil)
	register(t, graph, "root-c", nil)
	register(t, graph, "mid", []string{"root-c", "root-a", "root-b"})
	register(t, graph, "leaf", []string{"mid"})

	assertParents(t, graph, "mid", []string{"root-a", "root-b", "root-c"})
	assertChildren(t, graph, "root-a", []string{"mid"})
	assertChildren(t, graph, "root-b", []string{"mid"})
	assertChildren(t, graph, "root-c", []string{"mid"})
	assertChildren(t, graph, "mid", []string{"leaf"})
}

func TestCallerSliceMutationDoesNotAffectGraph(t *testing.T) {
	graph := map[string]*Lineage{}
	register(t, graph, "A", nil)
	register(t, graph, "B", nil)
	parents := []string{"A", "B"}
	register(t, graph, "C", parents)

	parents[0] = "zzz"
	parents[1] = "A"
	assertParents(t, graph, "C", []string{"A", "B"})
}

func TestCyclesAreRejected(t *testing.T) {
	graph := map[string]*Lineage{}
	register(t, graph, "A", nil)
	register(t, graph, "B", []string{"A"})
	register(t, graph, "C", []string{"B"})

	// Changing A to depend on C would close the loop A-C-B-A.
	if err := Register(graph, Dataset{Name: "A"}, []string{"C"}); err == nil {
		t.Fatal("Register(A, [C]) succeeded, want cycle error")
	}
	assertParents(t, graph, "A", nil)
	assertChildren(t, graph, "B", []string{"C"})

	// Self dependency is a cycle too.
	if err := Register(graph, Dataset{Name: "A"}, []string{"A"}); err == nil {
		t.Fatal("Register(A, [A]) succeeded, want cycle error")
	}
	assertParents(t, graph, "A", nil)
}

func TestCycleRejectionLeavesGraphUntouched(t *testing.T) {
	graph := map[string]*Lineage{}
	register(t, graph, "A", nil)
	register(t, graph, "B", []string{"A"})
	register(t, graph, "C", []string{"B"})

	before := snapshot(graph)
	if err := Register(graph, Dataset{Name: "A"}, []string{"C"}); err == nil {
		t.Fatal("want cycle error")
	}
	if after := snapshot(graph); !reflect.DeepEqual(before, after) {
		t.Fatalf("rejected registration changed lineage:\nbefore: %+v\nafter:  %+v", before, after)
	}
}

func TestRejectionWithUnknownParentLeavesGraphUntouched(t *testing.T) {
	graph := map[string]*Lineage{}
	register(t, graph, "A", nil)
	register(t, graph, "B", []string{"A"})

	before := snapshot(graph)
	// "A" is valid, "ghost" does not exist: no partial edge, no new node.
	if err := Register(graph, Dataset{Name: "B"}, []string{"A", "ghost"}); err == nil {
		t.Fatal("Register with unknown parent succeeded, want error")
	}
	if after := snapshot(graph); !reflect.DeepEqual(before, after) {
		t.Fatalf("rejected registration changed lineage:\nbefore: %+v\nafter:  %+v", before, after)
	}
	if _, ok := graph["ghost"]; ok {
		t.Fatal("rejected registration inserted a new node")
	}
}

func TestRepeatedRegistrationOfSameParentsSucceeds(t *testing.T) {
	graph := map[string]*Lineage{}
	register(t, graph, "A", nil)
	register(t, graph, "B", []string{"A"})

	if err := Register(graph, Dataset{Name: "B"}, []string{"A"}); err != nil {
		t.Fatalf("repeated Register(B, [A]): %v", err)
	}
	assertParents(t, graph, "B", []string{"A"})
}

func TestInvalidInputsReturnErrors(t *testing.T) {
	graph := map[string]*Lineage{}
	register(t, graph, "A", nil)

	if err := Register(nil, Dataset{Name: "A"}, nil); err == nil {
		t.Fatal("Register with nil graph succeeded, want error")
	}
	if err := Register(graph, Dataset{Name: ""}, nil); err == nil {
		t.Fatal("Register with empty name succeeded, want error")
	}
	if err := Register(graph, Dataset{Name: "B"}, []string{"missing"}); err == nil {
		t.Fatal("Register with unknown parent succeeded, want error")
	}
	if err := Register(graph, Dataset{Name: "B"}, []string{""}); err == nil {
		t.Fatal("Register with empty parent name succeeded, want error")
	}
	if len(graph) != 1 {
		t.Fatalf("invalid registrations changed graph: %+v", graph)
	}
}

func TestRootsStableOrder(t *testing.T) {
	graph := map[string]*Lineage{}
	register(t, graph, "zeta", nil)
	register(t, graph, "alpha", nil)
	register(t, graph, "mid", []string{"alpha"})

	if got := Roots(graph); !reflect.DeepEqual(got, []string{"alpha", "zeta"}) {
		t.Fatalf("Roots() = %v, want [alpha zeta]", got)
	}
}

func assertParents(t *testing.T, graph map[string]*Lineage, name string, want []string) {
	t.Helper()
	got := graph[name].Parents
	if len(got) == 0 && len(want) == 0 {
		return
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Parents(%q) = %v, want %v", name, got, want)
	}
}

func assertChildren(t *testing.T, graph map[string]*Lineage, name string, want []string) {
	t.Helper()
	got := graph[name].Children
	if len(got) == 0 && len(want) == 0 {
		return
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Children(%q) = %v, want %v", name, got, want)
	}
}

func snapshot(graph map[string]*Lineage) map[string][][]string {
	out := map[string][][]string{}
	for name, entry := range graph {
		out[name] = [][]string{append([]string(nil), entry.Parents...), append([]string(nil), entry.Children...)}
	}
	return out
}
