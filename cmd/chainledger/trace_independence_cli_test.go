package main

import (
	"encoding/json"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/asdhoaiqqq/chainledger-governance/chainledger"
)

// End-to-end (snapshot file in, JSON report out) regression coverage for the
// independence of trace reports. Every `trace` invocation is strictly
// read-only and answers only from the frozen snapshot file, so:
//
//   - two successive reports of the same query, and reports of two different
//     downstreams reaching the same roots through the SAME intermediate
//     dataset, are each self-contained: re-running any query returns
//     byte-identical JSON with the snapshot's content identifier and the
//     queried name;
//   - a root queried about itself gets its own single-element-path report;
//   - a later empty-name or unknown-name query fails with the existing error
//     behavior (non-zero exit, empty stdout, stderr naming the snapshot and
//     dataset), never emits a success report, and leaves both an earlier
//     success result and the snapshot file byte-for-byte untouched;
//   - the snapshot file's content identifier, graph content, and on-disk
//     bytes are identical before and after tracing.
//
// The command line shape stays `chainledger trace <snapshot> <dataset>`.

// traceIndependenceGraphFile is D -> P -> M -> {R1,R2} and E -> Q -> M, plus
// a disconnected branch: the two traced downstreams D and E converge on the
// same intermediate M and the same two roots.
const traceIndependenceGraphFile = `{"datasets":[
	{"name":"D","upstreams":["P"]},
	{"name":"E","upstreams":["Q"]},
	{"name":"P","upstreams":["M"]},
	{"name":"Q","upstreams":["M"]},
	{"name":"M","upstreams":["R2","R1"]},
	{"name":"R1","upstreams":[]},
	{"name":"R2","upstreams":[]},
	{"name":"U","upstreams":["X"]},
	{"name":"X","upstreams":[]}
]}`

// runTraceOK runs one successful trace and returns its stdout.
func runTraceOK(t *testing.T, snapPath, dataset string) string {
	t.Helper()
	stdout, stderr, exit := captureStdout(t, func() int {
		return run([]string{"trace", snapPath, dataset})
	})
	if exit != 0 {
		t.Fatalf("trace %q exit = %d, stderr = %s", dataset, exit, stderr)
	}
	return stdout
}

// parseTraceReport decodes one trace stdout report.
func parseTraceReport(t *testing.T, stdout string) *chainledger.TraceReport {
	t.Helper()
	var report chainledger.TraceReport
	if err := json.Unmarshal([]byte(stdout), &report); err != nil {
		t.Fatalf("trace stdout is not valid JSON: %v\n%s", err, stdout)
	}
	return &report
}

// TestCLITraceRepeatedAndSharedIntermediateReportsAreIndependent drives
// several successful traces against one snapshot file — the same downstream
// twice, a second downstream through the same intermediate, and a root about
// itself — and pins that every answer is independently correct and stable,
// carries the snapshot's content id and the queried name, and leaves the
// snapshot file and its directory untouched.
func TestCLITraceRepeatedAndSharedIntermediateReportsAreIndependent(t *testing.T) {
	snapPath := snapshotForTrace(t, traceIndependenceGraphFile)
	snapBytes := readFile(t, snapPath)
	before := dirNames(t, filepath.Dir(snapPath))
	snap, err := chainledger.ParseSnapshot([]byte(snapBytes))
	if err != nil {
		t.Fatalf("parse snapshot: %v", err)
	}

	outD1 := runTraceOK(t, snapPath, "D")
	outD2 := runTraceOK(t, snapPath, "D")
	outE := runTraceOK(t, snapPath, "E")
	outR1 := runTraceOK(t, snapPath, "R1")

	// The same query repeated is byte-identical: a later invocation cannot
	// answer differently from an earlier held result.
	if outD1 != outD2 {
		t.Errorf("repeated trace D reports differ:\n%s\n%s", outD1, outD2)
	}

	wantD := []chainledger.SourceTrace{
		{Root: "R1", Path: []string{"D", "P", "M", "R1"}},
		{Root: "R2", Path: []string{"D", "P", "M", "R2"}},
	}
	wantE := []chainledger.SourceTrace{
		{Root: "R1", Path: []string{"E", "Q", "M", "R1"}},
		{Root: "R2", Path: []string{"E", "Q", "M", "R2"}},
	}
	wantRoot := []chainledger.SourceTrace{{Root: "R1", Path: []string{"R1"}}}

	for _, tc := range []struct {
		name    string
		out     string
		dataset string
		sources []chainledger.SourceTrace
	}{
		{"downstream D", outD1, "D", wantD},
		{"other downstream E through same M", outE, "E", wantE},
		{"root R1 single element", outR1, "R1", wantRoot},
	} {
		t.Run(tc.name, func(t *testing.T) {
			report := parseTraceReport(t, tc.out)
			if report.ContentID != snap.ContentID {
				t.Errorf("ContentID = %q, want snapshot id %q", report.ContentID, snap.ContentID)
			}
			if report.Dataset != tc.dataset {
				t.Errorf("Dataset = %q, want %q", report.Dataset, tc.dataset)
			}
			if !reflect.DeepEqual(report.Sources, tc.sources) {
				t.Errorf("Sources = %+v, want %+v", report.Sources, tc.sources)
			}
		})
	}

	// The two downstreams must not share a single answer: D's path starts at
	// D, E's at E, despite both passing through M.
	if strings.Contains(outE, `"D"`) || strings.Contains(outD1, `"E"`) {
		t.Errorf("shared-intermediate reports cross-contaminated:\nD:\n%s\nE:\n%s", outD1, outE)
	}

	// A third D run after the E and R1 runs is still the original D answer,
	// byte for byte.
	if got := runTraceOK(t, snapPath, "D"); got != outD1 {
		t.Errorf("trace D drifted after other queries:\n got %s\nwant %s", got, outD1)
	}

	// Strictly read-only: same bytes, no new sibling files.
	if got := readFile(t, snapPath); got != snapBytes {
		t.Errorf("snapshot file changed after tracing:\n%s", got)
	}
	if after := dirNames(t, filepath.Dir(snapPath)); !reflect.DeepEqual(after, before) {
		t.Errorf("tracing changed directory contents: before %v, after %v", before, after)
	}
}

