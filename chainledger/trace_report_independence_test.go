package chainledger

import (
	"bytes"
	"encoding/json"
	"errors"
	"reflect"
	"testing"
)

// This file pins the report-independence contract of TraceSources. A caller
// that edits a returned report for presentation must only ever move its own
// copy:
//
//   - replacing the queried name, an intermediate name, or the root name on
//     one source record, or appending a name to that path, must change only
//     the record being edited — even when the two paths share a prefix;
//   - renaming a source's root, reordering the sources array, or removing a
//     source from it must never reach the input snapshot's dataset names or
//     direct upstream relationships;
//
// Two reports taken from one snapshot (possibly of two downstreams passing
// through the same intermediate dataset) own their slices independently; a
// re-query after edits still returns the snapshot's original sources and
// paths with the correct content identifier and queried name. A root query's
// single-element path owns the same independence. Failed follow-up queries
// (empty or unknown name) keep the existing error categories, produce no
// success report, and leave prior reports and the snapshot untouched.
//
// The independence-bearing reports must still obey the existing business
// rules: every root appears exactly once, the path with the fewest direct
// relations is chosen, ties are broken element by element from the query end
// in UTF-8 byte order, sources are sorted by root name, names keep their
// Chinese characters, casing, and surrounding spaces verbatim, and the path
// is the array from the queried dataset to the root including both ends.

// traceSharedIntermediateJSON is the headline shape: downstream D and the
// second downstream E reach two roots through their own prefix datasets and
// then the SAME intermediate M:
//
//	D -> P -> M -> {R1,R2}
//	E -> Q -> M -> {R1,R2}
//
// D's two paths share the prefix [D P M] and diverge only at the root. At
// four names each, a careless append-based builder would grow both paths in
// the same backing array (the [D P M] slice is allocated with room for one
// more name), so the two records are the tempting aliasing case.
const traceSharedIntermediateJSON = `{"datasets":[
	{"name":"D","upstreams":["P"]},
	{"name":"E","upstreams":["Q"]},
	{"name":"P","upstreams":["M"]},
	{"name":"Q","upstreams":["M"]},
	{"name":"M","upstreams":["R2","R1"]},
	{"name":"R1","upstreams":[]},
	{"name":"R2","upstreams":[]}
]}`

// traceReportJSON renders a trace report the way a caller displaying it
// would, failing the test on an impossible-on-valid-input error.
func traceReportJSON(t *testing.T, r *TraceReport) []byte {
	t.Helper()
	b, err := json.Marshal(r)
	if err != nil {
		t.Fatalf("json.Marshal trace report unexpected error: %v", err)
	}
	return b
}

// sourceByRoot returns the source record whose Root equals root, or nil if
// absent (a reorder/remove test can legitimately remove it).
func sourceByRoot(r *TraceReport, root string) *SourceTrace {
	for i := range r.Sources {
		if r.Sources[i].Root == root {
			return &r.Sources[i]
		}
	}
	return nil
}

