package chainledger

import (
	"reflect"
	"slices"
	"sort"
	"strings"
	"testing"
)

// This file is the regression guard for the relationship between the scoped
// lineage export and the two directional queries, as the queried source and
// target change within one registered graph:
//
//   - ExportSourceTargetLineage keeps EVERY existing route between the two
//     endpoints (all branches), while Impacts and Upstreams each explain a
//     reachable dataset with exactly one shortest, tie-broken path.
//   - For one pair of distinct, genuinely connected endpoints, the downstream
//     row Impacts(source) carries for target and the upstream row
//     Upstreams(target) carries for source must agree on distance and path, and
//     that explanation must be fully present inside the export — while the
//     export additionally keeps the routes the explanation path did not pick.
//
// Every expectation here is derived independently by enumerating simple walks
// over the stored parent edges (a route witness), never from the production
// traversals, so a wrong branch/edge selection in the export itself is caught.

// correspondenceFixtureRegistrations builds a registered acyclic graph with
// successive forks and merges, routes of different lengths sharing segments,
// a direct source->target edge, an intermediate dataset with an independent
// second source, a source downstream that cannot reach the target, and the
// target's own downstream:
//
//	extra ──┐ (a's independent source, not downstream of src)
//	        v
//	src ──┬─> a ──> g ──┬──────────────┐
//	  │   │      │      └─> d ──> m <───┘ (g -> m is also direct)
//	  │   │      └──────────────> report  (direct a -> report too)
//	  ├─> b ──┬─> c ───────────> m ─> report
//	  │       └─> d  (d merges b and g)
//	  ├─> report                  (direct src -> report)
//	  ├─> dead                    (src downstream that never reaches report)
//	  └─(report -> view)          (target's own downstream)
//
// src therefore reaches report six ways at once: directly (1 edge), through
// a (2 edges), through a -> g -> m (4 edges), through b -> c -> m and
// b -> d -> m (4 edges), and through a -> g -> d -> m (5 edges).
func correspondenceFixtureRegistrations() [][]string {
	return [][]string{
		{"src"},
		{"extra"},
		{"a", "src", "extra"},
		{"b", "src"},
		{"g", "a"},
		{"c", "b"},
		{"d", "b", "g"},
		{"m", "c", "d", "g"},
		{"report", "src", "a", "m"},
		{"dead", "src"},
		{"view", "report"},
	}
}

// enumerateSourceTargetRoutes returns every simple walk from source to target
// over registered child edges, each including both ends. Expansion stops at
// target, so target's downstream cannot enter a route. Register rejects
// cycles, so the simple-walk search terminates.
func enumerateSourceTargetRoutes(t *testing.T, graph map[string]*Lineage, source, target string) [][]string {
	t.Helper()
	var routes [][]string
	seen := map[string]bool{source: true}
	var walk func(path []string)
	walk = func(path []string) {
		cur := path[len(path)-1]
		if cur == target {
			routes = append(routes, append([]string(nil), path...))
			return
		}
		for _, next := range graph[cur].Children {
			if seen[next] {
				continue
			}
			seen[next] = true
			walk(append(path, next))
			delete(seen, next)
		}
	}
	walk([]string{source})
	return routes
}

// expectedScopedDocument derives the scoped export independently from the
// route witness: one node per dataset appearing on at least one source ->
// target route, one edge per consecutive dependency carried by any such
// route, presented in the public ordering (nodes by name, edges by from then
// to).
func expectedScopedDocument(t *testing.T, graph map[string]*Lineage, source, target string) ([]string, [][2]string) {
	t.Helper()
	nodeSet := map[string]bool{}
	edgeSet := map[[2]string]bool{}
	for _, route := range enumerateSourceTargetRoutes(t, graph, source, target) {
		for i, name := range route {
			nodeSet[name] = true
			if i+1 < len(route) {
				edgeSet[[2]string{route[i], route[i+1]}] = true
			}
		}
	}
	nodes := make([]string, 0, len(nodeSet))
	for name := range nodeSet {
		nodes = append(nodes, name)
	}
	sort.Strings(nodes)
	edges := make([][2]string, 0, len(edgeSet))
	for edge := range edgeSet {
		edges = append(edges, edge)
	}
	sort.Slice(edges, func(i, j int) bool {
		if edges[i][0] != edges[j][0] {
			return edges[i][0] < edges[j][0]
		}
		return edges[i][1] < edges[j][1]
	})
	return nodes, edges
}

