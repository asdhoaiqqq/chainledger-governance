package chainledger

import (
	"reflect"
	"slices"
	"sort"
	"testing"
)

// This file is the regression guard for the relationship between the nearest
// common-upstream query and the complete upstream lineage export:
//
//   - CommonUpstreams(first, second) reports only the FRONTIER of the shared
//     lineage — the closest shared sources, each with one shortest, tie-broken
//     explanation path per side.
//   - ExportUpstreamLineage(target) preserves EVERY derivation branch of one
//     target, including direct edges that duplicate longer routes.
//
// The two surfaces show different scopes of the same registered edges, so they
// must never contradict each other about whether a source takes part in both
// targets' derivation: every source the query returns must appear in both
// exports, every frontier source derivable from the two exports must be
// returned, and each explanation path must exist hop by hop, in the derivation
// direction, inside the corresponding target's export — while the exports
// additionally keep the routes and hidden sources the query does not show.
//
// Expectations are derived independently from the exported documents and from
// a simple-walk oracle over the stored parent edges, never from the production
// traversals, so a wrong frontier, distance, path or branch selection on
// either side is caught.

// commonExportFixtureRegistrations builds a registered acyclic graph where
// several routes converge on two compared targets:
//
//	raw ──┬──> shared ──┬──> a ──> z ──┐
//	      │             └──> b ──> c ──┤
//	      │                            ├──> left ──┐
//	      ├──> peer ───────> mid ──┐   │           ├──> view
//	      │                        ├──> right ────┘
//	      └──────── direct raw edges into left and right ──┘
//
// shared reaches left over TWO equal-length routes (shared -> a -> z -> left
// and shared -> b -> c -> left, both 3 edges) and right directly; peer reaches
// left directly and right through mid; raw reaches both targets directly (the
// SHORTEST relation on each side) and through shared and peer. Comparing left
// and right must therefore return peer and shared — raw is hidden behind them
// despite its shorter direct edges — while the full exports of left and right
// still carry raw's direct edges and every longer branch. alien and alienleaf
// form a second, fully disjoint lineage.
func commonExportFixtureRegistrations() [][]string {
	return [][]string{
		{"raw"},
		{"shared", "raw"},
		{"peer", "raw"},
		{"b", "shared"}, // b-side branch registered first on purpose
		{"c", "b"},
		{"a", "shared"},
		{"z", "a"},
		{"mid", "peer"},
		{"left", "raw", "peer", "c", "z"}, // c listed ahead of z on purpose
		{"right", "raw", "shared", "mid"},
		{"view", "left", "right"}, // common downstream of both targets
		{"alien"},
		{"alienleaf", "alien"},
	}
}

// commonFrontierFromExports independently derives which sources a nearest
// common-upstream query may report, using only the two exported documents: a
// dataset participates in both derivations exactly when it is a node in both
// exports, and it is hidden exactly when some documented edge leads from it to
// another dataset that is also common (every direct dependency between two
// common nodes is carried by both exports, so one edge witnesses any longer
// downstream chain of common sources). The result is ordered by name, matching
// the query's presentation.
func commonFrontierFromExports(first, second exportDocument) []string {
	inFirst := map[string]bool{}
	for _, name := range first.Nodes {
		inFirst[name] = true
	}
	common := map[string]bool{}
	for _, name := range second.Nodes {
		if inFirst[name] {
			common[name] = true
		}
	}
	hidden := map[string]bool{}
	for _, doc := range []exportDocument{first, second} {
		for _, e := range doc.Edges {
			if common[e.From] && common[e.To] {
				hidden[e.From] = true
			}
		}
	}
	frontier := []string{}
	for name := range common {
		if !hidden[name] {
			frontier = append(frontier, name)
		}
	}
	sort.Strings(frontier)
	return frontier
}

