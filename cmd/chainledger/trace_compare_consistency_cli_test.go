package main

import (
	"encoding/json"
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/asdhoaiqqq/chainledger-governance/chainledger"
)

// This file is the end-to-end regression net for the guarantee that, when a
// user views the SAME pair of snapshots through both commands,
// `chainledger compare` and `chainledger trace` explain root sources
// identically. Everything goes through the real CLI: graphs are snapshotted
// to files, compare reads the two files, and trace is run once per dataset in
// each frozen version.
//
// The fixture combines every required situation:
//
//   - q is a path-only change: it stops depending directly on rootA and hangs
//     off a0, another route to the very same root. Its representative trace
//     path changes but its root set does not, so compare must not list it;
//   - a1 is repointed from rootA to rootB and a2 drops rootA (keeping
//     rootC), so the last routes from the merge node to rootA disappear:
//     merge and its downstream down lose rootA even though their DIRECT
//     upstreams never change;
//   - clear has all of its upstreams cleared and becomes a root, while after
//     keeps [clear] directly and moves roots purely as a consequence;
//   - gone exists only in the old version, born only in the new one.
const cliConsistencyOldGraph = `{"datasets":[
	{"name":"rootA","upstreams":[]},
	{"name":"rootB","upstreams":[]},
	{"name":"rootC","upstreams":[]},
	{"name":"a0","upstreams":["rootA"]},
	{"name":"a1","upstreams":["rootA"]},
	{"name":"a2","upstreams":["rootA","rootC"]},
	{"name":"b1","upstreams":["rootB"]},
	{"name":"merge","upstreams":["a1","a2","b1"]},
	{"name":"down","upstreams":["merge"]},
	{"name":"q","upstreams":["rootA"]},
	{"name":"clear","upstreams":["rootA"]},
	{"name":"after","upstreams":["clear"]},
	{"name":"gone","upstreams":["rootA"]}
]}`

const cliConsistencyNewGraph = `{"datasets":[
	{"name":"rootA","upstreams":[]},
	{"name":"rootB","upstreams":[]},
	{"name":"rootC","upstreams":[]},
	{"name":"a0","upstreams":["rootA"]},
	{"name":"a1","upstreams":["rootB"]},
	{"name":"a2","upstreams":["rootC"]},
	{"name":"b1","upstreams":["rootB"]},
	{"name":"merge","upstreams":["a1","a2","b1"]},
	{"name":"down","upstreams":["merge"]},
	{"name":"q","upstreams":["a0"]},
	{"name":"clear","upstreams":[]},
	{"name":"after","upstreams":["clear"]},
	{"name":"born","upstreams":["rootB"]}
]}`

// traceCLI runs `trace snapPath dataset` through the real command and parses
// the successful JSON report.
func traceCLI(t *testing.T, snapPath, dataset string) *chainledger.TraceReport {
	t.Helper()
	stdout, stderr, exit := captureStdout(t, func() int {
		return run([]string{"trace", snapPath, dataset})
	})
	if exit != 0 {
		t.Fatalf("trace %q exit = %d, stderr = %s", dataset, exit, stderr)
	}
	var report chainledger.TraceReport
	if err := json.Unmarshal([]byte(stdout), &report); err != nil {
		t.Fatalf("trace %q stdout is not valid JSON: %v\n%s", dataset, err, stdout)
	}
	return &report
}

// traceRootNamesCLI returns just the sorted-by-output root names of a trace.
func traceRootNamesCLI(t *testing.T, snapPath, dataset string) []string {
	t.Helper()
	report := traceCLI(t, snapPath, dataset)
	roots := make([]string, 0, len(report.Sources))
	for _, src := range report.Sources {
		roots = append(roots, src.Root)
	}
	return roots
}