// assertScopedExportShape pins the public document behavior directly:
//
//   - nodes are unique and in Go string order, edges unique and ordered by
//     (from, to);
//   - every endpoint and every dependency is a registered edge pointing in the
//     derivation direction (from is a direct upstream of to), and every edge
//     joins two documented nodes;
//   - a document with no routes (or the single-node document) carries empty
//     arrays, not null.
func assertScopedExportShape(t *testing.T, graph map[string]*Lineage, doc exportDocument) {
	t.Helper()
	if doc.Nodes == nil {
		t.Fatalf("nodes must be a non-nil JSON array in %+v", doc)
	}
	if doc.Edges == nil {
		t.Fatalf("edges must be a non-nil JSON array in %+v", doc)
	}
	if !slices.IsSorted(doc.Nodes) {
		t.Errorf("nodes are not in Go string order: %v", doc.Nodes)
	}
	seenNode := map[string]bool{}
	for _, name := range doc.Nodes {
		if seenNode[name] {
			t.Errorf("node %q appears more than once in %v", name, doc.Nodes)
		}
		seenNode[name] = true
		if _, ok := graph[name]; !ok {
			t.Errorf("document names unregistered dataset %q", name)
		}
	}
	seenEdge := map[[2]string]bool{}
	for i, e := range doc.Edges {
		edge := [2]string{e.From, e.To}
		if seenEdge[edge] {
			t.Errorf("edge %s -> %s appears more than once", e.From, e.To)
		}
		seenEdge[edge] = true
		if i > 0 {
			prev := doc.Edges[i-1]
			if prev.From > e.From || (prev.From == e.From && prev.To >= e.To) {
				t.Errorf("edges are not ordered by (from, to) at %d: %s->%s follows %s->%s",
					i, prev.From, prev.To, e.From, e.To)
			}
		}
		if !seenNode[e.From] || !seenNode[e.To] {
			t.Errorf("edge %s -> %s joins a node missing from the document: nodes %v",
				e.From, e.To, doc.Nodes)
		}
		derived, ok := graph[e.To]
		if !ok || !slices.Contains(derived.Parents, e.From) {
			t.Errorf("edge %s -> %s is not an existing upstream-to-derived dependency", e.From, e.To)
		}
	}
}

// assertPathInsideDocument requires every hop of an explanation path to be a
// documented node joined by the documented dependency in derivation order.
func assertPathInsideDocument(t *testing.T, doc exportDocument, path []string) {
	t.Helper()
	nodes := map[string]bool{}
	for _, name := range doc.Nodes {
		nodes[name] = true
	}
	edges := map[[2]string]bool{}
	for _, e := range doc.Edges {
		edges[[2]string{e.From, e.To}] = true
	}
	for i, name := range path {
		if !nodes[name] {
			t.Fatalf("explanation path %v uses node %q absent from document nodes %v",
				path, name, doc.Nodes)
		}
		if i+1 < len(path) && !edges[[2]string{path[i], path[i+1]}] {
			t.Fatalf("explanation path %v uses dependency %s -> %s absent from document edges",
				path, path[i], path[i+1])
		}
	}
}

func impactRecordFor(impacts []Impact, name string) (Impact, bool) {
	for _, im := range impacts {
		if im.Dataset == name {
			return im, true
		}
	}
	return Impact{}, false
}

func upstreamRecordFor(upstreams []Upstream, name string) (Upstream, bool) {
	for _, up := range upstreams {
		if up.Dataset == name {
			return up, true
		}
	}
	return Upstream{}, false
}

