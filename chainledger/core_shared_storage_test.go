package chainledger

import (
	"reflect"
	"testing"
)

// sharedCDList builds the headline alias graph without going through Register:
// A and B are roots, C and D both depend on A and B. Every record is
// independent, but A.Children and B.Children share one backing array
// ([C D]), just as two slices produced by reslicing the same storage do.
func sharedCDList() (map[string]*Lineage, *Lineage, *Lineage, *Lineage, *Lineage) {
	shared := []string{"C", "D"}
	a := &Lineage{Dataset: "A", Children: shared}
	b := &Lineage{Dataset: "B", Children: shared}
	c := &Lineage{Dataset: "C", Parents: append([]string(nil), "A", "B")}
	d := &Lineage{Dataset: "D", Parents: append([]string(nil), "A", "B")}
	graph := map[string]*Lineage{"A": a, "B": b, "C": c, "D": d}
	return graph, a, b, c, d
}

// TestRegisterSharedChildrenStorageRepoint is the headline fix: replacing C's
// upstreams with only B must leave A.Children == [D], B.Children == [C D],
// D.Parents == [A B], and C's own downstream records untouched — even though
// A and B's downstream lists share one backing array.
func TestRegisterSharedChildrenStorageRepoint(t *testing.T) {
	graph, heldA, heldB, heldC, heldD := sharedCDList()

	if err := Register(graph, Dataset{Name: "C"}, []string{"B"}); err != nil {
		t.Fatalf("Register(C, [B]) unexpected error: %v", err)
	}
	assertLineageConsistent(t, graph)

	if got := graph["C"].Parents; !reflect.DeepEqual(got, []string{"B"}) {
		t.Errorf("C.Parents = %v, want [B]", got)
	}
	if got := graph["A"].Children; !reflect.DeepEqual(got, []string{"D"}) {
		t.Errorf("A.Children = %v, want [D]", got)
	}
	if got := graph["B"].Children; !reflect.DeepEqual(got, []string{"C", "D"}) {
		t.Errorf("B.Children = %v, want [C D] (must not lose C or duplicate D)", got)
	}
	if got := graph["D"].Parents; !reflect.DeepEqual(got, []string{"A", "B"}) {
		t.Errorf("D.Parents = %v, want [A B] (unrelated record must stay unchanged)", got)
	}
	if got := graph["C"].Children; len(got) != 0 {
		t.Errorf("C.Children = %v, want empty", got)
	}

	// Records the caller already held must read the same correct relations.
	if got := heldA.Children; !reflect.DeepEqual(got, []string{"D"}) {
		t.Errorf("held A record Children = %v, want [D]", got)
	}
	if got := heldB.Children; !reflect.DeepEqual(got, []string{"C", "D"}) {
		t.Errorf("held B record Children = %v, want [C D]", got)
	}
	if got := heldC.Parents; !reflect.DeepEqual(got, []string{"B"}) {
		t.Errorf("held C record Parents = %v, want [B]", got)
	}
	if got := heldD.Parents; !reflect.DeepEqual(got, []string{"A", "B"}) {
		t.Errorf("held D record Parents = %v, want [A B]", got)
	}
}

// TestRegisterSharedChildrenStorageKeepEdge checks that when the changed
// dataset keeps the edge into a list sharing storage with another node's list,
// neither list is mutated by in-place removal.
func TestRegisterSharedChildrenStorageKeepEdge(t *testing.T) {
	graph, _, heldB, _, heldD := sharedCDList()

	// Replace C's parents with [A] (A kept, B dropped).
	if err := Register(graph, Dataset{Name: "C"}, []string{"A"}); err != nil {
		t.Fatalf("Register(C, [A]) unexpected error: %v", err)
	}
	assertLineageConsistent(t, graph)
	if got := graph["A"].Children; !reflect.DeepEqual(got, []string{"C", "D"}) {
		t.Errorf("A.Children = %v, want [C D]", got)
	}
	if got := graph["B"].Children; !reflect.DeepEqual(got, []string{"D"}) {
		t.Errorf("B.Children = %v, want [D]", got)
	}
	if got := heldB.Children; !reflect.DeepEqual(got, []string{"D"}) {
		t.Errorf("held B record Children = %v, want [D]", got)
	}
	if got := heldD.Parents; !reflect.DeepEqual(got, []string{"A", "B"}) {
		t.Errorf("held D record Parents = %v, want [A B]", got)
	}
}