// TestCLICompareAndTraceAgreeOnRootExplanations is the headline end-to-end
// check: for every common dataset the compare report's oldRoots/newRoots equal
// the roots the trace command reports in the corresponding frozen snapshot,
// and exactly the datasets with differing traced root sets appear in
// rootSourceChanges.
func TestCLICompareAndTraceAgreeOnRootExplanations(t *testing.T) {
	oldPath := snapshotForTrace(t, cliConsistencyOldGraph)
	newPath := snapshotForTrace(t, cliConsistencyNewGraph)
	oldBytes, newBytes := readFile(t, oldPath), readFile(t, newPath)

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

	// Direct-change and version-only classifications stay exactly as the
	// underlying rules state; root-source explanation is checked separately.
	if got, want := report.NewDatasets, []string{"born"}; !reflect.DeepEqual(got, want) {
		t.Errorf("NewDatasets = %v, want %v", got, want)
	}
	if got, want := report.RemovedDatasets, []string{"gone"}; !reflect.DeepEqual(got, want) {
		t.Errorf("RemovedDatasets = %v, want %v", got, want)
	}
	if got, want := report.ChangedDatasets, []string{"a1", "a2", "clear", "q"}; !reflect.DeepEqual(got, want) {
		t.Errorf("ChangedDatasets = %v, want %v", got, want)
	}

	wantChanges := []chainledger.RootSourceChange{
		{Dataset: "a1", OldRoots: []string{"rootA"}, NewRoots: []string{"rootB"}},
		{Dataset: "a2", OldRoots: []string{"rootA", "rootC"}, NewRoots: []string{"rootC"}},
		{Dataset: "after", OldRoots: []string{"rootA"}, NewRoots: []string{"clear"}},
		{Dataset: "clear", OldRoots: []string{"rootA"}, NewRoots: []string{"clear"}},
		{Dataset: "down", OldRoots: []string{"rootA", "rootB", "rootC"}, NewRoots: []string{"rootB", "rootC"}},
		{Dataset: "merge", OldRoots: []string{"rootA", "rootB", "rootC"}, NewRoots: []string{"rootB", "rootC"}},
	}
	if got := report.RootSourceChanges; !reflect.DeepEqual(got, wantChanges) {
		t.Fatalf("RootSourceChanges = %v\nwant %v", got, wantChanges)
	}

	// Every common dataset: reconcile compare's recorded sets against actual
	// trace command output in each frozen snapshot.
	common := []string{
		"rootA", "rootB", "rootC", "a0", "a1", "a2", "b1",
		"merge", "down", "q", "clear", "after",
	}
	sort.Strings(common)
	listed := map[string]bool{}
	for _, change := range report.RootSourceChanges {
		listed[change.Dataset] = true
		if got := traceRootNamesCLI(t, oldPath, change.Dataset); !reflect.DeepEqual(got, change.OldRoots) {
			t.Errorf("%s: old trace roots %v != compare oldRoots %v", change.Dataset, got, change.OldRoots)
		}
		if got := traceRootNamesCLI(t, newPath, change.Dataset); !reflect.DeepEqual(got, change.NewRoots) {
			t.Errorf("%s: new trace roots %v != compare newRoots %v", change.Dataset, got, change.NewRoots)
		}
	}
	for _, name := range common {
		oldRoots := traceRootNamesCLI(t, oldPath, name)
		newRoots := traceRootNamesCLI(t, newPath, name)
		differ := !reflect.DeepEqual(oldRoots, newRoots)
		if differ != listed[name] {
			t.Errorf("dataset %q: traced sets differ=%v but rootSourceChanges presence=%v (old=%v new=%v)",
				name, differ, listed[name], oldRoots, newRoots)
		}
	}

	// Path-only change: q's representative path changes (rootA becomes two
	// relations away via a0), yet its root set stays {rootA} and compare does
	// not list it. Both trace paths are complete arrays ending at rootA.
	oldQ := traceCLI(t, oldPath, "q")
	newQ := traceCLI(t, newPath, "q")
	if got, want := oldQ.Sources, []chainledger.SourceTrace{
		{Root: "rootA", Path: []string{"q", "rootA"}},
	}; !reflect.DeepEqual(got, want) {
		t.Errorf("old q sources = %+v, want %+v", got, want)
	}
	if got, want := newQ.Sources, []chainledger.SourceTrace{
		{Root: "rootA", Path: []string{"q", "a0", "rootA"}},
	}; !reflect.DeepEqual(got, want) {
		t.Errorf("new q sources = %+v, want %+v (path changed, root did not)", got, want)
	}
	if listed["q"] {
		t.Errorf("q must not be a root-source change when only its path changed")
	}

	// The cleared dataset traces itself only, as a single-element path, in the
	// new version; its unchanged-direct-upstream child after reports it as the
	// sole root too.
	if got, want := traceCLI(t, newPath, "clear").Sources,
		[]chainledger.SourceTrace{{Root: "clear", Path: []string{"clear"}}}; !reflect.DeepEqual(got, want) {
		t.Errorf("new clear sources = %+v, want single-element self path %+v", got, want)
	}
	if got, want := traceCLI(t, newPath, "after").Sources,
		[]chainledger.SourceTrace{{Root: "clear", Path: []string{"after", "clear"}}}; !reflect.DeepEqual(got, want) {
		t.Errorf("new after sources = %+v, want %+v", got, want)
	}

	// Neither report may mistake an intermediate with upstreams for a root:
	// merge/down never appear as roots in any trace.
	for _, snapPath := range []string{oldPath, newPath} {
		for _, name := range common {
			for _, src := range traceCLI(t, snapPath, name).Sources {
				if src.Root == "merge" || src.Root == "down" || src.Root == "a1" || src.Root == "a2" || src.Root == "b1" || src.Root == "after" {
					t.Errorf("trace of %q in %s reports intermediate %q as a root: %+v", name, snapPath, src.Root, src)
				}
			}
		}
	}

	// Version-only datasets stay out of rootSourceChanges, trace successfully
	// where they exist, and fail with no success output where they do not.
	for _, change := range report.RootSourceChanges {
		if change.Dataset == "born" || change.Dataset == "gone" {
			t.Errorf("version-only dataset %q must not appear in rootSourceChanges", change.Dataset)
		}
	}
	if got := traceRootNamesCLI(t, newPath, "born"); !reflect.DeepEqual(got, []string{"rootB"}) {
		t.Errorf("new trace born roots = %v, want [rootB]", got)
	}
	if got := traceRootNamesCLI(t, oldPath, "gone"); !reflect.DeepEqual(got, []string{"rootA"}) {
		t.Errorf("old trace gone roots = %v, want [rootA]", got)
	}
	for _, tc := range []struct {
		snapPath, dataset string
	}{
		{oldPath, "born"},
		{newPath, "gone"},
	} {
		out, stderr, exitCode := captureStdout(t, func() int {
			return run([]string{"trace", tc.snapPath, tc.dataset})
		})
		if exitCode == 0 {
			t.Errorf("trace %q in snapshot missing it succeeded, want failure", tc.dataset)
		}
		if out != "" {
			t.Errorf("trace %q in snapshot missing it wrote success output %q", tc.dataset, out)
		}
		if !strings.Contains(stderr, tc.dataset) || !strings.Contains(stderr, tc.snapPath) {
			t.Errorf("trace %q stderr = %q, must name the dataset and snapshot file", tc.dataset, stderr)
		}
	}

	// Both commands are read-only: the snapshot files are byte-for-byte
	// unchanged after compare and every trace.
	if got := readFile(t, oldPath); got != oldBytes {
		t.Errorf("old snapshot modified:\n got %s\nwant %s", got, oldBytes)
	}
	if got := readFile(t, newPath); got != newBytes {
		t.Errorf("new snapshot modified:\n got %s\nwant %s", got, newBytes)
	}
}