// assertSourceTargetPairContract runs the whole correspondence guard for one
// ordered endpoint pair against the route witness:
//
//  1. the export equals the union of every source -> target route (nodes and
//     edges), ordered and directed per the public JSON contract;
//  2. for distinct connected endpoints, Impacts(source) finds target and
//     Upstreams(target) finds source with the same distance and the same
//     source-to-target path, equal to the witness's shortest route, and that
//     path exists hop by hop inside the export;
//  3. for distinct endpoints with no forward route, both queries deny the
//     relation and the export is two empty arrays (a reverse dependency
//     included);
//  4. for identical endpoints the document is just that node with no edges,
//     and neither query lists the node itself.
func assertSourceTargetPairContract(t *testing.T, graph map[string]*Lineage, source, target string) {
	t.Helper()
	out := mustExportScoped(t, graph, source, target)
	doc := parseExport(t, out)
	assertScopedExportShape(t, graph, doc)

	wantNodes, wantEdges := expectedScopedDocument(t, graph, source, target)
	if !reflect.DeepEqual(doc.Nodes, wantNodes) {
		t.Errorf("ExportSourceTargetLineage(%q, %q) nodes = %v, want %v",
			source, target, doc.Nodes, wantNodes)
	}
	if got := exportEdgeNames(doc); !reflect.DeepEqual(got, wantEdges) {
		t.Errorf("ExportSourceTargetLineage(%q, %q) edges = %v, want %v",
			source, target, got, wantEdges)
	}

	routes := enumerateSourceTargetRoutes(t, graph, source, target)
	impacts := mustImpacts(t, graph, source)
	upstreams := mustUpstreams(t, graph, target)

	if source == target {
		if len(doc.Nodes) != 1 || doc.Nodes[0] != source || len(doc.Edges) != 0 {
			t.Errorf("same endpoint %q export must be the singleton node with no edges, got %v / %v",
				source, doc.Nodes, exportEdgeNames(doc))
		}
		if _, ok := impactRecordFor(impacts, source); ok {
			t.Errorf("Impacts(%q) must not list the origin itself", source)
		}
		if _, ok := upstreamRecordFor(upstreams, target); ok {
			t.Errorf("Upstreams(%q) must not list the target itself", target)
		}
		return
	}

	downRow, downOK := impactRecordFor(impacts, target)
	upRow, upOK := upstreamRecordFor(upstreams, source)
	if len(routes) == 0 {
		if len(doc.Nodes) != 0 || len(doc.Edges) != 0 {
			t.Errorf("(%q, %q) have no forward route: export must be two empty arrays, got %v / %v",
				source, target, doc.Nodes, exportEdgeNames(doc))
		}
		if downOK {
			t.Errorf("(%q -> %q) is not a forward route but Impacts lists %v", source, target, downRow)
		}
		if upOK {
			t.Errorf("(%q -> %q) is not a forward route but Upstreams(%q) lists %v",
				source, target, target, upRow)
		}
		return
	}

	if !downOK {
		t.Fatalf("Impacts(%q) must find connected target %q", source, target)
	}
	if !upOK {
		t.Fatalf("Upstreams(%q) must find connected source %q", target, source)
	}
	if downRow.Distance != upRow.Distance {
		t.Errorf("distance for %q -> %q disagrees: Impacts %d, Upstreams %d",
			source, target, downRow.Distance, upRow.Distance)
	}
	if !reflect.DeepEqual(downRow.Path, upRow.Path) {
		t.Errorf("path for %q -> %q disagrees: Impacts %v, Upstreams %v",
			source, target, downRow.Path, upRow.Path)
	}
	if downRow.Distance != len(downRow.Path)-1 {
		t.Errorf("distance %d does not match path %v edge count",
			downRow.Distance, downRow.Path)
	}

	// Independent shortest witness over the same registered edges.
	oracle := newLineageTestOracle(t, graph)
	wantDistance, wantPath, reachable := oracle.shortest(source, target)
	if !reachable {
		t.Fatalf("route witness found routes but the shortest-path oracle denies %q -> %q", source, target)
	}
	if downRow.Distance != wantDistance || !reflect.DeepEqual(downRow.Path, wantPath) {
		t.Errorf("query row for %q -> %q = (distance %d, path %v), want (distance %d, path %v)",
			source, target, downRow.Distance, downRow.Path, wantDistance, wantPath)
	}

	// The chosen explanation path must exist completely inside the exported
	// document, which itself keeps every route — including the unchosen ones.
	assertPathEdgesRegistered(t, graph, downRow.Path)
	assertPathInsideDocument(t, doc, downRow.Path)
	assertImpactListShape(t, graph, source, impacts)
	assertUpstreamListShape(t, graph, target, upstreams)

	// Every enumerated route, not only the shortest explanation, must survive
	// in the document node and edge set; this is the branch-preservation core.
	for _, route := range routes {
		assertPathInsideDocument(t, doc, route)
	}
}

