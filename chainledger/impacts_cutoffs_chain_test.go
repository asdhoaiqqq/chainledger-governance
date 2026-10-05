package chainledger

import (
	"reflect"
	"slices"
	"strings"
	"testing"
)

// Regression for TWO cutoff points lying on the same lineage route. A cutoff
// the unrestricted query can reach is not guaranteed in the cutoff result:
// when an earlier cutoff blocks every route to a later one, the later one is
// never reached and must not appear, while the earlier one is still reported
// itself at its own distance. Reaching a cutoff stops propagation, but cutoff
// validation is a separate whole-request step that still fails the query on an
// unregistered name even after an earlier cutoff has truncated every route.
//
// Lineage used here (four registered datasets on one chain):
//
//	source -> a -> b -> report
//
// With both a and b named, only a comes back: distance 1, path [source a].
// b is reachable in the full query (distance 2) yet must not appear, and
// report past b is unreachable. Naming both cutoffs neither unregisters them
// nor dissolves any lineage relationship.

// buildChainedCutoffChain registers source -> a -> b -> report.
func buildChainedCutoffChain(t *testing.T) map[string]*Lineage {
	t.Helper()
	return buildRegisteredGraph(t, [][]string{
		{"source"},
		{"a", "source"},
		{"b", "a"},
		{"report", "b"},
	})
}

// The full query establishes what is reachable without cutoffs: b at distance
// 2 and report at distance 3 along the single chain.
func TestImpactsWithCutoffsChainedFullQueryBaseline(t *testing.T) {
	graph := buildChainedCutoffChain(t)
	assertConsistent(t, graph)

	full := mustImpacts(t, graph, "source")
	want := []Impact{
		{Dataset: "a", Distance: 1, Path: []string{"source", "a"}},
		{Dataset: "b", Distance: 2, Path: []string{"source", "a", "b"}},
		{Dataset: "report", Distance: 3, Path: []string{"source", "a", "b", "report"}},
	}
	if !reflect.DeepEqual(full, want) {
		t.Fatalf("full query = %v, want %v", full, want)
	}
}

// With a and b both named as cutoffs, the traversal ends at a: only a is
// returned at distance 1, b is full-query reachable but hidden behind a, and
// report is unreachable. This is the judgment the old regression got wrong —
// "reachable in the full query" does not imply "present with cutoffs".
func TestImpactsWithCutoffsChainedEarlierCutoffHidesLaterOne(t *testing.T) {
	graph := buildChainedCutoffChain(t)
	assertConsistent(t, graph)
	before := snapshot(graph)

	impacts := mustImpactsWithCutoffs(t, graph, "source", "a", "b")
	want := []Impact{
		{Dataset: "a", Distance: 1, Path: []string{"source", "a"}},
	}
	if !reflect.DeepEqual(impacts, want) {
		t.Fatalf("cutoffs a,b: impacts = %v, want %v", impacts, want)
	}
	assertImpactOnce(t, impacts, "a", 1, []string{"source", "a"})
	names := impactNames(impacts)
	for _, gone := range []string{"b", "report"} {
		if slices.Contains(names, gone) {
			t.Fatalf("%s must not appear: every route to it passes cutoff a, got %v", gone, names)
		}
	}

	// The cutoff query neither unregistered the named datasets nor dissolved a
	// lineage relationship; every node, edge and stored list order survives.
	if !reflect.DeepEqual(snapshot(graph), before) {
		t.Fatalf("cutoff query changed graph: before=%v after=%v", before, snapshot(graph))
	}
	assertEntry(t, graph, "a", []string{"source"}, []string{"b"})
	assertEntry(t, graph, "b", []string{"a"}, []string{"report"})
	assertEntry(t, graph, "report", []string{"b"}, nil)
	assertConsistent(t, graph)

	// The cutoff registrations remain usable by ordinary queries.
	if got, err := Impacts(graph, "b"); err != nil {
		t.Fatalf("cutoff b must stay registered: %v", err)
	} else if want := []Impact{{Dataset: "report", Distance: 1, Path: []string{"b", "report"}}}; !reflect.DeepEqual(got, want) {
		t.Fatalf("Impacts(b) = %v, want %v", got, want)
	}
}

