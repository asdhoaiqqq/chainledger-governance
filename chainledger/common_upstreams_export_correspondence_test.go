package chainledger

import (
	"reflect"
	"slices"
	"strings"
	"testing"
)

// This file is the regression guard for the relationship between the closest
// common-upstream query and the two COMPLETE upstream lineage exports:
//
//   - ExportUpstreamLineage(target) keeps every direct dependency the target's
//     derivation uses, including every forked branch and every longer route, so
//     two exports name a source exactly when it really participates in the
//     derivation of BOTH targets.
//   - CommonUpstreams(first, second) reports only the closest of those shared
//     sources (a shared source with a later shared source downstream of it is
//     hidden), explaining each side with one independently traced shortest,
//     tie-broken path.
//
// The two surfaces show different ranges, but they must never contradict each
// other on whether a source takes part in both derivations: every reported
// common source must be a node reachable along exported, direction-consistent
// edges in BOTH documents, its two distances/paths must be the shortest routes
// on THEIR OWN sides, and every source present in both exports but absent from
// the query must be demonstrably hidden behind a later common source.
//
// Every expectation here is derived independently — simple walks over the
// stored parent edges and node/edge sets parsed out of the export documents —
// never from CommonUpstreams' own traversal.

// commonExportFixtureRegistrations builds the merging-routes scenario:
//
//	raw ──┬─> shared ─┬─> a ─> z ─┬─> left   shared ──────────> right
//	  │   │           └─> b ─> c ─┘ (z and c both feed left)
//	  ├─> peer ───────────────────────> left   peer ─> mid ───> right
//	  ├─> left (direct)
//	  └─> right (direct)
//
// Comparing left and right therefore merges several routes at once:
//   - shared reaches left over two equal-length routes (shared->a->z->left and
//     shared->b->c->left) but reaches right directly;
//   - peer reaches left directly and right through mid;
//   - raw reaches both targets directly AND through shared/peer, so even its
//     shorter direct relations cannot un-hide it once shared and peer qualify
//     as later common sources.
func commonExportFixtureRegistrations() [][]string {
	return [][]string{
		{"raw"},
		{"shared", "raw"},
		{"peer", "raw"},
		{"a", "shared"},
		{"b", "shared"},
		{"z", "a"},
		{"c", "b"},
		{"mid", "peer"},
		{"left", "raw", "peer", "z", "c"},
		{"right", "raw", "shared", "mid"},
	}
}

// enumerateAncestorNames independently collects target and every dataset
// reachable from it by following stored parent edges, via a simple-walk DFS.
// Register rejects cycles, so the walk terminates.
func enumerateAncestorNames(graph map[string]*Lineage, target string) map[string]bool {
	included := map[string]bool{}
	seen := map[string]bool{target: true}
	var walk func(node string)
	walk = func(node string) {
		included[node] = true
		for _, parent := range graph[node].Parents {
			if seen[parent] {
				continue
			}
			seen[parent] = true
			walk(parent)
			delete(seen, parent)
		}
	}
	walk(target)
	return included
}

// expectedFullUpstreamDocument derives what a full upstream export must contain
// straight from the registered edges: target plus every ancestor, and every
// direct dependency between two included nodes, in the public ordering.
func expectedFullUpstreamDocument(t *testing.T, graph map[string]*Lineage, target string) ([]string, [][2]string) {
	t.Helper()
	included := enumerateAncestorNames(graph, target)
	nodes := make([]string, 0, len(included))
	for name := range included {
		nodes = append(nodes, name)
	}
	slices.Sort(nodes)
	edgeSet := map[[2]string]bool{}
	for _, name := range nodes {
		for _, parent := range graph[name].Parents {
			if included[parent] {
				edgeSet[[2]string{parent, name}] = true
			}
		}
	}
	edges := make([][2]string, 0, len(edgeSet))
	for edge := range edgeSet {
		edges = append(edges, edge)
	}
	slices.SortFunc(edges, func(x, y [2]string) int {
		if x[0] != y[0] {
			return strings.Compare(x[0], y[0])
		}
		return strings.Compare(x[1], y[1])
	})
	return nodes, edges
}

