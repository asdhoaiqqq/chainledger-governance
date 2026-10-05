package chainledger

import (
	"reflect"
	"slices"
	"strings"
	"testing"
)

// buildCutoffUnregisterGraph builds the lineage shared by the
// removal-plus-cutoff regressions:
//
//	source -> a ---------> report
//	  |                    ^
//	  v                    |
//	  b -> mid ------------+
//	       |
//	       +-> view
//
// source derives a and b; a derives report directly (the short route); b
// derives mid; mid derives both report (the long route around a) and view;
// report and view are leaves with no downstreams. report is the multi-parent
// leaf the scenario removes: it keeps two routes from source (a direct one of
// length 2 through a and a long one of length 3 through b and mid), while view
// is reached only on the b -> mid side at distance 3.
func buildCutoffUnregisterGraph(t *testing.T) map[string]*Lineage {
	t.Helper()
	return buildRegisteredGraph(t, [][]string{
		{"source"},
		{"a", "source"},
		{"b", "source"},
		{"mid", "b"},
		{"report", "a", "mid"},
		{"view", "mid"},
	})
}

// End-to-end regression for the combined workflow "query the downstream impact
// scope with propagation cutoffs, remove a leaf dataset, query again":
//
//  1. From source with a as the cutoff, a itself stays in the result at
//     distance 1, report is reached around the cutoff via source -> b -> mid ->
//     report at distance 3, and view via source -> b -> mid -> view at
//     distance 3. Every dataset appears once, ordered by distance then name.
//  2. The leaf report is then unregistered: the next same cutoff query drops
//     report but keeps view's distance and explanation path exactly as they
//     were. a and mid each lose report from their downstream lists (mid keeps
//     view and the remaining list order); every other node is untouched.
//  3. The cutoff result returned before the removal is an independent snapshot:
//     report's record and its bypass path stay readable with their old content.
func TestImpactsWithCutoffsReflectUnregisteredLeafAndKeepsOldResult(t *testing.T) {
	graph := buildCutoffUnregisterGraph(t)
	assertConsistent(t, graph)
	assertEntry(t, graph, "report", []string{"a", "mid"}, nil)
	assertEntry(t, graph, "a", []string{"source"}, []string{"report"})
	assertEntry(t, graph, "mid", []string{"b"}, []string{"report", "view"})
	assertEntry(t, graph, "view", []string{"mid"}, nil)

	// Step 1: cutoff query before the removal. a is a dead end for propagation,
	// but both leaves stay reachable around it through b and mid.
	wantBefore := []Impact{
		{Dataset: "a", Distance: 1, Path: []string{"source", "a"}},
		{Dataset: "b", Distance: 1, Path: []string{"source", "b"}},
		{Dataset: "mid", Distance: 2, Path: []string{"source", "b", "mid"}},
		{Dataset: "report", Distance: 3, Path: []string{"source", "b", "mid", "report"}},
		{Dataset: "view", Distance: 3, Path: []string{"source", "b", "mid", "view"}},
	}
	before := mustImpactsWithCutoffs(t, graph, "source", "a")
	if !reflect.DeepEqual(before, wantBefore) {
		t.Fatalf("initial cutoff query = %v, want %v", before, wantBefore)
	}
	// Distance-3 records are name-tied, so report sorts before view; each
	// dataset is listed exactly once, and report's explanation must detour
	// around a rather than reuse the truncated short route.
	assertImpactOnce(t, before, "a", 1, []string{"source", "a"})
	assertImpactOnce(t, before, "report", 3, []string{"source", "b", "mid", "report"})
	assertImpactOnce(t, before, "view", 3, []string{"source", "b", "mid", "view"})
	for _, im := range before {
		if im.Path[0] != "source" || im.Path[len(im.Path)-1] != im.Dataset || len(im.Path) != im.Distance+1 {
			t.Fatalf("malformed record: %v", im)
		}
		if im.Dataset != "a" && slices.Contains(im.Path[:len(im.Path)-1], "a") {
			t.Fatalf("path traverses cutoff a to reach %s: %v", im.Dataset, im.Path)
		}
	}
	beforeSnapshot := copyImpacts(before)

	// Step 2: report is a leaf (no direct downstream) despite its two upstreams,
	// so removal succeeds.
	mustUnregister(t, graph, "report")
	if _, ok := graph["report"]; ok {
		t.Fatal("report registration still present after Unregister")
	}
	if got, want := len(graph), 5; got != want {
		t.Fatalf("dataset count = %d, want %d; graph=%v", got, want, graph)
	}
	assertConsistent(t, graph)

	// Both direct upstreams lose report. a had report as its only downstream;
	// mid keeps view with its relationship and its position in the remaining
	// list. Nodes not directly connected to report are byte-for-byte untouched.
	assertEntry(t, graph, "a", []string{"source"}, nil)
	assertEntry(t, graph, "mid", []string{"b"}, []string{"view"})
	assertEntry(t, graph, "view", []string{"mid"}, nil)
	assertEntry(t, graph, "b", []string{"source"}, []string{"mid"})
	assertEntry(t, graph, "source", nil, []string{"a", "b"})

	// Step 3: the same origin and cutoff list now answer over the current
	// registration and lineage. report is gone, view keeps its distance 3 and
	// the exact path it had, and the surviving records keep the standard order.
	wantAfter := []Impact{
		{Dataset: "a", Distance: 1, Path: []string{"source", "a"}},
		{Dataset: "b", Distance: 1, Path: []string{"source", "b"}},
		{Dataset: "mid", Distance: 2, Path: []string{"source", "b", "mid"}},
		{Dataset: "view", Distance: 3, Path: []string{"source", "b", "mid", "view"}},
	}
	after := mustImpactsWithCutoffs(t, graph, "source", "a")
	if !reflect.DeepEqual(after, wantAfter) {
		t.Fatalf("cutoff query after removal = %v, want %v", after, wantAfter)
	}
	if names := impactNames(after); slices.Contains(names, "report") {
		t.Fatalf("removed report leaked into cutoff result: %v", names)
	}
	assertImpactOnce(t, after, "view", 3, []string{"source", "b", "mid", "view"})

	// The unrestricted query over the current graph drops report too, while view
	// keeps the same detour-free explanation it always had on the b side.
	full := mustImpacts(t, graph, "source")
	if got, want := impactNames(full), []string{"a", "b", "mid", "view"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("unrestricted query after removal = %v, want %v", got, want)
	}
	assertImpactOnce(t, full, "view", 3, []string{"source", "b", "mid", "view"})

	// The removed name is treated as unregistered by both lineage queries and a
	// repeated removal.
	if _, err := Impacts(graph, "report"); err == nil || !strings.Contains(err.Error(), "report") {
		t.Fatalf("Impacts(report) after removal: want not-found error naming report, got %v", err)
	}
	if _, err := Upstreams(graph, "report"); err == nil || !strings.Contains(err.Error(), "report") {
		t.Fatalf("Upstreams(report) after removal: want not-found error naming report, got %v", err)
	}
	if err := Unregister(graph, "report"); err == nil || !strings.Contains(err.Error(), "report") {
		t.Fatalf("second Unregister(report): want not-found error naming report, got %v", err)
	}

	// The result returned before the removal cannot be rewritten by it:
	// report's record and the source -> b -> mid -> report path are still
	// readable with the content they had when returned.
	if !reflect.DeepEqual(before, beforeSnapshot) {
		t.Fatalf("earlier cutoff result changed after removal: got %v, snapshot %v",
			before, beforeSnapshot)
	}
	assertImpactOnce(t, before, "report", 3, []string{"source", "b", "mid", "report"})
	assertImpactOnce(t, before, "view", 3, []string{"source", "b", "mid", "view"})
}

