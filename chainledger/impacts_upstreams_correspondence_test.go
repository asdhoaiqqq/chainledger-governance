package chainledger

import (
	"reflect"
	"slices"
	"strings"
	"testing"
)

// assertExplanationPath verifies that path is a distance-long walk of
// registered lineage edges leading from source to derived along the actual
// derivation direction, both endpoints included: every consecutive pair must
// be a registered downstream edge on one side and the matching upstream edge
// on the other, and the hop count must equal the reported distance.
func assertExplanationPath(t *testing.T, graph map[string]*Lineage, source, derived string, distance int, path []string) {
	t.Helper()
	if len(path) != distance+1 {
		t.Errorf("%s -> %s: path %v spans %d edges, want distance %d", source, derived, path, len(path)-1, distance)
		return
	}
	if path[0] != source || path[len(path)-1] != derived {
		t.Errorf("%s -> %s: path %v does not run from the source to the derived dataset", source, derived, path)
	}
	for i := 0; i+1 < len(path); i++ {
		from, to := path[i], path[i+1]
		entry, ok := graph[from]
		if !ok || !slices.Contains(entry.Children, to) {
			t.Errorf("%s -> %s: path %v uses unregistered downstream edge %s -> %s", source, derived, path, from, to)
		}
		next, ok := graph[to]
		if !ok || !slices.Contains(next.Parents, from) {
			t.Errorf("%s -> %s: path %v uses unregistered upstream edge %s -> %s", source, derived, path, to, from)
		}
	}
}

// assertImpactOrder verifies the shared result ordering: distance ascending,
// ties broken by dataset name in Go string order.
func assertImpactOrder(t *testing.T, origin string, impacts []Impact) {
	t.Helper()
	for i := 1; i < len(impacts); i++ {
		prev, cur := impacts[i-1], impacts[i]
		if prev.Distance > cur.Distance ||
			(prev.Distance == cur.Distance && prev.Dataset >= cur.Dataset) {
			t.Errorf("Impacts(%s) out of order at position %d: (%s,%d) before (%s,%d)",
				origin, i, prev.Dataset, prev.Distance, cur.Dataset, cur.Distance)
		}
	}
}

// assertUpstreamOrder is the upstream-direction counterpart of
// assertImpactOrder.
func assertUpstreamOrder(t *testing.T, target string, upstreams []Upstream) {
	t.Helper()
	for i := 1; i < len(upstreams); i++ {
		prev, cur := upstreams[i-1], upstreams[i]
		if prev.Distance > cur.Distance ||
			(prev.Distance == cur.Distance && prev.Dataset >= cur.Dataset) {
			t.Errorf("Upstreams(%s) out of order at position %d: (%s,%d) before (%s,%d)",
				target, i, prev.Dataset, prev.Distance, cur.Dataset, cur.Distance)
		}
	}
}

