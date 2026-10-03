package chainledger

import (
	"reflect"
	"slices"
	"strings"
	"testing"
)

func sourceNames(sources []Source) []string {
	names := make([]string, len(sources))
	for i, s := range sources {
		names[i] = s.Name
	}
	return names
}

func assertSource(t *testing.T, sources []Source, name string, wantDistance int, wantPath []string) {
	t.Helper()
	for _, s := range sources {
		if s.Name != name {
			continue
		}
		if s.Distance != wantDistance {
			t.Errorf("source %s distance = %d, want %d", name, s.Distance, wantDistance)
		}
		if !sameStrings(s.Path, wantPath) {
			t.Errorf("source %s path = %v, want %v", name, s.Path, wantPath)
		}
		return
	}
	t.Fatalf("source %q missing from %v", name, sourceNames(sources))
}

// assertSourceOnce checks the entry for name and that name appears exactly once
// in the result, even where several branches derive from the same source.
func assertSourceOnce(t *testing.T, sources []Source, name string, wantDistance int, wantPath []string) {
	t.Helper()
	count := 0
	for _, s := range sources {
		if s.Name == name {
			count++
		}
	}
	if count > 1 {
		t.Fatalf("source %q appears %d times in %v, want exactly once", name, count, sourceNames(sources))
	}
	assertSource(t, sources, name, wantDistance, wantPath)
}

func mustSources(t *testing.T, graph map[string]*Lineage, dataset string) []Source {
	t.Helper()
	sources, err := Sources(graph, dataset)
	if err != nil {
		t.Fatalf("Sources(%s): %v", dataset, err)
	}
	return sources
}

// The worked example from the spec: source feeds a and b; a feeds z; b feeds c;
// report depends on z and c together (c declared ahead of z on purpose); view
// depends on report. Querying report must trace source at distance 3 along the
// a -> z route even though c sorts ahead of z, because full paths are compared
// from the source and a < b.
func TestSourcesMergedBranchesFullPathTieBreak(t *testing.T) {
	// Each sequence builds the same graph with a legal but different
	// registration order; the second also flips report's upstream list, and the
	// third registers the isolated node early. Names, distances and paths must
	// come out identical regardless.
	sequences := [][][]string{
		{
			{"source"},
			{"b", "source"}, // b-side branch registered first
			{"a", "source"},
			{"c", "b"},
			{"z", "a"},
			{"report", "c", "z"}, // c listed ahead of z on purpose
			{"view", "report"},
			{"isolated"},
		},
		{
			{"source"},
			{"a", "source"}, // opposite branch registered first
			{"z", "a"},
			{"b", "source"},
			{"c", "b"},
			{"report", "z", "c"}, // direct-upstream order flipped
			{"view", "report"},
			{"isolated"},
		},
		{
			{"source"},
			{"isolated"},
			{"b", "source"},
			{"a", "source"},
			{"z", "a"},
			{"c", "b"},
			{"report", "c", "z"},
			{"view", "report"},
		},
	}

	wantReport := []Source{
		{Name: "c", Distance: 1, Path: []string{"c", "report"}},
		{Name: "z", Distance: 1, Path: []string{"z", "report"}},
		{Name: "a", Distance: 2, Path: []string{"a", "z", "report"}},
		{Name: "b", Distance: 2, Path: []string{"b", "c", "report"}},
		{Name: "source", Distance: 3, Path: []string{"source", "a", "z", "report"}},
	}
	wantView := []Source{
		{Name: "report", Distance: 1, Path: []string{"report", "view"}},
		{Name: "c", Distance: 2, Path: []string{"c", "report", "view"}},
		{Name: "z", Distance: 2, Path: []string{"z", "report", "view"}},
		{Name: "a", Distance: 3, Path: []string{"a", "z", "report", "view"}},
		{Name: "b", Distance: 3, Path: []string{"b", "c", "report", "view"}},
		{Name: "source", Distance: 4, Path: []string{"source", "a", "z", "report", "view"}},
	}

	for i, seq := range sequences {
		graph := buildRegisteredGraph(t, seq)
		assertConsistent(t, graph)
		before := snapshot(graph)

		got := mustSources(t, graph, "report")
		if !reflect.DeepEqual(got, wantReport) {
			t.Fatalf("sequence %d: Sources(report) = %v, want %v", i, got, wantReport)
		}
		// Explicit anchors: source is explained exactly once through a/z even
		// though c sorts ahead of z, and the path starts with the source itself.
		assertSourceOnce(t, got, "source", 3, []string{"source", "a", "z", "report"})

		gotView := mustSources(t, graph, "view")
		if !reflect.DeepEqual(gotView, wantView) {
			t.Fatalf("sequence %d: Sources(view) = %v, want %v", i, gotView, wantView)
		}
		assertSourceOnce(t, gotView, "source", 4, []string{"source", "a", "z", "report", "view"})

		// The queried dataset, its downstreams and an unconnected dataset never
		// appear.
		if names := sourceNames(got); slices.Contains(names, "report") ||
			slices.Contains(names, "view") || slices.Contains(names, "isolated") {
			t.Fatalf("sequence %d: queried dataset, downstream or isolated dataset leaked into %v", i, names)
		}
		if names := sourceNames(gotView); slices.Contains(names, "view") || slices.Contains(names, "isolated") {
			t.Fatalf("sequence %d: queried dataset or isolated dataset leaked into %v", i, names)
		}

		// The query changes no node, relationship or stored list order.
		if !reflect.DeepEqual(snapshot(graph), before) {
			t.Fatalf("sequence %d: query changed graph: before=%v after=%v", i, before, snapshot(graph))
		}
	}
}

