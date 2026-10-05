package chainledger

import (
	"reflect"
	"slices"
	"strings"
	"testing"
)

// buildCutoffDetourMergeGraph builds the easy-to-misjudge merge used by the
// rename-after-cutoff regressions:
//
//	s -> cut -------> join -> tail
//	 |                 ^
//	 +-> a -> z -------+
//	 |                 |
//	 +-> b -> c -------+
//
// From s the derived dataset join has one SHORT route s -> cut -> join of
// length 2, but it runs through the cutoff cut. Two equally long DETOUR routes
// avoid the cutoff and merge at join: s -> a -> z -> join and
// s -> b -> c -> join, both of length 3; tail sits one hop past the merge.
// The two detours first differ at hop 1 where a < b, while the merge node's
// direct upstreams order as c < z — opposite verdicts, so a rule that looks
// only at the last name before the merge cannot choose correctly.
func buildCutoffDetourMergeGraph(t *testing.T) map[string]*Lineage {
	t.Helper()
	return buildRegisteredGraph(t, [][]string{
		{"s"},
		{"cut", "s"},              // short, cutoff-carrying route
		{"a", "s"},                // first detour, hop 1
		{"b", "s"},                // second detour, hop 1
		{"z", "a"},                // first detour, hop 2 (z sorts past c)
		{"c", "b"},                // second detour, hop 2 (c sorts ahead of z)
		{"join", "cut", "z", "c"}, // c listed ahead of z on purpose
		{"tail", "join"},
	})
}