// TestTraceEditingOnePathLeavesOtherPathAndSnapshotUntouched is the headline
// independence guarantee: on a report whose two paths share a prefix, every
// kind of edit a caller might make on one path — overwrite the queried name,
// overwrite the shared intermediate name, overwrite the root name, and
// append onto that path — changes only that one record. The other source
// keeps its original root and complete path, and the input snapshot's
// dataset names and direct upstreams do not move.
func TestTraceEditingOnePathLeavesOtherPathAndSnapshotUntouched(t *testing.T) {
	snap := snapshotOf(t, traceSharedIntermediateJSON)
	wantGraph := snap.Graph
	wantID := snap.ContentID
	wantBytes := mustMarshalSnapshot(t, snap)

	for _, tc := range []struct {
		name   string
		edit   func(r *TraceReport)
		edited func(r *TraceReport) SourceTrace
	}{
		{
			name: "replace queried name on R1 path",
			edit: func(r *TraceReport) {
				sourceByRoot(r, "R1").Path[0] = "QUERY-X"
			},
			edited: func(r *TraceReport) SourceTrace {
				return SourceTrace{Root: "R1", Path: []string{"QUERY-X", "P", "M", "R1"}}
			},
		},
		{
			name: "replace shared intermediate name on R1 path",
			edit: func(r *TraceReport) {
				sourceByRoot(r, "R1").Path[2] = "MID-X"
			},
			edited: func(r *TraceReport) SourceTrace {
				return SourceTrace{Root: "R1", Path: []string{"D", "P", "MID-X", "R1"}}
			},
		},
		{
			name: "replace root name of R1 record",
			edit: func(r *TraceReport) {
				sourceByRoot(r, "R1").Root = "ROOT-X"
			},
			edited: func(r *TraceReport) SourceTrace {
				return SourceTrace{Root: "ROOT-X", Path: []string{"D", "P", "M", "R1"}}
			},
		},
		{
			name: "append a name to the R1 path",
			edit: func(r *TraceReport) {
				s := sourceByRoot(r, "R1")
				s.Path = append(s.Path, "TAIL-X")
			},
			edited: func(r *TraceReport) SourceTrace {
				return SourceTrace{Root: "R1", Path: []string{"D", "P", "M", "R1", "TAIL-X"}}
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			report, err := TraceSources(snap, "D")
			if err != nil {
				t.Fatalf("TraceSources: %v", err)
			}
			wantSources := []SourceTrace{
				{Root: "R1", Path: []string{"D", "P", "M", "R1"}},
				{Root: "R2", Path: []string{"D", "P", "M", "R2"}},
			}
			if !reflect.DeepEqual(report.Sources, wantSources) {
				t.Fatalf("Sources = %+v, want %+v", report.Sources, wantSources)
			}

			tc.edit(report)

			// The other source record keeps its original root and COMPLETE
			// original path, even though both paths started [D P M].
			if got := sourceByRoot(report, "R2"); got == nil {
				t.Fatalf("R2 source vanished after editing the R1 record")
			} else if want := (SourceTrace{Root: "R2", Path: []string{"D", "P", "M", "R2"}}); !reflect.DeepEqual(*got, want) {
				t.Errorf("R2 record = %+v, want untouched %+v", *got, want)
			}
			// The edited record is exactly the caller's edit.
			editedRec := SourceTrace{}
			for _, s := range report.Sources {
				if s.Root == tc.edited(report).Root {
					editedRec = s
				}
			}
			if want := tc.edited(report); !reflect.DeepEqual(editedRec, want) {
				t.Errorf("edited record = %+v, want %+v", editedRec, want)
			}

			// The input snapshot did not follow the edit: same content id,
			// same graph, same export bytes.
			if got := snap.ContentID; got != wantID {
				t.Errorf("snapshot content id moved: %q, want %q", got, wantID)
			}
			if !reflect.DeepEqual(snap.Graph, wantGraph) {
				t.Errorf("snapshot graph moved:\n got %#v\nwant %#v", snap.Graph, wantGraph)
			}
			if got := mustMarshalSnapshot(t, snap); !bytes.Equal(got, wantBytes) {
				t.Errorf("snapshot export moved:\n got %s\nwant %s", got, wantBytes)
			}
			if got := mustSnapshotDataset(t, snap, "M").Upstreams; !reflect.DeepEqual(got, []string{"R1", "R2"}) {
				t.Errorf("snapshot M.upstreams = %v, want frozen [R1 R2]", got)
			}
			if got := mustSnapshotDataset(t, snap, "D").Upstreams; !reflect.DeepEqual(got, []string{"P"}) {
				t.Errorf("snapshot D.upstreams = %v, want frozen [P]", got)
			}
		})
	}
}

