package chainledger

import (
	"reflect"
	"strings"
	"testing"
)

// These regressions protect CompareUpstreamLineage over the exact workflow the
// library documents: lineage is registered through Register, the target's
// complete upstream export is saved, one intermediate dataset's DIRECT upstreams
// are replaced by a same-name Register (the target's own direct upstreams stay
// untouched), the target is exported again, and CompareUpstreamLineage reports
// what changed across the full ancestor scope — not just the target's direct
// upstream list.

// depPairSet indexes a dependency list for membership checks.
func depPairSet(deps []Dependency) map[[2]string]bool {
	set := make(map[[2]string]bool, len(deps))
	for _, d := range deps {
		set[[2]string{d.From, d.To}] = true
	}
	return set
}

// assertNoDependencyTouches fails when any listed dependency has one of names as
// an endpoint: datasets outside the comparison scope must never appear.
func assertNoDependencyTouches(t *testing.T, deps []Dependency, names ...string) {
	t.Helper()
	for _, d := range deps {
		for _, name := range names {
			if d.From == name || d.To == name {
				t.Errorf("out-of-scope dataset %q appears in dependency %s -> %s (%v)",
					name, d.From, d.To, depPairs(deps))
			}
		}
	}
}

// assertEachDependencyOnce fails when one dependency pair is listed more than
// once: merging branches must not duplicate a shared relationship.
func assertEachDependencyOnce(t *testing.T, deps []Dependency, label string) {
	t.Helper()
	seen := map[[2]string]int{}
	for _, d := range deps {
		seen[[2]string{d.From, d.To}]++
	}
	for pair, count := range seen {
		if count > 1 {
			t.Errorf("%s lists %s -> %s %d times, want exactly once", label, pair[0], pair[1], count)
		}
	}
}

// sharedSourceSwapGraph builds the shared-source scenario: old itself derives
// from oldroot, and target t reaches old through TWO different intermediate
// datasets m1 and m2. The already-registered replacement source new carries its
// own, disjoint ancestry (newroot -> n1 -> new). old additionally feeds
// offnode, a branch that cannot reach t. lone is independent and down is t's
// downstream; neither belongs to t's upstream scope.
func sharedSourceSwapGraph(t *testing.T) map[string]*Lineage {
	t.Helper()
	return buildRegisteredGraph(t, [][]string{
		{"oldroot"},
		{"old", "oldroot"},
		{"newroot"},
		{"n1", "newroot"},
		{"new", "n1"},
		{"m1", "old"},
		{"m2", "old"},
		{"t", "m1", "m2"},
		{"offnode", "old"},
		{"down", "t"},
		{"lone"},
	})
}

