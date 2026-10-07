package chainledger

import (
	"reflect"
	"strings"
	"testing"
)

// assertCycleErrorPath validates the shape of a cycle-rejection error against
// the document's declared direct dependencies: the message carries the
// documented prefix, the explanation route starts and ends on the same
// dataset, every hop is a declared {"from": upstream, "to": derived} edge
// written in that derivation direction, no dataset appears twice inside the
// route (a complete closed loop, not a chain leading into one), and the route
// starts at the smallest name on the loop in Go string order. It returns the
// parsed route for further assertions.
func assertCycleErrorPath(t *testing.T, err error, edges ...[2]string) []string {
	t.Helper()
	if err == nil {
		t.Fatal("want a cycle error, got nil")
	}
	const prefix = "lineage contains a cycle: "
	if !strings.HasPrefix(err.Error(), prefix) {
		t.Fatalf("error %q must start with %q", err.Error(), prefix)
	}
	route := strings.Split(strings.TrimPrefix(err.Error(), prefix), " -> ")
	if len(route) < 2 {
		t.Fatalf("cycle route %v must hold at least a dataset and its return", route)
	}
	if route[0] != route[len(route)-1] {
		t.Fatalf("cycle route %v must start and end on the same dataset", route)
	}

	declared := make(map[[2]string]bool, len(edges))
	for _, e := range edges {
		declared[e] = true
	}
	seen := make(map[string]bool, len(route)-1)
	for i, name := range route[:len(route)-1] {
		if seen[name] {
			t.Fatalf("cycle route %v revisits %q before closing", route, name)
		}
		seen[name] = true
		if hop := [2]string{name, route[i+1]}; !declared[hop] {
			t.Fatalf("cycle route %v hop %q -> %q is not a declared direct dependency", route, hop[0], hop[1])
		}
	}
	for _, name := range route[1 : len(route)-1] {
		if name < route[0] {
			t.Fatalf("cycle route %v must start at its smallest name, %q < %q", route, name, route[0])
		}
	}
	return route
}

// A document mixing a complete legal derivation chain, an independent dataset
// and a disconnected cyclic region is rejected as a whole: the legal part is
// fully declared and could build a graph on its own, but the caller still gets
// a nil graph and the cycle error — never the legal half. The cyclic region
// here is neither connected to the legal branch nor holds the document's
// smallest names.
func TestImportLineageCycleInOneRegionRejectsWholeDocument(t *testing.T) {
	edges := [][2]string{
		{"raw", "stage"}, {"stage", "report"}, // complete legal chain
		{"x", "y"}, {"y", "z"}, {"z", "x"}, // disconnected cycle
	}
	text := marshalLineageDoc(
		[]string{"raw", "stage", "report", "isolated", "x", "y", "z"},
		edges...,
	)

	graph, err := ImportLineage(text)
	if err == nil {
		t.Fatalf("document with a cyclic region imported as %v, want rejection", graph)
	}
	if graph != nil {
		t.Fatalf("rejected import returned a partial graph %v, want nil", graph)
	}
	if want := "lineage contains a cycle: x -> y -> z -> x"; err.Error() != want {
		t.Errorf("cycle error = %q, want %q", err.Error(), want)
	}
	assertCycleErrorPath(t, err, edges...)

	// The legal part being complete and queryable does not matter: no graph
	// escapes, so nothing from raw/stage/report/isolated is observable.
	if strings.Contains(err.Error(), "raw") || strings.Contains(err.Error(), "isolated") {
		t.Errorf("cycle error %q must not drag the legal region into the route", err.Error())
	}
}