// assertCommonSidesAgainstExports checks one query result for the ordered pair
// (first, second) against the two exported documents and the independent
// shortest-path oracle: the returned sources must be exactly the frontier
// derived from the exports, and each side's distance and path must be the
// minimum-edge, lexicographically smallest route from the source to THAT side's
// target — never borrowed from the other side — with every hop present as a
// documented direct dependency in that target's own export.
func assertCommonSidesAgainstExports(t *testing.T, graph map[string]*Lineage,
	oracle *lineageTestOracle, first, second string,
	firstDoc, secondDoc exportDocument, found []CommonUpstream) {
	t.Helper()

	if found == nil {
		t.Fatalf("CommonUpstreams(%q, %q) returned a nil list, want non-nil", first, second)
	}
	wantNames := commonFrontierFromExports(firstDoc, secondDoc)
	if got := commonNames(found); !reflect.DeepEqual(got, wantNames) {
		t.Fatalf("CommonUpstreams(%q, %q) sources = %v, want the frontier %v derived from the two exports",
			first, second, got, wantNames)
	}
	assertEachCommonOnce(t, found)

	for _, c := range found {
		// Participation must agree: a returned source is a node in BOTH exports.
		if !slices.Contains(firstDoc.Nodes, c.Dataset) || !slices.Contains(secondDoc.Nodes, c.Dataset) {
			t.Fatalf("common source %q missing from an export: %q nodes %v, %q nodes %v",
				c.Dataset, first, firstDoc.Nodes, second, secondDoc.Nodes)
		}

		for _, side := range []struct {
			target  string
			doc     exportDocument
			dist    int
			path    []string
			isFirst bool
		}{
			{first, firstDoc, c.DistanceToFirst, c.PathToFirst, true},
			{second, secondDoc, c.DistanceToSecond, c.PathToSecond, false},
		} {
			label := "second"
			if side.isFirst {
				label = "first"
			}
			wantDist, wantPath, reachable := oracle.shortest(c.Dataset, side.target)
			if !reachable {
				t.Fatalf("(%q, %q): oracle finds no route %q -> %q but the query reports one",
					first, second, c.Dataset, side.target)
			}
			if side.dist != wantDist || !reflect.DeepEqual(side.path, wantPath) {
				t.Fatalf("(%q, %q) source %q %s side = (distance %d, path %v), want (distance %d, path %v)",
					first, second, c.Dataset, label, side.dist, side.path, wantDist, wantPath)
			}
			if len(side.path) != side.dist+1 {
				t.Fatalf("(%q, %q) source %q %s side: path %v has %d edges, want distance %d",
					first, second, c.Dataset, label, side.path, len(side.path)-1, side.dist)
			}
			if side.path[0] != c.Dataset || side.path[len(side.path)-1] != side.target {
				t.Fatalf("(%q, %q) source %q %s side: path %v must run from the source to %q",
					first, second, c.Dataset, label, side.path, side.target)
			}
			// Every hop is a registered dependency and is documented, in the
			// derivation direction, by this target's own complete export.
			assertPathEdgesRegistered(t, graph, side.path)
			assertPathInsideDocument(t, side.doc, side.path)
		}
	}
}

