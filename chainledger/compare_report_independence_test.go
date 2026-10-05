package chainledger

import (
	"bytes"
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

// This file is the regression net for the independence of diff reports
// returned by CompareSnapshots. A caller that receives a *CompareReport keeps
// reshaping it for display — dropping entries it does not care about,
// reordering lists, renaming records — and may hold several reports at once
// while it keeps holding both input snapshots. None of those views may alias
// each other or the inputs:
//
//   - every RootSourceChange owns its Dataset name and BOTH of its root
//     arrays: editing one entry's OldRoots, NewRoots, or Dataset changes only
//     that entry, never the entry's other root array, never another dataset's
//     entry (even when several common datasets moved between the same two
//     overlapping root sets), and never the input snapshots;
//   - two reports computed back to back from the same pair of legal snapshots
//     are fully independent: rewriting, reordering, or trimming any of the
//     six lists of one report leaves the other representing the complete
//     original diff, leaves both snapshots' names, upstream relations,
//     content identifiers, and export bytes untouched, and a fresh comparison
//     still returns the original diff;
//   - the reverse direction holds as well: after a report is returned, the
//     caller editing its own snapshot records or comparing another pair of
//     legal snapshots cannot move the already returned report;
//   - a no-diff comparison returns independent empty results too: every list
//     stays a non-nil empty array that encodes as [], with no entries left
//     over from an earlier non-empty comparison;
//   - name casing and surrounding spaces survive verbatim, the existing
//     sorting of datasets, relations, and root lists is unchanged, and
//     feeding legal input with shuffled records or repeated upstreams does
//     not change an unedited report's content or JSON bytes.
//
// Only the preservation of already returned results is pinned here; how the
// compare entry point treats a corrupted snapshot is out of scope.

// compareIndependenceOldGraph and compareIndependenceNewGraph are the shared
// fixture: every common dataset that derives from the roots migrates from the
// old root set {R1, R2} to the new root set {R2, R3}, whose intersection is
// {R2}. N reaches R1 along TWO paths in the old version (directly and through
// M) and must still list it once; leaf keeps its direct upstreams untouched
// in both versions and changes roots only because M moved. R2 is common to
// both versions and keeps reaching only itself, so it never appears in the
// report.
const compareIndependenceOldGraph = `{"datasets":[
	{"name":"R1","upstreams":[]},
	{"name":"R2","upstreams":[]},
	{"name":"M","upstreams":["R1","R2"]},
	{"name":"N","upstreams":["M","R1"]},
	{"name":"leaf","upstreams":["M"]}
]}`

const compareIndependenceNewGraph = `{"datasets":[
	{"name":"R2","upstreams":[]},
	{"name":"R3","upstreams":[]},
	{"name":"M","upstreams":["R2","R3"]},
	{"name":"N","upstreams":["M","R3"]},
	{"name":"leaf","upstreams":["M"]}
]}`

// freshCompareIndependenceReport returns the complete expected diff of the
// shared fixture, old -> new, with freshly allocated slices so a test can
// mutate its own report without touching the expectation. The fixture
// exercises every report list at once: R3 is new, R1 is removed, M and N
// changed direct upstreams, two edges were added and two removed, and the
// three common derived datasets each moved root set [R1 R2] -> [R2 R3].
func freshCompareIndependenceReport() CompareReport {
	return CompareReport{
		NewDatasets:       []string{"R3"},
		RemovedDatasets:   []string{"R1"},
		ChangedDatasets:   []string{"M", "N"},
		AddedRelations:    []Relation{{Upstream: "R3", Downstream: "M"}, {Upstream: "R3", Downstream: "N"}},
		RemovedRelations:  []Relation{{Upstream: "R1", Downstream: "M"}, {Upstream: "R1", Downstream: "N"}},
		RootSourceChanges: []RootSourceChange{
			{Dataset: "M", OldRoots: []string{"R1", "R2"}, NewRoots: []string{"R2", "R3"}},
			{Dataset: "N", OldRoots: []string{"R1", "R2"}, NewRoots: []string{"R2", "R3"}},
			{Dataset: "leaf", OldRoots: []string{"R1", "R2"}, NewRoots: []string{"R2", "R3"}},
		},
	}
}

// mustMarshalCompareReport renders a report the way a caller displaying or
// exporting it would, failing the test on an impossible-on-valid-input error.
func mustMarshalCompareReport(t *testing.T, report *CompareReport) []byte {
	t.Helper()
	data, err := json.Marshal(report)
	if err != nil {
		t.Fatalf("json.Marshal report unexpected error: %v", err)
	}
	return data
}

// compareFixtureSnapshots builds the shared fixture pair and captures
// everything a test later checks for unwanted movement: both export byte
// strings and deep copies of both dataset record lists.
func compareFixtureSnapshots(t *testing.T) (oldSnap, newSnap *SnapshotFile, oldBytes, newBytes []byte, oldDatasets, newDatasets []GraphDataset) {
	t.Helper()
	oldSnap = snapshotOf(t, compareIndependenceOldGraph)
	newSnap = snapshotOf(t, compareIndependenceNewGraph)
	oldBytes = mustMarshalSnapshot(t, oldSnap)
	newBytes = mustMarshalSnapshot(t, newSnap)
	oldDatasets = snapshotDatasetsCopy(oldSnap)
	newDatasets = snapshotDatasetsCopy(newSnap)
	return oldSnap, newSnap, oldBytes, newBytes, oldDatasets, newDatasets
}

// assertFixtureSnapshotsUntouched fails if either fixture snapshot moved away
// from the state captured by compareFixtureSnapshots: content identifier,
// dataset names with their direct upstream relations, and export bytes.
func assertFixtureSnapshotsUntouched(t *testing.T, oldSnap, newSnap *SnapshotFile, oldBytes, newBytes []byte, oldDatasets, newDatasets []GraphDataset) {
	t.Helper()
	if got := mustMarshalSnapshot(t, oldSnap); !bytes.Equal(got, oldBytes) {
		t.Errorf("old snapshot export changed:\n got %s\nwant %s", got, oldBytes)
	}
	if got := mustMarshalSnapshot(t, newSnap); !bytes.Equal(got, newBytes) {
		t.Errorf("new snapshot export changed:\n got %s\nwant %s", got, newBytes)
	}
	if got := snapshotDatasetsCopy(oldSnap); !reflect.DeepEqual(got, oldDatasets) {
		t.Errorf("old snapshot datasets changed:\n got %v\nwant %v", got, oldDatasets)
	}
	if got := snapshotDatasetsCopy(newSnap); !reflect.DeepEqual(got, newDatasets) {
		t.Errorf("new snapshot datasets changed:\n got %v\nwant %v", got, newDatasets)
	}
}

// TestCompareRootChangeEntriesAreIndependentOfEachOther is the headline
// aliasing guarantee for the root-source list: several common datasets moved
// between the same two overlapping root sets, and every reported entry must
// own its dataset name and both of its root arrays. Editing one entry —
// replacing or appending an old root name, replacing or appending a new root
// name, or renaming the dataset — changes only that entry: the entry's other
// root array keeps its original names, the other datasets' entries keep
// everything, and both input snapshots stay frozen.
func TestCompareRootChangeEntriesAreIndependentOfEachOther(t *testing.T) {
	edits := []struct {
		name        string
		apply       func(*RootSourceChange)
		oldTouched  bool
		newTouched  bool
		nameTouched bool
	}{
		{"replace old root name", func(c *RootSourceChange) { c.OldRoots[0] = "HACK" }, true, false, false},
		{"append to old roots", func(c *RootSourceChange) { c.OldRoots = append(c.OldRoots, "EXTRA") }, true, false, false},
		{"replace new root name", func(c *RootSourceChange) { c.NewRoots[0] = "HACK" }, false, true, false},
		{"append to new roots", func(c *RootSourceChange) { c.NewRoots = append(c.NewRoots, "EXTRA") }, false, true, false},
		{"rename dataset", func(c *RootSourceChange) { c.Dataset = "HACK" }, false, false, true},
	}
	// Entries are sorted by dataset name: M, N, leaf. Exercise the edit on
	// two different entries, since either could alias its neighbours.
	for _, entry := range []int{0, 1} {
		for _, tc := range edits {
			t.Run(freshCompareIndependenceReport().RootSourceChanges[entry].Dataset+"/"+tc.name, func(t *testing.T) {
				oldSnap, newSnap, oldBytes, newBytes, oldDatasets, newDatasets := compareFixtureSnapshots(t)
				report := CompareSnapshots(oldSnap, newSnap)
				want := freshCompareIndependenceReport()
				if !reflect.DeepEqual(*report, want) {
					t.Fatalf("test setup: report = %+v, want %+v", *report, want)
				}

				tc.apply(&report.RootSourceChanges[entry])

				// The edit really landed on the targeted entry.
				if reflect.DeepEqual(report.RootSourceChanges[entry], want.RootSourceChanges[entry]) {
					t.Fatalf("test setup: editing entry %d had no local effect: %+v", entry, report.RootSourceChanges[entry])
				}
				// Every other dataset's entry is untouched.
				for i := range want.RootSourceChanges {
					if i == entry {
						continue
					}
					if got := report.RootSourceChanges[i]; !reflect.DeepEqual(got, want.RootSourceChanges[i]) {
						t.Errorf("entry %d (%s) changed after editing entry %d:\n got %+v\nwant %+v",
							i, want.RootSourceChanges[i].Dataset, entry, got, want.RootSourceChanges[i])
					}
				}
				// The edited entry's own fields that the edit did not target
				// keep their original values: rewriting one root array never
				// reaches the other side, and renaming the dataset reaches
				// neither.
				got := report.RootSourceChanges[entry]
				if !tc.oldTouched && !reflect.DeepEqual(got.OldRoots, want.RootSourceChanges[entry].OldRoots) {
					t.Errorf("OldRoots of edited entry = %v, want untouched %v", got.OldRoots, want.RootSourceChanges[entry].OldRoots)
				}
				if !tc.newTouched && !reflect.DeepEqual(got.NewRoots, want.RootSourceChanges[entry].NewRoots) {
					t.Errorf("NewRoots of edited entry = %v, want untouched %v", got.NewRoots, want.RootSourceChanges[entry].NewRoots)
				}
				if !tc.nameTouched && got.Dataset != want.RootSourceChanges[entry].Dataset {
					t.Errorf("Dataset of edited entry = %q, want untouched %q", got.Dataset, want.RootSourceChanges[entry].Dataset)
				}

				// Both input snapshots are exactly as parsed.
				assertFixtureSnapshotsUntouched(t, oldSnap, newSnap, oldBytes, newBytes, oldDatasets, newDatasets)
			})
		}
	}
}

// TestCompareReportsFromSamePairAreIndependent verifies that two reports
// computed back to back from the same pair of legal snapshots start out
// identical, and that thoroughly editing one — rewriting entries in all six
// lists, reordering, trimming, and appending — leaves the other representing
// the complete original diff, leaves both input snapshots frozen, and leaves
// a fresh comparison returning the original diff.
func TestCompareReportsFromSamePairAreIndependent(t *testing.T) {
	oldSnap, newSnap, oldBytes, newBytes, oldDatasets, newDatasets := compareFixtureSnapshots(t)
	first := CompareSnapshots(oldSnap, newSnap)
	second := CompareSnapshots(oldSnap, newSnap)
	wantSecond := mustMarshalCompareReport(t, second)

	if got := mustMarshalCompareReport(t, first); !bytes.Equal(got, wantSecond) {
		t.Fatalf("two comparisons of one pair differ:\n got %s\nwant %s", got, wantSecond)
	}

	// Edit the first report as thoroughly as a caller reshaping it for
	// display might: rewrite, reorder, trim, and extend every list.
	first.NewDatasets[0] = "HACK"
	first.NewDatasets = append(first.NewDatasets, "EXTRA")
	first.RemovedDatasets = first.RemovedDatasets[:0]
	first.ChangedDatasets[0], first.ChangedDatasets[1] = first.ChangedDatasets[1], first.ChangedDatasets[0]
	first.ChangedDatasets = append(first.ChangedDatasets, "EXTRA")
	first.AddedRelations[0].Upstream = "HACK"
	first.AddedRelations = append(first.AddedRelations, Relation{Upstream: "FAKE", Downstream: "FAKE"})
	first.RemovedRelations[0].Downstream = "HACK"
	first.RemovedRelations = first.RemovedRelations[:1]
	first.RootSourceChanges[0].Dataset = "HACK"
	first.RootSourceChanges[1].OldRoots[0] = "HACK"
	first.RootSourceChanges[2].NewRoots = append(first.RootSourceChanges[2].NewRoots, "EXTRA")
	first.RootSourceChanges = first.RootSourceChanges[:1]

	// The untouched report of the same comparison is byte-identical to what
	// the comparison returned and still holds the complete original diff.
	if got := mustMarshalCompareReport(t, second); !bytes.Equal(got, wantSecond) {
		t.Errorf("second report changed after editing the first:\n got %s\nwant %s", got, wantSecond)
	}
	if want := freshCompareIndependenceReport(); !reflect.DeepEqual(*second, want) {
		t.Errorf("second report = %+v, want %+v", *second, want)
	}

	// Both input snapshots kept their names, upstream relations, content
	// identifiers, and export bytes.
	assertFixtureSnapshotsUntouched(t, oldSnap, newSnap, oldBytes, newBytes, oldDatasets, newDatasets)

	// Comparing the same pair again still returns the original diff.
	fresh := CompareSnapshots(oldSnap, newSnap)
	if got := mustMarshalCompareReport(t, fresh); !bytes.Equal(got, wantSecond) {
		t.Errorf("fresh comparison after edits = %s, want %s", got, wantSecond)
	}
}

// TestCompareReportSurvivesSnapshotEditsAndOtherComparisons pins the reverse
// direction: once a report has been returned, nothing the caller does next —
// rewriting the snapshot records it still holds, or comparing another pair of
// legal snapshots — can move the differences that report already recorded.
// The caller-edited snapshot copy is corrupt afterwards and is never compared
// again: only the preservation of the returned result is pinned.
func TestCompareReportSurvivesSnapshotEditsAndOtherComparisons(t *testing.T) {
	oldSnap, newSnap, _, _, _, _ := compareFixtureSnapshots(t)
	report := CompareSnapshots(oldSnap, newSnap)
	wantReport := mustMarshalCompareReport(t, report)

	// The caller rewrites the snapshot records it still holds: an upstream
	// name in place, an append into the same record, and a dataset rename.
	iM := snapshotDatasetIndex(oldSnap, "M")
	if iM < 0 {
		t.Fatalf("old snapshot missing M")
	}
	oldSnap.Graph.Datasets[iM].Upstreams[0] = "HACK"
	oldSnap.Graph.Datasets[iM].Upstreams = append(oldSnap.Graph.Datasets[iM].Upstreams, "ghost")
	iN := snapshotDatasetIndex(newSnap, "N")
	if iN < 0 {
		t.Fatalf("new snapshot missing N")
	}
	newSnap.Graph.Datasets[iN].Name = "DISPLAY"

	// The caller compares another pair of legal snapshots.
	otherOld := snapshotOf(t, `{"datasets":[{"name":"Q","upstreams":[]}]}`)
	otherNew := snapshotOf(t, `{"datasets":[{"name":"Q","upstreams":[]},{"name":"Z","upstreams":["Q"]}]}`)
	other := CompareSnapshots(otherOld, otherNew)
	if got, want := other.NewDatasets, []string{"Z"}; !reflect.DeepEqual(got, want) {
		t.Errorf("other comparison NewDatasets = %v, want %v", got, want)
	}
	if got, want := other.AddedRelations, []Relation{{Upstream: "Q", Downstream: "Z"}}; !reflect.DeepEqual(got, want) {
		t.Errorf("other comparison AddedRelations = %v, want %v", got, want)
	}

	// The report returned earlier still records exactly the original diff.
	if got := mustMarshalCompareReport(t, report); !bytes.Equal(got, wantReport) {
		t.Errorf("returned report moved after later caller activity:\n got %s\nwant %s", got, wantReport)
	}
	if want := freshCompareIndependenceReport(); !reflect.DeepEqual(*report, want) {
		t.Errorf("returned report = %+v, want %+v", *report, want)
	}
}

// TestCompareEmptyReportsAreIndependentAndStayArrays covers the no-diff
// comparison: its empty result is independent too. A non-empty comparison
// runs first so that entries left over from it would show; both empty reports
// keep every list as a non-nil empty array that encodes as [], and filling
// one empty report with fabricated entries leaves the other — and a fresh
// self-comparison — completely empty.
func TestCompareEmptyReportsAreIndependentAndStayArrays(t *testing.T) {
	oldSnap, newSnap, _, _, _, _ := compareFixtureSnapshots(t)
	nonEmpty := CompareSnapshots(oldSnap, newSnap)
	if len(nonEmpty.RootSourceChanges) == 0 || len(nonEmpty.AddedRelations) == 0 {
		t.Fatalf("test setup: fixture comparison is empty: %+v", nonEmpty)
	}

	snap := snapshotOf(t, `{"datasets":[{"name":"A","upstreams":[]},{"name":"B","upstreams":["A"]}]}`)
	snapBytes := mustMarshalSnapshot(t, snap)

	first := CompareSnapshots(snap, snap)
	second := CompareSnapshots(snap, snap)
	assertEmptyReport(t, first)
	assertEmptyReport(t, second)
	wantSecond := mustMarshalCompareReport(t, second)
	if strings.Contains(string(wantSecond), "null") {
		t.Fatalf("empty report encoded a null list: %s", wantSecond)
	}
	for _, key := range []string{`"newDatasets":`, `"removedDatasets":`, `"changedDatasets":`, `"addedRelations":`, `"removedRelations":`, `"rootSourceChanges":`} {
		if !strings.Contains(string(wantSecond), key+"[]") {
			t.Errorf("empty report key %s not encoded as an empty array:\n%s", key, wantSecond)
		}
	}

	// The caller fills the first empty report with fabricated entries in
	// every list.
	first.NewDatasets = append(first.NewDatasets, "GHOST")
	first.RemovedDatasets = append(first.RemovedDatasets, "GHOST")
	first.ChangedDatasets = append(first.ChangedDatasets, "GHOST")
	first.AddedRelations = append(first.AddedRelations, Relation{Upstream: "GHOST", Downstream: "GHOST"})
	first.RemovedRelations = append(first.RemovedRelations, Relation{Upstream: "GHOST", Downstream: "GHOST"})
	first.RootSourceChanges = append(first.RootSourceChanges, RootSourceChange{Dataset: "GHOST", OldRoots: []string{"GHOST"}, NewRoots: []string{"GHOST"}})

	// The other empty report is byte-identical and still completely empty.
	if got := mustMarshalCompareReport(t, second); !bytes.Equal(got, wantSecond) {
		t.Errorf("second empty report changed after filling the first:\n got %s\nwant %s", got, wantSecond)
	}
	assertEmptyReport(t, second)

	// A fresh self-comparison is still empty, and the snapshot never moved.
	assertEmptyReport(t, CompareSnapshots(snap, snap))
	if got := mustMarshalSnapshot(t, snap); !bytes.Equal(got, snapBytes) {
		t.Errorf("snapshot export changed:\n got %s\nwant %s", got, snapBytes)
	}
}

// TestCompareUneditedReportBytesIndependentOfInputSpelling verifies that the
// way legal input was spelled — record order, upstream order, repeated
// upstreams — cannot reach an unedited report: the shuffled spelling produces
// a byte-identical report, and editing the report derived from the shuffled
// input leaves the canonical report's content and JSON bytes untouched.
func TestCompareUneditedReportBytesIndependentOfInputSpelling(t *testing.T) {
	oldSnap, newSnap, _, _, _, _ := compareFixtureSnapshots(t)
	canonical := CompareSnapshots(oldSnap, newSnap)
	wantCanonical := mustMarshalCompareReport(t, canonical)

	// Same graph semantics, spelled with shuffled records, shuffled upstream
	// order, and repeated upstreams.
	oldShuffled := snapshotOf(t, `{"datasets":[
		{"name":"leaf","upstreams":["M","M"]},
		{"upstreams":["R1","M"],"name":"N"},
		{"name":"M","upstreams":["R2","R1","R2"]},
		{"name":"R2","upstreams":[]},
		{"name":"R1","upstreams":[]}
	]}`)
	newShuffled := snapshotOf(t, `{"datasets":[
		{"upstreams":["M"],"name":"leaf"},
		{"name":"N","upstreams":["R3","M","R3"]},
		{"name":"M","upstreams":["R3","R2"]},
		{"name":"R3","upstreams":[]},
		{"name":"R2","upstreams":[]}
	]}`)
	shuffled := CompareSnapshots(oldShuffled, newShuffled)
	if got := mustMarshalCompareReport(t, shuffled); !bytes.Equal(got, wantCanonical) {
		t.Fatalf("shuffled input produced a different report:\n got %s\nwant %s", got, wantCanonical)
	}

	// Edit the report derived from the shuffled input; the canonical report
	// must not follow.
	shuffled.NewDatasets[0] = "HACK"
	shuffled.RootSourceChanges[0].OldRoots[0] = "HACK"
	shuffled.RootSourceChanges[1].NewRoots = append(shuffled.RootSourceChanges[1].NewRoots, "EXTRA")
	shuffled.AddedRelations[0].Downstream = "HACK"

	if got := mustMarshalCompareReport(t, canonical); !bytes.Equal(got, wantCanonical) {
		t.Errorf("canonical report changed after editing the shuffled report:\n got %s\nwant %s", got, wantCanonical)
	}
	if want := freshCompareIndependenceReport(); !reflect.DeepEqual(*canonical, want) {
		t.Errorf("canonical report = %+v, want %+v", *canonical, want)
	}
}

// TestCompareReportPreservesNameSpellingsWhenEdited verifies that the
// independence copies keep dataset and root names verbatim: casing and
// surrounding spaces in the reported source change survive, editing the
// returned names cannot rename anything in the input snapshots, and a fresh
// comparison reports the original spellings again.
func TestCompareReportPreservesNameSpellingsWhenEdited(t *testing.T) {
	oldSnap := snapshotOf(t, `{"datasets":[{"name":" Raw ","upstreams":[]},{"name":"RAW","upstreams":[" Raw "]}]}`)
	newSnap := snapshotOf(t, `{"datasets":[{"name":" Raw ","upstreams":[]},{"name":"RAW","upstreams":[]}]}`)
	oldBytes := mustMarshalSnapshot(t, oldSnap)
	newBytes := mustMarshalSnapshot(t, newSnap)

	report := CompareSnapshots(oldSnap, newSnap)
	wantChanges := []RootSourceChange{{Dataset: "RAW", OldRoots: []string{" Raw "}, NewRoots: []string{"RAW"}}}
	if !reflect.DeepEqual(report.RootSourceChanges, wantChanges) {
		t.Fatalf("RootSourceChanges = %#v, want %#v", report.RootSourceChanges, wantChanges)
	}

	// The caller rewrites every name the report returned.
	report.RootSourceChanges[0].Dataset = "trimmed"
	report.RootSourceChanges[0].OldRoots[0] = "trimmed"
	report.RootSourceChanges[0].NewRoots[0] = "trimmed"

	// Both snapshots keep the exact frozen spellings, spaces included.
	if got := mustSnapshotDataset(t, oldSnap, "RAW").Upstreams; !reflect.DeepEqual(got, []string{" Raw "}) {
		t.Errorf("old snapshot RAW.upstreams = %v, want verbatim [\" Raw \"]", got)
	}
	if !bytes.Contains(mustMarshalSnapshot(t, oldSnap), []byte(`" Raw "`)) {
		t.Errorf("old snapshot export lost the space-wrapped name")
	}
	if got := mustMarshalSnapshot(t, oldSnap); !bytes.Equal(got, oldBytes) {
		t.Errorf("old snapshot export changed:\n got %s\nwant %s", got, oldBytes)
	}
	if got := mustMarshalSnapshot(t, newSnap); !bytes.Equal(got, newBytes) {
		t.Errorf("new snapshot export changed:\n got %s\nwant %s", got, newBytes)
	}

	// A fresh comparison reports the original verbatim names again.
	fresh := CompareSnapshots(oldSnap, newSnap)
	if !reflect.DeepEqual(fresh.RootSourceChanges, wantChanges) {
		t.Errorf("fresh RootSourceChanges = %#v, want %#v", fresh.RootSourceChanges, wantChanges)
	}
}