// assertImpactsUpstreamsCorrespondence queries Impacts from every registered
// dataset and Upstreams from every registered dataset, then verifies that the
// two directions describe exactly the same dependency pairs: Impacts(s) lists
// d if and only if Upstreams(d) lists s. Each related pair appears exactly
// once on each side, both sides report the same distance, and both
// explanation paths are the identical name sequence written from s to d along
// registered edges with the hop count matching the distance. Each result list
// stays ordered by distance ascending and then by dataset name, and no
// dataset ever lists itself.
func assertImpactsUpstreamsCorrespondence(t *testing.T, graph map[string]*Lineage) {
	t.Helper()

	names := make([]string, 0, len(graph))
	for name := range graph {
		names = append(names, name)
	}
	slices.Sort(names)

	type pair struct{ source, derived string }
	down := map[pair]Impact{}
	up := map[pair]Upstream{}

	for _, origin := range names {
		impacts := mustImpacts(t, graph, origin)
		assertImpactOrder(t, origin, impacts)
		seen := map[string]bool{}
		for _, im := range impacts {
			if im.Dataset == origin {
				t.Errorf("Impacts(%s) lists the origin itself in %v", origin, impactNames(impacts))
			}
			if seen[im.Dataset] {
				t.Errorf("Impacts(%s) lists %s more than once in %v", origin, im.Dataset, impactNames(impacts))
			}
			seen[im.Dataset] = true
			assertExplanationPath(t, graph, origin, im.Dataset, im.Distance, im.Path)
			down[pair{origin, im.Dataset}] = im
		}
	}
	for _, target := range names {
		upstreams := mustUpstreams(t, graph, target)
		assertUpstreamOrder(t, target, upstreams)
		seen := map[string]bool{}
		for _, u := range upstreams {
			if u.Dataset == target {
				t.Errorf("Upstreams(%s) lists the target itself in %v", target, upstreamNames(upstreams))
			}
			if seen[u.Dataset] {
				t.Errorf("Upstreams(%s) lists %s more than once in %v", target, u.Dataset, upstreamNames(upstreams))
			}
			seen[u.Dataset] = true
			assertExplanationPath(t, graph, u.Dataset, target, u.Distance, u.Path)
			up[pair{u.Dataset, target}] = u
		}
	}

	// The two directions must name exactly the same related pairs, with equal
	// distances and byte-identical explanation paths.
	for p, im := range down {
		u, ok := up[p]
		if !ok {
			t.Errorf("Impacts(%s) lists %s, but Upstreams(%s) does not list %s",
				p.source, p.derived, p.derived, p.source)
			continue
		}
		if u.Distance != im.Distance {
			t.Errorf("pair %s -> %s: Impacts distance %d, Upstreams distance %d",
				p.source, p.derived, im.Distance, u.Distance)
		}
		if !sameStrings(u.Path, im.Path) {
			t.Errorf("pair %s -> %s: Impacts path %v, Upstreams path %v",
				p.source, p.derived, im.Path, u.Path)
		}
	}
	for p := range up {
		if _, ok := down[p]; !ok {
			t.Errorf("Upstreams(%s) lists %s, but Impacts(%s) does not list %s",
				p.derived, p.source, p.source, p.derived)
		}
	}
}

