package chainledger

import (
	"reflect"
	"sort"
	"testing"
)

// This file guards the correspondence between the scoped lineage export and
// the two lineage queries over one registered acyclic graph with consecutive
// forks and merges. ExportSourceTargetLineage must keep every existing
// derivation route between the queried source and target — complete, once per
// node and edge, and nothing beyond those routes — while Impacts and
// Upstreams explain the same dependency with a single shortest path. Whichever
// registered pair is queried (including former intermediate datasets promoted
// to endpoints), the three views must agree: the queries' shared explanation
// path lives inside the exported document, and the document is exactly the
// union of all routes the registered edges actually carry.

// forkMergeFixture builds a registered acyclic lineage with consecutive forks
// and merges, a direct source -> report edge next to three longer branches of
// different lengths, an intermediate dataset that also depends on an
// independent source, and decoys on every side:
//
//	pre ──> source ──┬──> a ──┐
//	                 │        ├──> m ──┐
//	                 ├──> b ──┘   ^     ├──> n ──┐
//	                 │   │        │     │         ├──> report ──> view
//	                 │   └────────┼─────┴─────────┘
//	                 │           extra (independent source of m)
//	                 ├──> report (direct route)
//	                 └──> sidetrack (source downstream that never reaches report)
//	lone ──> report (independent source of the target)
//
// source reaches report four ways: directly, through b -> n, through
// a -> m -> n and through b -> m -> n.
func forkMergeFixture(t *testing.T) map[string]*Lineage {
	return buildRegisteredGraph(t, [][]string{
		{"pre"},
		{"source", "pre"},
		{"extra"},
		{"a", "source"},
		{"b", "source"},
		{"m", "a", "b", "extra"},
		{"n", "m", "b"},
		{"sidetrack", "source"},
		{"lone"},
		{"report", "n", "source", "lone"},
		{"view", "report"},
	})
}

var forkMergeWantNodes = []string{"a", "b", "m", "n", "report", "source"}

var forkMergeWantEdges = [][2]string{
	{"a", "m"},
	{"b", "m"},
	{"b", "n"},
	{"m", "n"},
	{"n", "report"},
	{"source", "a"},
	{"source", "b"},
	{"source", "report"},
}

// routeUnion independently enumerates every simple derivation route from
// source to target over the registered child edges (via the test oracle, not
// the production traversal) and returns the union of their nodes and hop
// edges: exactly what a scoped export must contain, each exactly once. A
// source == target walk is the single-node route, matching the degenerate
// export; an unreachable target yields two empty sets.
func routeUnion(oracle *lineageTestOracle, source, target string) (map[string]bool, map[[2]string]bool) {
	nodes := map[string]bool{}
	edges := map[[2]string]bool{}
	seen := map[string]bool{source: true}
	var walk func(path []string)
	walk = func(path []string) {
		cur := path[len(path)-1]
		if cur == target {
			for _, name := range path {
				nodes[name] = true
			}
			for i := 0; i+1 < len(path); i++ {
				edges[[2]string{path[i], path[i+1]}] = true
			}
			return
		}
		for _, next := range oracle.children[cur] {
			if seen[next] {
				continue
			}
			seen[next] = true
			walk(append(path, next))
			delete(seen, next)
		}
	}
	walk([]string{source})
	return nodes, edges
}

// sortedRouteNodes flattens a route-union node set into the export's
// nodes-by-name Go string order.
func sortedRouteNodes(nodes map[string]bool) []string {
	out := make([]string, 0, len(nodes))
	for name := range nodes {
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}

// sortedRouteEdges flattens a route-union edge set into the export's
// from-then-to Go string order.
func sortedRouteEdges(edges map[[2]string]bool) [][2]string {
	out := make([][2]string, 0, len(edges))
	for edge := range edges {
		out = append(out, edge)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i][0] != out[j][0] {
			return out[i][0] < out[j][0]
		}
		return out[i][1] < out[j][1]
	})
	return out
}

// assertScopedExportMatchesRoutes exports source -> target and requires the
// document to equal the independent route union: every exported node lies on
// at least one existing route, every direct dependency carried by those
// routes is present, shared nodes and edges appear exactly once, and nothing
// else is swept in. The comparison is order-sensitive, so it also pins the
// nodes-by-name and edges-by-from-then-to arrangement.
func assertScopedExportMatchesRoutes(t *testing.T, graph map[string]*Lineage, oracle *lineageTestOracle, source, target string) exportDocument {
	t.Helper()
	doc := parseExport(t, mustExportScoped(t, graph, source, target))

	wantNodes, wantEdges := routeUnion(oracle, source, target)
	if want := sortedRouteNodes(wantNodes); !reflect.DeepEqual(doc.Nodes, want) {
		t.Errorf("ExportSourceTargetLineage(%q, %q) nodes = %v, want route union %v",
			source, target, doc.Nodes, want)
	}
	if want := sortedRouteEdges(wantEdges); !reflect.DeepEqual(exportEdgeNames(doc), want) {
		t.Errorf("ExportSourceTargetLineage(%q, %q) edges = %v, want route union %v",
			source, target, exportEdgeNames(doc), want)
	}
	return doc
}

