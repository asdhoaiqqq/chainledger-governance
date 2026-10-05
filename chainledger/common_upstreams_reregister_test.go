package chainledger

import (
	"reflect"
	"strings"
	"testing"
)

// cloneCommons deep-copies a CommonUpstreams result so later graph changes or
// queries can be checked against the content as it was originally returned.
func cloneCommons(found []CommonUpstream) []CommonUpstream {
	cloned := make([]CommonUpstream, len(found))
	for i, c := range found {
		cloned[i] = CommonUpstream{
			Dataset:          c.Dataset,
			DistanceToFirst:  c.DistanceToFirst,
			PathToFirst:      append([]string(nil), c.PathToFirst...),
			DistanceToSecond: c.DistanceToSecond,
			PathToSecond:     append([]string(nil), c.PathToSecond...),
		}
	}
	return cloned
}

// Regression: re-registering one target so a later common source no longer
// takes part in that target's derivation must drop the later source from the
// common frontier and let the shared ancestor behind it reappear — as long as
// the ancestor still derives the re-registered target over another route.
//
//	raw ──> mid ──────────────> right
//	 │       └──────╳ (dropped) left
//	 ├──> zb ──> z2 ──────────> left
//	 └──> ab ──> a2 ──────────> left
//
// Before the replacement, mid is a later common source of left and right and
// hides raw. Re-registering left with only z2 and a2 removes mid from left's
// lineage entirely, while raw still derives left through raw -> zb -> z2 and
// raw -> ab -> a2. mid stays registered and still derives right, but being in
// the graph is not being common: the new frontier is raw alone. The removed
// raw -> mid -> left route (length 2) must not be reused: raw is distance 3
// from left, explained by the lexicographically smallest length-3 route
// (ab < zb decides at hop 1), and distance 2 from right via mid.
func TestCommonUpstreamsReregisterDropsLaterSourceAncestorReappears(t *testing.T) {
	graph := buildRegisteredGraph(t, [][]string{
		{"raw"},
		{"mid", "raw"},
		{"zb", "raw"}, // zb-side branch registered before ab-side on purpose
		{"ab", "raw"},
		{"z2", "zb"},
		{"a2", "ab"},
		{"left", "mid", "z2", "a2"},
		{"right", "mid"},
	})
	assertConsistent(t, graph)

	// Before the replacement: mid is the sole frontier source, raw hidden.
	before := mustCommon(t, graph, "left", "right")
	if got, want := commonNames(before), []string{"mid"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("before replacement: common sources = %v, want %v (raw hidden behind mid)", got, want)
	}
	assertCommon(t, before, "mid", 1, []string{"mid", "left"}, 1, []string{"mid", "right"})
	beforeCopy := cloneCommons(before)

	// Replace left's direct upstreams: mid leaves left's derivation, the two
	// raw routes through z2 and a2 survive. right is not part of the request.
	mustRegister(t, graph, "left", "z2", "a2")
	assertConsistent(t, graph)
	// mid is still registered and still derives right; only the left edge is
	// gone. Stored list orders everywhere else are untouched.
	assertEntry(t, graph, "left", []string{"z2", "a2"}, nil)
	assertEntry(t, graph, "right", []string{"mid"}, nil)
	assertEntry(t, graph, "mid", []string{"raw"}, []string{"right"})
	assertEntry(t, graph, "raw", nil, []string{"mid", "zb", "ab"})
	graphBeforeQuery := snapshot(graph)

	// After the replacement: mid is no longer common (it reaches right only),
	// so it leaves the result even though it is still registered; raw reappears
	// because no common source stands between raw and the two targets anymore.
	after := mustCommon(t, graph, "left", "right")
	if got, want := commonNames(after), []string{"raw"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("after replacement: common sources = %v, want %v "+
			"(mid must exit although still registered, raw must reappear)", got, want)
	}
	assertEachCommonOnce(t, after)
	// Distances and paths follow the current edges only: the dropped
	// raw -> mid -> left route (length 2) must not shorten or explain the left
	// side, and of the two surviving length-3 routes the ab one wins at hop 1.
	assertCommon(t, after, "raw",
		3, []string{"raw", "ab", "a2", "left"},
		2, []string{"raw", "mid", "right"})

	// mid still derives right, so comparing mid with right keeps working and
	// reports mid itself — proof the dataset was not unregistered, only dropped
	// from left's lineage.
	stillThere := mustCommon(t, graph, "mid", "right")
	if got, want := commonNames(stillThere), []string{"mid"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("mid vs right: common sources = %v, want %v (mid still registered)", got, want)
	}

	// Successful and failed queries are read-only: no node, edge or stored
	// list order changes.
	if _, err := CommonUpstreams(graph, "left", "ghost"); err == nil ||
		!strings.Contains(err.Error(), "ghost") {
		t.Fatalf("unknown second target: want error naming ghost, got %v", err)
	}
	if !reflect.DeepEqual(snapshot(graph), graphBeforeQuery) {
		t.Fatalf("queries changed graph: before=%v after=%v", graphBeforeQuery, snapshot(graph))
	}

	// The result obtained before the replacement kept its original sources,
	// distances and paths; only the new query reflects the new edges.
	if !reflect.DeepEqual(before, beforeCopy) {
		t.Fatalf("earlier result changed after re-register: before=%v snapshot=%v", before, beforeCopy)
	}
}

