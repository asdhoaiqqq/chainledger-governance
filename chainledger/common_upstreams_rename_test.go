package chainledger

import (
	"reflect"
	"slices"
	"strings"
	"testing"
)

// buildDiamondShortcutGraph builds the lineage used by the common-upstreams
// rename regressions:
//
//	raw ──> shared ──> a ──> z ─┐
//	 │         └──> b ──> c ───┤
//	 ├─────────────────────────> left   (raw -> left direct, distance 1)
//	 ├─────────────────────────> right  (raw -> right direct, distance 1)
//	 └──> shared ──────────────> right  (shared -> right direct)
//
// left derives from shared over two equal length-3 routes (shared -> a -> z ->
// left and shared -> b -> c -> left); right depends on shared directly AND on
// raw directly. Both targets also depend on raw directly, so raw reaches both
// targets in one hop; it must nevertheless stay hidden behind shared, which is
// the later common source, and hidden even behind the length-3 branch nodes
// (a/z and b/c) that are common as closures but are not frontier points because
// they sit on a common chain raw -> shared -> a -> z -> left with shared
// downstream of raw and a downstream of shared.
//
// Registration order and direct-upstream list order are deliberately made to
// disagree with the tie-break: the b/c branch is registered first, and left's
// declared upstreams list c before z (so a rule comparing only left's direct
// upstreams would wrongly pick the b/c route; the full-path rule compares name
// by name from shared, where a < b).
func buildDiamondShortcutGraph(t *testing.T) [][]string {
	t.Helper()
	return [][]string{
		{"raw"},
		{"shared", "raw"},
		{"b", "shared"}, // b/c branch registered before a/z on purpose
		{"c", "b"},
		{"a", "shared"},
		{"z", "a"},
		{"right", "raw", "shared"},
		{"left", "raw", "c", "z"}, // c ahead of z on purpose
	}
}

// Regression: comparing left and right of the diamond-with-shortcuts graph
// reports shared alone — raw is a common source over the direct edges (and
// reaches both in one hop) but is hidden behind shared, a later common source;
// distance never rescues it. shared is three edges from left (tie-broken from
// the source over the a/z branch because a < b) and one edge from right.
//
// This is the baseline every later check in this file is anchored against:
// rename changes no edge, so the frontier identity and both shortest distances
// must survive the rename; only the name-dependent explanation path can flip.
func TestCommonUpstreamsDiamondShortcutOnlySharedFrontier(t *testing.T) {
	graph := buildRegisteredGraph(t, buildDiamondShortcutGraph(t))
	assertConsistent(t, graph)

	found := mustCommon(t, graph, "left", "right")
	if got, want := commonNames(found), []string{"shared"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("common sources = %v, want %v (raw must be hidden behind shared "+
			"even though it reaches both targets directly)", got, want)
	}
	assertEachCommonOnce(t, found)
	assertCommon(t, found, "shared",
		3, []string{"shared", "a", "z", "left"},
		1, []string{"shared", "right"})

	// Sanity anchors on the stored orders: they disagree with the chosen route
	// and with name order, proving the choice follows the current names rather
	// than registration history or the direct-upstream list.
	assertEntry(t, graph, "left", []string{"raw", "c", "z"}, nil)
	assertEntry(t, graph, "shared", []string{"raw"}, []string{"b", "a", "right"})
	assertEntry(t, graph, "raw", nil, []string{"shared", "right", "left"})

	// raw is common by closure (it reaches both targets) but is never reported:
	// a later common source (shared) stands between it and both targets.
	for _, c := range found {
		if c.Dataset == "raw" {
			t.Fatalf("raw leaked into frontier %v despite direct one-hop routes", found)
		}
	}

	// A failed common-upstream query is also read-only.
	before := snapshot(graph)
	if _, err := CommonUpstreams(graph, "left", "ghost"); err == nil ||
		!strings.Contains(err.Error(), "ghost") {
		t.Fatalf("unknown second target: want error naming ghost, got %v", err)
	}
	if !reflect.DeepEqual(snapshot(graph), before) {
		t.Fatalf("failed query changed graph: before=%v after=%v", before, snapshot(graph))
	}
}

// Order invariance: two builds of the same lineage with shuffled registration
// and direct-upstream orders must agree exactly, and the explanation of the
// shared source toward left must still run through a/z.
func TestCommonUpstreamsDiamondShortcutOrderInvariance(t *testing.T) {
	first := buildRegisteredGraph(t, buildDiamondShortcutGraph(t))
	second := buildRegisteredGraph(t, [][]string{
		{"raw"},
		{"shared", "raw"},
		{"a", "shared"}, // a/z branch first this time; raw's children get left before right
		{"z", "a"},
		{"b", "shared"},
		{"c", "b"},
		{"left", "z", "raw", "c"}, // direct-upstream list flipped vs build 1
		{"right", "shared", "raw"},
	})

	want := mustCommon(t, first, "left", "right")
	if got := mustCommon(t, second, "left", "right"); !reflect.DeepEqual(got, want) {
		t.Fatalf("equivalent graphs with shuffled orders differ: got %v want %v", got, want)
	}
	if got, want := commonNames(want), []string{"shared"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("common sources = %v, want %v", got, want)
	}
	assertCommon(t, want, "shared",
		3, []string{"shared", "a", "z", "left"},
		1, []string{"shared", "right"})
}

