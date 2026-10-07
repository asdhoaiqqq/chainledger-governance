package chainledger

import (
	"reflect"
	"testing"
)

// The graphs in this file are hand-constructed with UNSORTED or DUPLICATED
// name lists. That is legal caller input: the relationships correspond, only
// the list hygiene is off. Register must still remove every copy of a dropped
// edge, add a gained edge exactly once, and leave the touched records' lists
// sorted and duplicate-free — without "cleaning up" records the registration
// does not involve.

// TestRegisterUnsortedDuplicateChildrenDetach is the headline case: C and D
// both depend on A, and A's downstream list is stored as [D C C]. Replacing
// C's upstreams with the empty list must make C a root, leave A listing
// exactly [D], and keep D depending on A — a binary search over the unsorted
// list would miss C or drop only one of its two copies.
func TestRegisterUnsortedDuplicateChildrenDetach(t *testing.T) {
	a := &Lineage{Dataset: "A", Children: []string{"D", "C", "C"}}
	c := &Lineage{Dataset: "C", Parents: []string{"A"}}
	d := &Lineage{Dataset: "D", Parents: []string{"A"}}
	graph := map[string]*Lineage{"A": a, "C": c, "D": d}

	if err := Register(graph, Dataset{Name: "C"}, nil); err != nil {
		t.Fatalf("Register(C, nil) unexpected error: %v", err)
	}
	assertLineageConsistent(t, graph)

	if got := graph["C"].Parents; len(got) != 0 {
		t.Errorf("C.Parents = %v, want empty (root)", got)
	}
	if got := graph["A"].Children; !reflect.DeepEqual(got, []string{"D"}) {
		t.Errorf("A.Children = %v, want [D] (every copy of C removed, D kept)", got)
	}
	if got := graph["D"].Parents; !reflect.DeepEqual(got, []string{"A"}) {
		t.Errorf("D.Parents = %v, want [A] (removing C must not lose D)", got)
	}
	// The record the caller held before the call shows the new relation.
	if got := a.Children; !reflect.DeepEqual(got, []string{"D"}) {
		t.Errorf("held A record Children = %v, want [D]", got)
	}
}

// TestRegisterUnsortedChildrenDetachKeepsOwnDownstream covers the same detach
// when the dropped edge sits behind a larger name in an unsorted list (a
// binary search would never find it), while C's own downstream survives.
func TestRegisterUnsortedChildrenDetachKeepsOwnDownstream(t *testing.T) {
	a := &Lineage{Dataset: "A", Children: []string{"D", "C"}}
	c := &Lineage{Dataset: "C", Parents: []string{"A"}, Children: []string{"E"}}
	d := &Lineage{Dataset: "D", Parents: []string{"A"}}
	e := &Lineage{Dataset: "E", Parents: []string{"C"}}
	graph := map[string]*Lineage{"A": a, "C": c, "D": d, "E": e}

	if err := Register(graph, Dataset{Name: "C"}, []string{}); err != nil {
		t.Fatalf("Register(C, []) unexpected error: %v", err)
	}
	assertLineageConsistent(t, graph)

	if got := graph["A"].Children; !reflect.DeepEqual(got, []string{"D"}) {
		t.Errorf("A.Children = %v, want [D]", got)
	}
	if got := graph["C"].Children; !reflect.DeepEqual(got, []string{"E"}) {
		t.Errorf("C.Children = %v, want [E] (own downstream preserved)", got)
	}
	if got := graph["E"].Parents; !reflect.DeepEqual(got, []string{"C"}) {
		t.Errorf("E.Parents = %v, want [C]", got)
	}
}

// TestRegisterAddIntoUnsortedDuplicateList checks the gain side: a new
// upstream whose stored downstream list is unsorted must end up sorted with
// the dataset listed exactly once, and a list that already (wrongly) holds
// duplicate names is normalized on the record that gains the edge.
func TestRegisterAddIntoUnsortedDuplicateList(t *testing.T) {
	b := &Lineage{Dataset: "B", Children: []string{"E", "D", "D"}}
	c := &Lineage{Dataset: "C"}
	d := &Lineage{Dataset: "D", Parents: []string{"B"}}
	e := &Lineage{Dataset: "E", Parents: []string{"B"}}
	graph := map[string]*Lineage{"B": b, "C": c, "D": d, "E": e}

	if err := Register(graph, Dataset{Name: "C"}, []string{"B"}); err != nil {
		t.Fatalf("Register(C, [B]) unexpected error: %v", err)
	}
	assertLineageConsistent(t, graph)

	if got := graph["B"].Children; !reflect.DeepEqual(got, []string{"C", "D", "E"}) {
		t.Errorf("B.Children = %v, want [C D E] (sorted, C exactly once)", got)
	}
	if got := graph["C"].Parents; !reflect.DeepEqual(got, []string{"B"}) {
		t.Errorf("C.Parents = %v, want [B]", got)
	}
}