func docEdgeSet(doc exportDocument) map[[2]string]bool {
	edges := map[[2]string]bool{}
	for _, e := range exportEdgeNames(doc) {
		edges[e] = true
	}
	return edges
}

// assertFullExportMatchesGraph pins one full export against the independently
// enumerated ancestor closure, in addition to the generic document shape.
func assertFullExportMatchesGraph(t *testing.T, graph map[string]*Lineage, doc exportDocument, target string) {
	t.Helper()
	assertScopedExportShape(t, graph, doc)
	wantNodes, wantEdges := expectedFullUpstreamDocument(t, graph, target)
	if !reflect.DeepEqual(doc.Nodes, wantNodes) {
		t.Errorf("ExportUpstreamLineage(%q) nodes = %v, want %v", target, doc.Nodes, wantNodes)
	}
	if got := exportEdgeNames(doc); !reflect.DeepEqual(got, wantEdges) {
		t.Errorf("ExportUpstreamLineage(%q) edges = %v, want %v", target, got, wantEdges)
	}
}

// assertCommonUpstreamExportCorrespondence is the pair-agnostic guard for one
// ordered target pair: it reconstructs the required CommonUpstreams answer
// entirely from the two full export documents and the simple-walk oracle, then
// checks the real query agrees and every explanation lives in the exports.
func assertCommonUpstreamExportCorrespondence(t *testing.T, graph map[string]*Lineage, first, second string) {
	t.Helper()
	found, err := CommonUpstreams(graph, first, second)
	if err != nil {
		t.Fatalf("CommonUpstreams(%q, %q): %v", first, second, err)
	}
	if found == nil {
		t.Fatalf("CommonUpstreams(%q, %q) must return a non-nil slice", first, second)
	}
	firstDoc := parseExport(t, mustExport(t, graph, first))
	secondDoc := parseExport(t, mustExport(t, graph, second))
	assertFullExportMatchesGraph(t, graph, firstDoc, first)
	assertFullExportMatchesGraph(t, graph, secondDoc, second)
	firstEdges := docEdgeSet(firstDoc)
	secondEdges := docEdgeSet(secondDoc)

	// A source participates in BOTH derivations exactly when it is a node of
	// both exports; derive the intersection straight from the documents.
	inFirst := map[string]bool{}
	for _, name := range firstDoc.Nodes {
		inFirst[name] = true
	}
	commonSet := map[string]bool{}
	var sharedNodes []string
	for _, name := range secondDoc.Nodes {
		if inFirst[name] {
			commonSet[name] = true
			sharedNodes = append(sharedNodes, name)
		}
	}
	slices.Sort(sharedNodes)

	// Relations between two common nodes are part of both derivations and must
	// be documented identically in both exports.
	for _, edge := range exportEdgeNames(firstDoc) {
		if commonSet[edge[0]] && commonSet[edge[1]] && !secondEdges[edge] {
			t.Errorf("relation %s -> %s joins two shared sources but is missing from the second export",
				edge[0], edge[1])
		}
	}
	for _, edge := range exportEdgeNames(secondDoc) {
		if commonSet[edge[0]] && commonSet[edge[1]] && !firstEdges[edge] {
			t.Errorf("relation %s -> %s joins two shared sources but is missing from the first export",
				edge[0], edge[1])
		}
	}

	// The query's frontier, independently characterized: a shared source stays
	// unless one of its DIRECT exported children is itself shared.
	hasSharedChild := func(name string, edges map[[2]string]bool) bool {
		for edge := range edges {
			if edge[0] == name && commonSet[edge[1]] {
				return true
			}
		}
		return false
	}
	var wantNames []string
	for _, name := range sharedNodes {
		// Both documents must witness the hiding relation in the same way.
		if hasSharedChild(name, firstEdges) != hasSharedChild(name, secondEdges) {
			t.Errorf("shared source %q hidden status disagrees between the two exports", name)
		}
		if !hasSharedChild(name, firstEdges) {
			wantNames = append(wantNames, name)
		}
	}
	if got := commonNames(found); !slices.Equal(got, wantNames) {
		t.Fatalf("CommonUpstreams(%q, %q) = %v, want export-derived frontier %v",
			first, second, got, wantNames)
	}
	assertEachCommonOnce(t, found)
	if !slices.IsSorted(commonNames(found)) {
		t.Errorf("common sources %v are not ordered by name", commonNames(found))
	}

	oracle := newLineageTestOracle(t, graph)
	for _, c := range found {
		if !commonSet[c.Dataset] {
			t.Errorf("reported common source %q is not a node of both exports", c.Dataset)
		}

		// Each side carries its OWN shortest distance and source-to-target path;
		// one side must never be substituted for the other.
		wantDistFirst, wantPathFirst, reachFirst := oracle.shortest(c.Dataset, first)
		wantDistSecond, wantPathSecond, reachSecond := oracle.shortest(c.Dataset, second)
		if !reachFirst || !reachSecond {
			t.Fatalf("reported common source %q does not derive both targets per the route oracle",
				c.Dataset)
		}
		if c.DistanceToFirst != wantDistFirst || !reflect.DeepEqual(c.PathToFirst, wantPathFirst) {
			t.Errorf("common %q first side = (distance %d, path %v), want (distance %d, path %v)",
				c.Dataset, c.DistanceToFirst, c.PathToFirst, wantDistFirst, wantPathFirst)
		}
		if c.DistanceToSecond != wantDistSecond || !reflect.DeepEqual(c.PathToSecond, wantPathSecond) {
			t.Errorf("common %q second side = (distance %d, path %v), want (distance %d, path %v)",
				c.Dataset, c.DistanceToSecond, c.PathToSecond, wantDistSecond, wantPathSecond)
		}

		// Shape: source first, target last, edge count matching the distance.
		if c.PathToFirst[0] != c.Dataset || c.PathToFirst[len(c.PathToFirst)-1] != first ||
			len(c.PathToFirst) != c.DistanceToFirst+1 {
			t.Errorf("common %q first path %v must run %s -> %s with %d edges",
				c.Dataset, c.PathToFirst, c.Dataset, first, c.DistanceToFirst)
		}
		if c.PathToSecond[0] != c.Dataset || c.PathToSecond[len(c.PathToSecond)-1] != second ||
			len(c.PathToSecond) != c.DistanceToSecond+1 {
			t.Errorf("common %q second path %v must run %s -> %s with %d edges",
				c.Dataset, c.PathToSecond, c.Dataset, second, c.DistanceToSecond)
		}

		// Every explanation hop must be a direction-consistent direct dependency
		// inside THAT target's own complete export.
		assertPathEdgesRegistered(t, graph, c.PathToFirst)
		assertPathEdgesRegistered(t, graph, c.PathToSecond)
		assertPathInsideDocument(t, firstDoc, c.PathToFirst)
		assertPathInsideDocument(t, secondDoc, c.PathToSecond)
	}

	// No contradictions the other way: a source both exports share but the
	// query omits must be hidden behind a strictly later source that is shared
	// too, not missing by accident or judged one-sided.
	for _, name := range sharedNodes {
		if slices.Contains(commonNames(found), name) {
			continue
		}
		if !hasSharedChild(name, firstEdges) {
			t.Errorf("shared source %q is omitted by the query but no later shared source hides it",
				name)
		}
	}
}