// The anchored convergence scenario: comparing left and right returns peer and
// shared by name order; raw — despite holding the SHORTEST direct relation to
// both targets — is hidden behind the later common sources, and the
// intermediates that take part in only one target's derivation (a, z, b, c on
// the left, mid on the right) never mix into the result. Both complete exports
// keep raw's direct edges and every longer branch.
func TestCommonUpstreamsExportCorrespondenceMergingRoutes(t *testing.T) {
	graph := buildRegisteredGraph(t, commonExportFixtureRegistrations())
	assertConsistent(t, graph)
	before := snapshot(graph)

	found := mustCommon(t, graph, "left", "right")
	if got, want := commonNames(found), []string{"peer", "shared"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("common sources = %v, want %v (raw hidden behind the later shared sources)", got, want)
	}
	assertEachCommonOnce(t, found)
	// Each side is traced from its own target: peer is 1 edge from left but 2
	// from right; shared is 3 edges from left but 1 from right.
	assertCommon(t, found, "peer",
		1, []string{"peer", "left"},
		2, []string{"peer", "mid", "right"})
	// shared's two equal-length routes to left are tie-broken by the full name
	// sequence FROM THE SOURCE: shared -> a -> z -> left wins because a < b at
	// hop 1 — comparing only the names in front of the target (z vs c) would
	// wrongly pick the b/c route.
	assertCommon(t, found, "shared",
		3, []string{"shared", "a", "z", "left"},
		1, []string{"shared", "right"})

	// raw's shorter direct edges must not un-hide it, and the one-sided
	// intermediates must not mix into the shared frontier.
	for _, banned := range []string{"raw", "a", "z", "b", "c", "mid"} {
		if slices.Contains(commonNames(found), banned) {
			t.Fatalf("%q must not appear in the common sources %v", banned, commonNames(found))
		}
	}

	// Swapping the targets swaps the two sides field by field.
	swapped := mustCommon(t, graph, "right", "left")
	if got, want := commonNames(swapped), []string{"peer", "shared"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("swapped common sources = %v, want %v", got, want)
	}
	assertCommon(t, swapped, "peer",
		2, []string{"peer", "mid", "right"},
		1, []string{"peer", "left"})
	assertCommon(t, swapped, "shared",
		1, []string{"shared", "right"},
		3, []string{"shared", "a", "z", "left"})

	// The complete exports keep everything the query hid or did not choose:
	// raw's direct edges into both targets and every longer branch.
	leftDoc := parseExport(t, mustExport(t, graph, "left"))
	for _, edge := range [][2]string{
		{"raw", "left"},   // raw's direct edge, hidden by the query frontier
		{"raw", "shared"}, // the longer raw -> shared -> a -> z -> left branch...
		{"shared", "a"},
		{"a", "z"},
		{"z", "left"},
		{"shared", "b"}, // ...and the equally long b/c route the query did not pick
		{"b", "c"},
		{"c", "left"},
		{"raw", "peer"},
		{"peer", "left"},
	} {
		if !slices.Contains(exportEdgeNames(leftDoc), edge) {
			t.Errorf("left export must keep edge %s -> %s: %v", edge[0], edge[1], exportEdgeNames(leftDoc))
		}
	}
	rightDoc := parseExport(t, mustExport(t, graph, "right"))
	for _, edge := range [][2]string{
		{"raw", "right"}, // raw's direct edge on the right side
		{"raw", "shared"},
		{"shared", "right"},
		{"raw", "peer"},
		{"peer", "mid"},
		{"mid", "right"},
	} {
		if !slices.Contains(exportEdgeNames(rightDoc), edge) {
			t.Errorf("right export must keep edge %s -> %s: %v", edge[0], edge[1], exportEdgeNames(rightDoc))
		}
	}

	// Every explanation path the query returned exists hop by hop, in the
	// derivation direction, inside the corresponding target's own export.
	oracle := newLineageTestOracle(t, graph)
	assertCommonSidesAgainstExports(t, graph, oracle, "left", "right", leftDoc, rightDoc, found)
	assertCommonSidesAgainstExports(t, graph, oracle, "right", "left", rightDoc, leftDoc, swapped)

	if !reflect.DeepEqual(snapshot(graph), before) {
		t.Fatalf("queries and exports changed the graph: before=%v after=%v", before, snapshot(graph))
	}
	assertConsistent(t, graph)
}

