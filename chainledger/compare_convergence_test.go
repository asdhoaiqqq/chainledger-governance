package chainledger

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

// Regression coverage for snapshot comparison on graphs where several lineage
// paths converge at one summary dataset and derivation continues past it.
//
// The shared fixture keeps two upstream-less roots (rootA, rootB), three
// intermediate branches (branchA1 and branchA2 depend only on rootA, branchB1
// depends on rootB), one summary where every branch converges, one dataset
// derived after the summary, and one unrelated branch (soloRoot/soloChild)
// that never changes. The compare reports must keep two questions apart:
//
//   - changedDatasets / addedRelations / removedRelations answer "which DIRECT
//     upstream relationships changed", and never count an indirect path as a
//     direct relation;
//   - rootSourceChanges answers "which datasets present in BOTH versions reach
//     a different set of roots", so a dataset whose direct upstreams are
//     unchanged can still appear there when an earlier branch moved, while a
//     mere swap onto another path to the SAME root set never appears.
//
// Both graphs below are legal: every upstream resolves and neither graph has
// a cycle.
const convergenceOldGraph = `{"datasets":[
	{"name":"rootA","upstreams":[]},
	{"name":"rootB","upstreams":[]},
	{"name":"branchA1","upstreams":["rootA"]},
	{"name":"branchA2","upstreams":["rootA"]},
	{"name":"branchB1","upstreams":["rootB"]},
	{"name":"summary","upstreams":["branchA1","branchA2","branchB1"]},
	{"name":"derived","upstreams":["summary"]},
	{"name":"soloRoot","upstreams":[]},
	{"name":"soloChild","upstreams":["soloRoot"]}
]}`

// TestCompareConvergenceOneBranchRerouted is the headline case: one branch
// that used to depend only on rootA is repointed at rootB; every other
// relationship is untouched. The summary and the dataset derived after it can
// still reach both roots (rootA survives via branchA2, rootB via branchB1 and
// the repointed branch), so they must NOT enter the root-source change list.
// The repointed branch itself changes source rootA -> rootB and must be
// reported even though the converged summary result is unchanged.
func TestCompareConvergenceOneBranchRerouted(t *testing.T) {
	oldSnap := snapshotOf(t, convergenceOldGraph)
	newSnap := snapshotOf(t, `{"datasets":[
		{"name":"rootA","upstreams":[]},
		{"name":"rootB","upstreams":[]},
		{"name":"branchA1","upstreams":["rootB"]},
		{"name":"branchA2","upstreams":["rootA"]},
		{"name":"branchB1","upstreams":["rootB"]},
		{"name":"summary","upstreams":["branchA1","branchA2","branchB1"]},
		{"name":"derived","upstreams":["summary"]},
		{"name":"soloRoot","upstreams":[]},
		{"name":"soloChild","upstreams":["soloRoot"]}
	]}`)

	report := CompareSnapshots(oldSnap, newSnap)

	if len(report.NewDatasets) != 0 {
		t.Errorf("NewDatasets = %v, want []", report.NewDatasets)
	}
	if len(report.RemovedDatasets) != 0 {
		t.Errorf("RemovedDatasets = %v, want []", report.RemovedDatasets)
	}
	if got, want := report.ChangedDatasets, []string{"branchA1"}; !reflect.DeepEqual(got, want) {
		t.Errorf("ChangedDatasets = %v, want %v", got, want)
	}
	// Only the two direct edges incident on the repointed branch differ; the
	// still-reachable rootA -> branchA1 path through branchA2 is indirect and
	// must not be reported as an added or removed relation.
	if got, want := report.AddedRelations, []Relation{{Upstream: "rootB", Downstream: "branchA1"}}; !reflect.DeepEqual(got, want) {
		t.Errorf("AddedRelations = %v, want %v", got, want)
	}
	if got, want := report.RemovedRelations, []Relation{{Upstream: "rootA", Downstream: "branchA1"}}; !reflect.DeepEqual(got, want) {
		t.Errorf("RemovedRelations = %v, want %v", got, want)
	}
	wantRootChanges := []RootSourceChange{
		{Dataset: "branchA1", OldRoots: []string{"rootA"}, NewRoots: []string{"rootB"}},
	}
	if got := report.RootSourceChanges; !reflect.DeepEqual(got, wantRootChanges) {
		t.Errorf("RootSourceChanges = %v, want %v", got, wantRootChanges)
	}

	// Explicitly pin the convergence guarantee: the summary still reaches both
	// roots through its other branches, so neither it nor the dataset derived
	// after it is a source change despite the repoint upstream of them.
	for _, name := range []string{"summary", "derived"} {
		if got := rootSourceNames(name, adjacencyFromValidFile(oldSnap.Graph)); !reflect.DeepEqual(got, []string{"rootA", "rootB"}) {
			t.Errorf("old rootSourceNames(%s) = %v, want [rootA rootB]", name, got)
		}
		if got := rootSourceNames(name, adjacencyFromValidFile(newSnap.Graph)); !reflect.DeepEqual(got, []string{"rootA", "rootB"}) {
			t.Errorf("new rootSourceNames(%s) = %v, want [rootA rootB]", name, got)
		}
	}
	assertNameAbsentFromReport(t, report, "summary")
	assertNameAbsentFromReport(t, report, "derived")
	assertNameAbsentFromReport(t, report, "branchA2")
	assertNameAbsentFromReport(t, report, "branchB1")
	assertNameAbsentFromReport(t, report, "soloRoot")
	assertNameAbsentFromReport(t, report, "soloChild")
}