// With a direct source->report edge added on top of the merge scenario, edge
// count takes priority: source shortens to distance 1 with path
// [source report], and the lexicographically smaller length-3 route through a
// must not survive. The direct edge's position in report's declared list must
// not change the result.
func TestSourcesMergedBranchesDirectEdgeShortensMerge(t *testing.T) {
	builds := []struct {
		registrations [][]string
		newParents    []string
	}{
		{
			registrations: [][]string{
				{"source"},
				{"b", "source"},
				{"a", "source"},
				{"c", "b"},
				{"z", "a"},
				{"report", "c", "z"},
				{"view", "report"},
				{"isolated"},
			},
			newParents: []string{"source", "c", "z"}, // direct edge first
		},
		{
			registrations: [][]string{
				{"source"},
				{"a", "source"},
				{"z", "a"},
				{"b", "source"},
				{"c", "b"},
				{"report", "z", "c"},
				{"view", "report"},
				{"isolated"},
			},
			newParents: []string{"c", "z", "source"}, // direct edge last
		},
	}

	want := []Source{
		{Name: "c", Distance: 1, Path: []string{"c", "report"}},
		{Name: "source", Distance: 1, Path: []string{"source", "report"}},
		{Name: "z", Distance: 1, Path: []string{"z", "report"}},
		{Name: "a", Distance: 2, Path: []string{"a", "z", "report"}},
		{Name: "b", Distance: 2, Path: []string{"b", "c", "report"}},
	}

	for i, build := range builds {
		graph := buildRegisteredGraph(t, build.registrations)
		mustRegister(t, graph, "report", build.newParents...)
		assertConsistent(t, graph)

		got := mustSources(t, graph, "report")
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("build %d: Sources(report) = %v, want %v", i, got, want)
		}
		assertSourceOnce(t, got, "source", 1, []string{"source", "report"})

		// view inherits the shortened explanation one hop further on.
		gotView := mustSources(t, graph, "view")
		assertSourceOnce(t, gotView, "source", 2, []string{"source", "report", "view"})
		assertSourceOnce(t, gotView, "report", 1, []string{"report", "view"})
	}
}

