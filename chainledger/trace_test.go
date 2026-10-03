package chainledger

import (
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
)

// traceOf traces dataset in the snapshot built from graphJSON.
func traceOf(t *testing.T, graphJSON, dataset string) *TraceReport {
	t.Helper()
	report, err := TraceSources(snapshotOf(t, graphJSON), dataset)
	if err != nil {
		t.Fatalf("TraceSources(%q): %v", dataset, err)
	}
	return report
}

func TestTraceDirectRootBeatsIndirect(t *testing.T) {
	// T depends on R directly and also reaches R through B: the direct,
	// shorter path wins.
	report := traceOf(t, `{"datasets":[
		{"name":"T","upstreams":["R","B"]},
		{"name":"B","upstreams":["R"]},
		{"name":"R","upstreams":[]}
	]}`, "T")
	want := []SourceTrace{{Root: "R", Path: []string{"T", "R"}}}
	if !reflect.DeepEqual(report.Sources, want) {
		t.Errorf("Sources = %+v, want %+v", report.Sources, want)
	}
}

func TestTraceEqualLengthPrefersByteOrder(t *testing.T) {
	// T reaches R through A or through B with the same number of relations;
	// the path via the bytewise-smaller intermediate name wins.
	report := traceOf(t, `{"datasets":[
		{"name":"T","upstreams":["B","A"]},
		{"name":"A","upstreams":["R"]},
		{"name":"B","upstreams":["R"]},
		{"name":"R","upstreams":[]}
	]}`, "T")
	want := []SourceTrace{{Root: "R", Path: []string{"T", "A", "R"}}}
	if !reflect.DeepEqual(report.Sources, want) {
		t.Errorf("Sources = %+v, want %+v", report.Sources, want)
	}
}

func TestTraceMultipleRootsSortedAndSharedIntermediates(t *testing.T) {
	// Two roots share the intermediate M; each keeps its own path, and the
	// sources are sorted by root name byte order ("Z" before "a").
	report := traceOf(t, `{"datasets":[
		{"name":"T","upstreams":["M"]},
		{"name":"M","upstreams":["a","Z"]},
		{"name":"a","upstreams":[]},
		{"name":"Z","upstreams":[]}
	]}`, "T")
	want := []SourceTrace{
		{Root: "Z", Path: []string{"T", "M", "Z"}},
		{Root: "a", Path: []string{"T", "M", "a"}},
	}
	if !reflect.DeepEqual(report.Sources, want) {
		t.Errorf("Sources = %+v, want %+v", report.Sources, want)
	}
}

func TestTraceRootQueriesItself(t *testing.T) {
	report := traceOf(t, `{"datasets":[
		{"name":"R","upstreams":[]},
		{"name":"S","upstreams":["R"]}
	]}`, "R")
	want := []SourceTrace{{Root: "R", Path: []string{"R"}}}
	if !reflect.DeepEqual(report.Sources, want) {
		t.Errorf("Sources = %+v, want %+v", report.Sources, want)
	}
}

func TestTraceIgnoresDisconnectedAndNonRootIntermediates(t *testing.T) {
	// X is disconnected and must not appear; M has an upstream so it is not
	// a root even though it is on the path.
	report := traceOf(t, `{"datasets":[
		{"name":"T","upstreams":["M"]},
		{"name":"M","upstreams":["R"]},
		{"name":"R","upstreams":[]},
		{"name":"X","upstreams":[]}
	]}`, "T")
	want := []SourceTrace{{Root: "R", Path: []string{"T", "M", "R"}}}
	if !reflect.DeepEqual(report.Sources, want) {
		t.Errorf("Sources = %+v, want %+v", report.Sources, want)
	}
}

func TestTraceReportCarriesContentIDAndQuery(t *testing.T) {
	snap := snapshotOf(t, `{"datasets":[{"name":"T","upstreams":[]}]}`)
	report, err := TraceSources(snap, "T")
	if err != nil {
		t.Fatalf("TraceSources: %v", err)
	}
	if report.ContentID != snap.ContentID {
		t.Errorf("ContentID = %q, want snapshot id %q", report.ContentID, snap.ContentID)
	}
	if report.Dataset != "T" {
		t.Errorf("Dataset = %q, want %q", report.Dataset, "T")
	}
}

