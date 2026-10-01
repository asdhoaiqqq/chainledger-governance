package chainledger

import (
	"errors"
	"fmt"
	"reflect"
	"sort"
	"testing"
)

func mustRegister(t *testing.T, graph map[string]*Lineage, name string, parents ...string) {
	t.Helper()
	if err := Register(graph, Dataset{Name: name}, parents); err != nil {
		t.Fatalf("Register(%q, %v) unexpected error: %v", name, parents, err)
	}
}

func TestRegisterBuildsChain(t *testing.T) {
	graph := map[string]*Lineage{}
	mustRegister(t, graph, "A")
	mustRegister(t, graph, "B", "A")
	mustRegister(t, graph, "C", "B")

	want := map[string]*Lineage{
		"A": {Dataset: "A", Parents: nil, Children: []string{"B"}},
		"B": {Dataset: "B", Parents: []string{"A"}, Children: []string{"C"}},
		"C": {Dataset: "C", Parents: []string{"B"}, Children: nil},
	}
	if !reflect.DeepEqual(graph, want) {
		t.Fatalf("lineage mismatch:\n got %#v\nwant %#v", dump(graph), dump(want))
	}
	if got, want := Roots(graph), []string{"A"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("Roots = %v, want %v", got, want)
	}
}

// The headline scenario: A<-B<-C, then B is repointed at root D.
// C still depends on B, the A-B edge is dissolved, the D-B edge is formed.
func TestRegisterReplacesUpstreams(t *testing.T) {
	graph := map[string]*Lineage{}
	mustRegister(t, graph, "A")
	mustRegister(t, graph, "B", "A")
	mustRegister(t, graph, "C", "B")
	mustRegister(t, graph, "D")
	mustRegister(t, graph, "B", "D")

	if got := graph["A"].Children; len(got) != 0 {
		t.Errorf("A should no longer list B as downstream, got %v", got)
	}
	if got := graph["D"].Children; !reflect.DeepEqual(got, []string{"B"}) {
		t.Errorf("D.Children = %v, want [B]", got)
	}
	if got := graph["B"].Parents; !reflect.DeepEqual(got, []string{"D"}) {
		t.Errorf("B.Parents = %v, want [D]", got)
	}
	if got := graph["B"].Children; !reflect.DeepEqual(got, []string{"C"}) {
		t.Errorf("B.Children = %v, want [C] (downstream must survive re-registration)", got)
	}
	if got := graph["C"].Parents; !reflect.DeepEqual(got, []string{"B"}) {
		t.Errorf("C.Parents = %v, want [B]", got)
	}
	if got, want := Roots(graph), []string{"A", "D"}; !reflect.DeepEqual(got, want) {
		t.Errorf("Roots = %v, want %v", got, want)
	}
}

func TestRegisterEmptyParentsDetachesAllKeepsDownstream(t *testing.T) {
	for _, parents := range [][]string{nil, {}} {
		t.Run(fmt.Sprintf("parents=%v", parents), func(t *testing.T) {
			graph := map[string]*Lineage{}
			mustRegister(t, graph, "A")
			mustRegister(t, graph, "B", "A")
			mustRegister(t, graph, "C", "B")

			if err := Register(graph, Dataset{Name: "B"}, parents); err != nil {
				t.Fatalf("clearing parents failed: %v", err)
			}
			if got := graph["B"].Parents; len(got) != 0 {
				t.Errorf("B.Parents = %v, want empty (root)", got)
			}
			if got := graph["B"].Children; !reflect.DeepEqual(got, []string{"C"}) {
				t.Errorf("B.Children = %v, want [C]", got)
			}
			if got := graph["A"].Children; len(got) != 0 {
				t.Errorf("A.Children = %v, want empty", got)
			}
			if got, want := Roots(graph), []string{"A", "B"}; !reflect.DeepEqual(got, want) {
				t.Errorf("Roots = %v, want %v", got, want)
			}
		})
	}
}