// The later cutoff alone does what an ordinary deep cutoff does: a and b both
// appear, report is dropped at b. This pins down that b disappears from the
// two-cutoff result purely because a blocks it, not because naming b has any
// special effect.
func TestImpactsWithCutoffsChainedLaterCutoffAloneStopsAtIt(t *testing.T) {
	graph := buildChainedCutoffChain(t)

	impacts := mustImpactsWithCutoffs(t, graph, "source", "b")
	want := []Impact{
		{Dataset: "a", Distance: 1, Path: []string{"source", "a"}},
		{Dataset: "b", Distance: 2, Path: []string{"source", "a", "b"}},
	}
	if !reflect.DeepEqual(impacts, want) {
		t.Fatalf("cutoff b: impacts = %v, want %v", impacts, want)
	}
	assertImpactOnce(t, impacts, "b", 2, []string{"source", "a", "b"})
	if names := impactNames(impacts); slices.Contains(names, "report") {
		t.Fatalf("report lies past cutoff b and must not appear, got %v", names)
	}
}

// Swapping the two cutoffs' input order, or repeating either name, must give
// the same result on the single chain.
func TestImpactsWithCutoffsChainedOrderAndDuplicatesInvariant(t *testing.T) {
	graph := buildChainedCutoffChain(t)
	want := []Impact{
		{Dataset: "a", Distance: 1, Path: []string{"source", "a"}},
	}
	for _, cutoffs := range [][]string{
		{"a", "b"},
		{"b", "a"},
		{"a", "a", "b"},
		{"b", "b", "a"},
		{"a", "b", "b"},
		{"b", "a", "b", "a"},
	} {
		got, err := ImpactsWithCutoffs(graph, "source", cutoffs)
		if err != nil {
			t.Fatalf("cutoffs %v: %v", cutoffs, err)
		}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("cutoffs %v: impacts = %v, want %v", cutoffs, got, want)
		}
	}
}

// Regression for the second lineage structure: a bypass branch reaches the
// later cutoff without passing the earlier one.
//
//	source -> a -> b -> report
//	  |                ^
//	  +-> c -> d ------+
//
// source derives a and c; a derives b; c derives d; b depends on a and d and
// derives report. With a and b both named, a stops the short branch but c and
// d reach b independently, so b DOES appear — exactly once, at distance 3 with
// path [source c d b], not at the full query's distance 2 via [source a b].
// Propagation still stops at b, so report is gone. A later cutoff must not be
// excluded merely because it sits downstream of another cutoff; it is included
// iff a cutoff-free route reaches it.

// buildChainedCutoffBypass registers the chain plus the c/d bypass into b.
func buildChainedCutoffBypass(t *testing.T) map[string]*Lineage {
	t.Helper()
	return buildRegisteredGraph(t, [][]string{
		{"source"},
		{"a", "source"},
		{"c", "source"},
		{"d", "c"},
		{"b", "a", "d"},
		{"report", "b"},
	})
}

// The unrestricted query explains b at distance 2 through a, the shortest
// route; that distance and path belong to the full query alone.
func TestImpactsWithCutoffsBypassFullQueryBaseline(t *testing.T) {
	graph := buildChainedCutoffBypass(t)
	assertConsistent(t, graph)

	full := mustImpacts(t, graph, "source")
	assertImpactOnce(t, full, "b", 2, []string{"source", "a", "b"})
	assertImpactOnce(t, full, "report", 3, []string{"source", "a", "b", "report"})
}

