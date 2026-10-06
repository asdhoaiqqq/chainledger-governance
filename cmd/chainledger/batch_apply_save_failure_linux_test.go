//go:build linux

package main

// Regression coverage for the graph-file write-back contract of a lineage
// batch adjustment.
//
// A valid, accepted plan does not by itself mean the adjustment is committed:
// apply must still save the final graph back to the graph file. The product
// promises that a failure at the SAVE stage — after the graph and plan were
// read and the adjustment fully validated —
//
//   - returns a non-zero process exit code,
//   - reports the graph file and the save failure on stderr,
//   - prints no success report on stdout,
//   - leaves the original graph file byte-for-byte intact (its JSON
//     whitespace, field order, and unknown fields the reader ignores),
//   - leaves the plan file intact,
//
// because the save goes through a temp file plus an atomic rename. The
// matching control proves a successful save really commits: the graph file
// re-reads to the same relations the preview showed, and the apply report is
// byte-identical to the preview report for the same inputs. That keeps "the
// plan is valid" and "the adjustment was saved" as two distinguishable,
// observable outcomes.
//
// Both save faults are provoked deterministically offline:
//
//   - temp-file creation denied: the graph's directory is chmod 0500, so the
//     files remain readable (validation still passes) but creating the temp
//     file fails with EACCES;
//   - final rename denied: the command runs in a private user+mount namespace
//     with the graph file bind-mounted onto itself, so the temp file is fully
//     written and only the rename-over-original fails with EBUSY (see
//     save_fault_helper_linux_test.go).
//
// No chain network, external service, real disk fault, or product seam is
// used; command arguments, the report structure, and the lineage rules are
// unchanged.

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/asdhoaiqqq/chainledger-governance/chainledger"
)

// The adjustment used throughout this regression. The original graph has two
// roots, A and D: B depends directly on A and C depends directly on B. The
// plan replaces all of B's direct upstreams with D; everything else is kept.
//
// The graph bytes deliberately carry details a successful save would rewrite
// or drop: non-canonical whitespace/indentation, an "upstreams" field written
// before "name" in B's record, an unknown top-level field with a nested
// object, and an unknown field inside B's record (both ignored by the reader).
// A failed save must preserve all of these byte-for-byte.
const saveFaultGraphJSON = `{
  "_provenance": {"exportedBy": "nightly-job", "retentionDays": 30},
  "datasets": [
    {"name": "A", "upstreams": []},
    {"upstreams": ["A"], "name": "B", "_qualityNote": "stable since v3"},
    {"name": "C", "upstreams": ["B"]},
    {"name": "D", "upstreams": []}
  ]
}`

const saveFaultPlanJSON = `{"changes":[{"name":"B","upstreams":["D"]}]}`

// saveFaultInputs writes a fresh graph and plan file in a new temp directory
// and returns the directory and both paths.
func saveFaultInputs(t *testing.T) (dir, graphPath, planPath string) {
	t.Helper()
	dir = t.TempDir()
	graphPath, _ = writeRoundInput(t, dir, "graph.json", saveFaultGraphJSON)
	planPath, _ = writeRoundInput(t, dir, "plan.json", saveFaultPlanJSON)
	return dir, graphPath, planPath
}

// runBuiltCommand runs the freshly built command binary with argv and returns
// its stdout, stderr, and real process exit code.
func runBuiltCommand(t *testing.T, argv ...string) (stdout, stderr string, exitCode int) {
	t.Helper()
	cmd := exec.Command(snapshotCLIBin, argv...)
	var outBuf, errBuf bytes.Buffer
	cmd.Stdout, cmd.Stderr = &outBuf, &errBuf
	if err := cmd.Run(); err != nil {
		if cmd.ProcessState == nil {
			t.Fatalf("command %v failed to run: %v", argv, err)
		}
	}
	return outBuf.String(), errBuf.String(), cmd.ProcessState.ExitCode()
}

