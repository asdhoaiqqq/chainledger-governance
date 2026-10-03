package chainledger

import (
	"reflect"
	"strings"
	"testing"
)

// assertImpacts checks the full ordered result: dataset, distance and path at
// every position, so a stale distance or a leftover path cannot slip through.
func assertImpacts(t *testing.T, impacts []Impact, want []Impact) {
	t.Helper()
	if len(impacts) != len(want) {
		t.Fatalf("got %d impacts %v, want %d %v", len(impacts), impacts, len(want), want)
	}
	for i, w := range want {
		got := impacts[i]
		if got.Dataset != w.Dataset || got.Distance != w.Distance || !sameStrings(got.Path, w.Path) {
			t.Fatalf("position %d = (%s,%d,%v), want (%s,%d,%v); full=%v",
				i, got.Dataset, got.Distance, got.Path, w.Dataset, w.Distance, w.Path, impacts)
		}
	}
}

// countImpact reports how many times name appears in the result.
func countImpact(impacts []Impact, name string) int {
	n := 0
	for _, im := range impacts {
		if im.Dataset == name {
			n++
		}
	}
	return n
}

// Re-pointing target away from its direct edge to old while a longer route
// through via -> mid survives: target and its own downstream leaf stay in
// old's scope, but at the recomputed shortest distance and path — never at
// the removed path's distance 1.
func TestReregisterRemovedShortPathFallsBackToLongerPath(t *testing.T) {
	graph := map[string]*Lineage{}
	mustRegister(t, graph, "old")
	mustRegister(t, graph, "via", "old")
	mustRegister(t, graph, "mid", "via")
	mustRegister(t, graph, "target", "old", "mid")
	mustRegister(t, graph, "leaf", "target")

	before, err := Impacts(graph, "old")
	if err != nil {
		t.Fatalf("Impacts(old): %v", err)
	}
	assertImpact(t, before, "target", 1, []string{"old", "target"})
	assertImpact(t, before, "leaf", 2, []string{"old", "target", "leaf"})

	// Drop the direct old -> target edge; the longer old -> via -> mid ->
	// target route remains.
	mustRegister(t, graph, "target", "mid")
	assertEntry(t, graph, "old", nil, []string{"via"})
	assertEntry(t, graph, "target", []string{"mid"}, []string{"leaf"})
	assertConsistent(t, graph)

	after, err := Impacts(graph, "old")
	if err != nil {
		t.Fatalf("Impacts(old) after re-register: %v", err)
	}
	assertImpacts(t, after, []Impact{
		{Dataset: "via", Distance: 1, Path: []string{"old", "via"}},
		{Dataset: "mid", Distance: 2, Path: []string{"old", "via", "mid"}},
		{Dataset: "target", Distance: 3, Path: []string{"old", "via", "mid", "target"}},
		{Dataset: "leaf", Distance: 4, Path: []string{"old", "via", "mid", "target", "leaf"}},
	})
	if n := countImpact(after, "target"); n != 1 {
		t.Fatalf("target listed %d times, want exactly 1: %v", n, after)
	}
	if n := countImpact(after, "leaf"); n != 1 {
		t.Fatalf("leaf listed %d times, want exactly 1: %v", n, after)
	}
}

// When the re-point severs old's last path to target, target and the
// downstreams reachable only through it leave old's scope together, while
// old's other branch is unaffected. The new source sees target and its
// retained downstreams with paths rooted at itself.
func TestReregisterSeversLastPathMovesScope(t *testing.T) {
	graph := map[string]*Lineage{}
	mustRegister(t, graph, "old")
	mustRegister(t, graph, "newsrc")
	mustRegister(t, graph, "target", "old")
	mustRegister(t, graph, "leaf1", "target")
	mustRegister(t, graph, "leaf2", "target")
	mustRegister(t, graph, "sibling", "old")

	mustRegister(t, graph, "target", "newsrc")
	assertEntry(t, graph, "old", nil, []string{"sibling"})
	assertEntry(t, graph, "newsrc", nil, []string{"target"})
	assertEntry(t, graph, "target", []string{"newsrc"}, []string{"leaf1", "leaf2"})
	assertConsistent(t, graph)

	fromOld, err := Impacts(graph, "old")
	if err != nil {
		t.Fatalf("Impacts(old) after re-register: %v", err)
	}
	assertImpacts(t, fromOld, []Impact{
		{Dataset: "sibling", Distance: 1, Path: []string{"old", "sibling"}},
	})

	fromNew, err := Impacts(graph, "newsrc")
	if err != nil {
		t.Fatalf("Impacts(newsrc) after re-register: %v", err)
	}
	assertImpacts(t, fromNew, []Impact{
		{Dataset: "target", Distance: 1, Path: []string{"newsrc", "target"}},
		{Dataset: "leaf1", Distance: 2, Path: []string{"newsrc", "target", "leaf1"}},
		{Dataset: "leaf2", Distance: 2, Path: []string{"newsrc", "target", "leaf2"}},
	})
}

