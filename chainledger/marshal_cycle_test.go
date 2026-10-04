package chainledger

import (
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
)

// cyclicGraph builds an in-memory graph with the given parent lists by hand.
// Register itself can never produce a cycle, so cycle fixtures must bypass it.
// Parent lists are stored verbatim (unsorted, with duplicates kept), which
// lets tests prove the export leaves the caller's lists untouched.
func cyclicGraph(t *testing.T, parents map[string][]string) map[string]*Lineage {
	t.Helper()
	graph := make(map[string]*Lineage, len(parents))
	for name, ups := range parents {
		graph[name] = &Lineage{Dataset: name, Parents: ups}
	}
	for name, entry := range graph {
		for _, parent := range entry.Parents {
			if p := graph[parent]; p != nil {
				p.Children = append(p.Children, name)
			}
		}
	}
	return graph
}

// cloneGraph returns a deep, value-equal copy of graph for before/after checks.
func cloneGraph(graph map[string]*Lineage) map[string]*Lineage {
	out := make(map[string]*Lineage, len(graph))
	for name, entry := range graph {
		if entry == nil {
			out[name] = nil
			continue
		}
		cp := &Lineage{Dataset: entry.Dataset}
		cp.Parents = append([]string(nil), entry.Parents...)
		cp.Children = append([]string(nil), entry.Children...)
		out[name] = cp
	}
	return out
}

// TestMarshalGraphFileRejectsCycles verifies the export refuses any cyclic
// graph, even one where every upstream reference resolves.
func TestMarshalGraphFileRejectsCycles(t *testing.T) {
	t.Run("dataset lists itself as upstream", func(t *testing.T) {
		graph := cyclicGraph(t, map[string][]string{
			"A": {"A"},
		})
		data, err := MarshalGraphFile(graph)
		if !errors.Is(err, ErrCycle) {
			t.Fatalf("err = %v, want ErrCycle", err)
		}
		if data != nil {
			t.Fatalf("rejected export returned bytes: %s", data)
		}
		if !strings.Contains(err.Error(), "A") {
			t.Errorf("error %q does not name dataset A", err)
		}
	})

	t.Run("two datasets reach back to each other", func(t *testing.T) {
		graph := cyclicGraph(t, map[string][]string{
			"A": {"B"},
			"B": {"A"},
		})
		data, err := MarshalGraphFile(graph)
		if !errors.Is(err, ErrCycle) {
			t.Fatalf("err = %v, want ErrCycle", err)
		}
		if data != nil {
			t.Fatalf("rejected export returned bytes: %s", data)
		}
		for _, name := range []string{"A", "B"} {
			if !strings.Contains(err.Error(), name) {
				t.Errorf("error %q does not name dataset %q", err, name)
			}
		}
	})

	t.Run("three-node cycle with all references present", func(t *testing.T) {
		// Every reference resolves, so this must be reported as a cycle rather
		// than as a missing upstream.
		graph := cyclicGraph(t, map[string][]string{
			"A": {"B"},
			"B": {"C"},
			"C": {"A"},
		})
		data, err := MarshalGraphFile(graph)
		if !errors.Is(err, ErrCycle) {
			t.Fatalf("err = %v, want ErrCycle", err)
		}
		if errors.Is(err, ErrNotFound) {
			t.Fatalf("resolved three-node cycle misreported as missing upstream: %v", err)
		}
		if data != nil {
			t.Fatalf("rejected export returned bytes: %s", data)
		}
		for _, name := range []string{"A", "B", "C"} {
			if !strings.Contains(err.Error(), name) {
				t.Errorf("error %q does not name dataset %q", err, name)
			}
		}
	})

	t.Run("cycle disconnected from healthy branch rejects whole graph", func(t *testing.T) {
		// A<-B is a healthy branch; X<->Y is an unrelated cycle. The export
		// must not silently deliver only the healthy part.
		graph := cyclicGraph(t, map[string][]string{
			"A": nil,
			"B": {"A"},
			"X": {"Y"},
			"Y": {"X"},
		})
		data, err := MarshalGraphFile(graph)
		if !errors.Is(err, ErrCycle) {
			t.Fatalf("err = %v, want ErrCycle", err)
		}
		if data != nil {
			t.Fatalf("rejected export returned bytes: %s", data)
		}
		if !strings.Contains(err.Error(), "X") || !strings.Contains(err.Error(), "Y") {
			t.Errorf("error %q does not name the cyclic datasets X and Y", err)
		}
	})

	t.Run("exported bytes never describe a cycle", func(t *testing.T) {
		// Sanity: the JSON the old exporter handed out for a cycle is refused
		// by the reader. After the fix the exporter refuses it too.
		graph := cyclicGraph(t, map[string][]string{
			"A": {"B"},
			"B": {"A"},
		})
		raw, err := json.Marshal(GraphFile{Datasets: []GraphDataset{
			{Name: "A", Upstreams: []string{"B"}},
			{Name: "B", Upstreams: []string{"A"}},
		}})
		if err != nil {
			t.Fatalf("json.Marshal: %v", err)
		}
		if _, err := UnmarshalGraphFile(raw); err == nil {
			t.Fatalf("reader accepted a cyclic graph file")
		}
		if _, err := MarshalGraphFile(graph); err == nil {
			t.Fatalf("exporter accepted a graph the reader rejects")
		}
	})
}

