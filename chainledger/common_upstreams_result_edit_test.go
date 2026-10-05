package chainledger

import (
	"reflect"
	"testing"
)

// buildCommonUpstreamsEditGraph builds a lineage with TWO nearest common
// sources whose explanation paths run through shared tail segments:
//
//	raw ─┬─> src1 ─┬─> ml ───────> left     (src1 short route to left)
//	     │         └─> h1 ─> mr ─> right    (src1 long route to right)
//	     └─> src2 ─┬─> mr ───────> right    (src2 short route to right)
//	               └─> h2 ─> ml ─> left     (src2 long route to left)
//
// Registered edges (derivation reads parent -> child):
//
//	raw    (root)
//	src1 -> raw; src2 -> raw
//	h1 -> src1; h2 -> src2
//	ml -> src1, h2
//	mr -> h1, src2
//	left  -> ml
//	right -> mr
//
// src1 reaches left directly over two edges and right the long way around
// over three; src2 is the mirror image. CommonUpstreams(left, right) reports
// src1 and src2: both are common, neither has a common child (ml and h2 lead
// to left alone, mr and h1 lead to right alone), and raw is hidden behind
// both of them. The two sources' paths toward the SAME target share a tail:
//
//	src1 -> left : [src1, ml, left]
//	src2 -> left : [src2, h2, ml, left]   (shared tail ml -> left)
//	src1 -> right: [src1, h1, mr, right]  (shared tail mr -> right)
//	src2 -> right: [src2, mr, right]
//
// Every route shown is the unique shortest route, so the expectations follow
// the current frontier and shortest/lexicographic rules without relying on a
// tie break.
func buildCommonUpstreamsEditGraph(t *testing.T) map[string]*Lineage {
	t.Helper()
	return buildRegisteredGraph(t, [][]string{
		{"raw"},
		{"src1", "raw"},
		{"src2", "raw"},
		{"h1", "src1"},
		{"h2", "src2"},
		{"ml", "src1", "h2"},
		{"mr", "h1", "src2"},
		{"left", "ml"},
		{"right", "mr"},
	})
}

// expectedEditedGraphCommons is the full CommonUpstreams(left, right) answer
// for buildCommonUpstreamsEditGraph, ordered by source name.
func expectedEditedGraphCommons() []CommonUpstream {
	return []CommonUpstream{
		{
			Dataset:          "src1",
			DistanceToFirst:  2,
			PathToFirst:      []string{"src1", "ml", "left"},
			DistanceToSecond: 3,
			PathToSecond:     []string{"src1", "h1", "mr", "right"},
		},
		{
			Dataset:          "src2",
			DistanceToFirst:  3,
			PathToFirst:      []string{"src2", "h2", "ml", "left"},
			DistanceToSecond: 2,
			PathToSecond:     []string{"src2", "mr", "right"},
		},
	}
}

// assertCommonsDeepEqual fails unless the two results agree in every field,
// including each side's path content and order.
func assertCommonsDeepEqual(t *testing.T, label string, got, want []CommonUpstream) {
	t.Helper()
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("%s = %v, want %v", label, got, want)
	}
}