// The headline merge scenario: the two closest common sources of left and
// right are peer and shared, ordered by name; raw is hidden behind them even
// though raw reaches both targets directly one edge away; a, z, b, c and mid
// participate on only one side and never enter the result. Each side keeps its
// own distance and path, shared's equal-length tie is broken from the source
// (a before b), and both full exports preserve raw's direct edges and every
// longer branch the query does not explain with.
func TestCommonUpstreamsCorrespondsWithFullUpstreamExportsMergingRoutes(t *testing.T) {
	graph := buildRegisteredGraph(t, commonExportFixtureRegistrations())
	assertConsistent(t, graph)
	before := snapshot(graph)

	found := mustCommon(t, graph, "left", "right")
	if got, want := commonNames(found), []string{"peer", "shared"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("common sources = %v, want %v (raw hidden; one-side intermediates excluded)",
			got, want)
	}
	assertEachCommonOnce(t, found)
	// peer reaches left directly and right only via mid.
	assertCommon(t, found, "peer", 1, []string{"peer", "left"}, 2, []string{"peer", "mid", "right"})
	// shared reaches right directly but left only over length-3 merges; the a/z
	// route wins because a < b is decided at hop 1 from the source, not by
	// comparing z against c in front of the target.
	assertCommon(t, found, "shared", 3, []string{"shared", "a", "z", "left"},
		1, []string{"shared", "right"})

	leftDoc := parseExport(t, mustExport(t, graph, "left"))
	rightDoc := parseExport(t, mustExport(t, graph, "right"))
	assertFullExportMatchesGraph(t, graph, leftDoc, "left")
	assertFullExportMatchesGraph(t, graph, rightDoc, "right")

	// Exact pinned documents: the exports keep ALL branches, including the
	// routes the query only uses as unchosen alternatives.
	wantLeftNodes := []string{"a", "b", "c", "left", "peer", "raw", "shared", "z"}
	wantLeftEdges := [][2]string{
		{"a", "z"},
		{"b", "c"},
		{"c", "left"},
		{"peer", "left"},
		{"raw", "left"},
		{"raw", "peer"},
		{"raw", "shared"},
		{"shared", "a"},
		{"shared", "b"},
		{"z", "left"},
	}
	if !reflect.DeepEqual(leftDoc.Nodes, wantLeftNodes) {
		t.Errorf("left export nodes = %v, want %v", leftDoc.Nodes, wantLeftNodes)
	}
	if got := exportEdgeNames(leftDoc); !reflect.DeepEqual(got, wantLeftEdges) {
		t.Errorf("left export edges = %v, want %v", got, wantLeftEdges)
	}
	wantRightNodes := []string{"mid", "peer", "raw", "right", "shared"}
	wantRightEdges := [][2]string{
		{"mid", "right"},
		{"peer", "mid"},
		{"raw", "peer"},
		{"raw", "right"},
		{"raw", "shared"},
		{"shared", "right"},
	}
	if !reflect.DeepEqual(rightDoc.Nodes, wantRightNodes) {
		t.Errorf("right export nodes = %v, want %v", rightDoc.Nodes, wantRightNodes)
	}
	if got := exportEdgeNames(rightDoc); !reflect.DeepEqual(got, wantRightEdges) {
		t.Errorf("right export edges = %v, want %v", got, wantRightEdges)
	}

	// Exact public JSON text for both documents.
	wantLeftJSON := `{"nodes":["a","b","c","left","peer","raw","shared","z"],` +
		`"edges":[` +
		`{"from":"a","to":"z"},{"from":"b","to":"c"},{"from":"c","to":"left"},` +
		`{"from":"peer","to":"left"},` +
		`{"from":"raw","to":"left"},{"from":"raw","to":"peer"},{"from":"raw","to":"shared"},` +
		`{"from":"shared","to":"a"},{"from":"shared","to":"b"},{"from":"z","to":"left"}]}`
	if got := mustExport(t, graph, "left"); got != wantLeftJSON {
		t.Errorf("left export JSON =\n%s\nwant:\n%s", got, wantLeftJSON)
	}
	wantRightJSON := `{"nodes":["mid","peer","raw","right","shared"],` +
		`"edges":[` +
		`{"from":"mid","to":"right"},{"from":"peer","to":"mid"},` +
		`{"from":"raw","to":"peer"},{"from":"raw","to":"right"},{"from":"raw","to":"shared"},` +
		`{"from":"shared","to":"right"}]}`
	if got := mustExport(t, graph, "right"); got != wantRightJSON {
		t.Errorf("right export JSON =\n%s\nwant:\n%s", got, wantRightJSON)
	}

	// raw's shorter direct relations survive in BOTH exports even though the
	// query hides raw; the query showing one explanation path must not prune the
	// other equal-length shared route.
	for _, tc := range []struct {
		doc  exportDocument
		edge [2]string
	}{
		{leftDoc, [2]string{"raw", "left"}},
		{rightDoc, [2]string{"raw", "right"}},
	} {
		if !docEdgeSet(tc.doc)[tc.edge] {
			t.Errorf("export must preserve direct edge %s -> %s", tc.edge[0], tc.edge[1])
		}
	}
	assertPathInsideDocument(t, leftDoc, []string{"shared", "a", "z", "left"})
	assertPathInsideDocument(t, leftDoc, []string{"shared", "b", "c", "left"})
	assertPathInsideDocument(t, rightDoc, []string{"peer", "mid", "right"})

	// Every enumerated route from every shared (and hidden-shared) source stays
	// fully present in the corresponding export — branch preservation, witnessed
	// independently of the production traversals.
	assertRoutesExactly := func(source, target string, got, want [][]string) {
		t.Helper()
		normalize := func(routes [][]string) [][]string {
			out := append([][]string(nil), routes...)
			slices.SortFunc(out, slices.Compare)
			return out
		}
		if !reflect.DeepEqual(normalize(got), normalize(want)) {
			t.Errorf("route witness for %s -> %s found %v, want %v", source, target, got, want)
		}
	}
	leftWants := map[string][][]string{
		"raw": {
			{"raw", "left"},
			{"raw", "peer", "left"},
			{"raw", "shared", "a", "z", "left"},
			{"raw", "shared", "b", "c", "left"},
		},
		"shared": {
			{"shared", "a", "z", "left"},
			{"shared", "b", "c", "left"},
		},
		"peer": {{"peer", "left"}},
	}
	for source, wantRoutes := range leftWants {
		enumerated := enumerateSourceTargetRoutes(t, graph, source, "left")
		assertRoutesExactly(source, "left", enumerated, wantRoutes)
		for _, route := range enumerated {
			assertPathInsideDocument(t, leftDoc, route)
		}
	}
	rightWants := map[string][][]string{
		"raw": {
			{"raw", "right"},
			{"raw", "shared", "right"},
			{"raw", "peer", "mid", "right"},
		},
		"shared": {{"shared", "right"}},
		"peer":   {{"peer", "mid", "right"}},
	}
	for source, wantRoutes := range rightWants {
		enumerated := enumerateSourceTargetRoutes(t, graph, source, "right")
		assertRoutesExactly(source, "right", enumerated, wantRoutes)
		for _, route := range enumerated {
			assertPathInsideDocument(t, rightDoc, route)
		}
	}

	// One-side-only intermediates cannot be common and must be absent from the
	// other target's export.
	for _, oneSided := range []string{"a", "b", "c", "z"} {
		if slices.Contains(rightDoc.Nodes, oneSided) {
			t.Errorf("left-only dataset %q must not appear in right's export %v", oneSided, rightDoc.Nodes)
		}
		if slices.Contains(commonNames(found), oneSided) {
			t.Errorf("left-only dataset %q must not be a common source", oneSided)
		}
	}
	if slices.Contains(leftDoc.Nodes, "mid") || slices.Contains(commonNames(found), "mid") {
		t.Errorf("right-only dataset mid must appear neither in left's export nor in common sources")
	}
	// raw is shared by both exports, but hidden from the query.
	if !slices.Contains(leftDoc.Nodes, "raw") || !slices.Contains(rightDoc.Nodes, "raw") {
		t.Errorf("raw must remain a documented ancestor of both targets")
	}
	if slices.Contains(commonNames(found), "raw") {
		t.Errorf("raw must be hidden behind peer and shared")
	}

	// Swapping the targets swaps the two sides field by field, nothing else.
	swapped := mustCommon(t, graph, "right", "left")
	if got, want := commonNames(swapped), []string{"peer", "shared"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("swapped common sources = %v, want %v", got, want)
	}
	assertCommon(t, swapped, "peer", 2, []string{"peer", "mid", "right"}, 1, []string{"peer", "left"})
	assertCommon(t, swapped, "shared", 1, []string{"shared", "right"},
		3, []string{"shared", "a", "z", "left"})

	if !reflect.DeepEqual(snapshot(graph), before) {
		t.Fatalf("queries and exports changed the graph: before=%v after=%v",
			before, snapshot(graph))
	}
	assertConsistent(t, graph)
}

