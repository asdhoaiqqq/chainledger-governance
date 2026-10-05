package chainledger

import (
	"bytes"
	"encoding/json"
	"errors"
	"reflect"
	"testing"
)

// This file is the regression net for the independence of source-trace reports
// returned by TraceSources. A caller that receives a *TraceReport keeps editing
// it for display, may hold several reports of the same snapshot at once (and
// reports for other datasets that pass through the same shared intermediate),
// and keeps holding the input snapshot. None of those views may alias each
// other or the snapshot:
//
//   - editing one source record (its root name, or a name at any position of
//     its path, or appending a name to that path) changes only that record,
//     even when two paths share a prefix (T -> M -> R1 and T -> M -> R2 both
//     start with T, M) — the other record keeps its own root and full path;
//   - reordering or removing entries of the sources array never reaches the
//     input snapshot's dataset names or direct upstream relations;
//   - editing one returned report leaves every other returned report (for the
//     same query or for another downstream through the same intermediate)
//     representing the result at the moment it was obtained, and a fresh query
//     after the edits still reports the frozen snapshot's sources and paths;
//   - the snapshot itself keeps the same content identifier, graph, and export
//     bytes before and after tracing, including after failed queries;
//   - a single-element path for a root query has the same independence;
//   - a failed query (empty name or unknown name) keeps using the existing
//     error categories, returns no success report, and changes neither an
//     earlier report nor the snapshot;
//   - every independent result still obeys the existing business rules: each
//     root once, fewest direct relations first, equal-length ties decided
//     element by element from the query end in UTF-8 byte order, sources sorted
//     by root name, and Chinese/case/space spellings preserved verbatim.

// traceIndependenceGraphJSON is the shared-prefix fixture: the derived
// datasets T and U both reach the two roots R1 and R2 through the common
// intermediate M, so T's two reported paths [T M R1] and [T M R2] share the
// prefix [T M]. X is a disconnected root and must never be reported.
const traceIndependenceGraphJSON = `{"datasets":[
	{"name":"T","upstreams":["M"]},
	{"name":"U","upstreams":["M"]},
	{"name":"M","upstreams":["R1","R2"]},
	{"name":"R1","upstreams":[]},
	{"name":"R2","upstreams":[]},
	{"name":"X","upstreams":[]}
]}`

// wantIndependenceTSources is the exact source report for T, sources sorted by
// root name byte order with complete query-to-root paths.
var wantIndependenceTSources = []SourceTrace{
	{Root: "R1", Path: []string{"T", "M", "R1"}},
	{Root: "R2", Path: []string{"T", "M", "R2"}},
}

// wantIndependenceUSources is the report for the other downstream through the
// same shared intermediate.
var wantIndependenceUSources = []SourceTrace{
	{Root: "R1", Path: []string{"U", "M", "R1"}},
	{Root: "R2", Path: []string{"U", "M", "R2"}},
}

// wantIndependenceSnapshotDatasets is the frozen graph as the snapshot stores
// it: records sorted by name, roots with a non-nil empty upstream list.
var wantIndependenceSnapshotDatasets = []GraphDataset{
	{Name: "M", Upstreams: []string{"R1", "R2"}},
	{Name: "R1", Upstreams: []string{}},
	{Name: "R2", Upstreams: []string{}},
	{Name: "T", Upstreams: []string{"M"}},
	{Name: "U", Upstreams: []string{"M"}},
	{Name: "X", Upstreams: []string{}},
}

// traceReportOf traces dataset in snap, failing the test on error.
func traceReportOf(t *testing.T, snap *SnapshotFile, dataset string) *TraceReport {
	t.Helper()
	report, err := TraceSources(snap, dataset)
	if err != nil {
		t.Fatalf("TraceSources(%q): %v", dataset, err)
	}
	return report
}