// A cutoff designation hides propagation routes in a query but does not detach
// any real dependency. While report is still registered, a directly derives
// report, so Unregister(a) must be refused for that remaining downstream even
// though a is named as the cutoff in the impact query. The refusal is atomic:
// registration, every relationship and list order, and the cutoff query result
// all stay exactly as before the attempt; the pre-attempt query snapshot keeps
// its content as well.
func TestUnregisterCutoffWithRealDownstreamRejected(t *testing.T) {
	graph := buildCutoffUnregisterGraph(t)
	assertConsistent(t, graph)

	beforeSnap := snapshot(graph)
	cutoffBefore := mustImpactsWithCutoffs(t, graph, "source", "a")
	cutoffSnapshot := copyImpacts(cutoffBefore)
	wantCutoff := []Impact{
		{Dataset: "a", Distance: 1, Path: []string{"source", "a"}},
		{Dataset: "b", Distance: 1, Path: []string{"source", "b"}},
		{Dataset: "mid", Distance: 2, Path: []string{"source", "b", "mid"}},
		{Dataset: "report", Distance: 3, Path: []string{"source", "b", "mid", "report"}},
		{Dataset: "view", Distance: 3, Path: []string{"source", "b", "mid", "view"}},
	}
	if !reflect.DeepEqual(cutoffBefore, wantCutoff) {
		t.Fatalf("baseline cutoff query = %v, want %v", cutoffBefore, wantCutoff)
	}

	// Cutting propagation at a in the query must not make a removable: the edge
	// a -> report is real lineage, so the request is refused for the remaining
	// direct downstream and must name both.
	err := Unregister(graph, "a")
	if err == nil {
		t.Fatal("Unregister(a) with report still downstream: expected refusal, got nil")
	}
	if !strings.Contains(err.Error(), "a") || !strings.Contains(err.Error(), "downstream") {
		t.Fatalf("want error naming a and its remaining downstream, got %v", err)
	}

	// Atomic refusal: the node, both directions of every edge and the stored
	// list order are byte-for-byte the pre-attempt graph.
	if !reflect.DeepEqual(snapshot(graph), beforeSnap) {
		t.Fatalf("rejected removal changed graph: before=%v after=%v", beforeSnap, snapshot(graph))
	}
	assertEntry(t, graph, "a", []string{"source"}, []string{"report"})
	assertEntry(t, graph, "report", []string{"a", "mid"}, nil)
	assertEntry(t, graph, "mid", []string{"b"}, []string{"report", "view"})
	assertEntry(t, graph, "view", []string{"mid"}, nil)
	assertEntry(t, graph, "source", nil, []string{"a", "b"})
	assertEntry(t, graph, "b", []string{"source"}, []string{"mid"})
	assertConsistent(t, graph)

	// The query answers exactly as before the attempt: the cutoff still hides
	// the a -> report route from the explanation, and report is still reached
	// around it at distance 3.
	cutoffAfter := mustImpactsWithCutoffs(t, graph, "source", "a")
	if !reflect.DeepEqual(cutoffAfter, wantCutoff) {
		t.Fatalf("cutoff query after refusal = %v, want %v", cutoffAfter, wantCutoff)
	}
	assertImpactOnce(t, cutoffAfter, "report", 3, []string{"source", "b", "mid", "report"})
	assertImpactOnce(t, cutoffAfter, "view", 3, []string{"source", "b", "mid", "view"})

	// The result already returned kept its content through the failed removal
	// and the follow-up query.
	if !reflect.DeepEqual(cutoffBefore, cutoffSnapshot) {
		t.Fatalf("earlier cutoff result changed after refusal: got %v, snapshot %v",
			cutoffBefore, cutoffSnapshot)
	}
}