// assertPathInExport requires every node and every hop-by-hop dependency of
// one explanation path to be present in the exported document.
func assertPathInExport(t *testing.T, source, target string, doc exportDocument, path []string) {
	t.Helper()
	nodeSet := make(map[string]bool, len(doc.Nodes))
	for _, name := range doc.Nodes {
		nodeSet[name] = true
	}
	edgeSet := make(map[[2]string]bool, len(doc.Edges))
	for _, e := range doc.Edges {
		edgeSet[[2]string{e.From, e.To}] = true
	}
	for _, name := range path {
		if !nodeSet[name] {
			t.Errorf("ExportSourceTargetLineage(%q, %q) is missing explanation-path node %q; nodes = %v",
				source, target, name, doc.Nodes)
		}
	}
	for i := 0; i+1 < len(path); i++ {
		if hop := [2]string{path[i], path[i+1]}; !edgeSet[hop] {
			t.Errorf("ExportSourceTargetLineage(%q, %q) is missing explanation-path edge %v; edges = %v",
				source, target, hop, exportEdgeNames(doc))
		}
	}
}

// assertPairQueriesCorrespond requires the downstream and upstream queries to
// witness the source -> target dependency identically — both list it, with
// the same shortest distance and the same source-to-derived explanation
// path — and requires that path to survive whole inside the scoped export.
func assertPairQueriesCorrespond(t *testing.T, graph map[string]*Lineage, source, target string, doc exportDocument, wantDistance int, wantPath []string) {
	t.Helper()
	impacts := mustImpacts(t, graph, source)
	assertImpactOnce(t, impacts, target, wantDistance, wantPath)
	upstreams := mustUpstreams(t, graph, target)
	assertUpstreamOnce(t, upstreams, source, wantDistance, wantPath)

	downRow, downOK := downRecord(impacts, target)
	upRow, upOK := upRecord(upstreams, source)
	if !downOK || !upOK {
		t.Fatalf("%s -> %s missing from one side: Impacts=%v Upstreams=%v",
			source, target, downOK, upOK)
	}
	if downRow.Distance != upRow.Distance || !reflect.DeepEqual(downRow.Path, upRow.Path) {
		t.Fatalf("%s -> %s explained differently per direction: downstream %v vs upstream %v",
			source, target, downRow, upRow)
	}
	assertPathInExport(t, source, target, doc, downRow.Path)
}

// The full fork/merge fixture scoped to (source, report): the direct edge and
// all three longer branches stay complete in one document, shared nodes and
// edges appear once, and every decoy — source's ancestor pre, m's independent
// source extra (and extra -> m), the dead-end sidetrack, report's independent
// upstream lone (and lone -> report) and report's downstream view — stays
// out. The exact JSON text pins the public shape, ordering and verbatim
// names.
func TestExportSourceTargetLineageForkMergeRoutes(t *testing.T) {
	graph := forkMergeFixture(t)
	assertConsistent(t, graph)
	before := snapshotExportGraph(graph)

	out := mustExportScoped(t, graph, "source", "report")
	doc := parseExport(t, out)

	if !reflect.DeepEqual(doc.Nodes, forkMergeWantNodes) {
		t.Errorf("nodes = %v, want %v", doc.Nodes, forkMergeWantNodes)
	}
	if got := exportEdgeNames(doc); !reflect.DeepEqual(got, forkMergeWantEdges) {
		t.Errorf("edges = %v, want %v", got, forkMergeWantEdges)
	}
	for _, excluded := range []string{"pre", "extra", "sidetrack", "lone", "view"} {
		for _, node := range doc.Nodes {
			if node == excluded {
				t.Errorf("nodes must not contain %q: %v", excluded, doc.Nodes)
			}
		}
	}
	for _, excluded := range [][2]string{
		{"pre", "source"}, {"extra", "m"}, {"source", "sidetrack"},
		{"lone", "report"}, {"report", "view"},
	} {
		for _, e := range doc.Edges {
			if e.From == excluded[0] && e.To == excluded[1] {
				t.Errorf("edges must not contain %v: %v", excluded, exportEdgeNames(doc))
			}
		}
	}

	want := `{"nodes":["a","b","m","n","report","source"],` +
		`"edges":[{"from":"a","to":"m"},{"from":"b","to":"m"},{"from":"b","to":"n"},` +
		`{"from":"m","to":"n"},{"from":"n","to":"report"},{"from":"source","to":"a"},` +
		`{"from":"source","to":"b"},{"from":"source","to":"report"}]}`
	if out != want {
		t.Errorf("export =\n%s\nwant:\n%s", out, want)
	}
	assertGraphUnchanged(t, before, graph)
}

