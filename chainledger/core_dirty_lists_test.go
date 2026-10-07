package chainledger

import (
	"errors"
	"reflect"
	"testing"
)

// This file pins Register's behavior on graphs the caller built by hand, where
// every dataset and direct relationship is legal but a stored name list may be
// unsorted or repeat a name. A successful registration must make exactly the
// records whose relationships change converge on sorted, duplicate-free lists,
// while untouched records keep their prior contents (order and repetition
// included), shared backing storage must never leak between records, and a
// rejected registration must leave everything byte-for-byte as it was.

// dirtyHeadlineGraph builds the headline example without going through
// Register: C and D both depend on A, but A's stored downstream list is the
// unsorted, duplicate-bearing [D C C]. E depends on C, so C has downstream of
// its own that must survive C becoming a root.
func dirtyHeadlineGraph() (map[string]*Lineage, *Lineage, *Lineage, *Lineage, *Lineage) {
	a := &Lineage{Dataset: "A", Children: []string{"D", "C", "C"}}
	c := &Lineage{Dataset: "C", Parents: []string{"A"}}
	d := &Lineage{Dataset: "D", Parents: []string{"A"}}
	e := &Lineage{Dataset: "E", Parents: []string{"C"}}
	c.Children = []string{"E"}
	graph := map[string]*Lineage{"A": a, "C": c, "D": d, "E": e}
	return graph, a, c, d, e
}

// TestRegisterDirtyListDetachAll is the headline fix: replacing C's upstreams
// with an empty list makes C a root, A's downstream must be exactly [D] (both
// stale C occurrences gone), D keeps depending on A, and C's own downstream E
// is untouched.
func TestRegisterDirtyListDetachAll(t *testing.T) {
	graph, heldA, heldC, heldD, heldE := dirtyHeadlineGraph()

	if err := Register(graph, Dataset{Name: "C"}, nil); err != nil {
		t.Fatalf("Register(C, []) unexpected error: %v", err)
	}
	assertLineageConsistent(t, graph)

	if got := graph["C"].Parents; len(got) != 0 {
		t.Errorf("C.Parents = %v, want empty (root)", got)
	}
	if got := graph["A"].Children; !reflect.DeepEqual(got, []string{"D"}) {
		t.Errorf("A.Children = %v, want [D] (both stale C occurrences removed)", got)
	}
	if got := graph["D"].Parents; !reflect.DeepEqual(got, []string{"A"}) {
		t.Errorf("D.Parents = %v, want [A] (D must not be lost with C)", got)
	}
	if got := graph["C"].Children; !reflect.DeepEqual(got, []string{"E"}) {
		t.Errorf("C.Children = %v, want [E] (own downstream preserved)", got)
	}
	if got := graph["E"].Parents; !reflect.DeepEqual(got, []string{"C"}) {
		t.Errorf("E.Parents = %v, want [C]", got)
	}

	// Records the caller already held read the corrected relations.
	if got := heldA.Children; !reflect.DeepEqual(got, []string{"D"}) {
		t.Errorf("held A record Children = %v, want [D]", got)
	}
	if got := heldC.Parents; len(got) != 0 {
		t.Errorf("held C record Parents = %v, want empty", got)
	}
	if got := heldD.Parents; !reflect.DeepEqual(got, []string{"A"}) {
		t.Errorf("held D record Parents = %v, want [A]", got)
	}
	if got := heldE.Parents; !reflect.DeepEqual(got, []string{"C"}) {
		t.Errorf("held E record Parents = %v, want [C]", got)
	}
}