// TestRegisterOverlappingChildrenSlices covers the partial-overlap variant:
// two child lists share only an interval of their backing storage (different
// lengths and offsets into the same array).
func TestRegisterOverlappingChildrenSlices(t *testing.T) {
	// Backing array [C D E Z]: A's list is the [C D E] window and B's list
	// is the [D E] window, so the two lists overlap on [D E].
	backing := []string{"C", "D", "E", "Z"}
	a := &Lineage{Dataset: "A", Children: backing[:3]}  // [C D E]
	b := &Lineage{Dataset: "B", Children: backing[1:3]} // [D E]
	c := &Lineage{Dataset: "C", Parents: []string{"A"}}
	d := &Lineage{Dataset: "D", Parents: []string{"A", "B"}}
	e := &Lineage{Dataset: "E", Parents: []string{"A", "B"}}
	graph := map[string]*Lineage{"A": a, "B": b, "C": c, "D": d, "E": e}

	// C moves from A to B. A's list loses C, B's list gains C; the shared
	// [D E] window must survive in both with no corruption.
	if err := Register(graph, Dataset{Name: "C"}, []string{"B"}); err != nil {
		t.Fatalf("Register(C, [B]) unexpected error: %v", err)
	}
	assertLineageConsistent(t, graph)
	if got := graph["A"].Children; !reflect.DeepEqual(got, []string{"D", "E"}) {
		t.Errorf("A.Children = %v, want [D E]", got)
	}
	if got := graph["B"].Children; !reflect.DeepEqual(got, []string{"C", "D", "E"}) {
		t.Errorf("B.Children = %v, want [C D E]", got)
	}
	if got := graph["D"].Parents; !reflect.DeepEqual(got, []string{"A", "B"}) {
		t.Errorf("D.Parents = %v, want [A B]", got)
	}
	if got := graph["E"].Parents; !reflect.DeepEqual(got, []string{"A", "B"}) {
		t.Errorf("E.Parents = %v, want [A B]", got)
	}
}

// TestRegisterAddIntoListWithSpareCapacity covers adding an edge into a child
// list that carries spare capacity: a second record's list may share that
// tail storage, so appending into the spare capacity must never overwrite a
// name the other record still uses.
func TestRegisterAddIntoListWithSpareCapacity(t *testing.T) {
	// B.Children = [D] with two slots of spare capacity. A.Children aliases
	// the tail window of the SAME backing array ([Y Z]), so the graph is
	// consistent but an in-place append into B would overwrite A's names.
	backing := make([]string, 3, 3)
	backing[0] = "D"
	backing[1] = "Y"
	backing[2] = "Z"
	b := &Lineage{Dataset: "B", Children: backing[:1]}  // [D], sees the spare capacity
	a := &Lineage{Dataset: "A", Children: backing[1:3]} // [Y Z], same array
	c := &Lineage{Dataset: "C", Parents: nil}           // root so far
	d := &Lineage{Dataset: "D", Parents: []string{"B"}}
	y := &Lineage{Dataset: "Y", Parents: []string{"A"}}
	z := &Lineage{Dataset: "Z", Parents: []string{"A"}}
	graph := map[string]*Lineage{"A": a, "B": b, "C": c, "D": d, "Y": y, "Z": z}

	// C gains B as its only upstream: B.Children must become [C D] without
	// touching the [Y Z] names living in B's spare capacity (which A's list
	// is modeled as owning independently).
	if err := Register(graph, Dataset{Name: "C"}, []string{"B"}); err != nil {
		t.Fatalf("Register(C, [B]) unexpected error: %v", err)
	}
	assertLineageConsistent(t, graph)
	if got := graph["B"].Children; !reflect.DeepEqual(got, []string{"C", "D"}) {
		t.Errorf("B.Children = %v, want [C D]", got)
	}
	if got := graph["A"].Children; !reflect.DeepEqual(got, []string{"Y", "Z"}) {
		t.Errorf("A.Children = %v, want [Y Z] (spare-capacity write leaked)", got)
	}
}