// Exhaustive correspondence over every ordered endpoint pair in the forked and
// merged graph: changing source and target — including promoting a former
// intermediate dataset to an endpoint — must change the export to exactly the
// routes that really connect the new pair, and both directional queries must
// witness the same shortest explanation inside it.
func TestExportSourceTargetLineageCorrespondsWithImpactsAndUpstreams(t *testing.T) {
	graph := buildRegisteredGraph(t, correspondenceFixtureRegistrations())
	assertConsistent(t, graph)
	names := sortedGraphNames(graph)
	before := snapshot(graph)

	for _, source := range names {
		for _, target := range names {
			assertSourceTargetPairContract(t, graph, source, target)
		}
	}

	// Explicit anchors at the tricky scopes.

	// The main pair: the direct edge gives the distance-1 explanation from
	// both queries, yet every longer fork/merge branch stays in the export.
	mainOut := mustExportScoped(t, graph, "src", "report")
	mainDoc := parseExport(t, mainOut)
	mainNodes := []string{"a", "b", "c", "d", "g", "m", "report", "src"}
	mainEdges := [][2]string{
		{"a", "g"},
		{"a", "report"},
		{"b", "c"},
		{"b", "d"},
		{"c", "m"},
		{"d", "m"},
		{"g", "d"},
		{"g", "m"},
		{"m", "report"},
		{"src", "a"},
		{"src", "b"},
		{"src", "report"},
	}
	if !reflect.DeepEqual(mainDoc.Nodes, mainNodes) {
		t.Errorf("(src, report) nodes = %v, want %v", mainDoc.Nodes, mainNodes)
	}
	if got := exportEdgeNames(mainDoc); !reflect.DeepEqual(got, mainEdges) {
		t.Errorf("(src, report) edges = %v, want %v", got, mainEdges)
	}
	// Shared nodes and edges appear once.
	if len(mainDoc.Nodes) != len(mainNodes) || len(mainDoc.Edges) != len(mainEdges) {
		t.Errorf("duplicate entries in singleton-checked document: %v / %v", mainDoc.Nodes, exportEdgeNames(mainDoc))
	}
	// Unrelated nodes never ride along: the independent source's edge, the
	// dead-end branch and the target's downstream are all absent.
	for _, banned := range []string{"extra", "dead", "view"} {
		if slices.Contains(mainDoc.Nodes, banned) {
			t.Errorf("(src, report) must not include %q: %v", banned, mainDoc.Nodes)
		}
	}
	for _, banned := range [][2]string{{"extra", "a"}, {"src", "dead"}, {"report", "view"}} {
		if slices.Contains(exportEdgeNames(mainDoc), banned) {
			t.Errorf("(src, report) must not carry edge %s -> %s", banned[0], banned[1])
		}
	}
	impacts := mustImpacts(t, graph, "src")
	upstreams := mustUpstreams(t, graph, "report")
	assertImpactOnce(t, impacts, "report", 1, []string{"src", "report"})
	assertUpstreamOnce(t, upstreams, "src", 1, []string{"src", "report"})
	// The unchosen long routes remain in the document with every hop.
	for _, route := range [][]string{
		{"src", "a", "g", "m", "report"},
		{"src", "b", "c", "m", "report"},
		{"src", "b", "d", "m", "report"},
		{"src", "a", "g", "d", "m", "report"},
	} {
		assertPathInsideDocument(t, mainDoc, route)
	}

	// Promote a former intermediate to the target: report's direct edges leave
	// the scope, while all merges feeding m remain.
	midTarget := parseExport(t, mustExportScoped(t, graph, "src", "m"))
	if got, want := midTarget.Nodes, []string{"a", "b", "c", "d", "g", "m", "src"}; !reflect.DeepEqual(got, want) {
		t.Errorf("(src, m) nodes = %v, want %v", got, want)
	}
	if got := exportEdgeNames(midTarget); !reflect.DeepEqual(got, [][2]string{
		{"a", "g"},
		{"b", "c"},
		{"b", "d"},
		{"c", "m"},
		{"d", "m"},
		{"g", "d"},
		{"g", "m"},
		{"src", "a"},
		{"src", "b"},
	}) {
		t.Errorf("(src, m) edges = %v", got)
	}
	for _, banned := range []string{"report", "view", "extra", "dead"} {
		if slices.Contains(midTarget.Nodes, banned) {
			t.Errorf("(src, m) must not include %q: %v", banned, midTarget.Nodes)
		}
	}

	// Promote a former intermediate to the source: the direct a -> report edge
	// is the shortest explanation, but a -> g -> m and a -> g -> d -> m remain.
	aSource := parseExport(t, mustExportScoped(t, graph, "a", "report"))
	if got, want := aSource.Nodes, []string{"a", "d", "g", "m", "report"}; !reflect.DeepEqual(got, want) {
		t.Errorf("(a, report) nodes = %v, want %v", got, want)
	}
	if got := exportEdgeNames(aSource); !reflect.DeepEqual(got, [][2]string{
		{"a", "g"},
		{"a", "report"},
		{"d", "m"},
		{"g", "d"},
		{"g", "m"},
		{"m", "report"},
	}) {
		t.Errorf("(a, report) edges = %v", got)
	}
	assertImpactOnce(t, mustImpacts(t, graph, "a"), "report", 1, []string{"a", "report"})
	assertUpstreamOnce(t, mustUpstreams(t, graph, "report"), "a", 1, []string{"a", "report"})
	for _, route := range [][]string{
		{"a", "g", "m", "report"},
		{"a", "g", "d", "m", "report"},
	} {
		assertPathInsideDocument(t, aSource, route)
	}

	// Querying from the formerly "independent" source makes it a legitimate
	// endpoint: its own routes enter the document, but src (not downstream of
	// extra) and b/c (unreachable from extra) leave it, together with their
	// edges into shared nodes.
	extraDoc := parseExport(t, mustExportScoped(t, graph, "extra", "report"))
	if got, want := extraDoc.Nodes, []string{"a", "d", "extra", "g", "m", "report"}; !reflect.DeepEqual(got, want) {
		t.Errorf("(extra, report) nodes = %v, want %v", got, want)
	}
	if got := exportEdgeNames(extraDoc); !reflect.DeepEqual(got, [][2]string{
		{"a", "g"},
		{"a", "report"},
		{"d", "m"},
		{"extra", "a"},
		{"g", "d"},
		{"g", "m"},
		{"m", "report"},
	}) {
		t.Errorf("(extra, report) edges = %v", got)
	}
	for _, banned := range [][2]string{{"src", "a"}, {"b", "d"}, {"c", "m"}} {
		if slices.Contains(exportEdgeNames(extraDoc), banned) {
			t.Errorf("(extra, report) must not carry edge %s -> %s", banned[0], banned[1])
		}
	}
	assertImpactOnce(t, mustImpacts(t, graph, "extra"), "report", 2, []string{"extra", "a", "report"})
	assertUpstreamOnce(t, mustUpstreams(t, graph, "report"), "extra", 2,
		[]string{"extra", "a", "report"})

	// A merge whose equal-length shortest routes are tie-broken lexicographically:
	// b -> c -> m wins over b -> d -> m (c < d), but both routes stay exported.
	bmDoc := parseExport(t, mustExportScoped(t, graph, "b", "m"))
	if got, want := bmDoc.Nodes, []string{"b", "c", "d", "m"}; !reflect.DeepEqual(got, want) {
		t.Errorf("(b, m) nodes = %v, want %v", got, want)
	}
	if got := exportEdgeNames(bmDoc); !reflect.DeepEqual(got, [][2]string{
		{"b", "c"},
		{"b", "d"},
		{"c", "m"},
		{"d", "m"},
	}) {
		t.Errorf("(b, m) edges = %v", got)
	}
	assertImpactOnce(t, mustImpacts(t, graph, "b"), "m", 2, []string{"b", "c", "m"})
	assertUpstreamOnce(t, mustUpstreams(t, graph, "m"), "b", 2, []string{"b", "c", "m"})
	assertPathInsideDocument(t, bmDoc, []string{"b", "d", "m"})

	// A direct g -> m edge cannot hide the longer g -> d -> m branch, and the
	// queries still explain via the shortest route.
	gmDoc := parseExport(t, mustExportScoped(t, graph, "g", "m"))
	if got, want := gmDoc.Nodes, []string{"d", "g", "m"}; !reflect.DeepEqual(got, want) {
		t.Errorf("(g, m) nodes = %v, want %v", got, want)
	}
	if got := exportEdgeNames(gmDoc); !reflect.DeepEqual(got, [][2]string{
		{"d", "m"},
		{"g", "d"},
		{"g", "m"},
	}) {
		t.Errorf("(g, m) edges = %v", got)
	}
	assertImpactOnce(t, mustImpacts(t, graph, "g"), "m", 1, []string{"g", "m"})

	// The target's downstream belongs to an export that actually ends there:
	// (src, view) extends through report, while (src, report) above excludes it.
	viewDoc := parseExport(t, mustExportScoped(t, graph, "src", "view"))
	if got, want := viewDoc.Nodes, []string{"a", "b", "c", "d", "g", "m", "report", "src", "view"}; !reflect.DeepEqual(got, want) {
		t.Errorf("(src, view) nodes = %v, want %v", got, want)
	}
	if !slices.Contains(exportEdgeNames(viewDoc), [2]string{"report", "view"}) {
		t.Errorf("(src, view) must carry report -> view: %v", exportEdgeNames(viewDoc))
	}
	assertImpactOnce(t, mustImpacts(t, graph, "src"), "view", 2, []string{"src", "report", "view"})
	assertUpstreamOnce(t, mustUpstreams(t, graph, "view"), "src", 2,
		[]string{"src", "report", "view"})

	// A source downstream that cannot reach the target scopes to itself only.
	deadDoc := parseExport(t, mustExportScoped(t, graph, "src", "dead"))
	if got, want := deadDoc.Nodes, []string{"dead", "src"}; !reflect.DeepEqual(got, want) {
		t.Errorf("(src, dead) nodes = %v, want %v", got, want)
	}
	if got := exportEdgeNames(deadDoc); !reflect.DeepEqual(got, [][2]string{{"src", "dead"}}) {
		t.Errorf("(src, dead) edges = %v", got)
	}

	// Reverse dependencies and disconnected pairs stay two empty arrays even
	// though a real edge exists the other way, and neither query claims them.
	for _, pair := range [][2]string{
		{"report", "src"}, // src derives report, not the other way around
		{"view", "report"},
		{"m", "src"},
		{"dead", "report"},
		{"extra", "b"},
		{"view", "src"}, // two hops the wrong way
	} {
		out := mustExportScoped(t, graph, pair[0], pair[1])
		if want := `{"nodes":[],"edges":[]}`; out != want {
			t.Errorf("(%q, %q) export = %s, want %s", pair[0], pair[1], out, want)
		}
	}

	// Identical endpoints — even a dataset with both ancestors and downstreams —
	// export the singleton with no edges.
	if out := mustExportScoped(t, graph, "m", "m"); out != `{"nodes":["m"],"edges":[]}` {
		t.Errorf("(m, m) export = %s, want singleton with empty edges", out)
	}

	if !reflect.DeepEqual(snapshot(graph), before) {
		t.Fatalf("exports and queries changed the graph: before=%v after=%v",
			before, snapshot(graph))
	}
	assertConsistent(t, graph)
}