// TestTraceEditingSourceMetadataLeavesSnapshotAndOtherPathsUntouched covers
// the array-level edits: rename a source's root in place, swap the sources
// order, and remove one source. None of these may move the snapshot's
// dataset names or direct upstreams, and the surviving record keeps its full
// path even after a backing-array reorder.
func TestTraceEditingSourceMetadataLeavesSnapshotAndOtherPathsUntouched(t *testing.T) {
	for _, tc := range []struct {
		name string
		edit func(r *TraceReport)
		// remaining maps the surviving roots to their expected full paths.
		remaining map[string][]string
	}{
		{
			name: "rename R1 root in place",
			edit: func(r *TraceReport) { sourceByRoot(r, "R1").Root = "HACK" },
			remaining: map[string][]string{
				"HACK": {"D", "P", "M", "R1"},
				"R2":   {"D", "P", "M", "R2"},
			},
		},
		{
			name: "reorder sources array",
			edit: func(r *TraceReport) {
				r.Sources[0], r.Sources[1] = r.Sources[1], r.Sources[0]
			},
			remaining: map[string][]string{
				"R1": {"D", "P", "M", "R1"},
				"R2": {"D", "P", "M", "R2"},
			},
		},
		{
			name: "remove one source",
			edit: func(r *TraceReport) {
				r.Sources = r.Sources[:1]
			},
			remaining: map[string][]string{
				"R1": {"D", "P", "M", "R1"},
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			snap := snapshotOf(t, traceSharedIntermediateJSON)
			wantGraph := snap.Graph
			wantID := snap.ContentID
			wantBytes := mustMarshalSnapshot(t, snap)

			report, err := TraceSources(snap, "D")
			if err != nil {
				t.Fatalf("TraceSources: %v", err)
			}
			tc.edit(report)

			if len(report.Sources) != len(tc.remaining) {
				t.Fatalf("sources count = %d, want %d: %+v", len(report.Sources), len(tc.remaining), report.Sources)
			}
			for _, s := range report.Sources {
				wantPath, ok := tc.remaining[s.Root]
				if !ok {
					t.Errorf("unexpected surviving source %+v", s)
					continue
				}
				if !reflect.DeepEqual(s.Path, wantPath) {
					t.Errorf("source %q path = %v, want untouched %v", s.Root, s.Path, wantPath)
				}
			}

			if got := snap.ContentID; got != wantID {
				t.Errorf("snapshot content id = %q, want %q", got, wantID)
			}
			if !reflect.DeepEqual(snap.Graph, wantGraph) {
				t.Errorf("snapshot graph moved:\n got %#v\nwant %#v", snap.Graph, wantGraph)
			}
			if got := mustMarshalSnapshot(t, snap); !bytes.Equal(got, wantBytes) {
				t.Errorf("snapshot export moved:\n got %s\nwant %s", got, wantBytes)
			}
			names := map[string]bool{}
			for _, ds := range snap.Graph.Datasets {
				names[ds.Name] = true
			}
			for _, want := range []string{"D", "E", "P", "Q", "M", "R1", "R2"} {
				if !names[want] {
					t.Errorf("snapshot lost dataset %q after a sources-array edit", want)
				}
			}
			if got := mustSnapshotDataset(t, snap, "R1").Upstreams; len(got) != 0 {
				t.Errorf("snapshot R1.upstreams = %v, want frozen empty", got)
			}
			if got := mustSnapshotDataset(t, snap, "R2").Upstreams; len(got) != 0 {
				t.Errorf("snapshot R2.upstreams = %v, want frozen empty", got)
			}
		})
	}
}