// TestCompareConvergenceAllRootAPathsRerouted covers the same converging graph
// when the remaining path to rootA (branchA2) is also rewired to rootB. After
// the change the summary and the derived dataset can no longer reach rootA,
// so both must be listed exactly once with their complete old and new root
// sets ([rootA,rootB] -> [rootB]), even though neither dataset's DIRECT
// upstreams changed and therefore neither belongs in changedDatasets.
func TestCompareConvergenceAllRootAPathsRerouted(t *testing.T) {
	oldSnap := snapshotOf(t, convergenceOldGraph)
	newSnap := snapshotOf(t, `{"datasets":[
		{"name":"rootA","upstreams":[]},
		{"name":"rootB","upstreams":[]},
		{"name":"branchA1","upstreams":["rootB"]},
		{"name":"branchA2","upstreams":["rootB"]},
		{"name":"branchB1","upstreams":["rootB"]},
		{"name":"summary","upstreams":["branchA1","branchA2","branchB1"]},
		{"name":"derived","upstreams":["summary"]},
		{"name":"soloRoot","upstreams":[]},
		{"name":"soloChild","upstreams":["soloRoot"]}
	]}`)

	report := CompareSnapshots(oldSnap, newSnap)

	if got, want := report.ChangedDatasets, []string{"branchA1", "branchA2"}; !reflect.DeepEqual(got, want) {
		t.Errorf("ChangedDatasets = %v, want %v; downstream datasets must not join just because their sources changed", got, want)
	}
	if got, want := report.AddedRelations, []Relation{
		{Upstream: "rootB", Downstream: "branchA1"},
		{Upstream: "rootB", Downstream: "branchA2"},
	}; !reflect.DeepEqual(got, want) {
		t.Errorf("AddedRelations = %v, want %v", got, want)
	}
	if got, want := report.RemovedRelations, []Relation{
		{Upstream: "rootA", Downstream: "branchA1"},
		{Upstream: "rootA", Downstream: "branchA2"},
	}; !reflect.DeepEqual(got, want) {
		t.Errorf("RemovedRelations = %v, want %v", got, want)
	}

	// Entries are sorted by dataset name; summary and derived each appear
	// exactly once, with full sorted root sets on both sides.
	wantRootChanges := []RootSourceChange{
		{Dataset: "branchA1", OldRoots: []string{"rootA"}, NewRoots: []string{"rootB"}},
		{Dataset: "branchA2", OldRoots: []string{"rootA"}, NewRoots: []string{"rootB"}},
		{Dataset: "derived", OldRoots: []string{"rootA", "rootB"}, NewRoots: []string{"rootB"}},
		{Dataset: "summary", OldRoots: []string{"rootA", "rootB"}, NewRoots: []string{"rootB"}},
	}
	if got := report.RootSourceChanges; !reflect.DeepEqual(got, wantRootChanges) {
		t.Errorf("RootSourceChanges = %v, want %v", got, wantRootChanges)
	}

	// Direct-upstream sets of summary and derived are identical in both
	// versions: their presence in rootSourceChanges must not also surface them
	// as direct changes.
	if containsString(report.ChangedDatasets, "summary") || containsString(report.ChangedDatasets, "derived") {
		t.Errorf("summary/derived have unchanged direct upstreams but appear in ChangedDatasets %v", report.ChangedDatasets)
	}
	// The roots themselves keep reaching only themselves, and the unrelated
	// branch is untouched. The roots may still appear as endpoints of the
	// actually added/removed direct edges, so only node lists and the
	// root-change list are checked here.
	assertDatasetNotAChange(t, report, "rootA")
	assertDatasetNotAChange(t, report, "rootB")
	assertNameAbsentFromReport(t, report, "branchB1")
	assertNameAbsentFromReport(t, report, "soloRoot")
	assertNameAbsentFromReport(t, report, "soloChild")
}