// Exhaustive correspondence over every ordered target pair in the merge
// fixture: the query answer must be reconstructable from the two full exports
// plus the route oracle for every pair, including same-target and
// ancestor-vs-descendant pairs.
func TestCommonUpstreamsExportCorrespondenceEveryPair(t *testing.T) {
	graph := buildRegisteredGraph(t, commonExportFixtureRegistrations())
	before := snapshot(graph)
	for _, first := range sortedGraphNames(graph) {
		for _, second := range sortedGraphNames(graph) {
			assertCommonUpstreamExportCorrespondence(t, graph, first, second)
		}
	}
	if !reflect.DeepEqual(snapshot(graph), before) {
		t.Fatalf("correspondence checks changed the graph: before=%v after=%v",
			before, snapshot(graph))
	}
	assertConsistent(t, graph)
}

// Comparing a dataset with itself returns exactly that dataset at distance
// zero with a self-only path on both sides, even though its full export (and
// both identical documents) carries every ancestor.
func TestCommonUpstreamsExportCorrespondenceSameTarget(t *testing.T) {
	graph := buildRegisteredGraph(t, commonExportFixtureRegistrations())
	before := snapshot(graph)

	found := mustCommon(t, graph, "left", "left")
	if got, want := commonNames(found), []string{"left"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("same target: common sources = %v, want %v", got, want)
	}
	assertCommon(t, found, "left", 0, []string{"left"}, 0, []string{"left"})

	// The ancestors hidden behind left are still all in the complete export.
	doc := parseExport(t, mustExport(t, graph, "left"))
	assertFullExportMatchesGraph(t, graph, doc, "left")
	for _, ancestor := range []string{"raw", "peer", "shared", "a", "b", "z", "c"} {
		if !slices.Contains(doc.Nodes, ancestor) {
			t.Errorf("ancestor %q must remain exported even though it is not a common result", ancestor)
		}
	}
	assertPathInsideDocument(t, doc, []string{"left"})

	// A non-root ancestor queried against itself behaves the same way.
	found = mustCommon(t, graph, "shared", "shared")
	if got, want := commonNames(found), []string{"shared"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("same non-root target: common sources = %v, want %v", got, want)
	}
	assertCommon(t, found, "shared", 0, []string{"shared"}, 0, []string{"shared"})

	if !reflect.DeepEqual(snapshot(graph), before) {
		t.Fatalf("same-target checks changed the graph: before=%v after=%v",
			before, snapshot(graph))
	}
}