// An origin whose every downstream was re-pointed away is still a valid query
// target and answers with a successful empty list.
func TestReregisterLeavesOriginWithEmptyScope(t *testing.T) {
	graph := map[string]*Lineage{}
	mustRegister(t, graph, "old")
	mustRegister(t, graph, "newsrc")
	mustRegister(t, graph, "only", "old")

	mustRegister(t, graph, "only", "newsrc")
	assertConsistent(t, graph)

	impacts, err := Impacts(graph, "old")
	if err != nil {
		t.Fatalf("Impacts(old) with no remaining downstream: %v", err)
	}
	if impacts == nil || len(impacts) != 0 {
		t.Fatalf("want empty non-nil list, got %v", impacts)
	}
}

// Diamond merge: removing one of two converging paths keeps the merge node
// listed once at the same distance with the surviving path; removing the last
// path drops the merge node and its exclusive downstream together while the
// merge's former sibling branches stay.
func TestReregisterDiamondPartialThenFullDisconnect(t *testing.T) {
	graph := map[string]*Lineage{}
	mustRegister(t, graph, "old")
	mustRegister(t, graph, "a", "old")
	mustRegister(t, graph, "b", "old")
	mustRegister(t, graph, "merge", "a", "b")
	mustRegister(t, graph, "down", "merge")

	before, err := Impacts(graph, "old")
	if err != nil {
		t.Fatalf("Impacts(old): %v", err)
	}
	// Two equal shortest paths to merge; the lexicographically smaller wins.
	assertImpacts(t, before, []Impact{
		{Dataset: "a", Distance: 1, Path: []string{"old", "a"}},
		{Dataset: "b", Distance: 1, Path: []string{"old", "b"}},
		{Dataset: "merge", Distance: 2, Path: []string{"old", "a", "merge"}},
		{Dataset: "down", Distance: 3, Path: []string{"old", "a", "merge", "down"}},
	})

	// One path removed: merge and down remain, re-explained through b.
	mustRegister(t, graph, "merge", "b")
	assertConsistent(t, graph)
	partial, err := Impacts(graph, "old")
	if err != nil {
		t.Fatalf("Impacts(old) after dropping a-edge: %v", err)
	}
	assertImpacts(t, partial, []Impact{
		{Dataset: "a", Distance: 1, Path: []string{"old", "a"}},
		{Dataset: "b", Distance: 1, Path: []string{"old", "b"}},
		{Dataset: "merge", Distance: 2, Path: []string{"old", "b", "merge"}},
		{Dataset: "down", Distance: 3, Path: []string{"old", "b", "merge", "down"}},
	})
	if n := countImpact(partial, "merge"); n != 1 {
		t.Fatalf("merge listed %d times after partial disconnect, want 1: %v", n, partial)
	}

	// Last path removed: merge becomes a root, taking down out of old's
	// scope; a and b are unaffected.
	mustRegister(t, graph, "merge")
	assertEntry(t, graph, "merge", nil, []string{"down"})
	assertConsistent(t, graph)
	full, err := Impacts(graph, "old")
	if err != nil {
		t.Fatalf("Impacts(old) after full disconnect: %v", err)
	}
	assertImpacts(t, full, []Impact{
		{Dataset: "a", Distance: 1, Path: []string{"old", "a"}},
		{Dataset: "b", Distance: 1, Path: []string{"old", "b"}},
	})
}