// The exact public JSON text of both targets' complete exports is pinned, so
// the branch preservation the query correspondence relies on cannot silently
// shrink.
func TestCommonUpstreamsExportCorrespondenceExactExportJSON(t *testing.T) {
	graph := buildRegisteredGraph(t, commonExportFixtureRegistrations())

	wantLeft := `{"nodes":["a","b","c","left","peer","raw","shared","z"],` +
		`"edges":[` +
		`{"from":"a","to":"z"},` +
		`{"from":"b","to":"c"},` +
		`{"from":"c","to":"left"},` +
		`{"from":"peer","to":"left"},` +
		`{"from":"raw","to":"left"},` +
		`{"from":"raw","to":"peer"},` +
		`{"from":"raw","to":"shared"},` +
		`{"from":"shared","to":"a"},` +
		`{"from":"shared","to":"b"},` +
		`{"from":"z","to":"left"}]}`
	if out := mustExport(t, graph, "left"); out != wantLeft {
		t.Errorf("left export =\n%s\nwant:\n%s", out, wantLeft)
	}

	wantRight := `{"nodes":["mid","peer","raw","right","shared"],` +
		`"edges":[` +
		`{"from":"mid","to":"right"},` +
		`{"from":"peer","to":"mid"},` +
		`{"from":"raw","to":"peer"},` +
		`{"from":"raw","to":"right"},` +
		`{"from":"raw","to":"shared"},` +
		`{"from":"shared","to":"right"}]}`
	if out := mustExport(t, graph, "right"); out != wantRight {
		t.Errorf("right export =\n%s\nwant:\n%s", out, wantRight)
	}
}

// Exhaustive correspondence over every ordered pair of registered datasets:
// the query's sources must be exactly the frontier derived from the two
// complete exports, and each side's distance and path must be the
// minimum-edge, tie-broken route to that side's own target, fully present in
// that target's export. This covers same-target pairs, one target upstream of
// the other, and disjoint lineages alongside the merging-routes shapes.
func TestCommonUpstreamsExportCorrespondenceAllPairs(t *testing.T) {
	graph := buildRegisteredGraph(t, commonExportFixtureRegistrations())
	assertConsistent(t, graph)
	oracle := newLineageTestOracle(t, graph)
	names := sortedGraphNames(graph)
	before := snapshot(graph)

	for _, first := range names {
		firstDoc := parseExport(t, mustExport(t, graph, first))
		for _, second := range names {
			secondDoc := parseExport(t, mustExport(t, graph, second))
			found, err := CommonUpstreams(graph, first, second)
			if err != nil {
				t.Fatalf("CommonUpstreams(%q, %q): %v", first, second, err)
			}
			assertCommonSidesAgainstExports(t, graph, oracle, first, second, firstDoc, secondDoc, found)
		}
	}

	if !reflect.DeepEqual(snapshot(graph), before) {
		t.Fatalf("queries and exports changed the graph: before=%v after=%v", before, snapshot(graph))
	}
	assertConsistent(t, graph)
}

// When both targets are the same dataset, that dataset is the only nearest
// common source — both distances zero, both paths just its own name — even
// though its complete export still carries every ancestor and every branch.
func TestCommonUpstreamsExportCorrespondenceSameTarget(t *testing.T) {
	graph := buildRegisteredGraph(t, commonExportFixtureRegistrations())
	before := snapshot(graph)

	for _, target := range []string{"left", "shared", "raw"} {
		found := mustCommon(t, graph, target, target)
		if got, want := commonNames(found), []string{target}; !reflect.DeepEqual(got, want) {
			t.Fatalf("CommonUpstreams(%q, %q) = %v, want only %v", target, target, got, want)
		}
		assertCommon(t, found, target, 0, []string{target}, 0, []string{target})
	}

	// The export of the same dataset keeps the ancestors the query hid: left's
	// document still holds all eight nodes and ten direct dependencies, and
	// shared's document still carries raw and the raw -> shared edge.
	leftDoc := parseExport(t, mustExport(t, graph, "left"))
	if got, want := leftDoc.Nodes,
		[]string{"a", "b", "c", "left", "peer", "raw", "shared", "z"}; !reflect.DeepEqual(got, want) {
		t.Errorf("left export nodes = %v, want %v", got, want)
	}
	sharedDoc := parseExport(t, mustExport(t, graph, "shared"))
	if got, want := sharedDoc.Nodes, []string{"raw", "shared"}; !reflect.DeepEqual(got, want) {
		t.Errorf("shared export nodes = %v, want %v", got, want)
	}
	if got := exportEdgeNames(sharedDoc); !reflect.DeepEqual(got, [][2]string{{"raw", "shared"}}) {
		t.Errorf("shared export edges = %v, want [[raw shared]]", got)
	}

	if !reflect.DeepEqual(snapshot(graph), before) {
		t.Fatalf("queries and exports changed the graph: before=%v after=%v", before, snapshot(graph))
	}
}

