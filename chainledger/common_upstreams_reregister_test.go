package chainledger

import (
	"reflect"
	"strings"
	"testing"
)

// copyCommon returns a deep copy of a CommonUpstreams result so a test can prove
// a result taken before a re-registration keeps its original source, distances
// and explanation paths.
func copyCommon(found []CommonUpstream) []CommonUpstream {
	out := make([]CommonUpstream, len(found))
	for i, c := range found {
		out[i] = CommonUpstream{
			Dataset:          c.Dataset,
			DistanceToFirst:  c.DistanceToFirst,
			PathToFirst:      append([]string(nil), c.PathToFirst...),
			DistanceToSecond: c.DistanceToSecond,
			PathToSecond:     append([]string(nil), c.PathToSecond...),
		}
	}
	return out
}

// buildSharedAncestorLaterGraph builds the lineage shared by the same-name
// re-registration common-upstream regressions:
//
//	raw ──> a ──┬──> left
//	  └────────> late ──> right
//
// raw derives a and late; a derives left; late derives right. Both derived
// targets therefore trace back to the shared ancestor raw and to the later
// shared source a, where the route a -> late is shared as well.
func buildSharedAncestorLaterGraph(t *testing.T) map[string]*Lineage {
	t.Helper()
	return buildRegisteredGraph(t, [][]string{
		{"raw"},
		{"a", "raw"},
		{"late", "raw", "a"},
		{"left", "a"},
		{"right", "late"},
	})
}

// Regression for the nearest-common-upstream frontier after a same-name
// Register withdraws a target's dependency on the later common source. Before
// the replacement both derived datasets depend on a lineage running through the
// later source a, which sits strictly downstream of the shared ancestor raw, so
// the query lists a and hides raw even though raw reaches both over shorter
// edges. Re-registering one target so a no longer participates in that target's
// derivation — while keeping a in the graph and still deriving the other
// target — makes a exit the shared set; with no other common source downstream
// of raw, the ancestor, which can still derive both targets, re-enters the
// frontier. The withdrawn route must not be reused for distance or path, the
// remaining source must not be mistaken for a still-common one, and results
// stay ordered by source name.
func TestCommonUpstreamsReregisterDropsLaterSourceAncestorReappears(t *testing.T) {
	graph := buildSharedAncestorLaterGraph(t)
	assertConsistent(t, graph)

	// Before the replacement: a is the only frontier source. It reaches left in
	// one edge and right through a -> late -> right. raw is common by closure but
	// hidden behind a, regardless of raw's shorter direct routes to the targets.
	wantBefore := []CommonUpstream{
		{
			Dataset:          "a",
			DistanceToFirst:  1,
			PathToFirst:      []string{"a", "left"},
			DistanceToSecond: 2,
			PathToSecond:     []string{"a", "late", "right"},
		},
	}
	before := mustCommon(t, graph, "left", "right")
	if !reflect.DeepEqual(before, wantBefore) {
		t.Fatalf("common sources before replacement = %v, want %v (raw hidden behind a)", before, wantBefore)
	}
	assertEachCommonOnce(t, before)
	beforeCopy := copyCommon(before)

	// Same-name registration replaces left's whole direct-upstream list. a no
	// longer participates in left's derivation, but raw reaches left through
	// another route (raw -> left). a stays registered and still derives right
	// through late; only the dependency on it is withdrawn.
	mustRegister(t, graph, "left", "raw")
	assertConsistent(t, graph)
	assertEntry(t, graph, "left", []string{"raw"}, nil)
	assertEntry(t, graph, "a", []string{"raw"}, []string{"late"})
	assertEntry(t, graph, "late", []string{"raw", "a"}, []string{"right"})
	assertEntry(t, graph, "raw", nil, []string{"a", "late", "left"})
	assertEntry(t, graph, "right", []string{"late"}, nil)

	// After the replacement a exits the shared set: left can no longer reach it,
	// and its mere presence in the graph (still an ancestor of right) must not
	// keep it listed. raw is common again and has no common child edge, so it
	// re-enters as the sole frontier source; its distances and paths use only
	// surviving relationships — the withdrawn a -> left edge explains nothing.
	wantAfter := []CommonUpstream{
		{
			Dataset:          "raw",
			DistanceToFirst:  1,
			PathToFirst:      []string{"raw", "left"},
			DistanceToSecond: 2,
			PathToSecond:     []string{"raw", "late", "right"},
		},
	}
	after := mustCommon(t, graph, "left", "right")
	if !reflect.DeepEqual(after, wantAfter) {
		t.Fatalf("common sources after replacement = %v, want %v (a leaves, raw reappears)", after, wantAfter)
	}
	assertEachCommonOnce(t, after)
	assertCommon(t, after, "raw", 1, []string{"raw", "left"}, 2, []string{"raw", "late", "right"})

	// raw's explanation toward right must never travel the withdrawn dependency:
	// the route raw -> a -> left is dissolved, and on the right side the
	// length-2 raw -> late route wins over the longer raw -> a -> late route by
	// edge count, not lexicographic luck.
	assertUpstreamsSatisfy(t, graph, "right", "raw", 2, []string{"raw", "late", "right"})
	assertUpstreamsAbsent(t, graph, "left", "a")

	// Swapping the query order swaps only the two sides; the re-appeared source
	// and its current paths are otherwise identical.
	swapped := mustCommon(t, graph, "right", "left")
	if !reflect.DeepEqual(swapped, []CommonUpstream{{
		Dataset:          "raw",
		DistanceToFirst:  2,
		PathToFirst:      []string{"raw", "late", "right"},
		DistanceToSecond: 1,
		PathToSecond:     []string{"raw", "left"},
	}}) {
		t.Fatalf("swapped query = %v, want raw with sides exchanged", swapped)
	}

	// The result obtained before the replacement keeps its original source,
	// distances and paths; only the new query reflects the changed lineage.
	if !reflect.DeepEqual(before, beforeCopy) {
		t.Fatalf("earlier result changed after re-register: before=%v snapshot=%v", before, beforeCopy)
	}

	// A successful query is read-only: nodes, relationships and stored list
	// order are exactly what the successful registration left behind.
	queryState := snapshot(graph)
	if got := mustCommon(t, graph, "left", "right"); !reflect.DeepEqual(got, wantAfter) {
		t.Fatalf("repeat query = %v, want %v", got, wantAfter)
	}
	if !reflect.DeepEqual(snapshot(graph), queryState) {
		t.Fatalf("query changed graph: before=%v after=%v", queryState, snapshot(graph))
	}
}