// Regression: renaming a (inside the a/z branch) to m — a name that sorts past
// b — changes no dependency edge, so the frontier stays shared alone and both
// shortest distances stay 3 (toward left) and 1 (toward right). Because names
// participate in the explanation-path comparison, the length-3 route toward
// left is re-chosen and now runs through b/c; the right-side explanation is
// untouched and stays the direct shared -> right edge.
//
// Paths are written from the source toward each target on both sides — never
// just chosen by comparing left's direct upstreams (c < z would keep a/z
// wrongly; the flip can only be observed by comparing from shared).
func TestCommonUpstreamsRenameFlipsLeftPathNotFrontierOrDistances(t *testing.T) {
	graph := buildRegisteredGraph(t, buildDiamondShortcutGraph(t))
	assertConsistent(t, graph)

	before := mustCommon(t, graph, "left", "right")
	if got, want := commonNames(before), []string{"shared"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("before rename: common sources = %v, want %v", got, want)
	}
	assertCommon(t, before, "shared",
		3, []string{"shared", "a", "z", "left"},
		1, []string{"shared", "right"})
	beforeCopy := cloneCommons(before)

	mustRename(t, graph, "a", "m")
	assertConsistent(t, graph)

	// The rename is a pure in-place name swap: no node count change and no edge
	// or ordering change beyond the rewired references to a.
	if got, want := len(graph), 8; got != want {
		t.Fatalf("dataset count after rename = %d, want %d", got, want)
	}
	if _, ok := graph["a"]; ok {
		t.Error("old name a still present after rename")
	}
	assertEntry(t, graph, "m", []string{"shared"}, []string{"z"})
	assertEntry(t, graph, "shared", []string{"raw"}, []string{"b", "m", "right"})
	assertEntry(t, graph, "z", []string{"m"}, []string{"left"})
	assertEntry(t, graph, "b", []string{"shared"}, []string{"c"})
	assertEntry(t, graph, "c", []string{"b"}, []string{"left"})
	assertEntry(t, graph, "left", []string{"raw", "c", "z"}, nil)
	assertEntry(t, graph, "right", []string{"raw", "shared"}, nil)

	after := mustCommon(t, graph, "left", "right")
	if got, want := commonNames(after), []string{"shared"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("after rename: common sources = %v, want %v (frontier identity unchanged)", got, want)
	}
	assertEachCommonOnce(t, after)
	// Identities and distances unchanged; only the left explanation flips to
	// the b/c route because b < m at hop 1; right keeps its direct edge.
	assertCommon(t, after, "shared",
		3, []string{"shared", "b", "c", "left"},
		1, []string{"shared", "right"})

	// The old name appears nowhere in the post-rename result; the renamed node
	// is explained under its current name.
	for _, c := range after {
		if c.Dataset == "a" {
			t.Fatalf("old name a leaked into post-rename common source list %v", after)
		}
		for _, step := range append(append([]string(nil), c.PathToFirst...), c.PathToSecond...) {
			if step == "a" {
				t.Fatalf("old name a leaked into post-rename path %+v", c)
			}
		}
	}

	// Swapping the targets swaps the two sides field by field under the current
	// names; paths still run source -> target.
	swapped := mustCommon(t, graph, "right", "left")
	assertCommon(t, swapped, "shared",
		1, []string{"shared", "right"},
		3, []string{"shared", "b", "c", "left"})

	// The result taken before the rename kept the a/z explanation and was not
	// rewritten by the rename or by the later queries.
	if !reflect.DeepEqual(before, beforeCopy) {
		t.Fatalf("pre-rename result rewritten after rename/later queries: before=%v snapshot=%v",
			before, beforeCopy)
	}
}