// TestRegisterMixedChangeWithSharedStorage exercises one replacement that
// removes an old upstream, keeps an existing one, and adds a new one at the
// same time, with child lists sharing storage.
func TestRegisterMixedChangeWithSharedStorage(t *testing.T) {
	// A and X currently list [C D] sharing storage; B lists [D]. C depends
	// on A and X. Replace C's parents with [X B]: A loses C, X keeps C, B
	// gains C. D keeps depending on A, X, and B... set D on A, X, B.
	shared := []string{"C", "D"}
	a := &Lineage{Dataset: "A", Children: shared}
	x := &Lineage{Dataset: "X", Children: shared}
	b := &Lineage{Dataset: "B", Children: []string{"D"}}
	c := &Lineage{Dataset: "C", Parents: []string{"A", "X"}}
	d := &Lineage{Dataset: "D", Parents: []string{"A", "B", "X"}}
	graph := map[string]*Lineage{"A": a, "B": b, "C": c, "D": d, "X": x}

	if err := Register(graph, Dataset{Name: "C"}, []string{"B", "X", "X"}); err != nil {
		t.Fatalf("Register(C, [B X X]) unexpected error: %v", err)
	}
	assertLineageConsistent(t, graph)
	if got := graph["C"].Parents; !reflect.DeepEqual(got, []string{"B", "X"}) {
		t.Errorf("C.Parents = %v, want [B X]", got)
	}
	if got := graph["A"].Children; !reflect.DeepEqual(got, []string{"D"}) {
		t.Errorf("A.Children = %v, want [D]", got)
	}
	if got := graph["X"].Children; !reflect.DeepEqual(got, []string{"C", "D"}) {
		t.Errorf("X.Children = %v, want [C D]", got)
	}
	if got := graph["B"].Children; !reflect.DeepEqual(got, []string{"C", "D"}) {
		t.Errorf("B.Children = %v, want [C D]", got)
	}
	if got := graph["D"].Parents; !reflect.DeepEqual(got, []string{"A", "B", "X"}) {
		t.Errorf("D.Parents = %v, want [A B X] (unrelated record changed)", got)
	}
}

// TestRegisterNewDatasetIntoSharedChildLists covers registration of a brand
// new dataset when both upstreams' child lists share backing storage: A and B
// both currently list only D, with spare capacity behind the shared window.
// Both must independently end up listing [C D]; the second insertion must not
// see the first one's write.
func TestRegisterNewDatasetIntoSharedChildLists(t *testing.T) {
	backing := make([]string, 1, 3)
	backing[0] = "D"
	shared := backing[:1]
	a := &Lineage{Dataset: "A", Children: shared}
	b := &Lineage{Dataset: "B", Children: shared}
	d := &Lineage{Dataset: "D", Parents: []string{"A", "B"}}
	graph := map[string]*Lineage{"A": a, "B": b, "D": d}

	if err := Register(graph, Dataset{Name: "C"}, []string{"A", "B"}); err != nil {
		t.Fatalf("Register(C, [A B]) unexpected error: %v", err)
	}
	assertLineageConsistent(t, graph)
	if got := graph["A"].Children; !reflect.DeepEqual(got, []string{"C", "D"}) {
		t.Errorf("A.Children = %v, want [C D]", got)
	}
	if got := graph["B"].Children; !reflect.DeepEqual(got, []string{"C", "D"}) {
		t.Errorf("B.Children = %v, want [C D]", got)
	}
	if got := graph["D"].Parents; !reflect.DeepEqual(got, []string{"A", "B"}) {
		t.Errorf("D.Parents = %v, want [A B]", got)
	}
	if got := graph["C"].Parents; !reflect.DeepEqual(got, []string{"A", "B"}) {
		t.Errorf("C.Parents = %v, want [A B]", got)
	}
	// The rebuilt lists are independent storage now.
	graph["A"].Children = append(graph["A"].Children, "leak")
	if got := graph["B"].Children; !reflect.DeepEqual(got, []string{"C", "D"}) {
		t.Errorf("B.Children = %v, want [C D] after mutating A's list", got)
	}
}

