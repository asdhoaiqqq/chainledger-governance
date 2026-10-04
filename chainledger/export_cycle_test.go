package chainledger

import (
	"bytes"
	"errors"
	"reflect"
	"strings"
	"testing"
)

// cyclicGraph builds a graph that already contains a dependency cycle; such a
// graph cannot be produced through Register, so the Lineage nodes are assembled
// directly, the way pre-existing in-memory corruption looks.
func cyclicGraph(t *testing.T, parents map[string][]string) map[string]*Lineage {
	t.Helper()
	graph := make(map[string]*Lineage, len(parents))
	for name := range parents {
		graph[name] = &Lineage{Dataset: name, Parents: append([]string(nil), parents[name]...)}
	}
	for name, ups := range parents {
		for _, up := range ups {
			graph[up].Children = append(graph[up].Children, name)
		}
	}
	for name := range graph {
		graph[name].Parents = append([]string(nil), parents[name]...)
		graph[name].Children = uniqueSorted(graph[name].Children)
	}
	return graph
}

// TestMarshalRejectsCycles verifies that exporting a graph with a dependency
// cycle returns no JSON and an error wrapping ErrCycle that names the datasets
// on the cycle. Self-dependencies and cycles of two or more nodes are all
// covered, including a three-node cycle whose references all resolve.
func TestMarshalRejectsCycles(t *testing.T) {
	cases := map[string]struct {
		parents map[string][]string
		wantOn  []string // every name must appear in the cycle message
	}{
		"direct self dependency": {
			map[string][]string{"A": {"A"}},
			[]string{"A"},
		},
		"two node cycle": {
			map[string][]string{"A": {"B"}, "B": {"A"}},
			[]string{"A", "B"},
		},
		"three node cycle with all references present": {
			map[string][]string{"A": {"B"}, "B": {"C"}, "C": {"A"}},
			[]string{"A", "B", "C"},
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			graph := cyclicGraph(t, tc.parents)
			data, err := MarshalGraphFile(graph)
			if err == nil {
				t.Fatalf("MarshalGraphFile cycle returned nil error and bytes %s", data)
			}
			if data != nil {
				t.Fatalf("MarshalGraphFile cycle returned saveable bytes %q, want nil", data)
			}
			if !errors.Is(err, ErrCycle) {
				t.Fatalf("err = %v, want ErrCycle", err)
			}
			msg := err.Error()
			for _, want := range tc.wantOn {
				if !strings.Contains(msg, want) {
					t.Errorf("error message %q does not name cycle dataset %q", msg, want)
				}
			}
			// A three-node cycle whose references all exist must not be
			// misreported as a missing upstream.
			if errors.Is(err, ErrNotFound) {
				t.Errorf("cycle error also matches ErrNotFound: %v", err)
			}
			// The rejected bytes would be refused by the reader too, and a
			// repeated export still fails: success cannot be achieved by
			// retrying against silently rewritten state.
			if _, err := MarshalGraphFile(graph); !errors.Is(err, ErrCycle) {
				t.Fatalf("second MarshalGraphFile err = %v, want ErrCycle", err)
			}
		})
	}
}

// TestMarshalCycleInDisconnectedComponent verifies the check covers the whole
// graph: a cycle sharing no node with an otherwise valid branch still rejects
// the entire export; the valid branch must not be exported on its own.
func TestMarshalCycleInDisconnectedComponent(t *testing.T) {
	graph := cyclicGraph(t, map[string][]string{
		// Valid branch: R -> S.
		"R": {},
		"S": {"R"},
		// Unrelated cycle X <-> Y.
		"X": {"Y"},
		"Y": {"X"},
	})
	data, err := MarshalGraphFile(graph)
	if !errors.Is(err, ErrCycle) {
		t.Fatalf("err = %v, want ErrCycle", err)
	}
	if data != nil {
		t.Fatalf("got bytes %q for graph with disconnected cycle, want nil", data)
	}
}

// TestMarshalCycleIsReadOnly verifies a rejected export leaves the in-memory
// graph exactly as it was: node names, parents, children, and the caller's
// slice order are all preserved.
func TestMarshalCycleIsReadOnly(t *testing.T) {
	graph := map[string]*Lineage{
		"A": {Dataset: "A", Parents: []string{"B"}},
		"B": {Dataset: "B", Parents: []string{"A"}},
	}
	graph["A"].Children = []string{"B"}
	graph["B"].Children = []string{"A"}
	want := map[string]*Lineage{
		"A": {Dataset: "A", Parents: []string{"B"}, Children: []string{"B"}},
		"B": {Dataset: "B", Parents: []string{"A"}, Children: []string{"A"}},
	}

	if _, err := MarshalGraphFile(graph); !errors.Is(err, ErrCycle) {
		t.Fatalf("err = %v, want ErrCycle", err)
	}
	if !reflect.DeepEqual(graph, want) {
		t.Fatalf("graph mutated by failed export:\n got %#v\nwant %#v", graph, want)
	}
}

