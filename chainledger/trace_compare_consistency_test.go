package chainledger

import (
	"encoding/json"
	"errors"
	"reflect"
	"sort"
	"strings"
	"testing"
)

// This file is the cross-report regression net for the two read-only reports a
// user gets for ONE pair of snapshots:
//
//   - TraceSources answers "which root sources does this dataset reach in this
//     frozen version, and by which representative path", and
//   - CompareSnapshots answers "for every dataset common to both versions, did
//     the reachable root set change between old and new".
//
// The two reports must explain the roots identically. Concretely, for each
// dataset present in both versions:
//
//	oldRoots == roots reported by TraceSources in the old snapshot
//	newRoots == roots reported by TraceSources in the new snapshot
//
// and a dataset lands in rootSourceChanges if and only if those two sets
// differ. A change of the representative PATH alone (a branch moving onto
// another route to the same root, or a new shortcut to an already reachable
// root) must never be reported as a source change; when the last route to an
// old root disappears, every still-common dataset that loses it must be listed
// with complete old and new sets, including datasets whose DIRECT upstreams did
// not change; datasets present in only one version stay in newDatasets /
// removedDatasets and tracing them in the version that lacks them must fail
// outright instead of returning a success report.

// branchOldGraph is the multi-root convergence fixture: three roots, two
// branches to rootA (a1 directly, a0 via a short chain) where a2 also carries
// rootC, one branch to rootB (b1), a merge node where every branch converges,
// and TWO levels of datasets derived after the merge (down, down2). "solo" is
// an unrelated disconnected root that no trace from the converging component
// may ever report.
const branchOldGraph = `{"datasets":[
	{"name":"rootA","upstreams":[]},
	{"name":"rootB","upstreams":[]},
	{"name":"rootC","upstreams":[]},
	{"name":"a0","upstreams":["rootA"]},
	{"name":"a1","upstreams":["rootA"]},
	{"name":"a2","upstreams":["rootA","rootC"]},
	{"name":"b1","upstreams":["rootB"]},
	{"name":"merge","upstreams":["a1","a2","b1"]},
	{"name":"down","upstreams":["merge"]},
	{"name":"down2","upstreams":["down"]},
	{"name":"solo","upstreams":[]}
]}`

// commonNamesIn returns the names present in both snapshots, sorted by name
// byte order the same way CompareSnapshots walks them.
func commonNamesIn(oldSnap, newSnap *SnapshotFile) []string {
	oldAdj := adjacencyFromValidFile(oldSnap.Graph)
	newAdj := adjacencyFromValidFile(newSnap.Graph)
	common := make([]string, 0)
	for name := range oldAdj {
		if _, ok := newAdj[name]; ok {
			common = append(common, name)
		}
	}
	sort.Strings(common)
	return common
}

// traceRoots runs a source trace and returns just the sorted root names,
// failing the test if the trace does not succeed.
func traceRoots(t *testing.T, snap *SnapshotFile, dataset string) []string {
	t.Helper()
	report, err := TraceSources(snap, dataset)
	if err != nil {
		t.Fatalf("TraceSources(%q): %v", dataset, err)
	}
	roots := make([]string, 0, len(report.Sources))
	for _, src := range report.Sources {
		roots = append(roots, src.Root)
	}
	return roots
}

// rootChangeMap indexes a compare report's root-source changes by dataset.
func rootChangeMap(report *CompareReport) map[string]RootSourceChange {
	byName := make(map[string]RootSourceChange, len(report.RootSourceChanges))
	for _, change := range report.RootSourceChanges {
		byName[change.Dataset] = change
	}
	return byName
}