// TestTraceTwoReportsOfSameSnapshotAreIndependent pins that two reports
// obtained successively from one snapshot — same query, then a different
// downstream that passes through the SAME intermediate dataset — start
// equal, but editing one leaves the other still representing the result as
// of when it was taken. After the edits, re-querying the original dataset
// still returns the snapshot's original sources and paths.
func TestTraceTwoReportsOfSameSnapshotAreIndependent(t *testing.T) {
	snap := snapshotOf(t, traceSharedIntermediateJSON)
	wantID := snap.ContentID
	wantBytes := mustMarshalSnapshot(t, snap)

	first, err := TraceSources(snap, "D")
	if err != nil {
		t.Fatalf("first TraceSources: %v", err)
	}
	second, err := TraceSources(snap, "D")
	if err != nil {
		t.Fatalf("second TraceSources: %v", err)
	}
	// A third query: another downstream through the same intermediate.
	otherDownstream, err := TraceSources(snap, "E")
	if err != nil {
		t.Fatalf("TraceSources(E): %v", err)
	}
	wantD := []SourceTrace{
		{Root: "R1", Path: []string{"D", "P", "M", "R1"}},
		{Root: "R2", Path: []string{"D", "P", "M", "R2"}},
	}
	wantE := []SourceTrace{
		{Root: "R1", Path: []string{"E", "Q", "M", "R1"}},
		{Root: "R2", Path: []string{"E", "Q", "M", "R2"}},
	}
	if !reflect.DeepEqual(first.Sources, wantD) || !reflect.DeepEqual(second.Sources, wantD) {
		t.Fatalf("initial D reports = %+v / %+v, want %+v", first.Sources, second.Sources, wantD)
	}
	if !reflect.DeepEqual(otherDownstream.Sources, wantE) {
		t.Fatalf("E report = %+v, want %+v", otherDownstream.Sources, wantE)
	}
	secondJSON := traceReportJSON(t, second)
	otherJSON := traceReportJSON(t, otherDownstream)

	// Edit the first report exhaustively: both path names in place, append
	// onto the path, rename the root, reorder, and remove the second record.
	s1 := sourceByRoot(first, "R1")
	s1.Path[0] = "QUERY-X"
	s1.Path[1] = "MID-X"
	s1.Path = append(s1.Path, "TAIL-X")
	s1.Root = "ROOT-X"
	first.Sources[1].Root = "ROOT-Y"
	first.Sources[0], first.Sources[1] = first.Sources[1], first.Sources[0]
	first.Dataset = "EDITED"
	first.ContentID = "EDITED-ID"

	if got := traceReportJSON(t, second); !bytes.Equal(got, secondJSON) {
		t.Errorf("second report changed after the first was edited:\n got %s\nwant %s", got, secondJSON)
	}
	if !reflect.DeepEqual(second.Sources, wantD) {
		t.Errorf("second report Sources = %+v, want untouched %+v", second.Sources, wantD)
	}
	if got := sourceByRoot(second, "R1").Path; !reflect.DeepEqual(got, []string{"D", "P", "M", "R1"}) {
		t.Errorf("second report R1 path = %v, want [D P M R1]", got)
	}
	if got := sourceByRoot(second, "R2").Path; !reflect.DeepEqual(got, []string{"D", "P", "M", "R2"}) {
		t.Errorf("second report R2 path = %v, want [D P M R2]", got)
	}
	if got := sourceByRoot(otherDownstream, "R1").Path; !reflect.DeepEqual(got, []string{"E", "Q", "M", "R1"}) {
		t.Errorf("E report R1 path = %v, want [E Q M R1]", got)
	}
	if got := traceReportJSON(t, otherDownstream); !bytes.Equal(got, otherJSON) {
		t.Errorf("E report changed after the D report was edited:\n got %s\nwant %s", got, otherJSON)
	}

	// Re-query after the edits: the snapshot answers with the original
	// sources and paths, content id and queried name correct.
	again, err := TraceSources(snap, "D")
	if err != nil {
		t.Fatalf("re-query TraceSources: %v", err)
	}
	if !reflect.DeepEqual(again.Sources, wantD) {
		t.Errorf("re-query Sources = %+v, want original %+v", again.Sources, wantD)
	}
	if got := again.ContentID; got != wantID {
		t.Errorf("re-query ContentID = %q, want %q", got, wantID)
	}
	if got := again.Dataset; got != "D" {
		t.Errorf("re-query Dataset = %q, want %q", got, "D")
	}
	if got := mustMarshalSnapshot(t, snap); !bytes.Equal(got, wantBytes) {
		t.Errorf("snapshot export changed after report edits/re-query:\n got %s\nwant %s", got, wantBytes)
	}
}