// Regression for re-querying ImpactsWithCutoffs after Rename. With cut as the
// cutoff, the short route s -> cut -> join is truncated: cut stays in the
// result at distance 1 but must not explain anything past it, so join is
// reached around it on the two equal detours at distance 3 and tail at 4.
// The detours tie on edge count; comparing full paths hop by hop from the
// origin selects the a/z route because a < b at hop 1, even though c < z at
// the merge. Renaming the selected route's middle dataset a to q — a name
// that sorts past b — changes no dependency, reachable set or distance, but
// the merge node and its downstream tail must now both be explained by the
// other allowed route through b/c. The bypass edge count still decides over
// the truncated short route: join may neither shorten back to 2 via cut nor
// be listed twice, and the freed name a must survive in neither record nor
// explanation path. Result order stays distance ascending and current name
// within a distance, independent of registration and upstream-list order.
func TestImpactsWithCutoffsAfterRenameDetourFlipsExplanation(t *testing.T) {
	graph := buildCutoffDetourMergeGraph(t)
	assertConsistent(t, graph)

	// Anchor the unrestricted behavior that the cutoff must override: without
	// cutoffs the short route through cut wins on edge count.
	full := mustImpacts(t, graph, "s")
	assertImpactOnce(t, full, "join", 2, []string{"s", "cut", "join"})
	assertImpactOnce(t, full, "tail", 3, []string{"s", "cut", "join", "tail"})

	// Cutoff query before the rename: the short route is dead for anything
	// beyond cut, so both the merge node and the downstream past it survive
	// only at the detour distances.
	wantBefore := []Impact{
		{Dataset: "a", Distance: 1, Path: []string{"s", "a"}},
		{Dataset: "b", Distance: 1, Path: []string{"s", "b"}},
		{Dataset: "cut", Distance: 1, Path: []string{"s", "cut"}},
		{Dataset: "c", Distance: 2, Path: []string{"s", "b", "c"}},
		{Dataset: "z", Distance: 2, Path: []string{"s", "a", "z"}},
		{Dataset: "join", Distance: 3, Path: []string{"s", "a", "z", "join"}},
		{Dataset: "tail", Distance: 4, Path: []string{"s", "a", "z", "join", "tail"}},
	}
	before := mustImpactsWithCutoffs(t, graph, "s", "cut")
	if !reflect.DeepEqual(before, wantBefore) {
		t.Fatalf("cutoff query before rename = %v, want %v", before, wantBefore)
	}
	// The cutoff itself is present at its own distance, but the merge node's
	// explanation must not pass through it, and join is listed exactly once.
	assertImpactOnce(t, before, "cut", 1, []string{"s", "cut"})
	assertImpactOnce(t, before, "join", 3, []string{"s", "a", "z", "join"})
	assertImpactOnce(t, before, "tail", 4, []string{"s", "a", "z", "join", "tail"})
	for _, im := range before {
		if im.Dataset != "cut" && slices.Contains(im.Path[:len(im.Path)-1], "cut") {
			t.Fatalf("path to %s passes through cutoff cut: %v", im.Dataset, im.Path)
		}
	}
	beforeCopy := copyImpacts(before)

	// Rename the middle dataset on the selected detour. q sorts past b, so the
	// other allowed route becomes the smaller full path. Edges never move.
	mustRename(t, graph, "a", "q")
	assertConsistent(t, graph)
	if _, ok := graph["a"]; ok {
		t.Fatal("freed name a still registered after rename")
	}
	if got, want := len(graph), 8; got != want {
		t.Fatalf("dataset count = %d, want %d (rename adds nor removes no node)", got, want)
	}
	// The renamed node keeps a's exact slots: after s in the hop-1 list and
	// before z in z's parent list; every other list keeps its order.
	assertEntry(t, graph, "s", nil, []string{"cut", "q", "b"})
	assertEntry(t, graph, "q", []string{"s"}, []string{"z"})
	assertEntry(t, graph, "z", []string{"q"}, []string{"join"})
	assertEntry(t, graph, "b", []string{"s"}, []string{"c"})
	assertEntry(t, graph, "c", []string{"b"}, []string{"join"})
	assertEntry(t, graph, "cut", []string{"s"}, []string{"join"})
	assertEntry(t, graph, "join", []string{"cut", "z", "c"}, []string{"tail"})

	// Re-query with the same cutoff: scope and distances keep their meaning
	// (only the renamed dataset wears its new name), and the merge node plus
	// its downstream now explain themselves on the other allowed detour.
	wantAfter := []Impact{
		{Dataset: "b", Distance: 1, Path: []string{"s", "b"}},
		{Dataset: "cut", Distance: 1, Path: []string{"s", "cut"}},
		{Dataset: "q", Distance: 1, Path: []string{"s", "q"}},
		{Dataset: "c", Distance: 2, Path: []string{"s", "b", "c"}},
		{Dataset: "z", Distance: 2, Path: []string{"s", "q", "z"}},
		{Dataset: "join", Distance: 3, Path: []string{"s", "b", "c", "join"}},
		{Dataset: "tail", Distance: 4, Path: []string{"s", "b", "c", "join", "tail"}},
	}
	after := mustImpactsWithCutoffs(t, graph, "s", "cut")
	if !reflect.DeepEqual(after, wantAfter) {
		t.Fatalf("cutoff query after rename = %v, want %v", after, wantAfter)
	}
	// The flips: not the q/z route (q > b at hop 1), and never the short
	// route through cut — distance stays the detour edge count of 3.
	assertImpactOnce(t, after, "join", 3, []string{"s", "b", "c", "join"})
	assertImpactOnce(t, after, "tail", 4, []string{"s", "b", "c", "join", "tail"})
	assertImpactOnce(t, after, "cut", 1, []string{"s", "cut"})
	assertImpactOnce(t, after, "q", 1, []string{"s", "q"})
	assertImpactOnce(t, after, "z", 2, []string{"s", "q", "z"})

	// The freed name must survive in neither records nor explanation paths,
	// and the affected scope is exactly the old one with a swapped for q.
	if names := impactNames(after); slices.Contains(names, "a") {
		t.Fatalf("freed name a leaked into records %v", names)
	}
	for _, im := range after {
		if slices.Contains(im.Path, "a") {
			t.Fatalf("freed name a leaked into path of %+v", im)
		}
		if im.Dataset != "cut" && slices.Contains(im.Path[:len(im.Path)-1], "cut") {
			t.Fatalf("path to %s passes through cutoff cut after rename: %v", im.Dataset, im.Path)
		}
		if len(im.Path) != im.Distance+1 || im.Path[0] != "s" || im.Path[len(im.Path)-1] != im.Dataset {
			t.Fatalf("malformed record after rename: %+v", im)
		}
	}
	if got, want := impactNames(after), []string{"b", "cut", "q", "c", "z", "join", "tail"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("distance/name order = %v, want %v", got, want)
	}

	// The unrestricted query keeps the short route after the rename as well:
	// cutting is a cutoff-query decision, not a graph change; the only visible
	// difference there is likewise a's new name and the distance-1 ordering.
	wantFullAfter := []Impact{
		{Dataset: "b", Distance: 1, Path: []string{"s", "b"}},
		{Dataset: "cut", Distance: 1, Path: []string{"s", "cut"}},
		{Dataset: "q", Distance: 1, Path: []string{"s", "q"}},
		{Dataset: "c", Distance: 2, Path: []string{"s", "b", "c"}},
		{Dataset: "join", Distance: 2, Path: []string{"s", "cut", "join"}},
		{Dataset: "z", Distance: 2, Path: []string{"s", "q", "z"}},
		{Dataset: "tail", Distance: 3, Path: []string{"s", "cut", "join", "tail"}},
	}
	if got := mustImpacts(t, graph, "s"); !reflect.DeepEqual(got, wantFullAfter) {
		t.Fatalf("unrestricted query after rename = %v, want %v", got, wantFullAfter)
	}

	// The result fetched before the rename keeps its old names and routes.
	if !reflect.DeepEqual(before, beforeCopy) {
		t.Fatalf("pre-rename cutoff result changed: got %v, snapshot %v", before, beforeCopy)
	}
}