// TestMarshalGraphFileKeepsNonCycleRejections verifies the pre-existing
// rejection categories survive unchanged: cycles must not absorb them.
func TestMarshalGraphFileKeepsNonCycleRejections(t *testing.T) {
	t.Run("uninitialized graph", func(t *testing.T) {
		_, err := MarshalGraphFile(nil)
		if !errors.Is(err, ErrNotInitialized) {
			t.Fatalf("err = %v, want ErrNotInitialized", err)
		}
		if errors.Is(err, ErrCycle) {
			t.Fatalf("nil graph misreported as cycle: %v", err)
		}
	})

	t.Run("nil lineage node", func(t *testing.T) {
		graph := map[string]*Lineage{"A": nil}
		_, err := MarshalGraphFile(graph)
		if !errors.Is(err, ErrInvalidArgument) {
			t.Fatalf("err = %v, want ErrInvalidArgument", err)
		}
	})

	t.Run("empty name", func(t *testing.T) {
		graph := map[string]*Lineage{"": {Dataset: ""}}
		_, err := MarshalGraphFile(graph)
		if !errors.Is(err, ErrInvalidArgument) {
			t.Fatalf("err = %v, want ErrInvalidArgument", err)
		}
	})

	t.Run("missing upstream", func(t *testing.T) {
		// A references a name that does not exist: not found, not a cycle.
		graph := map[string]*Lineage{
			"A": {Dataset: "A", Parents: []string{"ghost"}},
		}
		_, err := MarshalGraphFile(graph)
		if !errors.Is(err, ErrNotFound) {
			t.Fatalf("err = %v, want ErrNotFound", err)
		}
		if errors.Is(err, ErrCycle) {
			t.Fatalf("missing upstream misreported as cycle: %v", err)
		}
	})
}

// TestMarshalGraphFileAcceptsLegalGraphs verifies DAG shapes that merely share
// sources still export and round-trip.
func TestMarshalGraphFileAcceptsLegalGraphs(t *testing.T) {
	t.Run("convergence diamond with shared root", func(t *testing.T) {
		// A is the root; B and C depend on A; D depends on both B and C. D
		// reaches A along two paths, which is sharing a source, not a cycle.
		graph := buildGraph(t,
			P("A"),
			P("B", "A"),
			P("C", "A"),
			P("D", "B", "C"),
		)
		data, err := MarshalGraphFile(graph)
		if err != nil {
			t.Fatalf("MarshalGraphFile unexpected error: %v", err)
		}
		parsed, err := UnmarshalGraphFile(data)
		if err != nil {
			t.Fatalf("reader rejected exported graph: %v", err)
		}
		if !reflect.DeepEqual(parsed, graph) {
			t.Fatalf("round-trip mismatch:\n got %#v\nwant %#v", parsed, graph)
		}
	})

	t.Run("repeated upstream is one relationship", func(t *testing.T) {
		// Hand-built graph with a duplicated parent edge: multiple mentions of
		// the same name are one relationship, never a cycle.
		graph := cyclicGraph(t, map[string][]string{
			"A": nil,
			"B": {"A", "A"},
		})
		if _, err := MarshalGraphFile(graph); err != nil {
			t.Fatalf("duplicate upstream edge rejected: %v", err)
		}
	})

	t.Run("multiple disconnected legal branches", func(t *testing.T) {
		graph := buildGraph(t,
			P("A"), P("B", "A"),
			P("X"), P("Y", "X"), P("Z", "Y"),
		)
		if _, err := MarshalGraphFile(graph); err != nil {
			t.Fatalf("disconnected legal branches rejected: %v", err)
		}
	})

	t.Run("single root", func(t *testing.T) {
		graph := buildGraph(t, P("solo"))
		data, err := MarshalGraphFile(graph)
		if err != nil {
			t.Fatalf("MarshalGraphFile unexpected error: %v", err)
		}
		if !strings.Contains(string(data), `"upstreams": []`) {
			t.Errorf("root record does not show empty upstreams:\n%s", data)
		}
	})

	t.Run("empty graph outputs empty datasets array", func(t *testing.T) {
		graph := map[string]*Lineage{}
		data, err := MarshalGraphFile(graph)
		if err != nil {
			t.Fatalf("MarshalGraphFile empty unexpected error: %v", err)
		}
		if strings.TrimSpace(string(data)) != `{
  "datasets": []
}` {
			t.Errorf("empty graph JSON = %q, want datasets empty array", data)
		}
	})

	t.Run("case and surrounding spaces stay significant", func(t *testing.T) {
		graph := buildGraph(t,
			P(" Raw "),
			P("RAW", " Raw "),
			P("raw", " Raw "),
		)
		data, err := MarshalGraphFile(graph)
		if err != nil {
			t.Fatalf("MarshalGraphFile unexpected error: %v", err)
		}
		parsed, err := UnmarshalGraphFile(data)
		if err != nil {
			t.Fatalf("reader rejected exported graph: %v", err)
		}
		if !reflect.DeepEqual(parsed, graph) {
			t.Fatalf("names were case-folded or trimmed:\n got %#v\nwant %#v", parsed, graph)
		}
	})
}

