package chainledger

import (
	"reflect"
	"slices"
	"strings"
	"testing"
)

// Regression suite for the interaction of Rename with the lineage queries.
//
// Rename adds and removes no edges, but dataset names participate in two query
// decisions: the lexicographic tie-break among equal-length shortest
// explanation paths (compared name by name from the path's start), and the
// same-distance ordering of result records. These tests pin the behaviour
// "relationships unchanged, yet the query explanation changes with the new
// name", in both query directions, and the atomicity of the taken-name
// rejection.
//
// The shared scenario is two equal-length branches from src that merge at
// report, with the renamed dataset an INTERIOR node more than one hop away
// from the merge (the branch-internal name near the merge sorts in the
// opposite direction, so a last-hop-only comparison would pick the wrong
// route), a continuation beyond the merge, an unrelated branch, and a direct
// src -> report shortcut that gives report a strictly shorter route:
//
//	src ──> a1 ──> za ──┐
//	 │                  ├──> report ──> view
//	 ├──> b1 ──> cb ────┘
//	 └──────────────────> report   (direct shortcut)
//	solo ──> solochild            (unrelated branch)
func renameLineageRegistrations() [][]string {
	return [][]string{
		{"src"},
		{"solo"},
		// Register the b-side branch first and put cb ahead of za in report's
		// declared upstream list, so neither registration order nor list order
		// explains the path choice.
		{"b1", "src"},
		{"cb", "b1"},
		{"a1", "src"},
		{"za", "a1"},
		{"solochild", "solo"},
		{"report", "cb", "za", "src"},
		{"view", "report"},
	}
}

func buildRenameLineageGraph(t *testing.T) map[string]*Lineage {
	t.Helper()
	graph := buildRegisteredGraph(t, renameLineageRegistrations())
	assertConsistent(t, graph)
	return graph
}

// wantedImpactsAfterBranchRename is the complete, order-exact Impacts(src)
// result once the a-side interior node a1 has been renamed to za1. Only the
// a-side explanation changes: same reachable set, same shortest distances,
// records re-sorted by their current names.
func wantedImpactsAfterBranchRename() []Impact {
	return []Impact{
		{Dataset: "b1", Distance: 1, Path: []string{"src", "b1"}},
		{Dataset: "report", Distance: 1, Path: []string{"src", "report"}},
		{Dataset: "za1", Distance: 1, Path: []string{"src", "za1"}},
		{Dataset: "cb", Distance: 2, Path: []string{"src", "b1", "cb"}},
		{Dataset: "view", Distance: 2, Path: []string{"src", "report", "view"}},
		{Dataset: "za", Distance: 2, Path: []string{"src", "za1", "za"}},
	}
}

// wantedUpstreamsAfterBranchRename is the complete, order-exact
// Upstreams(report) result once a1 has been renamed to za1. The shared src
// source is reached through two equal-length branches and must be explained
// along the b-side one.
func wantedUpstreamsAfterBranchRename() []Upstream {
	return []Upstream{
		{Dataset: "cb", Distance: 1, Path: []string{"cb", "report"}},
		{Dataset: "src", Distance: 1, Path: []string{"src", "report"}},
		{Dataset: "za", Distance: 1, Path: []string{"za", "report"}},
		{Dataset: "b1", Distance: 2, Path: []string{"b1", "cb", "report"}},
		{Dataset: "za1", Distance: 2, Path: []string{"za1", "za", "report"}},
	}
}

