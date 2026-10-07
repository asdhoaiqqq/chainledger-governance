package chainledger

import (
	"fmt"
	"reflect"
	"testing"
)

// This file pins the storage refactor of source tracing at the graph shapes
// that made the old per-node full-path storage grow quadratically:
//
//   - one root behind a single long derivation chain (every intermediate used
//     to keep its own complete path copy, even though only the root's path is
//     reported), and
//   - branches that diverge and converge again, level after level, before
//     reaching one or several roots.
//
// Tracing must still return a COMPLETE report at any depth: no depth cap, no
// truncated path, no dropped source, with the same shortest-path and
// query-end UTF-8 tie-break rules, and returned paths stay independent of one
// another, of later queries, and of the input snapshot.

// longChainJSON builds a single chain n0 -> n1 -> ... -> nK, where n0 is the
// queried derived dataset and nK is the only root.
func longChainJSON(k int) string {
	json := `{"datasets":[`
	for i := 0; i < k; i++ {
		if i > 0 {
			json += ","
		}
		if i == k-1 {
			json += fmt.Sprintf(`{"name":"n%d","upstreams":[]}`, i)
		} else {
			json += fmt.Sprintf(`{"name":"n%d","upstreams":["n%d"]}`, i, i+1)
		}
	}
	return json + `]}`
}

// TestTraceLongSingleChainIsComplete traces a chain far longer than any fixed
// depth limit a truncated implementation could choose: the full query-to-root
// path, both endpoints included, must come back with every intermediate.
func TestTraceLongSingleChainIsComplete(t *testing.T) {
	const depth = 2000
	report := traceOf(t, longChainJSON(depth+1), "n0")
	if len(report.Sources) != 1 {
		t.Fatalf("sources = %d entries, want exactly one root", len(report.Sources))
	}
	wantPath := make([]string, depth+1)
	for i := range wantPath {
		wantPath[i] = fmt.Sprintf("n%d", i)
	}
	got := report.Sources[0]
	if got.Root != fmt.Sprintf("n%d", depth) {
		t.Errorf("root = %q, want n%d", got.Root, depth)
	}
	if !reflect.DeepEqual(got.Path, wantPath) {
		t.Fatalf("path length = %d, want %d; head %v tail %v",
			len(got.Path), len(wantPath), firstNames(got.Path, 3), lastNames(got.Path, 3))
	}
}

// TestTraceLongChainPathsStayIndependent edits one report from a deep chain
// while holding a second one: the compact parent-pointer reconstruction must
// hand out independent slices, never a shared tree a caller can reach through.
func TestTraceLongChainPathsStayIndependent(t *testing.T) {
	snap := snapshotOf(t, longChainJSON(301))
	first, err := TraceSources(snap, "n0")
	if err != nil {
		t.Fatalf("first TraceSources: %v", err)
	}
	second, err := TraceSources(snap, "n0")
	if err != nil {
		t.Fatalf("second TraceSources: %v", err)
	}
	want := append([]string{}, second.Sources[0].Path...)

	for i := range first.Sources[0].Path {
		first.Sources[0].Path[i] = "HACK"
	}
	first.Sources[0].Path = append(first.Sources[0].Path, "EXTRA")
	first.Sources[0].Root = "HACK"

	if !reflect.DeepEqual(second.Sources[0].Path, want) {
		t.Errorf("held report aliases the edited one: head %v tail %v, want intact path",
			firstNames(second.Sources[0].Path, 3), lastNames(second.Sources[0].Path, 3))
	}
	fresh := traceOf(t, longChainJSON(301), "n0")
	if !reflect.DeepEqual(fresh.Sources[0].Path, want) {
		t.Errorf("fresh query after edit did not return the frozen path")
	}
}

// convergingLadderJSON builds levels of two nodes each, where both nodes of a
// level point at BOTH nodes of the next level, so branches split and merge
// again at every step. After levels levels the ladder ends:
//   - at the single root end ("R"), or
//   - at a merge node M that continues to two separate roots R1 and R2.
//
// Names are chosen so the two rails are bytewise ordered "aN" (smaller) and
// "bN" (larger) at every level; the representative path must then always take
// the a-rail, decided at the first differing name right after the query. When
// directR1 is set, the queried dataset T additionally links straight to R1,
// giving only that root a shortcut.
func convergingLadderJSON(levels int, multiRoot, directR1 bool) string {
	json := `{"datasets":[`
	first := true
	emit := func(rec string) {
		if !first {
			json += ","
		}
		first = false
		json += rec
	}
	tUpstreams := []string{"a0", "b0"}
	if directR1 {
		tUpstreams = []string{"R1", "a0", "b0"}
	}
	emit(fmt.Sprintf(`{"name":"T","upstreams":[%s]}`, quoteList(tUpstreams)))
	for i := 0; i < levels; i++ {
		var upA, upB []string
		if i+1 < levels {
			upA = []string{fmt.Sprintf("a%d", i+1), fmt.Sprintf("b%d", i+1)}
			upB = upA
		} else if multiRoot {
			upA = []string{"M"}
			upB = []string{"M"}
		} else {
			upA = []string{"R"}
			upB = []string{"R"}
		}
		emit(fmt.Sprintf(`{"name":"a%d","upstreams":[%s]}`, i, quoteList(upA)))
		emit(fmt.Sprintf(`{"name":"b%d","upstreams":[%s]}`, i, quoteList(upB)))
	}
	if multiRoot {
		emit(`{"name":"M","upstreams":["R1","R2"]}`)
		emit(`{"name":"R1","upstreams":[]}`)
		emit(`{"name":"R2","upstreams":[]}`)
	} else {
		emit(`{"name":"R","upstreams":[]}`)
	}
	return json + `]}`
}