// assertTracePathsAreReal walks every reported path through adj and pins the
// path rules the comparison relies on: the path starts at the queried
// dataset, every consecutive pair is a real direct-upstream edge, only the
// final node is a root (intermediate nodes still have upstreams), roots are
// reported once, and entries are ordered by root name byte order.
func assertTracePathsAreReal(t *testing.T, report *TraceReport, adj adjacency) {
	t.Helper()
	seen := make(map[string]bool)
	var prevRoot string
	for i, src := range report.Sources {
		if seen[src.Root] {
			t.Errorf("root %q reported more than once for query %q", src.Root, report.Dataset)
		}
		seen[src.Root] = true
		if i > 0 && src.Root <= prevRoot {
			t.Errorf("sources for %q not in strict root byte order at %d: %q after %q", report.Dataset, i, src.Root, prevRoot)
		}
		prevRoot = src.Root
		if len(src.Path) == 0 {
			t.Errorf("root %q for query %q has an empty path", src.Root, report.Dataset)
			continue
		}
		if src.Path[0] != report.Dataset {
			t.Errorf("path to %q starts at %q, want queried dataset %q", src.Root, src.Path[0], report.Dataset)
		}
		if src.Path[len(src.Path)-1] != src.Root {
			t.Errorf("path %v ends at %q, want its root %q", src.Path, src.Path[len(src.Path)-1], src.Root)
		}
		for j := 1; j < len(src.Path); j++ {
			node, parent := src.Path[j-1], src.Path[j]
			if !containsString(adj[node], parent) {
				t.Errorf("path %v uses an edge that does not exist: %q -> %q", src.Path, node, parent)
			}
			if j < len(src.Path)-1 && len(adj[parent]) == 0 {
				t.Errorf("path %v passes through root %q before its end", src.Path, parent)
			}
		}
		if len(adj[src.Root]) != 0 {
			t.Errorf("reported root %q still has upstreams %v", src.Root, adj[src.Root])
		}
	}
}

// assertTraceAgreesWithCompare is the headline invariant of this file: for
// every common dataset the roots TraceSources reports in each version equal
// the compare report's oldRoots/newRoots, the dataset appears in
// rootSourceChanges exactly when the two traced sets differ, and every traced
// path is a real edge-walk that ends at a genuine root. Datasets present on
// only one side are never looked up here (the caller pins their trace
// failure separately).
func assertTraceAgreesWithCompare(t *testing.T, oldSnap, newSnap *SnapshotFile, report *CompareReport) {
	t.Helper()
	oldAdj := adjacencyFromValidFile(oldSnap.Graph)
	newAdj := adjacencyFromValidFile(newSnap.Graph)
	changes := rootChangeMap(report)
	for _, name := range commonNamesIn(oldSnap, newSnap) {
		oldReport, err := TraceSources(oldSnap, name)
		if err != nil {
			t.Fatalf("old TraceSources(%q): %v", name, err)
		}
		newReport, err := TraceSources(newSnap, name)
		if err != nil {
			t.Fatalf("new TraceSources(%q): %v", name, err)
		}
		assertTracePathsAreReal(t, oldReport, oldAdj)
		assertTracePathsAreReal(t, newReport, newAdj)
		oldTraced := traceRoots(t, oldSnap, name)
		newTraced := traceRoots(t, newSnap, name)

		change, listed := changes[name]
		setsDiffer := !reflect.DeepEqual(oldTraced, newTraced)
		if listed != setsDiffer {
			t.Errorf("dataset %q: trace root sets differ=%v but rootSourceChanges lists it=%v (old=%v new=%v)",
				name, setsDiffer, listed, oldTraced, newTraced)
		}
		if listed {
			if !reflect.DeepEqual(change.OldRoots, oldTraced) {
				t.Errorf("dataset %q oldRoots = %v, want old trace roots %v", name, change.OldRoots, oldTraced)
			}
			if !reflect.DeepEqual(change.NewRoots, newTraced) {
				t.Errorf("dataset %q newRoots = %v, want new trace roots %v", name, change.NewRoots, newTraced)
			}
		}
	}
	// The converse: every name the report lists really is common to both.
	for _, change := range report.RootSourceChanges {
		if _, ok := oldAdj[change.Dataset]; !ok {
			t.Errorf("rootSourceChanges lists %q, absent from the old snapshot", change.Dataset)
		}
		if _, ok := newAdj[change.Dataset]; !ok {
			t.Errorf("rootSourceChanges lists %q, absent from the new snapshot", change.Dataset)
		}
	}
}