// Re-scoping the same graph to different registered endpoints — in particular
// promoting former intermediate datasets to source or target — must shrink or
// shift the document to exactly the routes between the new pair: the direct
// edge and shorter routes never make longer branches disappear, and nothing
// outside the new routes (another source's input, a dead-end side branch, the
// target's downstream) is carried along. Each pair's Impacts/Upstreams rows
// agree with each other and their shared explanation path is whole inside the
// export.
func TestExportSourceTargetLineageRescopedEndpoints(t *testing.T) {
	graph := forkMergeFixture(t)
	assertConsistent(t, graph)
	oracle := newLineageTestOracle(t, graph)
	before := snapshotExportGraph(graph)

	cases := []struct {
		name         string
		source       string
		target       string
		wantNodes    []string
		wantEdges    [][2]string
		wantDistance int
		wantPath     []string
	}{
		{
			// The direct edge wins the explanation, yet all four routes
			// stay in the document.
			name:         "direct edge plus three longer branches",
			source:       "source",
			target:       "report",
			wantNodes:    forkMergeWantNodes,
			wantEdges:    forkMergeWantEdges,
			wantDistance: 1,
			wantPath:     []string{"source", "report"},
		},
		{
			// Target promoted to the former intermediate n: report, view
			// and lone drop out, the three source -> n routes remain.
			name:      "intermediate dataset as target",
			source:    "source",
			target:    "n",
			wantNodes: []string{"a", "b", "m", "n", "source"},
			wantEdges: [][2]string{
				{"a", "m"},
				{"b", "m"},
				{"b", "n"},
				{"m", "n"},
				{"source", "a"},
				{"source", "b"},
			},
			wantDistance: 2,
			wantPath:     []string{"source", "b", "n"},
		},
		{
			// Source promoted to the former intermediate b: source, pre,
			// a and sidetrack drop out, and m's other independent source
			// extra is still not downstream of b, so extra and
			// extra -> m stay excluded.
			name:      "intermediate dataset as source",
			source:    "b",
			target:    "report",
			wantNodes: []string{"b", "m", "n", "report"},
			wantEdges: [][2]string{
				{"b", "m"},
				{"b", "n"},
				{"m", "n"},
				{"n", "report"},
			},
			wantDistance: 2,
			wantPath:     []string{"b", "n", "report"},
		},
		{
			name:      "merge node as source",
			source:    "m",
			target:    "report",
			wantNodes: []string{"m", "n", "report"},
			wantEdges: [][2]string{
				{"m", "n"},
				{"n", "report"},
			},
			wantDistance: 2,
			wantPath:     []string{"m", "n", "report"},
		},
		{
			name:      "single branch between intermediates",
			source:    "a",
			target:    "n",
			wantNodes: []string{"a", "m", "n"},
			wantEdges: [][2]string{
				{"a", "m"},
				{"m", "n"},
			},
			wantDistance: 2,
			wantPath:     []string{"a", "m", "n"},
		},
		{
			// The first merge as target: both source -> a -> m and
			// source -> b -> m survive; extra -> m does not, because
			// extra is not downstream of source.
			name:      "merge node as target",
			source:    "source",
			target:    "m",
			wantNodes: []string{"a", "b", "m", "source"},
			wantEdges: [][2]string{
				{"a", "m"},
				{"b", "m"},
				{"source", "a"},
				{"source", "b"},
			},
			wantDistance: 2,
			wantPath:     []string{"source", "a", "m"},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			doc := assertScopedExportMatchesRoutes(t, graph, oracle, tc.source, tc.target)
			if !reflect.DeepEqual(doc.Nodes, tc.wantNodes) {
				t.Errorf("nodes = %v, want %v", doc.Nodes, tc.wantNodes)
			}
			if got := exportEdgeNames(doc); !reflect.DeepEqual(got, tc.wantEdges) {
				t.Errorf("edges = %v, want %v", got, tc.wantEdges)
			}
			assertPairQueriesCorrespond(t, graph, tc.source, tc.target, doc, tc.wantDistance, tc.wantPath)
		})
	}
	assertGraphUnchanged(t, before, graph)
}