// mustMarshalTraceReport renders a report the way a caller displaying it would.
func mustMarshalTraceReport(t *testing.T, report *TraceReport) []byte {
	t.Helper()
	data, err := json.Marshal(report)
	if err != nil {
		t.Fatalf("json.Marshal report unexpected error: %v", err)
	}
	return data
}

// snapshotDatasetsCopy deep-copies the snapshot's dataset records so a test can
// compare names and direct upstreams after the caller mutates a returned
// report. Empty upstream lists stay non-nil, matching the snapshot's own
// representation.
func snapshotDatasetsCopy(snap *SnapshotFile) []GraphDataset {
	datasets := make([]GraphDataset, len(snap.Graph.Datasets))
	for i, ds := range snap.Graph.Datasets {
		upstreams := make([]string, len(ds.Upstreams))
		copy(upstreams, ds.Upstreams)
		datasets[i] = GraphDataset{Name: ds.Name, Upstreams: upstreams}
	}
	return datasets
}

// sourceForRoot finds the record for root in report, so tests can locate the
// "other" record after the sources array has been reordered.
func sourceForRoot(report *TraceReport, root string) (SourceTrace, bool) {
	for _, src := range report.Sources {
		if src.Root == root {
			return src, true
		}
	}
	return SourceTrace{}, false
}

// TestTraceEditingOneSharedPrefixPathLeavesTheOtherUntouched is the headline
// aliasing guarantee: every edit a caller can make inside one source record of
// a two-path report must change only that record. The other record keeps its
// original root and its complete path, including the shared T, M prefix; the
// report-level identifiers and the input snapshot stay exactly as returned.
func TestTraceEditingOneSharedPrefixPathLeavesTheOtherUntouched(t *testing.T) {
	edits := map[string]func(*TraceReport, int){
		"replace query name in path": func(r *TraceReport, i int) {
			r.Sources[i].Path[0] = "HACK"
		},
		"replace intermediate name in path": func(r *TraceReport, i int) {
			r.Sources[i].Path[1] = "HACK"
		},
		"replace root name in path": func(r *TraceReport, i int) {
			r.Sources[i].Path[2] = "HACK"
		},
		"append name to path": func(r *TraceReport, i int) {
			r.Sources[i].Path = append(r.Sources[i].Path, "EXTRA")
		},
		"replace root field": func(r *TraceReport, i int) {
			r.Sources[i].Root = "HACK"
		},
	}
	// Sources are sorted by root name, so R1 is first and R2 second; exercise
	// the edit in both directions, since either shared-prefix path could alias.
	targets := []struct {
		editedIndex int
		editedRoot  string
		otherRoot   string
		other       SourceTrace
	}{
		{0, "R1", "R2", SourceTrace{Root: "R2", Path: []string{"T", "M", "R2"}}},
		{1, "R2", "R1", SourceTrace{Root: "R1", Path: []string{"T", "M", "R1"}}},
	}
	for _, target := range targets {
		for editName, edit := range edits {
			t.Run(target.editedRoot+"/"+editName, func(t *testing.T) {
				snap := snapshotOf(t, traceIndependenceGraphJSON)
				report := traceReportOf(t, snap, "T")

				edit(report, target.editedIndex)

				// The other record is byte-for-byte the original: its root, and
				// every name of its full path, including the shared prefix.
				got, ok := sourceForRoot(report, target.otherRoot)
				if !ok {
					t.Fatalf("other source %q vanished after editing %q", target.otherRoot, target.editedRoot)
				}
				if !reflect.DeepEqual(got, target.other) {
					t.Errorf("other source = %+v, want untouched %+v", got, target.other)
				}

				// The edit really landed on the record the caller changed.
				if edited, ok := sourceForRoot(report, target.editedRoot); ok && reflect.DeepEqual(edited, target.other) {
					t.Errorf("editing %q had no local effect: %+v", target.editedRoot, edited)
				}

				// Report-level identifiers are separate fields from the paths:
				// rewriting a path's query name never moves Dataset.
				if report.Dataset != "T" {
					t.Errorf("report Dataset = %q, want %q", report.Dataset, "T")
				}
				if report.ContentID != snap.ContentID {
					t.Errorf("report ContentID = %q, want %q", report.ContentID, snap.ContentID)
				}

				// The input snapshot keeps every dataset name and direct
				// upstream relation.
				if got := snapshotDatasetsCopy(snap); !reflect.DeepEqual(got, wantIndependenceSnapshotDatasets) {
					t.Errorf("snapshot changed after editing a report record:\n got %v\nwant %v", got, wantIndependenceSnapshotDatasets)
				}
			})
		}
	}
}