// Renaming an interior dataset on one of two equal-length branches that merge
// downstream flips the selected shortest explanation for the merge, in both
// query directions, even though no relationship changes and the renamed node
// is more than one hop away from the merge.
//
// Before the rename, both equal routes to report (without the shortcut) are
// [src a1 za report] and [src b1 cb report]; they first differ at hop 1 where
// a1 < b1, so the a-side wins even though cb < za at the merge. Renaming a1
// to za1 puts za1 past b1 in Go string order, so the b-side route must win,
// with the comparison still performed from the explanation's start. Paths
// stay written along the actual derivation direction, and each dataset
// reached by several routes appears exactly once.
func TestRenameInteriorBranchFlippersMergedPathTieBreak(t *testing.T) {
	graph := buildRenameLineageGraph(t)

	// Before the rename the a-side route explains the merge and its
	// continuation. This anchors both the from-the-start comparison (a1 < b1
	// decides at hop 1, not cb < za at the last hop) and the pre-rename state
	// the later queries must move away from.
	impactsBefore := mustImpacts(t, graph, "src")
	assertImpactOnce(t, impactsBefore, "report", 1, []string{"src", "report"})
	assertImpactOnce(t, impactsBefore, "view", 2, []string{"src", "report", "view"})
	assertImpact(t, impactsBefore, "a1", 1, []string{"src", "a1"})
	assertImpact(t, impactsBefore, "za", 2, []string{"src", "a1", "za"})

	upstreamsBefore := mustUpstreams(t, graph, "report")
	assertUpstreamOnce(t, upstreamsBefore, "src", 1, []string{"src", "report"})
	assertUpstream(t, upstreamsBefore, "a1", 2, []string{"a1", "za", "report"})

	// Temporarily remove the direct shortcut to expose the equal-length branch
	// decision, then restore it; both graphs must stay internally consistent.
	mustRegister(t, graph, "report", "cb", "za")
	assertConsistent(t, graph)
	equalImpacts := mustImpacts(t, graph, "src")
	assertImpactOnce(t, equalImpacts, "report", 3, []string{"src", "a1", "za", "report"})
	assertImpactOnce(t, equalImpacts, "view", 4, []string{"src", "a1", "za", "report", "view"})
	equalUpstreams := mustUpstreams(t, graph, "report")
	assertUpstreamOnce(t, equalUpstreams, "src", 3, []string{"src", "a1", "za", "report"})
	mustRegister(t, graph, "report", "cb", "za", "src")
	assertConsistent(t, graph)

	if err := Rename(graph, "a1", "za1"); err != nil {
		t.Fatalf("Rename(a1, za1): %v", err)
	}
	assertConsistent(t, graph)

	// The old name is gone and the new node kept its exact lists; the node
	// count is unchanged and the name was replaced in place in the one
	// neighbor list that referenced it.
	if _, ok := graph["a1"]; ok {
		t.Fatal("old name a1 still present after rename")
	}
	if got, want := len(graph), 9; got != want {
		t.Fatalf("dataset count = %d, want %d", got, want)
	}
	assertEntry(t, graph, "za1", []string{"src"}, []string{"za"})
	assertEntry(t, graph, "src", nil, []string{"b1", "za1", "report"})
	assertEntry(t, graph, "za", []string{"za1"}, []string{"report"})
	assertEntry(t, graph, "b1", []string{"src"}, []string{"cb"})
	assertEntry(t, graph, "cb", []string{"b1"}, []string{"report"})
	// report's declared upstream list keeps cb in its original slot, followed
	// by the renamed a-side reference, followed by the shortcut.
	assertEntry(t, graph, "report", []string{"cb", "za", "src"}, []string{"view"})
	// Unrelated branch untouched.
	assertEntry(t, graph, "solo", nil, []string{"solochild"})
	assertEntry(t, graph, "solochild", []string{"solo"}, nil)

	// Downstream: same reachable set and shortest distances, complete order
	// recomputed under the current names; report and view keep the strictly
	// shorter direct-route explanation (see the dedicated edge-count test).
	wantImpacts := wantedImpactsAfterBranchRename()
	if got := mustImpacts(t, graph, "src"); !reflect.DeepEqual(got, wantImpacts) {
		t.Fatalf("Impacts(src) after rename = %v, want %v", got, wantImpacts)
	}
	impacts := mustImpacts(t, graph, "src")
	assertImpactOnce(t, impacts, "za1", 1, []string{"src", "za1"})
	assertImpactOnce(t, impacts, "za", 2, []string{"src", "za1", "za"})
	assertImpactOnce(t, impacts, "report", 1, []string{"src", "report"})
	assertImpactOnce(t, impacts, "view", 2, []string{"src", "report", "view"})
	// Same-distance records are ordered by current name, not by registration
	// order (a1 was registered after b1) or by the old name's slot (a1 < b1).
	if got, want := impactNames(impacts), []string{
		"b1", "report", "za1", "cb", "view", "za",
	}; !reflect.DeepEqual(got, want) {
		t.Fatalf("impact order = %v, want %v", got, want)
	}

	// Upstream: src reaches report through both equal branches exactly once,
	// and the explanation now runs along the b-side because za1 crossed past
	// b1; paths are written from the source toward report.
	wantUpstreams := wantedUpstreamsAfterBranchRename()
	if got := mustUpstreams(t, graph, "report"); !reflect.DeepEqual(got, wantUpstreams) {
		t.Fatalf("Upstreams(report) after rename = %v, want %v", got, wantUpstreams)
	}
	upstreams := mustUpstreams(t, graph, "report")
	assertUpstreamOnce(t, upstreams, "src", 1, []string{"src", "report"})
	assertUpstreamOnce(t, upstreams, "za1", 2, []string{"za1", "za", "report"})
	assertUpstreamOnce(t, upstreams, "b1", 2, []string{"b1", "cb", "report"})
	if got, want := upstreamNames(upstreams), []string{
		"cb", "src", "za", "b1", "za1",
	}; !reflect.DeepEqual(got, want) {
		t.Fatalf("upstream order = %v, want %v", got, want)
	}

	// With the shortcut removed again, the equal-length branches alone decide:
	// the rename flips both explanations to the b-side, compared from the
	// explanation's start even though cb still sorts ahead of za at the merge.
	mustRegister(t, graph, "report", "cb", "za")
	assertConsistent(t, graph)
	assertEntry(t, graph, "report", []string{"cb", "za"}, []string{"view"})

	equalImpactsAfter := mustImpacts(t, graph, "src")
	wantEqualImpacts := []Impact{
		{Dataset: "b1", Distance: 1, Path: []string{"src", "b1"}},
		{Dataset: "za1", Distance: 1, Path: []string{"src", "za1"}},
		{Dataset: "cb", Distance: 2, Path: []string{"src", "b1", "cb"}},
		{Dataset: "za", Distance: 2, Path: []string{"src", "za1", "za"}},
		{Dataset: "report", Distance: 3, Path: []string{"src", "b1", "cb", "report"}},
		{Dataset: "view", Distance: 4, Path: []string{"src", "b1", "cb", "report", "view"}},
	}
	if !reflect.DeepEqual(equalImpactsAfter, wantEqualImpacts) {
		t.Fatalf("equal-branch Impacts(src) = %v, want %v", equalImpactsAfter, wantEqualImpacts)
	}
	assertImpactOnce(t, equalImpactsAfter, "report", 3, []string{"src", "b1", "cb", "report"})
	assertImpactOnce(t, equalImpactsAfter, "view", 4, []string{"src", "b1", "cb", "report", "view"})

	equalUpstreamsAfter := mustUpstreams(t, graph, "report")
	wantEqualUpstreams := []Upstream{
		{Dataset: "cb", Distance: 1, Path: []string{"cb", "report"}},
		{Dataset: "za", Distance: 1, Path: []string{"za", "report"}},
		{Dataset: "b1", Distance: 2, Path: []string{"b1", "cb", "report"}},
		{Dataset: "za1", Distance: 2, Path: []string{"za1", "za", "report"}},
		{Dataset: "src", Distance: 3, Path: []string{"src", "b1", "cb", "report"}},
	}
	if !reflect.DeepEqual(equalUpstreamsAfter, wantEqualUpstreams) {
		t.Fatalf("equal-branch Upstreams(report) = %v, want %v", equalUpstreamsAfter, wantEqualUpstreams)
	}
	assertUpstreamOnce(t, equalUpstreamsAfter, "src", 3, []string{"src", "b1", "cb", "report"})
}

