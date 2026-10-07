package chainledger

import (
	"errors"
	"reflect"
	"testing"
)

// These tests pin the rule that a single Register changes relationships solely
// according to the submitted dataset name and its new upstreams. When a graph
// is assembled in memory, two Lineage records may legitimately point their
// Parents/Children slices at the same backing array (fully shared, partially
// overlapping windows, or windows with spare capacity). Editing one record's
// list must never write through such an alias into another record's list.

// sharedCDGraph builds the headline graph: A and B are roots, C and D both
// depend on A and B, and the two roots' downstream lists share ONE backing
// array [C D]. Every dataset still holds its own Lineage record.
func sharedCDGraph() map[string]*Lineage {
	shared := []string{"C", "D"}
	graph := map[string]*Lineage{
		"A": {Dataset: "A", Parents: nil, Children: shared},
		"B": {Dataset: "B", Parents: nil, Children: shared},
		"C": {Dataset: "C", Parents: []string{"A", "B"}, Children: nil},
		"D": {Dataset: "D", Parents: []string{"A", "B"}, Children: nil},
	}
	return graph
}

func TestRegisterSharedDownstreamStorageRepointOne(t *testing.T) {
	graph := sharedCDGraph()
	heldA, heldB, heldC, heldD := graph["A"], graph["B"], graph["C"], graph["D"]

	// Replace C's upstreams with only B: edge A->C dissolved, B->C kept,
	// everything else untouched.
	if err := Register(graph, Dataset{Name: "C"}, []string{"B"}); err != nil {
		t.Fatalf("Register(C,[B]) unexpected error: %v", err)
	}
	assertLineageConsistent(t, graph)

	checks := []struct {
		entry       *Lineage
		wantParents []string
		wantChild   []string
	}{
		{heldA, nil, []string{"D"}},
		{heldB, nil, []string{"C", "D"}},
		{heldC, []string{"B"}, nil},
		{heldD, []string{"A", "B"}, nil},
	}
	for _, c := range checks {
		if got := c.entry.Parents; !reflect.DeepEqual(got, c.wantParents) {
			t.Errorf("%s.Parents = %v, want %v", c.entry.Dataset, got, c.wantParents)
		}
		if got := c.entry.Children; !reflect.DeepEqual(got, c.wantChild) {
			t.Errorf("%s.Children = %v, want %v", c.entry.Dataset, got, c.wantChild)
		}
	}
}

func TestRegisterSharedDownstreamStorageAddViaSpareCapacity(t *testing.T) {
	// B's child list is a one-element window with spare capacity into the
	// same array A's window lives in. Appending B's new child in place would
	// overwrite the slot A reads.
	backing := make([]string, 4)
	backing[0] = "C"
	backing[1] = "Z"
	graph := map[string]*Lineage{
		"B": {Dataset: "B", Children: backing[0:1]}, // [C], cap 4
		"A": {Dataset: "A", Children: backing[1:2]}, // [Z]
		"C": {Dataset: "C", Parents: []string{"B"}},
		"Z": {Dataset: "Z", Parents: []string{"A"}},
	}
	heldA, heldZ := graph["A"], graph["Z"]

	mustRegister(t, graph, "D", "B") // adds B->D
	assertLineageConsistent(t, graph)

	if got := graph["B"].Children; !reflect.DeepEqual(got, []string{"C", "D"}) {
		t.Errorf("B.Children = %v, want [C D]", got)
	}
	if got := heldA.Children; !reflect.DeepEqual(got, []string{"Z"}) {
		t.Errorf("A.Children corrupted through shared capacity: %v, want [Z]", got)
	}
	if got := heldZ.Parents; !reflect.DeepEqual(got, []string{"A"}) {
		t.Errorf("Z.Parents = %v, want [A]", got)
	}
}

func TestRegisterPartiallyOverlappingWindows(t *testing.T) {
	// Two windows overlap only in their middle:
	// backing = [X Y Z], A sees [X Y], B sees [Y Z].
	backing := []string{"X", "Y", "Z"}
	graph := map[string]*Lineage{
		"A": {Dataset: "A", Children: backing[0:2]}, // [X Y]
		"B": {Dataset: "B", Children: backing[1:3]}, // [Y Z]
		"X": {Dataset: "X", Parents: []string{"A"}},
		"Y": {Dataset: "Y", Parents: []string{"A", "B"}},
		"Z": {Dataset: "Z", Parents: []string{"B"}},
	}
	heldX, heldY, heldZ, heldB := graph["X"], graph["Y"], graph["Z"], graph["B"]

	// Repoint Y at B alone: A drops Y, B keeps it; X and Z are unrelated.
	if err := Register(graph, Dataset{Name: "Y"}, []string{"B"}); err != nil {
		t.Fatalf("Register(Y,[B]) unexpected error: %v", err)
	}
	assertLineageConsistent(t, graph)

	if got := graph["A"].Children; !reflect.DeepEqual(got, []string{"X"}) {
		t.Errorf("A.Children = %v, want [X]", got)
	}
	if got := heldB.Children; !reflect.DeepEqual(got, []string{"Y", "Z"}) {
		t.Errorf("B.Children = %v, want [Y Z]", got)
	}
	if got := heldX.Parents; !reflect.DeepEqual(got, []string{"A"}) {
		t.Errorf("X.Parents = %v, want [A]", got)
	}
	if got := heldY.Parents; !reflect.DeepEqual(got, []string{"B"}) {
		t.Errorf("Y.Parents = %v, want [B]", got)
	}
	if got := heldZ.Parents; !reflect.DeepEqual(got, []string{"B"}) {
		t.Errorf("Z.Parents = %v, want [B] (unrelated record changed)", got)
	}
}