func quoteList(names []string) string {
	out := ""
	for i, name := range names {
		if i > 0 {
			out += ","
		}
		out += fmt.Sprintf("%q", name)
	}
	return out
}

// TestTraceRepeatedlyConvergingBranches walks a long split/merge ladder ending
// at one root. All routes have the same number of relations; the chosen one
// takes the bytewise-smaller rail from the very first fork, level after level,
// and the root is reported exactly once with its complete path.
func TestTraceRepeatedlyConvergingBranches(t *testing.T) {
	const levels = 100
	report := traceOf(t, convergingLadderJSON(levels, false, false), "T")
	wantPath := make([]string, 0, levels+2)
	wantPath = append(wantPath, "T")
	for i := 0; i < levels; i++ {
		wantPath = append(wantPath, fmt.Sprintf("a%d", i))
	}
	wantPath = append(wantPath, "R")
	want := []SourceTrace{{Root: "R", Path: wantPath}}
	if !reflect.DeepEqual(report.Sources, want) {
		t.Errorf("Sources = head %v tail %v, want a-rail path ending R",
			firstNames(report.Sources[0].Path, 4), lastNames(report.Sources[0].Path, 4))
	}
}

// TestTraceConvergingLadderDivergesToTwoRoots ends the ladder at a merge node
// that continues to two roots: each root keeps its own complete shortest path
// (sharing every name up to the merge), both roots are reported, and a shortcut
// added to just one root later only shortens that root.
func TestTraceConvergingLadderDivergesToTwoRoots(t *testing.T) {
	const levels = 50
	report := traceOf(t, convergingLadderJSON(levels, true, false), "T")

	railPath := func(root string) []string {
		path := []string{"T"}
		for i := 0; i < levels; i++ {
			path = append(path, fmt.Sprintf("a%d", i))
		}
		path = append(path, "M", root)
		return path
	}
	want := []SourceTrace{
		{Root: "R1", Path: railPath("R1")},
		{Root: "R2", Path: railPath("R2")},
	}
	if !reflect.DeepEqual(report.Sources, want) {
		t.Errorf("Sources = %+v, want both roots on the a-rail through M", report.Sources)
	}

	// The two paths share a long prefix by content; editing one must not move
	// the other, even at the shared positions.
	report.Sources[0].Path[1] = "HACK"
	report.Sources[0].Path[len(report.Sources[0].Path)-1] = "HACK"
	if !reflect.DeepEqual(report.Sources[1], want[1]) {
		t.Errorf("R2 path changed after editing R1's: %+v", report.Sources[1])
	}
}

// TestTraceLongLadderShortcutToOneRoot rewires the queried dataset straight to
// R1 alongside the deep ladder. R1 gets the one-relation path, while R2 still
// reports its complete ladder path; completeness at depth and per-root path
// independence both hold.
func TestTraceLongLadderShortcutToOneRoot(t *testing.T) {
	const levels = 80
	json := convergingLadderJSON(levels, true, true)
	report := traceOf(t, json, "T")

	r2Path := []string{"T"}
	for i := 0; i < levels; i++ {
		r2Path = append(r2Path, fmt.Sprintf("a%d", i))
	}
	r2Path = append(r2Path, "M", "R2")
	want := []SourceTrace{
		{Root: "R1", Path: []string{"T", "R1"}},
		{Root: "R2", Path: r2Path},
	}
	if len(report.Sources) != 2 ||
		!reflect.DeepEqual(report.Sources[0], want[0]) ||
		!reflect.DeepEqual(report.Sources[1], want[1]) {
		t.Errorf("Sources = %+v\nwant R1 shortcut and full R2 ladder path", report.Sources)
	}
}

// TestTraceDeepChainFromIntermediateAndRoot queries several nodes of a deep
// chain: each intermediate sees only the suffix toward the same root, and the
// root itself reports the single-element path, regardless of chain depth.
func TestTraceDeepChainFromIntermediateAndRoot(t *testing.T) {
	const depth = 500
	snap := snapshotOf(t, longChainJSON(depth+1))
	for _, q := range []int{0, 1, depth / 2, depth - 1} {
		report, err := TraceSources(snap, fmt.Sprintf("n%d", q))
		if err != nil {
			t.Fatalf("TraceSources(n%d): %v", q, err)
		}
		wantPath := []string{}
		for i := q; i <= depth; i++ {
			wantPath = append(wantPath, fmt.Sprintf("n%d", i))
		}
		if got := report.Sources; !reflect.DeepEqual(got, []SourceTrace{
			{Root: fmt.Sprintf("n%d", depth), Path: wantPath},
		}) {
			t.Errorf("query n%d: head %v tail %v, want suffix path to n%d",
				q, firstNames(got[0].Path, 2), lastNames(got[0].Path, 2), depth)
		}
	}
	rootReport := traceReportOf(t, snap, fmt.Sprintf("n%d", depth))
	if got := rootReport.Sources; !reflect.DeepEqual(got, []SourceTrace{
		{Root: fmt.Sprintf("n%d", depth), Path: []string{fmt.Sprintf("n%d", depth)}},
	}) {
		t.Errorf("root self-query deep = %+v, want single-element path", got)
	}
}

func firstNames(names []string, n int) []string {
	if len(names) < n {
		return names
	}
	return names[:n]
}

func lastNames(names []string, n int) []string {
	if len(names) < n {
		return names
	}
	return names[len(names)-n:]
}