// TestRegisterPreservesOwnDownstreamSharedStorage verifies that the changed
// dataset keeps its own downstream even when its child list shares storage
// with an unrelated record's list, and that other record is left intact.
func TestRegisterPreservesOwnDownstreamSharedStorage(t *testing.T) {
	// C and X both list child E through the same backing array window.
	backing := make([]string, 1, 3)
	backing[0] = "E"
	c := &Lineage{Dataset: "C", Parents: []string{"A", "B"}, Children: backing[:1]}
	x := &Lineage{Dataset: "X", Children: backing[:1]}
	e := &Lineage{Dataset: "E", Parents: []string{"C", "X"}}
	a := &Lineage{Dataset: "A", Children: []string{"C"}}
	b := &Lineage{Dataset: "B", Children: []string{"C"}}
	graph := map[string]*Lineage{"A": a, "B": b, "C": c, "E": e, "X": x}

	if err := Register(graph, Dataset{Name: "C"}, []string{"B"}); err != nil {
		t.Fatalf("Register(C, [B]) unexpected error: %v", err)
	}
	assertLineageConsistent(t, graph)
	if got := graph["A"].Children; len(got) != 0 {
		t.Errorf("A.Children = %v, want empty", got)
	}
	if got := graph["B"].Children; !reflect.DeepEqual(got, []string{"C"}) {
		t.Errorf("B.Children = %v, want [C]", got)
	}
	if got := graph["C"].Children; !reflect.DeepEqual(got, []string{"E"}) {
		t.Errorf("C.Children = %v, want [E] (own downstream preserved)", got)
	}
	if got := graph["X"].Children; !reflect.DeepEqual(got, []string{"E"}) {
		t.Errorf("X.Children = %v, want [E] (unrelated list untouched)", got)
	}
	if got := graph["E"].Parents; !reflect.DeepEqual(got, []string{"C", "X"}) {
		t.Errorf("E.Parents = %v, want [C X]", got)
	}
}

// TestRegisterRejectionWithSharedStorageAtomic verifies that a rejected
// registration leaves every record exactly as it was, including lists that
// share storage.
func TestRegisterRejectionWithSharedStorageAtomic(t *testing.T) {
	t.Run("missing upstream", func(t *testing.T) {
		graph, heldA, heldB, heldC, heldD := sharedCDList()
		err := Register(graph, Dataset{Name: "C"}, []string{"B", "ghost"})
		if err == nil {
			t.Fatal("expected error for missing upstream")
		}
		if got := graph["C"].Parents; !reflect.DeepEqual(got, []string{"A", "B"}) {
			t.Errorf("C.Parents = %v, want [A B]", got)
		}
		if got := heldC.Parents; !reflect.DeepEqual(got, []string{"A", "B"}) {
			t.Errorf("held C record Parents = %v, want [A B]", got)
		}
		if got := heldA.Children; !reflect.DeepEqual(got, []string{"C", "D"}) {
			t.Errorf("A.Children = %v, want [C D]", got)
		}
		if got := heldB.Children; !reflect.DeepEqual(got, []string{"C", "D"}) {
			t.Errorf("B.Children = %v, want [C D]", got)
		}
		if got := heldD.Parents; !reflect.DeepEqual(got, []string{"A", "B"}) {
			t.Errorf("D.Parents = %v, want [A B]", got)
		}
	})

	t.Run("indirect cycle", func(t *testing.T) {
		// A <- C, and C <- D; making A depend on C already handled elsewhere,
		// here: C depends on A,B and has child D depending on C. Registering A
		// with upstream D forms A -> D -> C -> A.
		graph, heldA, heldB, heldC, _ := sharedCDList()
		mustRegister(t, graph, "E", "C") // E depends on C
		err := Register(graph, Dataset{Name: "A"}, []string{"E"})
		if err == nil {
			t.Fatal("expected cycle error")
		}
		if got := heldA.Parents; len(got) != 0 {
			t.Errorf("A.Parents = %v, want still root", got)
		}
		if got := heldA.Children; !reflect.DeepEqual(got, []string{"C", "D"}) {
			t.Errorf("A.Children = %v, want [C D]", got)
		}
		if got := heldB.Children; !reflect.DeepEqual(got, []string{"C", "D"}) {
			t.Errorf("B.Children = %v, want [C D]", got)
		}
		if got := heldC.Parents; !reflect.DeepEqual(got, []string{"A", "B"}) {
			t.Errorf("C.Parents = %v, want [A B]", got)
		}
	})
}
