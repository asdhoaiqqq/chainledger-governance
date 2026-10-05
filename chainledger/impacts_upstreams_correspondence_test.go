package chainledger

import (
	"reflect"
	"slices"
	"sort"
	"strings"
	"testing"
)

// This file is the bidirectional regression guard for the two lineage queries:
// Impacts (source -> derived datasets) and Upstreams (derived dataset ->
// sources). They are opposite views of the very same registered parent edges,
// so for two distinct registered datasets u and v the relation v derives from
// u must read identically from either side: the dataset is listed in both
// directions or in neither, the Distance is the same edge count, and the Path
// is the very same name sequence written from the source to the derived data.

// lineageTestOracle is an independent witness for both lineage queries. It
// re-derives reachability, shortest edge counts and the lexicographically
// smallest shortest path directly from the stored parent edges by enumerating
// simple walks, without calling Impacts, Upstreams or any production
// traversal. Register rejects cycles, so simple-walk enumeration terminates.
type lineageTestOracle struct {
	children map[string][]string
}

func newLineageTestOracle(t *testing.T, graph map[string]*Lineage) *lineageTestOracle {
	t.Helper()
	o := &lineageTestOracle{children: map[string][]string{}}
	for name := range graph {
		o.children[name] = nil
	}
	for name, entry := range graph {
		for _, parent := range entry.Parents {
			if _, ok := graph[parent]; !ok {
				t.Fatalf("oracle setup: %s lists unregistered parent %s", name, parent)
			}
			o.children[parent] = append(o.children[parent], name)
		}
	}
	return o
}

// shortest answers whether from derives to along registered parent edges and,
// when it does, the minimum edge count and the lexicographically smallest
// shortest path written from `from` to `to`, including both ends.
func (o *lineageTestOracle) shortest(from, to string) (int, []string, bool) {
	var walks [][]string
	seen := map[string]bool{from: true}
	var walk func(path []string)
	walk = func(path []string) {
		cur := path[len(path)-1]
		if cur == to {
			walks = append(walks, append([]string(nil), path...))
			return
		}
		for _, next := range o.children[cur] {
			if seen[next] {
				continue
			}
			seen[next] = true
			walk(append(path, next))
			delete(seen, next)
		}
	}
	walk([]string{from})
	if len(walks) == 0 {
		return 0, nil, false
	}
	shortestLength := len(walks[0])
	for _, p := range walks[1:] {
		if len(p) < shortestLength {
			shortestLength = len(p)
		}
	}
	var candidates [][]string
	for _, p := range walks {
		if len(p) == shortestLength {
			candidates = append(candidates, p)
		}
	}
	chosen := slices.MinFunc(candidates, slices.Compare)
	return shortestLength - 1, append([]string(nil), chosen...), true
}