// TestTraceAndComparePathOnlyRerouteAndShortcutReportsNoSourceChange is the
// "same roots, different representative path" case end to end. In the new
// version a1 stops depending directly on rootA and hangs off a2 (another
// route to the same root), merge gains a shortcut straight to rootA, and down
// gains a shortcut straight to rootB. The shortest representative paths of the
// trace change for a1, merge, and down, but every common dataset still reaches
// exactly the same root set: rootSourceChanges must be empty even though three
// direct-upstream relationships genuinely changed.
func TestTraceAndComparePathOnlyRerouteAndShortcutReportsNoSourceChange(t *testing.T) {
	oldSnap := snapshotOf(t, branchOldGraph)
	newSnap := snapshotOf(t, `{"datasets":[
		{"name":"rootA","upstreams":[]},
		{"name":"rootB","upstreams":[]},
		{"name":"rootC","upstreams":[]},
		{"name":"a0","upstreams":["rootA"]},
		{"name":"a1","upstreams":["a0"]},
		{"name":"a2","upstreams":["rootA","rootC"]},
		{"name":"b1","upstreams":["rootB"]},
		{"name":"merge","upstreams":["a1","a2","b1","rootA"]},
		{"name":"down","upstreams":["merge","rootB"]},
		{"name":"down2","upstreams":["down"]},
		{"name":"solo","upstreams":[]}
	]}`)

	report := CompareSnapshots(oldSnap, newSnap)

	if got, want := report.ChangedDatasets, []string{"a1", "down", "merge"}; !reflect.DeepEqual(got, want) {
		t.Errorf("ChangedDatasets = %v, want %v", got, want)
	}
	if got, want := report.AddedRelations, []Relation{
		{Upstream: "a0", Downstream: "a1"},
		{Upstream: "rootA", Downstream: "merge"},
		{Upstream: "rootB", Downstream: "down"},
	}; !reflect.DeepEqual(got, want) {
		t.Errorf("AddedRelations = %v, want %v", got, want)
	}
	if got, want := report.RemovedRelations, []Relation{
		{Upstream: "rootA", Downstream: "a1"},
	}; !reflect.DeepEqual(got, want) {
		t.Errorf("RemovedRelations = %v, want %v", got, want)
	}
	if len(report.RootSourceChanges) != 0 {
		t.Fatalf("path-only reroutes/shortcuts to the same roots must not report source changes, got %v", report.RootSourceChanges)
	}

	// Pin the representative path changes the trace does report, proving the
	// compare decision is based on the root SET rather than the paths.
	oldMerge, err := TraceSources(oldSnap, "merge")
	if err != nil {
		t.Fatalf("old trace merge: %v", err)
	}
	newMerge, err := TraceSources(newSnap, "merge")
	if err != nil {
		t.Fatalf("new trace merge: %v", err)
	}
	if got, want := oldMerge.Sources, []SourceTrace{
		{Root: "rootA", Path: []string{"merge", "a1", "rootA"}},
		{Root: "rootB", Path: []string{"merge", "b1", "rootB"}},
		{Root: "rootC", Path: []string{"merge", "a2", "rootC"}},
	}; !reflect.DeepEqual(got, want) {
		t.Errorf("old merge sources = %+v, want %+v", got, want)
	}
	if got, want := newMerge.Sources, []SourceTrace{
		{Root: "rootA", Path: []string{"merge", "rootA"}},
		{Root: "rootB", Path: []string{"merge", "b1", "rootB"}},
		{Root: "rootC", Path: []string{"merge", "a2", "rootC"}},
	}; !reflect.DeepEqual(got, want) {
		t.Errorf("new merge sources = %+v, want %+v", got, want)
	}
	// down's path to rootB shortens through its new shortcut; rootA keeps
	// arriving through merge's own shortcut. Root sets are unchanged either
	// way, and down2 inherits the new routes with no direct edit of its own.
	newDown := traceReportSources(t, newSnap, "down")
	if got, want := newDown, []SourceTrace{
		{Root: "rootA", Path: []string{"down", "merge", "rootA"}},
		{Root: "rootB", Path: []string{"down", "rootB"}},
		{Root: "rootC", Path: []string{"down", "merge", "a2", "rootC"}},
	}; !reflect.DeepEqual(got, want) {
		t.Errorf("new down sources = %+v, want %+v", got, want)
	}
	newDown2 := traceReportSources(t, newSnap, "down2")
	if got, want := newDown2, []SourceTrace{
		{Root: "rootA", Path: []string{"down2", "down", "merge", "rootA"}},
		{Root: "rootB", Path: []string{"down2", "down", "rootB"}},
		{Root: "rootC", Path: []string{"down2", "down", "merge", "a2", "rootC"}},
	}; !reflect.DeepEqual(got, want) {
		t.Errorf("new down2 sources = %+v, want %+v", got, want)
	}
	for _, name := range []string{"merge", "down", "down2"} {
		if got := traceRoots(t, oldSnap, name); !reflect.DeepEqual(got, []string{"rootA", "rootB", "rootC"}) {
			t.Errorf("old roots of %s = %v, want all three roots", name, got)
		}
		if got := traceRoots(t, newSnap, name); !reflect.DeepEqual(got, []string{"rootA", "rootB", "rootC"}) {
			t.Errorf("new roots of %s = %v, want all three roots", name, got)
		}
	}

	assertTraceAgreesWithCompare(t, oldSnap, newSnap, report)
}

