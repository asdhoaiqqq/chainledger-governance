package chainledger

import (
	"bytes"
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

// This file is the regression net for the independence of comparison reports
// returned by CompareSnapshots. A Go caller keeps processing a returned report
// — deleting entries it does not care about, reordering lists for display, or
// rewriting a name — may hold several reports of the same snapshot pair at
// once (as well as reports for other pairs), and keeps holding both input
// snapshots. Every report must be this comparison alone:
//
//   - when several datasets common to both versions reached one shared root
//     set in the old version and another shared root set in the new version
//     (the two sets intersecting), each root-source entry keeps its own
//     accurate old/new root names: rewriting one entry's old roots, new roots,
//     or dataset name reaches neither the other entries nor that entry's
//     other side; a root reached by several paths is still listed once;
//   - a downstream whose DIRECT upstreams did not change still keeps its own
//     root-source change when an upstream adjustment moved its root set;
//   - two reports of the same legal pair never alias: rewriting, reordering,
//     or pruning any of the six lists in one leaves the other representing
//     the complete original diff, and both input snapshots keep their names,
//     direct upstream relations, content identifiers, and serialized bytes;
//     comparing the same pair again reproduces the original report byte for
//     byte;
//   - after a report is returned the caller may edit the snapshot records it
//     holds or compare a different legal pair; the earlier report's recorded
//     diff never moves (the compare interface gains no new corrupted-snapshot
//     behavior — only already returned results are pinned);
//   - a comparison with no diff gets its own empty result with every list
//     serialized as [], never entries left over from an earlier comparison;
//   - name casing and surrounding spaces are kept verbatim and the existing
//     ordering of sources, datasets, and relations is unchanged, while record
//     order and duplicate upstreams in legal inputs cannot move an unedited
//     report's content or JSON bytes.
//
// The deliberately edited report a caller keeps for display is out of scope:
// nothing repairs it or recomputes its lists.

// intersectRootsOldGraph is the headline fixture: three roots R1 R2 R3, two
// old intermediates a1/a2, one shared node depending on both intermediates,
// and two downstreams D1 and D2. shared is reached by more than one path from
// every root (and D1 additionally depends directly on a1), so each root must
// still be listed exactly once. Every one of shared, D1, D2 reaches the same
// old root set {R1,R2,R3}.
const intersectRootsOldGraph = `{"datasets":[
	{"name":"R1","upstreams":[]},
	{"name":"R2","upstreams":[]},
	{"name":"R3","upstreams":[]},
	{"name":"a1","upstreams":["R1","R2"]},
	{"name":"a2","upstreams":["R2","R3"]},
	{"name":"shared","upstreams":["a1","a2"]},
	{"name":"D1","upstreams":["a1","shared"]},
	{"name":"D2","upstreams":["shared"]}
]}`

// intersectRootsNewGraph keeps R2 and R3 (the intersection with the old root
// set), drops R1/a1/a2, adds R4/b1/b2, and repoints shared at the new
// intermediates. shared, D1, and D2 now reach the same new root set
// {R2,R3,R4}. D2's DIRECT upstream stays [shared] on both sides, so it is the
// downstream whose roots moved purely because of the upstream adjustment.
const intersectRootsNewGraph = `{"datasets":[
	{"name":"R2","upstreams":[]},
	{"name":"R3","upstreams":[]},
	{"name":"R4","upstreams":[]},
	{"name":"b1","upstreams":["R2","R4"]},
	{"name":"b2","upstreams":["R3","R4"]},
	{"name":"shared","upstreams":["b1","b2"]},
	{"name":"D1","upstreams":["b1","shared"]},
	{"name":"D2","upstreams":["shared"]}
]}`

// wantIntersectRootChanges are the three independently stored root-source
// entries, sorted by dataset name byte order.
var wantIntersectRootChanges = []RootSourceChange{
	{Dataset: "D1", OldRoots: []string{"R1", "R2", "R3"}, NewRoots: []string{"R2", "R3", "R4"}},
	{Dataset: "D2", OldRoots: []string{"R1", "R2", "R3"}, NewRoots: []string{"R2", "R3", "R4"}},
	{Dataset: "shared", OldRoots: []string{"R1", "R2", "R3"}, NewRoots: []string{"R2", "R3", "R4"}},
}

// rootChangeFor finds the root-source entry for dataset in report.
func rootChangeFor(report *CompareReport, dataset string) (RootSourceChange, bool) {
	for _, change := range report.RootSourceChanges {
		if change.Dataset == dataset {
			return change, true
		}
	}
	return RootSourceChange{}, false
}

// mustMarshalCompareReport renders a compare report the way a caller
// displaying it would.
func mustMarshalCompareReport(t *testing.T, report *CompareReport) []byte {
	t.Helper()
	data, err := json.Marshal(report)
	if err != nil {
		t.Fatalf("json.Marshal compare report unexpected error: %v", err)
	}
	return data
}

// TestCompareSharedRootSetEntriesAreAccurateAndSorted pins the business
// result of the headline fixture before any independence edit: three common
// datasets move between two overlapping root sets, and each entry records the
// exact sorted, once-only old and new root names. D2 has unchanged direct
// upstreams and must not appear in changedDatasets, but its root-source change
// is still recorded.
func TestCompareSharedRootSetEntriesAreAccurateAndSorted(t *testing.T) {
	oldSnap := snapshotOf(t, intersectRootsOldGraph)
	newSnap := snapshotOf(t, intersectRootsNewGraph)
	report := CompareSnapshots(oldSnap, newSnap)

	if got, want := report.NewDatasets, []string{"R4", "b1", "b2"}; !reflect.DeepEqual(got, want) {
		t.Errorf("NewDatasets = %v, want %v", got, want)
	}
	if got, want := report.RemovedDatasets, []string{"R1", "a1", "a2"}; !reflect.DeepEqual(got, want) {
		t.Errorf("RemovedDatasets = %v, want %v", got, want)
	}
	// D1 gains b1 and loses a1 as a direct upstream; shared is fully repointed;
	// D2 keeps [shared] directly even though its root set moves.
	if got, want := report.ChangedDatasets, []string{"D1", "shared"}; !reflect.DeepEqual(got, want) {
		t.Errorf("ChangedDatasets = %v, want %v", got, want)
	}
	if got, want := report.AddedRelations, []Relation{
		{Upstream: "R2", Downstream: "b1"},
		{Upstream: "R3", Downstream: "b2"},
		{Upstream: "R4", Downstream: "b1"},
		{Upstream: "R4", Downstream: "b2"},
		{Upstream: "b1", Downstream: "D1"},
		{Upstream: "b1", Downstream: "shared"},
		{Upstream: "b2", Downstream: "shared"},
	}; !reflect.DeepEqual(got, want) {
		t.Errorf("AddedRelations = %v, want %v", got, want)
	}
	if got, want := report.RemovedRelations, []Relation{
		{Upstream: "R1", Downstream: "a1"},
		{Upstream: "R2", Downstream: "a1"},
		{Upstream: "R2", Downstream: "a2"},
		{Upstream: "R3", Downstream: "a2"},
		{Upstream: "a1", Downstream: "D1"},
		{Upstream: "a1", Downstream: "shared"},
		{Upstream: "a2", Downstream: "shared"},
	}; !reflect.DeepEqual(got, want) {
		t.Errorf("RemovedRelations = %v, want %v", got, want)
	}

	if got := report.RootSourceChanges; !reflect.DeepEqual(got, wantIntersectRootChanges) {
		t.Fatalf("RootSourceChanges = %v\nwant %v", got, wantIntersectRootChanges)
	}

	// Every root reachable by several paths is still listed exactly once.
	for _, change := range report.RootSourceChanges {
		if seen := dupNames(change.OldRoots); len(seen) != 0 {
			t.Errorf("%s OldRoots lists roots more than once: %v in %v", change.Dataset, seen, change.OldRoots)
		}
		if seen := dupNames(change.NewRoots); len(seen) != 0 {
			t.Errorf("%s NewRoots lists roots more than once: %v in %v", change.Dataset, seen, change.NewRoots)
		}
	}
}

// TestCompareRootSourceEntriesAreIndependentOfEachOther is the headline
// aliasing guarantee: when several root-source entries start with identical
// old and new root arrays (because the datasets moved between the same two
// overlapping root sets), every display edit a caller makes on ONE entry —
// its old roots, its new roots, or its dataset name — changes only that
// entry. The other entries keep both of their root arrays exactly, and the
// edited entry's other side is untouched too. A separately returned report of
// the same pair stays the complete original diff.
func TestCompareRootSourceEntriesAreIndependentOfEachOther(t *testing.T) {
	editKinds := map[string]func(*RootSourceChange){
		"rewrite old roots in place and append": func(c *RootSourceChange) {
			c.OldRoots[0] = "HACK"
			c.OldRoots = append(c.OldRoots, "GHOST")
		},
		"rewrite new roots in place and append": func(c *RootSourceChange) {
			c.NewRoots[0] = "HACK"
			c.NewRoots = append(c.NewRoots, "GHOST")
		},
		"rename the dataset": func(c *RootSourceChange) {
			c.Dataset = "HACK"
		},
	}
	for _, target := range []string{"D1", "D2", "shared"} {
		for editName, edit := range editKinds {
			t.Run(target+"/"+editName, func(t *testing.T) {
				oldSnap := snapshotOf(t, intersectRootsOldGraph)
				newSnap := snapshotOf(t, intersectRootsNewGraph)
				report := CompareSnapshots(oldSnap, newSnap)
				// An independently returned report of the same pair is the
				// oracle for everything not deliberately edited.
				oracle := CompareSnapshots(snapshotOf(t, intersectRootsOldGraph), snapshotOf(t, intersectRootsNewGraph))

				idx := indexOfRootChange(report, target)
				if idx < 0 {
					t.Fatalf("report has no root-source entry for %q", target)
				}
				otherSideBefore := struct{ old, new []string }{
					old: append([]string(nil), report.RootSourceChanges[idx].OldRoots...),
					new: append([]string(nil), report.RootSourceChanges[idx].NewRoots...),
				}
				// Edit the entry through its slice position the way a caller
				// reorganizing a held report does.
				edit(&report.RootSourceChanges[idx])
				editedName := target
				if editName == "rename the dataset" {
					editedName = "HACK"
				}

				// Every OTHER dataset keeps both of its root arrays exactly as
				// originally reported.
				for _, want := range wantIntersectRootChanges {
					if want.Dataset == target {
						continue
					}
					got, ok := rootChangeFor(report, want.Dataset)
					if !ok {
						t.Fatalf("entry for %q vanished after editing %q", want.Dataset, target)
					}
					if !reflect.DeepEqual(got, want) {
						t.Errorf("entry for %q changed when %q was edited:\n got %+v\nwant %+v", want.Dataset, target, got, want)
					}
				}

				// The edited entry's OTHER side is untouched (the edit must not
				// share backing storage between old and new root arrays).
				gotEdited, ok := rootChangeFor(report, editedName)
				if !ok {
					t.Fatalf("edited entry for %q vanished (renamed=%v)", target, editedName)
				}
				if editName != "rewrite old roots in place and append" {
					if !reflect.DeepEqual(gotEdited.OldRoots, otherSideBefore.old) {
						t.Errorf("edited entry OldRoots = %v, want untouched %v", gotEdited.OldRoots, otherSideBefore.old)
					}
				}
				if editName != "rewrite new roots in place and append" {
					if !reflect.DeepEqual(gotEdited.NewRoots, otherSideBefore.new) {
						t.Errorf("edited entry NewRoots = %v, want untouched %v", gotEdited.NewRoots, otherSideBefore.new)
					}
				}

				// The independently returned report is the complete original.
				if !reflect.DeepEqual(oracle.RootSourceChanges, wantIntersectRootChanges) {
					t.Errorf("separate report root changes = %v, want %v", oracle.RootSourceChanges, wantIntersectRootChanges)
				}
				// The input snapshots are untouched too.
				if got := snapshotDatasetsCopy(oldSnap); !reflect.DeepEqual(got, snapshotDatasetsCopy(snapshotOf(t, intersectRootsOldGraph))) {
					t.Errorf("old snapshot changed after editing a report entry: %v", got)
				}
				if got := snapshotDatasetsCopy(newSnap); !reflect.DeepEqual(got, snapshotDatasetsCopy(snapshotOf(t, intersectRootsNewGraph))) {
					t.Errorf("new snapshot changed after editing a report entry: %v", got)
				}
			})
		}
	}
}

// indexOfRootChange returns the first index of an entry whose Dataset equals
// name, or -1.
func indexOfRootChange(report *CompareReport, name string) int {
	for i := range report.RootSourceChanges {
		if report.RootSourceChanges[i].Dataset == name {
			return i
		}
	}
	return -1
}

// TestCompareTwoReportsOfSamePairAreIndependent thoroughly edits one report
// (in-place rewrites, appends, swaps, and pruning across every list kind) and
// verifies the other report still describes the complete original diff, while
// both input snapshots keep names, upstreams, content ids, and export bytes.
// Comparing the same pair again afterwards reproduces the original report,
// including its JSON bytes.
func TestCompareTwoReportsOfSamePairAreIndependent(t *testing.T) {
	oldSnap := snapshotOf(t, intersectRootsOldGraph)
	newSnap := snapshotOf(t, intersectRootsNewGraph)
	first := CompareSnapshots(oldSnap, newSnap)
	second := CompareSnapshots(oldSnap, newSnap)
	oracle := CompareSnapshots(snapshotOf(t, intersectRootsOldGraph), snapshotOf(t, intersectRootsNewGraph))

	wantBytes := mustMarshalCompareReport(t, oracle)
	oldBytesBefore := mustMarshalSnapshot(t, oldSnap)
	newBytesBefore := mustMarshalSnapshot(t, newSnap)
	oldDatasetsBefore := snapshotDatasetsCopy(oldSnap)
	newDatasetsBefore := snapshotDatasetsCopy(newSnap)
	oldID, newID := oldSnap.ContentID, newSnap.ContentID

	// Rewrite, reorder, append, and prune every list kind in the first report.
	first.NewDatasets[0] = "HACK"
	first.NewDatasets = append(first.NewDatasets, "GHOST")
	first.RemovedDatasets[0], first.RemovedDatasets[len(first.RemovedDatasets)-1] =
		first.RemovedDatasets[len(first.RemovedDatasets)-1], first.RemovedDatasets[0]
	first.RemovedDatasets[1] = "HACK"
	first.ChangedDatasets = first.ChangedDatasets[:1]
	first.ChangedDatasets[0] = "HACK"
	first.AddedRelations[0], first.AddedRelations[1] = first.AddedRelations[1], first.AddedRelations[0]
	first.AddedRelations[0].Upstream = "HACK"
	first.AddedRelations[0].Downstream = "GHOST"
	first.AddedRelations = append(first.AddedRelations, Relation{Upstream: "FAKE", Downstream: "FAKE"})
	first.RemovedRelations = first.RemovedRelations[:2]
	first.RemovedRelations[1].Downstream = "GHOST"
	// Swap the root-change entries, edit through the swapped position, prune.
	first.RootSourceChanges[0], first.RootSourceChanges[2] = first.RootSourceChanges[2], first.RootSourceChanges[0]
	first.RootSourceChanges[0].OldRoots[0] = "HACK"
	first.RootSourceChanges[0].NewRoots = append(first.RootSourceChanges[0].NewRoots, "GHOST")
	first.RootSourceChanges[0].Dataset = "DISPLAY"
	first.RootSourceChanges = first.RootSourceChanges[:1]

	// The edit really landed on the first copy.
	if !reflect.DeepEqual(first.RootSourceChanges, []RootSourceChange{
		{Dataset: "DISPLAY", OldRoots: []string{"HACK", "R2", "R3"}, NewRoots: []string{"R2", "R3", "R4", "GHOST"}},
	}) {
		t.Fatalf("test setup: first report edit had no expected effect: %+v", first.RootSourceChanges)
	}

	// The untouched report is the complete original diff.
	if !reflect.DeepEqual(second, oracle) {
		t.Errorf("second report differs from the original after editing the first:\n got %#v\nwant %#v", second, oracle)
	}
	if got := mustMarshalCompareReport(t, second); !bytes.Equal(got, wantBytes) {
		t.Errorf("second report JSON moved with the first:\n got %s\nwant %s", got, wantBytes)
	}

	// Both input snapshots are unchanged: content id, graph records, bytes.
	if oldSnap.ContentID != oldID || newSnap.ContentID != newID {
		t.Errorf("an input content id moved: old=%q want %q, new=%q want %q",
			oldSnap.ContentID, oldID, newSnap.ContentID, newID)
	}
	if got := snapshotDatasetsCopy(oldSnap); !reflect.DeepEqual(got, oldDatasetsBefore) {
		t.Errorf("old snapshot records changed during/after comparison:\n got %v\nwant %v", got, oldDatasetsBefore)
	}
	if got := snapshotDatasetsCopy(newSnap); !reflect.DeepEqual(got, newDatasetsBefore) {
		t.Errorf("new snapshot records changed during/after comparison:\n got %v\nwant %v", got, newDatasetsBefore)
	}
	if got := mustMarshalSnapshot(t, oldSnap); !bytes.Equal(got, oldBytesBefore) {
		t.Errorf("old snapshot bytes changed:\n got %s\nwant %s", got, oldBytesBefore)
	}
	if got := mustMarshalSnapshot(t, newSnap); !bytes.Equal(got, newBytesBefore) {
		t.Errorf("new snapshot bytes changed:\n got %s\nwant %s", got, newBytesBefore)
	}

	// Comparing the same pair again still reproduces the original result and
	// its exact JSON bytes.
	again := CompareSnapshots(oldSnap, newSnap)
	if !reflect.DeepEqual(again, oracle) {
		t.Errorf("re-comparison differs from the original report:\n got %#v\nwant %#v", again, oracle)
	}
	if got := mustMarshalCompareReport(t, again); !bytes.Equal(got, wantBytes) {
		t.Errorf("re-comparison JSON differs:\n got %s\nwant %s", got, wantBytes)
	}
}

// TestCompareEarlierReportFreezesAcrossLaterEditsAndOtherPairs verifies the
// time direction: once a report has been returned, the caller editing the
// snapshot records it still holds — or comparing a different legal pair —
// cannot move the recorded diff. The compare interface is not extended for
// the edited snapshots; only the already returned result is pinned.
func TestCompareEarlierReportFreezesAcrossLaterEditsAndOtherPairs(t *testing.T) {
	oldSnap := snapshotOf(t, intersectRootsOldGraph)
	newSnap := snapshotOf(t, intersectRootsNewGraph)
	earlier := CompareSnapshots(oldSnap, newSnap)
	earlierBytes := mustMarshalCompareReport(t, earlier)

	// The caller reorganizes its own snapshot records for display after the
	// report returned: repoint in place, append a ghost upstream, rename a
	// record (the documents are now the caller's edited copies — no repair is
	// expected of the compare interface).
	iShared := snapshotDatasetIndex(oldSnap, "shared")
	oldSnap.Graph.Datasets[iShared].Upstreams[0] = "HACK"
	oldSnap.Graph.Datasets[iShared].Upstreams = append(oldSnap.Graph.Datasets[iShared].Upstreams, "ghost")
	iD2 := snapshotDatasetIndex(newSnap, "D2")
	newSnap.Graph.Datasets[iD2].Name = "DISPLAY"
	if got := mustMarshalCompareReport(t, earlier); !bytes.Equal(got, earlierBytes) {
		t.Fatalf("test setup: earlier report already moved:\n got %s\nwant %s", got, earlierBytes)
	}

	// Comparing a different, legal pair must not touch the held report.
	otherOld := snapshotOf(t, `{"datasets":[{"name":"A","upstreams":[]},{"name":"B","upstreams":["A"]}]}`)
	otherNew := snapshotOf(t, `{"datasets":[{"name":"A","upstreams":[]},{"name":"B","upstreams":[]}]}`)
	other := CompareSnapshots(otherOld, otherNew)
	if got, want := other.ChangedDatasets, []string{"B"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("sanity: other-pair ChangedDatasets = %v, want %v", got, want)
	}
	if got, want := other.RootSourceChanges, []RootSourceChange{
		{Dataset: "B", OldRoots: []string{"A"}, NewRoots: []string{"B"}},
	}; !reflect.DeepEqual(got, want) {
		t.Fatalf("sanity: other-pair root changes = %v, want %v", got, want)
	}

	if got := mustMarshalCompareReport(t, earlier); !bytes.Equal(got, earlierBytes) {
		t.Errorf("earlier report changed after editing inputs and comparing another pair:\n got %s\nwant %s", got, earlierBytes)
	}
	if !reflect.DeepEqual(earlier.RootSourceChanges, wantIntersectRootChanges) {
		t.Errorf("earlier root changes = %v, want %v", earlier.RootSourceChanges, wantIntersectRootChanges)
	}

	// Comparing the caller's now-edited pair still has no write-back into the
	// held report.
	_ = CompareSnapshots(oldSnap, newSnap)
	if got := mustMarshalCompareReport(t, earlier); !bytes.Equal(got, earlierBytes) {
		t.Errorf("earlier report changed after re-comparing the edited pair:\n got %s\nwant %s", got, earlierBytes)
	}
}

// TestCompareEmptyResultIsIndependentAndCarriesNoResidue verifies that a diff
// with no changes gets its own fresh result: after a non-empty comparison, the
// empty lists are zero-length, non-nil, serialize as [], and contain no name
// seen in the earlier report. Editing either empty report afterwards cannot
// move the other one, and a further identical comparison stays empty.
func TestCompareEmptyResultIsIndependentAndCarriesNoResidue(t *testing.T) {
	oldSnap := snapshotOf(t, intersectRootsOldGraph)
	newSnap := snapshotOf(t, intersectRootsNewGraph)
	nonEmpty := CompareSnapshots(oldSnap, newSnap)
	if len(nonEmpty.RootSourceChanges) == 0 {
		t.Fatalf("test setup: headline comparison produced no changes")
	}
	// Display-edit the non-empty report; residue must not leak from it either.
	nonEmpty.RootSourceChanges[0].OldRoots[0] = "HACK"
	nonEmpty.NewDatasets = append(nonEmpty.NewDatasets, "GHOST")

	identical := snapshotOf(t, intersectRootsOldGraph)
	empty := CompareSnapshots(identical, identical)
	assertEmptyReport(t, empty)

	emptyJSON := string(mustMarshalCompareReport(t, empty))
	for _, key := range []string{`"newDatasets":`, `"removedDatasets":`, `"changedDatasets":`, `"addedRelations":`, `"removedRelations":`, `"rootSourceChanges":`} {
		if !strings.Contains(emptyJSON, key+"[]") {
			t.Errorf("empty report key %s not encoded as []:\n%s", key, emptyJSON)
		}
	}
	for _, leftover := range []string{"R1", "R4", "shared", "b1", "a2", "HACK", "GHOST"} {
		if strings.Contains(emptyJSON, leftover) {
			t.Errorf("empty report carries a leftover entry %q from an earlier comparison:\n%s", leftover, emptyJSON)
		}
	}

	// Two empty results are independent as well: appending a display entry to
	// one must not give the other any content.
	emptyToo := CompareSnapshots(identical, identical)
	empty.NewDatasets = append(empty.NewDatasets, "GHOST")
	empty.RootSourceChanges = append(empty.RootSourceChanges, RootSourceChange{Dataset: "FAKE"})
	assertEmptyReport(t, emptyToo)

	// A further identical comparison, after the edits, is still its own empty
	// result with the same JSON bytes.
	emptyAgain := CompareSnapshots(identical, identical)
	assertEmptyReport(t, emptyAgain)
	if got := mustMarshalCompareReport(t, emptyAgain); !strings.Contains(string(got), `"rootSourceChanges":[]`) {
		t.Errorf("repeated empty comparison did not encode []:\n%s", got)
	}
}

// TestCompareReportBytesUnaffectedByRecordOrderAndDuplicates feeds the
// headline scenario in shuffled form with shuffled and repeated upstreams;
// the unedited report must come out content- and byte-identical to the
// canonical comparison, with verbatim case/space name spellings in the
// root-source entries and the existing byte-order sorting on every list.
func TestCompareReportBytesUnaffectedByRecordOrderAndDuplicates(t *testing.T) {
	oldCanonical := snapshotOf(t, intersectRootsOldGraph)
	newCanonical := snapshotOf(t, intersectRootsNewGraph)
	wantBytes := mustMarshalCompareReport(t, CompareSnapshots(oldCanonical, newCanonical))
	oldBytesBefore := mustMarshalSnapshot(t, oldCanonical)
	newBytesBefore := mustMarshalSnapshot(t, newCanonical)

	// Same semantics as the headline fixtures: records shuffled, upstream
	// lists shuffled and repeated, including D1's multipath dependency.
	oldShuffled := snapshotOf(t, `{"datasets":[
		{"name":"D2","upstreams":["shared"]},
		{"upstreams":["shared","a1","a1"],"name":"D1"},
		{"upstreams":["a2","a1","a2"],"name":"shared"},
		{"name":"a2","upstreams":["R3","R2"]},
		{"upstreams":["R2","R1","R1"],"name":"a1"},
		{"name":"R3","upstreams":[]},
		{"name":"R2","upstreams":[]},
		{"name":"R1","upstreams":[]}
	]}`)
	newShuffled := snapshotOf(t, `{"datasets":[
		{"upstreams":["shared"],"name":"D2"},
		{"name":"D1","upstreams":["shared","b1","b1"]},
		{"upstreams":["b2","b1","b2"],"name":"shared"},
		{"upstreams":["R4","R3"],"name":"b2"},
		{"name":"b1","upstreams":["R4","R2","R2"]},
		{"name":"R4","upstreams":[]},
		{"name":"R3","upstreams":[]},
		{"name":"R2","upstreams":[]}
	]}`)

	report := CompareSnapshots(oldShuffled, newShuffled)
	if got := mustMarshalCompareReport(t, report); !bytes.Equal(got, wantBytes) {
		t.Fatalf("shuffled/duplicated inputs produced a different report:\n got %s\nwant %s", got, wantBytes)
	}
	if !reflect.DeepEqual(report.RootSourceChanges, wantIntersectRootChanges) {
		t.Errorf("shuffled inputs root changes = %v, want %v", report.RootSourceChanges, wantIntersectRootChanges)
	}

	// Comparison stayed read-only on the canonical snapshots handed in.
	if got := mustMarshalSnapshot(t, oldCanonical); !bytes.Equal(got, oldBytesBefore) {
		t.Errorf("old snapshot bytes changed during comparison:\n got %s\nwant %s", got, oldBytesBefore)
	}
	if got := mustMarshalSnapshot(t, newCanonical); !bytes.Equal(got, newBytesBefore) {
		t.Errorf("new snapshot bytes changed during comparison:\n got %s\nwant %s", got, newBytesBefore)
	}

	// Editing the shuffled-input report's first entry cannot rewrite the
	// verbatim spellings a second report carries — here with case/space names.
	caseOld := snapshotOf(t, `{"datasets":[
		{"name":" A ","upstreams":[]},
		{"name":"a","upstreams":[]},
		{"name":"A","upstreams":[]},
		{"name":"M","upstreams":[" A ","a"]},
		{"name":"n","upstreams":["M"]}
	]}`)
	caseNew := snapshotOf(t, `{"datasets":[
		{"name":" A ","upstreams":[]},
		{"name":"a","upstreams":[]},
		{"name":"A","upstreams":[]},
		{"name":"M","upstreams":["A"]},
		{"name":"n","upstreams":["M"]}
	]}`)
	first := CompareSnapshots(caseOld, caseNew)
	second := CompareSnapshots(caseOld, caseNew)
	secondBytes := mustMarshalCompareReport(t, second)
	wantCaseChanges := []RootSourceChange{
		{Dataset: "M", OldRoots: []string{" A ", "a"}, NewRoots: []string{"A"}},
		{Dataset: "n", OldRoots: []string{" A ", "a"}, NewRoots: []string{"A"}},
	}
	if !reflect.DeepEqual(first.RootSourceChanges, wantCaseChanges) {
		t.Fatalf("case/space root changes = %v, want %v", first.RootSourceChanges, wantCaseChanges)
	}
	first.RootSourceChanges[0].OldRoots[0] = "trimmed"
	first.RootSourceChanges[0].OldRoots = append(first.RootSourceChanges[0].OldRoots, "HACK")
	first.RootSourceChanges[0].Dataset = "HACK"
	if !reflect.DeepEqual(second.RootSourceChanges, wantCaseChanges) {
		t.Errorf("case/space spellings moved into the other report: %v", second.RootSourceChanges)
	}
	if got := mustMarshalCompareReport(t, second); !bytes.Equal(got, secondBytes) {
		t.Errorf("case/space report JSON changed after editing the first:\n got %s\nwant %s", got, secondBytes)
	}
}

// dupNames returns the names appearing more than once in list.
func dupNames(list []string) []string {
	seen := make(map[string]int)
	var dups []string
	for _, name := range list {
		seen[name]++
		if seen[name] == 2 {
			dups = append(dups, name)
		}
	}
	return dups
}