// Regression: a dependency recorded through normal registration must read
// identically from both ends — the downstream impact query from the source
// and the upstream provenance query from the derived dataset name the same
// pairs, distances and explanation paths. The graph spans several levels and
// a branch merge, so both direct and multi-level dependencies are cross-checked.
func TestImpactsUpstreamsCorrespondenceAcrossLevels(t *testing.T) {
	graph := buildRegisteredGraph(t, [][]string{
		{"source"},
		{"b", "source"}, // b-side branch registered first on purpose
		{"a", "source"},
		{"c", "b"},
		{"z", "a"},
		{"report", "c", "z"}, // c listed ahead of z on purpose
		{"view", "report"},
		{"isolated"},
	})
	assertConsistent(t, graph)
	before := snapshot(graph)

	assertImpactsUpstreamsCorrespondence(t, graph)

	// Anchors for the merge: both source->report routes have 3 edges, and the
	// full-path comparison from the source picks a/z (a < b at hop 1) even
	// though report's direct upstream c sorts ahead of z. Both directions must
	// tell the same story for the same pair.
	assertImpactOnce(t, mustImpacts(t, graph, "source"), "report", 3, []string{"source", "a", "z", "report"})
	assertUpstreamOnce(t, mustUpstreams(t, graph, "report"), "source", 3, []string{"source", "a", "z", "report"})
	assertImpactOnce(t, mustImpacts(t, graph, "source"), "view", 4, []string{"source", "a", "z", "report", "view"})
	assertUpstreamOnce(t, mustUpstreams(t, graph, "view"), "source", 4, []string{"source", "a", "z", "report", "view"})

	// Intermediate datasets on each branch keep their own distances and paths:
	// from b, report is explained through c, not through the other branch's
	// a/z route, and neither side borrows another pair's explanation.
	assertImpactOnce(t, mustImpacts(t, graph, "b"), "report", 2, []string{"b", "c", "report"})
	assertUpstreamOnce(t, mustUpstreams(t, graph, "report"), "b", 2, []string{"b", "c", "report"})
	assertImpactOnce(t, mustImpacts(t, graph, "a"), "view", 3, []string{"a", "z", "report", "view"})
	assertUpstreamOnce(t, mustUpstreams(t, graph, "view"), "a", 3, []string{"a", "z", "report", "view"})

	// Unrelated registered datasets are not linked in either direction, and
	// queries that find nothing in one direction succeed with non-nil empty
	// lists rather than errors.
	if impacts := mustImpacts(t, graph, "isolated"); impacts == nil || len(impacts) != 0 {
		t.Fatalf("Impacts(isolated) = %v, want non-nil empty list", impacts)
	}
	if upstreams := mustUpstreams(t, graph, "isolated"); upstreams == nil || len(upstreams) != 0 {
		t.Fatalf("Upstreams(isolated) = %v, want non-nil empty list", upstreams)
	}
	if names := impactNames(mustImpacts(t, graph, "source")); slices.Contains(names, "isolated") {
		t.Fatalf("unrelated dataset leaked into Impacts(source): %v", names)
	}
	if names := upstreamNames(mustUpstreams(t, graph, "view")); slices.Contains(names, "isolated") {
		t.Fatalf("unrelated dataset leaked into Upstreams(view): %v", names)
	}
	// A leaf has no downstream and a root has no upstream: successful empties.
	if impacts := mustImpacts(t, graph, "view"); impacts == nil || len(impacts) != 0 {
		t.Fatalf("Impacts(view) = %v, want non-nil empty list", impacts)
	}
	if upstreams := mustUpstreams(t, graph, "source"); upstreams == nil || len(upstreams) != 0 {
		t.Fatalf("Upstreams(source) = %v, want non-nil empty list", upstreams)
	}

	// An unregistered name fails both queries with an error naming it and nil
	// results.
	impacts, err := Impacts(graph, "ghost")
	if err == nil || !strings.Contains(err.Error(), "ghost") || impacts != nil {
		t.Fatalf("Impacts(ghost) = %v, %v; want nil results and an error naming ghost", impacts, err)
	}
	upstreams, err := Upstreams(graph, "ghost")
	if err == nil || !strings.Contains(err.Error(), "ghost") || upstreams != nil {
		t.Fatalf("Upstreams(ghost) = %v, %v; want nil results and an error naming ghost", upstreams, err)
	}

	// All of the queries above are read-only: nodes, edges and stored list
	// orders are exactly as registered.
	if !reflect.DeepEqual(snapshot(graph), before) {
		t.Fatalf("queries changed graph: before=%v after=%v", before, snapshot(graph))
	}
	assertConsistent(t, graph)
}

// Regression: when a short route and a longer but lexicographically smaller
// route both connect the same pair, edge count wins in BOTH query directions.
// source reaches report via source -> z -> report (2 edges) and via
// source -> a -> b -> report (3 edges); the a-route's name sequence sorts
// earlier, but the z-route must explain the pair from both ends.
func TestImpactsUpstreamsShorterRouteBeatsLexicographicOrder(t *testing.T) {
	graph := buildRegisteredGraph(t, [][]string{
		{"source"},
		{"a", "source"},
		{"b", "a"},
		{"z", "source"},
		{"report", "b", "z"}, // the long route's hop listed first on purpose
	})
	assertConsistent(t, graph)
	before := snapshot(graph)

	wantImpacts := []Impact{
		{Dataset: "a", Distance: 1, Path: []string{"source", "a"}},
		{Dataset: "z", Distance: 1, Path: []string{"source", "z"}},
		{Dataset: "b", Distance: 2, Path: []string{"source", "a", "b"}},
		{Dataset: "report", Distance: 2, Path: []string{"source", "z", "report"}},
	}
	if got := mustImpacts(t, graph, "source"); !reflect.DeepEqual(got, wantImpacts) {
		t.Fatalf("Impacts(source) = %v, want %v", got, wantImpacts)
	}
	wantUpstreams := []Upstream{
		{Dataset: "b", Distance: 1, Path: []string{"b", "report"}},
		{Dataset: "z", Distance: 1, Path: []string{"z", "report"}},
		{Dataset: "a", Distance: 2, Path: []string{"a", "b", "report"}},
		{Dataset: "source", Distance: 2, Path: []string{"source", "z", "report"}},
	}
	if got := mustUpstreams(t, graph, "report"); !reflect.DeepEqual(got, wantUpstreams) {
		t.Fatalf("Upstreams(report) = %v, want %v", got, wantUpstreams)
	}

	// Anchors: the pair is explained by the 2-edge z route from both ends,
	// never by the lexicographically smaller 3-edge a/b route.
	assertImpactOnce(t, mustImpacts(t, graph, "source"), "report", 2, []string{"source", "z", "report"})
	assertUpstreamOnce(t, mustUpstreams(t, graph, "report"), "source", 2, []string{"source", "z", "report"})

	// The datasets on the longer branch keep their own correct records: b is
	// still a direct upstream of report, and a reaches report only through b.
	assertUpstreamOnce(t, mustUpstreams(t, graph, "report"), "a", 2, []string{"a", "b", "report"})
	assertImpactOnce(t, mustImpacts(t, graph, "a"), "report", 2, []string{"a", "b", "report"})

	assertImpactsUpstreamsCorrespondence(t, graph)
	if !reflect.DeepEqual(snapshot(graph), before) {
		t.Fatalf("queries changed graph: before=%v after=%v", before, snapshot(graph))
	}
}