// assertDatasetNotAChange fails if name appears in any node list or as the
// dataset of a root-source change. Unlike assertNameAbsentFromReport it does
// not inspect relation endpoints, which a root may legitimately touch when an
// edge from that root is genuinely added or removed.
func assertDatasetNotAChange(t *testing.T, report *CompareReport, name string) {
	t.Helper()
	if containsString(report.NewDatasets, name) {
		t.Errorf("%q must not be a new dataset; report = %+v", name, report)
	}
	if containsString(report.RemovedDatasets, name) {
		t.Errorf("%q must not be a removed dataset; report = %+v", name, report)
	}
	if containsString(report.ChangedDatasets, name) {
		t.Errorf("%q must not be a directly changed dataset; report = %+v", name, report)
	}
	for _, change := range report.RootSourceChanges {
		if change.Dataset == name {
			t.Errorf("%q must not appear in root source changes; report = %+v", name, report)
		}
	}
}

// TestCompareConvergenceRerouteOntoPathToSameRoot: a branch may be moved onto
// a different path that still leads to the same root. branchA1 stops
// depending directly on rootA and instead depends on branchA2 (which reaches
// rootA); the direct edge rootA -> branchA1 disappears and branchA2 ->
// branchA1 appears, but every dataset still reaches the same root set, so the
// root-source change list must stay empty. The indirect reachability of rootA
// from branchA1 must not be invented as an added direct relation.
func TestCompareConvergenceRerouteOntoPathToSameRoot(t *testing.T) {
	oldSnap := snapshotOf(t, convergenceOldGraph)
	newSnap := snapshotOf(t, `{"datasets":[
		{"name":"rootA","upstreams":[]},
		{"name":"rootB","upstreams":[]},
		{"name":"branchA1","upstreams":["branchA2"]},
		{"name":"branchA2","upstreams":["rootA"]},
		{"name":"branchB1","upstreams":["rootB"]},
		{"name":"summary","upstreams":["branchA1","branchA2","branchB1"]},
		{"name":"derived","upstreams":["summary"]},
		{"name":"soloRoot","upstreams":[]},
		{"name":"soloChild","upstreams":["soloRoot"]}
	]}`)

	report := CompareSnapshots(oldSnap, newSnap)
	if got, want := report.ChangedDatasets, []string{"branchA1"}; !reflect.DeepEqual(got, want) {
		t.Errorf("ChangedDatasets = %v, want %v", got, want)
	}
	if got, want := report.AddedRelations, []Relation{{Upstream: "branchA2", Downstream: "branchA1"}}; !reflect.DeepEqual(got, want) {
		t.Errorf("AddedRelations = %v, want %v", got, want)
	}
	if got, want := report.RemovedRelations, []Relation{{Upstream: "rootA", Downstream: "branchA1"}}; !reflect.DeepEqual(got, want) {
		t.Errorf("RemovedRelations = %v, want %v", got, want)
	}
	if len(report.RootSourceChanges) != 0 {
		t.Errorf("a path-only swap to the same root set must not report source changes, got %v", report.RootSourceChanges)
	}
	// Sanity-check the rerouted branch still resolves to rootA, now indirectly.
	if got := rootSourceNames("branchA1", adjacencyFromValidFile(newSnap.Graph)); !reflect.DeepEqual(got, []string{"rootA"}) {
		t.Errorf("new rootSourceNames(branchA1) = %v, want [rootA]", got)
	}
}