// Whole-graph correspondence: for every ordered pair of distinct registered
// datasets, the scoped export equals the union of existing routes between
// them; a connected pair is witnessed identically by Impacts and Upstreams
// (same distance, same path, both present) with the explanation path whole
// inside the document; an unconnected pair — reverse dependencies included —
// exports two empty arrays and is listed by neither query. Same-name pairs
// export the dataset alone with empty edges and never list themselves in
// either query. All queries and exports together leave the graph untouched.
func TestExportSourceTargetLineageCorrespondsToQueriesForAllPairs(t *testing.T) {
	graph := forkMergeFixture(t)
	assertConsistent(t, graph)
	names := sortedGraphNames(graph)
	oracle := newLineageTestOracle(t, graph)
	before := snapshotExportGraph(graph)

	impactsByName := make(map[string][]Impact, len(names))
	upstreamsByName := make(map[string][]Upstream, len(names))
	for _, name := range names {
		impactsByName[name] = mustImpacts(t, graph, name)
		upstreamsByName[name] = mustUpstreams(t, graph, name)
	}

	for _, source := range names {
		// A dataset never lists itself in either query.
		if _, ok := downRecord(impactsByName[source], source); ok {
			t.Errorf("Impacts(%q) must not list the origin itself", source)
		}
		if _, ok := upRecord(upstreamsByName[source], source); ok {
			t.Errorf("Upstreams(%q) must not list the target itself", source)
		}

		for _, target := range names {
			doc := assertScopedExportMatchesRoutes(t, graph, oracle, source, target)

			if source == target {
				// Same registered dataset: the node alone, an empty
				// (non-null) edges array, and no self-listing above.
				if !reflect.DeepEqual(doc.Nodes, []string{source}) {
					t.Errorf("(%q, %q) nodes = %v, want just the dataset", source, target, doc.Nodes)
				}
				if doc.Edges == nil || len(doc.Edges) != 0 {
					t.Errorf("(%q, %q) edges must be a non-nil empty array, got %#v",
						source, target, doc.Edges)
				}
				continue
			}

			downRow, downOK := downRecord(impactsByName[source], target)
			upRow, upOK := upRecord(upstreamsByName[target], source)
			if downOK != upOK {
				t.Fatalf("directions disagree on %s -> %s: Impacts lists it = %v, Upstreams lists it = %v",
					source, target, downOK, upOK)
			}

			if !downOK {
				// No forward derivation route — reverse dependencies
				// included: the export succeeds with two empty arrays and
				// keeps no isolated endpoint.
				if len(doc.Nodes) != 0 || doc.Nodes == nil {
					t.Errorf("(%q, %q) unreachable: nodes must be a non-nil empty array, got %#v",
						source, target, doc.Nodes)
				}
				if len(doc.Edges) != 0 || doc.Edges == nil {
					t.Errorf("(%q, %q) unreachable: edges must be a non-nil empty array, got %#v",
						source, target, doc.Edges)
				}
				continue
			}

			// Connected pair: both directions report the same distance and
			// the same explanation path, and that path's nodes and
			// hop-by-hop dependencies are complete inside the export —
			// alongside every other valid route the export preserves.
			if downRow.Distance != upRow.Distance {
				t.Errorf("%s -> %s distance: Impacts %d, Upstreams %d",
					source, target, downRow.Distance, upRow.Distance)
			}
			if !reflect.DeepEqual(downRow.Path, upRow.Path) {
				t.Errorf("%s -> %s path: Impacts %v, Upstreams %v",
					source, target, downRow.Path, upRow.Path)
			}
			assertPathInExport(t, source, target, doc, downRow.Path)
		}
	}

	assertGraphUnchanged(t, before, graph)
	assertConsistent(t, graph)
}

// Explicit anchors for the degenerate pairs of the fork/merge fixture: the
// exact JSON text for a same-dataset export and for registered pairs with no
// forward route, including a reverse dependency and a dead-end side branch
// used as source.
func TestExportSourceTargetLineageDegeneratePairs(t *testing.T) {
	graph := forkMergeFixture(t)
	before := snapshotExportGraph(graph)

	out := mustExportScoped(t, graph, "m", "m")
	if want := `{"nodes":["m"],"edges":[]}`; out != want {
		t.Errorf("same-dataset export = %s, want %s", out, want)
	}

	for _, tc := range []struct {
		name   string
		source string
		target string
	}{
		{"reverse dependency", "report", "source"},
		{"reverse through merge", "n", "b"},
		{"dead-end branch as source", "sidetrack", "report"},
		{"independent roots", "extra", "lone"},
		{"target downstream as source", "view", "report"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			out := mustExportScoped(t, graph, tc.source, tc.target)
			if want := `{"nodes":[],"edges":[]}`; out != want {
				t.Errorf("export = %s, want %s", out, want)
			}
		})
	}
	assertGraphUnchanged(t, before, graph)
}
