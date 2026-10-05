package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/asdhoaiqqq/chainledger-governance/chainledger"
)

// traceGraph is a diamond with a direct shortcut: T reaches R directly, via
// B, and via A; it also reaches root Z via A. S is disconnected.
const traceGraph = `{"datasets":[
	{"name":"T","upstreams":["R","B","A"]},
	{"name":"A","upstreams":["R","Z"]},
	{"name":"B","upstreams":["R"]},
	{"name":"R","upstreams":[]},
	{"name":"Z","upstreams":[]},
	{"name":"S","upstreams":[]}
]}`

// snapshotForTrace writes graphJSON to a graph file, snapshots it, and
// returns the snapshot path.
func snapshotForTrace(t *testing.T, graphJSON string) string {
	t.Helper()
	dir := t.TempDir()
	graphPath := filepath.Join(dir, "graph.json")
	if err := os.WriteFile(graphPath, []byte(graphJSON), 0o644); err != nil {
		t.Fatalf("write graph: %v", err)
	}
	snapPath := filepath.Join(dir, "snap.json")
	if _, stderr, exit := captureStdout(t, func() int {
		return run([]string{"snapshot", graphPath, snapPath})
	}); exit != 0 {
		t.Fatalf("snapshot exit = %d, stderr = %s", exit, stderr)
	}
	return snapPath
}

func TestCLITraceProducesReport(t *testing.T) {
	snapPath := snapshotForTrace(t, traceGraph)
	snapBytes := readFile(t, snapPath)
	before := dirNames(t, filepath.Dir(snapPath))

	stdout, stderr, exit := captureStdout(t, func() int {
		return run([]string{"trace", snapPath, "T"})
	})
	if exit != 0 {
		t.Fatalf("trace exit = %d, stderr = %s", exit, stderr)
	}
	var report chainledger.TraceReport
	if err := json.Unmarshal([]byte(stdout), &report); err != nil {
		t.Fatalf("trace stdout is not valid JSON: %v\n%s", err, stdout)
	}
	if report.Dataset != "T" {
		t.Errorf("Dataset = %q, want %q", report.Dataset, "T")
	}
	snap, err := chainledger.ParseSnapshot([]byte(snapBytes))
	if err != nil {
		t.Fatalf("parse snapshot: %v", err)
	}
	if report.ContentID != snap.ContentID {
		t.Errorf("ContentID = %q, want %q", report.ContentID, snap.ContentID)
	}
	want := []chainledger.SourceTrace{
		{Root: "R", Path: []string{"T", "R"}},
		{Root: "Z", Path: []string{"T", "A", "Z"}},
	}
	if !reflect.DeepEqual(report.Sources, want) {
		t.Errorf("Sources = %+v, want %+v", report.Sources, want)
	}

	// trace is read-only: the snapshot keeps every byte and no lock file or
	// other sibling is created.
	if got := readFile(t, snapPath); got != snapBytes {
		t.Errorf("trace modified the snapshot:\n%s", got)
	}
	if after := dirNames(t, filepath.Dir(snapPath)); !reflect.DeepEqual(after, before) {
		t.Errorf("trace changed directory contents: before %v, after %v", before, after)
	}
}

// dirNames returns the sorted names of the entries in dir.
func dirNames(t *testing.T, dir string) []string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("readdir: %v", err)
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		names = append(names, e.Name())
	}
	return names
}

func TestCLITraceRootQueriesItself(t *testing.T) {
	snapPath := snapshotForTrace(t, traceGraph)
	stdout, stderr, exit := captureStdout(t, func() int {
		return run([]string{"trace", snapPath, "R"})
	})
	if exit != 0 {
		t.Fatalf("trace exit = %d, stderr = %s", exit, stderr)
	}
	var report chainledger.TraceReport
	if err := json.Unmarshal([]byte(stdout), &report); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}
	want := []chainledger.SourceTrace{{Root: "R", Path: []string{"R"}}}
	if !reflect.DeepEqual(report.Sources, want) {
		t.Errorf("Sources = %+v, want %+v", report.Sources, want)
	}
}

