//go:build linux

package main

// Regression coverage for a save-stage failure of `apply` in the lineage
// batch adjustment feature.
//
// The product promise (see README, "错误处理"): when a submitted plan is legal
// and preview shows the correct final graph, apply can still fail because the
// final graph cannot be saved back to the graph file. In that case apply must
// keep the ORIGINAL graph file byte-for-byte intact, say so on stderr with a
// non-zero exit code, and print no success report on stdout. These tests pin
// that promise at the real save boundary instead of substituting a failure the
// validation stages already cover — every case here uses a graph and a plan
// that both read fine and pass every batch rule:
//
//	original graph: A and D are roots, B depends directly on A, C on B;
//	plan:            replace ALL of B's direct upstreams with D, nothing else.
//
// Preview for those inputs succeeds and its report is checked exhaustively: B
// depends on D in the final graph while C still depends on B, only B is a
// direct change, C is the affected downstream, the relation diff is remove
// A->B / add D->B, and no dataset is added or removed. Preview never touches
// the input files.
//
// Two genuine save failures are then injected, each striking one stage of the
// atomic temp-file-plus-rename write (atomicWrite in main.go):
//
//   - the graph's directory refuses to CREATE the temp file (the directory is
//     made read-only; os.CreateTemp fails with EACCES);
//   - the new content has already been written to the temp file but the temp
//     cannot REPLACE the original: the graph file is bind-mounted over itself
//     inside a private unprivileged user+mount namespace, so rename(2) over the
//     mount point fails deterministically with EBUSY ("device or resource
//     busy"). The mount exists only inside the short-lived helper process,
//     needs no root and no real on-chain network or external service, and is
//     not a probabilistic disk fault.
//
// Both apply runs must fail the same observable way: non-zero exit, stderr
// naming the graph file and the save reason, no success report on stdout, the
// graph file preserved byte-for-byte (original JSON whitespace, field order,
// and unknown fields the reader ignores included), and the plan file untouched.
//
// A successful apply is the control: re-reading the graph file yields exactly
// the relations preview's final graph shows, and apply's report is
// byte-identical to preview's report for the same original graph and plan.
// That separates "the plan is valid" from "the adjustment was saved".

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"syscall"
	"testing"

	"github.com/asdhoaiqqq/chainledger-governance/chainledger"
)

// bindMountApplyHelperMode is the hidden argument under which the test binary
// re-enters as a subprocess inside a private user+mount namespace: it bind
// mounts the graph file over itself and then runs the real apply command, so
// the temp file is created and fully written but its rename over the graph
// path cannot replace the mount point.
const bindMountApplyHelperMode = "--chainledger-test-bind-mount-apply-helper"

// helperSetupSkipped is the helper's exit code when the private namespace or
// bind mount cannot be set up on this host. The parent treats it as a skip
// rather than a failure; every other code is the apply command's own exit
// code (0, 1, or 2).
const helperSetupSkipped = 30

// extraTestHelper is the hook TestMain calls before normal test startup, so a
// re-entered test binary can act as the bind-mount helper. On non-Linux builds
// a stub of the same name is compiled instead.
func extraTestHelper(args []string) (int, bool) {
	if len(args) >= 1 && args[0] == bindMountApplyHelperMode {
		return runBindMountApplyHelper(args[1:]), true
	}
	return 0, false
}

// runBindMountApplyHelper executes inside a fresh user+mount namespace (the
// clone and the uid/gid maps are configured by the parent). It makes the whole
// mount tree a slave so the bind mount cannot propagate anywhere, bind mounts
// the graph file onto itself, and then runs the genuine apply command
// in-process. The graph and plan are read normally; only the final rename of
// the fully written temp file onto the mounted path fails (EBUSY).
func runBindMountApplyHelper(args []string) int {
	if len(args) != 2 {
		fmt.Fprintln(os.Stderr, "usage: bind-mount-apply-helper <graph.json> <plan.json>")
		return 2
	}
	graphPath, planPath := args[0], args[1]
	if err := syscall.Mount("", "/", "", syscall.MS_SLAVE|syscall.MS_REC, ""); err != nil {
		fmt.Fprintln(os.Stderr, "bind-mount helper: cannot make the mount tree a slave:", err)
		return helperSetupSkipped
	}
	if err := syscall.Mount(graphPath, graphPath, "", syscall.MS_BIND, ""); err != nil {
		fmt.Fprintln(os.Stderr, "bind-mount helper: cannot bind-mount the graph file over itself:", err)
		return helperSetupSkipped
	}
	return run([]string{"apply", graphPath, planPath})
}