// Replacing only m1's direct upstream with the registered source new must list
// new -> m1 together with new's own ancestor dependencies entering t's scope,
// and list old -> m1 as removed. Because old still reaches t through the
// untouched m2 branch, old's own relation to oldroot and every edge on the m2
// branch must NOT be reported removed, shared relationships must not duplicate
// where branches merge, and independent datasets, t's downstream and old's
// non-target branch must stay out of both lists. t's direct upstreams never
// change.
func TestCompareUpstreamLineageReregisterSharedSourceOtherBranch(t *testing.T) {
	graph := sharedSourceSwapGraph(t)
	assertConsistent(t, graph)

	beforeText := mustExport(t, graph, "t")
	beforeDoc := parseExport(t, beforeText)
	wantBeforeEdges := [][2]string{
		{"m1", "t"},
		{"m2", "t"},
		{"old", "m1"},
		{"old", "m2"},
		{"oldroot", "old"},
	}
	if got := exportEdgeNames(beforeDoc); !reflect.DeepEqual(got, wantBeforeEdges) {
		t.Fatalf("before export edges = %v, want %v", got, wantBeforeEdges)
	}

	// Swap just m1's direct upstream; t itself is not re-registered, so its
	// direct upstream list is the same afterwards.
	mustRegister(t, graph, "m1", "new")
	assertConsistent(t, graph)
	assertEntry(t, graph, "m1", []string{"new"}, []string{"t"})
	assertEntry(t, graph, "m2", []string{"old"}, []string{"t"})
	assertEntry(t, graph, "t", []string{"m1", "m2"}, []string{"down"})
	assertEntry(t, graph, "old", []string{"oldroot"}, []string{"m2", "offnode"})
	assertEntry(t, graph, "new", []string{"n1"}, []string{"m1"})

	afterText := mustExport(t, graph, "t")
	afterDoc := parseExport(t, afterText)
	wantAfterEdges := [][2]string{
		{"m1", "t"},
		{"m2", "t"},
		{"n1", "new"},
		{"new", "m1"},
		{"newroot", "n1"},
		{"old", "m2"},
		{"oldroot", "old"},
	}
	if got := exportEdgeNames(afterDoc); !reflect.DeepEqual(got, wantAfterEdges) {
		t.Fatalf("after export edges = %v, want %v", got, wantAfterEdges)
	}

	diff, err := CompareUpstreamLineage(beforeText, afterText, "t")
	if err != nil {
		t.Fatalf("CompareUpstreamLineage: %v", err)
	}

	wantAdded := [][2]string{
		{"n1", "new"},
		{"new", "m1"},
		{"newroot", "n1"},
	}
	if got := depPairs(diff.Added); !reflect.DeepEqual(got, wantAdded) {
		t.Errorf("Added = %v, want %v", got, wantAdded)
	}
	if got, want := depPairs(diff.Removed), [][2]string{{"old", "m1"}}; !reflect.DeepEqual(got, want) {
		t.Errorf("Removed = %v, want %v", got, want)
	}
	assertEachDependencyOnce(t, diff.Added, "Added")
	assertEachDependencyOnce(t, diff.Removed, "Removed")

	// The shared source survives via the m2 branch: its ancestor edge and the
	// retained branch's edges are common to both scopes and must not be reported
	// as removed (or added).
	removed := depPairSet(diff.Removed)
	added := depPairSet(diff.Added)
	for _, common := range [][2]string{
		{"oldroot", "old"}, // old still participates, so its own ancestry stays
		{"old", "m2"},      // the retained branch from old
		{"m2", "t"},
		{"m1", "t"}, // t's unchanged direct upstream
	} {
		if removed[common] {
			t.Errorf("surviving relationship %s -> %s wrongly listed as removed", common[0], common[1])
		}
		if added[common] {
			t.Errorf("common relationship %s -> %s wrongly listed as added", common[0], common[1])
		}
	}

	// Independent datasets, the target's downstream and old's branch that cannot
	// reach t never enter either list, even though their nodes and edges remain
	// in the graph.
	assertNoDependencyTouches(t, diff.Added, "lone", "down", "offnode")
	assertNoDependencyTouches(t, diff.Removed, "lone", "down", "offnode")
	if removed[[2]string{"old", "offnode"}] || added[[2]string{"old", "offnode"}] {
		t.Errorf("old -> offnode cannot reach t and must never be diffed")
	}
}

// When old reached t ONLY through the modified intermediate, the same swap must
// remove old's whole ancestor segment edge by edge — even though old, oldroot
// and their relationship stay registered in the graph (old still feeds a
// non-target branch). The shared m1 -> t edge stays common, and nothing outside
// t's scope appears.
func TestCompareUpstreamLineageReregisterLastRouteDropsOldAncestors(t *testing.T) {
	graph := buildRegisteredGraph(t, [][]string{
		{"oldroot"},
		{"old", "oldroot"},
		{"newroot"},
		{"n1", "newroot"},
		{"new", "n1"},
		{"m1", "old"},
		{"t", "m1"},
		{"offnode", "old"}, // old's non-target branch survives the swap in the graph
		{"down", "t"},
		{"lone"},
	})
	assertConsistent(t, graph)

	beforeText := mustExport(t, graph, "t")
	if got, want := exportEdgeNames(parseExport(t, beforeText)), [][2]string{
		{"m1", "t"},
		{"old", "m1"},
		{"oldroot", "old"},
	}; !reflect.DeepEqual(got, want) {
		t.Fatalf("before export edges = %v, want %v", got, want)
	}

	// m1's only route from old is replaced wholesale by new; t's own direct
	// upstream list still names m1 and is not part of the request.
	mustRegister(t, graph, "m1", "new")
	assertConsistent(t, graph)
	assertEntry(t, graph, "m1", []string{"new"}, []string{"t"})
	assertEntry(t, graph, "t", []string{"m1"}, []string{"down"})

	// The old segment's nodes and edges remain in the graph itself: old still
	// derives from oldroot and still feeds offnode. They must nevertheless leave
	// t's comparison scope.
	assertEntry(t, graph, "old", []string{"oldroot"}, []string{"offnode"})
	assertEntry(t, graph, "oldroot", nil, []string{"old"})
	assertEntry(t, graph, "offnode", []string{"old"}, nil)

	afterText := mustExport(t, graph, "t")
	if got, want := exportEdgeNames(parseExport(t, afterText)), [][2]string{
		{"m1", "t"},
		{"n1", "new"},
		{"new", "m1"},
		{"newroot", "n1"},
	}; !reflect.DeepEqual(got, want) {
		t.Fatalf("after export edges = %v, want %v", got, want)
	}

	diff, err := CompareUpstreamLineage(beforeText, afterText, "t")
	if err != nil {
		t.Fatalf("CompareUpstreamLineage: %v", err)
	}

	wantAdded := [][2]string{
		{"n1", "new"},
		{"new", "m1"},
		{"newroot", "n1"},
	}
	if got := depPairs(diff.Added); !reflect.DeepEqual(got, wantAdded) {
		t.Errorf("Added = %v, want %v", got, wantAdded)
	}
	// Ordered by From then To: "old" sorts before "oldroot".
	wantRemoved := [][2]string{
		{"old", "m1"},
		{"oldroot", "old"},
	}
	if got := depPairs(diff.Removed); !reflect.DeepEqual(got, wantRemoved) {
		t.Errorf("Removed = %v, want %v", got, wantRemoved)
	}
	assertEachDependencyOnce(t, diff.Added, "Added")
	assertEachDependencyOnce(t, diff.Removed, "Removed")

	// m1 -> t is shared even though everything behind m1 changed, and the old
	// segment's surviving graph edges are reported only through t's scope.
	added, removed := depPairSet(diff.Added), depPairSet(diff.Removed)
	if added[[2]string{"m1", "t"}] || removed[[2]string{"m1", "t"}] {
		t.Errorf("m1 -> t is t's unchanged direct dependency and must stay common")
	}
	for _, pair := range wantAdded {
		if removed[pair] {
			t.Errorf("%s -> %s cannot be both added and removed", pair[0], pair[1])
		}
	}
	assertNoDependencyTouches(t, diff.Added, "lone", "down", "offnode")
	assertNoDependencyTouches(t, diff.Removed, "lone", "down", "offnode")
}