// The merge's route flip must come from the current names alone, never from
// the order in which datasets were registered or the order in which join
// lists its direct upstreams. The same final lineage is rebuilt three ways:
// b-side branch registered first vs q-side first, and join's parents written
// with z and c in opposite orders. In every build cut is the cutoff, and the
// post-rename cutoff result must be identical, with join and tail explained
// through the b/c detour at the detour distances.
func TestImpactsWithCutoffsAfterRenameOrderInvariance(t *testing.T) {
	sequences := [][][]string{
		{
			{"s"},
			{"b", "s"}, // b-side branch registered first
			{"c", "b"},
			{"a", "s"},
			{"z", "a"},
			{"cut", "s"},
			{"join", "cut", "z", "c"}, // z listed ahead of c
			{"tail", "join"},
		},
		{
			{"s"},
			{"cut", "s"},
			{"a", "s"}, // a-side branch registered first
			{"z", "a"},
			{"b", "s"},
			{"c", "b"},
			{"join", "c", "cut", "z"}, // c first, z last
			{"tail", "join"},
		},
		{
			{"s"},
			{"b", "s"},
			{"a", "s"},
			{"cut", "s"},
			{"z", "a"},
			{"c", "b"},
			{"join", "cut", "c", "z"}, // c and z adjacent, opposite order again
			{"tail", "join"},
		},
	}

	want := []Impact{
		{Dataset: "b", Distance: 1, Path: []string{"s", "b"}},
		{Dataset: "cut", Distance: 1, Path: []string{"s", "cut"}},
		{Dataset: "q", Distance: 1, Path: []string{"s", "q"}},
		{Dataset: "c", Distance: 2, Path: []string{"s", "b", "c"}},
		{Dataset: "z", Distance: 2, Path: []string{"s", "q", "z"}},
		{Dataset: "join", Distance: 3, Path: []string{"s", "b", "c", "join"}},
		{Dataset: "tail", Distance: 4, Path: []string{"s", "b", "c", "join", "tail"}},
	}

	for i, seq := range sequences {
		graph := buildRegisteredGraph(t, seq)
		mustRename(t, graph, "a", "q")
		assertConsistent(t, graph)
		snap := snapshot(graph)

		got := mustImpactsWithCutoffs(t, graph, "s", "cut")
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("build %d: cutoff result = %v, want %v", i, got, want)
		}
		assertImpactOnce(t, got, "join", 3, []string{"s", "b", "c", "join"})
		assertImpactOnce(t, got, "tail", 4, []string{"s", "b", "c", "join", "tail"})
		// Neither registration history nor stored list order may decide routing,
		// and the query stays read-only.
		if !reflect.DeepEqual(snapshot(graph), snap) {
			t.Fatalf("build %d: cutoff query changed graph: before=%v after=%v", i, snap, snapshot(graph))
		}
	}
}