// TestMarshalGraphFileIsReadOnly verifies export never mutates the in-memory
// graph, including on rejection of a cyclic graph: node names, parent/child
// lists (and their caller-supplied order), and relations survive byte-for-byte.
func TestMarshalGraphFileIsReadOnly(t *testing.T) {
	t.Run("failed cyclic export leaves graph untouched", func(t *testing.T) {
		// Unsorted parent/children lists and a duplicated edge are stored
		// verbatim; the export normalizes only internal copies.
		graph := map[string]*Lineage{
			"A": {Dataset: "A", Parents: []string{"B"}, Children: []string{"B"}},
			"B": {Dataset: "B", Parents: []string{"A"}, Children: []string{"A"}},
		}
		before := cloneGraph(graph)
		if _, err := MarshalGraphFile(graph); err == nil {
			t.Fatalf("expected cycle error")
		}
		if !reflect.DeepEqual(graph, before) {
			t.Fatalf("graph mutated by failed export:\n got %#v\nwant %#v", graph, before)
		}
	})

	t.Run("successful export leaves caller list order untouched", func(t *testing.T) {
		// B stores a duplicated, reverse-order parent list by hand; export
		// succeeds and must not rewrite the in-memory slice.
		graph := cyclicGraph(t, map[string][]string{
			"A": nil,
			"B": {"A", "A"},
		})
		before := cloneGraph(graph)
		if _, err := MarshalGraphFile(graph); err != nil {
			t.Fatalf("MarshalGraphFile unexpected error: %v", err)
		}
		if !reflect.DeepEqual(graph, before) {
			t.Fatalf("graph mutated by export:\n got %#v\nwant %#v", graph, before)
		}
		if got := graph["B"].Parents; !reflect.DeepEqual(got, []string{"A", "A"}) {
			t.Errorf("caller parents rewritten to %v", got)
		}
	})

	t.Run("legal export bytes are stable and sorted", func(t *testing.T) {
		graph := buildGraph(t,
			P("A"),
			P("B", "A"),
			P("C", "A"),
			P("D", "B", "C"),
		)
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
        "A"
      ]
    },
    {
      "name": "D",
      "upstreams": [
        "B",
        "C"
      ]
    }
  ]
}`
		data, err := MarshalGraphFile(graph)
		if err != nil {
			t.Fatalf("MarshalGraphFile unexpected error: %v", err)
		}
		if string(data) != want {
			t.Fatalf("export bytes changed:\n got:\n%s\nwant:\n%s", data, want)
		}
		// Repeated exports of the same graph return identical bytes.
		data2, err := MarshalGraphFile(graph)
		if err != nil {
			t.Fatalf("second MarshalGraphFile unexpected error: %v", err)
		}
		if string(data) != string(data2) {
			t.Fatal("export bytes differ between calls")
		}
	})
}
