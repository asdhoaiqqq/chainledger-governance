package main

import (
	"encoding/json"
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/asdhoaiqqq/chainledger-governance/chainledger"
)

// This file is the end-to-end counterpart of the core cross-report
// consistency tests. It drives the real command line (snapshot, compare,
// trace) through run(), using snapshot files on disk, and requires that a user
// inspecting one pair of snapshots with both commands always reads one
// consistent explanation of the root sources:
//
//   - for every dataset present in both snapshot files, the root set printed
//     by `trace` in the old file equals compare's oldRoots and the set printed
//     in the new file equals compare's newRoots; the dataset is listed under
//     rootSourceChanges if and only if those two sets differ;
//   - a representative-path-only change (reroute to the same root, shortcut to
//     an already reachable root) never shows up as a source change;
//   - losing the last route to an old root lists every affected common
//     dataset, including one whose direct upstream is unchanged;
//   - a common dataset cleared to a root traces only to itself (single-element
//     path); a dataset existing in only one snapshot is new/removed, never a
//     source change, and tracing it in the snapshot that lacks it fails with
//     empty stdout;
//   - compare and trace stay read-only: both snapshot files keep their bytes.

// cliTraceRoots runs `trace snapPath dataset`, requiring success, and returns
// the sorted root names the command printed together with the full sources.
func cliTrace(t *testing.T, snapPath, dataset string) []chainledger.SourceTrace {
	t.Helper()
	stdout, stderr, exit := captureStdout(t, func() int {
		return run([]string{"trace", snapPath, dataset})
	})
	if exit != 0 {
		t.Fatalf("trace %q in %s exit = %d, stderr = %s", dataset, snapPath, exit, stderr)
	}
	var report chainledger.TraceReport
	if err := json.Unmarshal([]byte(stdout), &report); err != nil {
		t.Fatalf("trace %q stdout is not valid JSON: %v\n%s", dataset, err, stdout)
	}
	return report.Sources
}

// cliRootNames extracts and validates the sorted, once-only root names from a
// trace result.
func cliRootNames(t *testing.T, dataset string, sources []chainledger.SourceTrace) []string {
	t.Helper()
	roots := make([]string, 0, len(sources))
	seen := map[string]bool{}
	for _, src := range sources {
		if seen[src.Root] {
			t.Fatalf("trace %q lists root %q more than once: %+v", dataset, src.Root, sources)
		}
		seen[src.Root] = true
		roots = append(roots, src.Root)
	}
	if !sort.StringsAreSorted(roots) {
		t.Fatalf("trace %q roots not in byte order: %v", dataset, roots)
	}
	return roots
}

// cliCommonDatasets returns the dataset names present in both parsed snapshot
// files, sorted.
func cliCommonDatasets(t *testing.T, oldPath, newPath string) []string {
	t.Helper()
	oldSnap := cliParseSnapshot(t, oldPath)
	newSnap := cliParseSnapshot(t, newPath)
	oldNames := map[string]bool{}
	for _, ds := range oldSnap.Graph.Datasets {
		oldNames[ds.Name] = true
	}
	common := make([]string, 0)
	for _, ds := range newSnap.Graph.Datasets {
		if oldNames[ds.Name] {
			common = append(common, ds.Name)
		}
	}
	sort.Strings(common)
	return common
}

func cliParseSnapshot(t *testing.T, path string) *chainledger.SnapshotFile {
	t.Helper()
	snap, err := chainledger.ParseSnapshot([]byte(readFile(t, path)))
	if err != nil {
		t.Fatalf("parse snapshot %s: %v", path, err)
	}
	return snap
}

// assertCLITraceCompareAgree drives the full cross-report invariant from the
// command line over the two snapshot files.
func assertCLITraceCompareAgree(t *testing.T, oldPath, newPath string, report *chainledger.CompareReport) {
	t.Helper()
	entries := map[string]chainledger.RootSourceChange{}
	for _, c := range report.RootSourceChanges {
		if _, dup := entries[c.Dataset]; dup {
			t.Fatalf("dataset %q listed twice in rootSourceChanges: %+v", c.Dataset, report.RootSourceChanges)
		}
		entries[c.Dataset] = c
	}
	for _, name := range cliCommonDatasets(t, oldPath, newPath) {
		wantOld := cliRootNames(t, name, cliTrace(t, oldPath, name))
		wantNew := cliRootNames(t, name, cliTrace(t, newPath, name))
		differs := !reflect.DeepEqual(wantOld, wantNew)
		entry, listed := entries[name]
		if listed != differs {
			t.Errorf("dataset %q source-change-listed = %v, but trace roots old=%v new=%v differ=%v",
				name, listed, wantOld, wantNew, differs)
		}
		if listed {
			if !reflect.DeepEqual(entry.OldRoots, wantOld) {
				t.Errorf("dataset %q oldRoots = %v, trace says %v", name, entry.OldRoots, wantOld)
			}
			if !reflect.DeepEqual(entry.NewRoots, wantNew) {
				t.Errorf("dataset %q newRoots = %v, trace says %v", name, entry.NewRoots, wantNew)
			}
		}
	}
}