func TestTraceUnknownAndEmptyDatasetRejected(t *testing.T) {
	snap := snapshotOf(t, `{"datasets":[{"name":"T","upstreams":[]}]}`)
	if _, err := TraceSources(snap, ""); !errors.Is(err, ErrInvalidArgument) {
		t.Errorf("empty name error = %v, want ErrInvalidArgument", err)
	}
	if _, err := TraceSources(snap, "ghost"); !errors.Is(err, ErrNotFound) {
		t.Errorf("unknown name error = %v, want ErrNotFound", err)
	}
	if _, err := TraceSources(snap, "ghost"); err == nil || !strings.Contains(err.Error(), "ghost") {
		t.Errorf("unknown name error = %v, must name the dataset", err)
	}
}

// TestTraceDeterministicAcrossEquivalentSnapshots checks that record order,
// upstream order, duplicate upstreams, and JSON whitespace never change the
// trace report for the same query.
func TestTraceDeterministicAcrossEquivalentSnapshots(t *testing.T) {
	variants := []string{
		`{"datasets":[{"name":"T","upstreams":["A","B"]},{"name":"A","upstreams":["R"]},{"name":"B","upstreams":["R"]},{"name":"R","upstreams":[]}]}`,
		`{ "datasets": [ {"upstreams":[],"name":"R"}, {"name":"B","upstreams":["R","R"]}, {"name":"A","upstreams":["R"]}, {"name":"T","upstreams":["B","A","B"]} ] }`,
	}
	var want *TraceReport
	for i, in := range variants {
		report := traceOf(t, in, "T")
		if i == 0 {
			want = report
			continue
		}
		if !reflect.DeepEqual(report, want) {
			t.Errorf("variant %d report = %+v, want %+v", i, report, want)
		}
	}
}

// TestTraceSnapshotIsTheOnlySource pins the version semantics: the trace is
// computed from the snapshot alone, so mutating the live graph afterwards
// cannot change what the frozen version reports.
func TestTraceSnapshotIsTheOnlySource(t *testing.T) {
	graph := graphFromJSON(t, `{"datasets":[{"name":"T","upstreams":["R"]},{"name":"R","upstreams":[]}]}`)
	snap, err := BuildSnapshot(graph)
	if err != nil {
		t.Fatalf("BuildSnapshot: %v", err)
	}
	// Rewire the live graph after the snapshot was frozen.
	if err := Register(graph, Dataset{Name: "S"}, nil); err != nil {
		t.Fatalf("Register S: %v", err)
	}
	if err := Register(graph, Dataset{Name: "T"}, []string{"S"}); err != nil {
		t.Fatalf("rewire T: %v", err)
	}
	report, err := TraceSources(snap, "T")
	if err != nil {
		t.Fatalf("TraceSources: %v", err)
	}
	want := []SourceTrace{{Root: "R", Path: []string{"T", "R"}}}
	if !reflect.DeepEqual(report.Sources, want) {
		t.Errorf("Sources = %+v, want frozen %+v", report.Sources, want)
	}
}

// convergedDeepGraph is the main deep-lineage fixture of this regression
// set, viewed "upstream side first":
//
//	T -> A -> Z -> M -> R
//	T -> B -> C -> M -> R
//	T -> S -> D -> M -> Q
//	M -> K -> Q
//
// From T, both R and Q are reached through routes that merge at M, plus a
// small-named but longer route to R (T,S,D,L,R) and a shortcut S->Q that
// only changes Q's representative path. X (with its own upstream U) is a
// disconnected branch and must never enter the report.
const convergedDeepGraph = `{"datasets":[
	{"name":"R","upstreams":[]},
	{"name":"Q","upstreams":[]},
	{"name":"M","upstreams":["R","K"]},
	{"name":"K","upstreams":["Q"]},
	{"name":"Z","upstreams":["M"]},
	{"name":"A","upstreams":["Z"]},
	{"name":"C","upstreams":["M"]},
	{"name":"B","upstreams":["C"]},
	{"name":"L","upstreams":["R"]},
	{"name":"D","upstreams":["M","L"]},
	{"name":"S","upstreams":["Q","D"]},
	{"name":"T","upstreams":["A","B","S"]},
	{"name":"X","upstreams":["U"]},
	{"name":"U","upstreams":[]}
]}`