// TestRegisterMixedChangeWithMessyLists exercises one replacement that drops
// an old upstream, keeps one, and adds one, where the dropped and added
// lists are messy but the kept and unrelated records must stay byte-for-byte
// as they were — Register must not tidy lists it has no relation change for.
func TestRegisterMixedChangeWithMessyLists(t *testing.T) {
	a := &Lineage{Dataset: "A", Children: []string{"D", "C", "C"}} // loses C
	x := &Lineage{Dataset: "X", Children: []string{"D", "C", "C"}} // keeps C: untouched
	b := &Lineage{Dataset: "B", Children: []string{"D"}}           // gains C
	u := &Lineage{Dataset: "U", Children: []string{"Z", "Z", "Y"}} // unrelated: untouched
	c := &Lineage{Dataset: "C", Parents: []string{"A", "X"}}
	d := &Lineage{Dataset: "D", Parents: []string{"A", "B", "X"}}
	y := &Lineage{Dataset: "Y", Parents: []string{"U"}}
	z := &Lineage{Dataset: "Z", Parents: []string{"U"}}
	graph := map[string]*Lineage{
		"A": a, "B": b, "C": c, "D": d, "U": u, "X": x, "Y": y, "Z": z,
	}

	if err := Register(graph, Dataset{Name: "C"}, []string{"X", "B"}); err != nil {
		t.Fatalf("Register(C, [X B]) unexpected error: %v", err)
	}

	if got := graph["C"].Parents; !reflect.DeepEqual(got, []string{"B", "X"}) {
		t.Errorf("C.Parents = %v, want [B X]", got)
	}
	if got := graph["A"].Children; !reflect.DeepEqual(got, []string{"D"}) {
		t.Errorf("A.Children = %v, want [D] (all copies of C removed)", got)
	}
	if got := graph["B"].Children; !reflect.DeepEqual(got, []string{"C", "D"}) {
		t.Errorf("B.Children = %v, want [C D] (C added exactly once)", got)
	}
	// Kept upstream and unrelated records keep their original contents,
	// duplicates and all — this registration has no relation change for them.
	if got := graph["X"].Children; !reflect.DeepEqual(got, []string{"D", "C", "C"}) {
		t.Errorf("X.Children = %v, want [D C C] unchanged (kept upstream)", got)
	}
	if got := graph["U"].Children; !reflect.DeepEqual(got, []string{"Z", "Z", "Y"}) {
		t.Errorf("U.Children = %v, want [Z Z Y] unchanged (unrelated record)", got)
	}
	if got := graph["D"].Parents; !reflect.DeepEqual(got, []string{"A", "B", "X"}) {
		t.Errorf("D.Parents = %v, want [A B X] unchanged", got)
	}
}

// TestRegisterMessyListsSharedStorage combines messy lists with shared
// backing storage: rewriting the dropped upstream's list must not shift the
// names another record's list reads from the same array.
func TestRegisterMessyListsSharedStorage(t *testing.T) {
	shared := []string{"D", "C", "C"}
	a := &Lineage{Dataset: "A", Children: shared}     // loses C
	x := &Lineage{Dataset: "X", Children: shared[:1]} // [D], unrelated to the change
	c := &Lineage{Dataset: "C", Parents: []string{"A"}}
	d := &Lineage{Dataset: "D", Parents: []string{"A", "X"}}
	graph := map[string]*Lineage{"A": a, "C": c, "D": d, "X": x}

	if err := Register(graph, Dataset{Name: "C"}, nil); err != nil {
		t.Fatalf("Register(C, nil) unexpected error: %v", err)
	}

	if got := graph["A"].Children; !reflect.DeepEqual(got, []string{"D"}) {
		t.Errorf("A.Children = %v, want [D]", got)
	}
	// X's list is a window into A's old backing array and is not part of the
	// change: it must still read exactly what it read before.
	if got := graph["X"].Children; !reflect.DeepEqual(got, []string{"D"}) {
		t.Errorf("X.Children = %v, want [D] (shared storage untouched)", got)
	}
	if got := graph["D"].Parents; !reflect.DeepEqual(got, []string{"A", "X"}) {
		t.Errorf("D.Parents = %v, want [A X]", got)
	}
}

// TestRegisterRejectionKeepsMessyListsAtomic verifies a rejected registration
// leaves every messy list exactly as it was — original order and duplicates —
// rather than normalizing first and failing later.
func TestRegisterRejectionKeepsMessyListsAtomic(t *testing.T) {
	t.Run("missing upstream", func(t *testing.T) {
		a := &Lineage{Dataset: "A", Children: []string{"D", "C", "C"}}
		c := &Lineage{Dataset: "C", Parents: []string{"A"}}
		d := &Lineage{Dataset: "D", Parents: []string{"A"}}
		graph := map[string]*Lineage{"A": a, "C": c, "D": d}

		err := Register(graph, Dataset{Name: "C"}, []string{"ghost"})
		if err == nil {
			t.Fatal("expected error for missing upstream")
		}
		if got := graph["A"].Children; !reflect.DeepEqual(got, []string{"D", "C", "C"}) {
			t.Errorf("A.Children = %v, want [D C C] unchanged after rejection", got)
		}
		if got := graph["C"].Parents; !reflect.DeepEqual(got, []string{"A"}) {
			t.Errorf("C.Parents = %v, want [A] unchanged after rejection", got)
		}
	})

	t.Run("cycle", func(t *testing.T) {
		a := &Lineage{Dataset: "A", Children: []string{"C", "C"}}
		c := &Lineage{Dataset: "C", Parents: []string{"A"}, Children: []string{"E", "E"}}
		e := &Lineage{Dataset: "E", Parents: []string{"C"}}
		graph := map[string]*Lineage{"A": a, "C": c, "E": e}

		// A depending on E would close A -> E -> C -> A... via C's parent A.
		err := Register(graph, Dataset{Name: "A"}, []string{"E"})
		if err == nil {
			t.Fatal("expected cycle error")
		}
		if got := graph["A"].Children; !reflect.DeepEqual(got, []string{"C", "C"}) {
			t.Errorf("A.Children = %v, want [C C] unchanged after rejection", got)
		}
		if got := graph["C"].Children; !reflect.DeepEqual(got, []string{"E", "E"}) {
			t.Errorf("C.Children = %v, want [E E] unchanged after rejection", got)
		}
		if got := graph["E"].Children; len(got) != 0 {
			t.Errorf("E.Children = %v, want empty after rejection", got)
		}
	})
}
