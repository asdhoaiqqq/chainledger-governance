package chainledger

import (
	"reflect"
	"strings"
	"testing"
)

func registerOK(t *testing.T, graph map[string]*Lineage, name string, parents []string) {
	t.Helper()
	if err := Register(graph, Dataset{Name: name}, parents); err != nil {
		t.Fatalf("Register(%q) failed: %v", name, err)
	}
}

// snapshot deep-copies every node's Parents/Children so failures can prove
// that no partial update happened.
func snapshot(graph map[string]*Lineage) map[string][2][]string {
	out := map[string][2][]string{}
	for name, e := range graph {
		out[name] = [2][]string{
			append([]string(nil), e.Parents...),
			append([]string(nil), e.Children...),
		}
	}
	return out
}

func TestRegisterBasicAndRoots(t *testing.T) {
	graph := map[string]*Lineage{}
	registerOK(t, graph, "raw", nil)
	registerOK(t, graph, "detail", []string{"raw"})
	registerOK(t, graph, "summary", []string{"detail"})

	if got, want := Roots(graph), []string{"raw"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("roots = %v, want %v", got, want)
	}
	if got, want := graph["raw"].Children, []string{"detail"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("raw children = %v, want %v", got, want)
	}
	if got, want := graph["detail"].Parents, []string{"raw"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("detail parents = %v, want %v", got, want)
	}
	if got, want := graph["detail"].Children, []string{"summary"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("detail children = %v, want %v", got, want)
	}
	if got, want := graph["summary"].Parents, []string{"detail"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("summary parents = %v, want %v", got, want)
	}
}

func TestReregisterReplacesParentsPreservesChildren(t *testing.T) {
	graph := map[string]*Lineage{}
	registerOK(t, graph, "raw", nil)
	registerOK(t, graph, "detail", []string{"raw"})
	registerOK(t, graph, "summary", []string{"detail"})
	registerOK(t, graph, "detail", []string{"raw"})

	if got, want := graph["detail"].Parents, []string{"raw"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("detail parents = %v, want %v", got, want)
	}
	if got, want := graph["detail"].Children, []string{"summary"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("detail children = %v, want %v", got, want)
	}
	if got, want := graph["summary"].Parents, []string{"detail"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("summary parents = %v, want %v", got, want)
	}
	if got, want := graph["raw"].Children, []string{"detail"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("raw children = %v, want %v", got, want)
	}
}

func TestReverseLinksUpdated(t *testing.T) {
	graph := map[string]*Lineage{}
	registerOK(t, graph, "a", nil)
	registerOK(t, graph, "b", nil)
	registerOK(t, graph, "c", []string{"a"})
	registerOK(t, graph, "c", []string{"b"})

	if got := graph["a"].Children; len(got) != 0 {
		t.Fatalf("a children = %v, want empty", got)
	}
	if got, want := graph["b"].Children, []string{"c"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("b children = %v, want %v", got, want)
	}
	if got, want := graph["c"].Parents, []string{"b"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("c parents = %v, want %v", got, want)
	}
}

func TestParentsDedupedByFirstOccurrence(t *testing.T) {
	graph := map[string]*Lineage{}
	registerOK(t, graph, "a", nil)
	registerOK(t, graph, "b", nil)
	registerOK(t, graph, "c", []string{"a", "b", "a", "b"})

	if got, want := graph["c"].Parents, []string{"a", "b"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("c parents = %v, want %v", got, want)
	}
	if got, want := graph["a"].Children, []string{"c"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("a children = %v, want %v", got, want)
	}
	if got, want := graph["b"].Children, []string{"c"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("b children = %v, want %v", got, want)
	}
}

func TestRepeatedRegistrationIsStable(t *testing.T) {
	graph := map[string]*Lineage{}
	registerOK(t, graph, "raw", nil)
	registerOK(t, graph, "detail", []string{"raw"})
	registerOK(t, graph, "summary", []string{"detail"})
	before := snapshot(graph)

	for i := 0; i < 3; i++ {
		registerOK(t, graph, "detail", []string{"raw"})
		if got := snapshot(graph); !reflect.DeepEqual(got, before) {
			t.Fatalf("graph changed on repeated registration #%d:\nbefore=%v\nafter=%v", i+1, before, got)
		}
	}
}