// TestTraceDeepMergeTieBreakAtFirstDifference pins the tie-break in deeper
// lineage: T reaches R both via T,A,Z,M,R and via T,B,C,M,R (equal length,
// no shorter route). Choice happens at the first differing node, where
// "A" < "B"; the bytewise-smaller name "C" that occurs later on the losing
// branch must not move the choice. The shared merge node M stays on the
// chosen path and M itself is not reported as a root.
func TestTraceDeepMergeTieBreakAtFirstDifference(t *testing.T) {
	report := traceOf(t, convergedDeepGraph, "T")
	want := []SourceTrace{
		{Root: "Q", Path: []string{"T", "S", "Q"}},
		{Root: "R", Path: []string{"T", "A", "Z", "M", "R"}},
	}
	if !reflect.DeepEqual(report.Sources, want) {
		t.Errorf("Sources = %+v, want %+v", report.Sources, want)
	}
}

// TestTraceLateDivergenceAfterSharedPrefix covers equally long paths that
// share their first several nodes and split only at depth. The two routes
// T,M,N,A1,A2,R and T,M,N,B1,B2,R share T,M,N and diverge only at the
// fourth element, where A1 < B1; the reported path is the complete name
// array with the query object, every intermediate, and the root.
func TestTraceLateDivergenceAfterSharedPrefix(t *testing.T) {
	graph := `{"datasets":[
		{"name":"R","upstreams":[]},
		{"name":"A2","upstreams":["R"]},
		{"name":"B2","upstreams":["R"]},
		{"name":"A1","upstreams":["A2"]},
		{"name":"B1","upstreams":["B2"]},
		{"name":"N","upstreams":["B1","A1"]},
		{"name":"M","upstreams":["N"]},
		{"name":"T","upstreams":["M"]}
	]}`
	report := traceOf(t, graph, "T")
	want := []SourceTrace{
		{Root: "R", Path: []string{"T", "M", "N", "A1", "A2", "R"}},
	}
	if !reflect.DeepEqual(report.Sources, want) {
		t.Errorf("Sources = %+v, want %+v", report.Sources, want)
	}
}

// TestTraceShortestBeatsSmallerNamedLongerRoute pins length as the primary
// criterion at depth. From S, R is reached in 3 relations via S,D,M,R and
// in 4 relations via S,A,b,c,R. The longer route is bytewise smaller at the
// first differing node (A < D), so a comparator that ignores length would
// pick it; length must still win.
func TestTraceShortestBeatsSmallerNamedLongerRoute(t *testing.T) {
	graph := `{"datasets":[
		{"name":"R","upstreams":[]},
		{"name":"M","upstreams":["R"]},
		{"name":"D","upstreams":["M"]},
		{"name":"c","upstreams":["R"]},
		{"name":"b","upstreams":["c"]},
		{"name":"A","upstreams":["b"]},
		{"name":"S","upstreams":["D","A"]}
	]}`
	report := traceOf(t, graph, "S")
	want := []SourceTrace{{Root: "R", Path: []string{"S", "D", "M", "R"}}}
	if !reflect.DeepEqual(report.Sources, want) {
		t.Errorf("Sources = %+v, want %+v", report.Sources, want)
	}
}