func sortedGraphNames(graph map[string]*Lineage) []string {
	names := make([]string, 0, len(graph))
	for name := range graph {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// assertPathEdgesRegistered checks every hop of an explanation path against
// the graph: adjacent names must be registered datasets linked by a parent
// edge (the earlier name is a direct upstream of the later one).
func assertPathEdgesRegistered(t *testing.T, graph map[string]*Lineage, path []string) {
	t.Helper()
	if len(path) == 0 {
		t.Fatal("explanation path is empty")
	}
	for i := 0; i+1 < len(path); i++ {
		child, ok := graph[path[i+1]]
		if !ok {
			t.Fatalf("path %v names unregistered dataset %s", path, path[i+1])
		}
		if !slices.Contains(child.Parents, path[i]) {
			t.Fatalf("path %v claims the unregistered dependency %s -> %s", path, path[i], path[i+1])
		}
	}
}

// assertImpactListShape enforces the presentation contract shared by both
// queries on a downstream result: non-nil list, no self row, no duplicates,
// distance-ascending then name ordering, and well-formed registered-edge paths.
func assertImpactListShape(t *testing.T, graph map[string]*Lineage, origin string, got []Impact) {
	t.Helper()
	if got == nil {
		t.Fatalf("Impacts(%s) returned nil", origin)
	}
	listed := map[string]bool{}
	for i, im := range got {
		if im.Dataset == origin {
			t.Fatalf("Impacts(%s) lists the origin itself", origin)
		}
		if listed[im.Dataset] {
			t.Fatalf("Impacts(%s) lists %s more than once", origin, im.Dataset)
		}
		listed[im.Dataset] = true
		if len(im.Path) != im.Distance+1 {
			t.Fatalf("Impacts(%s) record %v: path edges %d do not match distance %d",
				origin, im, len(im.Path)-1, im.Distance)
		}
		if im.Path[0] != origin || im.Path[len(im.Path)-1] != im.Dataset {
			t.Fatalf("Impacts(%s) record %v must run from the origin to the dataset, inclusive",
				origin, im)
		}
		assertPathEdgesRegistered(t, graph, im.Path)
		if i > 0 {
			prev := got[i-1]
			if prev.Distance > im.Distance ||
				(prev.Distance == im.Distance && prev.Dataset >= im.Dataset) {
				t.Fatalf("Impacts(%s) order violation at %d: %v comes before %v",
					origin, i, prev, im)
			}
		}
	}
}

// assertUpstreamListShape is the provenance-side counterpart: the path runs
// from the listed source to the queried target, inclusive.
func assertUpstreamListShape(t *testing.T, graph map[string]*Lineage, target string, got []Upstream) {
	t.Helper()
	if got == nil {
		t.Fatalf("Upstreams(%s) returned nil", target)
	}
	listed := map[string]bool{}
	for i, up := range got {
		if up.Dataset == target {
			t.Fatalf("Upstreams(%s) lists the target itself", target)
		}
		if listed[up.Dataset] {
			t.Fatalf("Upstreams(%s) lists %s more than once", target, up.Dataset)
		}
		listed[up.Dataset] = true
		if len(up.Path) != up.Distance+1 {
			t.Fatalf("Upstreams(%s) record %v: path edges %d do not match distance %d",
				target, up, len(up.Path)-1, up.Distance)
		}
		if up.Path[0] != up.Dataset || up.Path[len(up.Path)-1] != target {
			t.Fatalf("Upstreams(%s) record %v must run from the source to the target, inclusive",
				target, up)
		}
		assertPathEdgesRegistered(t, graph, up.Path)
		if i > 0 {
			prev := got[i-1]
			if prev.Distance > up.Distance ||
				(prev.Distance == up.Distance && prev.Dataset >= up.Dataset) {
				t.Fatalf("Upstreams(%s) order violation at %d: %v comes before %v",
					target, i, prev, up)
			}
		}
	}
}

// assertDirectionsCorrespond is the core guard: for every ordered pair of
// distinct registered datasets (u, v), v appears in Impacts(u) exactly when u
// appears in Upstreams(v), and the two rows agree on distance and on the full
// source-to-derived path. Membership is additionally cross-checked against the
// independent oracle.
func assertDirectionsCorrespond(t *testing.T, graph map[string]*Lineage, names []string, oracle *lineageTestOracle) {
	t.Helper()
	down := make(map[string]map[string]Impact, len(names))
	up := make(map[string]map[string]Upstream, len(names))
	for _, n := range names {
		impacts := mustImpacts(t, graph, n)
		upstreams := mustUpstreams(t, graph, n)
		assertImpactListShape(t, graph, n, impacts)
		assertUpstreamListShape(t, graph, n, upstreams)
		down[n] = make(map[string]Impact, len(impacts))
		for _, im := range impacts {
			down[n][im.Dataset] = im
		}
		up[n] = make(map[string]Upstream, len(upstreams))
		for _, um := range upstreams {
			up[n][um.Dataset] = um
		}
	}

	for _, u := range names {
		for _, v := range names {
			if u == v {
				continue
			}
			dr, downOK := down[u][v]
			ur, upOK := up[v][u]
			wantDistance, wantPath, reachable := oracle.shortest(u, v)

			if downOK != upOK {
				t.Fatalf("directions disagree on %s -> %s: Impacts lists it = %v, Upstreams(%s) lists %s = %v",
					u, v, downOK, v, u, upOK)
			}
			if downOK != reachable {
				t.Fatalf("Impacts(%s) lists %s = %v, but the registered edges reach it = %v",
					u, v, downOK, reachable)
			}
			if !reachable {
				continue
			}
			if dr.Distance != wantDistance || ur.Distance != wantDistance {
				t.Fatalf("%s -> %s distances disagree: Impacts %d, Upstreams %d, oracle %d",
					u, v, dr.Distance, ur.Distance, wantDistance)
			}
			if dr.Distance != ur.Distance {
				t.Fatalf("%s -> %s: distance seen from downstream (%d) and upstream (%d) must be equal",
					u, v, dr.Distance, ur.Distance)
			}
			if !reflect.DeepEqual(dr.Path, wantPath) {
				t.Fatalf("%s -> %s: Impacts path %v, want %v", u, v, dr.Path, wantPath)
			}
			if !reflect.DeepEqual(ur.Path, wantPath) {
				t.Fatalf("%s -> %s: Upstreams(%s) path for %s is %v, want %v",
					u, v, v, u, ur.Path, wantPath)
			}
			if !reflect.DeepEqual(dr.Path, ur.Path) {
				t.Fatalf("%s -> %s: the two directions explain one dependency with different paths %v vs %v",
					u, v, dr.Path, ur.Path)
			}
		}
	}
}

// expectedImpacts is the oracle-derived complete answer for Impacts(origin),
// sorted exactly as the production contract requires.
func expectedImpacts(names []string, oracle *lineageTestOracle, origin string) []Impact {
	want := []Impact{}
	for _, to := range names {
		if to == origin {
			continue
		}
		if distance, path, ok := oracle.shortest(origin, to); ok {
			want = append(want, Impact{Dataset: to, Distance: distance, Path: path})
		}
	}
	sort.Slice(want, func(i, j int) bool {
		if want[i].Distance != want[j].Distance {
			return want[i].Distance < want[j].Distance
		}
		return want[i].Dataset < want[j].Dataset
	})
	return want
}

// expectedUpstreams is the oracle-derived complete answer for Upstreams(target):
// every source with its path written from source to target.
func expectedUpstreams(names []string, oracle *lineageTestOracle, target string) []Upstream {
	want := []Upstream{}
	for _, from := range names {
		if from == target {
			continue
		}
		if distance, path, ok := oracle.shortest(from, target); ok {
			want = append(want, Upstream{Dataset: from, Distance: distance, Path: path})
		}
	}
	sort.Slice(want, func(i, j int) bool {
		if want[i].Distance != want[j].Distance {
			return want[i].Distance < want[j].Distance
		}
		return want[i].Dataset < want[j].Dataset
	})
	return want
}

// layeredCorrespondenceRegistrations builds a graph spanning direct and
// multi-level (up to five edges) relationships, diamonds, merges where the
// branches arrive at different depths, a merge whose equal-length routes are
// decided by the full path rather than the merge node's direct-upstream order,
// two disconnected components and fully isolated datasets:
//
//	root -> a -> z ------> m ----\
//	  \      \-> long ---/  \     \
//	   \      \-> q -> merge2 -> tip -> leaf
//	    \-> b -> c ------/^  (merge2 also takes q at one level less)
//	tip also takes long at the same depth as merge2.
//	r2 -> d1 -> d2 is a separate component; iso and solo are isolated.
func layeredCorrespondenceRegistrations() [][]string {
	return [][]string{
		{"root"},
		{"r2"},
		{"iso"},
		{"solo"},
		{"a", "root"},
		{"b", "root"},
		{"z", "a"},
		{"c", "b"},
		{"q", "a"},
		{"m", "c", "z"}, // c listed before z on purpose: c < z at the merge
		{"long", "z"},
		{"merge2", "m", "q"},
		{"d1", "r2"},
		{"d2", "d1"},
		{"tip", "merge2", "long"},
		{"leaf", "tip"},
	}
}

// Whole-graph regression: both queries for every registered node agree with
// each other and with an independent shortest-path oracle, across direct and
// multi-level relations, branch merges and disconnected components.
func TestImpactsUpstreamsCorrespondenceAcrossLayeredGraph(t *testing.T) {
	graph := buildRegisteredGraph(t, layeredCorrespondenceRegistrations())
	assertConsistent(t, graph)
	names := sortedGraphNames(graph)
	oracle := newLineageTestOracle(t, graph)
	before := snapshot(graph)

	for _, n := range names {
		impacts := mustImpacts(t, graph, n)
		assertImpactListShape(t, graph, n, impacts)
		if want := expectedImpacts(names, oracle, n); !reflect.DeepEqual(impacts, want) {
			t.Fatalf("Impacts(%s) = %v, want %v", n, impacts, want)
		}

		upstreams := mustUpstreams(t, graph, n)
		assertUpstreamListShape(t, graph, n, upstreams)
		if want := expectedUpstreams(names, oracle, n); !reflect.DeepEqual(upstreams, want) {
			t.Fatalf("Upstreams(%s) = %v, want %v", n, upstreams, want)
		}
	}

	// Every related pair is witnessed identically from both directions, and no
	// unrelated pair in either direction.
	assertDirectionsCorrespond(t, graph, names, oracle)

	// Explicit anchors at the tricky merges.

	// m is reached over two equal three-edge routes, root->a->z->m and
	// root->b->c->m. Comparing only m's direct upstreams would wrongly pick the
	// c route (c < z); the full-path rule first differs at hop 1 where a < b, so
	// both directions explain m through a and z.
	impacts := mustImpacts(t, graph, "root")
	assertImpactOnce(t, impacts, "m", 3, []string{"root", "a", "z", "m"})
	upstreams := mustUpstreams(t, graph, "m")
	assertUpstreamOnce(t, upstreams, "root", 3, []string{"root", "a", "z", "m"})

	// merge2 takes m (depth 3) and q (depth 2): the q route is one edge shorter
	// and wins from both sides regardless of list order.
	assertImpactOnce(t, impacts, "merge2", 3, []string{"root", "a", "q", "merge2"})
	assertUpstreamOnce(t, mustUpstreams(t, graph, "merge2"), "root", 3,
		[]string{"root", "a", "q", "merge2"})

	// tip merges merge2 and long, both at depth 3. Its two four-edge routes are
	// root->a->q->merge2->tip and root->a->z->long->tip; they first differ at
	// hop 2 where q < z, so the merge2 route explains tip and continues into leaf.
	assertImpactOnce(t, impacts, "tip", 4, []string{"root", "a", "q", "merge2", "tip"})
	assertImpactOnce(t, impacts, "leaf", 5, []string{"root", "a", "q", "merge2", "tip", "leaf"})
	tipUpstreams := mustUpstreams(t, graph, "tip")
	assertUpstreamOnce(t, tipUpstreams, "root", 4, []string{"root", "a", "q", "merge2", "tip"})
	assertUpstreamOnce(t, mustUpstreams(t, graph, "leaf"), "root", 5,
		[]string{"root", "a", "q", "merge2", "tip", "leaf"})

	// Branch intermediates keep their own, distinct explanations instead of
	// borrowing the merge node's route.
	assertImpactOnce(t, impacts, "z", 2, []string{"root", "a", "z"})
	assertImpactOnce(t, impacts, "c", 2, []string{"root", "b", "c"})
	assertImpactOnce(t, impacts, "long", 3, []string{"root", "a", "z", "long"})
	assertUpstreamOnce(t, tipUpstreams, "long", 1, []string{"long", "tip"})
	assertUpstreamOnce(t, tipUpstreams, "merge2", 1, []string{"merge2", "tip"})
	assertUpstreamOnce(t, tipUpstreams, "q", 2, []string{"q", "merge2", "tip"})
	assertUpstreamOnce(t, tipUpstreams, "z", 2, []string{"z", "long", "tip"})

	// Direct relations at every level are present on both sides with equal rows.
	assertImpactOnce(t, impacts, "a", 1, []string{"root", "a"})
	assertImpactOnce(t, impacts, "b", 1, []string{"root", "b"})
	assertUpstream(t, mustUpstreams(t, graph, "z"), "a", 1, []string{"a", "z"})
	assertUpstream(t, mustUpstreams(t, graph, "leaf"), "tip", 1, []string{"tip", "leaf"})

	// The two components and the isolated datasets must never be related in
	// either direction, while each component stays internally consistent.
	r2Impacts := mustImpacts(t, graph, "r2")
	if got, want := impactNames(r2Impacts), []string{"d1", "d2"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("Impacts(r2) = %v, want only its own component %v", got, want)
	}
	assertImpact(t, r2Impacts, "d2", 2, []string{"r2", "d1", "d2"})
	d2Upstreams := mustUpstreams(t, graph, "d2")
	if got, want := upstreamNames(d2Upstreams), []string{"d1", "r2"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("Upstreams(d2) = %v, want only its own component %v", got, want)
	}
	assertUpstream(t, d2Upstreams, "r2", 2, []string{"r2", "d1", "d2"})
	for _, across := range [][2]string{
		{"root", "r2"}, {"root", "d2"}, {"a", "d1"}, {"m", "d2"},
		{"r2", "leaf"}, {"iso", "root"}, {"solo", "tip"}, {"d1", "iso"},
	} {
		if _, ok := downRecord(mustImpacts(t, graph, across[0]), across[1]); ok {
			t.Fatalf("%s must not list unrelated %s downstream", across[0], across[1])
		}
		if _, ok := upRecord(mustUpstreams(t, graph, across[1]), across[0]); ok {
			t.Fatalf("%s must not list unrelated %s upstream", across[1], across[0])
		}
	}

	// Registered nodes with no relation in a direction answer with a non-nil
	// empty list rather than an error or a nil slice.
	for _, leafName := range []string{"leaf", "d2", "iso", "solo"} {
		got := mustImpacts(t, graph, leafName)
		if got == nil || len(got) != 0 {
			t.Fatalf("Impacts(%s) = %v, want non-nil empty list", leafName, got)
		}
	}
	for _, rootName := range []string{"root", "r2", "iso", "solo"} {
		got := mustUpstreams(t, graph, rootName)
		if got == nil || len(got) != 0 {
			t.Fatalf("Upstreams(%s) = %v, want non-nil empty list", rootName, got)
		}
	}

	// Unregistered names fail in both directions, the error names the dataset
	// and the result is nil; an empty name is a missing-name error.
	if got, err := Impacts(graph, "ghost"); err == nil ||
		!strings.Contains(err.Error(), "ghost") || got != nil {
		t.Fatalf("Impacts(ghost): want naming error and nil, got %v, %v", got, err)
	}
	if got, err := Upstreams(graph, "ghost"); err == nil ||
		!strings.Contains(err.Error(), "ghost") || got != nil {
		t.Fatalf("Upstreams(ghost): want naming error and nil, got %v, %v", got, err)
	}
	if got, err := Impacts(graph, ""); err == nil ||
		!strings.Contains(err.Error(), "name is required") || got != nil {
		t.Fatalf("Impacts(empty): want required-name error and nil, got %v, %v", got, err)
	}
	if got, err := Upstreams(graph, ""); err == nil ||
		!strings.Contains(err.Error(), "name is required") || got != nil {
		t.Fatalf("Upstreams(empty): want required-name error and nil, got %v, %v", got, err)
	}
	if got, err := Impacts(map[string]*Lineage{}, "ghost"); err == nil || got != nil ||
		!strings.Contains(err.Error(), "ghost") {
		t.Fatalf("Impacts on empty graph: want naming error and nil, got %v, %v", got, err)
	}
	if got, err := Upstreams(nil, "ghost"); err == nil || got != nil ||
		!strings.Contains(err.Error(), "ghost") {
		t.Fatalf("Upstreams on nil graph: want naming error and nil, got %v, %v", got, err)
	}

	// All queries together leave nodes, relationships and stored list order as
	// they were before and after.
	if !reflect.DeepEqual(snapshot(graph), before) {
		t.Fatalf("queries changed the graph: before=%v after=%v", before, snapshot(graph))
	}
	assertConsistent(t, graph)
}

func downRecord(impacts []Impact, name string) (Impact, bool) {
	for _, im := range impacts {
		if im.Dataset == name {
			return im, true
		}
	}
	return Impact{}, false
}

func upRecord(upstreams []Upstream, name string) (Upstream, bool) {
	for _, up := range upstreams {
		if up.Dataset == name {
			return up, true
		}
	}
	return Upstream{}, false
}

// Focused merge regression exercised simultaneously from both directions.
// source feeds a and b; a feeds z and d1; b feeds c; report depends on c and z
// (c declared first); view derives report; d1 -> d2 and win depends on d2 and b
// together; iso stands alone.
//
//	report: source->a->z->report vs source->b->c->report, equal length. The
//	        merge node's own direct-upstream order prefers c (c < z), but the
//	        full path first differs at hop 1 (a < b): both queries must choose
//	        the a/z route.
//	win:    source->b->win is two edges; source->a->d1->d2->win is four edges
//	        and is lexicographically smaller at hop 1 (a < b) but longer, so the
//	        shorter b route must win from both queries.
func TestImpactsUpstreamsMergedBranchesAgreeFromBothDirections(t *testing.T) {
	build := func() map[string]*Lineage {
		return buildRegisteredGraph(t, [][]string{
			{"source"},
			{"a", "source"},
			{"b", "source"},
			{"z", "a"},
			{"c", "b"},
			{"report", "c", "z"}, // c ahead of z on purpose
			{"view", "report"},
			{"d1", "a"},
			{"d2", "d1"},
			{"win", "d2", "b"}, // equal parents ordered against the short-route name
			{"iso"},
		})
	}

	graph := build()
	assertConsistent(t, graph)
	names := sortedGraphNames(graph)
	oracle := newLineageTestOracle(t, graph)
	before := snapshot(graph)

	impacts := mustImpacts(t, graph, "source")
	wantImpacts := []Impact{
		{Dataset: "a", Distance: 1, Path: []string{"source", "a"}},
		{Dataset: "b", Distance: 1, Path: []string{"source", "b"}},
		{Dataset: "c", Distance: 2, Path: []string{"source", "b", "c"}},
		{Dataset: "d1", Distance: 2, Path: []string{"source", "a", "d1"}},
		{Dataset: "win", Distance: 2, Path: []string{"source", "b", "win"}},
		{Dataset: "z", Distance: 2, Path: []string{"source", "a", "z"}},
		{Dataset: "d2", Distance: 3, Path: []string{"source", "a", "d1", "d2"}},
		{Dataset: "report", Distance: 3, Path: []string{"source", "a", "z", "report"}},
		{Dataset: "view", Distance: 4, Path: []string{"source", "a", "z", "report", "view"}},
	}
	if !reflect.DeepEqual(impacts, wantImpacts) {
		t.Fatalf("Impacts(source) = %v, want %v", impacts, wantImpacts)
	}

	upstreams := mustUpstreams(t, graph, "report")
	wantUpstreams := []Upstream{
		{Dataset: "c", Distance: 1, Path: []string{"c", "report"}},
		{Dataset: "z", Distance: 1, Path: []string{"z", "report"}},
		{Dataset: "a", Distance: 2, Path: []string{"a", "z", "report"}},
		{Dataset: "b", Distance: 2, Path: []string{"b", "c", "report"}},
		{Dataset: "source", Distance: 3, Path: []string{"source", "a", "z", "report"}},
	}
	if !reflect.DeepEqual(upstreams, wantUpstreams) {
		t.Fatalf("Upstreams(report) = %v, want %v", upstreams, wantUpstreams)
	}

	// The single shared source->report dependency must read identically from
	// either query: same distance, same full explanation path.
	downRow, downOK := downRecord(impacts, "report")
	upRow, upOK := upRecord(upstreams, "source")
	if !downOK || !upOK {
		t.Fatalf("the relation is missing from one side: Impacts=%v Upstreams=%v", downOK, upOK)
	}
	if downRow.Distance != upRow.Distance || !reflect.DeepEqual(downRow.Path, upRow.Path) {
		t.Fatalf("source->report explained differently: downstream %v vs upstream %v", downRow, upRow)
	}
	if got, want := downRow.Path, []string{"source", "a", "z", "report"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("merge path = %v, want %v (full path a<b must beat merge-local c<z)", got, want)
	}

	// win's shorter b route beats the lexicographically smaller a route from
	// both directions, and both sides report the exact same row.
	assertImpactOnce(t, impacts, "win", 2, []string{"source", "b", "win"})
	winUpstreams := mustUpstreams(t, graph, "win")
	wantWinUpstreams := []Upstream{
		{Dataset: "b", Distance: 1, Path: []string{"b", "win"}},
		{Dataset: "d2", Distance: 1, Path: []string{"d2", "win"}},
		{Dataset: "d1", Distance: 2, Path: []string{"d1", "d2", "win"}},
		{Dataset: "source", Distance: 2, Path: []string{"source", "b", "win"}},
		{Dataset: "a", Distance: 3, Path: []string{"a", "d1", "d2", "win"}},
	}
	if !reflect.DeepEqual(winUpstreams, wantWinUpstreams) {
		t.Fatalf("Upstreams(win) = %v, want %v", winUpstreams, wantWinUpstreams)
	}
	winDown, _ := downRecord(impacts, "win")
	winUp, _ := upRecord(winUpstreams, "source")
	if winDown.Distance != 2 || winUp.Distance != 2 ||
		!reflect.DeepEqual(winDown.Path, []string{"source", "b", "win"}) ||
		!reflect.DeepEqual(winUp.Path, []string{"source", "b", "win"}) {
		t.Fatalf("win's longer a-first route must not replace the shorter b route: down=%v up=%v",
			winDown, winUp)
	}

	// Branch intermediates keep their own correct rows on both sides despite
	// sharing source with each other and report as their merge point: none is
	// missing, duplicated, or explained through another pair's route.
	for _, want := range []Impact{
		{Dataset: "a", Distance: 1, Path: []string{"source", "a"}},
		{Dataset: "b", Distance: 1, Path: []string{"source", "b"}},
		{Dataset: "z", Distance: 2, Path: []string{"source", "a", "z"}},
		{Dataset: "c", Distance: 2, Path: []string{"source", "b", "c"}},
		{Dataset: "view", Distance: 4, Path: []string{"source", "a", "z", "report", "view"}},
	} {
		assertImpactOnce(t, impacts, want.Dataset, want.Distance, want.Path)
	}
	for _, want := range []Upstream{
		{Dataset: "z", Distance: 1, Path: []string{"z", "report"}},
		{Dataset: "c", Distance: 1, Path: []string{"c", "report"}},
		{Dataset: "a", Distance: 2, Path: []string{"a", "z", "report"}},
		{Dataset: "b", Distance: 2, Path: []string{"b", "c", "report"}},
	} {
		assertUpstreamOnce(t, upstreams, want.Dataset, want.Distance, want.Path)
	}
	// view inherits report's merge explanation; source reaches it only through
	// report, and the row matches the downstream side exactly.
	assertUpstreamOnce(t, mustUpstreams(t, graph, "view"), "source", 4,
		[]string{"source", "a", "z", "report", "view"})

	// The isolated dataset is related to nothing in either direction.
	if slices.Contains(impactNames(impacts), "iso") {
		t.Fatalf("iso leaked into Impacts(source): %v", impactNames(impacts))
	}
	if slices.Contains(upstreamNames(upstreams), "iso") {
		t.Fatalf("iso leaked into Upstreams(report): %v", upstreamNames(upstreams))
	}
	isoDown := mustImpacts(t, graph, "iso")
	isoUp := mustUpstreams(t, graph, "iso")
	if isoDown == nil || len(isoDown) != 0 || isoUp == nil || len(isoUp) != 0 {
		t.Fatalf("iso queries must be non-nil empty, got down=%v up=%v", isoDown, isoUp)
	}

	// Full pairwise correspondence guard for this graph too.
	assertDirectionsCorrespond(t, graph, names, oracle)
	if !reflect.DeepEqual(snapshot(graph), before) {
		t.Fatalf("queries changed the graph: before=%v after=%v", before, snapshot(graph))
	}

	// Adding the direct source->report edge makes edge count win outright: the
	// three-edge a/z route must disappear from both sides even though it is
	// lexicographically smaller, and the new edge's position in report's
	// declared list must not change anything.
	for _, parents := range [][]string{
		{"source", "c", "z"},
		{"z", "c", "source"},
		{"c", "source", "z"},
	} {
		mustRegister(t, graph, "report", parents...)
		assertConsistent(t, graph)

		impacts = mustImpacts(t, graph, "source")
		wantImpacts = []Impact{
			{Dataset: "a", Distance: 1, Path: []string{"source", "a"}},
			{Dataset: "b", Distance: 1, Path: []string{"source", "b"}},
			{Dataset: "report", Distance: 1, Path: []string{"source", "report"}},
			{Dataset: "c", Distance: 2, Path: []string{"source", "b", "c"}},
			{Dataset: "d1", Distance: 2, Path: []string{"source", "a", "d1"}},
			{Dataset: "view", Distance: 2, Path: []string{"source", "report", "view"}},
			{Dataset: "win", Distance: 2, Path: []string{"source", "b", "win"}},
			{Dataset: "z", Distance: 2, Path: []string{"source", "a", "z"}},
			{Dataset: "d2", Distance: 3, Path: []string{"source", "a", "d1", "d2"}},
		}
		if !reflect.DeepEqual(impacts, wantImpacts) {
			t.Fatalf("parents %v: Impacts(source) = %v, want %v", parents, impacts, wantImpacts)
		}
		upstreams = mustUpstreams(t, graph, "report")
		wantUpstreams = []Upstream{
			{Dataset: "c", Distance: 1, Path: []string{"c", "report"}},
			{Dataset: "source", Distance: 1, Path: []string{"source", "report"}},
			{Dataset: "z", Distance: 1, Path: []string{"z", "report"}},
			{Dataset: "a", Distance: 2, Path: []string{"a", "z", "report"}},
			{Dataset: "b", Distance: 2, Path: []string{"b", "c", "report"}},
		}
		if !reflect.DeepEqual(upstreams, wantUpstreams) {
			t.Fatalf("parents %v: Upstreams(report) = %v, want %v", parents, upstreams, wantUpstreams)
		}
		assertImpactOnce(t, impacts, "report", 1, []string{"source", "report"})
		assertImpactOnce(t, impacts, "view", 2, []string{"source", "report", "view"})
		assertUpstreamOnce(t, upstreams, "source", 1, []string{"source", "report"})

		// Same dependency, identical row from both directions; the unrelated win
		// merge and its own explanation are untouched.
		downRow, _ = downRecord(impacts, "report")
		upRow, _ = upRecord(upstreams, "source")
		if downRow.Distance != upRow.Distance || !reflect.DeepEqual(downRow.Path, upRow.Path) {
			t.Fatalf("parents %v: source->report rows diverge: %v vs %v", parents, downRow, upRow)
		}
		assertImpactOnce(t, impacts, "win", 2, []string{"source", "b", "win"})
		assertDirectionsCorrespond(t, graph, names, newLineageTestOracle(t, graph))
	}
}

// parentEdgeSet flattens the final registered dependencies as unordered pairs
// [child, parent], so two builds with different registration or list orders
// can be compared on relationships alone.
func parentEdgeSet(graph map[string]*Lineage) map[[2]string]bool {
	edges := map[[2]string]bool{}
	for name, entry := range graph {
		for _, parent := range entry.Parents {
			edges[[2]string{name, parent}] = true
		}
	}
	return edges
}

// The same final lineage built through different legal registration orders and
// with differently ordered direct-upstream lists must yield identical
// relations, distances and explanation paths in both directions, for every
// registered dataset.
func TestImpactsUpstreamsIndependentOfRegistrationOrder(t *testing.T) {
	sequences := [][][]string{
		// Baseline: one component top-down, the other early, merge parents in
		// non-name order.
		layeredCorrespondenceRegistrations(),
		// Second component first, branches reversed, every merge's parent list
		// flipped, roots iso/solo registered last.
		{
			{"r2"},
			{"d1", "r2"},
			{"d2", "d1"},
			{"root"},
			{"b", "root"},
			{"c", "b"},
			{"a", "root"},
			{"z", "a"},
			{"q", "a"},
			{"long", "z"},
			{"m", "z", "c"},
			{"merge2", "q", "m"},
			{"tip", "long", "merge2"},
			{"leaf", "tip"},
			{"iso"},
			{"solo"},
		},
		// Roots first, middle layer interleaved across components, parent lists
		// mixed yet differently again.
		{
			{"iso"},
			{"root"},
			{"r2"},
			{"solo"},
			{"a", "root"},
			{"z", "a"},
			{"b", "root"},
			{"c", "b"},
			{"d1", "r2"},
			{"long", "z"},
			{"m", "z", "c"},
			{"q", "a"},
			{"merge2", "m", "q"},
			{"d2", "d1"},
			{"tip", "merge2", "long"},
			{"leaf", "tip"},
		},
	}

	graphs := make([]map[string]*Lineage, len(sequences))
	for i, seq := range sequences {
		graphs[i] = buildRegisteredGraph(t, seq)
		assertConsistent(t, graphs[i])
	}

	base := graphs[0]
	baseNames := sortedGraphNames(base)
	baseEdges := parentEdgeSet(base)
	for gi := 1; gi < len(graphs); gi++ {
		g := graphs[gi]
		if got := parentEdgeSet(g); !reflect.DeepEqual(got, baseEdges) {
			t.Fatalf("build %d relationships differ: got %v want %v", gi, got, baseEdges)
		}
		if names := sortedGraphNames(g); !reflect.DeepEqual(names, baseNames) {
			t.Fatalf("build %d datasets differ: got %v want %v", gi, names, baseNames)
		}
	}

	// Identical relationships imply identical complete query answers, node by
	// node, in both directions — ordering included.
	for _, name := range baseNames {
		wantDown := mustImpacts(t, base, name)
		wantUp := mustUpstreams(t, base, name)
		for gi := 1; gi < len(graphs); gi++ {
			if got := mustImpacts(t, graphs[gi], name); !reflect.DeepEqual(got, wantDown) {
				t.Fatalf("build %d Impacts(%s) = %v, want %v", gi, name, got, wantDown)
			}
			if got := mustUpstreams(t, graphs[gi], name); !reflect.DeepEqual(got, wantUp) {
				t.Fatalf("build %d Upstreams(%s) = %v, want %v", gi, name, got, wantUp)
			}
		}
	}

	// Every build also passes the pairwise two-direction correspondence guard.
	for gi, g := range graphs {
		assertDirectionsCorrespond(t, g, sortedGraphNames(g), newLineageTestOracle(t, g))
		if !slices.Equal(sortedGraphNames(g), baseNames) {
			t.Fatalf("build %d dataset set changed after queries", gi)
		}
	}
}

// Small-graph contract for the boundary cases: a registered node without a
// relation in a direction succeeds with a non-nil empty list and never lists
// itself, while unknown names fail in both directions with nil results.
func TestImpactsUpstreamsEmptyListsAndErrors(t *testing.T) {
	graph := buildRegisteredGraph(t, [][]string{
		{"lone"},
		{"p"},
		{"c", "p"},
	})
	before := snapshot(graph)

	// Registered but no downstream / no upstream: successful non-nil empties.
	for name, query := range map[string]func() (int, error){
		"Impacts(lone)":   func() (int, error) { r, e := Impacts(graph, "lone"); return len(r), e },
		"Impacts(c)":      func() (int, error) { r, e := Impacts(graph, "c"); return len(r), e },
		"Upstreams(lone)": func() (int, error) { r, e := Upstreams(graph, "lone"); return len(r), e },
		"Upstreams(p)":    func() (int, error) { r, e := Upstreams(graph, "p"); return len(r), e },
	} {
		if n, err := query(); err != nil || n != 0 {
			t.Fatalf("%s: want successful empty list, got n=%d err=%v", name, n, err)
		}
	}
	down := mustImpacts(t, graph, "lone")
	if down == nil {
		t.Fatal("Impacts(lone) must be non-nil")
	}
	up := mustUpstreams(t, graph, "lone")
	if up == nil {
		t.Fatal("Upstreams(lone) must be non-nil")
	}

	// The single related pair is present on both matching sides and absent from
	// the reverse queries, with equal distance and identical paths.
	assertImpactOnce(t, mustImpacts(t, graph, "p"), "c", 1, []string{"p", "c"})
	assertUpstreamOnce(t, mustUpstreams(t, graph, "c"), "p", 1, []string{"p", "c"})
	if got := mustImpacts(t, graph, "c"); len(got) != 0 {
		t.Fatalf("Impacts(c) must not list its upstream p, got %v", got)
	}
	if got := mustUpstreams(t, graph, "p"); len(got) != 0 {
		t.Fatalf("Upstreams(p) must not list its downstream c, got %v", got)
	}

	// Unregistered and empty names in both directions. The raw result must be
	// nil; names are only extracted on success.
	if got, err := Impacts(graph, "ghost"); err == nil || got != nil ||
		!strings.Contains(err.Error(), "ghost") {
		t.Fatalf("Impacts(ghost): want naming error and nil, got %v, %v", got, err)
	}
	if got, err := Impacts(graph, ""); err == nil || got != nil ||
		!strings.Contains(err.Error(), "name is required") {
		t.Fatalf("Impacts(empty): want required-name error and nil, got %v, %v", got, err)
	}
	if got, err := Upstreams(graph, "ghost"); err == nil || got != nil ||
		!strings.Contains(err.Error(), "ghost") {
		t.Fatalf("Upstreams(ghost): want naming error and nil, got %v, %v", got, err)
	}
	if got, err := Upstreams(graph, ""); err == nil || got != nil ||
		!strings.Contains(err.Error(), "name is required") {
		t.Fatalf("Upstreams(empty): want required-name error and nil, got %v, %v", got, err)
	}

	// Read-only: successful and failing queries leave the graph untouched.
	if !reflect.DeepEqual(snapshot(graph), before) {
		t.Fatalf("graph changed after boundary queries: before=%v after=%v", before, snapshot(graph))
	}
	assertConsistent(t, graph)
}