// traceReportSources traces dataset and returns its source records, failing on
// error.
func traceReportSources(t *testing.T, snap *SnapshotFile, dataset string) []SourceTrace {
	t.Helper()
	report, err := TraceSources(snap, dataset)
	if err != nil {
		t.Fatalf("TraceSources(%q): %v", dataset, err)
	}
	return report.Sources
}

// TestTraceAndCompareLastRouteToOldRootLostEverywhere is the root-loss case
// across a converged lineage: a1 is repointed from rootA to rootB and a2
// drops rootA (keeping rootC), so the merge and BOTH post-merge derivations
// lose rootA while rootB and rootC stay reachable through other branches.
// merge/down/down2 have unchanged DIRECT upstreams (down2 is two levels away
// from the edit), yet all three must be listed with complete old and new root
// sets. The traces must report exactly the actually reachable roots, never an
// intermediate with upstreams as a root, and never the disconnected "solo".
func TestTraceAndCompareLastRouteToOldRootLostEverywhere(t *testing.T) {
	oldSnap := snapshotOf(t, branchOldGraph)
	newSnap := snapshotOf(t, `{"datasets":[
		{"name":"rootA","upstreams":[]},
		{"name":"rootB","upstreams":[]},
		{"name":"rootC","upstreams":[]},
		{"name":"a0","upstreams":["rootA"]},
		{"name":"a1","upstreams":["rootB"]},
		{"name":"a2","upstreams":["rootC"]},
		{"name":"b1","upstreams":["rootB"]},
		{"name":"merge","upstreams":["a1","a2","b1"]},
		{"name":"down","upstreams":["merge"]},
		{"name":"down2","upstreams":["down"]},
		{"name":"solo","upstreams":[]}
	]}`)

	report := CompareSnapshots(oldSnap, newSnap)

	if len(report.NewDatasets) != 0 || len(report.RemovedDatasets) != 0 {
		t.Errorf("node lists must be empty, got new=%v removed=%v", report.NewDatasets, report.RemovedDatasets)
	}
	if got, want := report.ChangedDatasets, []string{"a1", "a2"}; !reflect.DeepEqual(got, want) {
		t.Errorf("ChangedDatasets = %v, want only the repointed branches %v", got, want)
	}
	if got, want := report.AddedRelations, []Relation{
		{Upstream: "rootB", Downstream: "a1"},
	}; !reflect.DeepEqual(got, want) {
		t.Errorf("AddedRelations = %v, want %v", got, want)
	}
	if got, want := report.RemovedRelations, []Relation{
		{Upstream: "rootA", Downstream: "a1"},
		{Upstream: "rootA", Downstream: "a2"},
	}; !reflect.DeepEqual(got, want) {
		t.Errorf("RemovedRelations = %v, want %v", got, want)
	}

	wantChanges := []RootSourceChange{
		{Dataset: "a1", OldRoots: []string{"rootA"}, NewRoots: []string{"rootB"}},
		{Dataset: "a2", OldRoots: []string{"rootA", "rootC"}, NewRoots: []string{"rootC"}},
		{Dataset: "down", OldRoots: []string{"rootA", "rootB", "rootC"}, NewRoots: []string{"rootB", "rootC"}},
		{Dataset: "down2", OldRoots: []string{"rootA", "rootB", "rootC"}, NewRoots: []string{"rootB", "rootC"}},
		{Dataset: "merge", OldRoots: []string{"rootA", "rootB", "rootC"}, NewRoots: []string{"rootB", "rootC"}},
	}
	if got := report.RootSourceChanges; !reflect.DeepEqual(got, wantChanges) {
		t.Fatalf("RootSourceChanges = %v\nwant %v", got, wantChanges)
	}

	// The lost root must be absent from every new-version trace, present in
	// every old one; intermediates must never stand in as roots, and the
	// disconnected solo root must never join a result.
	for _, name := range []string{"merge", "down", "down2"} {
		if got := traceRoots(t, oldSnap, name); !reflect.DeepEqual(got, []string{"rootA", "rootB", "rootC"}) {
			t.Errorf("old roots of %s = %v, want [rootA rootB rootC]", name, got)
		}
		if got := traceRoots(t, newSnap, name); !reflect.DeepEqual(got, []string{"rootB", "rootC"}) {
			t.Errorf("new roots of %s = %v, want only the still reachable [rootB rootC]", name, got)
		}
		for _, src := range traceReportSources(t, newSnap, name) {
			if src.Root == "solo" {
				t.Errorf("trace of %s must not include disconnected root solo, got %v", name, src)
			}
			if src.Root == "merge" || src.Root == "down" || src.Root == "down2" {
				t.Errorf("intermediate %q must not be reported as a root", src.Root)
			}
		}
	}
	// rootA itself still exists as a root in both versions and reaches only
	// itself, so it is not a source change despite being lost downstream.
	assertDatasetNotAChange(t, report, "rootA")
	assertNameAbsentFromReport(t, report, "solo")

	assertTraceAgreesWithCompare(t, oldSnap, newSnap, report)
}