// TestTraceReorderingAndRemovingSourcesLeavesSnapshotAndRequeryUntouched pins
// the array-level edits: swapping the two source records, mutating a path after
// the swap, and dropping a record from the array must not move the snapshot,
// and a fresh query still reports both roots with their full original paths.
func TestTraceReorderingAndRemovingSourcesLeavesSnapshotAndRequeryUntouched(t *testing.T) {
	snap := snapshotOf(t, traceIndependenceGraphJSON)
	wantID := snap.ContentID
	snapshotBytes := mustMarshalSnapshot(t, snap)

	report := traceReportOf(t, snap, "T")
	report.Sources[0], report.Sources[1] = report.Sources[1], report.Sources[0]

	// After the swap each record still owns its own complete path.
	if got, ok := sourceForRoot(report, "R1"); !ok || !reflect.DeepEqual(got, wantIndependenceTSources[0]) {
		t.Errorf("R1 after swap = %+v ok=%v, want %+v", got, ok, wantIndependenceTSources[0])
	}
	if got, ok := sourceForRoot(report, "R2"); !ok || !reflect.DeepEqual(got, wantIndependenceTSources[1]) {
		t.Errorf("R2 after swap = %+v ok=%v, want %+v", got, ok, wantIndependenceTSources[1])
	}
	// Mutating through the swapped position edits the record now held there
	// (R2's), never R1's record.
	report.Sources[0].Path[0] = "HACK"
	if got, ok := sourceForRoot(report, "R1"); !ok || !reflect.DeepEqual(got, wantIndependenceTSources[0]) {
		t.Errorf("R1 changed after editing the swapped R2 record: %+v ok=%v", got, ok)
	}

	// A separate report from which the caller removes one record.
	pruned := traceReportOf(t, snap, "T")
	pruned.Sources = pruned.Sources[:1]
	if len(pruned.Sources) != 1 || pruned.Sources[0].Root != "R1" {
		t.Fatalf("pruned report = %+v, want only R1", pruned.Sources)
	}

	// The snapshot is unchanged: content id, graph, export bytes.
	if snap.ContentID != wantID {
		t.Errorf("content id changed: %q", snap.ContentID)
	}
	if got := snapshotDatasetsCopy(snap); !reflect.DeepEqual(got, wantIndependenceSnapshotDatasets) {
		t.Errorf("snapshot changed:\n got %v\nwant %v", got, wantIndependenceSnapshotDatasets)
	}
	if got := mustMarshalSnapshot(t, snap); !bytes.Equal(got, snapshotBytes) {
		t.Errorf("snapshot export changed:\n got %s\nwant %s", got, snapshotBytes)
	}

	// A fresh query still sees both roots and both full paths.
	fresh := traceReportOf(t, snap, "T")
	if !reflect.DeepEqual(fresh.Sources, wantIndependenceTSources) {
		t.Errorf("fresh query after edits = %+v, want %+v", fresh.Sources, wantIndependenceTSources)
	}
	if fresh.ContentID != snap.ContentID || fresh.Dataset != "T" {
		t.Errorf("fresh report metadata = %q %q, want %q %q", fresh.ContentID, fresh.Dataset, snap.ContentID, "T")
	}
}