// Two mutually disconnected cycles in one document: the error reports one
// real, complete cycle — deterministic across calls — and the route stays
// inside a single cyclic region.
func TestImportLineageTwoDisconnectedCycles(t *testing.T) {
	edges := [][2]string{
		{"ca1", "ca2"}, {"ca2", "ca1"}, // first cycle, two datasets
		{"cb1", "cb2"}, {"cb2", "cb3"}, {"cb3", "cb1"}, // second cycle, three datasets
		{"feed", "dash"}, // legal branch alongside both
	}
	text := marshalLineageDoc(
		[]string{"ca1", "ca2", "cb1", "cb2", "cb3", "feed", "dash"},
		edges...,
	)

	graph, err := ImportLineage(text)
	if graph != nil || err == nil {
		t.Fatalf("want nil graph and cycle error, got graph=%v err=%v", graph, err)
	}
	if want := "lineage contains a cycle: ca1 -> ca2 -> ca1"; err.Error() != want {
		t.Errorf("cycle error = %q, want %q", err.Error(), want)
	}
	route := assertCycleErrorPath(t, err, edges...)
	for _, name := range route {
		if strings.HasPrefix(name, "cb") || name == "feed" || name == "dash" {
			t.Errorf("route %v mixes regions; want only datasets of one cycle", route)
		}
	}

	// Repeated imports of the same document report the same cycle.
	if _, again := ImportLineage(text); again == nil || again.Error() != err.Error() {
		t.Errorf("repeated import error = %v, want identical %q", again, err.Error())
	}
}

// Several cycles sharing a node (a figure eight, and a chain of 2-cycles):
// the error still describes exactly one real closed route through declared
// dependencies, not a merge of both loops.
func TestImportLineageSharedNodeCycles(t *testing.T) {
	t.Run("figure eight", func(t *testing.T) {
		edges := [][2]string{
			{"s", "t"}, {"t", "s"}, // loop s <-> t
			{"t", "u"}, {"u", "t"}, // loop t <-> u, sharing t
		}
		graph, err := ImportLineage(marshalLineageDoc([]string{"s", "t", "u"}, edges...))
		if graph != nil || err == nil {
			t.Fatalf("want nil graph and cycle error, got graph=%v err=%v", graph, err)
		}
		if want := "lineage contains a cycle: s -> t -> s"; err.Error() != want {
			t.Errorf("cycle error = %q, want %q", err.Error(), want)
		}
		assertCycleErrorPath(t, err, edges...)
	})
	t.Run("chained two-cycles", func(t *testing.T) {
		edges := [][2]string{
			{"a", "b"}, {"b", "a"}, // loop a <-> b
			{"b", "c"}, {"c", "b"}, // loop b <-> c, sharing b
		}
		graph, err := ImportLineage(marshalLineageDoc([]string{"a", "b", "c"}, edges...))
		if graph != nil || err == nil {
			t.Fatalf("want nil graph and cycle error, got graph=%v err=%v", graph, err)
		}
		if want := "lineage contains a cycle: a -> b -> a"; err.Error() != want {
			t.Errorf("cycle error = %q, want %q", err.Error(), want)
		}
		assertCycleErrorPath(t, err, edges...)
	})
}

// A plain chain feeding into a cycle (a lollipop): the explanation route is
// the closed loop only — the approach chain and any side branch off the loop
// are not part of it, even though they are declared dependencies of loop
// members.
func TestImportLineageCycleRouteExcludesApproachChainAndSideBranch(t *testing.T) {
	edges := [][2]string{
		{"tail", "c1"},             // approach chain into the loop
		{"c1", "c2"}, {"c2", "c1"}, // the loop
		{"c2", "downstream-side"}, // side branch off a loop member
	}
	text := marshalLineageDoc(
		[]string{"tail", "c1", "c2", "downstream-side"},
		edges...,
	)

	graph, err := ImportLineage(text)
	if graph != nil || err == nil {
		t.Fatalf("want nil graph and cycle error, got graph=%v err=%v", graph, err)
	}
	if want := "lineage contains a cycle: c1 -> c2 -> c1"; err.Error() != want {
		t.Errorf("cycle error = %q, want %q", err.Error(), want)
	}
	route := assertCycleErrorPath(t, err, edges...)
	for _, name := range route {
		if name == "tail" || name == "downstream-side" {
			t.Errorf("route %v must stay on the loop, found %q", route, name)
		}
	}
}