// TestTraceAndCompareCommonDatasetClearedToBecomeRoot covers a common dataset
// whose ENTIRE upstream set is cleared: in the new snapshot its trace returns
// only itself with a single-element path, the comparison's new root set is
// just its own name, and the dataset downstream of it (direct upstream
// unchanged) moves to the new root as well. The still-existing old root p is
// not removed from either version.
func TestTraceAndCompareCommonDatasetClearedToBecomeRoot(t *testing.T) {
	oldSnap := snapshotOf(t, `{"datasets":[
		{"name":"p","upstreams":[]},
		{"name":"m","upstreams":["p"]},
		{"name":"n","upstreams":["m"]},
		{"name":"x","upstreams":[]}
	]}`)
	newSnap := snapshotOf(t, `{"datasets":[
		{"name":"p","upstreams":[]},
		{"name":"m","upstreams":[]},
		{"name":"n","upstreams":["m"]},
		{"name":"x","upstreams":[]}
	]}`)

	report := CompareSnapshots(oldSnap, newSnap)
	if got, want := report.ChangedDatasets, []string{"m"}; !reflect.DeepEqual(got, want) {
		t.Errorf("ChangedDatasets = %v, want only %v", got, want)
	}
	if len(report.NewDatasets) != 0 || len(report.RemovedDatasets) != 0 {
		t.Errorf("no node is added or removed, got new=%v removed=%v", report.NewDatasets, report.RemovedDatasets)
	}
	wantChanges := []RootSourceChange{
		{Dataset: "m", OldRoots: []string{"p"}, NewRoots: []string{"m"}},
		{Dataset: "n", OldRoots: []string{"p"}, NewRoots: []string{"m"}},
	}
	if got := report.RootSourceChanges; !reflect.DeepEqual(got, wantChanges) {
		t.Fatalf("RootSourceChanges = %v, want %v", got, wantChanges)
	}

	// The new root traces only itself as a single-element path.
	if got, want := traceReportSources(t, newSnap, "m"), []SourceTrace{{Root: "m", Path: []string{"m"}}}; !reflect.DeepEqual(got, want) {
		t.Errorf("new trace m = %+v, want single-element self path %+v", got, want)
	}
	// n still has an upstream, so m — not n — is its only root; the
	// disconnected x never joins.
	if got, want := traceReportSources(t, newSnap, "n"), []SourceTrace{{Root: "m", Path: []string{"n", "m"}}}; !reflect.DeepEqual(got, want) {
		t.Errorf("new trace n = %+v, want %+v", got, want)
	}
	// Old traces are unchanged in meaning.
	if got, want := traceReportSources(t, oldSnap, "n"), []SourceTrace{{Root: "p", Path: []string{"n", "m", "p"}}}; !reflect.DeepEqual(got, want) {
		t.Errorf("old trace n = %+v, want %+v", got, want)
	}

	assertTraceAgreesWithCompare(t, oldSnap, newSnap, report)
}

