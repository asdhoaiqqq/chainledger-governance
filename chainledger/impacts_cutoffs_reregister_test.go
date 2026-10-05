package chainledger

import (
	"reflect"
	"slices"
	"strings"
	"testing"
)

// buildCutoffRewireGraph builds the lineage shared by the same-name
// re-registration cutoff regressions:
//
//	source -> a ---------> report -> view
//	  |                    ^
//	  v                    |
//	  b -> mid ------------+
//	       |
//	       +-> tail
//
// source derives a and b; b derives mid; mid has the additional downstream
// tail; report depends on a and mid together; view depends on report.
func buildCutoffRewireGraph(t *testing.T) map[string]*Lineage {
	t.Helper()
	return buildRegisteredGraph(t, [][]string{
		{"source"},
		{"a", "source"},
		{"b", "source"},
		{"mid", "b"},
		{"tail", "mid"},
		{"report", "a", "mid"},
		{"view", "report"},
	})
}

// Regression for the cutoff query after a same-name Register replaces the
// dataset's direct upstreams wholesale. With a as the cutoff, report initially
// survives around it through b and mid. Re-registering report with direct
// upstreams a and tail severs the mid -> report edge while keeping a -> report
// and report -> view. The cutoff query must then be recomputed over the current
// lineage: report and view stay reachable but only via b -> mid -> tail, at
// distances 4 and 5, and the dissolved mid -> report relationship must not
// survive in any explanation. The unrestricted query still reaches report
// directly through a at distance 2; that shorter route must never be offered
// as the cutoff query's explanation.
func TestImpactsWithCutoffsReregisterReroutesViaOtherBranch(t *testing.T) {
	graph := buildCutoffRewireGraph(t)
	assertConsistent(t, graph)

	// Baseline unrestricted query: report is distance 2 through a.
	full := mustImpacts(t, graph, "source")
	assertImpactOnce(t, full, "report", 2, []string{"source", "a", "report"})
	assertImpactOnce(t, full, "view", 3, []string{"source", "a", "report", "view"})

	// Cutoff a before any re-registration: a stays in the result at distance 1,
	// but report must detour through b and mid.
	wantInitial := []Impact{
		{Dataset: "a", Distance: 1, Path: []string{"source", "a"}},
		{Dataset: "b", Distance: 1, Path: []string{"source", "b"}},
		{Dataset: "mid", Distance: 2, Path: []string{"source", "b", "mid"}},
		{Dataset: "report", Distance: 3, Path: []string{"source", "b", "mid", "report"}},
		{Dataset: "tail", Distance: 3, Path: []string{"source", "b", "mid", "tail"}},
		{Dataset: "view", Distance: 4, Path: []string{"source", "b", "mid", "report", "view"}},
	}
	initial := mustImpactsWithCutoffs(t, graph, "source", "a")
	if !reflect.DeepEqual(initial, wantInitial) {
		t.Fatalf("initial cutoff query = %v, want %v", initial, wantInitial)
	}
	// The cutoff itself is present; change simply must not travel past it.
	assertImpactOnce(t, initial, "a", 1, []string{"source", "a"})
	assertImpactOnce(t, initial, "report", 3, []string{"source", "b", "mid", "report"})
	assertImpactOnce(t, initial, "view", 4, []string{"source", "b", "mid", "report", "view"})
	initialSnapshot := copyImpacts(initial)

	// Same-name registration: replace report's direct upstreams as a whole with
	// a and tail. report keeps its own downstream view; the other branch's
	// internal b -> mid -> tail relationships are untouched; mid loses the
	// reverse edge to report and tail gains it, appended after its list.
	mustRegister(t, graph, "report", "a", "tail")
	assertConsistent(t, graph)
	assertEntry(t, graph, "report", []string{"a", "tail"}, []string{"view"})
	assertEntry(t, graph, "a", []string{"source"}, []string{"report"})
	assertEntry(t, graph, "mid", []string{"b"}, []string{"tail"})
	assertEntry(t, graph, "tail", []string{"mid"}, []string{"report"})
	assertEntry(t, graph, "b", []string{"source"}, []string{"mid"})
	assertEntry(t, graph, "view", []string{"report"}, nil)

	// The cutoff result after the replacement: report and view survive by
	// walking all the way around through tail, one edge farther each. The path
	// must not contain the dissolved mid -> report direct relationship.
	wantRerouted := []Impact{
		{Dataset: "a", Distance: 1, Path: []string{"source", "a"}},
		{Dataset: "b", Distance: 1, Path: []string{"source", "b"}},
		{Dataset: "mid", Distance: 2, Path: []string{"source", "b", "mid"}},
		{Dataset: "tail", Distance: 3, Path: []string{"source", "b", "mid", "tail"}},
		{Dataset: "report", Distance: 4, Path: []string{"source", "b", "mid", "tail", "report"}},
		{Dataset: "view", Distance: 5, Path: []string{"source", "b", "mid", "tail", "report", "view"}},
	}
	// Both library entry-point shapes (variadic and explicit cutoff slice) stay
	// compatible and agree.
	rerouted := mustImpactsWithCutoffs(t, graph, "source", "a")
	reroutedSlice, err := ImpactsWithCutoffs(graph, "source", []string{"a"})
	if err != nil {
		t.Fatalf("ImpactsWithCutoffs with explicit slice: %v", err)
	}
	if !reflect.DeepEqual(rerouted, reroutedSlice) {
		t.Fatalf("entry-point forms disagree: variadic=%v slice=%v", rerouted, reroutedSlice)
	}
	if !reflect.DeepEqual(rerouted, wantRerouted) {
		t.Fatalf("cutoff query after replacement = %v, want %v", rerouted, wantRerouted)
	}
	for _, im := range rerouted {
		if im.Path[0] != "source" || im.Path[len(im.Path)-1] != im.Dataset || len(im.Path) != im.Distance+1 {
			t.Fatalf("malformed record after replacement: %v", im)
		}
		if im.Dataset == "report" && slices.Contains(im.Path[:len(im.Path)-1], "a") {
			t.Fatalf("report explanation traverses cutoff a: %v", im.Path)
		}
	}
	assertImpactOnce(t, rerouted, "report", 4, []string{"source", "b", "mid", "tail", "report"})
	assertImpactOnce(t, rerouted, "view", 5, []string{"source", "b", "mid", "tail", "report", "view"})

	// The unrestricted query still takes the short route through a. Its shorter
	// distances and paths belong to the unrestricted query alone.
	wantFull := []Impact{
		{Dataset: "a", Distance: 1, Path: []string{"source", "a"}},
		{Dataset: "b", Distance: 1, Path: []string{"source", "b"}},
		{Dataset: "mid", Distance: 2, Path: []string{"source", "b", "mid"}},
		{Dataset: "report", Distance: 2, Path: []string{"source", "a", "report"}},
		{Dataset: "tail", Distance: 3, Path: []string{"source", "b", "mid", "tail"}},
		{Dataset: "view", Distance: 3, Path: []string{"source", "a", "report", "view"}},
	}
	afterFull := mustImpacts(t, graph, "source")
	if !reflect.DeepEqual(afterFull, wantFull) {
		t.Fatalf("unrestricted query after replacement = %v, want %v", afterFull, wantFull)
	}
	assertImpactOnce(t, afterFull, "report", 2, []string{"source", "a", "report"})
	// A nil cutoff list remains exactly the unrestricted query.
	if got, err := ImpactsWithCutoffs(graph, "source", nil); err != nil || !reflect.DeepEqual(got, afterFull) {
		t.Fatalf("nil cutoffs = %v, %v; want %v", got, err, afterFull)
	}

	// The result returned before the replacement keeps the distances and paths
	// it had at the time: queries return copies and the re-registration cannot
	// rewrite history.
	if !reflect.DeepEqual(initial, initialSnapshot) {
		t.Fatalf("earlier cutoff result changed after replacement: got %v, snapshot %v",
			initial, initialSnapshot)
	}
}