// TestMarshalNonCycleRejectionsKeepCategories verifies that the other existing
// rejections survive unchanged and are not folded into the cycle category.
func TestMarshalNonCycleRejectionsKeepCategories(t *testing.T) {
	t.Run("uninitialized graph", func(t *testing.T) {
		data, err := MarshalGraphFile(nil)
		if !errors.Is(err, ErrNotInitialized) {
			t.Fatalf("err = %v, want ErrNotInitialized", err)
		}
		if data != nil {
			t.Fatalf("got bytes %q, want nil", data)
		}
		if errors.Is(err, ErrCycle) {
			t.Fatalf("nil graph error matches ErrCycle: %v", err)
		}
	})

	t.Run("nil node", func(t *testing.T) {
		_, err := MarshalGraphFile(map[string]*Lineage{"A": nil})
		if !errors.Is(err, ErrInvalidArgument) {
			t.Fatalf("err = %v, want ErrInvalidArgument", err)
		}
		if errors.Is(err, ErrCycle) {
			t.Fatalf("nil node error matches ErrCycle: %v", err)
		}
	})

	t.Run("empty name", func(t *testing.T) {
		_, err := MarshalGraphFile(map[string]*Lineage{"": {Dataset: ""}})
		if !errors.Is(err, ErrInvalidArgument) {
			t.Fatalf("err = %v, want ErrInvalidArgument", err)
		}
		if errors.Is(err, ErrCycle) {
			t.Fatalf("empty-name error matches ErrCycle: %v", err)
		}
	})

	t.Run("missing upstream", func(t *testing.T) {
		graph := map[string]*Lineage{
			"A": {Dataset: "A", Parents: []string{"ghost"}},
		}
		_, err := MarshalGraphFile(graph)
		if !errors.Is(err, ErrNotFound) {
			t.Fatalf("err = %v, want ErrNotFound", err)
		}
		if errors.Is(err, ErrCycle) {
			t.Fatalf("missing-upstream error matches ErrCycle: %v", err)
		}
	})
}

// TestMarshalConvergingDiamondIsNotCycle verifies that D reaching root A along
// two paths (D->B->A and D->C->A) is shared ancestry, not a cycle, and still
// exports and round-trips.
func TestMarshalConvergingDiamondIsNotCycle(t *testing.T) {
	graph := buildGraph(t,
		P("A"),
		P("B", "A"),
		P("C", "A"),
		P("D", "B", "C"),
	)
	data, err := MarshalGraphFile(graph)
	if err != nil {
		t.Fatalf("MarshalGraphFile diamond unexpected error: %v", err)
	}
	parsed, err := UnmarshalGraphFile(data)
	if err != nil {
		t.Fatalf("reader rejected exported diamond: %v\n%s", err, data)
	}
	if !reflect.DeepEqual(parsed, graph) {
		t.Fatalf("round-trip mismatch:\n got %#v\nwant %#v", parsed, graph)
	}
}

// TestMarshalDisconnectedBranches verifies several unrelated valid branches
// export together.
func TestMarshalDisconnectedBranches(t *testing.T) {
	graph := buildGraph(t,
		P("A"),
		P("B", "A"),
		P("X"),
		P("Y", "X"),
		P("Z", "Y"),
	)
	if _, err := MarshalGraphFile(graph); err != nil {
		t.Fatalf("MarshalGraphFile branches unexpected error: %v", err)
	}
}

// TestMarshalSingleRootAndEmpty verifies a lone root and the empty graph both
// export, with the empty graph encoding datasets as [].
func TestMarshalSingleRootAndEmpty(t *testing.T) {
	if _, err := MarshalGraphFile(buildGraph(t, P("A"))); err != nil {
		t.Fatalf("single root unexpected error: %v", err)
	}
	data, err := MarshalGraphFile(map[string]*Lineage{})
	if err != nil {
		t.Fatalf("empty graph unexpected error: %v", err)
	}
	if !bytes.Contains(data, []byte(`"datasets": []`)) {
		t.Fatalf("empty graph JSON = %s, want datasets: []", data)
	}
}

// TestMarshalPreservesNamesAndDuplicateUpstreams verifies case sensitivity,
// surrounding whitespace, and that a repeated upstream is still one relation:
// reaching the same dataset name through two listed entries must not look like
// a cycle, and the exported bytes must name the datasets verbatim.
func TestMarshalPreservesNamesAndDuplicateUpstreams(t *testing.T) {
	graph := map[string]*Lineage{}
	if err := Register(graph, Dataset{Name: " Root "}, nil); err != nil {
		t.Fatalf("Register root: %v", err)
	}
	if err := Register(graph, Dataset{Name: "child"}, []string{" Root ", " Root "}); err != nil {
		t.Fatalf("Register child with duplicate upstream: %v", err)
	}
	if err := Register(graph, Dataset{Name: "CHILD"}, []string{" Root "}); err != nil {
		t.Fatalf("Register case-distinct CHILD: %v", err)
	}
	data, err := MarshalGraphFile(graph)
	if err != nil {
		t.Fatalf("MarshalGraphFile unexpected error: %v", err)
	}
	text := string(data)
	for _, want := range []string{`" Root "`, `"child"`, `"CHILD"`} {
		if !strings.Contains(text, want) {
			t.Errorf("export %s missing verbatim name %s", text, want)
		}
	}
	parsed, err := UnmarshalGraphFile(data)
	if err != nil {
		t.Fatalf("reader rejected export: %v", err)
	}
	if got := parsed["child"].Parents; !reflect.DeepEqual(got, []string{" Root "}) {
		t.Errorf("child parents = %v, want single [ Root ]", got)
	}
}

// TestMarshalValidBytesUnchanged verifies the fix does not change the bytes of
// a legal graph: the records stay name-sorted with sorted upstreams and the
// existing two-space indentation.
func TestMarshalValidBytesUnchanged(t *testing.T) {
	graph := buildGraph(t,
		P("A"),
		P("B", "A"),
		P("C", "A", "B"),
	)
	got, err := MarshalGraphFile(graph)
	if err != nil {
		t.Fatalf("MarshalGraphFile unexpected error: %v", err)
	}
	want := `{
  "datasets": [
    {
      "name": "A",
      "upstreams": []
    },
    {
      "name": "B",
      "upstreams": [
        "A"
      ]
    },
    {
      "name": "C",
      "upstreams": [
        "A",
        "B"
      ]
    }
  ]
}`
	if string(got) != want {
		t.Fatalf("export bytes changed:\n got:\n%s\nwant:\n%s", got, want)
	}
}