// A caller is allowed to rewrite a name inside one returned explanation path
// for display. That edit belongs to the single path the caller holds: the
// other side of the SAME record, every path of every OTHER record (including
// the same tail name reached from another source), the common-source names,
// both sides' distances, the record ordering, a query result obtained
// earlier, the lineage graph and any later query all keep their query-time
// content.
//
// This test edits the FIRST-target side (src1 -> left) at the shared tail
// name ml, which src2's own first-side path also passes through one hop
// later — exactly where a shared backing array between records would leak
// the rewrite.
func TestCommonUpstreamsCallerEditOnePathIsolatesOthersFirstSide(t *testing.T) {
	graph := buildCommonUpstreamsEditGraph(t)
	assertConsistent(t, graph)

	want := expectedEditedGraphCommons()
	found := mustCommon(t, graph, "left", "right")
	assertCommonsDeepEqual(t, "common sources", found, want)
	assertEachCommonOnce(t, found)

	// A second, independently obtained result for the same pair is taken
	// before the edit: it must never observe the caller's rewrite.
	sibling := mustCommon(t, graph, "left", "right")
	siblingCopy := cloneCommons(sibling)
	original := cloneCommons(found)
	graphBefore := snapshot(graph)

	// Display-only rewrite of the src1 -> left path: rename the shared tail
	// name ml (src2's path to the same target also runs through ml) and append
	// a presentation marker at the path's end.
	found[0].PathToFirst[1] = "ml-display"
	found[0].PathToFirst = append(found[0].PathToFirst, "display-marker")

	// The edited path itself reflects exactly the caller's two edits and
	// nothing else grew or shrank.
	if got, wantPath := found[0].PathToFirst, []string{"src1", "ml-display", "left", "display-marker"}; !sameStrings(got, wantPath) {
		t.Fatalf("edited path-to-first = %v, want %v", got, wantPath)
	}

	// The other side of the same record is untouched: same head name src1,
	// same inner names, same end, same length — the rewrite did not cross
	// between the two sides' backing arrays.
	if got, wantPath := found[0].PathToSecond, []string{"src1", "h1", "mr", "right"}; !sameStrings(got, wantPath) {
		t.Fatalf("src1 path-to-second leaked the edit: got %v, want %v", got, wantPath)
	}

	// The other record is untouched name by name in BOTH paths: its first-side
	// path keeps the shared tail name ml at its own position (one hop later
	// than in the edited path) and still ends at left; its second side never
	// met the edit.
	if got, wantPath := found[1].PathToFirst, []string{"src2", "h2", "ml", "left"}; !sameStrings(got, wantPath) {
		t.Fatalf("src2 path-to-first leaked the edit at the shared tail name: got %v, want %v", got, wantPath)
	}
	if got, wantPath := found[1].PathToSecond, []string{"src2", "mr", "right"}; !sameStrings(got, wantPath) {
		t.Fatalf("src2 path-to-second leaked the edit: got %v, want %v", got, wantPath)
	}

	// Source identity, distances and record order are properties of the query
	// and survive a presentation rewrite of one path.
	if got, wantNames := commonNames(found), []string{"src1", "src2"}; !reflect.DeepEqual(got, wantNames) {
		t.Fatalf("source names/order changed: got %v, want %v", got, wantNames)
	}
	if found[0].Dataset != "src1" || found[0].DistanceToFirst != 2 || found[0].DistanceToSecond != 3 {
		t.Fatalf("edited record identity/distances changed: %+v", found[0])
	}
	if found[1].Dataset != "src2" || found[1].DistanceToFirst != 3 || found[1].DistanceToSecond != 2 {
		t.Fatalf("other record identity/distances changed: %+v", found[1])
	}

	// Appending the marker lengthened only the edited path: no other path
	// gained an extra name or a different end.
	if len(found[0].PathToSecond) != 4 || len(found[1].PathToFirst) != 4 || len(found[1].PathToSecond) != 3 {
		t.Fatalf("the marker leaked beyond the edited path: src1.second=%v src2.first=%v src2.second=%v",
			found[0].PathToSecond, found[1].PathToFirst, found[1].PathToSecond)
	}

	// The graph keeps its registered names, upstream/downstream relationships
	// and stored list orders.
	if !reflect.DeepEqual(snapshot(graph), graphBefore) {
		t.Fatalf("editing a result changed the graph: before=%v after=%v", graphBefore, snapshot(graph))
	}
	assertEntry(t, graph, "raw", nil, []string{"src1", "src2"})
	assertEntry(t, graph, "src1", []string{"raw"}, []string{"h1", "ml"})
	assertEntry(t, graph, "src2", []string{"raw"}, []string{"h2", "mr"})
	assertEntry(t, graph, "h1", []string{"src1"}, []string{"mr"})
	assertEntry(t, graph, "h2", []string{"src2"}, []string{"ml"})
	assertEntry(t, graph, "ml", []string{"src1", "h2"}, []string{"left"})
	assertEntry(t, graph, "mr", []string{"h1", "src2"}, []string{"right"})
	assertEntry(t, graph, "left", []string{"ml"}, nil)
	assertEntry(t, graph, "right", []string{"mr"}, nil)
	assertConsistent(t, graph)

	// A fresh query recomputes the answer from the untouched graph and shows
	// no trace of the display rewrite or the marker.
	fresh := mustCommon(t, graph, "left", "right")
	assertCommonsDeepEqual(t, "fresh query after edit", fresh, original)

	// The sibling result obtained before the edit stayed at its query-time
	// content, name by name on both sides.
	assertCommonsDeepEqual(t, "earlier sibling result", sibling, siblingCopy)
}