// TestTracePerRootShortcutIsIndependent checks that a shortcut changing one
// root's representative path neither drops another reachable root nor
// promotes the still-fed merge node M to a root. Without S->Q, Q's path
// from T runs through M,K; adding the shortcut shortens only Q's entry.
func TestTracePerRootShortcutIsIndependent(t *testing.T) {
	withoutShortcut := `{"datasets":[
		{"name":"R","upstreams":[]},
		{"name":"Q","upstreams":[]},
		{"name":"M","upstreams":["R","K"]},
		{"name":"K","upstreams":["Q"]},
		{"name":"Z","upstreams":["M"]},
		{"name":"A","upstreams":["Z"]},
		{"name":"C","upstreams":["M"]},
		{"name":"B","upstreams":["C"]},
		{"name":"S","upstreams":["D"]},
		{"name":"D","upstreams":["M"]},
		{"name":"T","upstreams":["A","B","S"]}
	]}`
	report := traceOf(t, withoutShortcut, "T")
	want := []SourceTrace{
		{Root: "Q", Path: []string{"T", "A", "Z", "M", "K", "Q"}},
		{Root: "R", Path: []string{"T", "A", "Z", "M", "R"}},
	}
	if !reflect.DeepEqual(report.Sources, want) {
		t.Fatalf("without shortcut Sources = %+v, want %+v", report.Sources, want)
	}

	withShortcut := traceOf(t, convergedDeepGraph, "T")
	wantShortcut := []SourceTrace{
		{Root: "Q", Path: []string{"T", "S", "Q"}},
		{Root: "R", Path: []string{"T", "A", "Z", "M", "R"}},
	}
	if !reflect.DeepEqual(withShortcut.Sources, wantShortcut) {
		t.Errorf("with shortcut Sources = %+v, want %+v", withShortcut.Sources, wantShortcut)
	}
}

// TestTraceRootItselfWithDeepAndDisconnectedBranches queries a root
// directly: the answer is always itself with a single-element path,
// regardless of long converged routes elsewhere in the snapshot and of the
// disconnected branch (X,U) that shares no path with the query.
func TestTraceRootItselfWithDeepAndDisconnectedBranches(t *testing.T) {
	report := traceOf(t, convergedDeepGraph, "R")
	want := []SourceTrace{{Root: "R", Path: []string{"R"}}}
	if !reflect.DeepEqual(report.Sources, want) {
		t.Errorf("Sources = %+v, want %+v", report.Sources, want)
	}
}

// TestTraceEquivalentDeepSnapshotsByteIdentical requires the full JSON
// report to be byte-for-byte identical across reorderings of dataset
// records, reorderings of upstream lists, added duplicate upstreams, and
// JSON whitespace differences. The losing C-branch record is even listed
// first so input record order cannot influence the chosen paths.
func TestTraceEquivalentDeepSnapshotsByteIdentical(t *testing.T) {
	shuffled := `{ "datasets" : [
		{"upstreams":["M","M"],"name":"C"},
		{ "name":"B","upstreams":["C"] },
		{"upstreams":[],"name":"U"},
		{"name":"X","upstreams":["U","U"]},
		{"name":"T","upstreams":["S","A","B","A","S"]},
		{"upstreams":["D","Q","D"],"name":"S"},
		{"upstreams":["M","L","L","M"],"name":"D"},
		{"name":"L","upstreams":["R"]},
		{"upstreams":["Z"],"name":"A"},
		{"name":"Z","upstreams":["M"]},
		{"upstreams":["Q"],"name":"K"},
		{"name":"M","upstreams":["K","R","K"]},
		{"upstreams":[],"name":"Q"},
		{"name":"R","upstreams":[]}
	]}`
	out := func(t *testing.T, graphJSON string) string {
		t.Helper()
		report := traceOf(t, graphJSON, "T")
		data, err := json.Marshal(report)
		if err != nil {
			t.Fatalf("marshal report: %v", err)
		}
		return string(data)
	}
	if got, want := out(t, shuffled), out(t, convergedDeepGraph); got != want {
		t.Errorf("deep trace reports differ byte-for-byte:\n%s\n%s", got, want)
	}
}
