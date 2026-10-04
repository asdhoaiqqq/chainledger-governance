package chainledger

import (
	"reflect"
	"slices"
	"strings"
	"testing"
)

// copyImpacts deep-copies an Impacts result so later mutations of the graph or
// the returned slices can be detected against the original content.
func copyImpacts(impacts []Impact) []Impact {
	out := make([]Impact, len(impacts))
	for i, im := range impacts {
		out[i] = Impact{im.Dataset, im.Distance, append([]string(nil), im.Path...)}
	}
	return out
}

// copyUpstreams deep-copies an Upstreams result so later mutations of the
// graph or the returned slices can be detected against the original content.
func copyUpstreams(upstreams []Upstream) []Upstream {
	out := make([]Upstream, len(upstreams))
	for i, up := range upstreams {
		out[i] = Upstream{up.Dataset, up.Distance, append([]string(nil), up.Path...)}
	}
	return out
}

// buildMergedBranchGraph builds the two-branch merge lineage used by the
// rename regressions: source feeds a and b; a feeds z; b feeds c; report
// depends on z and c together (c declared ahead of z on purpose); view depends
// on report; isolated is unrelated. The renamed dataset a sits inside one
// branch, two hops above the merge node report, and near the merge point the
// direct upstreams order as c < z — opposite to the hop-1 order a < b that the
// full-path rule decides on.
func buildMergedBranchGraph(t *testing.T) map[string]*Lineage {
	t.Helper()
	return buildRegisteredGraph(t, [][]string{
		{"source"},
		{"b", "source"}, // b-side branch registered first
		{"a", "source"},
		{"c", "b"},
		{"z", "a"},
		{"report", "c", "z"}, // c listed ahead of z on purpose
		{"view", "report"},
		{"isolated"},
	})
}