// Same nodes and same direct dependencies written differently — nodes and
// edges rearranged, nodes and dependencies repeated, object field order
// swapped — must report the identical cycle with the identical full error
// text. This compares spellings of one lineage; it does not pick a canonical
// cycle across different lineages.
func TestImportLineageCycleReportStableAcrossDocumentSpellings(t *testing.T) {
	edges := [][2]string{
		{"raw", "stage"}, {"stage", "report"}, // legal chain
		{"m1", "m2"}, {"m2", "m1"}, // cycle one
		{"z1", "z2"}, {"z2", "z3"}, {"z3", "z1"}, // cycle two
	}
	canonical := marshalLineageDoc(
		[]string{"raw", "stage", "report", "alone", "m1", "m2", "z1", "z2", "z3"},
		edges...,
	)

	_, canonicalErr := ImportLineage(canonical)
	if canonicalErr == nil {
		t.Fatal("canonical document must be rejected")
	}
	const want = "lineage contains a cycle: m1 -> m2 -> m1"
	if canonicalErr.Error() != want {
		t.Fatalf("canonical cycle error = %q, want %q", canonicalErr.Error(), want)
	}

	spellings := []string{
		// Nodes reversed, edges reversed, edges field listed before nodes.
		`{"edges":[{"from":"z3","to":"z1"},{"from":"z2","to":"z3"},{"from":"z1","to":"z2"},` +
			`{"from":"m2","to":"m1"},{"from":"m1","to":"m2"},` +
			`{"from":"stage","to":"report"},{"from":"raw","to":"stage"}],` +
			`"nodes":["z3","z2","z1","m2","m1","alone","report","stage","raw"]}`,
		// Shuffled order with a repeated node and a repeated dependency.
		`{"nodes":["m2","z1","raw","m1","stage","z3","alone","report","z2","m1","z1"],` +
			`"edges":[{"from":"z2","to":"z3"},{"from":"m1","to":"m2"},{"from":"raw","to":"stage"},` +
			`{"from":"z3","to":"z1"},{"from":"m2","to":"m1"},{"from":"stage","to":"report"},` +
			`{"from":"z1","to":"z2"},{"from":"m1","to":"m2"}]}`,
		// Interleaved regions, every node and edge listed exactly once.
		`{"nodes":["z2","m1","report","alone","z1","m2","raw","stage","z3"],` +
			`"edges":[{"from":"z1","to":"z2"},{"from":"m2","to":"m1"},{"from":"raw","to":"stage"},` +
			`{"from":"z3","to":"z1"},{"from":"stage","to":"report"},{"from":"m1","to":"m2"},` +
			`{"from":"z2","to":"z3"}]}`,
	}
	for i, text := range spellings {
		graph, err := ImportLineage(text)
		if graph != nil || err == nil {
			t.Fatalf("spelling %d: want nil graph and cycle error, got graph=%v err=%v", i, graph, err)
		}
		if err.Error() != canonicalErr.Error() {
			t.Errorf("spelling %d: cycle error = %q, want identical %q", i, err.Error(), canonicalErr.Error())
		}
	}
}

// The reported route for every cyclic shape is a genuine loop of declared
// dependencies: self-dependency, mutual pair, long loop, and a loop whose
// members also carry legal-looking extra dependencies.
func TestImportLineageCycleRoutesAreDeclaredLoops(t *testing.T) {
	cases := []struct {
		name  string
		nodes []string
		edges [][2]string
		want  string
	}{
		{
			"self dependency",
			[]string{"a", "b"},
			[][2]string{{"a", "a"}, {"a", "b"}},
			"lineage contains a cycle: a -> a",
		},
		{
			"mutual pair with legal branch",
			[]string{"p", "q", "root", "leaf"},
			[][2]string{{"p", "q"}, {"q", "p"}, {"root", "leaf"}},
			"lineage contains a cycle: p -> q -> p",
		},
		{
			"long loop with extra dependencies on members",
			[]string{"n1", "n2", "n3", "n4", "feed", "sink"},
			[][2]string{
				{"n1", "n2"}, {"n2", "n3"}, {"n3", "n4"}, {"n4", "n1"},
				{"feed", "n1"}, {"n4", "sink"}, // loop members with outside links
			},
			"lineage contains a cycle: n1 -> n2 -> n3 -> n4 -> n1",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			graph, err := ImportLineage(marshalLineageDoc(tc.nodes, tc.edges...))
			if graph != nil || err == nil {
				t.Fatalf("want nil graph and cycle error, got graph=%v err=%v", graph, err)
			}
			if err.Error() != tc.want {
				t.Errorf("cycle error = %q, want %q", err.Error(), tc.want)
			}
			assertCycleErrorPath(t, err, tc.edges...)
		})
	}
}