// The same logical graph built with different registration orders and
// different upstream list orders must answer identically, including the
// lexicographic tie-break among equal shortest paths.
func TestReregisterResultsIndependentOfRegistrationAndListOrder(t *testing.T) {
	build := func(registerMergeFirst bool, mergeParents ...string) map[string]*Lineage {
		graph := map[string]*Lineage{}
		mustRegister(t, graph, "old")
		mustRegister(t, graph, "newsrc")
		if registerMergeFirst {
			mustRegister(t, graph, "b", "old")
			mustRegister(t, graph, "a", "old")
		} else {
			mustRegister(t, graph, "a", "old")
			mustRegister(t, graph, "b", "old")
		}
		mustRegister(t, graph, "merge", mergeParents...)
		mustRegister(t, graph, "down", "merge")
		return graph
	}

	// g1 registers a before b and lists parents a,b; g2 does both in the
	// opposite order. The re-point then swaps each one's list order again.
	g1 := build(false, "a", "b")
	g2 := build(true, "b", "a")
	mustRegister(t, g1, "merge", "b", "a")
	mustRegister(t, g2, "merge", "a", "b")
	assertConsistent(t, g1)
	assertConsistent(t, g2)

	impacts1, err := Impacts(g1, "old")
	if err != nil {
		t.Fatalf("Impacts(g1, old): %v", err)
	}
	impacts2, err := Impacts(g2, "old")
	if err != nil {
		t.Fatalf("Impacts(g2, old): %v", err)
	}
	if !reflect.DeepEqual(impacts1, impacts2) {
		t.Fatalf("registration/list order changed results:\n g1=%v\n g2=%v", impacts1, impacts2)
	}
	assertImpacts(t, impacts1, []Impact{
		{Dataset: "a", Distance: 1, Path: []string{"old", "a"}},
		{Dataset: "b", Distance: 1, Path: []string{"old", "b"}},
		{Dataset: "merge", Distance: 2, Path: []string{"old", "a", "merge"}},
		{Dataset: "down", Distance: 3, Path: []string{"old", "a", "merge", "down"}},
	})

	// Re-point merge to newsrc in both graphs, again with opposite list
	// orders; the new source's view must agree as well.
	mustRegister(t, g1, "merge", "newsrc")
	mustRegister(t, g2, "merge", "newsrc")
	fromNew1, err := Impacts(g1, "newsrc")
	if err != nil {
		t.Fatalf("Impacts(g1, newsrc): %v", err)
	}
	fromNew2, err := Impacts(g2, "newsrc")
	if err != nil {
		t.Fatalf("Impacts(g2, newsrc): %v", err)
	}
	if !reflect.DeepEqual(fromNew1, fromNew2) {
		t.Fatalf("re-point results differ by order:\n g1=%v\n g2=%v", fromNew1, fromNew2)
	}
	assertImpacts(t, fromNew1, []Impact{
		{Dataset: "merge", Distance: 1, Path: []string{"newsrc", "merge"}},
		{Dataset: "down", Distance: 2, Path: []string{"newsrc", "merge", "down"}},
	})
}

// A rejected re-point — unknown new upstream, or a new upstream that is the
// dataset's own downstream and would close a cycle — names the offending
// upstream and leaves every previously queryable relation intact: no partial
// edge changes, no shifted distances or paths from either the old or the
// would-be new source.
func TestRejectedReregisterLeavesAllQueriesUnchanged(t *testing.T) {
	graph := map[string]*Lineage{}
	mustRegister(t, graph, "old")
	mustRegister(t, graph, "newsrc")
	mustRegister(t, graph, "target", "old")
	mustRegister(t, graph, "leaf", "target")

	beforeSnap := snapshot(graph)
	beforeOld, err := Impacts(graph, "old")
	if err != nil {
		t.Fatalf("Impacts(old): %v", err)
	}
	beforeNew, err := Impacts(graph, "newsrc")
	if err != nil {
		t.Fatalf("Impacts(newsrc): %v", err)
	}
	if len(beforeNew) != 0 {
		t.Fatalf("newsrc should start with empty scope, got %v", beforeNew)
	}

	assertUnchanged := func(t *testing.T) {
		t.Helper()
		if !reflect.DeepEqual(snapshot(graph), beforeSnap) {
			t.Fatalf("graph changed after rejected re-point: before=%v after=%v", beforeSnap, snapshot(graph))
		}
		gotOld, err := Impacts(graph, "old")
		if err != nil {
			t.Fatalf("Impacts(old) after rejection: %v", err)
		}
		if !reflect.DeepEqual(gotOld, beforeOld) {
			t.Fatalf("old scope changed after rejection: before=%v after=%v", beforeOld, gotOld)
		}
		gotNew, err := Impacts(graph, "newsrc")
		if err != nil {
			t.Fatalf("Impacts(newsrc) after rejection: %v", err)
		}
		if !reflect.DeepEqual(gotNew, beforeNew) {
			t.Fatalf("newsrc scope changed after rejection: before=%v after=%v", beforeNew, gotNew)
		}
		assertConsistent(t, graph)
	}

	// Unknown new upstream, listed after a valid one so a partial apply
	// would be visible.
	err = Register(graph, Dataset{Name: "target"}, []string{"newsrc", "ghost"})
	if err == nil || !strings.Contains(err.Error(), "ghost") {
		t.Fatalf("want unknown-parent error naming ghost, got %v", err)
	}
	assertUnchanged(t)

	// New upstream is target's own downstream: would close a cycle.
	err = Register(graph, Dataset{Name: "target"}, []string{"leaf"})
	if err == nil || !strings.Contains(err.Error(), "cycle") || !strings.Contains(err.Error(), "leaf") {
		t.Fatalf("want cycle error naming leaf, got %v", err)
	}
	assertUnchanged(t)

	// The old relations still answer exactly as before, including distances
	// and explanation paths.
	assertImpacts(t, beforeOld, []Impact{
		{Dataset: "target", Distance: 1, Path: []string{"old", "target"}},
		{Dataset: "leaf", Distance: 2, Path: []string{"old", "target", "leaf"}},
	})
}