// TestTraceRootSingleElementPathIsIndependent pins the same independence for
// the minimal result: a root queried about itself gets a one-element path;
// editing it (in place, by append, by root rename) and removing the only
// record neither moves another report nor the snapshot, and a re-query still
// returns the single-element answer.
func TestTraceRootSingleElementPathIsIndependent(t *testing.T) {
	snap := snapshotOf(t, traceSharedIntermediateJSON)
	wantBytes := mustMarshalSnapshot(t, snap)

	first, err := TraceSources(snap, "R1")
	if err != nil {
		t.Fatalf("first TraceSources(R1): %v", err)
	}
	wantRoot := []SourceTrace{{Root: "R1", Path: []string{"R1"}}}
	if !reflect.DeepEqual(first.Sources, wantRoot) {
		t.Fatalf("Sources = %+v, want %+v", first.Sources, wantRoot)
	}
	second, err := TraceSources(snap, "R1")
	if err != nil {
		t.Fatalf("second TraceSources(R1): %v", err)
	}
	secondJSON := traceReportJSON(t, second)

	// Overwrite the single name in place, append a tail, rename the root,
	// then drop the only record entirely.
	first.Sources[0].Path[0] = "SELF-X"
	if sourceByRoot(second, "R1") == nil {
		t.Fatalf("second report lost its record before any edit reached it")
	}
	if got := sourceByRoot(second, "R1").Path; !reflect.DeepEqual(got, []string{"R1"}) {
		t.Errorf("second report path = %v, want [R1]", got)
	}
	first.Sources[0].Path = append(first.Sources[0].Path, "TAIL-X")
	first.Sources[0].Root = "ROOT-X"
	if got := traceReportJSON(t, second); !bytes.Equal(got, secondJSON) {
		t.Errorf("second root report changed:\n got %s\nwant %s", got, secondJSON)
	}
	first.Sources = first.Sources[:0]

	again, err := TraceSources(snap, "R1")
	if err != nil {
		t.Fatalf("re-query TraceSources(R1): %v", err)
	}
	if !reflect.DeepEqual(again.Sources, wantRoot) {
		t.Errorf("re-query Sources = %+v, want %+v", again.Sources, wantRoot)
	}
	if got := again.ContentID; got != snap.ContentID {
		t.Errorf("re-query ContentID = %q, want %q", got, snap.ContentID)
	}
	if got := mustMarshalSnapshot(t, snap); !bytes.Equal(got, wantBytes) {
		t.Errorf("snapshot export moved:\n got %s\nwant %s", got, wantBytes)
	}
}

// TestTraceFailedFollowUpQueriesKeepPriorReportAndSnapshot pins the error
// boundary around a held success report: while that report is retained,
// querying an empty name or a name absent from the snapshot must fail with
// the existing error categories, return no usable report, and leave the
// earlier success report and the input snapshot exactly as they were.
func TestTraceFailedFollowUpQueriesKeepPriorReportAndSnapshot(t *testing.T) {
	snap := snapshotOf(t, traceSharedIntermediateJSON)
	wantBytes := mustMarshalSnapshot(t, snap)

	prior, err := TraceSources(snap, "D")
	if err != nil {
		t.Fatalf("prior TraceSources: %v", err)
	}
	priorJSON := traceReportJSON(t, prior)
	wantSources := []SourceTrace{
		{Root: "R1", Path: []string{"D", "P", "M", "R1"}},
		{Root: "R2", Path: []string{"D", "P", "M", "R2"}},
	}

	for _, tc := range []struct {
		name    string
		dataset string
		wantErr error
	}{
		{"empty name", "", ErrInvalidArgument},
		{"unknown name", "ghost", ErrNotFound},
	} {
		t.Run(tc.name, func(t *testing.T) {
			bad, err := TraceSources(snap, tc.dataset)
			if !errors.Is(err, tc.wantErr) {
				t.Fatalf("TraceSources(%q) err = %v, want %v", tc.dataset, err, tc.wantErr)
			}
			if bad != nil {
				t.Fatalf("failed query returned a usable report: %#v", bad)
			}
			if got := traceReportJSON(t, prior); !bytes.Equal(got, priorJSON) {
				t.Errorf("prior success report moved after a failed query:\n got %s\nwant %s", got, priorJSON)
			}
			if !reflect.DeepEqual(prior.Sources, wantSources) {
				t.Errorf("prior Sources = %+v, want %+v", prior.Sources, wantSources)
			}
			if got := prior.ContentID; got != snap.ContentID {
				t.Errorf("prior ContentID = %q, want %q", got, snap.ContentID)
			}
			if got := mustMarshalSnapshot(t, snap); !bytes.Equal(got, wantBytes) {
				t.Errorf("snapshot export moved after failed query:\n got %s\nwant %s", got, wantBytes)
			}
		})
	}
}