// The exact public JSON text for the main forked-and-merged pair is pinned.
func TestExportSourceTargetLineageCorrespondenceExactJSON(t *testing.T) {
	graph := buildRegisteredGraph(t, correspondenceFixtureRegistrations())

	out := mustExportScoped(t, graph, "src", "report")
	want := `{"nodes":["a","b","c","d","g","m","report","src"],` +
		`"edges":[` +
		`{"from":"a","to":"g"},{"from":"a","to":"report"},` +
		`{"from":"b","to":"c"},{"from":"b","to":"d"},` +
		`{"from":"c","to":"m"},` +
		`{"from":"d","to":"m"},` +
		`{"from":"g","to":"d"},{"from":"g","to":"m"},` +
		`{"from":"m","to":"report"},` +
		`{"from":"src","to":"a"},{"from":"src","to":"b"},{"from":"src","to":"report"}]}`
	if out != want {
		t.Errorf("export =\n%s\nwant:\n%s", out, want)
	}
}

// The same final lineage built through a different registration order and with
// reordered direct-upstream lists must export byte-identical documents for
// every ordered endpoint pair.
func TestExportSourceTargetLineageCorrespondenceOrderIndependent(t *testing.T) {
	first := buildRegisteredGraph(t, correspondenceFixtureRegistrations())
	// Same final relationships in a legal topological order, with flipped
	// parent lists at every merge.
	second := buildRegisteredGraph(t, [][]string{
		{"src"},
		{"extra"},
		{"b", "src"},
		{"a", "extra", "src"}, // parent list flipped vs the fixture
		{"c", "b"},
		{"g", "a"},
		{"d", "g", "b"}, // parents in the opposite order
		{"m", "g", "c", "d"},
		{"dead", "src"},
		{"report", "m", "src", "a"}, // direct-upstream order flipped
		{"view", "report"},
	})

	if got := parentEdgeSet(second); !reflect.DeepEqual(got, parentEdgeSet(first)) {
		t.Fatalf("the two builds hold different relationships:\n%v\n%v", got, parentEdgeSet(first))
	}
	names := sortedGraphNames(first)
	for _, source := range names {
		for _, target := range names {
			if got, want := mustExportScoped(t, first, source, target),
				mustExportScoped(t, second, source, target); got != want {
				t.Errorf("(%q, %q) export depends on registration order:\nfirst:  %s\nsecond: %s",
					source, target, got, want)
			}
		}
	}
}