// Two lineages that share no source at all: the comparison succeeds with a
// non-nil empty list, and both complete exports stay fully populated — the
// empty frontier is a property of the intersection, not of missing lineage.
func TestCommonUpstreamsExportCorrespondenceDisjointLineages(t *testing.T) {
	graph := buildRegisteredGraph(t, commonExportFixtureRegistrations())
	before := snapshot(graph)

	found, err := CommonUpstreams(graph, "left", "alienleaf")
	if err != nil {
		t.Fatalf("CommonUpstreams(left, alienleaf): %v", err)
	}
	if found == nil || len(found) != 0 {
		t.Fatalf("disjoint lineages: want empty non-nil list, got %v", found)
	}

	leftDoc := parseExport(t, mustExport(t, graph, "left"))
	if got, want := len(leftDoc.Nodes), 8; got != want {
		t.Errorf("left export holds %d nodes, want %d", got, want)
	}
	alienDoc := parseExport(t, mustExport(t, graph, "alienleaf"))
	if got, want := alienDoc.Nodes, []string{"alien", "alienleaf"}; !reflect.DeepEqual(got, want) {
		t.Errorf("alienleaf export nodes = %v, want %v", got, want)
	}
	if got := exportEdgeNames(alienDoc); !reflect.DeepEqual(got, [][2]string{{"alien", "alienleaf"}}) {
		t.Errorf("alienleaf export edges = %v, want [[alien alienleaf]]", got)
	}

	// The disjoint lineage itself still resolves normally when it does share.
	found = mustCommon(t, graph, "alien", "alienleaf")
	if got, want := commonNames(found), []string{"alien"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("CommonUpstreams(alien, alienleaf) = %v, want %v", got, want)
	}
	assertCommon(t, found, "alien", 0, []string{"alien"}, 1, []string{"alien", "alienleaf"})

	if !reflect.DeepEqual(snapshot(graph), before) {
		t.Fatalf("queries and exports changed the graph: before=%v after=%v", before, snapshot(graph))
	}
}

// The same final lineage built through a different registration order and with
// reordered direct-upstream lists must give identical query results and
// byte-identical exports for every ordered pair of targets.
func TestCommonUpstreamsExportCorrespondenceOrderIndependent(t *testing.T) {
	first := buildRegisteredGraph(t, commonExportFixtureRegistrations())
	second := buildRegisteredGraph(t, [][]string{
		{"alien"},
		{"alienleaf", "alien"},
		{"raw"},
		{"peer", "raw"}, // peer branch registered before shared
		{"mid", "peer"},
		{"shared", "raw"},
		{"a", "shared"}, // a-side branch registered before b
		{"z", "a"},
		{"b", "shared"},
		{"c", "b"},
		{"left", "z", "c", "peer", "raw"}, // direct-upstream order flipped
		{"right", "mid", "shared", "raw"}, // direct-upstream order flipped
		{"view", "right", "left"},         // direct-upstream order flipped
	})

	if got, want := parentEdgeSet(second), parentEdgeSet(first); !reflect.DeepEqual(got, want) {
		t.Fatalf("the two builds hold different relationships:\n%v\n%v", got, want)
	}

	names := sortedGraphNames(first)
	for _, a := range names {
		if got, want := mustExport(t, first, a), mustExport(t, second, a); got != want {
			t.Errorf("export of %q depends on registration order:\nfirst:  %s\nsecond: %s", a, got, want)
		}
		for _, b := range names {
			got := mustCommon(t, first, a, b)
			want := mustCommon(t, second, a, b)
			if !reflect.DeepEqual(got, want) {
				t.Errorf("CommonUpstreams(%q, %q) depends on registration order:\nfirst:  %v\nsecond: %v",
					a, b, got, want)
			}
		}
	}
}