// TestCompareConvergenceReportIsDeterministicBytes: record order, upstream
// order, and repeated upstreams carry no semantics, so feeding the converging
// scenario (the all-paths-rerouted version, which exercises every non-empty
// list) in shuffled, duplicated form must produce a byte-identical report.
// Comparison also stays read-only: neither input snapshot is modified.
func TestCompareConvergenceReportIsDeterministicBytes(t *testing.T) {
	oldCanonical := snapshotOf(t, convergenceOldGraph)
	newCanonical := snapshotOf(t, `{"datasets":[
		{"name":"rootA","upstreams":[]},
		{"name":"rootB","upstreams":[]},
		{"name":"branchA1","upstreams":["rootB"]},
		{"name":"branchA2","upstreams":["rootB"]},
		{"name":"branchB1","upstreams":["rootB"]},
		{"name":"summary","upstreams":["branchA1","branchA2","branchB1"]},
		{"name":"derived","upstreams":["summary"]},
		{"name":"soloRoot","upstreams":[]},
		{"name":"soloChild","upstreams":["soloRoot"]}
	]}`)

	// Same semantics: records shuffled, summary's upstream list shuffled and
	// repeated, the repointed branches list their single (repeated) upstream.
	oldShuffled := snapshotOf(t, `{"datasets":[
		{"upstreams":["soloRoot"],"name":"soloChild"},
		{"upstreams":[],"name":"soloRoot"},
		{"name":"derived","upstreams":["summary","summary"]},
		{"name":"summary","upstreams":["branchB1","branchA1","branchA2","branchA1"]},
		{"name":"branchB1","upstreams":["rootB"]},
		{"name":"branchA2","upstreams":["rootA","rootA"]},
		{"name":"branchA1","upstreams":["rootA"]},
		{"name":"rootB","upstreams":[]},
		{"name":"rootA","upstreams":[]}
	]}`)
	newShuffled := snapshotOf(t, `{"datasets":[
		{"name":"soloChild","upstreams":["soloRoot"]},
		{"name":"soloRoot","upstreams":[]},
		{"name":"derived","upstreams":["summary"]},
		{"upstreams":["branchA2","branchB1","branchA1","branchA2"],"name":"summary"},
		{"upstreams":["rootB"],"name":"branchB1"},
		{"upstreams":["rootB","rootB"],"name":"branchA2"},
		{"upstreams":["rootB"],"name":"branchA1"},
		{"name":"rootB","upstreams":[]},
		{"name":"rootA","upstreams":[]}
	]}`)

	oldBytesBefore, err := MarshalSnapshot(oldCanonical)
	if err != nil {
		t.Fatalf("marshal old snapshot: %v", err)
	}
	newBytesBefore, err := MarshalSnapshot(newCanonical)
	if err != nil {
		t.Fatalf("marshal new snapshot: %v", err)
	}

	want, err := json.Marshal(CompareSnapshots(oldCanonical, newCanonical))
	if err != nil {
		t.Fatalf("marshal canonical report: %v", err)
	}
	got, err := json.Marshal(CompareSnapshots(oldShuffled, newShuffled))
	if err != nil {
		t.Fatalf("marshal shuffled report: %v", err)
	}
	if string(got) != string(want) {
		t.Fatalf("compare bytes differ for semantically equal inputs:\n got %s\nwant %s", got, want)
	}

	// Read-only: the snapshot documents handed to the comparison are intact.
	oldBytesAfter, err := MarshalSnapshot(oldCanonical)
	if err != nil {
		t.Fatalf("re-marshal old snapshot: %v", err)
	}
	newBytesAfter, err := MarshalSnapshot(newCanonical)
	if err != nil {
		t.Fatalf("re-marshal new snapshot: %v", err)
	}
	if string(oldBytesAfter) != string(oldBytesBefore) || string(newBytesAfter) != string(newBytesBefore) {
		t.Errorf("CompareSnapshots mutated an input snapshot")
	}
}