// TestRegisterDirtyListMixedChange exercises one replacement that removes an
// old upstream, keeps one, and adds a new one while the stored lists are
// unsorted and carry duplicates. The dropped upstream must lose EVERY copy of
// the dataset, the new upstream must list it exactly once alongside all its
// other downstreams, and the kept upstream's record is left exactly as stored.
func TestRegisterDirtyListMixedChange(t *testing.T) {
	// C depends on A (dropped) and X (kept) and gains B. A.Children is the
	// unsorted [D C C D2]-shaped list below; X.Children is a sorted-but-dirty
	// [C C D]; B starts with [D]. D and D2 keep depending on A/X as given.
	a := &Lineage{Dataset: "A", Children: []string{"D2", "C", "D", "C"}}
	x := &Lineage{Dataset: "X", Children: []string{"C", "C", "D"}}
	b := &Lineage{Dataset: "B", Children: []string{"D"}}
	c := &Lineage{Dataset: "C", Parents: []string{"X", "A", "X"}}
	d := &Lineage{Dataset: "D", Parents: []string{"A", "B", "X"}}
	d2 := &Lineage{Dataset: "D2", Parents: []string{"A"}}
	graph := map[string]*Lineage{"A": a, "B": b, "C": c, "D": d, "D2": d2, "X": x}
	heldX, heldD := x, d

	if err := Register(graph, Dataset{Name: "C"}, []string{"B", "X", "B"}); err != nil {
		t.Fatalf("Register(C, [B X B]) unexpected error: %v", err)
	}
	// No graph-wide consistency check here on purpose: X is a KEPT upstream,
	// so its pre-existing duplicate [C C D] must survive verbatim — a global
	// normalized-list assertion would demand it be tidied, which the
	// registration is explicitly forbidden to do. The changed records A and B
	// are checked for exact contents and correspondence below.

	if got := graph["C"].Parents; !reflect.DeepEqual(got, []string{"B", "X"}) {
		t.Errorf("C.Parents = %v, want [B X]", got)
	}
	if got := graph["A"].Children; !reflect.DeepEqual(got, []string{"D", "D2"}) {
		t.Errorf("A.Children = %v, want [D D2] (all C occurrences removed, rest kept)", got)
	}
	if got := graph["B"].Children; !reflect.DeepEqual(got, []string{"C", "D"}) {
		t.Errorf("B.Children = %v, want [C D] (C added exactly once, D kept)", got)
	}
	// Changed records must keep exact two-way correspondence.
	for parent, wantCount := range map[string]int{"A": 0, "B": 1} {
		count := 0
		for _, child := range graph[parent].Children {
			if child == "C" {
				count++
			}
			if graph[child] == nil {
				t.Errorf("%s.Children references absent dataset %q", parent, child)
			}
		}
		if count != wantCount {
			t.Errorf("%s lists C %d times, want %d", parent, count, wantCount)
		}
	}
	// X is a KEPT upstream: its record participates in no edge change, so its
	// pre-existing duplicate content is deliberately not tidied.
	if got := graph["X"].Children; !reflect.DeepEqual(got, []string{"C", "C", "D"}) {
		t.Errorf("X.Children = %v, want untouched [C C D]", got)
	}
	if got := heldX.Children; !reflect.DeepEqual(got, []string{"C", "C", "D"}) {
		t.Errorf("held X record Children = %v, want untouched [C C D]", got)
	}
	// D is unrelated to this registration: parents preserved verbatim.
	if got := graph["D"].Parents; !reflect.DeepEqual(got, []string{"A", "B", "X"}) {
		t.Errorf("D.Parents = %v, want [A B X]", got)
	}
	if got := heldD.Parents; !reflect.DeepEqual(got, []string{"A", "B", "X"}) {
		t.Errorf("held D record Parents = %v, want [A B X]", got)
	}
	if got := graph["D2"].Parents; !reflect.DeepEqual(got, []string{"A"}) {
		t.Errorf("D2.Parents = %v, want [A]", got)
	}
}

// TestRegisterDirtyOwnChildrenNormalized verifies that the changed dataset's
// own unsorted, duplicate-bearing downstream list is normalized as part of the
// successful commit without losing any distinct downstream.
func TestRegisterDirtyOwnChildrenNormalized(t *testing.T) {
	a := &Lineage{Dataset: "A", Children: []string{"C"}}
	b := &Lineage{Dataset: "B"}
	c := &Lineage{Dataset: "C", Parents: []string{"A"}, Children: []string{"E2", "E", "E"}}
	e := &Lineage{Dataset: "E", Parents: []string{"C"}}
	e2 := &Lineage{Dataset: "E2", Parents: []string{"C"}}
	graph := map[string]*Lineage{"A": a, "B": b, "C": c, "E": e, "E2": e2}

	if err := Register(graph, Dataset{Name: "C"}, []string{"B"}); err != nil {
		t.Fatalf("Register(C, [B]) unexpected error: %v", err)
	}
	assertLineageConsistent(t, graph)
	if got := graph["C"].Children; !reflect.DeepEqual(got, []string{"E", "E2"}) {
		t.Errorf("C.Children = %v, want [E E2] normalized, no loss", got)
	}
	if got := graph["A"].Children; len(got) != 0 {
		t.Errorf("A.Children = %v, want empty", got)
	}
	if got := graph["B"].Children; !reflect.DeepEqual(got, []string{"C"}) {
		t.Errorf("B.Children = %v, want [C]", got)
	}
}

