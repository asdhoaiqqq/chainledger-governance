package chainledger

import (
	"reflect"
	"testing"
)

// Regression: a caller may rewrite one returned explanation path for display —
// rename a name inside it or append a marker at the end — but such an edit
// belongs to the caller's own path alone. The same record's other side, every
// other record from the same query, the lineage graph, results already handed
// out, and later queries must all keep the content they had at query time.
//
// The fixture has two nearest common sources whose paths to the first target
// converge on the same trailing segment, while the two sides differ in
// distance and route:
//
//	base ──> s1 ──> mixL ──> left        s1 ────────────────> right
//	  └──> s2 ──> mixL                   s2 ──> w ──> right
//	        └──> v ───────────────────────────────> right
//
// base is common by closure but hidden behind the later common sources s1 and
// s2, so the frontier is exactly [s1 s2]. Both sources explain left through
// the shared tail [mixL left]; s1 reaches right in one hop while s2 reaches it
// in two, and s2's two equal-length routes are tie-broken to the v route
// (v < w at hop 1).
func TestCommonUpstreamsEditedPathIsolatedFromSiblingResults(t *testing.T) {
	graph := buildRegisteredGraph(t, [][]string{
		{"base"},
		{"s1", "base"},
		{"s2", "base"},
		{"mixL", "s1", "s2"},
		{"w", "s2"},
		{"v", "s2"},
		{"left", "mixL"},
		{"right", "s1", "w", "v"},
	})
	before := snapshot(graph)

	// Another result handed out before any edit: it must survive the caller's
	// edits to the later result untouched.
	prior := mustCommon(t, graph, "left", "right")
	priorCopy := cloneCommons(prior)

	found := mustCommon(t, graph, "left", "right")
	if got, want := commonNames(found), []string{"s1", "s2"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("common sources = %v, want %v (base must stay hidden behind s1 and s2)", got, want)
	}
	assertEachCommonOnce(t, found)
	assertCommon(t, found, "s1", 2, []string{"s1", "mixL", "left"}, 1, []string{"s1", "right"})
	assertCommon(t, found, "s2", 2, []string{"s2", "mixL", "left"}, 2, []string{"s2", "v", "right"})
	original := cloneCommons(found)

	// Display edits on the first-target side of s1's record: rewrite the source
	// name, the shared trailing name mixL, and the target name, then append a
	// display marker. The same names sit in s1's other side and in s2's record.
	found[0].PathToFirst[0] = "s1 (renamed)"
	found[0].PathToFirst[1] = "mixL (renamed)"
	found[0].PathToFirst[2] = "left (renamed)"
	found[0].PathToFirst = append(found[0].PathToFirst, "★")

	// Display edits on the second-target side of s2's record: rewrite the route
	// and target names, then append a marker. right also ends s1's other side.
	found[1].PathToSecond[1] = "v (renamed)"
	found[1].PathToSecond[2] = "right (renamed)"
	found[1].PathToSecond = append(found[1].PathToSecond, "★")

	// The edits took effect on exactly the two edited paths and nowhere else:
	// every unedited path keeps its original names in order, and source names,
	// distances and record order are unchanged.
	want := cloneCommons(original)
	want[0].PathToFirst = []string{"s1 (renamed)", "mixL (renamed)", "left (renamed)", "★"}
	want[1].PathToSecond = []string{"s2", "v (renamed)", "right (renamed)", "★"}
	if !reflect.DeepEqual(found, want) {
		t.Fatalf("edit leaked outside the edited paths:\n got %v\nwant %v", found, want)
	}
	// Anchors with explicit messages: the shared names in the sibling record
	// and in each edited record's other side keep their original values, and
	// no appended marker landed on another path's tail.
	if got := found[1].PathToFirst; !sameStrings(got, []string{"s2", "mixL", "left"}) {
		t.Errorf("s2 path-to-first after editing s1's = %v, want [s2 mixL left]", got)
	}
	if got := found[0].PathToSecond; !sameStrings(got, []string{"s1", "right"}) {
		t.Errorf("s1 path-to-second after editing its first side = %v, want [s1 right]", got)
	}

	// The graph kept its registered names, relationships and list orders.
	if !reflect.DeepEqual(snapshot(graph), before) {
		t.Fatalf("editing results changed graph: before=%v after=%v", before, snapshot(graph))
	}
	assertConsistent(t, graph)

	// The result obtained before the edits is untouched.
	if !reflect.DeepEqual(prior, priorCopy) {
		t.Fatalf("earlier result changed after edits to a later one: got %v, want %v", prior, priorCopy)
	}

	// A fresh query still returns the complete original content, computed from
	// the untouched lineage; the library does not adopt the caller's rewrites.
	fresh := mustCommon(t, graph, "left", "right")
	if !reflect.DeepEqual(fresh, original) {
		t.Fatalf("re-query after edits = %v, want original %v", fresh, original)
	}
}

// Regression boundary: querying a dataset against itself yields one record
// with both distances zero and both paths holding only the target's name.
// Rewriting or extending one side for display must not change the other side,
// even though both sides carry the same single name.
func TestCommonUpstreamsSameTargetEditedSideIsolated(t *testing.T) {
	graph := buildRegisteredGraph(t, [][]string{
		{"raw"},
		{"a", "raw"},
		{"only", "a"},
	})
	before := snapshot(graph)

	found := mustCommon(t, graph, "only", "only")
	if got, want := commonNames(found), []string{"only"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("same target: common sources = %v, want %v", got, want)
	}
	assertCommon(t, found, "only", 0, []string{"only"}, 0, []string{"only"})

	// Rewrite and extend the first-target side.
	found[0].PathToFirst[0] = "only (renamed)"
	found[0].PathToFirst = append(found[0].PathToFirst, "★")

	// The second side of the same record keeps its original content, and the
	// record's name and zero distances are unaffected.
	if got := found[0].PathToSecond; !sameStrings(got, []string{"only"}) {
		t.Fatalf("path-to-second after editing first side = %v, want [only]", got)
	}
	if found[0].Dataset != "only" || found[0].DistanceToFirst != 0 || found[0].DistanceToSecond != 0 {
		t.Fatalf("record scalars changed by path edit: %+v", found[0])
	}

	// The mirror edit on the second side likewise leaves the first side as the
	// caller left it.
	found[0].PathToSecond[0] = "only (renamed)"
	found[0].PathToSecond = append(found[0].PathToSecond, "★")
	if got := found[0].PathToFirst; !sameStrings(got, []string{"only (renamed)", "★"}) {
		t.Fatalf("path-to-first after editing second side = %v, want [only (renamed) ★]", got)
	}

	// The graph is untouched and a re-query recomputes the original record.
	if !reflect.DeepEqual(snapshot(graph), before) {
		t.Fatalf("editing results changed graph: before=%v after=%v", before, snapshot(graph))
	}
	assertConsistent(t, graph)
	fresh := mustCommon(t, graph, "only", "only")
	if got, want := commonNames(fresh), []string{"only"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("re-query common sources = %v, want %v", got, want)
	}
	assertCommon(t, fresh, "only", 0, []string{"only"}, 0, []string{"only"})
}