// A replacement request that mixes a legal, already-registered new source with
// the target itself (which would close a cycle) must be rejected naming the
// target as the cycle cause and leave every relationship exactly as it was. The
// target export saved afterwards is byte-identical to the one saved before the
// rejected request, so comparing the two succeeds with two non-nil empty lists
// — never partial adds or removes.
func TestCompareUpstreamLineageRejectedCycleReregisterComparesClean(t *testing.T) {
	graph := buildRegisteredGraph(t, [][]string{
		{"old"},
		{"m1", "old"},
		{"t", "m1"},
		{"down", "t"},
		{"newroot"},
		{"new", "newroot"}, // legal new source, registered with its own ancestor
		{"lone"},
	})
	assertConsistent(t, graph)

	beforeText := mustExport(t, graph, "t")
	if got, want := exportEdgeNames(parseExport(t, beforeText)), [][2]string{
		{"m1", "t"},
		{"old", "m1"},
	}; !reflect.DeepEqual(got, want) {
		t.Fatalf("before export edges = %v, want %v", got, want)
	}
	saved := snapshotExportGraph(graph)

	// Pointing m1 at its own downstream t closes old -> m1 -> t -> m1; the legal
	// source new in the same request must not make the cycle acceptable. The
	// offending upstream is reported in either list position.
	for _, parents := range [][]string{{"new", "t"}, {"t", "new"}} {
		err := Register(graph, Dataset{Name: "m1"}, parents)
		if err == nil {
			t.Fatalf("Register(m1, %v) must be rejected", parents)
		}
		if !strings.Contains(err.Error(), "cycle") || !strings.Contains(err.Error(), "t") {
			t.Errorf("Register(m1, %v) error = %q, want a cycle error naming t", parents, err.Error())
		}
		// No partial application: m1 keeps old, never gains new, and every other
		// node and list order is exactly as saved before the request.
		assertGraphUnchanged(t, saved, graph)
		assertEntry(t, graph, "m1", []string{"old"}, []string{"t"})
		assertEntry(t, graph, "t", []string{"m1"}, []string{"down"})
		assertEntry(t, graph, "new", []string{"newroot"}, nil)
		assertEntry(t, graph, "old", nil, []string{"m1"})
	}
	assertConsistent(t, graph)

	// The export after the rejected attempts is byte-identical to the one saved
	// before them.
	afterText := mustExport(t, graph, "t")
	if afterText != beforeText {
		t.Fatalf("target export changed after rejected re-register:\nbefore: %s\nafter:  %s",
			beforeText, afterText)
	}
	if strings.Contains(afterText, "new") {
		t.Fatalf("rejected request leaked new into the export: %s", afterText)
	}

	diff, err := CompareUpstreamLineage(beforeText, afterText, "t")
	if err != nil {
		t.Fatalf("CompareUpstreamLineage on unchanged exports: %v", err)
	}
	if diff.Added == nil || diff.Removed == nil {
		t.Fatalf("want two non-nil empty lists, got %+v", diff)
	}
	if len(diff.Added) != 0 || len(diff.Removed) != 0 {
		t.Fatalf("rejected replacement must produce no difference, got added=%v removed=%v",
			depPairs(diff.Added), depPairs(diff.Removed))
	}
}