// versionOnlyOldGraph / versionOnlyNewGraph share p and keep but each carry
// datasets the other side lacks, including a short derivation chain that
// disappears or appears as a whole.
const versionOnlyOldGraph = `{"datasets":[
	{"name":"p","upstreams":[]},
	{"name":"keep","upstreams":[]},
	{"name":"gone","upstreams":["p"]},
	{"name":"g","upstreams":["gone"]}
]}`

const versionOnlyNewGraph = `{"datasets":[
	{"name":"p","upstreams":[]},
	{"name":"keep","upstreams":[]},
	{"name":"added","upstreams":["p"]},
	{"name":"h","upstreams":["added"]}
]}`

// TestTraceAndCompareVersionOnlyDatasetsStayOutOfSourceChanges pins the
// boundary: added/removed datasets are classified exactly once, never enter
// rootSourceChanges, and tracing them in the version that lacks them fails
// with no success report instead of returning fabricated roots.
func TestTraceAndCompareVersionOnlyDatasetsStayOutOfSourceChanges(t *testing.T) {
	oldSnap := snapshotOf(t, versionOnlyOldGraph)
	newSnap := snapshotOf(t, versionOnlyNewGraph)
	report := CompareSnapshots(oldSnap, newSnap)

	if got, want := report.NewDatasets, []string{"added", "h"}; !reflect.DeepEqual(got, want) {
		t.Errorf("NewDatasets = %v, want %v", got, want)
	}
	if got, want := report.RemovedDatasets, []string{"g", "gone"}; !reflect.DeepEqual(got, want) {
		t.Errorf("RemovedDatasets = %v, want %v", got, want)
	}
	if len(report.ChangedDatasets) != 0 {
		t.Errorf("no common dataset changed direct upstreams, got %v", report.ChangedDatasets)
	}
	if len(report.RootSourceChanges) != 0 {
		t.Fatalf("version-only datasets must not enter rootSourceChanges, got %v", report.RootSourceChanges)
	}

	// Names present in a version trace successfully there.
	if got := traceRoots(t, oldSnap, "gone"); !reflect.DeepEqual(got, []string{"p"}) {
		t.Errorf("old trace gone roots = %v, want [p]", got)
	}
	if got := traceRoots(t, oldSnap, "g"); !reflect.DeepEqual(got, []string{"p"}) {
		t.Errorf("old trace g roots = %v, want [p]", got)
	}
	if got := traceRoots(t, newSnap, "added"); !reflect.DeepEqual(got, []string{"p"}) {
		t.Errorf("new trace added roots = %v, want [p]", got)
	}
	if got := traceRoots(t, newSnap, "h"); !reflect.DeepEqual(got, []string{"p"}) {
		t.Errorf("new trace h roots = %v, want [p]", got)
	}

	// The same names fail explicitly in the version missing them: no report,
	// ErrNotFound, and the missing name is stated.
	for _, name := range []string{"added", "h"} {
		r, err := TraceSources(oldSnap, name)
		if r != nil || err == nil {
			t.Errorf("old TraceSources(%q) = %+v, %v; want nil report and an error", name, r, err)
		} else if !errors.Is(err, ErrNotFound) || !strings.Contains(err.Error(), name) {
			t.Errorf("old TraceSources(%q) err = %v, want ErrNotFound naming %q", name, err, name)
		}
	}
	for _, name := range []string{"gone", "g"} {
		if r, err := TraceSources(newSnap, name); r != nil || !errors.Is(err, ErrNotFound) {
			t.Errorf("new TraceSources(%q) = %+v, err=%v; want nil report and ErrNotFound", name, r, err)
		}
	}

	assertTraceAgreesWithCompare(t, oldSnap, newSnap, report)
}