// Among multiple shortest paths the lexicographically smallest full name
// sequence wins, compared name by name from the source in Go string order, even
// when stored parent order would suggest otherwise. Distance still beats
// lexicographic order when routes differ in length.
func TestSourcesLexicographicallySmallestShortestPath(t *testing.T) {
	graph := map[string]*Lineage{}
	mustRegister(t, graph, "s")
	// Register the lexicographically larger branch first so stored parent order
	// disagrees with the expected path choice.
	mustRegister(t, graph, "z", "s")
	mustRegister(t, graph, "a", "s")
	mustRegister(t, graph, "m", "z", "a")

	sources := mustSources(t, graph, "m")
	assertSource(t, sources, "s", 2, []string{"s", "a", "m"})

	// Tie decided at the final hop: both x routes from s to n have equal length.
	mustRegister(t, graph, "x1", "s")
	mustRegister(t, graph, "x2", "s")
	mustRegister(t, graph, "n", "x2", "x1")
	sources = mustSources(t, graph, "n")
	assertSource(t, sources, "s", 2, []string{"s", "x1", "n"})

	// A longer-looking but lexicographically smaller route must not replace a
	// genuinely shorter path: distance takes priority. q joins s at distance 2
	// via z, and at distance 3 via a -> m.
	mustRegister(t, graph, "q", "m", "z")
	sources = mustSources(t, graph, "q")
	assertSource(t, sources, "s", 2, []string{"s", "z", "q"})
}

// Same-distance results are ordered by source name regardless of the order in
// which parents were registered; a shared ancestor appears once.
func TestSourcesOrderedByDistanceThenName(t *testing.T) {
	graph := map[string]*Lineage{}
	mustRegister(t, graph, "root")
	mustRegister(t, graph, "zebra", "root")
	mustRegister(t, graph, "alpha", "root")
	mustRegister(t, graph, "mid", "root")
	mustRegister(t, graph, "merge", "zebra", "alpha", "mid")

	sources := mustSources(t, graph, "merge")
	want := []struct {
		name string
		dist int
	}{
		{"alpha", 1}, {"mid", 1}, {"zebra", 1},
		{"root", 2},
	}
	if len(sources) != len(want) {
		t.Fatalf("got %d sources %v, want %d", len(sources), sourceNames(sources), len(want))
	}
	for i, w := range want {
		if sources[i].Name != w.name || sources[i].Distance != w.dist {
			t.Fatalf("position %d = (%s,%d), want (%s,%d); full=%v",
				i, sources[i].Name, sources[i].Distance, w.name, w.dist, sources)
		}
	}
	// root feeds merge through three equal branches but appears once, explained
	// through the lexicographically smallest first hop.
	assertSourceOnce(t, sources, "root", 2, []string{"root", "alpha", "merge"})

	// A single chain reports distance and path straight through.
	mustRegister(t, graph, "leaf-z", "zebra")
	chain := mustSources(t, graph, "leaf-z")
	assertSource(t, chain, "zebra", 1, []string{"zebra", "leaf-z"})
	assertSource(t, chain, "root", 2, []string{"root", "zebra", "leaf-z"})
}

// A registered dataset without upstreams succeeds with an empty (but non-nil)
// list; independent datasets, the queried dataset itself and its downstreams
// never appear.
func TestSourcesEmptyAndScope(t *testing.T) {
	graph := map[string]*Lineage{}
	mustRegister(t, graph, "lonely")
	mustRegister(t, graph, "other")
	mustRegister(t, graph, "p")
	mustRegister(t, graph, "c", "p")
	mustRegister(t, graph, "grandchild", "c")

	sources, err := Sources(graph, "lonely")
	if err != nil {
		t.Fatalf("Sources(lonely): %v", err)
	}
	if sources == nil || len(sources) != 0 {
		t.Fatalf("want empty non-nil list, got %v", sources)
	}

	sources = mustSources(t, graph, "c")
	if got, want := sourceNames(sources), []string{"p"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("independent datasets leaked: got %v, want %v", got, want)
	}
	assertSource(t, sources, "p", 1, []string{"p", "c"})

	// grandchild's own downstream scope is irrelevant upstream; c and p appear.
	sources = mustSources(t, graph, "grandchild")
	if got, want := sourceNames(sources), []string{"c", "p"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("Sources(grandchild) = %v, want %v", got, want)
	}
	for _, s := range sources {
		if s.Name == "grandchild" {
			t.Fatal("queried dataset must not appear in its own source list")
		}
	}
}