// TestTraceReturnedReportsAreIndependentOfEachOther verifies that with several
// reports held at once — two of the same query and one of another downstream
// through the same shared intermediate — thoroughly editing one report leaves
// the others representing their return-time results, the snapshot frozen, and
// fresh queries of T and U reporting the original sources and paths.
func TestTraceReturnedReportsAreIndependentOfEachOther(t *testing.T) {
	snap := snapshotOf(t, traceIndependenceGraphJSON)
	first := traceReportOf(t, snap, "T")
	second := traceReportOf(t, snap, "T")
	downU := traceReportOf(t, snap, "U")

	wantSecond := mustMarshalTraceReport(t, second)
	wantU := mustMarshalTraceReport(t, downU)
	snapshotBytes := mustMarshalSnapshot(t, snap)

	// Edit the first report as thoroughly as a caller might: report-level
	// identifiers, both root fields, every path position, appends into both
	// paths (including the shared prefix), and a fabricated extra source.
	first.ContentID = "HACK"
	first.Dataset = "HACK"
	first.Sources[0].Root = "HACK"
	first.Sources[0].Path[0] = "HACK"
	first.Sources[0].Path[1] = "HACK"
	first.Sources[0].Path[2] = "HACK"
	first.Sources[0].Path = append(first.Sources[0].Path, "EXTRA")
	first.Sources[1].Path = append(first.Sources[1].Path, "EXTRA")
	first.Sources = append(first.Sources, SourceTrace{Root: "FAKE", Path: []string{"FAKE"}})

	// The untouched report of the same query is byte-identical to what T
	// returned.
	if got := mustMarshalTraceReport(t, second); !bytes.Equal(got, wantSecond) {
		t.Errorf("second T report changed after editing the first:\n got %s\nwant %s", got, wantSecond)
	}
	if !reflect.DeepEqual(second.Sources, wantIndependenceTSources) {
		t.Errorf("second T sources = %+v, want %+v", second.Sources, wantIndependenceTSources)
	}
	if second.Dataset != "T" || second.ContentID != snap.ContentID {
		t.Errorf("second T metadata = %q %q, want %q %q", second.Dataset, second.ContentID, "T", snap.ContentID)
	}

	// The report for the other downstream through the same M is independent as
	// well.
	if got := mustMarshalTraceReport(t, downU); !bytes.Equal(got, wantU) {
		t.Errorf("U report changed after editing T's report:\n got %s\nwant %s", got, wantU)
	}
	if !reflect.DeepEqual(downU.Sources, wantIndependenceUSources) {
		t.Errorf("U sources = %+v, want %+v", downU.Sources, wantIndependenceUSources)
	}

	// The snapshot stays frozen.
	if got := mustMarshalSnapshot(t, snap); !bytes.Equal(got, snapshotBytes) {
		t.Errorf("snapshot export changed:\n got %s\nwant %s", got, snapshotBytes)
	}
	if got := snapshotDatasetsCopy(snap); !reflect.DeepEqual(got, wantIndependenceSnapshotDatasets) {
		t.Errorf("snapshot datasets changed:\n got %v\nwant %v", got, wantIndependenceSnapshotDatasets)
	}

	// Fresh queries still describe the frozen snapshot, not the edited report.
	freshT := traceReportOf(t, snap, "T")
	if !reflect.DeepEqual(freshT.Sources, wantIndependenceTSources) {
		t.Errorf("fresh T sources = %+v, want %+v", freshT.Sources, wantIndependenceTSources)
	}
	if freshT.ContentID != snap.ContentID || freshT.Dataset != "T" {
		t.Errorf("fresh T metadata = %q %q, want %q %q", freshT.ContentID, freshT.Dataset, snap.ContentID, "T")
	}
	freshU := traceReportOf(t, snap, "U")
	if !reflect.DeepEqual(freshU.Sources, wantIndependenceUSources) {
		t.Errorf("fresh U sources = %+v, want %+v", freshU.Sources, wantIndependenceUSources)
	}
	if freshU.ContentID != snap.ContentID || freshU.Dataset != "U" {
		t.Errorf("fresh U metadata = %q %q, want %q %q", freshU.ContentID, freshU.Dataset, snap.ContentID, "U")
	}
}

