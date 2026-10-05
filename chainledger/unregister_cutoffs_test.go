package chainledger

import (
	"reflect"
	"slices"
	"strings"
	"testing"
)

// Regression for the combined lifecycle of Unregister used together with
// ImpactsWithCutoffs. The lineage is the cutoff scenario with two leaves that
// share a merge node:
//
//	source ──> a ────────> report
//	  │                    ^
//	  └──> b ──> mid ──────┤
//	                │       │
//	                └──> view
//
// source derives a and b; a derives report directly (the short route); b
// reaches report through mid (the route around a cutoff); mid additionally
// derives view. report depends on two upstreams, and both report and view are
// leaves. With a named as the cutoff, a itself still appears at distance 1,
// report is reached around the cutoff via source -> b -> mid -> report at
// distance 3, and view is reached via source -> b -> mid -> view at distance 3.
// Each dataset appears once, ordered by distance then name.
//
// The sequence under regression:
//
//  1. The cutoff query is run first and its result saved.
//  2. While report is still registered, Unregister(a) must be refused because a
//     still has a direct downstream; naming a as a propagation cutoff in a query
//     hides no real dependency and must not make it removable. The refusal
//     leaves registration, relationships, list order and query results exactly
//     as they were.
//  3. Unregister(report) succeeds: report's registration disappears and it is
//     dropped from both a's and mid's downstream lists, mid's relationship to
//     view and the remaining list order survive, and every other node's
//     relationships are untouched.
//  4. The same origin/cutoff query now reflects the current registration and
//     lineage: report is gone while view keeps its distance and explanation
//     path; the result saved before the removal still contains report's record
//     and path exactly as returned.
//  5. After the removal, naming report in the cutoff list (even after the
//     still-valid a) fails the whole query with nil results and an error naming
//     the unregistered cutoff; no partial impact scope is returned, and the
//     failed query changes no lineage.
func TestUnregisterLeafAndCutoffImpactsCombinedLifecycle(t *testing.T) {
	graph := buildRegisteredGraph(t, [][]string{
		{"source"},
		{"a", "source"},
		{"b", "source"},
		{"mid", "b"},
		{"report", "a", "mid"}, // leaf with two upstreams: short via a, long via mid
		{"view", "mid"},        // the other leaf off mid
	})
	assertConsistent(t, graph)
	assertEntry(t, graph, "source", nil, []string{"a", "b"})
	assertEntry(t, graph, "a", []string{"source"}, []string{"report"})
	assertEntry(t, graph, "b", []string{"source"}, []string{"mid"})
	assertEntry(t, graph, "mid", []string{"b"}, []string{"report", "view"})
	assertEntry(t, graph, "report", []string{"a", "mid"}, nil)
	assertEntry(t, graph, "view", []string{"mid"}, nil)

	// Phase 1: query the impact scope with a as cutoff before any removal. a
	// stays in the result itself, but nothing propagates through it: report
	// survives around the cutoff along b and mid at distance 3, and view is
	// reached off mid, also at distance 3, without passing through report.
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
	// Anchors: the cutoff node is present, and both distance-3 leaves are
	// explained by routes that traverse no cutoff; each dataset appears once.
	assertImpactOnce(t, before, "a", 1, []string{"source", "a"})
	assertImpactOnce(t, before, "report", 3, []string{"source", "b", "mid", "report"})
	assertImpactOnce(t, before, "view", 3, []string{"source", "b", "mid", "view"})
	if got, want := impactNames(before), []string{"a", "b", "mid", "report", "view"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("initial cutoff query order = %v, want %v", got, want)
	}
	// Save the result; later graph changes must not rewrite already-returned
	// records or their independently copied paths.
	saved := copyImpacts(before)

	// Phase 2: before report is removed, a still has it as a direct downstream.
	// Having served as a propagation cutoff in the read-only query changes no
	// edge, so the removal must be refused on the real dependency.
	preRefusal := snapshot(graph)
	err := Unregister(graph, "a")
	if err == nil {
		t.Fatal("Unregister(a) with direct downstream report: expected refusal, got nil")
	}
	if !strings.Contains(err.Error(), "a") || !strings.Contains(err.Error(), "downstream") {
		t.Fatalf("Unregister(a): want error naming a and its remaining downstream, got %v", err)
	}

	// The refusal is atomic: nodes, edges and stored list order are exactly the
	// pre-request graph.
	if !reflect.DeepEqual(snapshot(graph), preRefusal) {
		t.Fatalf("rejected Unregister(a) changed graph: before=%v after=%v",
			preRefusal, snapshot(graph))
	}
	assertEntry(t, graph, "a", []string{"source"}, []string{"report"})
	assertEntry(t, graph, "mid", []string{"b"}, []string{"report", "view"})
	assertEntry(t, graph, "report", []string{"a", "mid"}, nil)
	assertEntry(t, graph, "view", []string{"mid"}, nil)
	assertConsistent(t, graph)

	// The cutoff query answers identically after the refusal, and the result
	// saved before it keeps its original content.
	if got := mustImpactsWithCutoffs(t, graph, "source", "a"); !reflect.DeepEqual(got, wantBefore) {
		t.Fatalf("cutoff query after refusal = %v, want %v", got, wantBefore)
	}
	if !reflect.DeepEqual(before, saved) {
		t.Fatalf("saved cutoff result changed after the refusal: got %v, snapshot %v", before, saved)
	}

	// Phase 3: report is a leaf even though it has two upstreams, so removal
	// succeeds.
	preRemoval := snapshot(graph)
	if err := Unregister(graph, "report"); err != nil {
		t.Fatalf("Unregister(report): %v", err)
	}
	if _, ok := graph["report"]; ok {
		t.Fatal("report registration still present after Unregister")
	}
	if got, want := len(graph), 5; got != want {
		t.Fatalf("dataset count = %d, want %d; graph=%v", got, want, graph)
	}

	// Both direct upstreams lose report from their downstream lists. a had
	// report as its only downstream, mid keeps view in the surviving slot.
	assertEntry(t, graph, "a", []string{"source"}, nil)
	assertEntry(t, graph, "mid", []string{"b"}, []string{"view"})
	// mid's relationship to view is preserved, and every node not directly
	// connected to report keeps exactly its pre-removal lists; no surviving node
	// had report as an upstream (report was a leaf), so all parent lists stay.
	for name, entry := range graph {
		if !slices.Equal(entry.Parents, preRemoval.parents[name]) {
			t.Errorf("%s parents = %v, want %v", name, entry.Parents, preRemoval.parents[name])
		}
		wantChildren := preRemoval.children[name]
		if name == "a" || name == "mid" {
			wantChildren = nil
			for _, c := range preRemoval.children[name] {
				if c != "report" {
					wantChildren = append(wantChildren, c)
				}
			}
		}
		if !slices.Equal(entry.Children, wantChildren) {
			t.Errorf("%s children = %v, want %v", name, entry.Children, wantChildren)
		}
	}
	assertEntry(t, graph, "source", nil, []string{"a", "b"})
	assertEntry(t, graph, "b", []string{"source"}, []string{"mid"})
	assertEntry(t, graph, "view", []string{"mid"}, nil)
	assertConsistent(t, graph)

	// Phase 4: the same origin and cutoff list now describe the current
	// registration and lineage. report has left the result, while view keeps its
	// distance 3 and its b/mid explanation path; ordering stays by distance then
	// name.
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
		t.Fatalf("report must be gone from the cutoff result, got %v", names)
	}
	assertImpactOnce(t, after, "view", 3, []string{"source", "b", "mid", "view"})
	if got, want := impactNames(after), []string{"a", "b", "mid", "view"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("cutoff query order after removal = %v, want %v", got, want)
	}
	// The explicit-slice entry-point form agrees with the variadic form.
	if got, err := ImpactsWithCutoffs(graph, "source", []string{"a"}); err != nil || !reflect.DeepEqual(got, wantAfter) {
		t.Fatalf("explicit cutoff slice = %v, %v; want %v", got, err, wantAfter)
	}

	// The unrestricted query likewise reflects the current edges: view is
	// distance 3 through b and mid, report is absent.
	fullAfter := mustImpacts(t, graph, "source")
	if got, want := impactNames(fullAfter), []string{"a", "b", "mid", "view"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("full query after removal = %v, want %v", got, want)
	}
	assertImpactOnce(t, fullAfter, "view", 3, []string{"source", "b", "mid", "view"})

	// The result saved before the removal is still readable with report's
	// original record and bypass path, untouched by the removal and new queries.
	if !reflect.DeepEqual(before, saved) {
		t.Fatalf("saved cutoff result was rewritten: got %v, snapshot %v", before, saved)
	}
	assertImpactOnce(t, saved, "report", 3, []string{"source", "b", "mid", "report"})
	assertImpactOnce(t, saved, "view", 3, []string{"source", "b", "mid", "view"})

	// Phase 5: a cutoff list that still names the now-unregistered report fails
	// the whole query. The valid cutoff a listed first must not yield a partial
	// scope: results are nil and the error names report.
	preFailedQuery := snapshot(graph)
	partial, err := ImpactsWithCutoffs(graph, "source", []string{"a", "report"})
	if err == nil || !strings.Contains(err.Error(), "cutoff dataset not found: report") {
		t.Fatalf("cutoffs [a report]: want not-found cutoff error naming report, got %v", err)
	}
	if partial != nil {
		t.Fatalf("cutoffs [a report]: want nil results, got %v", partial)
	}
	// The stale name alone is rejected identically.
	if _, err := ImpactsWithCutoffs(graph, "source", []string{"report"}); err == nil ||
		!strings.Contains(err.Error(), "cutoff dataset not found: report") {
		t.Fatalf("cutoffs [report]: want not-found cutoff error naming report, got %v", err)
	}
	// The failed queries change no lineage, and a subsequent valid query still
	// returns the post-removal scope.
	if !reflect.DeepEqual(snapshot(graph), preFailedQuery) {
		t.Fatalf("failed cutoff query changed graph: before=%v after=%v",
			preFailedQuery, snapshot(graph))
	}
	assertConsistent(t, graph)
	if got := mustImpactsWithCutoffs(t, graph, "source", "a"); !reflect.DeepEqual(got, wantAfter) {
		t.Fatalf("cutoff query after failed query = %v, want %v", got, wantAfter)
	}

	// The removed name is treated as unregistered by the other public entry
	// points too, and the saved pre-removal result remains intact to the end.
	if _, err := Impacts(graph, "report"); err == nil || !strings.Contains(err.Error(), "report") {
		t.Fatalf("Impacts(report) after removal: want not-found error, got %v", err)
	}
	if _, err := Upstreams(graph, "report"); err == nil || !strings.Contains(err.Error(), "report") {
		t.Fatalf("Upstreams(report) after removal: want not-found error, got %v", err)
	}
	if !reflect.DeepEqual(before, saved) {
		t.Fatalf("saved cutoff result changed by later failed queries: got %v, snapshot %v",
			before, saved)
	}
}