func TestSourcesErrors(t *testing.T) {
	graph := map[string]*Lineage{}
	mustRegister(t, graph, "a")

	sources, err := Sources(graph, "")
	if err == nil || !strings.Contains(err.Error(), "name is required") {
		t.Fatalf("empty name: want required-name error, got %v", err)
	}
	if sources != nil {
		t.Fatalf("empty name: want nil results, got %v", sources)
	}

	sources, err = Sources(graph, "ghost")
	if err == nil || !strings.Contains(err.Error(), "ghost") {
		t.Fatalf("unknown dataset: want error naming ghost, got %v", err)
	}
	if sources != nil {
		t.Fatalf("unknown dataset: want nil results, got %v", sources)
	}

	// Non-empty name against an initialized-but-empty graph, and against nil.
	sources, err = Sources(map[string]*Lineage{}, "ghost")
	if err == nil || !strings.Contains(err.Error(), "ghost") || sources != nil {
		t.Fatalf("empty graph: want naming error and nil results, got %v, %v", sources, err)
	}
	sources, err = Sources(nil, "ghost")
	if err == nil || !strings.Contains(err.Error(), "ghost") || sources != nil {
		t.Fatalf("nil graph: want naming error and nil results, got %v, %v", sources, err)
	}
}

// Names match by exact registered value.
func TestSourcesExactNameMatch(t *testing.T) {
	graph := map[string]*Lineage{}
	mustRegister(t, graph, "raw")
	mustRegister(t, graph, "RAW", "raw")

	if _, err := Sources(graph, "RaW"); err == nil || !strings.Contains(err.Error(), "RaW") {
		t.Fatalf("case-insensitive match: want error naming RaW, got %v", err)
	}
	sources := mustSources(t, graph, "RAW")
	assertSource(t, sources, "raw", 1, []string{"raw", "RAW"})
}

// A query never mutates the graph, and mutating one returned record's path
// cannot reach back into the graph, any other record, or a later query.
func TestSourcesReadOnlyAndIsolated(t *testing.T) {
	graph := map[string]*Lineage{}
	mustRegister(t, graph, "source")
	mustRegister(t, graph, "a", "source")
	mustRegister(t, graph, "b", "source")
	mustRegister(t, graph, "report", "a", "b")
	before := snapshot(graph)

	sources, err := Sources(graph, "report")
	if err != nil {
		t.Fatalf("Sources(report): %v", err)
	}
	if !reflect.DeepEqual(snapshot(graph), before) {
		t.Fatalf("query changed graph: before=%v after=%v", before, snapshot(graph))
	}

	// Deep copy the expected result: the paths are what the abuse below mutates.
	good := make([]Source, len(sources))
	for i, s := range sources {
		good[i] = Source{s.Name, s.Distance, append([]string(nil), s.Path...)}
	}
	sources[0].Path[0] = "tampered"
	sources[0].Path = append(sources[0].Path, "extra")
	sources[0].Name = "tampered"
	sources[0].Distance = 99
	if !reflect.DeepEqual(snapshot(graph), before) {
		t.Fatalf("mutating results changed graph: before=%v after=%v", before, snapshot(graph))
	}
	if got := mustSources(t, graph, "report"); !reflect.DeepEqual(got, good) {
		t.Fatalf("fresh query = %v, want %v (earlier mutation must not leak)", got, good)
	}

	// A failed query is also read-only.
	if _, err := Sources(graph, "missing"); err == nil {
		t.Fatal("expected not-found error")
	}
	if !reflect.DeepEqual(snapshot(graph), before) {
		t.Fatalf("failed query changed graph: before=%v after=%v", before, snapshot(graph))
	}
	assertConsistent(t, graph)
}

// After report is re-registered directly under source, distances shorten and
// the previously returned result keeps its original content because paths are
// independent copies.
func TestSourcesReflectReregisterAndOldResultStable(t *testing.T) {
	graph := map[string]*Lineage{}
	mustRegister(t, graph, "source")
	mustRegister(t, graph, "a", "source")
	mustRegister(t, graph, "b", "source")
	mustRegister(t, graph, "report", "a", "b")
	mustRegister(t, graph, "view", "report")

	before := mustSources(t, graph, "report")
	beforeCopy := make([]Source, len(before))
	for i, s := range before {
		beforeCopy[i] = Source{s.Name, s.Distance, append([]string(nil), s.Path...)}
	}

	// report now depends directly on source as well; a and b remain its parents.
	mustRegister(t, graph, "report", "source", "a", "b")

	after := mustSources(t, graph, "report")
	if got, want := sourceNames(after), []string{"a", "b", "source"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("source order = %v, want %v", got, want)
	}
	assertSource(t, after, "source", 1, []string{"source", "report"})

	afterView := mustSources(t, graph, "view")
	assertSource(t, afterView, "source", 2, []string{"source", "report", "view"})

	if !reflect.DeepEqual(before, beforeCopy) {
		t.Fatalf("earlier result changed after re-register/new query: before=%v snapshot=%v", before, beforeCopy)
	}
}