// A route with fewer edges always wins: the name-order change caused by the
// rename must not let the longer equal-branch explanation replace the direct
// src -> report shortcut. The merge and its continuation keep their shortest
// distances and shortcut explanation before and after the rename, while the
// branch-internal records merely relabel, in both query directions.
func TestRenameShorterRouteBeatsNameOrderChange(t *testing.T) {
	graph := buildRenameLineageGraph(t)
	before := snapshot(graph)

	mustRename(t, graph, "a1", "za1")
	if !reflect.DeepEqual(snapshot(graph), renameExpectedSnapshot(before, "a1", "za1")) {
		t.Fatalf("rename changed more than the name in place: before=%v after=%v",
			before, snapshot(graph))
	}

	impacts := mustImpacts(t, graph, "src")
	// report stays distance 1 through the shortcut and never takes the
	// length-3 branch route made lexicographically relevant by the rename.
	assertImpactOnce(t, impacts, "report", 1, []string{"src", "report"})
	assertImpactOnce(t, impacts, "view", 2, []string{"src", "report", "view"})
	// The renamed branch is still reached at its own shortest distances.
	assertImpactOnce(t, impacts, "za1", 1, []string{"src", "za1"})
	assertImpactOnce(t, impacts, "za", 2, []string{"src", "za1", "za"})
	// The other equal-length branch is unaffected in name, distance or path.
	assertImpactOnce(t, impacts, "b1", 1, []string{"src", "b1"})
	assertImpactOnce(t, impacts, "cb", 2, []string{"src", "b1", "cb"})

	upstreams := mustUpstreams(t, graph, "report")
	// src is a direct source via the shortcut, not a distance-3 source via
	// either branch, regardless of the renamed branch's ordering.
	assertUpstreamOnce(t, upstreams, "src", 1, []string{"src", "report"})
	assertUpstreamOnce(t, upstreams, "za1", 2, []string{"za1", "za", "report"})
	assertUpstreamOnce(t, upstreams, "b1", 2, []string{"b1", "cb", "report"})
}