// The same isolation holds when the caller edits the SECOND-target side of
// the other source record: renaming the shared tail name mr on src2's
// [src2, mr, right] path (src1's long path to the same target also passes
// through mr one hop later) and appending a marker must change nothing but
// that one path.
func TestCommonUpstreamsCallerEditOnePathIsolatesOthersSecondSide(t *testing.T) {
	graph := buildCommonUpstreamsEditGraph(t)

	found := mustCommon(t, graph, "left", "right")
	original := cloneCommons(found)
	sibling := mustCommon(t, graph, "right", "left")
	siblingCopy := cloneCommons(sibling)
	graphBefore := snapshot(graph)

	// found[1] is the src2 record (results are ordered by source name); edit
	// the shared tail name mr on its second-side path [src2, mr, right].
	found[1].PathToSecond[1] = "mr-display"
	found[1].PathToSecond = append(found[1].PathToSecond, "display-marker")

	if got, wantPath := found[1].PathToSecond, []string{"src2", "mr-display", "right", "display-marker"}; !sameStrings(got, wantPath) {
		t.Fatalf("edited path-to-second = %v, want %v", got, wantPath)
	}
	// src2's first-side long path keeps its full original sequence and
	// length, including the tail ml that also terminates src1's first side.
	if got, wantPath := found[1].PathToFirst, []string{"src2", "h2", "ml", "left"}; !sameStrings(got, wantPath) {
		t.Fatalf("src2 path-to-first leaked the edit: got %v, want %v", got, wantPath)
	}
	// The src1 record is untouched on both sides; its long second-side path
	// keeps the same tail name mr at its own position and still ends at right.
	if got, wantPath := found[0].PathToFirst, []string{"src1", "ml", "left"}; !sameStrings(got, wantPath) {
		t.Fatalf("src1 path-to-first leaked the edit: got %v, want %v", got, wantPath)
	}
	if got, wantPath := found[0].PathToSecond, []string{"src1", "h1", "mr", "right"}; !sameStrings(got, wantPath) {
		t.Fatalf("src1 path-to-second leaked the edit at the shared tail name: got %v, want %v", got, wantPath)
	}
	if len(found[0].PathToFirst) != 3 || len(found[0].PathToSecond) != 4 || len(found[1].PathToFirst) != 4 {
		t.Fatalf("the marker leaked beyond the edited path: src1.first=%v src1.second=%v src2.first=%v",
			found[0].PathToFirst, found[0].PathToSecond, found[1].PathToFirst)
	}
	if got, wantNames := commonNames(found), []string{"src1", "src2"}; !reflect.DeepEqual(got, wantNames) {
		t.Fatalf("source names/order changed: got %v, want %v", got, wantNames)
	}
	if found[1].DistanceToFirst != 3 || found[1].DistanceToSecond != 2 {
		t.Fatalf("edited record distances changed: %+v", found[1])
	}
	if found[0].DistanceToFirst != 2 || found[0].DistanceToSecond != 3 {
		t.Fatalf("other record distances changed: %+v", found[0])
	}

	if !reflect.DeepEqual(snapshot(graph), graphBefore) {
		t.Fatalf("editing a result changed the graph: before=%v after=%v", graphBefore, snapshot(graph))
	}
	assertConsistent(t, graph)

	fresh := mustCommon(t, graph, "left", "right")
	assertCommonsDeepEqual(t, "fresh query after edit", fresh, original)

	// Even a result whose sides are swapped (taken before the edit) is
	// unaffected by what happened to the other result afterwards.
	assertCommonsDeepEqual(t, "earlier swapped sibling result", sibling, siblingCopy)
}