// Re-wiring report from [a b] to [b] drops the direct a -> report edge. a and
// raw still explain report via the longer route through b, but their distances
// grow: everything is recomputed under the current edges rather than inherited.
func TestSourcesAfterRewireLongerPathSurvives(t *testing.T) {
	graph := map[string]*Lineage{}
	mustRegister(t, graph, "raw")
	mustRegister(t, graph, "a", "raw")
	mustRegister(t, graph, "b", "a")
	mustRegister(t, graph, "report", "a", "b")
	mustRegister(t, graph, "view", "report")

	before := mustSources(t, graph, "view")
	assertSource(t, before, "report", 1, []string{"report", "view"})
	assertSource(t, before, "a", 2, []string{"a", "report", "view"})
	assertSource(t, before, "b", 2, []string{"b", "report", "view"})
	assertSourceOnce(t, before, "raw", 3, []string{"raw", "a", "report", "view"})

	// Drop the direct a -> report edge; only the route through b remains.
	mustRegister(t, graph, "report", "b")
	assertConsistent(t, graph)

	sources := mustSources(t, graph, "view")
	if got, want := sourceNames(sources), []string{"report", "b", "a", "raw"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("source order = %v, want %v", got, want)
	}
	assertSourceOnce(t, sources, "b", 2, []string{"b", "report", "view"})
	assertSourceOnce(t, sources, "a", 3, []string{"a", "b", "report", "view"})
	assertSourceOnce(t, sources, "raw", 4, []string{"raw", "a", "b", "report", "view"})
}

// When the re-wire severs the last lineage path to an old source, that source
// leaves the queried subtree's provenance together with anything only reachable
// through it; the new source explains the moved subtree rooted at itself.
func TestSourcesAfterRewireLastPathRemoved(t *testing.T) {
	graph := map[string]*Lineage{}
	mustRegister(t, graph, "raw")
	mustRegister(t, graph, "mid", "raw")
	mustRegister(t, graph, "leaf", "mid")
	mustRegister(t, graph, "newsrc")

	leafBefore := mustSources(t, graph, "leaf")
	assertSource(t, leafBefore, "mid", 1, []string{"mid", "leaf"})
	assertSource(t, leafBefore, "raw", 2, []string{"raw", "mid", "leaf"})

	// mid moves from raw to newsrc, keeping its own downstream leaf.
	mustRegister(t, graph, "mid", "newsrc")
	assertConsistent(t, graph)

	leafAfter := mustSources(t, graph, "leaf")
	if got, want := sourceNames(leafAfter), []string{"mid", "newsrc"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("leaf provenance = %v, want %v (raw must be gone)", got, want)
	}
	assertSourceOnce(t, leafAfter, "mid", 1, []string{"mid", "leaf"})
	assertSourceOnce(t, leafAfter, "newsrc", 2, []string{"newsrc", "mid", "leaf"})

	midAfter := mustSources(t, graph, "mid")
	if got, want := sourceNames(midAfter), []string{"newsrc"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("mid provenance = %v, want %v", got, want)
	}

	// Cutting mid loose entirely (no upstreams) leaves only mid itself as
	// leaf's direct upstream; newsrc no longer explains leaf.
	mustRegister(t, graph, "mid")
	assertConsistent(t, graph)
	leafFinal, err := Sources(graph, "leaf")
	if err != nil {
		t.Fatalf("Sources(leaf): %v", err)
	}
	if got, want := sourceNames(leafFinal), []string{"mid"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("leaf provenance = %v, want %v", got, want)
	}

	// newsrc is still a valid query and answers with an empty list.
	sources, err := Sources(graph, "newsrc")
	if err != nil {
		t.Fatalf("Sources(newsrc): %v", err)
	}
	if sources == nil || len(sources) != 0 {
		t.Fatalf("want empty non-nil list from newsrc, got %v", sources)
	}
}

