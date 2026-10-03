package chainledger

import (
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

// TestTraceDeepConvergingBranches fixes the tie-break when two branches pass
// through different intermediate datasets and merge before the root. From T
// to R both routes have four relations:
//
//	T -> A -> Z -> M -> R
//	T -> B -> C -> M -> R
//
// There is no shorter route. The choice is made at the first differing name,
// which is position 1: A < B, so the path through A and Z wins even though
// the other branch passes through C, which is bytewise smaller than Z.
func TestTraceDeepConvergingBranches(t *testing.T) {
	report := traceOf(t, `{"datasets":[
		{"name":"T","upstreams":["B","A"]},
		{"name":"A","upstreams":["Z"]},
		{"name":"Z","upstreams":["M"]},
		{"name":"B","upstreams":["C"]},
		{"name":"C","upstreams":["M"]},
		{"name":"M","upstreams":["R"]},
		{"name":"R","upstreams":[]}
	]}`, "T")
	want := []SourceTrace{{Root: "R", Path: []string{"T", "A", "Z", "M", "R"}}}
	if !reflect.DeepEqual(report.Sources, want) {
		t.Errorf("Sources = %+v, want %+v", report.Sources, want)
	}
}

// TestTraceDeepSharedPrefixDivergesLate checks that equally long routes which
// share several first nodes are decided at the first position where they
// actually differ, not earlier or later. The full name array is returned:
// query object, every intermediate dataset, and the root.
func TestTraceDeepSharedPrefixDivergesLate(t *testing.T) {
	// T -> P -> Q then either X -> M -> R or Y -> M -> R; the branches merge
	// again at M. At the first differing position X < Y, so X's route wins
	// even though it then climbs through the large name M.
	report := traceOf(t, `{"datasets":[
		{"name":"T","upstreams":["P"]},
		{"name":"P","upstreams":["Q"]},
		{"name":"Q","upstreams":["Y","X"]},
		{"name":"X","upstreams":["M"]},
		{"name":"Y","upstreams":["M"]},
		{"name":"M","upstreams":["R"]},
		{"name":"R","upstreams":[]}
	]}`, "T")
	want := []SourceTrace{{Root: "R", Path: []string{"T", "P", "Q", "X", "M", "R"}}}
	if !reflect.DeepEqual(report.Sources, want) {
		t.Errorf("Sources = %+v, want %+v", report.Sources, want)
	}
}

// TestTraceShortPathBeatsSmallerNamedLongRoute fixes that the number of
// direct relations is decided first: the same root may also be reachable
// through a branch whose names are all bytewise smaller, but when that branch
// is one relation longer, the shorter path still represents the root.
func TestTraceShortPathBeatsSmallerNamedLongRoute(t *testing.T) {
	// Short route (3 relations): T -> A -> Z -> R.
	// Long route (4 relations): T -> 0a -> 0b -> 0c -> R; every name on it
	// sorts below "A" because '0' (0x30) precedes 'A' (0x41) in UTF-8 bytes.
	report := traceOf(t, `{"datasets":[
		{"name":"T","upstreams":["0a","A"]},
		{"name":"A","upstreams":["Z"]},
		{"name":"Z","upstreams":["R"]},
		{"name":"0a","upstreams":["0b"]},
		{"name":"0b","upstreams":["0c"]},
		{"name":"0c","upstreams":["R"]},
		{"name":"R","upstreams":[]}
	]}`, "T")
	want := []SourceTrace{{Root: "R", Path: []string{"T", "A", "Z", "R"}}}
	if !reflect.DeepEqual(report.Sources, want) {
		t.Errorf("Sources = %+v, want %+v", report.Sources, want)
	}
}

// TestTraceMergedIntermediateKeepsPerRootShortestPaths uses a merge node that
// continues to two roots. Every root keeps its own shortest path, and the
// merge node itself is never reported as a root because it still has
// upstreams.
func TestTraceMergedIntermediateKeepsPerRootShortestPaths(t *testing.T) {
	// Both branches merge at M; M then reaches roots R1 and R2.
	report := traceOf(t, `{"datasets":[
		{"name":"T","upstreams":["B","A"]},
		{"name":"A","upstreams":["M"]},
		{"name":"B","upstreams":["M"]},
		{"name":"M","upstreams":["R2","R1"]},
		{"name":"R1","upstreams":[]},
		{"name":"R2","upstreams":[]}
	]}`, "T")
	want := []SourceTrace{
		{Root: "R1", Path: []string{"T", "A", "M", "R1"}},
		{Root: "R2", Path: []string{"T", "A", "M", "R2"}},
	}
	if !reflect.DeepEqual(report.Sources, want) {
		t.Errorf("Sources = %+v, want %+v", report.Sources, want)
	}
}

// TestTraceShortcutToOneRootLeavesOtherRootsUntouched combines a multi-root
// merge with a shortcut to just one of the roots. The shortcut changes only
// that root's representative path; the other root is still reported with its
// own shortest path, and roots are ordered by root name byte order.
func TestTraceShortcutToOneRootLeavesOtherRootsUntouched(t *testing.T) {
	// T -> A -> M reaches both R1 and R2 (3 relations each). T also reaches
	// R1 directly, which shortens only R1's path to one relation.
	report := traceOf(t, `{"datasets":[
		{"name":"T","upstreams":["R1","A"]},
		{"name":"A","upstreams":["M"]},
		{"name":"M","upstreams":["R2","R1"]},
		{"name":"R1","upstreams":[]},
		{"name":"R2","upstreams":[]}
	]}`, "T")
	want := []SourceTrace{
		{Root: "R1", Path: []string{"T", "R1"}},
		{Root: "R2", Path: []string{"T", "A", "M", "R2"}},
	}
	if !reflect.DeepEqual(report.Sources, want) {
		t.Errorf("Sources = %+v, want %+v", report.Sources, want)
	}
}

// TestTraceRootQueriesItselfAmidDeepLineage pins the single-element result in
// a non-trivial graph: the queried root reports only itself, even though deep
// unrelated lineage exists around it.
func TestTraceRootQueriesItselfAmidDeepLineage(t *testing.T) {
	report := traceOf(t, `{"datasets":[
		{"name":"T","upstreams":["M"]},
		{"name":"M","upstreams":["R"]},
		{"name":"R","upstreams":[]}
	]}`, "R")
	want := []SourceTrace{{Root: "R", Path: []string{"R"}}}
	if !reflect.DeepEqual(report.Sources, want) {
		t.Errorf("Sources = %+v, want %+v", report.Sources, want)
	}
}

// TestTraceDeepLineageIgnoresDisconnectedBranches ensures branches that do
// not connect to the queried dataset — including a second deep diamond with
// its own roots — never appear among the sources or change path selection.
func TestTraceDeepLineageIgnoresDisconnectedBranches(t *testing.T) {
	report := traceOf(t, `{"datasets":[
		{"name":"T","upstreams":["A"]},
		{"name":"A","upstreams":["M"]},
		{"name":"M","upstreams":["R"]},
		{"name":"R","upstreams":[]},
		{"name":"U","upstreams":["C","B"]},
		{"name":"B","upstreams":["N"]},
		{"name":"C","upstreams":["N"]},
		{"name":"N","upstreams":["X"]},
		{"name":"X","upstreams":[]}
	]}`, "T")
	want := []SourceTrace{{Root: "R", Path: []string{"T", "A", "M", "R"}}}
	if !reflect.DeepEqual(report.Sources, want) {
		t.Errorf("Sources = %+v, want %+v", report.Sources, want)
	}
}

// TestTraceDeterministicAcrossEquivalentDeepSnapshots is the deep version of
// the determinism guarantee: record order, upstream order, duplicate
// upstreams, and whitespace reshuffle a graph with converging branches and
// multiple post-merge roots, yet every path choice stays identical.
func TestTraceDeterministicAcrossEquivalentDeepSnapshots(t *testing.T) {
	variants := []string{
		// Canonical-ish order: T with two branches merging at M, M reaching
		// two roots, plus a disconnected branch.
		`{"datasets":[
			{"name":"T","upstreams":["B","A"]},
			{"name":"A","upstreams":["Z"]},
			{"name":"Z","upstreams":["M"]},
			{"name":"B","upstreams":["C"]},
			{"name":"C","upstreams":["M"]},
			{"name":"M","upstreams":["R2","R1"]},
			{"name":"R1","upstreams":[]},
			{"name":"R2","upstreams":[]},
			{"name":"U","upstreams":["X"]},
			{"name":"X","upstreams":[]}
		]}`,
		// Same semantics: records reversed, upstream lists reordered with
		// duplicates, whitespace changed.
		`{ "datasets" : [
			{"upstreams":[],"name":"X"},
			{"name":"U","upstreams":["X","X"]},
			{"upstreams":[],"name":"R2"},
			{"name":"R1","upstreams":[]},
			{"upstreams":["R1","R2","R1"],"name":"M"},
			{"name":"C","upstreams":["M","M"]},
			{"upstreams":["M"],"name":"Z"},
			{"name":"B","upstreams":["C"]},
			{"name":"A","upstreams":["Z"]},
			{"upstreams":["A","B","B","A"],"name":"T"}
		] }`,
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
	// Pin the expected choice once so the determinism check cannot pass while
	// both variants make the same wrong selection.
	wantSources := []SourceTrace{
		{Root: "R1", Path: []string{"T", "A", "Z", "M", "R1"}},
		{Root: "R2", Path: []string{"T", "A", "Z", "M", "R2"}},
	}
	if !reflect.DeepEqual(want.Sources, wantSources) {
		t.Errorf("Sources = %+v, want %+v", want.Sources, wantSources)
	}
	if want.ContentID == "" || want.Dataset != "T" {
		t.Errorf("report metadata = contentId %q dataset %q, want snapshot id and %q", want.ContentID, want.Dataset, "T")
	}
}

// TestTraceReportCarriesContentIDAndQueryDeep pins, for a deep graph, that
// the report's identifiers come from the snapshot actually read and from the
// query as typed — never from whichever input record appeared first.
func TestTraceReportCarriesContentIDAndQueryDeep(t *testing.T) {
	in := `{"datasets":[
		{"name":"R","upstreams":[]},
		{"name":"M","upstreams":["R"]},
		{"name":"Z","upstreams":["M"]},
		{"name":"A","upstreams":["Z"]},
		{"name":"T","upstreams":["A"]}
	]}`
	snap := snapshotOf(t, in)
	report, err := TraceSources(snap, "T")
	if err != nil {
		t.Fatalf("TraceSources: %v", err)
	}
	if report.ContentID != snap.ContentID {
		t.Errorf("ContentID = %q, want %q", report.ContentID, snap.ContentID)
	}
	if report.Dataset != "T" {
		t.Errorf("Dataset = %q, want %q", report.Dataset, "T")
	}
	want := []SourceTrace{{Root: "R", Path: []string{"T", "A", "Z", "M", "R"}}}
	if !reflect.DeepEqual(report.Sources, want) {
		t.Errorf("Sources = %+v, want %+v; path must not depend on record order", report.Sources, want)
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