// TestTraceRootSingleElementPathIsIndependent pins the same guarantees for a
// root query: its one-element path [R1] and root field are independently owned,
// so editing them leaves the other held reports (another root query and a deep
// query), the snapshot export, and a fresh root query untouched.
func TestTraceRootSingleElementPathIsIndependent(t *testing.T) {
	snap := snapshotOf(t, traceIndependenceGraphJSON)
	root := traceReportOf(t, snap, "R1")
	other := traceReportOf(t, snap, "R1")
	deep := traceReportOf(t, snap, "T")
	wantOther := mustMarshalTraceReport(t, other)
	wantDeep := mustMarshalTraceReport(t, deep)
	snapshotBytes := mustMarshalSnapshot(t, snap)

	if got := root.Sources; !reflect.DeepEqual(got, []SourceTrace{{Root: "R1", Path: []string{"R1"}}}) {
		t.Fatalf("root sources = %+v, want single-element [R1]", got)
	}

	root.Sources[0].Root = "HACK"
	root.Sources[0].Path[0] = "HACK"
	root.Sources[0].Path = append(root.Sources[0].Path, "EXTRA")

	if got := mustMarshalTraceReport(t, other); !bytes.Equal(got, wantOther) {
		t.Errorf("other root report changed:\n got %s\nwant %s", got, wantOther)
	}
	if got := other.Sources; !reflect.DeepEqual(got, []SourceTrace{{Root: "R1", Path: []string{"R1"}}}) {
		t.Errorf("other root sources = %+v, want untouched single-element [R1]", got)
	}
	if got := mustMarshalTraceReport(t, deep); !bytes.Equal(got, wantDeep) {
		t.Errorf("deep T report changed after editing a root report:\n got %s\nwant %s", got, wantDeep)
	}
	if got := mustMarshalSnapshot(t, snap); !bytes.Equal(got, snapshotBytes) {
		t.Errorf("snapshot export changed:\n got %s\nwant %s", got, snapshotBytes)
	}

	fresh := traceReportOf(t, snap, "R1")
	if !reflect.DeepEqual(fresh.Sources, []SourceTrace{{Root: "R1", Path: []string{"R1"}}}) {
		t.Errorf("fresh root sources = %+v, want [R1]", fresh.Sources)
	}
	if fresh.ContentID != snap.ContentID || fresh.Dataset != "R1" {
		t.Errorf("fresh root metadata = %q %q, want %q %q", fresh.ContentID, fresh.Dataset, snap.ContentID, "R1")
	}
}

// TestTraceFailedQueriesAfterSuccessProduceNoReportAndChangeNothing pins the
// error boundary while a successful report is held open: an empty name keeps
// returning ErrInvalidArgument, an unknown name keeps returning ErrNotFound,
// neither returns a success report, and both leave the earlier report, its
// content identifier, and the input snapshot exactly as they were.
func TestTraceFailedQueriesAfterSuccessProduceNoReportAndChangeNothing(t *testing.T) {
	snap := snapshotOf(t, traceIndependenceGraphJSON)
	wantID := snap.ContentID
	ok := traceReportOf(t, snap, "T")
	wantReport := mustMarshalTraceReport(t, ok)
	snapshotBytes := mustMarshalSnapshot(t, snap)
	beforeDatasets := snapshotDatasetsCopy(snap)

	if report, err := TraceSources(snap, ""); report != nil || !errors.Is(err, ErrInvalidArgument) {
		t.Errorf("empty query: report=%+v err=%v, want nil report and ErrInvalidArgument", report, err)
	}
	if report, err := TraceSources(snap, "ghost"); report != nil || !errors.Is(err, ErrNotFound) {
		t.Errorf("unknown query: report=%+v err=%v, want nil report and ErrNotFound", report, err)
	}

	if got := mustMarshalTraceReport(t, ok); !bytes.Equal(got, wantReport) {
		t.Errorf("earlier report changed after failed queries:\n got %s\nwant %s", got, wantReport)
	}
	if !reflect.DeepEqual(ok.Sources, wantIndependenceTSources) {
		t.Errorf("earlier sources = %+v, want %+v", ok.Sources, wantIndependenceTSources)
	}
	if snap.ContentID != wantID {
		t.Errorf("snapshot content id changed: %q", snap.ContentID)
	}
	if got := snapshotDatasetsCopy(snap); !reflect.DeepEqual(got, beforeDatasets) {
		t.Errorf("snapshot datasets changed after failed queries:\n got %v\nwant %v", got, beforeDatasets)
	}
	if got := mustMarshalSnapshot(t, snap); !bytes.Equal(got, snapshotBytes) {
		t.Errorf("snapshot export changed after failed queries:\n got %s\nwant %s", got, snapshotBytes)
	}

	// A successful query still describes the frozen snapshot.
	fresh := traceReportOf(t, snap, "T")
	if !reflect.DeepEqual(fresh.Sources, wantIndependenceTSources) {
		t.Errorf("fresh sources = %+v, want %+v", fresh.Sources, wantIndependenceTSources)
	}
}