// Regression: when another later source still reaches both targets after the
// replacement, the shared ancestor stays hidden behind it and the surviving
// later source is still returned, with each side's distance and path computed
// over the remaining edges.
//
//	raw ──> late ─────────────> right
//	 │       └──────╳ (dropped) left
//	 └──> bridge ─────────────> left
//	       └──────> hub ──────> right
//
// Before the replacement, late and bridge are both frontier sources and hide
// raw. Re-registering left with only bridge drops late from left's lineage:
// late exits the result (although it still derives right), while bridge stays
// and keeps raw hidden — bridge is distance 1 from left directly, distance 2
// from right through hub.
func TestCommonUpstreamsReregisterLaterSourceRemainsAncestorStaysHidden(t *testing.T) {
	graph := buildRegisteredGraph(t, [][]string{
		{"raw"},
		{"late", "raw"},
		{"bridge", "raw"},
		{"hub", "bridge"},
		{"left", "late", "bridge"},
		{"right", "late", "hub"},
	})
	assertConsistent(t, graph)

	// Before: two frontier sources, ordered by source name; raw hidden behind
	// both even though raw also derives each target.
	before := mustCommon(t, graph, "left", "right")
	if got, want := commonNames(before), []string{"bridge", "late"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("before replacement: common sources = %v, want %v (raw hidden)", got, want)
	}
	assertEachCommonOnce(t, before)
	assertCommon(t, before, "bridge", 1, []string{"bridge", "left"}, 2, []string{"bridge", "hub", "right"})
	assertCommon(t, before, "late", 1, []string{"late", "left"}, 1, []string{"late", "right"})
	beforeCopy := cloneCommons(before)

	// Drop late from left's derivation; the bridge route survives untouched.
	mustRegister(t, graph, "left", "bridge")
	assertConsistent(t, graph)
	assertEntry(t, graph, "left", []string{"bridge"}, nil)
	assertEntry(t, graph, "late", []string{"raw"}, []string{"right"})
	graphBeforeQuery := snapshot(graph)

	// After: late is upstream of right alone and exits; bridge remains the sole
	// frontier source, so raw stays hidden behind it.
	after := mustCommon(t, graph, "left", "right")
	if got, want := commonNames(after), []string{"bridge"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("after replacement: common sources = %v, want %v "+
			"(late must exit, raw must stay hidden behind bridge)", got, want)
	}
	assertEachCommonOnce(t, after)
	assertCommon(t, after, "bridge",
		1, []string{"bridge", "left"},
		2, []string{"bridge", "hub", "right"})

	// Swapping the query swaps the two sides field by field under the same
	// surviving edges.
	swapped := mustCommon(t, graph, "right", "left")
	assertCommon(t, swapped, "bridge",
		2, []string{"bridge", "hub", "right"},
		1, []string{"bridge", "left"})

	if !reflect.DeepEqual(snapshot(graph), graphBeforeQuery) {
		t.Fatalf("queries changed graph: before=%v after=%v", graphBeforeQuery, snapshot(graph))
	}
	if !reflect.DeepEqual(before, beforeCopy) {
		t.Fatalf("earlier result changed after re-register: before=%v snapshot=%v", before, beforeCopy)
	}
}

// Regression: a replacement request naming an unregistered upstream is
// rejected with an error naming it, and afterwards the common-upstream query
// still returns exactly the pre-replacement sources, distances and paths; the
// graph's relationships and stored list orders are untouched.
func TestCommonUpstreamsRejectedReregisterKeepsCommonSources(t *testing.T) {
	graph := buildRegisteredGraph(t, [][]string{
		{"raw"},
		{"mid", "raw"},
		{"alt", "raw"},
		{"left", "mid", "alt"},
		{"right", "mid"},
	})
	assertConsistent(t, graph)

	before := mustCommon(t, graph, "left", "right")
	if got, want := commonNames(before), []string{"mid"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("before rejection: common sources = %v, want %v", got, want)
	}
	assertCommon(t, before, "mid", 1, []string{"mid", "left"}, 1, []string{"mid", "right"})
	graphBefore := snapshot(graph)

	// A valid upstream followed by an unregistered one: the whole request is
	// refused and the error names the unknown dataset.
	err := Register(graph, Dataset{Name: "left"}, []string{"alt", "ghost"})
	if err == nil || !strings.Contains(err.Error(), "ghost") {
		t.Fatalf("want unknown-parent error naming ghost, got %v", err)
	}

	// Nothing moved: relationships and stored list orders are exactly as before.
	if !reflect.DeepEqual(snapshot(graph), graphBefore) {
		t.Fatalf("rejected replacement changed graph: before=%v after=%v", graphBefore, snapshot(graph))
	}
	assertEntry(t, graph, "left", []string{"mid", "alt"}, nil)
	assertEntry(t, graph, "mid", []string{"raw"}, []string{"left", "right"})
	assertConsistent(t, graph)

	// The query still returns the pre-replacement frontier, distances and
	// paths, record for record.
	after := mustCommon(t, graph, "left", "right")
	if !reflect.DeepEqual(after, before) {
		t.Fatalf("common sources after rejected replacement = %v, want unchanged %v", after, before)
	}
}