func TestCLITraceUnknownAndEmptyDataset(t *testing.T) {
	snapPath := snapshotForTrace(t, traceGraph)
	for _, dataset := range []string{"ghost", ""} {
		stdout, stderr, exit := captureStdout(t, func() int {
			return run([]string{"trace", snapPath, dataset})
		})
		if exit == 0 {
			t.Fatalf("trace %q succeeded, want failure", dataset)
		}
		if stdout != "" {
			t.Errorf("trace %q stdout = %q, want empty", dataset, stdout)
		}
		if !strings.Contains(stderr, snapPath) {
			t.Errorf("trace %q stderr = %q, must name the snapshot file", dataset, stderr)
		}
	}
	// The unknown name itself must be mentioned.
	_, stderr, _ := captureStdout(t, func() int {
		return run([]string{"trace", snapPath, "ghost"})
	})
	if !strings.Contains(stderr, "ghost") {
		t.Errorf("stderr = %q, must name the queried dataset", stderr)
	}
}

func TestCLITraceRejectsInvalidSnapshot(t *testing.T) {
	for name, content := range map[string]string{
		"not json":       `garbage{`,
		"missing fields": `{}`,
		"bad version":    `{"formatVersion":9,"contentId":"x","graph":{"datasets":[]}}`,
		"cycle":          `{"formatVersion":1,"contentId":"x","graph":{"datasets":[{"name":"A","upstreams":["B"]},{"name":"B","upstreams":["A"]}]}}`,
	} {
		t.Run(name, func(t *testing.T) {
			snapPath := writeFile(t, "snap.json", content)
			stdout, stderr, exit := captureStdout(t, func() int {
				return run([]string{"trace", snapPath, "A"})
			})
			if exit == 0 {
				t.Fatalf("trace over invalid snapshot succeeded")
			}
			if stdout != "" {
				t.Errorf("stdout = %q, want empty", stdout)
			}
			if !strings.Contains(stderr, snapPath) {
				t.Errorf("stderr = %q, must name the file", stderr)
			}
		})
	}
}

func TestCLITraceMissingFile(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "missing.json")
	stdout, stderr, exit := captureStdout(t, func() int {
		return run([]string{"trace", missing, "A"})
	})
	if exit == 0 {
		t.Fatalf("trace of missing file succeeded")
	}
	if stdout != "" {
		t.Errorf("stdout = %q, want empty", stdout)
	}
	if !strings.Contains(stderr, missing) {
		t.Errorf("stderr = %q, must name the file", stderr)
	}
}

func TestCLITraceEmptyGraphHasNoDataset(t *testing.T) {
	snapPath := snapshotForTrace(t, `{"datasets":[]}`)
	stdout, stderr, exit := captureStdout(t, func() int {
		return run([]string{"trace", snapPath, "A"})
	})
	if exit == 0 {
		t.Fatalf("trace in empty snapshot succeeded")
	}
	if stdout != "" {
		t.Errorf("stdout = %q, want empty", stdout)
	}
	if !strings.Contains(stderr, "A") {
		t.Errorf("stderr = %q, must name the queried dataset", stderr)
	}
}

func TestCLITraceUsageOnWrongArgCount(t *testing.T) {
	for _, args := range [][]string{
		{"trace"},
		{"trace", "only-one.json"},
		{"trace", "a.json", "A", "extra"},
	} {
		stdout, stderr, exit := captureStdout(t, func() int { return run(args) })
		if exit == 0 {
			t.Fatalf("%v succeeded, want failure", args)
		}
		if stdout != "" {
			t.Errorf("%v stdout = %q, want empty", args, stdout)
		}
		if !strings.Contains(stderr, "usage: chainledger trace") {
			t.Errorf("%v stderr = %q, want usage", args, stderr)
		}
	}
}