// Two completely disjoint lineages compare successfully with a non-nil empty
// list, and each full export still keeps its own complete derivation; the two
// documents share no node or edge, so the surfaces cannot disagree about
// participation.
func TestCommonUpstreamsExportCorrespondenceDisjointLineages(t *testing.T) {
	graph := buildRegisteredGraph(t, [][]string{
		{"rawA"},
		{"x", "rawA"},
		{"left", "x"},
		{"rawB"},
		{"y", "rawB"},
		{"right", "y"},
		{"lonely"},
	})
	before := snapshot(graph)

	found, err := CommonUpstreams(graph, "left", "right")
	if err != nil {
		t.Fatalf("disjoint comparison should succeed, got %v", err)
	}
	if found == nil || len(found) != 0 {
		t.Fatalf("disjoint comparison: want non-nil empty list, got %v", found)
	}

	leftDoc := parseExport(t, mustExport(t, graph, "left"))
	rightDoc := parseExport(t, mustExport(t, graph, "right"))
	if got, want := leftDoc.Nodes, []string{"left", "rawA", "x"}; !reflect.DeepEqual(got, want) {
		t.Errorf("left export nodes = %v, want %v", got, want)
	}
	if got := exportEdgeNames(leftDoc); !reflect.DeepEqual(got, [][2]string{
		{"rawA", "x"}, {"x", "left"},
	}) {
		t.Errorf("left export edges = %v", got)
	}
	if got, want := rightDoc.Nodes, []string{"rawB", "right", "y"}; !reflect.DeepEqual(got, want) {
		t.Errorf("right export nodes = %v, want %v", got, want)
	}
	if got := exportEdgeNames(rightDoc); !reflect.DeepEqual(got, [][2]string{
		{"rawB", "y"}, {"y", "right"},
	}) {
		t.Errorf("right export edges = %v", got)
	}

	// Every ordered pair in the disjoint graph satisfies the full correspondence.
	for _, first := range sortedGraphNames(graph) {
		for _, second := range sortedGraphNames(graph) {
			assertCommonUpstreamExportCorrespondence(t, graph, first, second)
		}
	}

	if !reflect.DeepEqual(snapshot(graph), before) {
		t.Fatalf("disjoint checks changed the graph: before=%v after=%v",
			before, snapshot(graph))
	}
	assertConsistent(t, graph)
}