// Regression for the same withdrawal where another later source still bridges
// the ancestor to both targets. After left stops depending on a, a leaves the
// shared set exactly as in the re-appearance case, but the shared source q
// (raw -> q -> both targets) survives and still stands strictly downstream of
// raw: the ancestor must stay hidden behind q, and q must keep being returned
// normally with distances and paths taken from the current edges.
func TestCommonUpstreamsReregisterDropsOneLaterSourceAnotherKeepsAncestorHidden(t *testing.T) {
	graph := buildRegisteredGraph(t, [][]string{
		{"raw"},
		{"a", "raw"},
		{"q", "raw"},
		{"late", "raw", "a"},
		{"left", "a", "q"},
		{"right", "late", "q"},
	})
	assertConsistent(t, graph)

	// Before the replacement two later common sources a and q sit downstream of
	// the shared ancestor raw; neither is downstream of the other on a shared
	// path, so both are the frontier and raw is hidden.
	before := mustCommon(t, graph, "left", "right")
	if got, want := commonNames(before), []string{"a", "q"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("common sources before replacement = %v, want %v", got, want)
	}
	assertCommon(t, before, "a", 1, []string{"a", "left"}, 2, []string{"a", "late", "right"})
	assertCommon(t, before, "q", 1, []string{"q", "left"}, 1, []string{"q", "right"})

	// Withdraw left's dependency on a; q still reaches both targets and a still
	// reaches right alone through late.
	mustRegister(t, graph, "left", "q")
	assertConsistent(t, graph)
	assertEntry(t, graph, "left", []string{"q"}, nil)
	assertEntry(t, graph, "a", []string{"raw"}, []string{"late"})
	assertEntry(t, graph, "q", []string{"raw"}, []string{"left", "right"})
	assertEntry(t, graph, "late", []string{"raw", "a"}, []string{"right"})

	// After the replacement a exits (it no longer derives left even though it
	// stays in the graph as right's ancestor), q is the sole surviving later
	// common source, and raw remains hidden behind q.
	after := mustCommon(t, graph, "left", "right")
	wantAfter := []CommonUpstream{
		{
			Dataset:          "q",
			DistanceToFirst:  1,
			PathToFirst:      []string{"q", "left"},
			DistanceToSecond: 1,
			PathToSecond:     []string{"q", "right"},
		},
	}
	if !reflect.DeepEqual(after, wantAfter) {
		t.Fatalf("common sources after replacement = %v, want %v (q survives, raw and a hidden/gone)",
			after, wantAfter)
	}
	assertEachCommonOnce(t, after)

	// left no longer reaches a at all; the remaining source staying in the graph
	// must not be read as a still-common one.
	assertUpstreamsAbsent(t, graph, "left", "a")
	assertUpstreamsSatisfy(t, graph, "right", "a", 2, []string{"a", "late", "right"})
}

