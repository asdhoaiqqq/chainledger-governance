package chainledger

import (
	"errors"
	"reflect"
	"sort"
	"testing"
)

// This file is the cross-report regression net: a user looking at the same
// pair of snapshots through BOTH reports — the read-only `compare` report and
// the read-only `trace` report — must always get one consistent story about
// root sources. The compare report's rootSourceChanges and the trace report's
// sources are computed by separate code paths, so nothing but these tests
// forces them to agree dataset by dataset:
//
//   - for every dataset common to both versions, compare's oldRoots and
//     newRoots must be EXACTLY the root sets TraceSources returns in the old
//     and the new snapshot (each trace root reported once, sorted by root name
//     byte order); compare lists the dataset if and only if those two sets
//     differ — neither missing a changed dataset nor reporting a
//     representative-path-only change;
//   - a root reachable through several branches never disappears from either
//     report when only one branch changes, and converged datasets (and the
//     datasets derived after them) keep their complete, deduplicated root set;
//   - when the last route to an old root vanishes while other roots stay
//     reachable (including through a dataset whose DIRECT upstreams did not
//     change), both reports keep only the roots still reachable and compare
//     lists every affected common dataset with full old and new sets;
//   - neither report ever mistakes an intermediate that still has upstreams
//     for a root, or admits a disconnected root;
//   - a common dataset cleared of every upstream becomes its own sole trace
//     root with a single-element path and compare's new root set contains
//     only it; a dataset in only one version is new/removed (never a source
//     change) and tracing it in the version that lacks it fails explicitly
//     with no success report.
//
// Record order, upstream order, and duplicate upstreams carry no semantics, so
// the same answers must result from semantically equal encodings.

// tracedRoots traces dataset within snap and returns the sorted set of root
// names the trace reports. It fails the test if the trace itself fails.
func tracedRoots(t *testing.T, snap *SnapshotFile, dataset string) []string {
	t.Helper()
	report, err := TraceSources(snap, dataset)
	if err != nil {
		t.Fatalf("TraceSources(%q): %v", dataset, err)
	}
	roots := make([]string, 0, len(report.Sources))
	seen := map[string]bool{}
	for _, src := range report.Sources {
		if seen[src.Root] {
			t.Fatalf("TraceSources(%q) reports root %q more than once: %+v", dataset, src.Root, report.Sources)
		}
		seen[src.Root] = true
		roots = append(roots, src.Root)
	}
	if !sort.StringsAreSorted(roots) {
		t.Fatalf("TraceSources(%q) roots are not in byte order: %v", dataset, roots)
	}
	return roots
}

// commonDatasets returns the names present in both snapshots, sorted.
func commonDatasets(oldAdj, newAdj adjacency) []string {
	common := make([]string, 0)
	for name := range oldAdj {
		if _, ok := newAdj[name]; ok {
			common = append(common, name)
		}
	}
	sort.Strings(common)
	return common
}