// TestCLICompareAndTraceStableAcrossEquivalentSpellings feeds the same
// transition with records shuffled, upstreams reordered and repeated, and JSON
// whitespace changed: compare output must be byte-identical and every trace
// output must be byte-identical to the canonical-spelling run.
func TestCLICompareAndTraceStableAcrossEquivalentSpellings(t *testing.T) {
	oldCanonical := snapshotForTrace(t, cliConsistencyOldGraph)
	newCanonical := snapshotForTrace(t, cliConsistencyNewGraph)

	oldShuffledGraph := `{ "datasets" : [
		{"upstreams":["rootA"],"name":"gone"},
		{"name":"after","upstreams":["clear","clear"]},
		{"upstreams":["rootA"],"name":"clear"},
		{"upstreams":["rootA","rootA"],"name":"q"},
		{"name":"down","upstreams":["merge"]},
		{"upstreams":["a1","a2","b1","a2"],"name":"merge"},
		{"name":"b1","upstreams":["rootB"]},
		{"upstreams":["rootC","rootA"],"name":"a2"},
		{"name":"a1","upstreams":["rootA"]},
		{"name":"a0","upstreams":["rootA"]},
		{"name":"rootC","upstreams":[]},
		{"upstreams":[],"name":"rootB"},
		{"upstreams":[],"name":"rootA"}
	] }`
	newShuffledGraph := `{"datasets":[
		{"upstreams":["rootB"],"name":"born"},
		{"upstreams":["clear"],"name":"after"},
		{"name":"clear","upstreams":[]},
		{"name":"q","upstreams":["a0"]},
		{"upstreams":["merge","merge"],"name":"down"},
		{"name":"merge","upstreams":["b1","a2","a1"]},
		{"upstreams":["rootB"],"name":"b1"},
		{"name":"a2","upstreams":["rootC","rootC"]},
		{"name":"a1","upstreams":["rootB"]},
		{"upstreams":["rootA"],"name":"a0"},
		{"name":"rootC","upstreams":[]},
		{"name":"rootB","upstreams":[]},
		{"name":"rootA","upstreams":[]}
	]}`
	oldShuffled := snapshotForTrace(t, oldShuffledGraph)
	newShuffled := snapshotForTrace(t, newShuffledGraph)

	compareOut := func(oldSnap, newSnap string) string {
		t.Helper()
		out, stderr, exitCode := captureStdout(t, func() int {
			return run([]string{"compare", oldSnap, newSnap})
		})
		if exitCode != 0 {
			t.Fatalf("compare exit = %d, stderr = %s", exitCode, stderr)
		}
		return out
	}
	canonicalCompare := compareOut(oldCanonical, newCanonical)
	if got := compareOut(oldShuffled, newShuffled); got != canonicalCompare {
		t.Fatalf("compare differs across equivalent spellings:\n got %s\nwant %s", got, canonicalCompare)
	}

	for _, dataset := range []string{
		"rootA", "rootB", "rootC", "a0", "a1", "a2", "b1",
		"merge", "down", "q", "clear", "after",
	} {
		traceOut := func(snapPath string) string {
			t.Helper()
			out, stderr, exitCode := captureStdout(t, func() int {
				return run([]string{"trace", snapPath, dataset})
			})
			if exitCode != 0 {
				t.Fatalf("trace %q exit = %d, stderr = %s", dataset, exitCode, stderr)
			}
			return out
		}
		if got := traceOut(oldShuffled); got != traceOut(oldCanonical) {
			t.Errorf("old trace %q differs across equivalent spellings", dataset)
		}
		if got := traceOut(newShuffled); got != traceOut(newCanonical) {
			t.Errorf("new trace %q differs across equivalent spellings", dataset)
		}
	}

	// Shuffled-graph snapshots carry the same content identifiers as the
	// canonical ones because content addressing sees only semantics.
	oldCanonID := parseSnapshotID(t, oldCanonical)
	newCanonID := parseSnapshotID(t, newCanonical)
	if got := parseSnapshotID(t, oldShuffled); got != oldCanonID {
		t.Errorf("old shuffled contentId = %q, want %q", got, oldCanonID)
	}
	if got := parseSnapshotID(t, newShuffled); got != newCanonID {
		t.Errorf("new shuffled contentId = %q, want %q", got, newCanonID)
	}
}

// parseSnapshotID reads a snapshot file through the public parser and returns
// its content identifier.
func parseSnapshotID(t *testing.T, path string) string {
	t.Helper()
	snap, err := chainledger.ParseSnapshot([]byte(readFile(t, path)))
	if err != nil {
		t.Fatalf("parse snapshot %s: %v", path, err)
	}
	return snap.ContentID
}