// Regression for the case where the replacement removes every bypass: after
// report is re-registered with a as its only direct upstream, a is the sole
// gateway from source to report and view. Cutting a must then drop report and
// view from the cutoff result together, while a and the still-reachable other
// branch (b, mid, tail) remain. Report and view are registered datasets, not
// missing ones: the query succeeds and must never surface a not-found error.
// The unrestricted query continues to find both through a, each exactly once.
func TestImpactsWithCutoffsReregisterOnlyCutoffUpstreamDropsSubtree(t *testing.T) {
	graph := buildCutoffRewireGraph(t)

	// Replace report's direct upstreams with a alone. mid drops its reverse
	// edge to report; tail never carried one; report keeps view.
	mustRegister(t, graph, "report", "a")
	assertConsistent(t, graph)
	assertEntry(t, graph, "report", []string{"a"}, []string{"view"})
	assertEntry(t, graph, "a", []string{"source"}, []string{"report"})
	assertEntry(t, graph, "mid", []string{"b"}, []string{"tail"})
	assertEntry(t, graph, "tail", []string{"mid"}, nil)
	assertEntry(t, graph, "view", []string{"report"}, nil)
	before := snapshot(graph)

	impacts, err := ImpactsWithCutoffs(graph, "source", []string{"a"})
	if err != nil {
		t.Fatalf("cutoff query with no remaining bypass must succeed, got %v", err)
	}
	want := []Impact{
		{Dataset: "a", Distance: 1, Path: []string{"source", "a"}},
		{Dataset: "b", Distance: 1, Path: []string{"source", "b"}},
		{Dataset: "mid", Distance: 2, Path: []string{"source", "b", "mid"}},
		{Dataset: "tail", Distance: 3, Path: []string{"source", "b", "mid", "tail"}},
	}
	if !reflect.DeepEqual(impacts, want) {
		t.Fatalf("cutoff query = %v, want %v (report and view must be gone)", impacts, want)
	}
	names := impactNames(impacts)
	for _, gone := range []string{"report", "view"} {
		if slices.Contains(names, gone) {
			t.Fatalf("%s lies only past cutoff a and must be gone, got %v", gone, names)
		}
	}
	// Every surviving dataset is listed exactly once and in the standard order.
	seen := map[string]int{}
	for _, im := range impacts {
		seen[im.Dataset]++
	}
	for name, n := range seen {
		if n != 1 {
			t.Fatalf("dataset %s listed %d times in %v", name, n, names)
		}
	}

	// report and view are still registered and directly queryable: the empty
	// scope past the cutoff is not a dataset-not-registered condition.
	fromReport := mustImpacts(t, graph, "report")
	assertImpactOnce(t, fromReport, "view", 1, []string{"report", "view"})

	// The unrestricted query still reaches report and view through a.
	full := mustImpacts(t, graph, "source")
	assertImpactOnce(t, full, "report", 2, []string{"source", "a", "report"})
	assertImpactOnce(t, full, "view", 3, []string{"source", "a", "report", "view"})
	if !reflect.DeepEqual(snapshot(graph), before) {
		t.Fatalf("queries changed graph: before=%v after=%v", before, snapshot(graph))
	}
}