func TestCallerSliceMutationDoesNotAffectGraph(t *testing.T) {
	graph := map[string]*Lineage{}
	registerOK(t, graph, "a", nil)
	registerOK(t, graph, "b", nil)
	parents := []string{"a", "b"}
	registerOK(t, graph, "c", parents)

	parents[0] = "b"
	parents[1] = "a"
	parents = append(parents, "a")

	if got, want := graph["c"].Parents, []string{"a", "b"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("c parents = %v, want %v", got, want)
	}
}

func TestEmptyParentsMakesRoot(t *testing.T) {
	graph := map[string]*Lineage{}
	registerOK(t, graph, "raw", nil)
	registerOK(t, graph, "detail", []string{"raw"})
	registerOK(t, graph, "summary", []string{"detail"})
	registerOK(t, graph, "detail", nil)

	if got := graph["detail"].Parents; len(got) != 0 {
		t.Fatalf("detail parents = %v, want empty", got)
	}
	if got := graph["raw"].Children; len(got) != 0 {
		t.Fatalf("raw children = %v, want empty", got)
	}
	if got, want := graph["detail"].Children, []string{"summary"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("detail children = %v, want %v", got, want)
	}
	if got, want := Roots(graph), []string{"detail", "raw"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("roots = %v, want %v", got, want)
	}
}

func TestCycleSemantics(t *testing.T) {
	graph := map[string]*Lineage{}
	registerOK(t, graph, "raw", nil)
	registerOK(t, graph, "detail", []string{"raw"})
	registerOK(t, graph, "summary", []string{"detail"})

	// Re-registering detail with its real source raw must keep succeeding.
	registerOK(t, graph, "detail", []string{"raw"})

	// Making raw depend on summary reverses the real data flow: must fail.
	err := Register(graph, Dataset{Name: "raw"}, []string{"summary"})
	if err == nil {
		t.Fatal("expected cycle error when raw upstreams summary")
	}
	if !strings.Contains(err.Error(), "summary") {
		t.Fatalf("cycle error %q does not name summary", err)
	}

	// The refused registration changed nothing.
	if got := graph["raw"].Parents; len(got) != 0 {
		t.Fatalf("raw parents = %v, want empty", got)
	}
	if got, want := graph["raw"].Children, []string{"detail"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("raw children = %v, want %v", got, want)
	}
	if got, want := graph["detail"].Parents, []string{"raw"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("detail parents = %v, want %v", got, want)
	}
}

func TestCycleThroughMultipleUpstreamsAndLayers(t *testing.T) {
	graph := map[string]*Lineage{}
	registerOK(t, graph, "a", nil)
	registerOK(t, graph, "b", []string{"a"})
	registerOK(t, graph, "c", []string{"b"})
	registerOK(t, graph, "d", []string{"c"})

	// a -> b -> c -> d ; making a depend on d closes a multi-layer loop.
	err := Register(graph, Dataset{Name: "a"}, []string{"d"})
	if err == nil {
		t.Fatal("expected cycle error")
	}
	if !strings.Contains(err.Error(), "d") {
		t.Fatalf("error %q does not name d", err)
	}
	if got, want := graph["a"].Children, []string{"b"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("a children = %v, want %v", got, want)
	}
	if got, want := graph["c"].Children, []string{"d"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("c children = %v, want %v", got, want)
	}
	if len(graph["a"].Parents) != 0 {
		t.Fatalf("a parents = %v, want empty", graph["a"].Parents)
	}
}

func TestCycleWithMultipleUpstreams(t *testing.T) {
	graph := map[string]*Lineage{}
	registerOK(t, graph, "a", nil)
	registerOK(t, graph, "b", nil)
	registerOK(t, graph, "c", []string{"a", "b"})
	registerOK(t, graph, "d", []string{"c"})

	// Re-registering c with both a and b keeps the real flow and succeeds.
	registerOK(t, graph, "c", []string{"a", "b"})

	// Making a depend on d closes the loop through one of c's upstreams.
	if err := Register(graph, Dataset{Name: "a"}, []string{"d"}); err == nil {
		t.Fatal("expected cycle error")
	}
}

func TestValidationErrorsNameProblems(t *testing.T) {
	graph := map[string]*Lineage{}
	registerOK(t, graph, "a", nil)

	cases := []struct {
		name    string
		dataset Dataset
		parents []string
		want    string
	}{
		{"empty dataset name", Dataset{Name: ""}, []string{"a"}, "name"},
		{"empty upstream name", Dataset{Name: "x"}, []string{""}, "upstream"},
		{"unknown upstream", Dataset{Name: "x"}, []string{"ghost"}, "ghost"},
		{"self reference", Dataset{Name: "a"}, []string{"a"}, "a"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := Register(graph, tc.dataset, tc.parents)
			if err == nil {
				t.Fatalf("expected error containing %q", tc.want)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error %q does not contain %q", err, tc.want)
			}
		})
	}
}