// The same final lineage built in another order with flipped direct-upstream
// lists must give identical query results and byte-identical exports, and the
// query and the exports change no node, bidirectional edge or list order.
func TestCommonUpstreamsExportCorrespondenceOrderInvariance(t *testing.T) {
	first := buildRegisteredGraph(t, commonExportFixtureRegistrations())
	second := buildRegisteredGraph(t, [][]string{
		{"raw"},
		{"peer", "raw"},
		{"shared", "raw"},
		{"mid", "peer"},
		{"b", "shared"}, // b-side first this time
		{"c", "b"},
		{"a", "shared"},
		{"z", "a"},
		{"right", "mid", "raw", "shared"}, // direct-upstream order flipped
		{"left", "c", "z", "peer", "raw"}, // direct-upstream order flipped
	})
	if got := parentEdgeSet(second); !reflect.DeepEqual(got, parentEdgeSet(first)) {
		t.Fatalf("the two builds hold different relationships:\n%v\n%v", got, parentEdgeSet(first))
	}

	for _, pair := range [][2]string{{"left", "right"}, {"right", "left"}} {
		if got, want := mustCommon(t, first, pair[0], pair[1]),
			mustCommon(t, second, pair[0], pair[1]); !reflect.DeepEqual(got, want) {
			t.Fatalf("(%q,%q) query depends on registration order:\n%v\n%v",
				pair[0], pair[1], got, want)
		}
	}
	for _, target := range sortedGraphNames(first) {
		if got, want := mustExport(t, first, target), mustExport(t, second, target); got != want {
			t.Errorf("ExportUpstreamLineage(%q) depends on registration order:\nfirst:  %s\nsecond: %s",
				target, got, want)
		}
	}
}