// TestTraceSnapshotUnchangedBeforeAndAfterTracing compares the frozen document
// across successful and failed queries as a whole: the same content identifier,
// the same graph, and the same export bytes come out after tracing T, U, a
// root, and the rejected queries, and those bytes still parse as a legal
// snapshot whose id matches its graph.
func TestTraceSnapshotUnchangedBeforeAndAfterTracing(t *testing.T) {
	snap := snapshotOf(t, traceIndependenceGraphJSON)
	before := mustMarshalSnapshot(t, snap)

	for _, dataset := range []string{"T", "U", "R1", "R2", "M"} {
		if report, err := TraceSources(snap, dataset); err != nil {
			t.Fatalf("TraceSources(%q): %v", dataset, err)
		} else {
			// Display-edit the returned report, which must never write back.
			report.Sources[0].Path[0] = "HACK"
			report.Sources = append(report.Sources, SourceTrace{Root: "FAKE", Path: []string{"FAKE"}})
		}
	}
	if _, err := TraceSources(snap, ""); !errors.Is(err, ErrInvalidArgument) {
		t.Fatalf("empty query err = %v, want ErrInvalidArgument", err)
	}
	if _, err := TraceSources(snap, "ghost"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("unknown query err = %v, want ErrNotFound", err)
	}

	after := mustMarshalSnapshot(t, snap)
	if !bytes.Equal(after, before) {
		t.Errorf("snapshot export differs after tracing:\n got %s\nwant %s", after, before)
	}
	parsed, err := ParseSnapshot(after)
	if err != nil {
		t.Fatalf("snapshot no longer parses after tracing: %v", err)
	}
	if parsed.ContentID != snap.ContentID {
		t.Errorf("parsed id = %q, want %q", parsed.ContentID, snap.ContentID)
	}
	if !reflect.DeepEqual(parsed.Graph, snap.Graph) {
		t.Errorf("parsed graph differs:\n got %#v\nwant %#v", parsed.Graph, snap.Graph)
	}
	if got := snapshotDatasetsCopy(snap); !reflect.DeepEqual(got, wantIndependenceSnapshotDatasets) {
		t.Errorf("snapshot datasets = %v, want %v", got, wantIndependenceSnapshotDatasets)
	}
}