// renameExpectedSnapshot mirrors the graph snapshot after an in-place rename:
// the node moves keys with its lists unchanged and every neighbor reference to
// oldName is replaced by newName, with no other change. It lets the test prove
// that Rename only substitutes the name (edges, list lengths and positions and
// unrelated branches all preserved) without coupling to internal helpers.
func renameExpectedSnapshot(before snap, oldName, newName string) snap {
	rewrite := func(list []string) []string {
		out := append([]string(nil), list...)
		for i, v := range out {
			if v == oldName {
				out[i] = newName
			}
		}
		return out
	}
	after := snap{map[string][]string{}, map[string][]string{}}
	for name, parents := range before.parents {
		key := name
		if name == oldName {
			key = newName
		}
		after.parents[key] = rewrite(parents)
		after.children[key] = rewrite(before.children[name])
	}
	return after
}

// Results fetched before a successful rename keep the old name, distances and
// paths they were obtained with; queries after the rename use the current
// name; the two batches never overwrite each other, and mutating one batch
// reaches neither the graph nor the other batch.
func TestRenameOldResultsStableNewQueriesUseCurrentName(t *testing.T) {
	graph := buildRenameLineageGraph(t)

	downBefore := mustImpacts(t, graph, "src")
	upBefore := mustUpstreams(t, graph, "report")
	downBeforeCopy := make([]Impact, len(downBefore))
	for i, im := range downBefore {
		downBeforeCopy[i] = Impact{im.Dataset, im.Distance, append([]string(nil), im.Path...)}
	}
	upBeforeCopy := make([]Upstream, len(upBefore))
	for i, up := range upBefore {
		upBeforeCopy[i] = Upstream{up.Dataset, up.Distance, append([]string(nil), up.Path...)}
	}

	mustRename(t, graph, "a1", "za1")
	assertConsistent(t, graph)

	// The pre-rename batches keep the old name and old a-side explanation
	// verbatim, including the old name's original same-distance position.
	if !reflect.DeepEqual(downBefore, downBeforeCopy) {
		t.Fatalf("pre-rename impacts changed after rename/new query: got %v, want %v",
			downBefore, downBeforeCopy)
	}
	if !reflect.DeepEqual(upBefore, upBeforeCopy) {
		t.Fatalf("pre-rename upstreams changed after rename/new query: got %v, want %v",
			upBefore, upBeforeCopy)
	}
	assertImpact(t, downBefore, "a1", 1, []string{"src", "a1"})
	assertImpact(t, downBefore, "za", 2, []string{"src", "a1", "za"})
	assertUpstream(t, upBefore, "a1", 2, []string{"a1", "za", "report"})

	// Fresh queries use the current name and current-name ordering.
	downAfter := mustImpacts(t, graph, "src")
	upAfter := mustUpstreams(t, graph, "report")
	if !reflect.DeepEqual(downAfter, wantedImpactsAfterBranchRename()) {
		t.Fatalf("fresh impacts = %v, want %v", downAfter, wantedImpactsAfterBranchRename())
	}
	if !reflect.DeepEqual(upAfter, wantedUpstreamsAfterBranchRename()) {
		t.Fatalf("fresh upstreams = %v, want %v", upAfter, wantedUpstreamsAfterBranchRename())
	}
	if names := impactNames(downAfter); slices.Contains(names, "a1") {
		t.Fatalf("old name a1 leaked into fresh impacts: %v", names)
	}
	if names := upstreamNames(upAfter); slices.Contains(names, "a1") {
		t.Fatalf("old name a1 leaked into fresh upstreams: %v", names)
	}

	// Mutating the old batch cannot change the new batch, and mutating the new
	// batch cannot change the old one; the graph stays on the current names.
	graphBefore := snapshot(graph)
	downBefore[0].Path[0] = "stale-label"
	downBefore[0].Dataset = "stale"
	upRecord := upstreamRecord(t, upAfter, "za1")
	upRecord.Path[0] = "current-label"
	if got := mustImpacts(t, graph, "src"); !reflect.DeepEqual(got, wantedImpactsAfterBranchRename()) {
		t.Fatalf("batch edits reached a later downstream query or the graph: got %v", got)
	}
	if got := mustUpstreams(t, graph, "report"); !reflect.DeepEqual(got, wantedUpstreamsAfterBranchRename()) {
		t.Fatalf("batch edits reached a later upstream query or the graph: got %v", got)
	}
	if !reflect.DeepEqual(snapshot(graph), graphBefore) {
		t.Fatalf("editing returned batches changed the graph: before=%v after=%v",
			graphBefore, snapshot(graph))
	}
	// The edit to the old batch stays on that record only, and the batch that
	// was never edited keeps its obtained content (old name included).
	if downBefore[0].Dataset != "stale" || downBefore[0].Path[0] != "stale-label" {
		t.Fatalf("edit to old batch lost: got %v", downBefore[0])
	}
	assertUpstream(t, upBefore, "a1", 2, []string{"a1", "za", "report"})
}