// With a and b named, b survives only via the cutoff-free c/d route at
// distance 3; the truncated distance-2 route via a is never reused. b is
// listed once, report past b is dropped, and results stay ordered by distance
// ascending and then by dataset name in Go string order.
func TestImpactsWithCutoffsBypassReachesLaterCutoffOnAllowedRoute(t *testing.T) {
	graph := buildChainedCutoffBypass(t)
	assertConsistent(t, graph)
	before := snapshot(graph)

	impacts := mustImpactsWithCutoffs(t, graph, "source", "a", "b")
	want := []Impact{
		{Dataset: "a", Distance: 1, Path: []string{"source", "a"}},
		{Dataset: "c", Distance: 1, Path: []string{"source", "c"}},
		{Dataset: "d", Distance: 2, Path: []string{"source", "c", "d"}},
		{Dataset: "b", Distance: 3, Path: []string{"source", "c", "d", "b"}},
	}
	if !reflect.DeepEqual(impacts, want) {
		t.Fatalf("cutoffs a,b: impacts = %v, want %v", impacts, want)
	}
	// b appears exactly once and is explained by the bypass, never by the
	// full query's shorter a route.
	assertImpactOnce(t, impacts, "b", 3, []string{"source", "c", "d", "b"})
	for _, im := range impacts {
		if slices.Contains(im.Path[:len(im.Path)-1], "a") {
			t.Fatalf("path traverses cutoff a: %v", im.Path)
		}
	}
	if names := impactNames(impacts); slices.Contains(names, "report") {
		t.Fatalf("reaching cutoff b must still stop propagation there: report must be gone, got %v", names)
	}
	// Distance-ascending, name-tie-broken order.
	if got, wantNames := impactNames(impacts), []string{"a", "c", "d", "b"}; !reflect.DeepEqual(got, wantNames) {
		t.Fatalf("order = %v, want %v", got, wantNames)
	}

	// Success query preserves nodes, both-direction edges and list order.
	if !reflect.DeepEqual(snapshot(graph), before) {
		t.Fatalf("cutoff query changed graph: before=%v after=%v", before, snapshot(graph))
	}
	assertEntry(t, graph, "b", []string{"a", "d"}, []string{"report"})
	assertEntry(t, graph, "report", []string{"b"}, nil)
	assertConsistent(t, graph)

	// A subsequent full query is unaffected: b is back to distance 2 via a.
	full := mustImpacts(t, graph, "source")
	assertImpactOnce(t, full, "b", 2, []string{"source", "a", "b"})
	assertImpactOnce(t, full, "report", 3, []string{"source", "a", "b", "report"})
}

// Same bypass structure: cutoff input order and repeated names must not change
// the result, and neither cutoff being downstream of the other changes its
// inclusion.
func TestImpactsWithCutoffsBypassOrderAndDuplicatesInvariant(t *testing.T) {
	graph := buildChainedCutoffBypass(t)
	want := []Impact{
		{Dataset: "a", Distance: 1, Path: []string{"source", "a"}},
		{Dataset: "c", Distance: 1, Path: []string{"source", "c"}},
		{Dataset: "d", Distance: 2, Path: []string{"source", "c", "d"}},
		{Dataset: "b", Distance: 3, Path: []string{"source", "c", "d", "b"}},
	}
	for _, cutoffs := range [][]string{
		{"a", "b"},
		{"b", "a"},
		{"a", "a", "b"},
		{"b", "b", "a"},
		{"b", "a", "b"},
	} {
		got, err := ImpactsWithCutoffs(graph, "source", cutoffs)
		if err != nil {
			t.Fatalf("cutoffs %v: %v", cutoffs, err)
		}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("cutoffs %v: impacts = %v, want %v", cutoffs, got, want)
		}
	}
}