// Regression for a rejected same-name replacement: under the initial lineage,
// giving report the upstreams tail and view would close a cycle, because view
// is already report's downstream. The error must name view as the dataset that
// closes the loop, and the request must not take effect partially — the old
// upstreams and downstream, list ordering, unrestricted query results and the
// cutoff query results all stay exactly as they were. Queries stay read-only,
// and results already returned before the rejected replacement keep their
// original distances and paths.
func TestImpactsWithCutoffsReregisterCycleRejectedLeavesLineageAndQuery(t *testing.T) {
	graph := buildCutoffRewireGraph(t)
	assertConsistent(t, graph)

	// Capture the graph and both query shapes before the failed replacement.
	before := snapshot(graph)
	cutoffBefore := mustImpactsWithCutoffs(t, graph, "source", "a")
	cutoffSnapshot := copyImpacts(cutoffBefore)
	fullBefore := mustImpacts(t, graph, "source")
	fullSnapshot := copyImpacts(fullBefore)

	wantCutoff := []Impact{
		{Dataset: "a", Distance: 1, Path: []string{"source", "a"}},
		{Dataset: "b", Distance: 1, Path: []string{"source", "b"}},
		{Dataset: "mid", Distance: 2, Path: []string{"source", "b", "mid"}},
		{Dataset: "report", Distance: 3, Path: []string{"source", "b", "mid", "report"}},
		{Dataset: "tail", Distance: 3, Path: []string{"source", "b", "mid", "tail"}},
		{Dataset: "view", Distance: 4, Path: []string{"source", "b", "mid", "report", "view"}},
	}
	if !reflect.DeepEqual(cutoffBefore, wantCutoff) {
		t.Fatalf("baseline cutoff query = %v, want %v", cutoffBefore, wantCutoff)
	}

	// tail is a benign upstream; view is report's own downstream, so the edge
	// report -> view closes report -> view -> report. The error must name view.
	err := Register(graph, Dataset{Name: "report"}, []string{"tail", "view"})
	if err == nil || !strings.Contains(err.Error(), "cycle") || !strings.Contains(err.Error(), "view") {
		t.Fatalf("want cycle error naming view, got %v", err)
	}

	// The same cyclic candidate listed first must be rejected with the same
	// named error regardless of input order.
	err = Register(graph, Dataset{Name: "report"}, []string{"view", "tail"})
	if err == nil || !strings.Contains(err.Error(), "cycle") || !strings.Contains(err.Error(), "view") {
		t.Fatalf("reversed order: want cycle error naming view, got %v", err)
	}

	// Nothing was applied, partially or otherwise: nodes, edges and stored list
	// order are byte-for-byte the pre-request graph.
	if !reflect.DeepEqual(snapshot(graph), before) {
		t.Fatalf("rejected replacement changed graph: before=%v after=%v", before, snapshot(graph))
	}
	assertEntry(t, graph, "report", []string{"a", "mid"}, []string{"view"})
	assertEntry(t, graph, "mid", []string{"b"}, []string{"tail", "report"})
	assertEntry(t, graph, "tail", []string{"mid"}, nil)
	assertEntry(t, graph, "a", []string{"source"}, []string{"report"})
	assertEntry(t, graph, "view", []string{"report"}, nil)
	assertConsistent(t, graph)

	// Both query shapes answer exactly as before the rejected replacement.
	cutoffAfter := mustImpactsWithCutoffs(t, graph, "source", "a")
	if !reflect.DeepEqual(cutoffAfter, wantCutoff) {
		t.Fatalf("cutoff query after rejection = %v, want %v", cutoffAfter, wantCutoff)
	}
	assertImpactOnce(t, cutoffAfter, "report", 3, []string{"source", "b", "mid", "report"})
	assertImpactOnce(t, cutoffAfter, "view", 4, []string{"source", "b", "mid", "report", "view"})
	fullAfter := mustImpacts(t, graph, "source")
	if !reflect.DeepEqual(fullAfter, fullSnapshot) {
		t.Fatalf("unrestricted query after rejection = %v, want %v", fullAfter, fullSnapshot)
	}

	// The pre-rejection results themselves retained the distances and paths
	// they were returned with, and the queries left no trace in the graph.
	if !reflect.DeepEqual(cutoffBefore, cutoffSnapshot) {
		t.Fatalf("earlier cutoff result changed: got %v, snapshot %v", cutoffBefore, cutoffSnapshot)
	}
	if !reflect.DeepEqual(fullBefore, fullSnapshot) {
		t.Fatalf("earlier full result changed: got %v, snapshot %v", fullBefore, fullSnapshot)
	}
	if !reflect.DeepEqual(snapshot(graph), before) {
		t.Fatalf("queries changed graph: before=%v after=%v", before, snapshot(graph))
	}
}
