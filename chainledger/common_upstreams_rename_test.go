package chainledger

import (
	"reflect"
	"strings"
	"testing"
)

// buildCommonUpstreamsRenameGraph builds the lineage used by the
// common-upstreams rename regressions:
//
//	raw ──> shared ──> a ──> z ──┐
//	  │             └─> b ──> c ─┼─> left
//	  └──────────────────────────┘
//	  └──────────────────────────> right <── shared
//
// raw derives shared; shared derives left along two equal-length, three-edge
// routes (through a -> z and b -> c) and derives right directly; both targets
// additionally depend on raw directly. Comparing left with right must report
// shared alone: raw reaches both targets by a shorter direct route, but it sits
// upstream of another common source (shared) and is therefore hidden.
//
// The b-side branch is registered first and left declares c ahead of z with
// raw last, so the a/z explanation can only come from comparing the full path
// name by name from the source (a < b decides at hop 1) — a rule comparing
// only the target's direct upstreams would see c < z and pick the b/c branch.
func buildCommonUpstreamsRenameGraph(t *testing.T) map[string]*Lineage {
	t.Helper()
	return buildRegisteredGraph(t, [][]string{
		{"raw"},
		{"shared", "raw"},
		{"b", "shared"}, // b-side branch registered first on purpose
		{"c", "b"},
		{"a", "shared"},
		{"z", "a"},
		{"left", "c", "z", "raw"}, // c ahead of z, raw last
		{"right", "raw", "shared"},
	})
}

// Regression for a rename that changes neither dependency edges nor distances
// but does change which equal-length explanation path a re-query picks.
//
// Before the rename, CommonUpstreams(left, right) reports shared alone even
// though raw is a direct upstream of both targets: raw is hidden behind the
// later common source shared. shared is three edges from left, explained along
// shared -> a -> z -> left because a < b at hop 1, and one edge from right on
// the direct shared -> right edge. Renaming a to m touches no edge, so the
// common-source identity and both distances survive, but m now sorts past b
// and the left explanation flips to shared -> b -> c -> left; the right side
// stays the direct edge. Both paths are still written from the source toward
// the target. A result fetched before the rename keeps its a/z explanation,
// and the post-rename query carries no trace of the old name.
func TestCommonUpstreamsRenameFlipsExplanationRouteKeepsSharedSource(t *testing.T) {
	graph := buildCommonUpstreamsRenameGraph(t)
	assertConsistent(t, graph)

	before := mustCommon(t, graph, "left", "right")
	if got, want := commonNames(before), []string{"shared"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("common sources = %v, want %v (raw must stay hidden behind shared despite direct raw edges)",
			got, want)
	}
	assertEachCommonOnce(t, before)
	// Distances (3, 1) and the from-source paths: the a/z branch wins at hop 1
	// (a < b), not at left's direct-upstream comparison where c < z; right is
	// reached over the single shared edge.
	assertCommon(t, before, "shared",
		3, []string{"shared", "a", "z", "left"},
		1, []string{"shared", "right"})

	// Registration order and stored direct-upstream list order must not decide
	// any of it: a build with the branches registered and declared in opposite
	// orders returns the exact same record.
	flipped := buildRegisteredGraph(t, [][]string{
		{"raw"},
		{"shared", "raw"},
		{"a", "shared"},
		{"z", "a"},
		{"b", "shared"},
		{"c", "b"},
		{"right", "shared", "raw"}, // list order flipped
		{"left", "raw", "z", "c"},  // branches and list order flipped
	})
	if got := mustCommon(t, flipped, "left", "right"); !reflect.DeepEqual(got, before) {
		t.Fatalf("equivalent graphs disagree: %v vs %v", got, before)
	}

	beforeCopy := cloneCommons(before)

	// Rename the dataset inside the a/z branch: m sorts past b, so the other
	// three-edge route now wins the from-the-source comparison. No edge moves.
	mustRename(t, graph, "a", "m")
	assertConsistent(t, graph)

	// Only the in-place name swap happened: m occupies a's old slot in every
	// neighbor list, and every other list keeps its order.
	if _, ok := graph["a"]; ok {
		t.Error("old name a still present in graph")
	}
	if got, want := len(graph), 8; got != want {
		t.Fatalf("dataset count = %d, want %d", got, want)
	}
	assertEntry(t, graph, "shared", []string{"raw"}, []string{"b", "m", "right"})
	assertEntry(t, graph, "m", []string{"shared"}, []string{"z"})
	assertEntry(t, graph, "z", []string{"m"}, []string{"left"})
	assertEntry(t, graph, "b", []string{"shared"}, []string{"c"})
	assertEntry(t, graph, "c", []string{"b"}, []string{"left"})
	assertEntry(t, graph, "left", []string{"c", "z", "raw"}, nil)
	assertEntry(t, graph, "right", []string{"raw", "shared"}, nil)
	assertEntry(t, graph, "raw", nil, []string{"shared", "left", "right"})

	graphBeforeQuery := snapshot(graph)

	// Re-query the same pair: same sole source, same distances; only the left
	// explanation moved to the b/c branch, and the right path is untouched.
	after := mustCommon(t, graph, "left", "right")
	if got, want := commonNames(after), []string{"shared"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("after rename common sources = %v, want %v (identity unchanged, raw still hidden)",
			got, want)
	}
	assertEachCommonOnce(t, after)
	assertCommon(t, after, "shared",
		3, []string{"shared", "b", "c", "left"},
		1, []string{"shared", "right"})

	// The old name survives nowhere in the new result: not as a source and in
	// neither explanation path, and it is unregistered as a query target.
	for _, c := range after {
		if c.Dataset == "a" {
			t.Fatalf("old name a leaked into post-rename result: %+v", c)
		}
		for _, step := range append(append([]string(nil), c.PathToFirst...), c.PathToSecond...) {
			if step == "a" {
				t.Fatalf("old name a leaked into post-rename explanation: %+v", c)
			}
		}
	}
	if _, err := CommonUpstreams(graph, "a", "right"); err == nil ||
		!strings.Contains(err.Error(), "a") {
		t.Fatalf("CommonUpstreams(a, right) after rename: want not-found error naming a, got %v", err)
	}
	if _, err := CommonUpstreams(graph, "left", "a"); err == nil ||
		!strings.Contains(err.Error(), "a") {
		t.Fatalf("CommonUpstreams(left, a) after rename: want not-found error naming a, got %v", err)
	}

	// Successful and failing common-upstream queries only read lineage: no
	// node, edge or stored list order changed.
	if !reflect.DeepEqual(snapshot(graph), graphBeforeQuery) {
		t.Fatalf("queries changed graph: before=%v after=%v", graphBeforeQuery, snapshot(graph))
	}
	assertConsistent(t, graph)

	// The result obtained before the rename kept its original a/z explanation;
	// neither the rename nor the later queries rewrote it.
	if !reflect.DeepEqual(before, beforeCopy) {
		t.Fatalf("pre-rename result changed after rename/new query: before=%v snapshot=%v",
			before, beforeCopy)
	}
}