// TestTraceAndCompareByteOrderCaseAndSpaceRoots pins the ordering the two
// reports share when roots differ only by case or surrounding spaces: names
// are kept verbatim and ordered by UTF-8 byte order (space 0x20, then "A"
// 0x41, then "a" 0x61), both in the trace's sources and in the comparison's
// oldRoots/newRoots. n's direct upstream ([M]) never changes; its root set
// moves purely because M was repointed.
func TestTraceAndCompareByteOrderCaseAndSpaceRoots(t *testing.T) {
	oldSnap := snapshotOf(t, `{"datasets":[
		{"name":" A ","upstreams":[]},
		{"name":"A","upstreams":[]},
		{"name":"a","upstreams":[]},
		{"name":"M","upstreams":[" A ","a"]},
		{"name":"n","upstreams":["M"]}
	]}`)
	newSnap := snapshotOf(t, `{"datasets":[
		{"name":" A ","upstreams":[]},
		{"name":"A","upstreams":[]},
		{"name":"a","upstreams":[]},
		{"name":"M","upstreams":["A","a"]},
		{"name":"n","upstreams":["M"]}
	]}`)

	report := CompareSnapshots(oldSnap, newSnap)
	wantChanges := []RootSourceChange{
		{Dataset: "M", OldRoots: []string{" A ", "a"}, NewRoots: []string{"A", "a"}},
		{Dataset: "n", OldRoots: []string{" A ", "a"}, NewRoots: []string{"A", "a"}},
	}
	if got := report.RootSourceChanges; !reflect.DeepEqual(got, wantChanges) {
		t.Fatalf("RootSourceChanges = %v, want %v", got, wantChanges)
	}

	oldM := traceReportSources(t, oldSnap, "M")
	if got, want := []string{oldM[0].Root, oldM[1].Root}, []string{" A ", "a"}; !reflect.DeepEqual(got, want) {
		t.Errorf("old M root order = %v, want byte order %v", got, want)
	}
	newM := traceReportSources(t, newSnap, "M")
	if got, want := []string{newM[0].Root, newM[1].Root}, []string{"A", "a"}; !reflect.DeepEqual(got, want) {
		t.Errorf("new M root order = %v, want byte order %v", got, want)
	}
	// The spaced spelling and the single-letter casing are carried verbatim
	// into the path arrays.
	if got, want := newM[0].Path, []string{"M", "A"}; !reflect.DeepEqual(got, want) {
		t.Errorf("new M path to A = %v, want %v", got, want)
	}
	if got := traceReportSources(t, newSnap, "n"); !reflect.DeepEqual(got, []SourceTrace{
		{Root: "A", Path: []string{"n", "M", "A"}},
		{Root: "a", Path: []string{"n", "M", "a"}},
	}) {
		t.Errorf("new n sources = %+v, want verbatim case/space paths", got)
	}

	assertTraceAgreesWithCompare(t, oldSnap, newSnap, report)
}