// A legal merge — two branches sharing one upstream and jointly deriving one
// dataset — has no route back to itself and must import with every declared
// node and direct dependency intact.
func TestImportLineageMergeBranchesWithoutCycleImports(t *testing.T) {
	text := marshalLineageDoc(
		[]string{"raw", "left", "right", "join", "present"},
		[2]string{"raw", "left"}, [2]string{"raw", "right"},
		[2]string{"left", "join"}, [2]string{"right", "join"},
		[2]string{"join", "present"},
	)
	graph := mustImport(t, text)
	assertConsistent(t, graph)

	if got, want := len(graph), 5; got != want {
		t.Fatalf("graph has %d nodes, want %d", got, want)
	}
	assertEntry(t, graph, "raw", nil, []string{"left", "right"})
	assertEntry(t, graph, "left", []string{"raw"}, []string{"join"})
	assertEntry(t, graph, "right", []string{"raw"}, []string{"join"})
	assertEntry(t, graph, "join", []string{"left", "right"}, []string{"present"})
	assertEntry(t, graph, "present", []string{"join"}, nil)
	if got, want := Roots(graph), []string{"raw"}; !reflect.DeepEqual(got, want) {
		t.Errorf("Roots = %v, want %v", got, want)
	}
}

// Removing the dependency that closes a cyclic region makes the document
// importable again: the remaining structure may still look like almost a
// cycle and keep multiple branches, but with no route back to itself the
// whole document — every node and every remaining direct dependency —
// imports successfully.
func TestImportLineageCycleBrokenByRemovingClosingEdgeImports(t *testing.T) {
	t.Run("single loop opened into a chain", func(t *testing.T) {
		cyclic := marshalLineageDoc(
			[]string{"x", "y", "z"},
			[2]string{"x", "y"}, [2]string{"y", "z"}, [2]string{"z", "x"},
		)
		assertImportFails(t, cyclic, "cycle")

		opened := marshalLineageDoc(
			[]string{"x", "y", "z"},
			[2]string{"x", "y"}, [2]string{"y", "z"}, // closing z -> x removed
		)
		graph := mustImport(t, opened)
		assertConsistent(t, graph)
		assertEntry(t, graph, "x", nil, []string{"y"})
		assertEntry(t, graph, "y", []string{"x"}, []string{"z"})
		assertEntry(t, graph, "z", []string{"y"}, nil)
	})

	t.Run("every loop opened", func(t *testing.T) {
		// Two disconnected cyclic regions; opening both closing dependencies
		// leaves a branched but acyclic document that imports in full.
		opened := marshalLineageDoc(
			[]string{"m1", "m2", "z1", "z2", "z3", "keep"},
			[2]string{"m1", "m2"},                        // m2 -> m1 removed
			[2]string{"z1", "z2"}, [2]string{"z2", "z3"}, // z3 -> z1 removed
		)
		graph := mustImport(t, opened)
		assertConsistent(t, graph)
		if got, want := len(graph), 6; got != want {
			t.Fatalf("graph has %d nodes, want %d", got, want)
		}
		assertEntry(t, graph, "m1", nil, []string{"m2"})
		assertEntry(t, graph, "m2", []string{"m1"}, nil)
		assertEntry(t, graph, "z1", nil, []string{"z2"})
		assertEntry(t, graph, "z2", []string{"z1"}, []string{"z3"})
		assertEntry(t, graph, "z3", []string{"z2"}, nil)
		assertEntry(t, graph, "keep", nil, nil)
	})

	t.Run("one loop still closed still rejects", func(t *testing.T) {
		// Opening only one of two disconnected cycles is not enough: the
		// remaining cycle still fails the whole import.
		edges := [][2]string{
			{"m1", "m2"},                             // m2 -> m1 removed: this region is now acyclic
			{"z1", "z2"}, {"z2", "z3"}, {"z3", "z1"}, // still closed
		}
		graph, err := ImportLineage(marshalLineageDoc(
			[]string{"m1", "m2", "z1", "z2", "z3"}, edges...))
		if graph != nil || err == nil {
			t.Fatalf("want nil graph and cycle error, got graph=%v err=%v", graph, err)
		}
		if want := "lineage contains a cycle: z1 -> z2 -> z3 -> z1"; err.Error() != want {
			t.Errorf("cycle error = %q, want %q", err.Error(), want)
		}
		assertCycleErrorPath(t, err, edges...)
	})
}