// Regression for the rejected rename interacting with a common-upstreams query.
// Renaming a to the already-registered name b is refused with an error naming
// b; the graph — nodes, dependency edges and every stored list order — is
// preserved exactly, so re-comparing left with right returns the same sole
// source shared with the same distances and the same a/z explanation as before
// the refused request.
func TestCommonUpstreamsRejectedRenameKeepsCommonSources(t *testing.T) {
	graph := buildCommonUpstreamsRenameGraph(t)
	assertConsistent(t, graph)

	before := mustCommon(t, graph, "left", "right")
	if got, want := commonNames(before), []string{"shared"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("before rejection: common sources = %v, want %v", got, want)
	}
	assertCommon(t, before, "shared",
		3, []string{"shared", "a", "z", "left"},
		1, []string{"shared", "right"})
	graphBefore := snapshot(graph)

	// b is already registered to the other three-edge branch: the rename must
	// be refused with an error stating b is taken.
	err := Rename(graph, "a", "b")
	if err == nil || !strings.Contains(err.Error(), "already in use") ||
		!strings.Contains(err.Error(), "b") {
		t.Fatalf("want name-in-use error naming b, got %v", err)
	}

	// Nothing moved: the old dataset still exists, the dataset count is
	// unchanged, and relationships plus stored list orders are exactly as
	// before the request.
	if _, ok := graph["a"]; !ok {
		t.Error("dataset a lost after rejected rename")
	}
	if got, want := len(graph), 8; got != want {
		t.Fatalf("dataset count = %d, want %d", got, want)
	}
	if !reflect.DeepEqual(snapshot(graph), graphBefore) {
		t.Fatalf("rejected rename changed graph: before=%v after=%v", graphBefore, snapshot(graph))
	}
	assertEntry(t, graph, "a", []string{"shared"}, []string{"z"})
	assertEntry(t, graph, "b", []string{"shared"}, []string{"c"})
	assertEntry(t, graph, "shared", []string{"raw"}, []string{"b", "a", "right"})
	assertEntry(t, graph, "left", []string{"c", "z", "raw"}, nil)
	assertEntry(t, graph, "right", []string{"raw", "shared"}, nil)
	assertEntry(t, graph, "raw", nil, []string{"shared", "left", "right"})
	assertConsistent(t, graph)

	// Re-comparing the same pair returns source, distances and paths record
	// for record, including the a/z explanation that the refused rename must
	// not have disturbed.
	after := mustCommon(t, graph, "left", "right")
	if !reflect.DeepEqual(after, before) {
		t.Fatalf("common sources after rejected rename = %v, want unchanged %v", after, before)
	}
	assertCommon(t, after, "shared",
		3, []string{"shared", "a", "z", "left"},
		1, []string{"shared", "right"})

	// The query itself changed nothing either.
	if !reflect.DeepEqual(snapshot(graph), graphBefore) {
		t.Fatalf("query changed graph: before=%v after=%v", graphBefore, snapshot(graph))
	}
}