// Regression for a rename inside one of two equal-length branches that merge
// downstream. Before the rename, report's shortest explanation from source is
// [source a z report]: the candidates [source a z report] and
// [source b c report] have equal length and are compared name by name from the
// origin, so a < b at hop 1 selects the a/z branch even though the merge
// node's direct upstreams order as c < z. Renaming a to m — a name that sorts
// past b in Go string order — changes no edge, distance or reachable set, but
// the same full-path comparison now selects [source b c report], and view
// inherits that route one hop further on. A rule comparing only the last hop
// before the merge (c < z) could never observe this flip, so the chosen routes
// pin the from-the-start comparison. The upstream query reflects the same flip
// with paths still written along the actual derivation direction, and results
// fetched before the rename keep their original names, distances and paths.
func TestRenameInsideMergedBranchFlipsExplanationRoute(t *testing.T) {
	graph := buildMergedBranchGraph(t)
	assertConsistent(t, graph)

	wantImpactsBefore := []Impact{
		{Dataset: "a", Distance: 1, Path: []string{"source", "a"}},
		{Dataset: "b", Distance: 1, Path: []string{"source", "b"}},
		{Dataset: "c", Distance: 2, Path: []string{"source", "b", "c"}},
		{Dataset: "z", Distance: 2, Path: []string{"source", "a", "z"}},
		{Dataset: "report", Distance: 3, Path: []string{"source", "a", "z", "report"}},
		{Dataset: "view", Distance: 4, Path: []string{"source", "a", "z", "report", "view"}},
	}
	impactsBefore := mustImpacts(t, graph, "source")
	if !reflect.DeepEqual(impactsBefore, wantImpactsBefore) {
		t.Fatalf("impacts before rename = %v, want %v", impactsBefore, wantImpactsBefore)
	}
	// Anchor: the a/z branch explains the merge because a < b at hop 1, not
	// because of the merge node's direct upstream order (c < z).
	assertImpactOnce(t, impactsBefore, "report", 3, []string{"source", "a", "z", "report"})
	assertImpactOnce(t, impactsBefore, "view", 4, []string{"source", "a", "z", "report", "view"})
	impactsBeforeCopy := copyImpacts(impactsBefore)

	wantReportUpstreamsBefore := []Upstream{
		{Dataset: "c", Distance: 1, Path: []string{"c", "report"}},
		{Dataset: "z", Distance: 1, Path: []string{"z", "report"}},
		{Dataset: "a", Distance: 2, Path: []string{"a", "z", "report"}},
		{Dataset: "b", Distance: 2, Path: []string{"b", "c", "report"}},
		{Dataset: "source", Distance: 3, Path: []string{"source", "a", "z", "report"}},
	}
	reportUpstreamsBefore := mustUpstreams(t, graph, "report")
	if !reflect.DeepEqual(reportUpstreamsBefore, wantReportUpstreamsBefore) {
		t.Fatalf("upstreams before rename = %v, want %v", reportUpstreamsBefore, wantReportUpstreamsBefore)
	}
	// The shared source arrives over both branches but is listed exactly once.
	assertUpstreamOnce(t, reportUpstreamsBefore, "source", 3, []string{"source", "a", "z", "report"})
	reportUpstreamsBeforeCopy := copyUpstreams(reportUpstreamsBefore)

	viewUpstreamsBefore := mustUpstreams(t, graph, "view")
	assertUpstreamOnce(t, viewUpstreamsBefore, "source", 4, []string{"source", "a", "z", "report", "view"})
	viewUpstreamsBeforeCopy := copyUpstreams(viewUpstreamsBefore)

	// Rename the dataset inside the a/z branch: m sorts past b, so the other
	// branch's hop-1 name now wins the full-path comparison. No edge changes.
	mustRename(t, graph, "a", "m")
	assertConsistent(t, graph)

	// The graph changed only by the in-place name swap: m sits in a's old slot
	// in source's child list and in z's parent list; every other name keeps
	// its relative position, and the unrelated branch is untouched.
	if _, ok := graph["a"]; ok {
		t.Error("old name a still present in graph")
	}
	if got, want := len(graph), 8; got != want {
		t.Fatalf("dataset count = %d, want %d", got, want)
	}
	assertEntry(t, graph, "source", nil, []string{"b", "m"})
	assertEntry(t, graph, "m", []string{"source"}, []string{"z"})
	assertEntry(t, graph, "z", []string{"m"}, []string{"report"})
	assertEntry(t, graph, "b", []string{"source"}, []string{"c"})
	assertEntry(t, graph, "c", []string{"b"}, []string{"report"})
	assertEntry(t, graph, "report", []string{"c", "z"}, []string{"view"})
	assertEntry(t, graph, "view", []string{"report"}, nil)
	assertEntry(t, graph, "isolated", nil, nil)

	// Downstream query: the reachable set and every distance are unchanged,
	// but the explanation of the merge node (and of view behind it) now runs
	// through the b/c branch, and same-distance records sort by current name.
	wantImpactsAfter := []Impact{
		{Dataset: "b", Distance: 1, Path: []string{"source", "b"}},
		{Dataset: "m", Distance: 1, Path: []string{"source", "m"}},
		{Dataset: "c", Distance: 2, Path: []string{"source", "b", "c"}},
		{Dataset: "z", Distance: 2, Path: []string{"source", "m", "z"}},
		{Dataset: "report", Distance: 3, Path: []string{"source", "b", "c", "report"}},
		{Dataset: "view", Distance: 4, Path: []string{"source", "b", "c", "report", "view"}},
	}
	impactsAfter := mustImpacts(t, graph, "source")
	if !reflect.DeepEqual(impactsAfter, wantImpactsAfter) {
		t.Fatalf("impacts after rename = %v, want %v", impactsAfter, wantImpactsAfter)
	}
	assertImpactOnce(t, impactsAfter, "report", 3, []string{"source", "b", "c", "report"})
	assertImpactOnce(t, impactsAfter, "view", 4, []string{"source", "b", "c", "report", "view"})
	// The old name appears nowhere in the new results; the renamed node is
	// listed under its new name at its old distance.
	for _, im := range impactsAfter {
		if im.Dataset == "a" || slices.Contains(im.Path, "a") {
			t.Fatalf("old name a leaked into post-rename impact %+v", im)
		}
	}
	assertImpactOnce(t, impactsAfter, "m", 1, []string{"source", "m"})
	assertImpactOnce(t, impactsAfter, "z", 2, []string{"source", "m", "z"})

	// Upstream query: same flip, paths still written from each source toward
	// the target along the actual derivation direction.
	wantReportUpstreamsAfter := []Upstream{
		{Dataset: "c", Distance: 1, Path: []string{"c", "report"}},
		{Dataset: "z", Distance: 1, Path: []string{"z", "report"}},
		{Dataset: "b", Distance: 2, Path: []string{"b", "c", "report"}},
		{Dataset: "m", Distance: 2, Path: []string{"m", "z", "report"}},
		{Dataset: "source", Distance: 3, Path: []string{"source", "b", "c", "report"}},
	}
	reportUpstreamsAfter := mustUpstreams(t, graph, "report")
	if !reflect.DeepEqual(reportUpstreamsAfter, wantReportUpstreamsAfter) {
		t.Fatalf("upstreams after rename = %v, want %v", reportUpstreamsAfter, wantReportUpstreamsAfter)
	}
	assertUpstreamOnce(t, reportUpstreamsAfter, "source", 3, []string{"source", "b", "c", "report"})
	for _, up := range reportUpstreamsAfter {
		if up.Dataset == "a" || slices.Contains(up.Path, "a") {
			t.Fatalf("old name a leaked into post-rename upstream %+v", up)
		}
	}

	viewUpstreamsAfter := mustUpstreams(t, graph, "view")
	assertUpstreamOnce(t, viewUpstreamsAfter, "source", 4, []string{"source", "b", "c", "report", "view"})
	assertUpstreamOnce(t, viewUpstreamsAfter, "m", 3, []string{"m", "z", "report", "view"})

	// The old name is unregistered for queries in both directions.
	if _, err := Impacts(graph, "a"); err == nil || !strings.Contains(err.Error(), "a") {
		t.Fatalf("Impacts(a) after rename: want not-found error naming a, got %v", err)
	}
	if _, err := Upstreams(graph, "a"); err == nil || !strings.Contains(err.Error(), "a") {
		t.Fatalf("Upstreams(a) after rename: want not-found error naming a, got %v", err)
	}

	// Results fetched before the rename kept their original names, distances
	// and paths; the rename and the later queries did not overwrite them.
	if !reflect.DeepEqual(impactsBefore, impactsBeforeCopy) {
		t.Fatalf("pre-rename impacts changed: before=%v snapshot=%v", impactsBefore, impactsBeforeCopy)
	}
	if !reflect.DeepEqual(reportUpstreamsBefore, reportUpstreamsBeforeCopy) {
		t.Fatalf("pre-rename upstreams changed: before=%v snapshot=%v", reportUpstreamsBefore, reportUpstreamsBeforeCopy)
	}
	if !reflect.DeepEqual(viewUpstreamsBefore, viewUpstreamsBeforeCopy) {
		t.Fatalf("pre-rename view upstreams changed: before=%v snapshot=%v", viewUpstreamsBefore, viewUpstreamsBeforeCopy)
	}
}