func TestRegisterSharedStorageRemovesKeepsAndAddsTogether(t *testing.T) {
	graph := sharedCDGraph()
	mustRegister(t, graph, "E") // new root the replacement will point C at

	// One registration removes A, keeps B, and adds E for C.
	if err := Register(graph, Dataset{Name: "C"}, []string{"B", "E"}); err != nil {
		t.Fatalf("Register(C,[B E]) unexpected error: %v", err)
	}
	assertLineageConsistent(t, graph)

	if got := graph["C"].Parents; !reflect.DeepEqual(got, []string{"B", "E"}) {
		t.Errorf("C.Parents = %v, want [B E]", got)
	}
	if got := graph["A"].Children; !reflect.DeepEqual(got, []string{"D"}) {
		t.Errorf("A.Children = %v, want [D]", got)
	}
	if got := graph["B"].Children; !reflect.DeepEqual(got, []string{"C", "D"}) {
		t.Errorf("B.Children = %v, want [C D], not lost C or duplicated D", got)
	}
	if got := graph["E"].Children; !reflect.DeepEqual(got, []string{"C"}) {
		t.Errorf("E.Children = %v, want [C] once", got)
	}
	if got := graph["D"].Parents; !reflect.DeepEqual(got, []string{"A", "B"}) {
		t.Errorf("D.Parents = %v, want [A B] (D's sources must not be rewritten)", got)
	}
}

func TestRegisterSharedStorageKeepsDownstreamOfRepointedDataset(t *testing.T) {
	// C itself has a downstream; C.Children and X.Children share one backing
	// array, and C's window even has spare capacity into X's slot.
	rootShared := []string{"C", "D"}
	kids := []string{"G", "Q"}
	graph := map[string]*Lineage{
		"A": {Dataset: "A", Children: rootShared},
		"B": {Dataset: "B", Children: rootShared},
		"C": {Dataset: "C", Parents: []string{"A", "B"}, Children: kids[0:1]}, // [G], cap 2
		"D": {Dataset: "D", Parents: []string{"A", "B"}},
		"G": {Dataset: "G", Parents: []string{"C"}},
		"X": {Dataset: "X", Children: kids[1:2]}, // [Q]
		"Q": {Dataset: "Q", Parents: []string{"X"}},
	}

	if err := Register(graph, Dataset{Name: "C"}, []string{"B"}); err != nil {
		t.Fatalf("Register(C,[B]) unexpected error: %v", err)
	}
	assertLineageConsistent(t, graph)

	if got := graph["C"].Children; !reflect.DeepEqual(got, []string{"G"}) {
		t.Errorf("C.Children = %v, want [G] (own downstream preserved)", got)
	}
	if got := graph["X"].Children; !reflect.DeepEqual(got, []string{"Q"}) {
		t.Errorf("X.Children = %v, want [Q] (shared window disturbed)", got)
	}
}

func TestRegisterSharedStorageRejectionLeavesAliasesIntact(t *testing.T) {
	t.Run("unknown upstream", func(t *testing.T) {
		graph := sharedCDGraph()
		err := Register(graph, Dataset{Name: "C"}, []string{"B", "ghost"})
		if !errors.Is(err, ErrNotFound) {
			t.Fatalf("err = %v, want ErrNotFound", err)
		}
		assertLineageConsistent(t, graph)
		if got := graph["A"].Children; !reflect.DeepEqual(got, []string{"C", "D"}) {
			t.Errorf("A.Children = %v, want [C D] after rejection", got)
		}
		if got := graph["B"].Children; !reflect.DeepEqual(got, []string{"C", "D"}) {
			t.Errorf("B.Children = %v, want [C D] after rejection", got)
		}
		if got := graph["C"].Parents; !reflect.DeepEqual(got, []string{"A", "B"}) {
			t.Errorf("C.Parents = %v, want [A B] after rejection", got)
		}
		if got := graph["D"].Parents; !reflect.DeepEqual(got, []string{"A", "B"}) {
			t.Errorf("D.Parents = %v, want [A B] after rejection", got)
		}
	})

	t.Run("indirect cycle", func(t *testing.T) {
		// C and D both depend on A and B (shared root lists); make A depend
		// on C would close A->C->A.
		graph := sharedCDGraph()
		err := Register(graph, Dataset{Name: "A"}, []string{"C"})
		if !errors.Is(err, ErrCycle) {
			t.Fatalf("err = %v, want ErrCycle", err)
		}
		assertLineageConsistent(t, graph)
		if got := graph["A"].Parents; len(got) != 0 {
			t.Errorf("A.Parents = %v, want root after rejection", got)
		}
		if got := graph["B"].Children; !reflect.DeepEqual(got, []string{"C", "D"}) {
			t.Errorf("B.Children = %v, want [C D] after rejection", got)
		}
	})
}