// TestTraceAndCompareDeterministicAcrossShuffledSemantics feeds the root-loss
// scenario in shuffled form (records reordered, upstreams reordered and
// repeated) and requires both reports to be byte-identical to the canonical
// run, while both input snapshots keep their serialized bytes throughout.
func TestTraceAndCompareDeterministicAcrossShuffledSemantics(t *testing.T) {
	oldCanonical := snapshotOf(t, branchOldGraph)
	newCanonical := snapshotOf(t, `{"datasets":[
		{"name":"rootA","upstreams":[]},
		{"name":"rootB","upstreams":[]},
		{"name":"rootC","upstreams":[]},
		{"name":"a0","upstreams":["rootA"]},
		{"name":"a1","upstreams":["rootB"]},
		{"name":"a2","upstreams":["rootC"]},
		{"name":"b1","upstreams":["rootB"]},
		{"name":"merge","upstreams":["a1","a2","b1"]},
		{"name":"down","upstreams":["merge"]},
		{"name":"down2","upstreams":["down"]},
		{"name":"solo","upstreams":[]}
	]}`)
	oldShuffled := snapshotOf(t, `{"datasets":[
		{"upstreams":[],"name":"solo"},
		{"name":"down2","upstreams":["down","down"]},
		{"upstreams":["merge"],"name":"down"},
		{"name":"merge","upstreams":["b1","a2","a1","a2"]},
		{"upstreams":["rootB","rootB"],"name":"b1"},
		{"name":"a2","upstreams":["rootC","rootA"]},
		{"upstreams":["rootA"],"name":"a1"},
		{"name":"a0","upstreams":["rootA"]},
		{"name":"rootC","upstreams":[]},
		{"name":"rootB","upstreams":[]},
		{"name":"rootA","upstreams":[]}
	]}`)
	newShuffled := snapshotOf(t, `{ "datasets" : [
		{"name":"solo","upstreams":[]},
		{"upstreams":["down"],"name":"down2"},
		{"name":"down","upstreams":["merge","merge"]},
		{"upstreams":["a2","b1","a1"],"name":"merge"},
		{"name":"b1","upstreams":["rootB"]},
		{"name":"a2","upstreams":["rootC","rootC"]},
		{"name":"a1","upstreams":["rootB"]},
		{"name":"a0","upstreams":["rootA","rootA"]},
		{"name":"rootC","upstreams":[]},
		{"upstreams":[],"name":"rootB"},
		{"upstreams":[],"name":"rootA"}
	] }`)

	wantCompare, err := json.Marshal(CompareSnapshots(oldCanonical, newCanonical))
	if err != nil {
		t.Fatalf("marshal canonical compare: %v", err)
	}
	gotCompare, err := json.Marshal(CompareSnapshots(oldShuffled, newShuffled))
	if err != nil {
		t.Fatalf("marshal shuffled compare: %v", err)
	}
	if string(gotCompare) != string(wantCompare) {
		t.Fatalf("compare reports differ for semantically equal pairs:\n got %s\nwant %s", gotCompare, wantCompare)
	}

	// Every trace over the shuffled snapshots equals the canonical trace, both
	// the root sets and the representative paths.
	for _, name := range commonNamesIn(oldCanonical, newCanonical) {
		wantOld, err := TraceSources(oldCanonical, name)
		if err != nil {
			t.Fatalf("canonical old trace %q: %v", name, err)
		}
		wantNew, err := TraceSources(newCanonical, name)
		if err != nil {
			t.Fatalf("canonical new trace %q: %v", name, err)
		}
		gotOld, err := TraceSources(oldShuffled, name)
		if err != nil {
			t.Fatalf("shuffled old trace %q: %v", name, err)
		}
		gotNew, err := TraceSources(newShuffled, name)
		if err != nil {
			t.Fatalf("shuffled new trace %q: %v", name, err)
		}
		if !reflect.DeepEqual(gotOld, wantOld) {
			t.Errorf("old trace %q differs across equivalent spellings:\n got %+v\nwant %+v", name, gotOld, wantOld)
		}
		if !reflect.DeepEqual(gotNew, wantNew) {
			t.Errorf("new trace %q differs across equivalent spellings:\n got %+v\nwant %+v", name, gotNew, wantNew)
		}
	}

	// Read-only: the snapshots handed to both reports serialize identically
	// before and after.
	oldBytes, err := MarshalSnapshot(oldCanonical)
	if err != nil {
		t.Fatalf("marshal old: %v", err)
	}
	newBytes, err := MarshalSnapshot(newCanonical)
	if err != nil {
		t.Fatalf("marshal new: %v", err)
	}
	_ = CompareSnapshots(oldCanonical, newCanonical)
	for _, name := range commonNamesIn(oldCanonical, newCanonical) {
		if _, err := TraceSources(oldCanonical, name); err != nil {
			t.Fatalf("trace old %q: %v", name, err)
		}
		if _, err := TraceSources(newCanonical, name); err != nil {
			t.Fatalf("trace new %q: %v", name, err)
		}
	}
	if got, err := MarshalSnapshot(oldCanonical); err != nil || string(got) != string(oldBytes) {
		t.Errorf("old snapshot changed during reports: %v", err)
	}
	if got, err := MarshalSnapshot(newCanonical); err != nil || string(got) != string(newBytes) {
		t.Errorf("new snapshot changed during reports: %v", err)
	}
}