// rerootGoldenReport computes, purely in memory, the report preview/apply must
// produce for saveFaultGraphJSON + saveFaultPlanJSON. Building it through the
// library instead of hardcoding JSON also proves the inputs really parse and
// validate.
func rerootGoldenReport(t *testing.T) *chainledger.BatchReport {
	t.Helper()
	graph, err := chainledger.UnmarshalGraphFile([]byte(saveFaultGraphJSON))
	if err != nil {
		t.Fatalf("test graph must be valid: %v", err)
	}
	plan, err := chainledger.UnmarshalPlan([]byte(saveFaultPlanJSON))
	if err != nil {
		t.Fatalf("test plan must be valid: %v", err)
	}
	report, err := chainledger.PreviewBatch(graph, plan)
	if err != nil {
		t.Fatalf("preview of the test adjustment must succeed: %v", err)
	}
	return report
}

// assertRerootReportShape pins down, in human terms, what the one adjustment
// does: B is the only directly changed dataset, C its affected downstream,
// the only relation changes are A->B removed and D->B added, no dataset is
// added or removed, and the final graph has B depending on D while C still
// depends on B.
func assertRerootReportShape(t *testing.T, report *chainledger.BatchReport) {
	t.Helper()
	if got := report.NewDatasets; !reflect.DeepEqual(got, []string{}) {
		t.Errorf("newDatasets = %v, want []", got)
	}
	if got := report.ChangedDatasets; !reflect.DeepEqual(got, []string{"B"}) {
		t.Errorf("changedDatasets = %v, want [B]", got)
	}
	if got := report.RemovedDatasets; !reflect.DeepEqual(got, []string{}) {
		t.Errorf("removedDatasets = %v, want []", got)
	}
	wantAdded := []chainledger.Relation{{Upstream: "D", Downstream: "B"}}
	if got := report.AddedRelations; !reflect.DeepEqual(got, wantAdded) {
		t.Errorf("addedRelations = %v, want %v", got, wantAdded)
	}
	wantRemoved := []chainledger.Relation{{Upstream: "A", Downstream: "B"}}
	if got := report.RemovedRelations; !reflect.DeepEqual(got, wantRemoved) {
		t.Errorf("removedRelations = %v, want %v", got, wantRemoved)
	}
	if got := report.AffectedDownstreams; !reflect.DeepEqual(got, []string{"C"}) {
		t.Errorf("affectedDownstreams = %v, want [C]", got)
	}
	wantParents := map[string][]string{
		"A": {},
		"B": {"D"},
		"C": {"B"},
		"D": {},
	}
	gotParents := make(map[string][]string, len(report.FinalGraph.Datasets))
	for _, ds := range report.FinalGraph.Datasets {
		gotParents[ds.Name] = ds.Upstreams
	}
	if !reflect.DeepEqual(gotParents, wantParents) {
		t.Errorf("final graph relations = %v, want %v", gotParents, wantParents)
	}
}

// decodeBatchReport parses one printed report.
func decodeBatchReport(t *testing.T, raw string) chainledger.BatchReport {
	t.Helper()
	var report chainledger.BatchReport
	if err := json.Unmarshal([]byte(raw), &report); err != nil {
		t.Fatalf("stdout is not a valid batch report:\n%s\nerr: %v", raw, err)
	}
	return report
}

// assertInputsPreserved fails unless both files still hold their exact
// original bytes, and no atomic-write temp file was left behind.
func assertInputsPreserved(t *testing.T, dir, graphPath, planPath string) {
	t.Helper()
	if got, err := os.ReadFile(graphPath); err != nil || !bytes.Equal(got, []byte(saveFaultGraphJSON)) {
		t.Errorf("graph file not preserved byte-for-byte:\n got %q\nwant %q (err=%v)", got, saveFaultGraphJSON, err)
	}
	if got, err := os.ReadFile(planPath); err != nil || !bytes.Equal(got, []byte(saveFaultPlanJSON)) {
		t.Errorf("plan file changed after apply:\n got %q\nwant %q (err=%v)", got, saveFaultPlanJSON, err)
	}
	assertNoLeftoverTempFiles(t, dir)
}