// Renaming the cutoff itself: once cut is renamed halt, the propagation stop
// must be addressable by the new name. halt is still returned at distance 1,
// propagation still ends there, and everything reachable around it (the two
// detours, join and tail) keeps coming back on the detour routes. The old
// name, not yet re-registered, fails the whole query when still listed as a
// cutoff: the error names it and results are nil — no partial impact scope
// may be returned by silently ignoring it. Neither outcome changes lineage
// relationships or stored list order.
func TestImpactsWithCutoffsCutoffRenamedNewNameStopsOldNameFails(t *testing.T) {
	graph := buildCutoffDetourMergeGraph(t)
	assertConsistent(t, graph)

	baseline := mustImpactsWithCutoffs(t, graph, "s", "cut")
	assertImpactOnce(t, baseline, "cut", 1, []string{"s", "cut"})
	assertImpactOnce(t, baseline, "join", 3, []string{"s", "a", "z", "join"})
	baselineCopy := copyImpacts(baseline)

	// Rename the cutoff node itself; edges and positions are untouched.
	mustRename(t, graph, "cut", "halt")
	assertConsistent(t, graph)
	assertEntry(t, graph, "halt", []string{"s"}, []string{"join"})
	assertEntry(t, graph, "s", nil, []string{"halt", "a", "b"})
	assertEntry(t, graph, "join", []string{"halt", "z", "c"}, []string{"tail"})
	snap := snapshot(graph)

	// The stop is now named halt: it still appears itself, and nothing
	// propagates through it. The downstream reachable around it survives on
	// the a/z detour (a < b at hop 1), still at the detour edge counts.
	want := []Impact{
		{Dataset: "a", Distance: 1, Path: []string{"s", "a"}},
		{Dataset: "b", Distance: 1, Path: []string{"s", "b"}},
		{Dataset: "halt", Distance: 1, Path: []string{"s", "halt"}},
		{Dataset: "c", Distance: 2, Path: []string{"s", "b", "c"}},
		{Dataset: "z", Distance: 2, Path: []string{"s", "a", "z"}},
		{Dataset: "join", Distance: 3, Path: []string{"s", "a", "z", "join"}},
		{Dataset: "tail", Distance: 4, Path: []string{"s", "a", "z", "join", "tail"}},
	}
	byNewName := mustImpactsWithCutoffs(t, graph, "s", "halt")
	if !reflect.DeepEqual(byNewName, want) {
		t.Fatalf("cutoff under new name = %v, want %v", byNewName, want)
	}
	assertImpactOnce(t, byNewName, "halt", 1, []string{"s", "halt"})
	assertImpactOnce(t, byNewName, "join", 3, []string{"s", "a", "z", "join"})
	assertImpactOnce(t, byNewName, "tail", 4, []string{"s", "a", "z", "join", "tail"})
	for _, im := range byNewName {
		if slices.Contains(im.Path, "cut") {
			t.Fatalf("freed cutoff name cut leaked into path of %+v", im)
		}
		if im.Dataset != "halt" && slices.Contains(im.Path[:len(im.Path)-1], "halt") {
			t.Fatalf("path to %s passes through renamed cutoff halt: %v", im.Dataset, im.Path)
		}
	}

	// The old cutoff name is unregistered. Naming it must fail the whole
	// request with nil results rather than quietly stop at nothing and return
	// the full (or a partly filtered) impact scope.
	impacts, err := ImpactsWithCutoffs(graph, "s", []string{"cut"})
	if err == nil || !strings.Contains(err.Error(), "cutoff dataset not found: cut") {
		t.Fatalf("freed cutoff name: want not-found error naming cut, got %v", err)
	}
	if impacts != nil {
		t.Fatalf("freed cutoff name: want nil results, got %v", impacts)
	}

	// A valid cutoff listed alongside the still-unregistered old name still
	// fails as a whole: no partial answer, and the first bad name is reported
	// in cutoff input order. Placing cut first reports cut; placing halt first
	// still reaches cut and reports it rather than succeeding partially.
	if got, err := ImpactsWithCutoffs(graph, "s", []string{"cut", "halt"}); err == nil ||
		!strings.Contains(err.Error(), "cutoff dataset not found: cut") {
		t.Fatalf("bad cutoff first: want error naming cut, got %v (results %v)", err, got)
	} else if got != nil {
		t.Fatalf("bad cutoff first: want nil results, got %v", got)
	}
	if got, err := ImpactsWithCutoffs(graph, "s", []string{"halt", "cut"}); err == nil ||
		!strings.Contains(err.Error(), "cutoff dataset not found: cut") {
		t.Fatalf("bad cutoff second: want error naming cut, got %v (results %v)", err, got)
	} else if got != nil {
		t.Fatalf("bad cutoff second: want nil results, got %v", got)
	}

	// Success and failure alike leave the renamed lineage and stored order
	// exactly as they were.
	if !reflect.DeepEqual(snapshot(graph), snap) {
		t.Fatalf("cutoff queries changed graph after rename: before=%v after=%v", snap, snapshot(graph))
	}
	assertEntry(t, graph, "halt", []string{"s"}, []string{"join"})
	assertEntry(t, graph, "join", []string{"halt", "z", "c"}, []string{"tail"})
	assertConsistent(t, graph)

	// Re-querying by the new name after the failures is unaffected.
	if got := mustImpactsWithCutoffs(t, graph, "s", "halt"); !reflect.DeepEqual(got, byNewName) {
		t.Fatalf("cutoff result after failed queries = %v, want %v", got, byNewName)
	}

	// The result fetched before the rename keeps its old cutoff name and route.
	if !reflect.DeepEqual(baseline, baselineCopy) {
		t.Fatalf("pre-rename cutoff result changed: got %v, snapshot %v", baseline, baselineCopy)
	}
}