// Regression for the priority of edge count over name order under a rename.
// d is reachable from s over a short route (s -> short -> d, length 2) and a
// long route through the renamed branch (s -> aa -> bb -> d, length 3).
// Renaming aa to 0aa makes the long route lexicographically smaller than the
// short one at hop 1, but a longer path must never replace a shorter one:
// d stays at distance 2 explained via short, in both query directions.
func TestRenameCannotPromoteLongerRouteOverShorter(t *testing.T) {
	graph := buildRegisteredGraph(t, [][]string{
		{"s"},
		{"aa", "s"},
		{"bb", "aa"},
		{"short", "s"},
		{"d", "short", "bb"},
	})
	assertConsistent(t, graph)

	impactsBefore := mustImpacts(t, graph, "s")
	assertImpactOnce(t, impactsBefore, "d", 2, []string{"s", "short", "d"})
	upstreamsBefore := mustUpstreams(t, graph, "d")
	assertUpstreamOnce(t, upstreamsBefore, "s", 2, []string{"s", "short", "d"})

	// 0aa sorts ahead of short: the length-3 route is now lexicographically
	// smaller, and it must still lose to the shorter route.
	mustRename(t, graph, "aa", "0aa")
	assertConsistent(t, graph)
	assertEntry(t, graph, "0aa", []string{"s"}, []string{"bb"})
	assertEntry(t, graph, "bb", []string{"0aa"}, []string{"d"})
	assertEntry(t, graph, "d", []string{"short", "bb"}, nil)

	wantImpactsAfter := []Impact{
		{Dataset: "0aa", Distance: 1, Path: []string{"s", "0aa"}},
		{Dataset: "short", Distance: 1, Path: []string{"s", "short"}},
		{Dataset: "bb", Distance: 2, Path: []string{"s", "0aa", "bb"}},
		{Dataset: "d", Distance: 2, Path: []string{"s", "short", "d"}},
	}
	impactsAfter := mustImpacts(t, graph, "s")
	if !reflect.DeepEqual(impactsAfter, wantImpactsAfter) {
		t.Fatalf("impacts after rename = %v, want %v", impactsAfter, wantImpactsAfter)
	}
	// Anchor: distance 2 via short wins over the lexicographically smaller
	// length-3 route through 0aa; d appears exactly once.
	assertImpactOnce(t, impactsAfter, "d", 2, []string{"s", "short", "d"})

	wantUpstreamsAfter := []Upstream{
		{Dataset: "bb", Distance: 1, Path: []string{"bb", "d"}},
		{Dataset: "short", Distance: 1, Path: []string{"short", "d"}},
		{Dataset: "0aa", Distance: 2, Path: []string{"0aa", "bb", "d"}},
		{Dataset: "s", Distance: 2, Path: []string{"s", "short", "d"}},
	}
	upstreamsAfter := mustUpstreams(t, graph, "d")
	if !reflect.DeepEqual(upstreamsAfter, wantUpstreamsAfter) {
		t.Fatalf("upstreams after rename = %v, want %v", upstreamsAfter, wantUpstreamsAfter)
	}
	assertUpstreamOnce(t, upstreamsAfter, "s", 2, []string{"s", "short", "d"})
}