// TestCLITraceEquivalentSnapshotsSameReport snapshots two semantically equal
// graphs (differing only in record order, upstream order, duplicate
// upstreams, and whitespace) and requires byte-identical trace reports.
func TestCLITraceEquivalentSnapshotsSameReport(t *testing.T) {
	shuffled := `{ "datasets": [ {"upstreams":[],"name":"S"}, {"upstreams":[],"name":"Z"},
		{"name":"R","upstreams":[]}, {"name":"B","upstreams":["R","R"]},
		{"name":"A","upstreams":["Z","R"]}, {"name":"T","upstreams":["A","B","R","A"]} ] }`
	s1 := snapshotForTrace(t, traceGraph)
	s2 := snapshotForTrace(t, shuffled)

	out1, stderr, exit := captureStdout(t, func() int { return run([]string{"trace", s1, "T"}) })
	if exit != 0 {
		t.Fatalf("trace s1: %s", stderr)
	}
	out2, stderr, exit := captureStdout(t, func() int { return run([]string{"trace", s2, "T"}) })
	if exit != 0 {
		t.Fatalf("trace s2: %s", stderr)
	}
	if out1 != out2 {
		t.Errorf("equivalent snapshots produced different reports:\n%s\n%s", out1, out2)
	}
}

// TestCLITraceDeepConvergingBranchesByteStable exercises the path-selection
// rule through deep lineage end to end. T reaches R1 and R2 along two
// four-relation branches that merge at M:
//
//	T -> A -> Z -> M -> {R1,R2}
//	T -> B -> C -> M -> {R1,R2}
//
// The branch via A wins at the first differing name (A < B) even though the
// other branch passes through C < Z. The complete JSON report must be
// byte-for-byte identical when the same graph is reshuffled (record order,
// upstream order, duplicate upstreams, whitespace), including a disconnected
// branch, and must exactly match the golden bytes: sources ordered by root
// name byte order and full path arrays from the query object to each root.
func TestCLITraceDeepConvergingBranchesByteStable(t *testing.T) {
	deepGraph := `{"datasets":[
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
	]}`
	shuffled := `{ "datasets" : [
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
	] }`

	s1 := snapshotForTrace(t, deepGraph)
	s2 := snapshotForTrace(t, shuffled)

	out1, stderr, exit := captureStdout(t, func() int { return run([]string{"trace", s1, "T"}) })
	if exit != 0 {
		t.Fatalf("trace s1: %s", stderr)
	}
	out2, stderr, exit := captureStdout(t, func() int { return run([]string{"trace", s2, "T"}) })
	if exit != 0 {
		t.Fatalf("trace s2: %s", stderr)
	}
	if out1 != out2 {
		t.Errorf("equivalent deep snapshots produced different reports:\n%s\n%s", out1, out2)
	}

	// The content identifier comes from the snapshot actually read; the rest
	// is the exact golden report, pinning formatting, key order, root-name
	// ordering, and the full name arrays.
	snap, err := chainledger.ParseSnapshot([]byte(readFile(t, s1)))
	if err != nil {
		t.Fatalf("parse snapshot: %v", err)
	}
	golden := "{\n" +
		"  \"contentId\": " + fmt.Sprintf("%q", snap.ContentID) + ",\n" +
		"  \"dataset\": \"T\",\n" +
		"  \"sources\": [\n" +
		"    {\n" +
		"      \"root\": \"R1\",\n" +
		"      \"path\": [\n" +
		"        \"T\",\n" +
		"        \"A\",\n" +
		"        \"Z\",\n" +
		"        \"M\",\n" +
		"        \"R1\"\n" +
		"      ]\n" +
		"    },\n" +
		"    {\n" +
		"      \"root\": \"R2\",\n" +
		"      \"path\": [\n" +
		"        \"T\",\n" +
		"        \"A\",\n" +
		"        \"Z\",\n" +
		"        \"M\",\n" +
		"        \"R2\"\n" +
		"      ]\n" +
		"    }\n" +
		"  ]\n" +
		"}\n"
	if out1 != golden {
		t.Errorf("deep trace report differs from golden:\n got: %q\nwant: %q", out1, golden)
	}
}