// After report has been unregistered, a cutoff list that still names it must
// fail the whole query: the cutoff dataset is no longer registered, so the
// error names it and the result is nil. A valid cutoff (a) written first in the
// same list must not yield partial impacts, and the failed query must change no
// lineage relationship.
func TestImpactsWithCutoffsNamingUnregisteredLeafFailsWholly(t *testing.T) {
	graph := buildCutoffUnregisterGraph(t)
	mustUnregister(t, graph, "report")
	assertConsistent(t, graph)
	// Sanity: with the stale name dropped from the list, the valid cutoff query
	// succeeds and report is simply absent.
	surviving := mustImpactsWithCutoffs(t, graph, "source", "a")
	assertImpactOnce(t, surviving, "view", 3, []string{"source", "b", "mid", "view"})
	if slices.Contains(impactNames(surviving), "report") {
		t.Fatalf("report must be absent from the valid query: %v", surviving)
	}

	beforeSnap := snapshot(graph)

	// The valid cutoff a is listed first; the unregistered report later in the
	// list must still fail the entire request, with no partial results.
	impacts, err := ImpactsWithCutoffs(graph, "source", []string{"a", "report"})
	if err == nil || !strings.Contains(err.Error(), "cutoff dataset not found: report") {
		t.Fatalf("want unregistered-cutoff error naming report, got %v", err)
	}
	if impacts != nil {
		t.Fatalf("failed cutoff query must return nil results, got %v", impacts)
	}

	// The same stale name on its own fails identically, and so does naming it
	// ahead of the valid cutoff: input order cannot rescue any part of it.
	if got, err := ImpactsWithCutoffs(graph, "source", []string{"report"}); err == nil ||
		!strings.Contains(err.Error(), "cutoff dataset not found: report") || got != nil {
		t.Fatalf("single stale cutoff: want naming error and nil results, got %v, %v", got, err)
	}
	if got, err := ImpactsWithCutoffs(graph, "source", []string{"report", "a"}); err == nil ||
		!strings.Contains(err.Error(), "cutoff dataset not found: report") || got != nil {
		t.Fatalf("stale cutoff first: want naming error and nil results, got %v, %v", got, err)
	}

	// A failed query changes no node, edge or stored list order; the surviving
	// lineage still answers the valid cutoff query exactly as before.
	if !reflect.DeepEqual(snapshot(graph), beforeSnap) {
		t.Fatalf("failed cutoff query changed graph: before=%v after=%v", beforeSnap, snapshot(graph))
	}
	assertEntry(t, graph, "a", []string{"source"}, nil)
	assertEntry(t, graph, "mid", []string{"b"}, []string{"view"})
	assertEntry(t, graph, "view", []string{"mid"}, nil)
	assertConsistent(t, graph)
	again := mustImpactsWithCutoffs(t, graph, "source", "a")
	if !reflect.DeepEqual(again, surviving) {
		t.Fatalf("valid cutoff result changed after failed queries: got %v, want %v", again, surviving)
	}
}