// Regression for the rejected rename: the target name is already registered
// to another dataset. The error names the taken name, and the graph — the
// renamed candidate's node, every dependency edge and every list order — is
// preserved exactly, so re-issued queries return the same scope, distances,
// explanation paths and result ordering as before the request.
func TestRenameRejectedNameTakenKeepsLineageAndQueries(t *testing.T) {
	graph := buildMergedBranchGraph(t)
	assertConsistent(t, graph)

	impactsBefore := mustImpacts(t, graph, "source")
	reportUpstreamsBefore := mustUpstreams(t, graph, "report")
	viewUpstreamsBefore := mustUpstreams(t, graph, "view")
	before := snapshot(graph)

	// c is already registered to the b-side branch node: the rename must be
	// refused with an error naming c.
	err := Rename(graph, "a", "c")
	if err == nil || !strings.Contains(err.Error(), "c") {
		t.Fatalf("want name-in-use error naming c, got %v", err)
	}

	// The graph is byte-for-byte what it was before the request.
	if !reflect.DeepEqual(snapshot(graph), before) {
		t.Fatalf("rejected rename changed graph: before=%v after=%v", before, snapshot(graph))
	}
	assertEntry(t, graph, "a", []string{"source"}, []string{"z"})
	assertEntry(t, graph, "source", nil, []string{"b", "a"})
	assertEntry(t, graph, "z", []string{"a"}, []string{"report"})
	assertEntry(t, graph, "c", []string{"b"}, []string{"report"})
	assertEntry(t, graph, "report", []string{"c", "z"}, []string{"view"})
	assertConsistent(t, graph)

	// Re-issued queries see exactly the pre-request lineage: same reachable
	// set, distances, explanation paths and result order, in both directions.
	if got := mustImpacts(t, graph, "source"); !reflect.DeepEqual(got, impactsBefore) {
		t.Fatalf("impacts after rejected rename = %v, want %v", got, impactsBefore)
	}
	assertImpactOnce(t, impactsBefore, "report", 3, []string{"source", "a", "z", "report"})
	if got := mustUpstreams(t, graph, "report"); !reflect.DeepEqual(got, reportUpstreamsBefore) {
		t.Fatalf("report upstreams after rejected rename = %v, want %v", got, reportUpstreamsBefore)
	}
	assertUpstreamOnce(t, reportUpstreamsBefore, "source", 3, []string{"source", "a", "z", "report"})
	if got := mustUpstreams(t, graph, "view"); !reflect.DeepEqual(got, viewUpstreamsBefore) {
		t.Fatalf("view upstreams after rejected rename = %v, want %v", got, viewUpstreamsBefore)
	}

	// A second rejection against a different taken name (the merge node
	// itself) is equally atomic.
	err = Rename(graph, "a", "report")
	if err == nil || !strings.Contains(err.Error(), "report") {
		t.Fatalf("want name-in-use error naming report, got %v", err)
	}
	if !reflect.DeepEqual(snapshot(graph), before) {
		t.Fatalf("second rejected rename changed graph: before=%v after=%v", before, snapshot(graph))
	}
	if got := mustImpacts(t, graph, "source"); !reflect.DeepEqual(got, impactsBefore) {
		t.Fatalf("impacts after second rejection = %v, want %v", got, impactsBefore)
	}
	assertConsistent(t, graph)
}