// TestRegisterDirtyListsWithSharedStorage combines both hazards: the dropped
// and the new upstream's child lists share one backing array, and that array
// is unsorted with the moved dataset repeated. Neither rewrite may see the
// other's write, and all stale copies must disappear.
func TestRegisterDirtyListsWithSharedStorage(t *testing.T) {
	// A.Children and B.Children both window the same unsorted backing array
	// [D C C]; C currently depends on A only and moves to B.
	shared := []string{"D", "C", "C"}
	a := &Lineage{Dataset: "A", Children: shared}
	b := &Lineage{Dataset: "B", Children: shared}
	c := &Lineage{Dataset: "C", Parents: []string{"A"}}
	d := &Lineage{Dataset: "D", Parents: []string{"A", "B"}}
	graph := map[string]*Lineage{"A": a, "B": b, "C": c, "D": d}

	if err := Register(graph, Dataset{Name: "C"}, []string{"B"}); err != nil {
		t.Fatalf("Register(C, [B]) unexpected error: %v", err)
	}
	assertLineageConsistent(t, graph)
	if got := graph["A"].Children; !reflect.DeepEqual(got, []string{"D"}) {
		t.Errorf("A.Children = %v, want [D]", got)
	}
	if got := graph["B"].Children; !reflect.DeepEqual(got, []string{"C", "D"}) {
		t.Errorf("B.Children = %v, want [C D]", got)
	}
	if got := graph["D"].Parents; !reflect.DeepEqual(got, []string{"A", "B"}) {
		t.Errorf("D.Parents = %v, want [A B]", got)
	}
	// The rebuilt lists are independent.
	graph["A"].Children = append(graph["A"].Children, "leak")
	if got := graph["B"].Children; !reflect.DeepEqual(got, []string{"C", "D"}) {
		t.Errorf("B.Children = %v, want [C D] after mutating A's list", got)
	}
}

// TestRegisterDirtyListRejectionAtomic verifies that a rejected registration
// leaves the original unsorted/duplicated contents exactly as they were: the
// lists must not be normalized before the failing check, and no edge changes
// may survive.
func TestRegisterDirtyListRejectionAtomic(t *testing.T) {
	t.Run("missing upstream keeps order and duplicates", func(t *testing.T) {
		graph, heldA, heldC, heldD, _ := dirtyHeadlineGraph()
		err := Register(graph, Dataset{Name: "C"}, []string{"A", "ghost"})
		if !errors.Is(err, ErrNotFound) {
			t.Fatalf("err = %v, want ErrNotFound", err)
		}
		if got := heldA.Children; !reflect.DeepEqual(got, []string{"D", "C", "C"}) {
			t.Errorf("A.Children = %v, want untouched [D C C]", got)
		}
		if got := heldC.Parents; !reflect.DeepEqual(got, []string{"A"}) {
			t.Errorf("C.Parents = %v, want untouched [A]", got)
		}
		if got := heldD.Parents; !reflect.DeepEqual(got, []string{"A"}) {
			t.Errorf("D.Parents = %v, want untouched [A]", got)
		}
	})

	t.Run("indirect cycle keeps order and duplicates", func(t *testing.T) {
		graph, heldA, heldC, _, _ := dirtyHeadlineGraph()
		// Making A depend on E (which depends on C, which depends on A)
		// would close A -> E -> C -> A.
		err := Register(graph, Dataset{Name: "A"}, []string{"E"})
		if !errors.Is(err, ErrCycle) {
			t.Fatalf("err = %v, want ErrCycle", err)
		}
		if got := heldA.Children; !reflect.DeepEqual(got, []string{"D", "C", "C"}) {
			t.Errorf("A.Children = %v, want untouched [D C C]", got)
		}
		if got := heldA.Parents; len(got) != 0 {
			t.Errorf("A.Parents = %v, want still empty", got)
		}
		if got := heldC.Parents; !reflect.DeepEqual(got, []string{"A"}) {
			t.Errorf("C.Parents = %v, want untouched [A]", got)
		}
		if got := heldC.Children; !reflect.DeepEqual(got, []string{"E"}) {
			t.Errorf("C.Children = %v, want untouched [E]", got)
		}
	})
}

// TestRegisterDirtyListCallerSliceIndependence verifies that a duplicate-bearing
// submitted upstream slice still means one relationship and stays independent
// of the stored graph.
func TestRegisterDirtyListCallerSliceIndependence(t *testing.T) {
	a := &Lineage{Dataset: "A", Children: []string{"Z", "Z"}}
	z := &Lineage{Dataset: "Z", Parents: []string{"A"}}
	graph := map[string]*Lineage{"A": a, "Z": z}

	submitted := []string{"A", "A"}
	if err := Register(graph, Dataset{Name: "C"}, submitted); err != nil {
		t.Fatalf("Register(C, [A A]) unexpected error: %v", err)
	}
	assertLineageConsistent(t, graph)
	if got := graph["C"].Parents; !reflect.DeepEqual(got, []string{"A"}) {
		t.Errorf("C.Parents = %v, want [A] once", got)
	}
	if got := graph["A"].Children; !reflect.DeepEqual(got, []string{"C", "Z"}) {
		t.Errorf("A.Children = %v, want [C Z] normalized on this record", got)
	}

	// Mutating the caller's slice after success changes nothing.
	submitted[0] = "Z"
	submitted[1] = "Z"
	if got := graph["C"].Parents; !reflect.DeepEqual(got, []string{"A"}) {
		t.Errorf("C.Parents = %v, want [A] after caller mutation", got)
	}
}