// TestCLIBatchRerootPreviewReport pins the adjustment itself: preview of the
// same inputs succeeds with B rerooted onto D, C still on B, only B directly
// changed, C the sole affected downstream, and just the A->B / D->B relation
// changes. Preview must never alter either input file.
func TestCLIBatchRerootPreviewReport(t *testing.T) {
	golden := rerootGoldenReport(t)
	assertRerootReportShape(t, golden)

	_, graphPath, planPath := saveFaultInputs(t)
	dir := filepath.Dir(graphPath)
	stdout, stderr, exitCode := runBuiltCommand(t, "preview", graphPath, planPath)
	if exitCode != 0 {
		t.Fatalf("preview exit = %d, stderr = %s", exitCode, stderr)
	}
	printed := decodeBatchReport(t, stdout)
	if !reflect.DeepEqual(&printed, golden) {
		t.Errorf("printed preview report does not match the computed report:\n got %+v\nwant %+v", printed, golden)
	}
	// Preview is strictly read-only: both files keep their exact bytes.
	assertInputsPreserved(t, dir, graphPath, planPath)
}

// TestCLIBatchApplyTempFileCreationFailure covers the first save fault: the
// graph and plan are readable and the batch validates (preview on the same
// files succeeds first), but the graph's directory refuses to create the temp
// file. Apply must fail cleanly and preserve the original graph bytes.
func TestCLIBatchApplyTempFileCreationFailure(t *testing.T) {
	dir, graphPath, planPath := saveFaultInputs(t)

	// Establish the precondition: files readable and the adjustment valid.
	if _, stderr, code := runBuiltCommand(t, "preview", graphPath, planPath); code != 0 {
		t.Fatalf("preview must succeed before the save fault is injected: %s", stderr)
	}

	// Drop write permission on the directory only: files stay readable (the
	// command still validates the batch), but CreateTemp in this directory is
	// refused with EACCES. Restore it afterwards so temp-dir cleanup works.
	if err := os.Chmod(dir, 0o500); err != nil {
		t.Fatalf("chmod graph directory: %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o700) })

	stdout, stderr, exitCode := runBuiltCommand(t, "apply", graphPath, planPath)
	if exitCode == 0 {
		t.Fatalf("apply exit = 0, want non-zero when the temp file cannot be created")
	}
	if stdout != "" {
		t.Errorf("stdout = %q, want no success report on save failure", stdout)
	}
	if !strings.Contains(stderr, graphPath) {
		t.Errorf("stderr = %q, want it to name the graph file %q", stderr, graphPath)
	}
	if !strings.Contains(stderr, "cannot write graph file") {
		t.Errorf("stderr = %q, want it to report the graph-file save failure", stderr)
	}
	if !strings.Contains(stderr, "permission denied") {
		t.Errorf("stderr = %q, want the save-failure reason (permission denied)", stderr)
	}

	_ = os.Chmod(dir, 0o700)
	assertInputsPreserved(t, dir, graphPath, planPath)
}

// TestCLIBatchApplyRenameFailure covers the second save fault: inside a
// private user+mount namespace the graph file is bind-mounted onto itself, so
// the temp file is created and fully written but the final rename over the
// original fails with EBUSY. Apply must fail cleanly and preserve the
// original graph bytes.
func TestCLIBatchApplyRenameFailure(t *testing.T) {
	dir, graphPath, planPath := saveFaultInputs(t)

	// Establish the precondition on the host: files readable, batch valid.
	if _, stderr, code := runBuiltCommand(t, "preview", graphPath, planPath); code != 0 {
		t.Fatalf("preview must succeed before the save fault is injected: %s", stderr)
	}

	stdout, stderr, exitCode, startErr := runInMountNamespace(
		t, snapshotCLIBin, []string{"apply", graphPath, planPath}, graphPath)
	if startErr != nil {
		t.Skipf("unprivileged user+mount namespaces unavailable in this environment: %v", startErr)
	}
	// Helper setup failures (cannot privatize mounts / cannot bind-mount) are
	// environment limits, not product behavior; skip rather than fail.
	switch exitCode {
	case 10, 11:
		t.Skipf("mount-namespace fault setup unavailable (helper exit %d): %s", exitCode, stderr)
	case 12:
		t.Fatalf("fault helper could not exec the command: %s", stderr)
	}
	if exitCode == 0 {
		t.Fatalf("apply exit = 0, want non-zero when the rename cannot replace the graph file")
	}
	if stdout != "" {
		t.Errorf("stdout = %q, want no success report on save failure", stdout)
	}
	if !strings.Contains(stderr, graphPath) {
		t.Errorf("stderr = %q, want it to name the graph file %q", stderr, graphPath)
	}
	if !strings.Contains(stderr, "cannot write graph file") {
		t.Errorf("stderr = %q, want it to report the graph-file save failure", stderr)
	}
	if !strings.Contains(stderr, "device or resource busy") {
		t.Errorf("stderr = %q, want the save-failure reason (device or resource busy)", stderr)
	}

	assertInputsPreserved(t, dir, graphPath, planPath)
}

// TestCLIBatchApplySuccessMatchesPreview is the control the two failures are
// judged against: with the save healthy, apply commits. Re-reading the graph
// file yields exactly the relations the preview's final graph showed, and the
// apply report is byte-identical to the preview report for the same original
// graph and plan.
func TestCLIBatchApplySuccessMatchesPreview(t *testing.T) {
	golden := rerootGoldenReport(t)

	// Preview against one copy of the inputs.
	_, previewGraph, previewPlan := saveFaultInputs(t)
	previewOut, stderr, code := runBuiltCommand(t, "preview", previewGraph, previewPlan)
	if code != 0 {
		t.Fatalf("preview exit = %d, stderr = %s", code, stderr)
	}
	if !reflect.DeepEqual(decodeBatchReport(t, previewOut), *golden) {
		t.Errorf("preview report does not match the golden reroot report")
	}

	// Apply against an identical copy of the same original graph and plan.
	_, graphPath, planPath := saveFaultInputs(t)
	applyOut, stderr, exitCode := runBuiltCommand(t, "apply", graphPath, planPath)
	if exitCode != 0 {
		t.Fatalf("apply exit = %d, stderr = %s", exitCode, stderr)
	}
	if applyOut != previewOut {
		t.Errorf("apply report differs from the preview report for the same inputs:\napply:\n%s\npreview:\n%s",
			applyOut, previewOut)
	}
	if !reflect.DeepEqual(decodeBatchReport(t, applyOut), *golden) {
		t.Errorf("apply report does not match the golden reroot report")
	}

	// The adjustment was actually SAVED: re-reading the graph gives the same
	// relations as the preview's (and report's) final graph.
	savedBytes, err := os.ReadFile(graphPath)
	if err != nil {
		t.Fatalf("read saved graph: %v", err)
	}
	if bytes.Equal(savedBytes, []byte(saveFaultGraphJSON)) {
		t.Errorf("graph file still holds the original bytes after a successful apply")
	}
	savedGraph, err := chainledger.UnmarshalGraphFile(savedBytes)
	if err != nil {
		t.Fatalf("saved graph file is not a valid graph: %v", err)
	}
	if len(savedGraph) != len(golden.FinalGraph.Datasets) {
		t.Errorf("saved graph has %d datasets, want %d", len(savedGraph), len(golden.FinalGraph.Datasets))
	}
	// The saved file must be exactly the canonical encoding of the preview's
	// final graph (same sorting, dedup, and empty-list rules), so "valid plan"
	// and "committed adjustment" are shown as distinct outcomes.
	finalGraphJSON, err := json.Marshal(golden.FinalGraph)
	if err != nil {
		t.Fatalf("marshal preview final graph: %v", err)
	}
	finalGraph, err := chainledger.UnmarshalGraphFile(finalGraphJSON)
	if err != nil {
		t.Fatalf("parse preview final graph: %v", err)
	}
	wantSavedBytes, err := chainledger.MarshalGraphFile(finalGraph)
	if err != nil {
		t.Fatalf("canonical-encode preview final graph: %v", err)
	}
	if !bytes.Equal(savedBytes, wantSavedBytes) {
		t.Errorf("saved graph file is not the canonical preview final graph:\n got %s\nwant %s",
			savedBytes, wantSavedBytes)
	}
	// The plan file is never an output of apply.
	if got, err := os.ReadFile(planPath); err != nil || !bytes.Equal(got, []byte(saveFaultPlanJSON)) {
		t.Errorf("plan file changed after apply: %q (err=%v)", got, err)
	}
}