// Two targets with the same name are the important boundary: the sole record
// has both distances zero and both paths equal to [name]. Renaming or
// extending one side for display must not change the other side, the graph's
// registered names and relationships, a repeat query, or a sibling result
// already in hand. The edited path may stay display-only: the library is not
// required to turn it back into a valid lineage path.
func TestCommonUpstreamsSameTargetCallerEditIsolatesOtherSide(t *testing.T) {
	graph := buildRegisteredGraph(t, [][]string{
		{"raw"},
		{"a", "raw"},
		{"b", "raw"},
		{"only", "a", "b"},
	})
	assertConsistent(t, graph)

	found := mustCommon(t, graph, "only", "only")
	original := cloneCommons(found)
	if got, want := len(found), 1; got != want {
		t.Fatalf("same target: got %d records %v, want %d", got, found, want)
	}
	if found[0].Dataset != "only" || found[0].DistanceToFirst != 0 || found[0].DistanceToSecond != 0 {
		t.Fatalf("same target record = %+v, want only/(0,0)", found[0])
	}
	if !sameStrings(found[0].PathToFirst, []string{"only"}) || !sameStrings(found[0].PathToSecond, []string{"only"}) {
		t.Fatalf("same target paths = %v / %v, want [only] / [only]",
			found[0].PathToFirst, found[0].PathToSecond)
	}

	sibling := mustCommon(t, graph, "only", "only")
	siblingCopy := cloneCommons(sibling)
	graphBefore := snapshot(graph)

	// Rewrite the first side's only name and append a marker. The path may
	// remain a display-only string; nothing revalidates or restores it.
	found[0].PathToFirst[0] = "only-display"
	found[0].PathToFirst = append(found[0].PathToFirst, "display-marker")

	if got, wantPath := found[0].PathToFirst, []string{"only-display", "display-marker"}; !sameStrings(got, wantPath) {
		t.Fatalf("edited path-to-first = %v, want %v", got, wantPath)
	}
	// The other side is still the single, zero-distance target name.
	if got, wantPath := found[0].PathToSecond, []string{"only"}; !sameStrings(got, wantPath) {
		t.Fatalf("path-to-second leaked the same-target edit: got %v, want %v", got, wantPath)
	}
	if len(found[0].PathToSecond) != 1 {
		t.Fatalf("marker leaked onto the other side: %v", found[0].PathToSecond)
	}
	if found[0].Dataset != "only" || found[0].DistanceToFirst != 0 || found[0].DistanceToSecond != 0 {
		t.Fatalf("identity/distances changed after the display edit: %+v", found[0])
	}

	// Registered names, both edge directions and stored list orders stay as
	// they were; the display name never becomes a registered dataset.
	if _, ok := graph["only-display"]; ok {
		t.Fatal("display-only name was registered into the graph")
	}
	if !reflect.DeepEqual(snapshot(graph), graphBefore) {
		t.Fatalf("editing a result changed the graph: before=%v after=%v", graphBefore, snapshot(graph))
	}
	assertEntry(t, graph, "only", []string{"a", "b"}, nil)
	assertEntry(t, graph, "a", []string{"raw"}, []string{"only"})
	assertEntry(t, graph, "b", []string{"raw"}, []string{"only"})
	assertEntry(t, graph, "raw", nil, []string{"a", "b"})
	assertConsistent(t, graph)

	// Querying again recomputes from the original lineage: one record, zero
	// distances, both sides [only].
	fresh := mustCommon(t, graph, "only", "only")
	assertCommonsDeepEqual(t, "fresh same-target query", fresh, original)

	// The other result obtained before the edit is untouched as well.
	assertCommonsDeepEqual(t, "earlier same-target sibling result", sibling, siblingCopy)
}