// regressionGraphBytes is deliberately NOT the canonical serialization: it has
// unusual whitespace ("datasets" :), the datasets in an order different from
// the name-byte order the writer uses (A, D, B, C), and unknown fields both at
// the top level ("topX") and inside a dataset record ("note") that the reader
// is documented to ignore. A failed save must preserve all of these bytes;
// a successful save canonicalizes them away, which is exactly why the success
// control compares relations semantically instead of the bytes.
const regressionGraphBytes = `{
  "datasets" : [
    {"name":"A","upstreams":[], "note":"root a"},
    {"name":"D","upstreams":[]},
    {"name":"B","upstreams":["A"]},
    {"name":"C","upstreams":["B"]}
  ],
  "topX" : 42
}
`

// regressionPlanBytes is the one legal change the whole regression uses: B's
// complete direct-upstream list becomes [D]; every other dataset keeps its
// current upstreams.
const regressionPlanBytes = `{"changes":[{"name":"B","upstreams":["D"]}]}`

// expectedBatchReport is preview's (and a successful apply's) exact report for
// regressionGraphBytes + regressionPlanBytes: B moves from A to D, C keeps
// depending on B, only B is a direct change and C the affected downstream.
func expectedBatchReport() chainledger.BatchReport {
	return chainledger.BatchReport{
		FinalGraph: chainledger.GraphFile{Datasets: []chainledger.GraphDataset{
			{Name: "A", Upstreams: []string{}},
			{Name: "B", Upstreams: []string{"D"}},
			{Name: "C", Upstreams: []string{"B"}},
			{Name: "D", Upstreams: []string{}},
		}},
		NewDatasets:     []string{},
		ChangedDatasets: []string{"B"},
		RemovedDatasets: []string{},
		AddedRelations:  []chainledger.Relation{{Upstream: "D", Downstream: "B"}},
		RemovedRelations: []chainledger.Relation{
			{Upstream: "A", Downstream: "B"},
		},
		AffectedDownstreams: []string{"C"},
	}
}

// writeRegressionInputs materializes the graph and plan in a fresh temporary
// directory and returns their paths and the exact bytes written.
func writeRegressionInputs(t *testing.T) (graphPath, planPath string, graphBytes, planBytes []byte) {
	t.Helper()
	dir := t.TempDir()
	graphPath = filepath.Join(dir, "graph.json")
	planPath = filepath.Join(dir, "plan.json")
	if err := os.WriteFile(graphPath, []byte(regressionGraphBytes), 0o644); err != nil {
		t.Fatalf("write graph: %v", err)
	}
	if err := os.WriteFile(planPath, []byte(regressionPlanBytes), 0o644); err != nil {
		t.Fatalf("write plan: %v", err)
	}
	return graphPath, planPath, []byte(regressionGraphBytes), []byte(regressionPlanBytes)
}