// A rename whose target name is already taken by another dataset is rejected
// with an error naming that target, and the refusal is atomic: the original
// node, every dependency and every list order survive, and requerying returns
// exactly the pre-request reachable set, distances, paths and ordering.
func TestRenameRejectedTargetTakenLeavesLineageUntouched(t *testing.T) {
	graph := buildRenameLineageGraph(t)

	impactsBefore := mustImpacts(t, graph, "src")
	upstreamsBefore := mustUpstreams(t, graph, "report")
	impactsCopy := make([]Impact, len(impactsBefore))
	for i, im := range impactsBefore {
		impactsCopy[i] = Impact{im.Dataset, im.Distance, append([]string(nil), im.Path...)}
	}
	upstreamsCopy := make([]Upstream, len(upstreamsBefore))
	for i, up := range upstreamsBefore {
		upstreamsCopy[i] = Upstream{up.Dataset, up.Distance, append([]string(nil), up.Path...)}
	}
	before := snapshot(graph)

	// "za" is a different, registered dataset on the a-side branch.
	err := Rename(graph, "a1", "za")
	if err == nil {
		t.Fatal("rename to an occupied name must be rejected, got nil")
	}
	if !strings.Contains(err.Error(), "already in use") || !strings.Contains(err.Error(), "za") {
		t.Fatalf("error %q must state the name is already in use and name za", err.Error())
	}

	// Atomic: nodes, edges and list orders are byte-for-byte the pre-request
	// ones.
	if !reflect.DeepEqual(snapshot(graph), before) {
		t.Fatalf("rejected rename changed graph: before=%v after=%v", before, snapshot(graph))
	}
	if _, ok := graph["a1"]; !ok {
		t.Fatal("original node a1 missing after rejected rename")
	}
	if got, want := len(graph), 9; got != want {
		t.Fatalf("dataset count = %d, want %d", got, want)
	}
	assertEntry(t, graph, "a1", []string{"src"}, []string{"za"})
	assertEntry(t, graph, "za", []string{"a1"}, []string{"report"})
	assertEntry(t, graph, "src", nil, []string{"b1", "a1", "report"})
	assertEntry(t, graph, "report", []string{"cb", "za", "src"}, []string{"view"})
	assertEntry(t, graph, "b1", []string{"src"}, []string{"cb"})
	assertEntry(t, graph, "cb", []string{"b1"}, []string{"report"})
	assertEntry(t, graph, "view", []string{"report"}, nil)
	assertConsistent(t, graph)

	// Re-queries reproduce the pre-request scope, distances, paths and order
	// exactly; the rejected name change leaves no trace in the explanations.
	if got := mustImpacts(t, graph, "src"); !reflect.DeepEqual(got, impactsCopy) {
		t.Fatalf("Impacts(src) after rejected rename = %v, want %v", got, impactsCopy)
	}
	if got := mustUpstreams(t, graph, "report"); !reflect.DeepEqual(got, upstreamsCopy) {
		t.Fatalf("Upstreams(report) after rejected rename = %v, want %v", got, upstreamsCopy)
	}
	impactsAfter := mustImpacts(t, graph, "src")
	assertImpactOnce(t, impactsAfter, "a1", 1, []string{"src", "a1"})
	assertImpactOnce(t, impactsAfter, "report", 1, []string{"src", "report"})
	upstreamsAfter := mustUpstreams(t, graph, "report")
	assertUpstreamOnce(t, upstreamsAfter, "a1", 2, []string{"a1", "za", "report"})
	assertUpstreamOnce(t, upstreamsAfter, "src", 1, []string{"src", "report"})

	// The old name still works and the taken name continues to resolve to its
	// own node; a later valid rename is unaffected.
	if _, err := Impacts(graph, "a1"); err != nil {
		t.Fatalf("Impacts(a1) after rejection: %v", err)
	}
	if _, err := Upstreams(graph, "za"); err != nil {
		t.Fatalf("Upstreams(za) after rejection: %v", err)
	}
	mustRename(t, graph, "a1", "za1")
	if got := mustImpacts(t, graph, "src"); !reflect.DeepEqual(got, wantedImpactsAfterBranchRename()) {
		t.Fatalf("Impacts(src) after the later valid rename = %v, want %v",
			got, wantedImpactsAfterBranchRename())
	}
}