// traceSharedPrefixGraph is the CLI counterpart of the report-independence
// fixture in the core package: T and U both reach roots R1 and R2 through the
// common intermediate M, so both of T's paths share the prefix [T M], and X is
// a disconnected root.
const traceSharedPrefixGraph = `{"datasets":[
	{"name":"T","upstreams":["M"]},
	{"name":"U","upstreams":["M"]},
	{"name":"M","upstreams":["R1","R2"]},
	{"name":"R1","upstreams":[]},
	{"name":"R2","upstreams":[]},
	{"name":"X","upstreams":[]}
]}`

// runTraceCommand invokes `trace snapPath dataset` and returns its stdout,
// stderr, and exit code.
func runTraceCommand(t *testing.T, snapPath, dataset string) (stdout, stderr string, exit int) {
	t.Helper()
	return captureStdout(t, func() int {
		return run([]string{"trace", snapPath, dataset})
	})
}

// TestCLITraceReportsAreIndependentAcrossInvocations pins the command-line
// shape of the report-independence guarantee: two successful trace outputs for
// the same query and one for another downstream through the same shared
// intermediate each keep representing the result at their own invocation, and
// the read-only command never modifies the snapshot. A caller editing one
// emitted JSON report cannot affect another invocation's report because each
// run computes from the frozen snapshot file, which these later runs must find
// byte-for-byte unchanged.
func TestCLITraceReportsAreIndependentAcrossInvocations(t *testing.T) {
	snapPath := snapshotForTrace(t, traceSharedPrefixGraph)
	snapBytes := readFile(t, snapPath)

	outT1, stderr, exit := runTraceCommand(t, snapPath, "T")
	if exit != 0 {
		t.Fatalf("first trace T exit = %d, stderr = %s", exit, stderr)
	}
	outT2, stderr, exit := runTraceCommand(t, snapPath, "T")
	if exit != 0 {
		t.Fatalf("second trace T exit = %d, stderr = %s", exit, stderr)
	}
	outU, stderr, exit := runTraceCommand(t, snapPath, "U")
	if exit != 0 {
		t.Fatalf("trace U exit = %d, stderr = %s", exit, stderr)
	}

	// Repeated results are byte-identical: every report is computed from the
	// same frozen snapshot, so display edits of one copy can never reach later
	// output.
	if outT1 != outT2 {
		t.Errorf("repeated T traces differ:\n%s\n%s", outT1, outT2)
	}

	// The other downstream through the same shared intermediate reports its
	// own query name and its own complete paths.
	var reportU chainledger.TraceReport
	if err := json.Unmarshal([]byte(outU), &reportU); err != nil {
		t.Fatalf("trace U stdout is not valid JSON: %v\n%s", err, outU)
	}
	if reportU.Dataset != "U" {
		t.Errorf("U report Dataset = %q, want %q", reportU.Dataset, "U")
	}
	wantU := []chainledger.SourceTrace{
		{Root: "R1", Path: []string{"U", "M", "R1"}},
		{Root: "R2", Path: []string{"U", "M", "R2"}},
	}
	if !reflect.DeepEqual(reportU.Sources, wantU) {
		t.Errorf("U Sources = %+v, want %+v", reportU.Sources, wantU)
	}
	var reportT chainledger.TraceReport
	if err := json.Unmarshal([]byte(outT1), &reportT); err != nil {
		t.Fatalf("trace T stdout is not valid JSON: %v\n%s", err, outT1)
	}
	wantT := []chainledger.SourceTrace{
		{Root: "R1", Path: []string{"T", "M", "R1"}},
		{Root: "R2", Path: []string{"T", "M", "R2"}},
	}
	if !reflect.DeepEqual(reportT.Sources, wantT) {
		t.Errorf("T Sources = %+v, want %+v", reportT.Sources, wantT)
	}

	// The snapshot file is still byte-for-byte the frozen input.
	if got := readFile(t, snapPath); got != snapBytes {
		t.Errorf("snapshot changed across trace invocations:\n got %s\nwant %s", got, snapBytes)
	}
}