// assertTraceAgreesWithCompare is the central invariant of this file. For the
// given snapshot pair it requires, for every common dataset, that the trace
// report's root set equals compare's recorded old/new root sets, and that the
// dataset appears among rootSourceChanges if and only if the two traced root
// sets differ. It also checks the one-sided datasets: they must be classified
// as new/removed, must never appear as a source change, and tracing them in
// the version that lacks them must fail explicitly without a report.
func assertTraceAgreesWithCompare(t *testing.T, oldSnap, newSnap *SnapshotFile, report *CompareReport) {
	t.Helper()
	oldAdj := adjacencyFromValidFile(oldSnap.Graph)
	newAdj := adjacencyFromValidFile(newSnap.Graph)

	changed := map[string]RootSourceChange{}
	for _, c := range report.RootSourceChanges {
		if _, dup := changed[c.Dataset]; dup {
			t.Fatalf("dataset %q listed more than once in rootSourceChanges: %+v", c.Dataset, report.RootSourceChanges)
		}
		changed[c.Dataset] = c
	}

	for _, name := range commonDatasets(oldAdj, newAdj) {
		wantOld := tracedRoots(t, oldSnap, name)
		wantNew := tracedRoots(t, newSnap, name)
		setsDiffer := !stringSliceEqual(wantOld, wantNew)

		entry, listed := changed[name]
		if listed != setsDiffer {
			t.Errorf("dataset %q listed-as-source-change = %v, but traced roots old=%v new=%v differ=%v",
				name, listed, wantOld, wantNew, setsDiffer)
		}
		if !listed {
			continue
		}
		// oldRoots/newRoots must be exactly the trace root sets, sorted and
		// non-nil so empty sets encode as [].
		if got := entry.OldRoots; !reflect.DeepEqual(got, wantOld) {
			t.Errorf("dataset %q OldRoots = %v, want trace root set %v", name, got, wantOld)
		}
		if got := entry.NewRoots; !reflect.DeepEqual(got, wantNew) {
			t.Errorf("dataset %q NewRoots = %v, want trace root set %v", name, got, wantNew)
		}
		if entry.OldRoots == nil || entry.NewRoots == nil {
			t.Errorf("dataset %q root sets must be non-nil so they encode as [], got %+v", name, entry)
		}
	}

	// One-sided datasets belong only to newDatasets/removedDatasets and never
	// to the source-change list; tracing one in the version that lacks it
	// fails explicitly and returns no report.
	wantNew := map[string]bool{}
	for _, n := range report.NewDatasets {
		wantNew[n] = true
		if _, ok := oldAdj[n]; ok {
			t.Errorf("dataset %q is a newDatasets entry but present in the old snapshot", n)
		}
	}
	wantRemoved := map[string]bool{}
	for _, n := range report.RemovedDatasets {
		wantRemoved[n] = true
		if _, ok := newAdj[n]; ok {
			t.Errorf("dataset %q is a removedDatasets entry but present in the new snapshot", n)
		}
	}
	for name := range newAdj {
		if _, common := oldAdj[name]; common {
			continue
		}
		if !wantNew[name] {
			t.Errorf("dataset %q exists only in the new snapshot but is not in newDatasets %v", name, report.NewDatasets)
		}
		if _, listed := changed[name]; listed {
			t.Errorf("new-only dataset %q must not appear in rootSourceChanges", name)
		}
		assertTraceMissing(t, oldSnap, name)
	}
	for name := range oldAdj {
		if _, common := newAdj[name]; common {
			continue
		}
		if !wantRemoved[name] {
			t.Errorf("dataset %q exists only in the old snapshot but is not in removedDatasets %v", name, report.RemovedDatasets)
		}
		if _, listed := changed[name]; listed {
			t.Errorf("removed-only dataset %q must not appear in rootSourceChanges", name)
		}
		assertTraceMissing(t, newSnap, name)
	}
}

// assertTraceMissing requires that tracing dataset within snap fails with
// ErrNotFound and returns no report.
func assertTraceMissing(t *testing.T, snap *SnapshotFile, dataset string) {
	t.Helper()
	report, err := TraceSources(snap, dataset)
	if report != nil {
		t.Errorf("trace of %q absent from a snapshot returned a report: %+v, want nil", dataset, report)
	}
	if !errors.Is(err, ErrNotFound) {
		t.Errorf("trace of absent %q err = %v, want ErrNotFound", dataset, err)
	}
}