// TestCLIBatchApplySaveFailureRegression is the end-to-end regression for a
// legal adjustment whose save fails. All file and plan failures here happen at
// the SAVE stage: the same inputs preview successfully first.
func TestCLIBatchApplySaveFailureRegression(t *testing.T) {
	// Part 1: preview for the same inputs succeeds, reports exactly the
	// intended change, and never modifies either input file.
	t.Run("preview_succeeds_and_touches_no_files", func(t *testing.T) {
		graphPath, planPath, graphBytes, planBytes := writeRegressionInputs(t)

		stdout, stderr, exit := captureStdout(t, func() int {
			return run([]string{"preview", graphPath, planPath})
		})
		if exit != 0 {
			t.Fatalf("preview exit = %d, stderr = %s", exit, stderr)
		}
		var got chainledger.BatchReport
		if err := json.Unmarshal([]byte(stdout), &got); err != nil {
			t.Fatalf("preview stdout is not a JSON report: %v\n%s", err, stdout)
		}
		if !reflect.DeepEqual(got, expectedBatchReport()) {
			t.Errorf("preview report mismatch:\n got  %#v\nwant %#v", got, expectedBatchReport())
		}
		if after, err := os.ReadFile(graphPath); err != nil || !bytes.Equal(after, graphBytes) {
			t.Errorf("preview modified the graph file:\n got  %q\nwant %q", string(after), string(graphBytes))
		}
		if after, err := os.ReadFile(planPath); err != nil || !bytes.Equal(after, planBytes) {
			t.Errorf("preview modified the plan file:\n got  %q\nwant %q", string(after), string(planBytes))
		}
	})

	// Part 2, failure mode 1: the graph directory refuses to create the temp
	// file the atomic save needs.
	t.Run("apply_fails_when_directory_refuses_new_file", func(t *testing.T) {
		// Directory permission bits are bypassed by root, so the fault could
		// not be injected meaningfully under root.
		if os.Getuid() == 0 {
			t.Skip("read-only-directory fault is bypassed when running as root")
		}
		graphPath, planPath, graphBytes, planBytes := writeRegressionInputs(t)
		dir := filepath.Dir(graphPath)

		// Sanity check: the inputs themselves are still readable and valid
		// after the directory is locked down (only CREATING new entries is
		// refused), so the failure that follows is really at save time.
		if err := os.Chmod(dir, 0o555); err != nil {
			t.Fatalf("chmod directory read-only: %v", err)
		}
		defer os.Chmod(dir, 0o755) // restore before TempDir cleanup

		stdout, stderr, exit := captureStdout(t, func() int {
			return run([]string{"apply", graphPath, planPath})
		})
		assertApplySaveFailure(t, exit, stdout, stderr, graphPath, dir, graphBytes, planBytes, "permission denied", "open")
	})

	// Part 2, failure mode 2: the new graph bytes are written to the temp
	// file, but replacing the original graph file fails because it is a mount
	// point (rename EXDEV/EBUSY class of "cannot replace original" faults).
	t.Run("apply_fails_when_written_temp_cannot_replace_original", func(t *testing.T) {
		graphPath, planPath, graphBytes, planBytes := writeRegressionInputs(t)
		dir := filepath.Dir(graphPath)

		stdout, stderr, exit := runApplyInBindMountNamespace(t, graphPath, planPath)
		assertApplySaveFailure(t, exit, stdout, stderr, graphPath, dir, graphBytes, planBytes, "device or resource busy", "rename")
	})

	// Part 3: the control. A save that completes normally must be observable
	// as distinct from a merely valid plan: the graph file re-reads to the
	// previewed final graph, and apply's report equals preview's report for the
	// same original inputs.
	t.Run("successful_apply_persists_previewed_graph_and_report", func(t *testing.T) {
		previewGraphPath, previewPlanPath, _, _ := writeRegressionInputs(t)
		previewOut, previewErr, previewExit := captureStdout(t, func() int {
			return run([]string{"preview", previewGraphPath, previewPlanPath})
		})
		if previewExit != 0 {
			t.Fatalf("preview exit = %d, stderr = %s", previewExit, previewErr)
		}

		applyGraphPath, applyPlanPath, graphBytes, planBytes := writeRegressionInputs(t)
		if !bytes.Equal([]byte(regressionGraphBytes), graphBytes) || !bytes.Equal([]byte(regressionPlanBytes), planBytes) {
			t.Fatalf("apply inputs differ from preview inputs")
		}
		applyOut, applyErr, applyExit := captureStdout(t, func() int {
			return run([]string{"apply", applyGraphPath, applyPlanPath})
		})
		if applyExit != 0 {
			t.Fatalf("apply exit = %d, stderr = %s", applyExit, applyErr)
		}
		if applyOut != previewOut {
			t.Errorf("apply report != preview report for the same graph and plan:\napply:\n%s\npreview:\n%s", applyOut, previewOut)
		}

		// The saved graph must re-read to exactly preview's finalGraph
		// relations (compared as upstream SETS, independent of ordering).
		saved, err := os.ReadFile(applyGraphPath)
		if err != nil {
			t.Fatalf("read saved graph: %v", err)
		}
		savedGraph, err := chainledger.UnmarshalGraphFile(saved)
		if err != nil {
			t.Fatalf("saved graph is not a valid graph: %v", err)
		}
		if got, want := parentSets(savedGraph), reportFinalParentSets(expectedBatchReport()); !reflect.DeepEqual(got, want) {
			t.Errorf("saved graph relations = %v, want preview final graph relations %v", got, want)
		}
		if after, err := os.ReadFile(applyPlanPath); err != nil || !bytes.Equal(after, planBytes) {
			t.Errorf("apply modified the plan file:\n got  %q\nwant %q", string(after), string(planBytes))
		}
	})
}