func TestFirstProblemInInputOrder(t *testing.T) {
	graph := map[string]*Lineage{}
	registerOK(t, graph, "a", nil)
	registerOK(t, graph, "b", []string{"a"})

	// "ghost" is the first problem in input order even though a cycle-causing
	// parent follows it.
	err := Register(graph, Dataset{Name: "a"}, []string{"ghost", "b"})
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "ghost") {
		t.Fatalf("error %q should name ghost (first problem in input order)", err)
	}
}

func TestFailureIsAtomic(t *testing.T) {
	graph := map[string]*Lineage{}
	registerOK(t, graph, "a", nil)
	registerOK(t, graph, "b", []string{"a"})
	before := snapshot(graph)

	// A legal upstream precedes the unknown one: nothing may change.
	err := Register(graph, Dataset{Name: "c"}, []string{"a", "ghost"})
	if err == nil {
		t.Fatal("expected error")
	}
	if _, ok := graph["c"]; ok {
		t.Fatal("failed registration created node c")
	}
	if got := snapshot(graph); !reflect.DeepEqual(got, before) {
		t.Fatalf("graph changed after failed registration:\nbefore=%v\nafter=%v", before, got)
	}

	// After fixing the request, registration works normally.
	registerOK(t, graph, "c", []string{"a"})
	if got, want := graph["a"].Children, []string{"b", "c"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("a children = %v, want %v", got, want)
	}
	if got, want := graph["c"].Parents, []string{"a"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("c parents = %v, want %v", got, want)
	}
}

func TestFailedReregistrationKeepsLineage(t *testing.T) {
	graph := map[string]*Lineage{}
	registerOK(t, graph, "a", nil)
	registerOK(t, graph, "b", []string{"a"})
	registerOK(t, graph, "c", []string{"b"})
	before := snapshot(graph)

	// Re-registering b with an unknown upstream must leave everything intact.
	if err := Register(graph, Dataset{Name: "b"}, []string{"a", "ghost"}); err == nil {
		t.Fatal("expected error")
	}
	if got := snapshot(graph); !reflect.DeepEqual(got, before) {
		t.Fatalf("graph changed after failed re-registration:\nbefore=%v\nafter=%v", before, got)
	}
}

func TestDownstreamOrderPreserved(t *testing.T) {
	graph := map[string]*Lineage{}
	registerOK(t, graph, "raw", nil)
	registerOK(t, graph, "b", []string{"raw"})
	registerOK(t, graph, "a", []string{"raw"})
	registerOK(t, graph, "c", []string{"raw"})

	// Re-registering raw must not reorder its existing downstreams.
	registerOK(t, graph, "raw", nil)
	if got, want := graph["raw"].Children, []string{"b", "a", "c"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("raw children = %v, want %v", got, want)
	}

	// A brand-new downstream is appended at the end.
	registerOK(t, graph, "d", []string{"raw"})
	if got, want := graph["raw"].Children, []string{"b", "a", "c", "d"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("raw children = %v, want %v", got, want)
	}
}

func TestDemoScenario(t *testing.T) {
	graph := map[string]*Lineage{}
	registerOK(t, graph, "raw-blocks", nil)
	registerOK(t, graph, "transfers", []string{"raw-blocks"})
	registerOK(t, graph, "wallet-daily", []string{"transfers"})

	// The demo's last registration (raw-blocks depending on wallet-daily)
	// must be refused, and the three earlier datasets stay valid.
	if err := Register(graph, Dataset{Name: "raw-blocks"}, []string{"wallet-daily"}); err == nil {
		t.Fatal("expected refusal: raw-blocks cannot depend on wallet-daily")
	}
	if got, want := Roots(graph), []string{"raw-blocks"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("roots = %v, want %v", got, want)
	}
	if len(graph) != 3 {
		t.Fatalf("datasets = %d, want 3", len(graph))
	}
	if got, want := graph["raw-blocks"].Children, []string{"transfers"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("raw-blocks children = %v, want %v", got, want)
	}
	if got, want := graph["transfers"].Parents, []string{"raw-blocks"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("transfers parents = %v, want %v", got, want)
	}
	if got, want := graph["wallet-daily"].Parents, []string{"transfers"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("wallet-daily parents = %v, want %v", got, want)
	}
}