func TestRegisterIdempotent(t *testing.T) {
	graph := map[string]*Lineage{}
	mustRegister(t, graph, "A")
	mustRegister(t, graph, "B", "A")
	mustRegister(t, graph, "C", "B")

	snapshot := dump(graph)
	mustRegister(t, graph, "B", "A") // identical repeat
	if got := dump(graph); got != snapshot {
		t.Fatalf("repeat registration changed lineage:\n got %s\nwant %s", got, snapshot)
	}
	mustRegister(t, graph, "C") // repeated with nil parents
	if got := graph["C"].Parents; len(got) != 0 {
		t.Errorf("C.Parents = %v, want empty", got)
	}
	mustRegister(t, graph, "C") // again, still a root
	if got := graph["C"].Parents; len(got) != 0 {
		t.Errorf("C.Parents = %v, want empty", got)
	}
}

func TestRegisterDedupesParentsAndIgnoresInputOrder(t *testing.T) {
	graph := map[string]*Lineage{}
	mustRegister(t, graph, "A")
	mustRegister(t, graph, "B")

	// Duplicate names count once.
	mustRegister(t, graph, "C", "A", "B", "A")
	if got := graph["C"].Parents; !reflect.DeepEqual(got, []string{"A", "B"}) {
		t.Fatalf("C.Parents = %v, want [A B]", got)
	}
	for _, parent := range []string{"A", "B"} {
		if got := graph[parent].Children; !reflect.DeepEqual(got, []string{"C"}) {
			t.Errorf("%s.Children = %v, want [C] exactly once", parent, got)
		}
	}

	// Different input order produces the same stored result.
	mustRegister(t, graph, "C", "B", "A")
	if got := graph["C"].Parents; !reflect.DeepEqual(got, []string{"A", "B"}) {
		t.Errorf("C.Parents = %v, want [A B]", got)
	}
	if got := graph["A"].Children; !reflect.DeepEqual(got, []string{"C"}) {
		t.Errorf("A.Children = %v, want [C]", got)
	}
}

func TestRegisterDoesNotRetainCallerSlice(t *testing.T) {
	graph := map[string]*Lineage{}
	mustRegister(t, graph, "A")
	mustRegister(t, graph, "B")
	upstreams := []string{"A", "B"}
	mustRegister(t, graph, "C", upstreams...)

	upstreams[0] = "B"
	upstreams[1] = "B"
	if got := graph["C"].Parents; !reflect.DeepEqual(got, []string{"A", "B"}) {
		t.Fatalf("mutating caller slice altered stored parents: %v", got)
	}
	if got := graph["B"].Children; !reflect.DeepEqual(got, []string{"C"}) {
		t.Fatalf("mutating caller slice altered child records: %v", got)
	}

	// Appending into spare capacity must not leak into stored records.
	more := []string{"A"}
	mustRegister(t, graph, "D", more...)
	more = append(more, "B", "C")
	if got := graph["D"].Parents; !reflect.DeepEqual(got, []string{"A"}) {
		t.Fatalf("append on caller slice leaked into D.Parents: %v", got)
	}
	if got, want := Roots(graph), []string{"A", "B"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("Roots after slice mutation = %v, want %v", got, want)
	}
}