// TestCompareConvergenceNoSourceChangesEncodesEmptyArray pins the empty-list
// rule on the converging graph: when two versions differ only by a path-only
// reroute (and by record/upstream ordering), rootSourceChanges must still
// serialize as [], and the whole report is byte-identical across equivalent
// inputs.
func TestCompareConvergenceNoSourceChangesEncodesEmptyArray(t *testing.T) {
	oldSnap := snapshotOf(t, convergenceOldGraph)
	newSnap := snapshotOf(t, `{"datasets":[
		{"name":"rootA","upstreams":[]},
		{"name":"rootB","upstreams":[]},
		{"name":"branchA1","upstreams":["branchA2"]},
		{"name":"branchA2","upstreams":["rootA"]},
		{"name":"branchB1","upstreams":["rootB"]},
		{"name":"summary","upstreams":["branchA1","branchA2","branchB1"]},
		{"name":"derived","upstreams":["summary"]},
		{"name":"soloRoot","upstreams":[]},
		{"name":"soloChild","upstreams":["soloRoot"]}
	]}`)
	newShuffled := snapshotOf(t, `{"datasets":[
		{"upstreams":["summary"],"name":"derived"},
		{"upstreams":["branchB1","branchA2","branchA1"],"name":"summary"},
		{"name":"soloChild","upstreams":["soloRoot","soloRoot"]},
		{"name":"branchA1","upstreams":["branchA2","branchA2"]},
		{"name":"branchA2","upstreams":["rootA"]},
		{"name":"branchB1","upstreams":["rootB"]},
		{"name":"soloRoot","upstreams":[]},
		{"name":"rootB","upstreams":[]},
		{"name":"rootA","upstreams":[]}
	]}`)

	b1, err := json.Marshal(CompareSnapshots(oldSnap, newSnap))
	if err != nil {
		t.Fatalf("marshal report: %v", err)
	}
	b2, err := json.Marshal(CompareSnapshots(oldSnap, newShuffled))
	if err != nil {
		t.Fatalf("marshal shuffled report: %v", err)
	}
	if string(b1) != string(b2) {
		t.Fatalf("equivalent inputs produced different reports:\n%s\n%s", b1, b2)
	}
	if !strings.Contains(string(b1), `"rootSourceChanges":[]`) {
		t.Errorf("rootSourceChanges must encode as [] when nothing changed source:\n%s", b1)
	}
}

// containsString reports whether list contains name.
func containsString(list []string, name string) bool {
	for _, item := range list {
		if item == name {
			return true
		}
	}
	return false
}

// assertNameAbsentFromReport fails if name shows up in any node list or as the
// dataset of a root-source change.
func assertNameAbsentFromReport(t *testing.T, report *CompareReport, name string) {
	t.Helper()
	if containsString(report.NewDatasets, name) {
		t.Errorf("%q must not be a new dataset; report = %+v", name, report)
	}
	if containsString(report.RemovedDatasets, name) {
		t.Errorf("%q must not be a removed dataset; report = %+v", name, report)
	}
	if containsString(report.ChangedDatasets, name) {
		t.Errorf("%q must not be a directly changed dataset; report = %+v", name, report)
	}
	for _, rel := range report.AddedRelations {
		if rel.Upstream == name || rel.Downstream == name {
			t.Errorf("%q must not appear in added relations; report = %+v", name, report)
		}
	}
	for _, rel := range report.RemovedRelations {
		if rel.Upstream == name || rel.Downstream == name {
			t.Errorf("%q must not appear in removed relations; report = %+v", name, report)
		}
	}
	for _, change := range report.RootSourceChanges {
		if change.Dataset == name {
			t.Errorf("%q must not appear in root source changes; report = %+v", name, report)
		}
	}
}