// Regression: a rename rejected because the new name is already taken must
// name the occupied dataset and leave every node, edge and stored list order
// exactly as they were. Re-comparing left and right afterwards returns the
// same sole source, distances and explanation paths record for record.
func TestCommonUpstreamsRejectedRenameKeepsGraphAndSources(t *testing.T) {
	graph := buildRegisteredGraph(t, buildDiamondShortcutGraph(t))
	assertConsistent(t, graph)

	before := mustCommon(t, graph, "left", "right")
	if got, want := commonNames(before), []string{"shared"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("before rejection: common sources = %v, want %v", got, want)
	}
	assertCommon(t, before, "shared",
		3, []string{"shared", "a", "z", "left"},
		1, []string{"shared", "right"})
	graphBefore := snapshot(graph)

	// b is already registered to the sibling branch node: the rename is refused
	// with an error naming the taken name b.
	err := Rename(graph, "a", "b")
	if err == nil || !strings.Contains(err.Error(), "b") ||
		!strings.Contains(err.Error(), "already in use") {
		t.Fatalf("want name-in-use error naming b, got %v", err)
	}

	// The graph is byte-for-byte what it was: node set, edges and list orders.
	if !reflect.DeepEqual(snapshot(graph), graphBefore) {
		t.Fatalf("rejected rename changed graph: before=%v after=%v", graphBefore, snapshot(graph))
	}
	assertEntry(t, graph, "a", []string{"shared"}, []string{"z"})
	assertEntry(t, graph, "b", []string{"shared"}, []string{"c"})
	assertEntry(t, graph, "shared", []string{"raw"}, []string{"b", "a", "right"})
	assertEntry(t, graph, "left", []string{"raw", "c", "z"}, nil)
	assertEntry(t, graph, "right", []string{"raw", "shared"}, nil)
	assertConsistent(t, graph)

	// Re-querying the same pair returns the pre-request result record for
	// record: shared alone with the a/z explanation and distances 3 and 1.
	after := mustCommon(t, graph, "left", "right")
	if !reflect.DeepEqual(after, before) {
		t.Fatalf("common sources after rejected rename = %v, want unchanged %v", after, before)
	}

	// The taken name still answers to its own identity: renaming must not have
	// merged or detached anything.
	if got := mustCommon(t, graph, "b", "c"); len(got) != 1 ||
		got[0].Dataset != "b" {
		t.Fatalf("b vs c after rejection: want sole common source b, got %v", got)
	}
}

// Read-only guarantee across the whole rename/query sequence: successful and
// failed CommonUpstreams calls change no node, relationship or stored list
// order, and a fresh query after all of it reproduces the current-name result
// with no stale names.
func TestCommonUpstreamsRenameSequenceQueriesReadOnly(t *testing.T) {
	graph := buildRegisteredGraph(t, buildDiamondShortcutGraph(t))
	assertConsistent(t, graph)

	// Query once before the rename, then snapshot the graph and run a mix of
	// successful and failing queries; none of it may move the graph.
	_ = mustCommon(t, graph, "left", "right")
	before := snapshot(graph)

	if _, err := CommonUpstreams(graph, "", "right"); err == nil ||
		!strings.Contains(err.Error(), "name is required") {
		t.Fatalf("empty first target: want required-name error, got %v", err)
	}
	if _, err := CommonUpstreams(graph, "left", "ghost"); err == nil ||
		!strings.Contains(err.Error(), "ghost") {
		t.Fatalf("unknown second target: want error naming ghost, got %v", err)
	}
	if !reflect.DeepEqual(snapshot(graph), before) {
		t.Fatalf("pre-rename queries changed graph: before=%v after=%v", before, snapshot(graph))
	}

	mustRename(t, graph, "a", "m")
	afterRename := snapshot(graph)

	_ = mustCommon(t, graph, "left", "right")
	_ = mustCommon(t, graph, "right", "left")
	if _, err := CommonUpstreams(graph, "ghost", "left"); err == nil ||
		!strings.Contains(err.Error(), "ghost") {
		t.Fatalf("unknown first target: want error naming ghost, got %v", err)
	}
	if !reflect.DeepEqual(snapshot(graph), afterRename) {
		t.Fatalf("post-rename queries changed graph: before=%v after=%v", afterRename, snapshot(graph))
	}

	// A rejected rename after the successful one is equally non-mutating, and a
	// subsequent query still sees the renamed graph.
	if err := Rename(graph, "m", "c"); err == nil ||
		!strings.Contains(err.Error(), "c") {
		t.Fatalf("want name-in-use error naming c, got %v", err)
	}
	if !reflect.DeepEqual(snapshot(graph), afterRename) {
		t.Fatalf("rejected rename changed graph: before=%v after=%v", afterRename, snapshot(graph))
	}

	fresh := mustCommon(t, graph, "left", "right")
	if got, want := commonNames(fresh), []string{"shared"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("fresh query common sources = %v, want %v", got, want)
	}
	assertCommon(t, fresh, "shared",
		3, []string{"shared", "b", "c", "left"},
		1, []string{"shared", "right"})

	// No stale old name survives anywhere in a fresh query's paths.
	for _, c := range fresh {
		if slices.Contains(append(append([]string(nil), c.PathToFirst...), c.PathToSecond...), "a") {
			t.Fatalf("stale name a survived in fresh query %+v", c)
		}
	}
}