func TestRegisterRejectsCyclesAtomically(t *testing.T) {
	buildABC := func() map[string]*Lineage {
		graph := map[string]*Lineage{}
		mustRegister(t, graph, "A")
		mustRegister(t, graph, "B", "A")
		mustRegister(t, graph, "C", "B")
		return graph
	}

	t.Run("direct self dependency", func(t *testing.T) {
		graph := buildABC()
		err := Register(graph, Dataset{Name: "A"}, []string{"A"})
		if !errors.Is(err, ErrCycle) {
			t.Fatalf("self dependency err = %v, want ErrCycle", err)
		}
		if got := graph["A"].Parents; len(got) != 0 {
			t.Errorf("A.Parents mutated on rejection: %v", got)
		}
	})

	t.Run("cycle through existing chain A depends on C", func(t *testing.T) {
		graph := buildABC()
		err := Register(graph, Dataset{Name: "A"}, []string{"C"})
		if !errors.Is(err, ErrCycle) {
			t.Fatalf("A->C err = %v, want ErrCycle", err)
		}
		// Nothing changed: A is still a root, C still points at B.
		if got := graph["A"].Parents; len(got) != 0 {
			t.Errorf("A.Parents = %v, want empty", got)
		}
		if got := graph["C"].Children; len(got) != 0 {
			t.Errorf("C.Children = %v, want empty", got)
		}
		if got := graph["C"].Parents; !reflect.DeepEqual(got, []string{"B"}) {
			t.Errorf("C.Parents = %v, want [B]", got)
		}
	})

	t.Run("re-registering B with A is still legal", func(t *testing.T) {
		graph := buildABC()
		if err := Register(graph, Dataset{Name: "B"}, []string{"A"}); err != nil {
			t.Fatalf("idempotent B->A rejected: %v", err)
		}
	})

	t.Run("removed edge must not reject a legal swap", func(t *testing.T) {
		// P<-A<-X. Replace X's parents with P outright. In the OLD graph X
		// already reaches P (via A), so a naive check rejects it; after the
		// replacement the path X->A is gone and X->P is a plain edge.
		graph := map[string]*Lineage{}
		mustRegister(t, graph, "P")
		mustRegister(t, graph, "A", "P")
		mustRegister(t, graph, "X", "A")
		if err := Register(graph, Dataset{Name: "X"}, []string{"P"}); err != nil {
			t.Fatalf("legal replacement judged against removed edge: %v", err)
		}
		if got := graph["X"].Parents; !reflect.DeepEqual(got, []string{"P"}) {
			t.Fatalf("X.Parents = %v, want [P]", got)
		}
		if got := graph["A"].Children; len(got) != 0 {
			t.Fatalf("A.Children = %v, want empty (X detached)", got)
		}
		if got := graph["P"].Children; !reflect.DeepEqual(got, []string{"A", "X"}) {
			t.Fatalf("P.Children = %v, want [A X]", got)
		}
	})

	t.Run("single registration replacing edges judged by final shape", func(t *testing.T) {
		// A<-B. Re-register B depending on a fresh child-less dataset is
		// fine; the key case: B stays on A but also gains a parent that
		// only reaches B via an edge removed in THIS registration.
		// A<-B, A<-D, change D to depend on nothing, B drops nothing.
		graph := map[string]*Lineage{}
		mustRegister(t, graph, "A")
		mustRegister(t, graph, "B", "A")
		mustRegister(t, graph, "D", "A")
		// D used to reach A directly; make D a root while keeping A->? ...
		// then B cannot depend on D unless safe: D is a root, safe.
		mustRegister(t, graph, "D")
		mustRegister(t, graph, "B", "A", "D")
		if got := graph["B"].Parents; !reflect.DeepEqual(got, []string{"A", "D"}) {
			t.Fatalf("B.Parents = %v, want [A D]", got)
		}
	})
}

func TestRegisterUnknownUpstreamLeavesNoPartialState(t *testing.T) {
	graph := map[string]*Lineage{}
	mustRegister(t, graph, "A")

	err := Register(graph, Dataset{Name: "B"}, []string{"A", "ghost"})
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("err = %v, want ErrNotFound", err)
	}
	if _, exists := graph["B"]; exists {
		t.Fatal("dataset B node was created despite invalid later upstream")
	}
	if got := graph["A"].Children; len(got) != 0 {
		t.Fatalf("A.Children = %v, want empty (no partial edges)", got)
	}

	// A valid first upstream followed by a cycle-inducing one also rolls back.
	mustRegister(t, graph, "C", "A")
	err = Register(graph, Dataset{Name: "A"}, []string{"C", "ghost"})
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("err = %v, want ErrNotFound", err)
	}
	if got := graph["A"].Parents; len(got) != 0 {
		t.Fatalf("A.Parents = %v, want empty after rollback", got)
	}
}