// assertApplySaveFailure checks the user-observable result shared by both save
// failures: non-zero exit, stderr naming the graph file and the concrete save
// reason, no success report on stdout, and both input files byte-for-byte
// preserved. failedStage names the write stage that actually failed ("open"
// when the temp file could not be created, "rename" after its content was
// written and the replacement of the original failed), so this regression can
// never be satisfied by a read or validation failure instead.
func assertApplySaveFailure(t *testing.T, exit int, stdout, stderr, graphPath, dir string, graphBytes, planBytes []byte, wantReason, failedStage string) {
	t.Helper()
	if exit == 0 {
		t.Fatalf("apply exit = 0, want non-zero for the save failure; stdout = %s", stdout)
	}
	if stdout != "" {
		t.Errorf("stdout = %q, want empty: no success report may be printed when the save fails", stdout)
	}
	if !strings.Contains(stderr, graphPath) {
		t.Errorf("stderr = %q, want it to name the graph file %q", stderr, graphPath)
	}
	if !strings.Contains(stderr, "cannot write graph file") {
		t.Errorf("stderr = %q, want a graph-file save failure message", stderr)
	}
	if !strings.Contains(stderr, failedStage) {
		t.Errorf("stderr = %q, want the failure at the %q stage of the save", stderr, failedStage)
	}
	if !strings.Contains(stderr, wantReason) {
		t.Errorf("stderr = %q, want the concrete save reason %q", stderr, wantReason)
	}
	if after, err := os.ReadFile(graphPath); err != nil || !bytes.Equal(after, graphBytes) {
		t.Errorf("graph file not preserved byte-for-byte:\n got  %q\nwant %q", string(after), string(graphBytes))
	}
	planPath := filepath.Join(filepath.Dir(graphPath), "plan.json")
	if after, err := os.ReadFile(planPath); err != nil || !bytes.Equal(after, planBytes) {
		t.Errorf("plan file not preserved byte-for-byte:\n got  %q\nwant %q", string(after), string(planBytes))
	}
	assertNoLeftoverTempFiles(t, dir)
}

// runApplyInBindMountNamespace re-executes the test binary as the bind-mount
// helper in a private unprivileged user+mount namespace and returns what the
// apply command wrote to stdout/stderr plus its exit code.
func runApplyInBindMountNamespace(t *testing.T, graphPath, planPath string) (stdout, stderr string, exit int) {
	t.Helper()
	cmd := exec.Command(os.Args[0], bindMountApplyHelperMode, graphPath, planPath)
	var outBuf, errBuf bytes.Buffer
	cmd.Stdout = &outBuf
	cmd.Stderr = &errBuf
	uid, gid := os.Getuid(), os.Getgid()
	cmd.SysProcAttr = &syscall.SysProcAttr{
		// Map the unprivileged test user to uid/gid 0 inside the new user
		// namespace so the bind mount is allowed; setgroups must be denied
		// before the gid map is written (the same sequence `unshare -U` uses),
		// otherwise creating the namespace fails with EPERM on hardened
		// kernels. Nothing here affects the host mount table: the next flag
		// gives the process its own mount namespace.
		Cloneflags:                 syscall.CLONE_NEWUSER | syscall.CLONE_NEWNS,
		UidMappings:                []syscall.SysProcIDMap{{ContainerID: 0, HostID: uid, Size: 1}},
		GidMappings:                []syscall.SysProcIDMap{{ContainerID: 0, HostID: gid, Size: 1}},
		GidMappingsEnableSetgroups: false,
	}
	if err := cmd.Run(); err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			exit = exitErr.ExitCode()
		} else {
			t.Skipf("unprivileged user/mount namespace unavailable on this host, cannot inject the replace failure: %v", err)
			return "", "", 0
		}
	}
	if exit == helperSetupSkipped {
		t.Skipf("unprivileged bind-mount fault injection unavailable on this host: %s", errBuf.String())
	}
	return outBuf.String(), errBuf.String(), exit
}

// parentSets renders a graph as dataset -> set of direct upstreams, with a nil
// and an empty upstream list treated the same, so a relation comparison is
// independent of slice ordering and nil-ness.
func parentSets(graph map[string]*chainledger.Lineage) map[string]map[string]bool {
	out := make(map[string]map[string]bool, len(graph))
	for name, node := range graph {
		parents := make(map[string]bool, len(node.Parents))
		for _, p := range node.Parents {
			parents[p] = true
		}
		out[name] = parents
	}
	return out
}

// reportFinalParentSets renders a report's final graph the same way.
func reportFinalParentSets(report chainledger.BatchReport) map[string]map[string]bool {
	out := make(map[string]map[string]bool, len(report.FinalGraph.Datasets))
	for _, ds := range report.FinalGraph.Datasets {
		parents := make(map[string]bool, len(ds.Upstreams))
		for _, p := range ds.Upstreams {
			parents[p] = true
		}
		out[ds.Name] = parents
	}
	return out
}