// cliCompare runs the compare command and parses its report.
func cliCompare(t *testing.T, oldPath, newPath string) *chainledger.CompareReport {
	t.Helper()
	stdout, stderr, exit := captureStdout(t, func() int {
		return run([]string{"compare", oldPath, newPath})
	})
	if exit != 0 {
		t.Fatalf("compare exit = %d, stderr = %s", exit, stderr)
	}
	var report chainledger.CompareReport
	if err := json.Unmarshal([]byte(stdout), &report); err != nil {
		t.Fatalf("compare stdout is not valid JSON: %v\n%s", err, stdout)
	}
	return &report
}

// The converging lineage, identical to the core fixture so the end-to-end run
// exercises multi-branch merges and post-merge derivation.
const cliOldGraph = `{"datasets":[
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

// TestCLIConsistencyPathOnlyRerouteReportsNoSourceChange: one branch moves to
// another path to the same root and the convergence point gains a shortcut to
// an already reachable root. Compare lists only the direct-upstream change;
// rootSourceChanges is empty even though trace representative paths move.
func TestCLIConsistencyPathOnlyRerouteReportsNoSourceChange(t *testing.T) {
	oldPath := snapshotForTrace(t, cliOldGraph)
	newPath := snapshotForTrace(t, `{"datasets":[
		{"name":"rootA","upstreams":[]},
		{"name":"rootB","upstreams":[]},
		{"name":"branchA1","upstreams":["branchA2"]},
		{"name":"branchA2","upstreams":["rootA"]},
		{"name":"branchB1","upstreams":["rootB"]},
		{"name":"summary","upstreams":["rootA","branchA1","branchA2","branchB1"]},
		{"name":"derived","upstreams":["summary"]},
		{"name":"soloRoot","upstreams":[]},
		{"name":"soloChild","upstreams":["soloRoot"]}
	]}`)
	oldBytes, newBytes := readFile(t, oldPath), readFile(t, newPath)

	report := cliCompare(t, oldPath, newPath)
	if got, want := report.ChangedDatasets, []string{"branchA1", "summary"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("ChangedDatasets = %v, want %v", got, want)
	}
	if len(report.RootSourceChanges) != 0 {
		t.Fatalf("path-only reroute plus shortcut must report no source change, got %v", report.RootSourceChanges)
	}

	// Trace representative paths moved (branchA1 now reaches rootA indirectly;
	// summary reaches rootA by the shortcut) while the root sets stayed equal.
	if got := cliTrace(t, newPath, "branchA1"); !reflect.DeepEqual(
		[]chainledger.SourceTrace{{Root: "rootA", Path: []string{"branchA1", "branchA2", "rootA"}}}, got) {
		t.Errorf("trace branchA1 = %+v, want indirect path to rootA", got)
	}
	if got := cliRootNames(t, "summary", cliTrace(t, newPath, "summary")); !reflect.DeepEqual(got, []string{"rootA", "rootB"}) {
		t.Errorf("trace summary roots = %v, want [rootA rootB]", got)
	}
	assertCLITraceCompareAgree(t, oldPath, newPath, report)

	// Read-only: both frozen files keep their bytes.
	if readFile(t, oldPath) != oldBytes || readFile(t, newPath) != newBytes {
		t.Errorf("compare/trace modified a snapshot file")
	}
}

// TestCLIConsistencyOneBranchChangeKeepsMultipathRoot: rootA remains reachable
// via branchA2 after branchA1 is repointed, so neither summary nor derived is a
// source change; only branchA1 changes.
func TestCLIConsistencyOneBranchChangeKeepsMultipathRoot(t *testing.T) {
	oldPath := snapshotForTrace(t, cliOldGraph)
	newPath := snapshotForTrace(t, `{"datasets":[
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
	report := cliCompare(t, oldPath, newPath)

	want := []chainledger.RootSourceChange{
		{Dataset: "branchA1", OldRoots: []string{"rootA"}, NewRoots: []string{"rootB"}},
	}
	if got := report.RootSourceChanges; !reflect.DeepEqual(got, want) {
		t.Fatalf("RootSourceChanges = %v, want %v", got, want)
	}
	if got := cliRootNames(t, "derived", cliTrace(t, newPath, "derived")); !reflect.DeepEqual(got, []string{"rootA", "rootB"}) {
		t.Errorf("trace derived roots = %v, rootA must survive via branchA2", got)
	}
	assertCLITraceCompareAgree(t, oldPath, newPath, report)
}

// TestCLIConsistencyLastRouteLostListsTransitiveDownstream: both A-branches
// move off rootA, so the converged summary and the downstream derived (whose
// direct upstream stays [summary]) lose rootA. Compare must list both with
// full old/new sets and the trace must keep only the reachable root.
func TestCLIConsistencyLastRouteLostListsTransitiveDownstream(t *testing.T) {
	oldPath := snapshotForTrace(t, cliOldGraph)
	newPath := snapshotForTrace(t, `{"datasets":[
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
	report := cliCompare(t, oldPath, newPath)

	want := []chainledger.RootSourceChange{
		{Dataset: "branchA1", OldRoots: []string{"rootA"}, NewRoots: []string{"rootB"}},
		{Dataset: "branchA2", OldRoots: []string{"rootA"}, NewRoots: []string{"rootB"}},
		{Dataset: "derived", OldRoots: []string{"rootA", "rootB"}, NewRoots: []string{"rootB"}},
		{Dataset: "summary", OldRoots: []string{"rootA", "rootB"}, NewRoots: []string{"rootB"}},
	}
	if got := report.RootSourceChanges; !reflect.DeepEqual(got, want) {
		t.Fatalf("RootSourceChanges = %v, want %v", got, want)
	}
	// derived is not a direct-upstream change.
	if got, want := report.ChangedDatasets, []string{"branchA1", "branchA2"}; !reflect.DeepEqual(got, want) {
		t.Errorf("ChangedDatasets = %v, want %v", got, want)
	}
	if got := cliRootNames(t, "derived", cliTrace(t, newPath, "derived")); !reflect.DeepEqual(got, []string{"rootB"}) {
		t.Errorf("trace derived roots = %v, want only still-reachable [rootB]", got)
	}
	assertCLITraceCompareAgree(t, oldPath, newPath, report)
}

// TestCLIConsistencyClearedToRootAndOneSidedFailures combines: keep becomes a
// root (trace only itself, single-element path), fresh exists only in the new
// snapshot, gone only in the old one. The one-sided names are new/removed,
// never source changes, and tracing them in the snapshot missing them fails
// with empty stdout.
func TestCLIConsistencyClearedToRootAndOneSidedFailures(t *testing.T) {
	oldPath := snapshotForTrace(t, `{"datasets":[
		{"name":"A","upstreams":[]},
		{"name":"gone","upstreams":["A"]},
		{"name":"keep","upstreams":["A"]}
	]}`)
	newPath := snapshotForTrace(t, `{"datasets":[
		{"name":"A","upstreams":[]},
		{"name":"fresh","upstreams":["A"]},
		{"name":"keep","upstreams":[]}
	]}`)
	report := cliCompare(t, oldPath, newPath)

	if got, want := report.NewDatasets, []string{"fresh"}; !reflect.DeepEqual(got, want) {
		t.Errorf("NewDatasets = %v, want %v", got, want)
	}
	if got, want := report.RemovedDatasets, []string{"gone"}; !reflect.DeepEqual(got, want) {
		t.Errorf("RemovedDatasets = %v, want %v", got, want)
	}
	wantChanges := []chainledger.RootSourceChange{
		{Dataset: "keep", OldRoots: []string{"A"}, NewRoots: []string{"keep"}},
	}
	if got := report.RootSourceChanges; !reflect.DeepEqual(got, wantChanges) {
		t.Fatalf("RootSourceChanges = %v, want %v", got, wantChanges)
	}

	// The cleared common dataset traces only to itself with a one-element path.
	if got := cliTrace(t, newPath, "keep"); !reflect.DeepEqual(
		[]chainledger.SourceTrace{{Root: "keep", Path: []string{"keep"}}}, got) {
		t.Errorf("trace keep (new) = %+v, want single-element self path", got)
	}

	// One-sided datasets trace fine where present and fail, with empty stdout,
	// in the snapshot that lacks them.
	if got := cliRootNames(t, "fresh", cliTrace(t, newPath, "fresh")); !reflect.DeepEqual(got, []string{"A"}) {
		t.Errorf("trace fresh where present = %v, want [A]", got)
	}
	assertCLITraceAbsent(t, oldPath, "fresh")
	assertCLITraceAbsent(t, newPath, "gone")

	assertCLITraceCompareAgree(t, oldPath, newPath, report)
}

// assertCLITraceAbsent requires that tracing an absent dataset fails with a
// non-zero exit and empty stdout.
func assertCLITraceAbsent(t *testing.T, snapPath, dataset string) {
	t.Helper()
	stdout, stderr, exit := captureStdout(t, func() int {
		return run([]string{"trace", snapPath, dataset})
	})
	if exit == 0 {
		t.Fatalf("trace of absent %q in %s succeeded, want failure", dataset, snapPath)
	}
	if stdout != "" {
		t.Errorf("trace of absent %q stdout = %q, want empty", dataset, stdout)
	}
	if !strings.Contains(stderr, dataset) {
		t.Errorf("trace of absent %q stderr = %q, must name the dataset", dataset, stderr)
	}
}