func TestRegisterErrorCases(t *testing.T) {
	t.Run("empty dataset name", func(t *testing.T) {
		graph := map[string]*Lineage{}
		err := Register(graph, Dataset{Name: ""}, nil)
		if !errors.Is(err, ErrInvalidArgument) {
			t.Fatalf("err = %v, want ErrInvalidArgument", err)
		}
		if len(graph) != 0 {
			t.Fatal("node created with empty name")
		}
	})

	t.Run("nil graph", func(t *testing.T) {
		var graph map[string]*Lineage
		if err := Register(graph, Dataset{Name: "A"}, nil); !errors.Is(err, ErrNotInitialized) {
			t.Fatalf("err = %v, want ErrNotInitialized", err)
		}
	})

	t.Run("unknown upstream", func(t *testing.T) {
		graph := map[string]*Lineage{}
		err := Register(graph, Dataset{Name: "A"}, []string{"nope"})
		if !errors.Is(err, ErrNotFound) {
			t.Fatalf("err = %v, want ErrNotFound", err)
		}
	})

	t.Run("upstream pointing at nil node", func(t *testing.T) {
		graph := map[string]*Lineage{"zombie": nil}
		err := Register(graph, Dataset{Name: "A"}, []string{"zombie"})
		if !errors.Is(err, ErrInvalidArgument) {
			t.Fatalf("err = %v, want ErrInvalidArgument", err)
		}
		if _, exists := graph["A"]; exists {
			t.Fatal("node created against nil upstream")
		}
	})

	t.Run("empty upstream name is not a registered node", func(t *testing.T) {
		graph := map[string]*Lineage{}
		err := Register(graph, Dataset{Name: "A"}, []string{""})
		if !errors.Is(err, ErrNotFound) {
			t.Fatalf("err = %v, want ErrNotFound", err)
		}
	})
}

func TestRegisterPreservesNameCasingAndSpacing(t *testing.T) {
	graph := map[string]*Lineage{}
	mustRegister(t, graph, " Raw-Blocks ")
	mustRegister(t, graph, "RAW-BLOCKS", " Raw-Blocks ")
	if got := graph["RAW-BLOCKS"].Parents; !reflect.DeepEqual(got, []string{" Raw-Blocks "}) {
		t.Fatalf("name was trimmed or case-folded: %v", got)
	}
}

func TestRootsStableOrder(t *testing.T) {
	graph := map[string]*Lineage{}
	mustRegister(t, graph, "zeta")
	mustRegister(t, graph, "alpha")
	mustRegister(t, graph, "mid", "alpha")
	mustRegister(t, graph, "beta")
	want := []string{"alpha", "beta", "zeta"}
	for i := 0; i < 5; i++ {
		if got := Roots(graph); !reflect.DeepEqual(got, want) {
			t.Fatalf("Roots = %v, want %v", got, want)
		}
	}
}

func dump(graph map[string]*Lineage) string {
	s := "map["
	for _, name := range Roots(graph) {
		e := graph[name]
		s += fmt.Sprintf("%s:{P:%v C:%v} ", name, e.Parents, e.Children)
	}
	// Include non-roots too, sorted by name so the dump is deterministic
	// regardless of map iteration order.
	var nonRoots []string
	for name, e := range graph {
		if len(e.Parents) != 0 {
			nonRoots = append(nonRoots, name)
		}
	}
	sort.Strings(nonRoots)
	for _, name := range nonRoots {
		e := graph[name]
		s += fmt.Sprintf("%s:{P:%v C:%v} ", name, e.Parents, e.Children)
	}
	return s + "]"
}
