package chainledger

import (
	"strings"
	"testing"
)

// assertCycleRoute checks that a cycle error's explanation route is a real
// cycle of the document: it is written upstream -> derived, starts and ends
// with the same dataset, every hop is one of the declared direct
// dependencies, no dataset repeats inside the loop, and the route begins at
// the smallest-named dataset on the loop in Go string order.
func assertCycleRoute(t *testing.T, err error, edges ...[2]string) []string {
	t.Helper()
	if err == nil {
		t.Fatal("want a cycle error, got nil")
	}
	const prefix = "lineage contains a cycle: "
	msg := err.Error()
	if !strings.HasPrefix(msg, prefix) {
		t.Fatalf("error %q must start with %q", msg, prefix)
	}
	route := strings.Split(strings.TrimPrefix(msg, prefix), " -> ")
	if len(route) < 2 {
		t.Fatalf("cycle route %q must hold at least a dataset and its return", msg)
	}
	if route[0] != route[len(route)-1] {
		t.Errorf("cycle route %v must start and end with the same dataset", route)
	}

	body := route[:len(route)-1]
	seen := make(map[string]bool, len(body))
	smallest := body[0]
	for _, name := range body {
		if seen[name] {
			t.Errorf("cycle route %v revisits %q before closing", route, name)
		}
		seen[name] = true
		if name < smallest {
			smallest = name
		}
	}
	if route[0] != smallest {
		t.Errorf("cycle route %v starts at %q, want smallest dataset on the loop %q",
			route, route[0], smallest)
	}

	declared := make(map[[2]string]bool, len(edges))
	for _, e := range edges {
		declared[e] = true
	}
	for i := 0; i+1 < len(route); i++ {
		hop := [2]string{route[i], route[i+1]}
		if !declared[hop] {
			t.Errorf("cycle route hop %q -> %q is not a direct dependency declared by the document",
				route[i], route[i+1])
		}
	}
	return route
}

// A document mixing a valid derivation chain, an independent dataset and a
// disconnected cyclic region is rejected as a whole: the import returns a nil
// graph and a cycle error, never the valid part or half a graph. The cyclic
// region is neither connected to the valid branch nor holds the document's
// smallest name.
func TestImportLineageCycleAmongValidRegionsFailsWholeImport(t *testing.T) {
	edges := [][2]string{
		{"raw", "mid"}, {"mid", "report"}, // valid derivation chain
		{"x", "y"}, {"y", "z"}, {"z", "x"}, // disconnected cycle
	}
	text := marshalLineageDoc(
		[]string{"raw", "mid", "report", "isolated", "x", "y", "z"},
		edges...,
	)

	graph, err := ImportLineage(text)
	if err == nil {
		t.Fatalf("import with a cyclic region succeeded with %v, want error", graph)
	}
	if graph != nil {
		t.Fatalf("rejected import returned a partial graph %v, want nil", graph)
	}
	if !strings.Contains(err.Error(), "cycle") {
		t.Errorf("error %q must state the lineage contains a cycle", err.Error())
	}
	route := assertCycleRoute(t, err, edges...)
	// The explanation stays inside the cyclic region: the valid chain, the
	// independent dataset and the document's smallest names are not on it.
	if want := []string{"x", "y", "z", "x"}; !sameStrings(route, want) {
		t.Errorf("cycle route = %v, want %v", route, want)
	}
}

// Several cycles in one document: the error names one real, complete cycle
// regardless of how the cycles relate to each other.
func TestImportLineageReportsOneRealCycleAmongSeveral(t *testing.T) {
	t.Run("two disconnected cycles", func(t *testing.T) {
		edges := [][2]string{
			{"m", "n"}, {"n", "o"}, {"o", "m"},
			{"p", "q"}, {"q", "p"},
		}
		text := marshalLineageDoc([]string{"m", "n", "o", "p", "q"}, edges...)

		graph, err := ImportLineage(text)
		if graph != nil || err == nil {
			t.Fatalf("want nil graph and cycle error, got graph=%v err=%v", graph, err)
		}
		route := assertCycleRoute(t, err, edges...)
		if want := []string{"m", "n", "o", "m"}; !sameStrings(route, want) {
			t.Errorf("cycle route = %v, want %v", route, want)
		}
	})

	t.Run("cycles sharing a node", func(t *testing.T) {
		edges := [][2]string{
			{"a", "b"}, {"b", "c"}, {"c", "a"}, // loop through a, b, c
			{"b", "d"}, {"d", "b"}, // second loop sharing b
		}
		text := marshalLineageDoc([]string{"a", "b", "c", "d"}, edges...)

		graph, err := ImportLineage(text)
		if graph != nil || err == nil {
			t.Fatalf("want nil graph and cycle error, got graph=%v err=%v", graph, err)
		}
		// One of the two real cycles is reported, as a complete route.
		route := assertCycleRoute(t, err, edges...)
		if want := []string{"a", "b", "c", "a"}; !sameStrings(route, want) {
			t.Errorf("cycle route = %v, want %v", route, want)
		}
	})
}