// Regression for a rejected same-name replacement in the common-upstream
// scenario. A re-registration naming an unregistered upstream is refused and
// the error names that upstream; the graph's relationships and list order are
// untouched, and the following CommonUpstreams query still returns the sources,
// distances and paths of the pre-replacement lineage. Both the successful and
// the failed query only read lineage: neither changes any registration record.
func TestCommonUpstreamsRejectedReregisterLeavesSourcesUntouched(t *testing.T) {
	graph := buildSharedAncestorLaterGraph(t)
	assertConsistent(t, graph)

	before := mustCommon(t, graph, "left", "right")
	if got, want := commonNames(before), []string{"a"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("common sources before attempt = %v, want %v", got, want)
	}
	beforeCopy := copyCommon(before)
	stateBefore := snapshot(graph)

	// The request keeps a but also names an upstream that was never registered.
	// Validation runs before the graph is touched, so nothing is replaced.
	err := Register(graph, Dataset{Name: "left"}, []string{"raw", "ghost"})
	if err == nil || !strings.Contains(err.Error(), "unknown parent ghost") {
		t.Fatalf("want unknown-parent error naming ghost, got %v", err)
	}
	if !reflect.DeepEqual(snapshot(graph), stateBefore) {
		t.Fatalf("rejected registration changed graph: before=%v after=%v",
			stateBefore, snapshot(graph))
	}
	assertEntry(t, graph, "left", []string{"a"}, nil)
	assertEntry(t, graph, "a", []string{"raw"}, []string{"late", "left"})
	assertEntry(t, graph, "raw", nil, []string{"a", "late"})
	assertConsistent(t, graph)

	// The query after the refusal still returns the pre-replacement common
	// source with its pre-replacement distances and paths.
	after := mustCommon(t, graph, "left", "right")
	if !reflect.DeepEqual(after, before) {
		t.Fatalf("common sources after refusal = %v, want unchanged %v", after, before)
	}
	assertCommon(t, after, "a", 1, []string{"a", "left"}, 2, []string{"a", "late", "right"})

	// A failed query is also read-only: naming an unregistered target changes no
	// registration record, and a following query sees the same lineage.
	if _, err := CommonUpstreams(graph, "ghost", "right"); err == nil ||
		!strings.Contains(err.Error(), "ghost") {
		t.Fatalf("want error naming unknown first target ghost, got %v", err)
	}
	if _, err := CommonUpstreams(graph, "left", "ghost"); err == nil ||
		!strings.Contains(err.Error(), "ghost") {
		t.Fatalf("want error naming unknown second target ghost, got %v", err)
	}
	if !reflect.DeepEqual(snapshot(graph), stateBefore) {
		t.Fatalf("failed query changed graph: before=%v after=%v", stateBefore, snapshot(graph))
	}
	if got := mustCommon(t, graph, "left", "right"); !reflect.DeepEqual(got, beforeCopy) {
		t.Fatalf("query after failed queries = %v, want unchanged %v", got, beforeCopy)
	}

	// The result fetched before the refused request kept its original content
	// through the refusal and every later query.
	if !reflect.DeepEqual(before, beforeCopy) {
		t.Fatalf("earlier result changed after refused re-register: before=%v snapshot=%v",
			before, beforeCopy)
	}
}

// assertUpstreamsSatisfy checks that target's Upstreams query lists name at
// exactly the given shortest distance and explanation path.
func assertUpstreamsSatisfy(t *testing.T, graph map[string]*Lineage, target, name string,
	wantDistance int, wantPath []string) {
	t.Helper()
	found := mustUpstreams(t, graph, target)
	assertUpstreamOnce(t, found, name, wantDistance, wantPath)
}

// assertUpstreamsAbsent checks that target's Upstreams query does not list name.
func assertUpstreamsAbsent(t *testing.T, graph map[string]*Lineage, target, name string) {
	t.Helper()
	found := mustUpstreams(t, graph, target)
	for _, up := range found {
		if up.Dataset == name {
			t.Fatalf("Upstreams(%s) unexpectedly lists %s at distance %d path %v; full=%v",
				target, name, up.Distance, up.Path, upstreamNames(found))
		}
	}
}
