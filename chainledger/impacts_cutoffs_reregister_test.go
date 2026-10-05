package chainledger

import (
	"reflect"
	"slices"
	"strings"
	"testing"
)

// buildCutoffReregisterGraph builds the shared scenario for the re-register
// regression tests: source derives a and b; b derives mid; mid has the further
// downstream tail; report depends on a and mid together; view depends on
// report. With a as the cutoff, report is reached around a via b and mid.
func buildCutoffReregisterGraph(t *testing.T) map[string]*Lineage {
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

// Baseline for the re-register scenario: with a as the cutoff, a itself is
// still listed at distance 1 but nothing propagates through it, so report is
// reached via the b/mid detour at distance 3 and view at distance 4, both
// explained by the route that avoided a. tail hangs off mid on the same
// branch.
func TestImpactsWithCutoffsReregisterBaseline(t *testing.T) {
	graph := buildCutoffReregisterGraph(t)
	assertConsistent(t, graph)

	impacts := mustImpactsWithCutoffs(t, graph, "source", "a")
	want := []Impact{
		{Dataset: "a", Distance: 1, Path: []string{"source", "a"}},
		{Dataset: "b", Distance: 1, Path: []string{"source", "b"}},
		{Dataset: "mid", Distance: 2, Path: []string{"source", "b", "mid"}},
		{Dataset: "report", Distance: 3, Path: []string{"source", "b", "mid", "report"}},
		{Dataset: "tail", Distance: 3, Path: []string{"source", "b", "mid", "tail"}},
		{Dataset: "view", Distance: 4, Path: []string{"source", "b", "mid", "report", "view"}},
	}
	if !reflect.DeepEqual(impacts, want) {
		t.Fatalf("cutoff a: impacts = %v, want %v", impacts, want)
	}
	// The cutoff itself stays in the result; the detour explains report/view.
	assertImpactOnce(t, impacts, "a", 1, []string{"source", "a"})
	assertImpactOnce(t, impacts, "report", 3, []string{"source", "b", "mid", "report"})
	assertImpactOnce(t, impacts, "view", 4, []string{"source", "b", "mid", "report", "view"})
}

// Re-registering report with the same name, replacing its direct upstreams
// [a mid] by [a tail], must make the cutoff query recompute over the current
// lineage: report and view stay in the result but move to distances 4 and 5,
// explained through b, mid and tail — the detached mid -> report edge must not
// be used. The ordinary downstream query still reaches report through a at
// distance 2, and that shorter route must not leak into the cutoff query's
// explanation. Results returned before the replacement keep their content.
func TestImpactsWithCutoffsAfterReregisterNewBypass(t *testing.T) {
	graph := buildCutoffReregisterGraph(t)

	// Results obtained before the replacement must survive it untouched.
	before := mustImpactsWithCutoffs(t, graph, "source", "a")
	beforeCopy := copyImpacts(before)

	mustRegister(t, graph, "report", "a", "tail")
	assertConsistent(t, graph)
	// The replacement swapped exactly one direct upstream: mid lost the
	// report edge, tail gained it; report's own downstream view is kept.
	assertEntry(t, graph, "report", []string{"a", "tail"}, []string{"view"})
	assertEntry(t, graph, "mid", []string{"b"}, []string{"tail"})
	assertEntry(t, graph, "tail", []string{"mid"}, []string{"report"})
	assertEntry(t, graph, "view", []string{"report"}, nil)

	// The ordinary query still uses the short route through a.
	full := mustImpacts(t, graph, "source")
	assertImpactOnce(t, full, "report", 2, []string{"source", "a", "report"})
	assertImpactOnce(t, full, "view", 3, []string{"source", "a", "report", "view"})

	graphBeforeQuery := snapshot(graph)
	impacts := mustImpactsWithCutoffs(t, graph, "source", "a")
	want := []Impact{
		{Dataset: "a", Distance: 1, Path: []string{"source", "a"}},
		{Dataset: "b", Distance: 1, Path: []string{"source", "b"}},
		{Dataset: "mid", Distance: 2, Path: []string{"source", "b", "mid"}},
		{Dataset: "tail", Distance: 3, Path: []string{"source", "b", "mid", "tail"}},
		{Dataset: "report", Distance: 4, Path: []string{"source", "b", "mid", "tail", "report"}},
		{Dataset: "view", Distance: 5, Path: []string{"source", "b", "mid", "tail", "report", "view"}},
	}
	if !reflect.DeepEqual(impacts, want) {
		t.Fatalf("cutoff a after re-register: impacts = %v, want %v", impacts, want)
	}
	// Anchors: report/view survive exactly once at the new distances, explained
	// by the b/mid/tail detour — never by the removed mid -> report edge, and
	// never by the ordinary query's shorter source -> a -> report route.
	assertImpactOnce(t, impacts, "report", 4, []string{"source", "b", "mid", "tail", "report"})
	assertImpactOnce(t, impacts, "view", 5, []string{"source", "b", "mid", "tail", "report", "view"})
	for _, im := range impacts {
		if slices.Contains(im.Path, "report") && im.Dataset != "report" && im.Dataset != "view" {
			t.Fatalf("report leaked into the explanation of %s: %v", im.Dataset, im.Path)
		}
		if im.Dataset == "report" || im.Dataset == "view" {
			if slices.Contains(im.Path[:len(im.Path)-1], "a") {
				t.Fatalf("cutoff explanation for %s reuses the truncated a route: %v", im.Dataset, im.Path)
			}
		}
	}

	// The cutoff query is read-only under the new lineage.
	if !reflect.DeepEqual(snapshot(graph), graphBeforeQuery) {
		t.Fatalf("query changed graph: before=%v after=%v", graphBeforeQuery, snapshot(graph))
	}
	// The pre-replacement result kept its original distances and paths.
	if !reflect.DeepEqual(before, beforeCopy) {
		t.Fatalf("earlier cutoff result changed after re-register: before=%v snapshot=%v", before, beforeCopy)
	}
}

// Re-registering report with a as its ONLY upstream removes every route that
// avoids the cutoff: report and view leave the cutoff result together, while
// the cutoff a itself and the still-reachable b/mid/tail branch remain. This
// is a successful query with no remaining detour, not an unregistered-dataset
// error, and the ordinary downstream query still finds report and view.
func TestImpactsWithCutoffsAfterReregisterNoRemainingBypass(t *testing.T) {
	graph := buildCutoffReregisterGraph(t)

	mustRegister(t, graph, "report", "a")
	assertConsistent(t, graph)
	assertEntry(t, graph, "report", []string{"a"}, []string{"view"})
	assertEntry(t, graph, "mid", []string{"b"}, []string{"tail"})

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
		t.Fatalf("cutoff a after dropping mid upstream: impacts = %v, want %v", impacts, want)
	}
	// report and view are gone from the cutoff result; everything left appears
	// exactly once, ordered by distance then name.
	names := impactNames(impacts)
	for _, gone := range []string{"report", "view"} {
		if slices.Contains(names, gone) {
			t.Fatalf("%s has no cutoff-free route and must be gone, got %v", gone, names)
		}
	}
	if got, wantNames := names, []string{"a", "b", "mid", "tail"}; !reflect.DeepEqual(got, wantNames) {
		t.Fatalf("order = %v, want %v", got, wantNames)
	}

	// The ordinary query is unaffected by the cutoff: report and view are
	// still downstream of source through a.
	full := mustImpacts(t, graph, "source")
	assertImpactOnce(t, full, "report", 2, []string{"source", "a", "report"})
	assertImpactOnce(t, full, "view", 3, []string{"source", "a", "report", "view"})
}