// The headline converging lineage used by the path-change scenarios. Two roots
// feed three branches that converge at summary, from which derived continues;
// soloRoot/soloChild are an unrelated component.
const consistencyOldGraph = `{"datasets":[
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

// TestTraceCompareAgreeRerouteOneBranchOntoSameRoot: branchA1 moves onto
// another path (branchA2) that still leads to rootA, and gains a shortcut.
// The shortest representative paths in the trace change, but every common
// dataset still reaches the identical root set, so compare must report no
// source change even though the direct-upstream change is reported normally.
func TestTraceCompareAgreeRerouteOneBranchOntoSameRoot(t *testing.T) {
	oldSnap := snapshotOf(t, consistencyOldGraph)
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
		t.Fatalf("ChangedDatasets = %v, want %v", got, want)
	}
	if len(report.RootSourceChanges) != 0 {
		t.Fatalf("a path-only reroute to the same root sets must list no source change, got %v", report.RootSourceChanges)
	}

	// Pin the trace side of "the representative path moved but the root set did
	// not": branchA1 used to reach rootA directly and now reaches it through
	// branchA2, still exactly once; summary and derived keep both roots with
	// complete, deduplicated sets despite the converging branches.
	if got := tracedRoots(t, newSnap, "branchA1"); !reflect.DeepEqual(got, []string{"rootA"}) {
		t.Errorf("new trace roots(branchA1) = %v, want [rootA]", got)
	}
	if report, err := TraceSources(newSnap, "branchA1"); err != nil || !reflect.DeepEqual(report.Sources,
		[]SourceTrace{{Root: "rootA", Path: []string{"branchA1", "branchA2", "rootA"}}}) {
		t.Errorf("new trace(branchA1) = %+v err=%v, want the longer representative path to rootA", report, err)
	}
	for _, name := range []string{"summary", "derived"} {
		if got := tracedRoots(t, oldSnap, name); !reflect.DeepEqual(got, []string{"rootA", "rootB"}) {
			t.Errorf("old trace roots(%s) = %v, want [rootA rootB]", name, got)
		}
		if got := tracedRoots(t, newSnap, name); !reflect.DeepEqual(got, []string{"rootA", "rootB"}) {
			t.Errorf("new trace roots(%s) = %v, want [rootA rootB]", name, got)
		}
	}
	// The unrelated component and the roots themselves must not be touched.
	assertNameAbsentFromReport(t, report, "soloRoot")
	assertNameAbsentFromReport(t, report, "soloChild")
	assertDatasetNotAChange(t, report, "rootA")
	assertDatasetNotAChange(t, report, "rootB")

	assertTraceAgreesWithCompare(t, oldSnap, newSnap, report)
}

// TestTraceCompareAgreeShortcutToExistingRoot: adding a shortcut edge from the
// convergence point straight to rootA shortens that root's representative
// trace path but neither adds nor removes any root. Neither the converged
// summary nor derived may be reported as a source change.
func TestTraceCompareAgreeShortcutToExistingRoot(t *testing.T) {
	oldSnap := snapshotOf(t, consistencyOldGraph)
	newSnap := snapshotOf(t, `{"datasets":[
		{"name":"rootA","upstreams":[]},
		{"name":"rootB","upstreams":[]},
		{"name":"branchA1","upstreams":["rootA"]},
		{"name":"branchA2","upstreams":["rootA"]},
		{"name":"branchB1","upstreams":["rootB"]},
		{"name":"summary","upstreams":["rootA","branchA1","branchA2","branchB1"]},
		{"name":"derived","upstreams":["summary"]},
		{"name":"soloRoot","upstreams":[]},
		{"name":"soloChild","upstreams":["soloRoot"]}
	]}`)
	report := CompareSnapshots(oldSnap, newSnap)

	if got, want := report.ChangedDatasets, []string{"summary"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("ChangedDatasets = %v, want %v", got, want)
	}
	if got, want := report.AddedRelations, []Relation{{Upstream: "rootA", Downstream: "summary"}}; !reflect.DeepEqual(got, want) {
		t.Errorf("AddedRelations = %v, want %v", got, want)
	}
	if len(report.RootSourceChanges) != 0 {
		t.Fatalf("a shortcut to an already reachable root must list no source change, got %v", report.RootSourceChanges)
	}

	// The shortcut changes only rootA's representative path for summary; both
	// roots remain, each reported once with its own path, root order unchanged.
	summaryTrace, err := TraceSources(newSnap, "summary")
	if err != nil {
		t.Fatalf("TraceSources(summary): %v", err)
	}
	wantSummary := []SourceTrace{
		{Root: "rootA", Path: []string{"summary", "rootA"}},
		{Root: "rootB", Path: []string{"summary", "branchB1", "rootB"}},
	}
	if !reflect.DeepEqual(summaryTrace.Sources, wantSummary) {
		t.Errorf("new trace(summary) = %+v, want %+v", summaryTrace.Sources, wantSummary)
	}
	assertTraceAgreesWithCompare(t, oldSnap, newSnap, report)
}

// TestTraceCompareAgreeMultipathRootSurvivesOneBranchChange is the "same root
// through several branches" guarantee: rootA is reachable from the converged
// datasets through both branchA1 and branchA2. Repointing branchA1 at rootB
// must not make rootA disappear (branchA2 still carries it), so summary and
// derived keep the complete deduplicated set {rootA,rootB}; only branchA1's
// own source set changes. The direct-upstream change is still reported.
func TestTraceCompareAgreeMultipathRootSurvivesOneBranchChange(t *testing.T) {
	oldSnap := snapshotOf(t, consistencyOldGraph)
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

	wantChanges := []RootSourceChange{
		{Dataset: "branchA1", OldRoots: []string{"rootA"}, NewRoots: []string{"rootB"}},
	}
	if got := report.RootSourceChanges; !reflect.DeepEqual(got, wantChanges) {
		t.Fatalf("RootSourceChanges = %v, want %v", got, wantChanges)
	}
	if got, want := report.ChangedDatasets, []string{"branchA1"}; !reflect.DeepEqual(got, want) {
		t.Errorf("ChangedDatasets = %v, want %v", got, want)
	}
	// rootA survives via branchA2: both reports keep it for summary/derived.
	for _, name := range []string{"summary", "derived"} {
		if got := tracedRoots(t, newSnap, name); !reflect.DeepEqual(got, []string{"rootA", "rootB"}) {
			t.Errorf("new trace roots(%s) = %v, rootA must survive via the other branch", name, got)
		}
	}
	assertNameAbsentFromReport(t, report, "summary")
	assertNameAbsentFromReport(t, report, "derived")
	assertTraceAgreesWithCompare(t, oldSnap, newSnap, report)
}

// TestTraceCompareAgreeLastPathToRootLostReportsAllAffected is the loss case:
// after rewiring BOTH branchA1 and branchA2 off rootA, the last route to
// rootA from the converged component disappears while rootB stays reachable.
// The trace must keep only the roots still reachable, and compare must list
// EVERY common dataset whose root set changed — including derived, whose
// direct upstream is still [summary] and which lost rootA solely because of an
// upstream-of-upstream adjustment — each with the complete old and new sets.
func TestTraceCompareAgreeLastPathToRootLostReportsAllAffected(t *testing.T) {
	oldSnap := snapshotOf(t, consistencyOldGraph)
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

	wantChanges := []RootSourceChange{
		{Dataset: "branchA1", OldRoots: []string{"rootA"}, NewRoots: []string{"rootB"}},
		{Dataset: "branchA2", OldRoots: []string{"rootA"}, NewRoots: []string{"rootB"}},
		{Dataset: "derived", OldRoots: []string{"rootA", "rootB"}, NewRoots: []string{"rootB"}},
		{Dataset: "summary", OldRoots: []string{"rootA", "rootB"}, NewRoots: []string{"rootB"}},
	}
	if got := report.RootSourceChanges; !reflect.DeepEqual(got, wantChanges) {
		t.Fatalf("RootSourceChanges = %v, want %v", got, wantChanges)
	}

	// derived's direct upstreams did not change, so it must not be a direct
	// change even though its source set moved.
	if got, want := report.ChangedDatasets, []string{"branchA1", "branchA2"}; !reflect.DeepEqual(got, want) {
		t.Errorf("ChangedDatasets = %v, want %v (derived changes only by transitive source)", got, want)
	}

	// Trace side: rootA is no longer reachable from the converged datasets,
	// rootB is the sole surviving root, and rootA remains its own root (it
	// still exists in the new snapshot as an isolated root).
	if got := tracedRoots(t, newSnap, "derived"); !reflect.DeepEqual(got, []string{"rootB"}) {
		t.Errorf("new trace roots(derived) = %v, want only the still-reachable [rootB]", got)
	}
	if got := tracedRoots(t, newSnap, "rootA"); !reflect.DeepEqual(got, []string{"rootA"}) {
		t.Errorf("new trace roots(rootA) = %v, an isolated root is still its own source", got)
	}
	// The intermediate datasets still have upstreams, so neither report may
	// treat them as roots.
	for _, snap := range []*SnapshotFile{oldSnap, newSnap} {
		for _, name := range []string{"branchA1", "branchA2", "branchB1", "summary", "derived"} {
			for _, src := range mustTrace(t, snap, name).Sources {
				switch src.Root {
				case "branchA1", "branchA2", "branchB1", "summary", "derived":
					t.Errorf("intermediate %q reported as a root when tracing %q", src.Root, name)
				}
			}
		}
	}
	assertTraceAgreesWithCompare(t, oldSnap, newSnap, report)
}

// TestTraceCompareAgreeIntermediateLosesSourceViaUpstreamOnly isolates the
// transitive-loss rule in a narrow chain: C keeps its direct upstream [B] in
// both versions, but B is repointed from A to X. C's direct upstream is
// unchanged yet it loses root A and gains X purely through the upstream
// adjustment; compare must list it with full sets and trace must agree.
func TestTraceCompareAgreeIntermediateLosesSourceViaUpstreamOnly(t *testing.T) {
	oldSnap := snapshotOf(t, `{"datasets":[
		{"name":"A","upstreams":[]},
		{"name":"B","upstreams":["A"]},
		{"name":"C","upstreams":["B"]}
	]}`)
	newSnap := snapshotOf(t, `{"datasets":[
		{"name":"A","upstreams":[]},
		{"name":"X","upstreams":[]},
		{"name":"B","upstreams":["X"]},
		{"name":"C","upstreams":["B"]}
	]}`)
	report := CompareSnapshots(oldSnap, newSnap)

	if got, want := report.ChangedDatasets, []string{"B"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("ChangedDatasets = %v, want only %v", got, want)
	}
	wantChanges := []RootSourceChange{
		{Dataset: "B", OldRoots: []string{"A"}, NewRoots: []string{"X"}},
		{Dataset: "C", OldRoots: []string{"A"}, NewRoots: []string{"X"}},
	}
	if got := report.RootSourceChanges; !reflect.DeepEqual(got, wantChanges) {
		t.Fatalf("RootSourceChanges = %v, want %v", got, wantChanges)
	}
	assertTraceAgreesWithCompare(t, oldSnap, newSnap, report)
}

// TestTraceCompareAgreeCommonDatasetClearedToRoot: a common dataset is
// emptied of every upstream and becomes a root. The new-snapshot trace returns
// only that dataset with a single-element path, and compare's new root set
// contains only it; the old root set is still the traced upstream roots.
func TestTraceCompareAgreeCommonDatasetClearedToRoot(t *testing.T) {
	oldSnap := snapshotOf(t, `{"datasets":[
		{"name":"A","upstreams":[]},
		{"name":"B","upstreams":["A"]},
		{"name":"C","upstreams":["B"]}
	]}`)
	newSnap := snapshotOf(t, `{"datasets":[
		{"name":"A","upstreams":[]},
		{"name":"B","upstreams":["A"]},
		{"name":"C","upstreams":[]}
	]}`)
	report := CompareSnapshots(oldSnap, newSnap)

	// C's direct upstream set changed (A-chain relation removed), and it
	// becomes its own root; A and B are unaffected.
	wantChanges := []RootSourceChange{
		{Dataset: "C", OldRoots: []string{"A"}, NewRoots: []string{"C"}},
	}
	if got := report.RootSourceChanges; !reflect.DeepEqual(got, wantChanges) {
		t.Fatalf("RootSourceChanges = %v, want %v", got, wantChanges)
	}
	newTrace, err := TraceSources(newSnap, "C")
	if err != nil {
		t.Fatalf("TraceSources(C): %v", err)
	}
	if got := newTrace.Sources; !reflect.DeepEqual(got, []SourceTrace{{Root: "C", Path: []string{"C"}}}) {
		t.Errorf("new trace(C) = %+v, want only itself with a single-element path", got)
	}
	assertTraceAgreesWithCompare(t, oldSnap, newSnap, report)
}

// TestTraceCompareAgreeOneSidedDatasetsAndFailedTraces covers datasets present
// in only one version: the new-only dataset is listed under newDatasets, the
// old-only one under removedDatasets, neither enters rootSourceChanges, and in
// the version that lacks it the trace fails explicitly with no success
// report. The shared dataset's genuine source change is still reported.
func TestTraceCompareAgreeOneSidedDatasetsAndFailedTraces(t *testing.T) {
	oldSnap := snapshotOf(t, `{"datasets":[
		{"name":"A","upstreams":[]},
		{"name":"gone","upstreams":["A"]},
		{"name":"keep","upstreams":["A"]}
	]}`)
	newSnap := snapshotOf(t, `{"datasets":[
		{"name":"A","upstreams":[]},
		{"name":"fresh","upstreams":["A"]},
		{"name":"keep","upstreams":[]}
	]}`)
	report := CompareSnapshots(oldSnap, newSnap)

	if got, want := report.NewDatasets, []string{"fresh"}; !reflect.DeepEqual(got, want) {
		t.Errorf("NewDatasets = %v, want %v", got, want)
	}
	if got, want := report.RemovedDatasets, []string{"gone"}; !reflect.DeepEqual(got, want) {
		t.Errorf("RemovedDatasets = %v, want %v", got, want)
	}
	wantChanges := []RootSourceChange{
		{Dataset: "keep", OldRoots: []string{"A"}, NewRoots: []string{"keep"}},
	}
	if got := report.RootSourceChanges; !reflect.DeepEqual(got, wantChanges) {
		t.Fatalf("RootSourceChanges = %v, want %v (one-sided datasets excluded)", got, wantChanges)
	}

	// The one-sided datasets trace fine in the version that has them and fail
	// in the version that does not.
	if got := tracedRoots(t, newSnap, "fresh"); !reflect.DeepEqual(got, []string{"A"}) {
		t.Errorf("trace roots(fresh) in new snapshot = %v, want [A]", got)
	}
	if got := tracedRoots(t, oldSnap, "gone"); !reflect.DeepEqual(got, []string{"A"}) {
		t.Errorf("trace roots(gone) in old snapshot = %v, want [A]", got)
	}
	assertTraceMissing(t, oldSnap, "fresh")
	assertTraceMissing(t, newSnap, "gone")
	assertTraceAgreesWithCompare(t, oldSnap, newSnap, report)
}

// TestTraceCompareAgreeNeverAdmitDisconnectedOrIntermediateRoots builds a graph
// with a disconnected root and multi-root convergence, then moves a branch so
// that an intermediate temporarily looks terminal-ish. Both reports must keep
// exactly the reachable roots: never an intermediate with upstreams, never a
// disconnected component's root.
func TestTraceCompareAgreeNeverAdmitDisconnectedOrIntermediateRoots(t *testing.T) {
	oldSnap := snapshotOf(t, `{"datasets":[
		{"name":"R1","upstreams":[]},
		{"name":"R2","upstreams":[]},
		{"name":"iso","upstreams":[]},
		{"name":"isoChild","upstreams":["iso"]},
		{"name":"m1","upstreams":["R1","R2"]},
		{"name":"m2","upstreams":["R1"]},
		{"name":"M","upstreams":["m1","m2"]},
		{"name":"D","upstreams":["M"]}
	]}`)
	// m2 reroutes to R2: every converged dataset still reaches {R1,R2} (R1 via
	// m1), only m2's source set changes. The iso component stays disconnected.
	newSnap := snapshotOf(t, `{"datasets":[
		{"name":"R1","upstreams":[]},
		{"name":"R2","upstreams":[]},
		{"name":"iso","upstreams":[]},
		{"name":"isoChild","upstreams":["iso"]},
		{"name":"m1","upstreams":["R1","R2"]},
		{"name":"m2","upstreams":["R2"]},
		{"name":"M","upstreams":["m1","m2"]},
		{"name":"D","upstreams":["M"]}
	]}`)
	report := CompareSnapshots(oldSnap, newSnap)

	wantChanges := []RootSourceChange{
		{Dataset: "m2", OldRoots: []string{"R1"}, NewRoots: []string{"R2"}},
	}
	if got := report.RootSourceChanges; !reflect.DeepEqual(got, wantChanges) {
		t.Fatalf("RootSourceChanges = %v, want %v", got, wantChanges)
	}
	for _, snap := range []*SnapshotFile{oldSnap, newSnap} {
		// D/M must never list the disconnected iso/isoChild roots, and m1/m2/M
		// (which keep upstreams) must never be reported as roots.
		for _, name := range []string{"D", "M"} {
			got := tracedRoots(t, snap, name)
			for _, r := range got {
				switch r {
				case "iso", "isoChild", "m1", "m2", "M", "D":
					t.Errorf("trace roots(%s) in a snapshot admits non-root %q: %v", name, r, got)
				}
			}
		}
		if got := tracedRoots(t, snap, "D"); !reflect.DeepEqual(got, []string{"R1", "R2"}) {
			t.Errorf("trace roots(D) = %v, want [R1 R2], disconnected roots excluded", got)
		}
	}
	assertNameAbsentFromReport(t, report, "iso")
	assertNameAbsentFromReport(t, report, "isoChild")
	assertTraceAgreesWithCompare(t, oldSnap, newSnap, report)
}

// TestTraceCompareAgreeCaseAndSpacingPreserved: names are case-sensitive and
// keep surrounding spaces. " A ", "A", and "a" are distinct roots; moving M
// off the spaced/lowercase roots onto "A" changes its source set, and both
// reports must carry the exact spellings in byte order.
func TestTraceCompareAgreeCaseAndSpacingPreserved(t *testing.T) {
	oldSnap := snapshotOf(t, `{"datasets":[
		{"name":" A ","upstreams":[]},
		{"name":"a","upstreams":[]},
		{"name":"A","upstreams":[]},
		{"name":"M","upstreams":[" A ","a"]},
		{"name":"n","upstreams":["M"]}
	]}`)
	newSnap := snapshotOf(t, `{"datasets":[
		{"name":" A ","upstreams":[]},
		{"name":"a","upstreams":[]},
		{"name":"A","upstreams":[]},
		{"name":"M","upstreams":["A"]},
		{"name":"n","upstreams":["M"]}
	]}`)
	report := CompareSnapshots(oldSnap, newSnap)

	// Byte order: " A " (leading space) before "a".
	wantChanges := []RootSourceChange{
		{Dataset: "M", OldRoots: []string{" A ", "a"}, NewRoots: []string{"A"}},
		{Dataset: "n", OldRoots: []string{" A ", "a"}, NewRoots: []string{"A"}},
	}
	if got := report.RootSourceChanges; !reflect.DeepEqual(got, wantChanges) {
		t.Fatalf("RootSourceChanges = %v, want %v", got, wantChanges)
	}
	if got := tracedRoots(t, oldSnap, "n"); !reflect.DeepEqual(got, []string{" A ", "a"}) {
		t.Errorf("old trace roots(n) = %v, want verbatim spaced/lowercase roots in byte order", got)
	}
	if got := tracedRoots(t, newSnap, "M"); !reflect.DeepEqual(got, []string{"A"}) {
		t.Errorf("new trace roots(M) = %v, want [A]", got)
	}
	assertTraceAgreesWithCompare(t, oldSnap, newSnap, report)
}

// TestTraceCompareAgreeEmptySnapshots: the empty graph is comparable and
// traceable in the sense that it has no datasets; the consistency check over
// an empty pair finds no common datasets and no source changes.
func TestTraceCompareAgreeEmptySnapshots(t *testing.T) {
	oldSnap := snapshotOf(t, `{"datasets":[]}`)
	newSnap := snapshotOf(t, `{"datasets":[{"name":"A","upstreams":[]}]}`)
	report := CompareSnapshots(oldSnap, newSnap)

	if got, want := report.NewDatasets, []string{"A"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("NewDatasets = %v, want %v", got, want)
	}
	if len(report.RootSourceChanges) != 0 {
		t.Fatalf("empty-to-populated comparison must have no source changes, got %v", report.RootSourceChanges)
	}
	assertTraceMissing(t, oldSnap, "A")
	if got := tracedRoots(t, newSnap, "A"); !reflect.DeepEqual(got, []string{"A"}) {
		t.Errorf("trace roots(A) in populated snapshot = %v, want [A]", got)
	}
	assertTraceAgreesWithCompare(t, oldSnap, newSnap, report)

	// An empty snapshot compared with itself is fully empty and traces nothing.
	empty := snapshotOf(t, `{"datasets":[]}`)
	emptyReport := CompareSnapshots(empty, empty)
	assertEmptyReport(t, emptyReport)
	assertTraceAgreesWithCompare(t, empty, empty, emptyReport)
}

// TestTraceCompareAgreeEquivalentEncodingsGiveSameAnswer is the determinism
// guarantee for the cross-report relationship: feeding the last-path-loss
// scenario with shuffled record order, shuffled and repeated upstreams must
// yield the same compare report and the same traced root sets for every
// common dataset as the canonical encoding.
func TestTraceCompareAgreeEquivalentEncodingsGiveSameAnswer(t *testing.T) {
	oldCanonical := snapshotOf(t, consistencyOldGraph)
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
	wantReport := CompareSnapshots(oldCanonical, newCanonical)

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
		{"name":"soloChild","upstreams":["soloRoot","soloRoot"]},
		{"name":"soloRoot","upstreams":[]},
		{"name":"derived","upstreams":["summary"]},
		{"upstreams":["branchA2","branchB1","branchA1","branchA2"],"name":"summary"},
		{"upstreams":["rootB"],"name":"branchB1"},
		{"upstreams":["rootB","rootB"],"name":"branchA2"},
		{"upstreams":["rootB"],"name":"branchA1"},
		{"name":"rootB","upstreams":[]},
		{"name":"rootA","upstreams":[]}
	]}`)
	gotReport := CompareSnapshots(oldShuffled, newShuffled)
	if !reflect.DeepEqual(gotReport, wantReport) {
		t.Fatalf("equivalent encodings produced different compare reports:\n got %+v\nwant %+v", gotReport, wantReport)
	}

	// Every common dataset's traced root sets must match between encodings and
	// agree with the (identical) compare report.
	common := commonDatasets(adjacencyFromValidFile(oldCanonical.Graph), adjacencyFromValidFile(newCanonical.Graph))
	for _, name := range common {
		if got, want := tracedRoots(t, oldShuffled, name), tracedRoots(t, oldCanonical, name); !reflect.DeepEqual(got, want) {
			t.Errorf("old trace roots(%s) = %v from shuffled encoding, want %v", name, got, want)
		}
		if got, want := tracedRoots(t, newShuffled, name), tracedRoots(t, newCanonical, name); !reflect.DeepEqual(got, want) {
			t.Errorf("new trace roots(%s) = %v from shuffled encoding, want %v", name, got, want)
		}
	}
	assertTraceAgreesWithCompare(t, oldShuffled, newShuffled, gotReport)
}

// mustTrace traces dataset within snap, failing the test on error.
func mustTrace(t *testing.T, snap *SnapshotFile, dataset string) *TraceReport {
	t.Helper()
	report, err := TraceSources(snap, dataset)
	if err != nil {
		t.Fatalf("TraceSources(%q): %v", dataset, err)
	}
	return report
}