// Once the freed cutoff name is registered again as a brand-new dataset it is
// a legal cutoff name again, but one with its own reachability only: the
// rename changed identities, not edges, so the re-registered cut is unrelated
// to halt's lineage and must not silently inherit the stop. Queries named in
// both orders behave by current graph membership, and nothing mutates stored
// relationships or order.
func TestImpactsWithCutoffsCutoffRenamedThenOldNameReregistered(t *testing.T) {
	graph := buildCutoffDetourMergeGraph(t)
	mustRename(t, graph, "cut", "halt")
	assertConsistent(t, graph)

	// While cut is still freed, a query that names it fails with nil results.
	if got, err := ImpactsWithCutoffs(graph, "s", []string{"cut"}); err == nil ||
		!strings.Contains(err.Error(), "cutoff dataset not found: cut") || got != nil {
		t.Fatalf("freed cut: want not-found error and nil results, got %v, %v", got, err)
	}

	// Register cut anew as an independent dataset unrelated to s. It now
	// validates as a cutoff name, but being unreachable from s it changes
	// nothing; halt still does the actual stopping.
	mustRegister(t, graph, "cut")
	assertConsistent(t, graph)
	if got, want := len(graph), 9; got != want {
		t.Fatalf("dataset count = %d, want %d", got, want)
	}
	snap := snapshot(graph)

	haltOnly := mustImpactsWithCutoffs(t, graph, "s", "halt")
	both := mustImpactsWithCutoffs(t, graph, "s", "halt", "cut")
	if !reflect.DeepEqual(both, haltOnly) {
		t.Fatalf("unreachable re-registered cutoff changed result: %v, want %v", both, haltOnly)
	}
	assertImpactOnce(t, both, "halt", 1, []string{"s", "halt"})
	assertImpactOnce(t, both, "join", 3, []string{"s", "a", "z", "join"})

	// Repeated mixed orders validate and agree.
	if got := mustImpactsWithCutoffs(t, graph, "s", "cut", "halt"); !reflect.DeepEqual(got, haltOnly) {
		t.Fatalf("cutoff order %v disagrees: %v, want %v", []string{"cut", "halt"}, got, haltOnly)
	}

	// The re-registered name is an isolated new node; halt's lineage and every
	// stored order are intact, and the queries changed nothing.
	if got := graph["cut"]; got == nil || len(got.Parents) != 0 || len(got.Children) != 0 {
		t.Fatalf("re-registered cut should be an isolated new node, got %+v", got)
	}
	assertEntry(t, graph, "halt", []string{"s"}, []string{"join"})
	assertEntry(t, graph, "join", []string{"halt", "z", "c"}, []string{"tail"})
	assertEntry(t, graph, "s", nil, []string{"halt", "a", "b"})
	if !reflect.DeepEqual(snapshot(graph), snap) {
		t.Fatalf("cutoff queries changed graph: before=%v after=%v", snap, snapshot(graph))
	}
}