// Both surfaces keep the public validation and read-only contracts: failures
// name the offending dataset and return nil results / an empty document, and a
// mix of successful and failing calls leaves every node, bidirectional edge
// and stored list order exactly as registered.
func TestCommonUpstreamsExportCorrespondenceErrorsAndReadOnly(t *testing.T) {
	graph := buildRegisteredGraph(t, commonExportFixtureRegistrations())
	before := snapshot(graph)

	for _, tc := range []struct {
		name   string
		first  string
		second string
		err    string
	}{
		{"empty first", "", "right", "dataset name is required"},
		{"unknown first", "ghost", "right", "dataset not found: ghost"},
		{"empty second", "left", "", "dataset name is required"},
		{"unknown second", "left", "ghost", "dataset not found: ghost"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			found, err := CommonUpstreams(graph, tc.first, tc.second)
			if err == nil || err.Error() != tc.err {
				t.Fatalf("CommonUpstreams(%q,%q): err = %v, want %q", tc.first, tc.second, err, tc.err)
			}
			if found != nil {
				t.Errorf("failed query must return nil, got %v", found)
			}
		})
	}
	if out, err := ExportUpstreamLineage(graph, ""); err == nil ||
		!strings.Contains(err.Error(), "name is required") {
		t.Errorf("empty-target export: want required-name error, got %v", err)
	} else if out != "" {
		t.Errorf("failed export returned partial content %q", out)
	}
	if out, err := ExportUpstreamLineage(graph, "ghost"); err == nil ||
		!strings.Contains(err.Error(), "ghost") {
		t.Errorf("unknown-target export: want error naming ghost, got %v", err)
	} else if out != "" {
		t.Errorf("failed export returned partial content %q", out)
	}

	// Exercise every target on both surfaces successfully.
	for _, target := range sortedGraphNames(graph) {
		mustCommon(t, graph, target, "left")
		mustExport(t, graph, target)
	}
	mustCommon(t, graph, "left", "right")

	if !reflect.DeepEqual(snapshot(graph), before) {
		t.Fatalf("queries and exports changed the graph: before=%v after=%v",
			before, snapshot(graph))
	}
	assertConsistent(t, graph)
}