// TestTraceIndependentReportStillObeysBusinessRulesWithUnicodeNames verifies on
// the independence results themselves that the existing business rules still
// hold and that name spellings survive verbatim: Chinese characters, ASCII
// case, and surrounding spaces are all kept; each root appears once; the
// shortest route wins; equal-length ties are decided element by element from
// the query end by UTF-8 byte order; sources are ordered by root name. Editing
// the returned names and re-querying returns the original frozen answer.
func TestTraceIndependentReportStillObeysBusinessRulesWithUnicodeNames(t *testing.T) {
	// Q reaches root r in two equal-length relations — via A or via a — and
	// reaches the two spaced/Chinese roots through the spaced intermediate
	// " 中间 ". "A" (0x41) beats "a" (0x61) at the first differing position.
	graphJSON := `{"datasets":[
		{"name":"Q","upstreams":["A","a"," 中间 "]},
		{"name":"A","upstreams":["r"]},
		{"name":"a","upstreams":["r"]},
		{"name":"r","upstreams":[]},
		{"name":" 中间 ","upstreams":[" 根 ","根"]},
		{"name":" 根 ","upstreams":[]},
		{"name":"根","upstreams":[]}
	]}`
	snap := snapshotOf(t, graphJSON)

	// Roots sort by UTF-8 byte order: the space-prefixed name (0x20) first,
	// then "r" (0x72), then the Chinese root (0xE6...).
	want := []SourceTrace{
		{Root: " 根 ", Path: []string{"Q", " 中间 ", " 根 "}},
		{Root: "r", Path: []string{"Q", "A", "r"}},
		{Root: "根", Path: []string{"Q", " 中间 ", "根"}},
	}
	report := traceReportOf(t, snap, "Q")
	if !reflect.DeepEqual(report.Sources, want) {
		t.Fatalf("Sources = %+v\nwant %+v", report.Sources, want)
	}
	if report.ContentID != snap.ContentID || report.Dataset != "Q" {
		t.Errorf("metadata = %q %q, want %q %q", report.ContentID, report.Dataset, snap.ContentID, "Q")
	}

	// Each root appears exactly once.
	seen := map[string]int{}
	for _, src := range report.Sources {
		seen[src.Root]++
	}
	for root, count := range seen {
		if count != 1 {
			t.Errorf("root %q reported %d times, want exactly once", root, count)
		}
	}

	// The spaced intermediate query keeps every spelling verbatim, and the
	// spaced root's own query is a single-element path with its spaces intact.
	mid := traceReportOf(t, snap, " 中间 ")
	wantMid := []SourceTrace{
		{Root: " 根 ", Path: []string{" 中间 ", " 根 "}},
		{Root: "根", Path: []string{" 中间 ", "根"}},
	}
	if !reflect.DeepEqual(mid.Sources, wantMid) {
		t.Errorf("intermediate sources = %+v, want %+v", mid.Sources, wantMid)
	}
	spacedRoot := traceReportOf(t, snap, " 根 ")
	if got := spacedRoot.Sources; !reflect.DeepEqual(got, []SourceTrace{{Root: " 根 ", Path: []string{" 根 "}}}) {
		t.Errorf("spaced root sources = %+v, want single-element verbatim path", got)
	}

	// Edit every name position of every returned path, then re-query: the
	// frozen answer must come back byte-for-byte, proving the independence copy
	// preserved the exact spellings.
	for i := range report.Sources {
		for j := range report.Sources[i].Path {
			report.Sources[i].Path[j] = " 改 A "
		}
		report.Sources[i].Root = " 改 A "
		report.Sources[i].Path = append(report.Sources[i].Path, "追加")
	}
	for i := range mid.Sources {
		mid.Sources[i].Path[0] = "HACK"
	}

	fresh := traceReportOf(t, snap, "Q")
	if !reflect.DeepEqual(fresh.Sources, want) {
		t.Errorf("fresh sources after edits = %+v, want %+v", fresh.Sources, want)
	}
	if fresh.Dataset != "Q" || fresh.ContentID != snap.ContentID {
		t.Errorf("fresh metadata = %q %q, want %q %q", fresh.Dataset, fresh.ContentID, "Q", snap.ContentID)
	}
	freshMid := traceReportOf(t, snap, " 中间 ")
	if !reflect.DeepEqual(freshMid.Sources, wantMid) {
		t.Errorf("fresh intermediate sources = %+v, want %+v", freshMid.Sources, wantMid)
	}
}