// TestCLITraceSuccessThenErrorsLeavesSnapshotAndRequeryUntouched pins, end to
// end, the boundary around rejected queries: after successful reports are in
// hand, an empty name and an unknown name keep failing with the existing error
// categories and empty stdout, while the snapshot file stays untouched and a
// later successful query still reports the frozen sources and paths.
func TestCLITraceSuccessThenErrorsLeavesSnapshotAndRequeryUntouched(t *testing.T) {
	snapPath := snapshotForTrace(t, traceSharedPrefixGraph)
	snapBytes := readFile(t, snapPath)

	okOut, stderr, exit := runTraceCommand(t, snapPath, "T")
	if exit != 0 {
		t.Fatalf("trace T exit = %d, stderr = %s", exit, stderr)
	}
	rootOut, stderr, exit := runTraceCommand(t, snapPath, "R1")
	if exit != 0 {
		t.Fatalf("trace R1 exit = %d, stderr = %s", exit, stderr)
	}

	for _, dataset := range []string{"", "ghost"} {
		stdout, stderr, exit := runTraceCommand(t, snapPath, dataset)
		if exit == 0 {
			t.Fatalf("trace %q succeeded, want failure", dataset)
		}
		if stdout != "" {
			t.Errorf("trace %q stdout = %q, want empty", dataset, stdout)
		}
		if !strings.Contains(stderr, snapPath) {
			t.Errorf("trace %q stderr = %q, must name the snapshot file", dataset, stderr)
		}
	}

	// The snapshot file is unchanged by the rejected queries.
	if got := readFile(t, snapPath); got != snapBytes {
		t.Errorf("snapshot changed after rejected queries:\n got %s\nwant %s", got, snapBytes)
	}

	// A later successful query returns the frozen result byte-for-byte,
	// including the root's single-element path.
	retryT, stderr, exit := runTraceCommand(t, snapPath, "T")
	if exit != 0 {
		t.Fatalf("retry trace T exit = %d, stderr = %s", exit, stderr)
	}
	if retryT != okOut {
		t.Errorf("retry trace T differs from the earlier success:\n got %s\nwant %s", retryT, okOut)
	}
	retryRoot, stderr, exit := runTraceCommand(t, snapPath, "R1")
	if exit != 0 {
		t.Fatalf("retry trace R1 exit = %d, stderr = %s", exit, stderr)
	}
	if retryRoot != rootOut {
		t.Errorf("retry trace R1 differs from the earlier success:\n got %s\nwant %s", retryRoot, rootOut)
	}
	var rootReport chainledger.TraceReport
	if err := json.Unmarshal([]byte(retryRoot), &rootReport); err != nil {
		t.Fatalf("trace R1 stdout is not valid JSON: %v\n%s", err, retryRoot)
	}
	if want := []chainledger.SourceTrace{{Root: "R1", Path: []string{"R1"}}}; !reflect.DeepEqual(rootReport.Sources, want) {
		t.Errorf("R1 Sources = %+v, want %+v", rootReport.Sources, want)
	}
}

func TestCLIHelpMentionsTrace(t *testing.T) {
	stdout, _, exit := captureStdout(t, func() int { return run([]string{"help"}) })
	if exit != 0 {
		t.Fatalf("help exit = %d", exit)
	}
	if !strings.Contains(stdout, "trace") {
		t.Errorf("help missing trace:\n%s", stdout)
	}
}