// Stopping propagation is not stopping validation: every cutoff name is
// checked against the registry BEFORE the traversal and regardless of where
// propagation would stop. On the single chain, a already blocks every route
// (so no successful traversal could reach anything beyond it), yet an
// unregistered name later in the list must fail the whole query with an error
// naming it and nil results — the already-reachable a must not come back as a
// partial result.
func TestImpactsWithCutoffsChainedUnregisteredNameAfterTruncatingCutoff(t *testing.T) {
	graph := buildChainedCutoffChain(t)
	assertConsistent(t, graph)
	before := snapshot(graph)

	for _, cutoffs := range [][]string{
		{"a", "ghost"},
		{"ghost", "a"},
		{"b", "ghost"}, // first cutoff alone would succeed; the bad name still fails it all
		{"a", "b", "ghost"},
		{"a", "a", "ghost"},
	} {
		got, err := ImpactsWithCutoffs(graph, "source", cutoffs)
		if err == nil || !strings.Contains(err.Error(), "cutoff dataset not found: ghost") {
			t.Fatalf("cutoffs %v: want not-found cutoff error naming ghost, got %v", cutoffs, err)
		}
		if got != nil {
			t.Fatalf("cutoffs %v: want nil results even though the traversal has reachable cutoffs, got %v",
				cutoffs, got)
		}
	}

	// The unregistered name is exactly "ghost"; the registered cutoffs stay
	// registered and no lineage is altered.
	if !reflect.DeepEqual(snapshot(graph), before) {
		t.Fatalf("failed queries changed graph: before=%v after=%v", before, snapshot(graph))
	}
	assertEntry(t, graph, "a", []string{"source"}, []string{"b"})
	assertEntry(t, graph, "b", []string{"a"}, []string{"report"})
	assertConsistent(t, graph)

	// The same rule on the bypass structure: a truncates the short branch and
	// b is reached around it, but the trailing unregistered name still fails
	// the whole request with nil results rather than returning a,c,d,b.
	bypass := buildChainedCutoffBypass(t)
	bypassBefore := snapshot(bypass)
	got, err := ImpactsWithCutoffs(bypass, "source", []string{"a", "b", "ghost"})
	if err == nil || !strings.Contains(err.Error(), "cutoff dataset not found: ghost") {
		t.Fatalf("bypass: want not-found cutoff error naming ghost, got %v", err)
	}
	if got != nil {
		t.Fatalf("bypass: want nil results despite reachable cutoffs a and b, got %v", got)
	}
	if !reflect.DeepEqual(snapshot(bypass), bypassBefore) {
		t.Fatalf("bypass: failed query changed graph: before=%v after=%v", bypassBefore, snapshot(bypass))
	}
}

// A failed whole-request validation leaves the very next valid query
// unaffected, in both lineage structures.
func TestImpactsWithCutoffsChainedFailedValidationDoesNotPoisonLaterQuery(t *testing.T) {
	chain := buildChainedCutoffChain(t)
	if _, err := ImpactsWithCutoffs(chain, "source", []string{"a", "missing"}); err == nil {
		t.Fatal("expected not-found cutoff error")
	}
	chainWant := []Impact{{Dataset: "a", Distance: 1, Path: []string{"source", "a"}}}
	if got := mustImpactsWithCutoffs(t, chain, "source", "a", "b"); !reflect.DeepEqual(got, chainWant) {
		t.Fatalf("chain valid query after failed one = %v, want %v", got, chainWant)
	}

	bypass := buildChainedCutoffBypass(t)
	if _, err := ImpactsWithCutoffs(bypass, "source", []string{"b", "missing"}); err == nil {
		t.Fatal("expected not-found cutoff error")
	}
	bypassWant := []Impact{
		{Dataset: "a", Distance: 1, Path: []string{"source", "a"}},
		{Dataset: "c", Distance: 1, Path: []string{"source", "c"}},
		{Dataset: "d", Distance: 2, Path: []string{"source", "c", "d"}},
		{Dataset: "b", Distance: 3, Path: []string{"source", "c", "d", "b"}},
	}
	if got := mustImpactsWithCutoffs(t, bypass, "source", "b", "a"); !reflect.DeepEqual(got, bypassWant) {
		t.Fatalf("bypass valid query after failed one = %v, want %v", got, bypassWant)
	}
}

// Existing no-cutoff behavior is unchanged by the chained-cutoff guarantees:
// the nil and empty cutoff forms stay exactly the full Impacts query on both
// structures, including the short a route and report.
func TestImpactsWithCutoffsChainedEmptyStillEqualsFull(t *testing.T) {
	for _, build := range []func(t *testing.T) map[string]*Lineage{
		buildChainedCutoffChain,
		buildChainedCutoffBypass,
	} {
		graph := build(t)
		full := mustImpacts(t, graph, "source")
		nilCut := mustImpactsWithCutoffs(t, graph, "source")
		if !reflect.DeepEqual(nilCut, full) {
			t.Fatalf("nil cutoffs = %v, want full Impacts %v", nilCut, full)
		}
		empty, err := ImpactsWithCutoffs(graph, "source", []string{})
		if err != nil || !reflect.DeepEqual(empty, full) {
			t.Fatalf("empty cutoffs = %v, %v; want full Impacts %v", empty, err, full)
		}
	}
}