// Same final graph, opposite registration and upstream-list orders: the provenance
// result must be identical (registration history must not matter).
func TestSourcesOrderInvarianceAcrossBuilds(t *testing.T) {
	build := func(parents ...string) map[string]*Lineage {
		graph := map[string]*Lineage{}
		mustRegister(t, graph, "o")
		mustRegister(t, graph, "x2", "o") // larger name registered first on purpose
		mustRegister(t, graph, "x1", "o")
		mustRegister(t, graph, "d", parents...)
		mustRegister(t, graph, "e", "d")
		return graph
	}

	direct := build("x2", "x1")
	rewired := build("x1")
	mustRegister(t, rewired, "d", "x2", "x1")
	assertConsistent(t, rewired)

	want := mustSources(t, direct, "e")
	assertSourceOnce(t, want, "d", 1, []string{"d", "e"})
	assertSourceOnce(t, want, "x1", 2, []string{"x1", "d", "e"})
	assertSourceOnce(t, want, "x2", 2, []string{"x2", "d", "e"})
	assertSourceOnce(t, want, "o", 3, []string{"o", "x1", "d", "e"})
	if got, wantNames := sourceNames(want), []string{"d", "x1", "x2", "o"}; !reflect.DeepEqual(got, wantNames) {
		t.Fatalf("source order = %v, want %v", got, wantNames)
	}

	if got := mustSources(t, rewired, "e"); !reflect.DeepEqual(got, want) {
		t.Fatalf("re-wired graph sources = %v, want %v (registration history must not matter)", got, want)
	}

	flipped := map[string]*Lineage{}
	mustRegister(t, flipped, "o")
	mustRegister(t, flipped, "x1", "o")
	mustRegister(t, flipped, "x2", "o")
	mustRegister(t, flipped, "d", "x1", "x2")
	mustRegister(t, flipped, "e", "d")
	if got := mustSources(t, flipped, "e"); !reflect.DeepEqual(got, want) {
		t.Fatalf("flipped build sources = %v, want %v (registration and list order must not matter)", got, want)
	}
}

// A rejected re-wire (unknown upstream, or an upstream that would close a
// cycle through the dataset's own downstream) must leave every previously
// queryable provenance relationship exactly as it was.
func TestFailedRewireLeavesSourcesUntouched(t *testing.T) {
	graph := map[string]*Lineage{}
	mustRegister(t, graph, "raw")
	mustRegister(t, graph, "mid", "raw")
	mustRegister(t, graph, "leaf", "mid")
	mustRegister(t, graph, "newsrc")
	mustRegister(t, graph, "newleaf", "newsrc")

	leafBefore := mustSources(t, graph, "leaf")
	newBefore := mustSources(t, graph, "newleaf")
	before := snapshot(graph)

	// Unknown upstream: the error names the missing dataset.
	err := Register(graph, Dataset{Name: "mid"}, []string{"newsrc", "ghost"})
	if err == nil || !strings.Contains(err.Error(), "ghost") {
		t.Fatalf("want unknown-parent error naming ghost, got %v", err)
	}

	// Cyclic upstream: pointing mid at its own downstream leaf must name leaf.
	err = Register(graph, Dataset{Name: "mid"}, []string{"leaf"})
	if err == nil || !strings.Contains(err.Error(), "cycle") || !strings.Contains(err.Error(), "leaf") {
		t.Fatalf("want cycle error naming leaf, got %v", err)
	}

	if !reflect.DeepEqual(snapshot(graph), before) {
		t.Fatalf("rejected re-wires changed graph: before=%v after=%v", before, snapshot(graph))
	}
	if got := mustSources(t, graph, "leaf"); !reflect.DeepEqual(got, leafBefore) {
		t.Fatalf("leaf sources changed after rejected re-wires: got %v, want %v", got, leafBefore)
	}
	if got := mustSources(t, graph, "newleaf"); !reflect.DeepEqual(got, newBefore) {
		t.Fatalf("newleaf sources changed after rejected re-wires: got %v, want %v", got, newBefore)
	}
	assertSource(t, leafBefore, "mid", 1, []string{"mid", "leaf"})
	assertSource(t, leafBefore, "raw", 2, []string{"raw", "mid", "leaf"})
	assertConsistent(t, graph)
}