// TestCLITraceFailedFollowUpLeavesSuccessReportAndSnapshotUntouched pins the
// error boundary end to end: after a successful report is obtained, a later
// empty-name or unknown-name query fails by the existing rules with no
// success report on stdout, while the earlier success result still repeats
// byte-for-byte and the snapshot file stays untouched.
func TestCLITraceFailedFollowUpLeavesSuccessReportAndSnapshotUntouched(t *testing.T) {
	snapPath := snapshotForTrace(t, traceIndependenceGraphFile)
	snapBytes := readFile(t, snapPath)
	before := dirNames(t, filepath.Dir(snapPath))

	success := runTraceOK(t, snapPath, "D")
	wantD := []chainledger.SourceTrace{
		{Root: "R1", Path: []string{"D", "P", "M", "R1"}},
		{Root: "R2", Path: []string{"D", "P", "M", "R2"}},
	}
	if !reflect.DeepEqual(parseTraceReport(t, success).Sources, wantD) {
		t.Fatalf("initial report = %s, want sources %+v", success, wantD)
	}

	for _, dataset := range []string{"", "ghost"} {
		t.Run("query="+dataset, func(t *testing.T) {
			stdout, stderr, exit := captureStdout(t, func() int {
				return run([]string{"trace", snapPath, dataset})
			})
			if exit == 0 {
				t.Fatalf("trace %q succeeded, want failure", dataset)
			}
			if stdout != "" {
				t.Errorf("trace %q stdout = %q, want empty (no success report)", dataset, stdout)
			}
			if !strings.Contains(stderr, snapPath) {
				t.Errorf("trace %q stderr = %q, must name the snapshot file", dataset, stderr)
			}
			if dataset == "ghost" && !strings.Contains(stderr, "ghost") {
				t.Errorf("stderr = %q, must name the queried dataset", stderr)
			}

			// The earlier success answer still repeats verbatim.
			if got := runTraceOK(t, snapPath, "D"); got != success {
				t.Errorf("prior success report changed after failed trace %q:\n got %s\nwant %s", dataset, got, success)
			}
			// The snapshot file and directory are untouched.
			if got := readFile(t, snapPath); got != snapBytes {
				t.Errorf("snapshot file changed after failed trace %q:\n%s", dataset, got)
			}
			if after := dirNames(t, filepath.Dir(snapPath)); !reflect.DeepEqual(after, before) {
				t.Errorf("failed trace %q changed directory contents: before %v, after %v", dataset, before, after)
			}
		})
	}
}

// TestCLITraceSnapshotContentStableAcrossTracing verifies the frozen
// document itself: content identifier, parsed graph, and on-disk bytes are
// identical before and after a full sequence of traces (including failures),
// and every success report embeds the same content identifier.
func TestCLITraceSnapshotContentStableAcrossTracing(t *testing.T) {
	snapPath := snapshotForTrace(t, traceIndependenceGraphFile)
	snapBytesBefore := readFile(t, snapPath)
	parsedBefore, err := chainledger.ParseSnapshot([]byte(snapBytesBefore))
	if err != nil {
		t.Fatalf("parse before: %v", err)
	}

	queries := []string{"D", "E", "R1", "R2", "M", "ghost", ""}
	seenIDs := map[string]bool{}
	for _, dataset := range queries {
		stdout, _, exit := captureStdout(t, func() int {
			return run([]string{"trace", snapPath, dataset})
		})
		if exit == 0 {
			seenIDs[parseTraceReport(t, stdout).ContentID] = true
		}
	}

	snapBytesAfter := readFile(t, snapPath)
	if snapBytesAfter != snapBytesBefore {
		t.Errorf("snapshot bytes moved after tracing:\n got %s\nwant %s", snapBytesAfter, snapBytesBefore)
	}
	parsedAfter, err := chainledger.ParseSnapshot([]byte(snapBytesAfter))
	if err != nil {
		t.Fatalf("parse after: %v", err)
	}
	if parsedAfter.ContentID != parsedBefore.ContentID {
		t.Errorf("content id moved: %q, want %q", parsedAfter.ContentID, parsedBefore.ContentID)
	}
	if !reflect.DeepEqual(parsedAfter.Graph, parsedBefore.Graph) {
		t.Errorf("graph content moved:\n got %#v\nwant %#v", parsedAfter.Graph, parsedBefore.Graph)
	}
	if len(seenIDs) != 1 {
		t.Errorf("success reports carried %d distinct content ids, want exactly 1: %v", len(seenIDs), seenIDs)
	}
	if !seenIDs[parsedBefore.ContentID] {
		t.Errorf("reports did not carry the snapshot content id %q: %v", parsedBefore.ContentID, seenIDs)
	}
}