// A rejected replacement must not disturb anything: pointing report at tail
// and view closes a cycle through view (view is already report's downstream),
// so the error names view, the whole request fails atomically, and the
// original edges, list orders and cutoff query results are exactly as before.
func TestImpactsWithCutoffsRejectedReregisterLeavesEverything(t *testing.T) {
	graph := buildCutoffReregisterGraph(t)
	before := snapshot(graph)
	cutoffBefore := mustImpactsWithCutoffs(t, graph, "source", "a")
	fullBefore := mustImpacts(t, graph, "source")

	err := Register(graph, Dataset{Name: "report"}, []string{"tail", "view"})
	if err == nil || !strings.Contains(err.Error(), "cycle") || !strings.Contains(err.Error(), "view") {
		t.Fatalf("want cycle error naming view, got %v", err)
	}

	// No partial effect: every node, edge and list order is untouched.
	if !reflect.DeepEqual(snapshot(graph), before) {
		t.Fatalf("rejected re-register changed graph: before=%v after=%v", before, snapshot(graph))
	}
	assertEntry(t, graph, "report", []string{"a", "mid"}, []string{"view"})
	assertEntry(t, graph, "mid", []string{"b"}, []string{"tail", "report"})
	assertEntry(t, graph, "tail", []string{"mid"}, nil)
	assertEntry(t, graph, "view", []string{"report"}, nil)
	assertConsistent(t, graph)

	// Both queries answer exactly as they did before the rejected request.
	if got := mustImpactsWithCutoffs(t, graph, "source", "a"); !reflect.DeepEqual(got, cutoffBefore) {
		t.Fatalf("cutoff result changed after rejected re-register: got %v, want %v", got, cutoffBefore)
	}
	if got := mustImpacts(t, graph, "source"); !reflect.DeepEqual(got, fullBefore) {
		t.Fatalf("full impacts changed after rejected re-register: got %v, want %v", got, fullBefore)
	}
	assertImpactOnce(t, cutoffBefore, "report", 3, []string{"source", "b", "mid", "report"})
	assertImpactOnce(t, cutoffBefore, "view", 4, []string{"source", "b", "mid", "report", "view"})
}