// The same lineage spelled differently — nodes and edges reordered, existing
// nodes and dependencies repeated — reports the same cycle with the identical
// error text. The cycle here sits behind an ordinary chain and feeds an
// outward branch; neither belongs to the explanation route.
func TestImportLineageCycleReportStableAcrossDocumentSpellings(t *testing.T) {
	nodes := []string{"a", "b", "c", "d", "e"}
	edges := [][2]string{
		{"a", "b"},                         // ordinary chain leading into the loop
		{"b", "c"}, {"c", "d"}, {"d", "b"}, // the loop
		{"c", "e"}, // branch leaving the loop
	}
	const want = "lineage contains a cycle: b -> c -> d -> b"

	spellings := map[string]string{
		"declared order": marshalLineageDoc(nodes, edges...),
		"reversed order": marshalLineageDoc(
			[]string{"e", "d", "c", "b", "a"},
			[2]string{"c", "e"}, [2]string{"d", "b"}, [2]string{"c", "d"},
			[2]string{"b", "c"}, [2]string{"a", "b"},
		),
		"interleaved": `{"nodes":["c","a","e","d","b"],"edges":[` +
			`{"from":"c","to":"d"},{"from":"a","to":"b"},{"from":"d","to":"b"},` +
			`{"from":"b","to":"c"},{"from":"c","to":"e"}]}`,
		"with duplicates": marshalLineageDoc(
			[]string{"e", "a", "b", "c", "d", "b", "a", "e"},
			[2]string{"b", "c"}, [2]string{"a", "b"}, [2]string{"c", "d"},
			[2]string{"d", "b"}, [2]string{"c", "e"},
			[2]string{"b", "c"}, [2]string{"d", "b"}, [2]string{"a", "b"},
		),
	}

	for name, text := range spellings {
		t.Run(name, func(t *testing.T) {
			graph, err := ImportLineage(text)
			if graph != nil {
				t.Fatalf("rejected import returned a partial graph %v, want nil", graph)
			}
			if err == nil {
				t.Fatal("want a cycle error, got nil")
			}
			if err.Error() != want {
				t.Errorf("cycle error = %q, want %q", err.Error(), want)
			}
			assertCycleRoute(t, err, edges...)
		})
	}
}

// A legitimate merge — two branches sharing one upstream and jointly deriving
// one dataset — has no route back to itself and imports successfully with
// every declared node and direct dependency kept.
func TestImportLineageSharedUpstreamMergeImports(t *testing.T) {
	text := marshalLineageDoc(
		[]string{"raw", "left", "right", "merge", "report", "isolated"},
		[2]string{"raw", "left"}, [2]string{"raw", "right"},
		[2]string{"left", "merge"}, [2]string{"right", "merge"},
		[2]string{"merge", "report"},
	)
	graph := mustImport(t, text)
	assertConsistent(t, graph)

	if got, want := len(graph), 6; got != want {
		t.Fatalf("graph has %d nodes, want %d", got, want)
	}
	assertEntry(t, graph, "raw", nil, []string{"left", "right"})
	assertEntry(t, graph, "left", []string{"raw"}, []string{"merge"})
	assertEntry(t, graph, "right", []string{"raw"}, []string{"merge"})
	assertEntry(t, graph, "merge", []string{"left", "right"}, []string{"report"})
	assertEntry(t, graph, "report", []string{"merge"}, nil)
	assertEntry(t, graph, "isolated", nil, nil)
}

// Once the dependencies that close the loops are removed, the very same
// document — still multi-branched, still shaped almost like the cyclic one —
// is acyclic and imports normally.
func TestImportLineageImportsOnceClosingDependenciesRemoved(t *testing.T) {
	t.Run("loop opened into a chain", func(t *testing.T) {
		// Same shape as the stable-report document above, minus d -> b.
		text := marshalLineageDoc(
			[]string{"a", "b", "c", "d", "e"},
			[2]string{"a", "b"},
			[2]string{"b", "c"}, [2]string{"c", "d"},
			[2]string{"c", "e"},
		)
		graph := mustImport(t, text)
		assertConsistent(t, graph)
		assertEntry(t, graph, "a", nil, []string{"b"})
		assertEntry(t, graph, "b", []string{"a"}, []string{"c"})
		assertEntry(t, graph, "c", []string{"b"}, []string{"d", "e"})
		assertEntry(t, graph, "d", []string{"c"}, nil)
		assertEntry(t, graph, "e", []string{"c"}, nil)
	})

	t.Run("shared-node loops both opened", func(t *testing.T) {
		// The shared-node document minus its two closing dependencies
		// (c -> a and d -> b) leaves an ordinary branching DAG.
		text := marshalLineageDoc(
			[]string{"a", "b", "c", "d"},
			[2]string{"a", "b"}, [2]string{"b", "c"}, [2]string{"b", "d"},
		)
		graph := mustImport(t, text)
		assertConsistent(t, graph)
		assertEntry(t, graph, "a", nil, []string{"b"})
		assertEntry(t, graph, "b", []string{"a"}, []string{"c", "d"})
		assertEntry(t, graph, "c", []string{"b"}, nil)
		assertEntry(t, graph, "d", []string{"b"}, nil)
	})
}