// Renaming an interior node on a branch leaves the unrelated branch and every
// non-renamed node's stored list positions exactly as they were, while the
// same-distance result ordering follows the current names rather than the
// registration order or the old name's slot.
func TestRenameListReplacementInPlaceAndUnrelatedBranchStable(t *testing.T) {
	graph := buildRenameLineageGraph(t)
	before := snapshot(graph)

	mustRename(t, graph, "a1", "za1")

	// Only src's child list carried the old name: za1 must occupy a1's exact
	// slot, between b1 and report, with the surrounding names unmoved.
	assertEntry(t, graph, "src", nil, []string{"b1", "za1", "report"})
	if got := before.children["src"]; !reflect.DeepEqual(got, []string{"b1", "a1", "report"}) {
		t.Fatalf("pre-rename src children = %v, want [b1 a1 report]", got)
	}
	// Lists that never mentioned a1 are unchanged in length and order.
	assertEntry(t, graph, "report", []string{"cb", "za", "src"}, []string{"view"})
	assertEntry(t, graph, "za", []string{"za1"}, []string{"report"})
	assertEntry(t, graph, "solo", nil, []string{"solochild"})
	assertEntry(t, graph, "solochild", []string{"solo"}, nil)
	assertConsistent(t, graph)

	// Same-distance ordering moves with the current name: at distance 1 from
	// src the order is b1, report, za1 (a1 used to sort ahead of b1; za1 sorts
	// behind report), never the registration order b1, a1, report.
	impacts := mustImpacts(t, graph, "src")
	d1 := []string{}
	for _, im := range impacts {
		if im.Distance == 1 {
			d1 = append(d1, im.Dataset)
		}
	}
	if got, want := d1, []string{"b1", "report", "za1"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("distance-1 order = %v, want %v", got, want)
	}
	// The unrelated branch stays invisible to src and internally unchanged.
	if names := impactNames(impacts); slices.Contains(names, "solo") || slices.Contains(names, "solochild") {
		t.Fatalf("unrelated branch leaked into Impacts(src): %v", names)
	}
	soloImpacts := mustImpacts(t, graph, "solo")
	assertImpactOnce(t, soloImpacts, "solochild", 1, []string{"solo", "solochild"})
}