// Regression: the same final dependency relationships must produce the same
// impacts, upstreams, distances and explanation paths no matter in which
// legal order the registrations happened or how each direct-upstream list was
// written. Three builds of the merge lineage differ in registration order and
// in report's upstream list order; every node's query results must be
// identical across all three.
func TestImpactsUpstreamsRegistrationOrderInvariance(t *testing.T) {
	builds := [][][]string{
		{
			{"source"},
			{"b", "source"},
			{"a", "source"},
			{"c", "b"},
			{"z", "a"},
			{"report", "c", "z"},
			{"view", "report"},
			{"isolated"},
		},
		{
			{"source"},
			{"a", "source"}, // opposite branch registered first
			{"z", "a"},
			{"b", "source"},
			{"c", "b"},
			{"report", "z", "c"}, // direct-upstream list order flipped
			{"view", "report"},
			{"isolated"},
		},
		{
			{"source"},
			{"isolated"}, // unrelated dataset registered early
			{"a", "source"},
			{"b", "source"},
			{"z", "a"},
			{"c", "b"},
			{"report", "z", "c"},
			{"view", "report"},
		},
	}

	names := []string{"source", "a", "b", "c", "z", "report", "view", "isolated"}
	var wantImpacts map[string][]Impact
	var wantUpstreams map[string][]Upstream

	for i, build := range builds {
		graph := buildRegisteredGraph(t, build)
		assertConsistent(t, graph)

		gotImpacts := map[string][]Impact{}
		gotUpstreams := map[string][]Upstream{}
		for _, name := range names {
			gotImpacts[name] = mustImpacts(t, graph, name)
			gotUpstreams[name] = mustUpstreams(t, graph, name)
		}

		if i == 0 {
			wantImpacts, wantUpstreams = gotImpacts, gotUpstreams
		} else {
			if !reflect.DeepEqual(gotImpacts, wantImpacts) {
				t.Fatalf("build %d: impacts differ from build 0: got %v, want %v", i, gotImpacts, wantImpacts)
			}
			if !reflect.DeepEqual(gotUpstreams, wantUpstreams) {
				t.Fatalf("build %d: upstreams differ from build 0: got %v, want %v", i, gotUpstreams, wantUpstreams)
			}
		}

		// Each build independently satisfies the two-direction correspondence.
		assertImpactsUpstreamsCorrespondence(t, graph)
	}

	// Anchor the shared expectation so the comparison above cannot pass on a
	// common wrong answer: the merge is explained through a/z from both ends.
	assertImpactOnce(t, wantImpacts["source"], "report", 3, []string{"source", "a", "z", "report"})
	assertUpstreamOnce(t, wantUpstreams["report"], "source", 3, []string{"source", "a", "z", "report"})
}