// Boundary behavior for registered endpoints with no forward derivation:
// same-node singleton, two empty arrays for unrelated or reverse pairs, with
// arrays (not null), and the queries answering with non-nil empty lists
// without listing the queried node itself.
func TestExportSourceTargetLineageCorrespondenceBoundaries(t *testing.T) {
	graph := buildRegisteredGraph(t, [][]string{
		{"p"},
		{"c", "p"},
		{"lone"},
	})
	before := snapshot(graph)

	if out := mustExportScoped(t, graph, "p", "p"); out != `{"nodes":["p"],"edges":[]}` {
		t.Errorf("(p, p) = %s, want singleton document", out)
	}
	for _, pair := range [][2]string{
		{"p", "lone"}, // unrelated roots
		{"lone", "c"}, // unrelated
		{"c", "p"},    // reverse of the one real edge
		{"c", "lone"},
	} {
		out := mustExportScoped(t, graph, pair[0], pair[1])
		if want := `{"nodes":[],"edges":[]}`; out != want {
			t.Errorf("(%q, %q) = %s, want %s", pair[0], pair[1], out, want)
		}
		doc := parseExport(t, out)
		if len(doc.Nodes) != 0 || doc.Nodes == nil || len(doc.Edges) != 0 || doc.Edges == nil {
			t.Errorf("(%q, %q) arrays must be non-nil and empty: nodes %#v edges %#v",
				pair[0], pair[1], doc.Nodes, doc.Edges)
		}
	}

	// The one real forward relation keeps behaving in all three surfaces.
	out := mustExportScoped(t, graph, "p", "c")
	doc := parseExport(t, out)
	if !reflect.DeepEqual(doc.Nodes, []string{"c", "p"}) {
		t.Errorf("(p, c) nodes = %v", doc.Nodes)
	}
	if got := exportEdgeNames(doc); !reflect.DeepEqual(got, [][2]string{{"p", "c"}}) {
		t.Errorf("(p, c) edges = %v", got)
	}
	assertImpactOnce(t, mustImpacts(t, graph, "p"), "c", 1, []string{"p", "c"})
	assertUpstreamOnce(t, mustUpstreams(t, graph, "c"), "p", 1, []string{"p", "c"})

	// Queries that find nothing still succeed with non-nil empty lists and
	// never list their own dataset.
	for name, downs := range map[string]int{"c": 0, "lone": 0} {
		impacts := mustImpacts(t, graph, name)
		if len(impacts) != downs || impacts == nil {
			t.Errorf("Impacts(%q) = %v, want non-nil empty list", name, impacts)
		}
	}
	for name := range map[string]int{"p": 0, "lone": 0} {
		upstreams := mustUpstreams(t, graph, name)
		if len(upstreams) != 0 || upstreams == nil {
			t.Errorf("Upstreams(%q) = %v, want non-nil empty list", name, upstreams)
		}
	}

	if !reflect.DeepEqual(snapshot(graph), before) {
		t.Fatalf("boundary operations changed the graph: before=%v after=%v",
			before, snapshot(graph))
	}
	assertConsistent(t, graph)
}