// TestTraceIndependencePreservesBusinessRulesAndVerbatimNames checks that the
// reports the independence tests rely on still follow every selection rule
// and preserve Chinese characters, casing, and surrounding spaces verbatim
// inside independently owned slices. Editing one path must not normalize,
// trim, case-fold, or re-sort anything in another record.
func TestTraceIndependencePreservesBusinessRulesAndVerbatimNames(t *testing.T) {
	// Two roots " A " (leading/trailing space, byte 0x20) and "根" reachable
	// via one intermediate; a second path to " A " of equal length through a
	// bytewise-larger intermediate loses the tie-break at position 1.
	//
	//	D -> M -> " A "
	//	D -> z -> " A "
	//	D -> M -> "根"
	graphJSON := `{"datasets":[
		{"name":"D","upstreams":["z","M"]},
		{"name":"M","upstreams":["根"," A "]},
		{"name":"z","upstreams":[" A "]},
		{"name":" A ","upstreams":[]},
		{"name":"根","upstreams":[]}
	]}`
	snap := snapshotOf(t, graphJSON)
	report := traceOf(t, graphJSON, "D")

	// Every root exactly once; " A " (0x20...) sorts before "根" (0xE8...) by
	// UTF-8 byte order; the equal-length choice to " A " is via M (0x4D <
	// 0x7A), decided element by element from the query end.
	want := []SourceTrace{
		{Root: " A ", Path: []string{"D", "M", " A "}},
		{Root: "根", Path: []string{"D", "M", "根"}},
	}
	if !reflect.DeepEqual(report.Sources, want) {
		t.Fatalf("Sources = %+v, want %+v", report.Sources, want)
	}

	// Edit the " A " path; the Chinese-named root record and the snapshot
	// stay verbatim.
	s := sourceByRoot(report, " A ")
	s.Path[2] = " A-X"
	s.Path = append(s.Path, "  ")
	report.Sources[0].Root = " 根 "

	if got := sourceByRoot(report, "根"); got == nil {
		t.Fatalf("Chinese-named root record lost after editing the other path")
	} else if wantRec := (SourceTrace{Root: "根", Path: []string{"D", "M", "根"}}); !reflect.DeepEqual(*got, wantRec) {
		t.Errorf("根 record = %+v, want untouched %+v", *got, wantRec)
	}
	if got := mustSnapshotDataset(t, snap, " A ").Name; got != " A " {
		t.Errorf("snapshot root name = %q, want verbatim %q", got, " A ")
	}
	if got := mustSnapshotDataset(t, snap, "根").Name; got != "根" {
		t.Errorf("snapshot root name = %q, want verbatim %q", got, "根")
	}
	if got := mustSnapshotDataset(t, snap, "M").Upstreams; !reflect.DeepEqual(got, []string{" A ", "根"}) {
		t.Errorf("snapshot M.upstreams = %v, want verbatim frozen [\" A \" 根]", got)
	}
}