// Public entry points, return shapes and error behavior stay compatible while
// the exports and queries run: missing names are rejected in source-first
// order with empty output, and no node, bidirectional relationship or stored
// list order moves.
func TestExportSourceTargetLineageCorrespondenceErrorsAndReadOnly(t *testing.T) {
	graph := buildRegisteredGraph(t, correspondenceFixtureRegistrations())
	before := snapshot(graph)

	for _, tc := range []struct {
		name    string
		source  string
		target  string
		wantErr string
	}{
		{"empty source", "", "report", "dataset name is required"},
		{"empty source over missing target", "", "ghost", "dataset name is required"},
		{"missing source first", "ghost", "phantom", "dataset not found: ghost"},
		{"valid source then empty target", "src", "", "dataset name is required"},
		{"valid source then missing target", "src", "ghost", "dataset not found: ghost"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			out, err := ExportSourceTargetLineage(graph, tc.source, tc.target)
			if err == nil || err.Error() != tc.wantErr {
				t.Fatalf("(%q, %q): err = %v, want %q", tc.source, tc.target, err, tc.wantErr)
			}
			if out != "" {
				t.Errorf("failed export returned partial content %q", out)
			}
		})
	}

	// Exercise every surface, successful and failing, then demand the exact
	// prior graph state including parent/child list positions.
	for _, source := range sortedGraphNames(graph) {
		mustImpacts(t, graph, source)
		for _, target := range sortedGraphNames(graph) {
			mustExportScoped(t, graph, source, target)
		}
	}
	for _, target := range sortedGraphNames(graph) {
		mustUpstreams(t, graph, target)
	}
	if _, err := Impacts(graph, "ghost"); err == nil || !strings.Contains(err.Error(), "ghost") {
		t.Errorf("Impacts(ghost): want error naming ghost, got %v", err)
	}
	if _, err := Upstreams(graph, "ghost"); err == nil || !strings.Contains(err.Error(), "ghost") {
		t.Errorf("Upstreams(ghost): want error naming ghost, got %v", err)
	}

	if !reflect.DeepEqual(snapshot(graph), before) {
		t.Fatalf("operations changed the graph: before=%v after=%v", before, snapshot(graph))
	}
	assertConsistent(t, graph)
}
