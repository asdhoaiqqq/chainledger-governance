package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/asdhoaiqqq/chainledger-governance/chainledger"
)

// writeFile creates a file in the test's temp directory with the given content.
func writeFile(t *testing.T, name, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("WriteFile %q: %v", name, err)
	}
	return path
}

// readFile returns the file content.
func readFile(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile %q: %v", path, err)
	}
	return string(data)
}

// captureStdout runs fn with os.Stdout and os.Stderr redirected to pipes,
// returning what was written to each.
func captureStdout(t *testing.T, fn func() int) (stdout, stderr string, exit int) {
	t.Helper()
	oldStdout := os.Stdout
	oldStderr := os.Stderr
	rOut, wOut, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	rErr, wErr, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	os.Stdout = wOut
	os.Stderr = wErr

	exit = fn()

	wOut.Close()
	wErr.Close()
	outBytes, _ := readAll(rOut)
	errBytes, _ := readAll(rErr)
	os.Stdout = oldStdout
	os.Stderr = oldStderr
	return string(outBytes), string(errBytes), exit
}

func readAll(f *os.File) ([]byte, error) {
	var out []byte
	buf := make([]byte, 4096)
	for {
		n, err := f.Read(buf)
		out = append(out, buf[:n]...)
		if err != nil {
			break
		}
	}
	return out, nil
}

func TestCLIPreviewKeepsGraphFile(t *testing.T) {
	graphPath := writeFile(t, "graph.json", `{"datasets":[{"name":"A","upstreams":[]},{"name":"B","upstreams":["A"]}]}`)
	planPath := writeFile(t, "plan.json", `{"changes":[{"name":"A","upstreams":["B"]},{"name":"B","upstreams":[]}]}`)

	original := readFile(t, graphPath)
	stdout, stderr, exit := captureStdout(t, func() int {
		return run([]string{"preview", graphPath, planPath})
	})
	if exit != 0 {
		t.Fatalf("preview exit = %d, stderr = %s", exit, stderr)
	}
	if !strings.Contains(stdout, `"changedDatasets"`) {
		t.Errorf("preview stdout missing report: %s", stdout)
	}
	// The graph file must be byte-for-byte unchanged after preview.
	got := readFile(t, graphPath)
	if got != original {
		t.Errorf("graph file changed after preview:\n got %s\nwant %s", got, original)
	}
}

func TestCLIApplyWritesGraphBack(t *testing.T) {
	graphPath := writeFile(t, "graph.json", `{"datasets":[{"name":"A","upstreams":[]},{"name":"B","upstreams":["A"]}]}`)
	planPath := writeFile(t, "plan.json", `{"changes":[{"name":"A","upstreams":["B"]},{"name":"B","upstreams":[]}]}`)

	stdout, stderr, exit := captureStdout(t, func() int {
		return run([]string{"apply", graphPath, planPath})
	})
	if exit != 0 {
		t.Fatalf("apply exit = %d, stderr = %s", exit, stderr)
	}
	if !strings.Contains(stdout, `"finalGraph"`) {
		t.Errorf("apply stdout missing report: %s", stdout)
	}
	// The graph file must now reflect the final graph: A depends on B, B root.
	got := readFile(t, graphPath)
	if !strings.Contains(got, `"B"`) {
		t.Errorf("graph file not updated after apply: %s", got)
	}
	// Re-read and unmarshal to verify the structure is correct.
	graph, err := chainledger.UnmarshalGraphFile([]byte(got))
	if err != nil {
		t.Fatalf("written graph file is invalid: %v", err)
	}
	if got := graph["A"].Parents; len(got) != 1 || got[0] != "B" {
		t.Errorf("A.Parents = %v, want [B]", got)
	}
	if got := graph["B"].Parents; len(got) != 0 {
		t.Errorf("B.Parents = %v, want empty (root)", got)
	}
}

func TestCLIApplyRejectionPreservesGraphFile(t *testing.T) {
	graphPath := writeFile(t, "graph.json", `{"datasets":[{"name":"A","upstreams":[]},{"name":"B","upstreams":["A"]}]}`)
	// Plan that creates a cycle: A depends on B, B depends on A.
	planPath := writeFile(t, "plan.json", `{"changes":[{"name":"A","upstreams":["B"]},{"name":"B","upstreams":["A"]}]}`)

	original := readFile(t, graphPath)
	stdout, stderr, exit := captureStdout(t, func() int {
		return run([]string{"apply", graphPath, planPath})
	})
	if exit == 0 {
		t.Fatalf("apply expected non-zero exit on cycle, got 0")
	}
	if !strings.Contains(stderr, "cycle") {
		t.Errorf("stderr = %q, want cycle error", stderr)
	}
	if stdout != "" {
		t.Errorf("stdout = %q, want empty on rejection", stdout)
	}
	// The graph file must be completely preserved.
	got := readFile(t, graphPath)
	if got != original {
		t.Errorf("graph file changed after rejected apply:\n got %s\nwant %s", got, original)
	}
}

func TestCLIMissingFileReturnsError(t *testing.T) {
	planPath := writeFile(t, "plan.json", `{"changes":[]}`)
	_, stderr, exit := captureStdout(t, func() int {
		return run([]string{"preview", "/nonexistent/graph.json", planPath})
	})
	if exit == 0 {
		t.Fatalf("expected non-zero exit for missing file, got 0")
	}
	if !strings.Contains(stderr, "cannot read graph file") {
		t.Errorf("stderr = %q, want read error", stderr)
	}
}

func TestCLIInvalidJSONReturnsError(t *testing.T) {
	graphPath := writeFile(t, "graph.json", `{"datasets":[]}`)
	planPath := writeFile(t, "plan.json", `{not json`)
	_, stderr, exit := captureStdout(t, func() int {
		return run([]string{"preview", graphPath, planPath})
	})
	if exit == 0 {
		t.Fatalf("expected non-zero exit for invalid JSON, got 0")
	}
	if !strings.Contains(stderr, "invalid plan JSON") {
		t.Errorf("stderr = %q, want invalid plan JSON error", stderr)
	}
}

func TestCLIHelpAndVersion(t *testing.T) {
	for _, tc := range []struct {
		args []string
		want string
	}{
		{[]string{"help"}, "preview"},
		{[]string{"version"}, "0.2.0"},
		{[]string{"demo"}, "roots"},
	} {
		stdout, _, exit := captureStdout(t, func() int {
			return run(tc.args)
		})
		if exit != 0 {
			t.Fatalf("%v exit = %d", tc.args, exit)
		}
		if !strings.Contains(stdout, tc.want) {
			t.Errorf("%v stdout = %q, want substring %q", tc.args, stdout, tc.want)
		}
	}
}

func TestCLIUnknownCommand(t *testing.T) {
	_, stderr, exit := captureStdout(t, func() int {
		return run([]string{"frobnicate"})
	})
	if exit != 2 {
		t.Fatalf("unknown command exit = %d, want 2", exit)
	}
	if !strings.Contains(stderr, "unknown command") {
		t.Errorf("stderr = %q, want unknown command error", stderr)
	}
}

// writeGraphFile creates a graph JSON file in the test's temp directory.
func writeGraphFile(t *testing.T, name string) string {
	t.Helper()
	return writeFile(t, name, `{"datasets":[{"name":"A","upstreams":[]},{"name":"B","upstreams":["A"]},{"name":"C","upstreams":["B"]}]}`)
}

func TestCLISnapshotCreatesFile(t *testing.T) {
	graphPath := writeGraphFile(t, "graph.json")
	snapPath := filepath.Join(t.TempDir(), "snapshot.json")

	stdout, stderr, exit := captureStdout(t, func() int {
		return run([]string{"snapshot", graphPath, snapPath})
	})
	if exit != 0 {
		t.Fatalf("snapshot exit = %d, stderr = %s", exit, stderr)
	}
	// The content identifier is printed on success.
	id := strings.TrimSpace(stdout)
	if len(id) != 64 {
		t.Errorf("stdout = %q, want a 64-char hex content identifier", stdout)
	}
	// The snapshot file exists and is valid.
	data, err := os.ReadFile(snapPath)
	if err != nil {
		t.Fatalf("snapshot file not written: %v", err)
	}
	snap, err := chainledger.UnmarshalSnapshot(data)
	if err != nil {
		t.Fatalf("written snapshot is invalid: %v", err)
	}
	if snap.ContentID != id {
		t.Errorf("snapshot contentId = %q, stdout = %q", snap.ContentID, id)
	}
	// The source graph file is untouched.
	if got := readFile(t, graphPath); !strings.Contains(got, `"C"`) {
		t.Errorf("graph file modified after snapshot: %s", got)
	}
}

func TestCLISnapshotIdempotentSameContent(t *testing.T) {
	graphPath := writeGraphFile(t, "graph.json")
	snapPath := filepath.Join(t.TempDir(), "snapshot.json")

	_, stderr, exit := captureStdout(t, func() int {
		return run([]string{"snapshot", graphPath, snapPath})
	})
	if exit != 0 {
		t.Fatalf("first snapshot exit = %d, stderr = %s", exit, stderr)
	}
	first := readFile(t, snapPath)

	// Save again: same content, same bytes, success.
	stdout2, stderr2, exit2 := captureStdout(t, func() int {
		return run([]string{"snapshot", graphPath, snapPath})
	})
	if exit2 != 0 {
		t.Fatalf("second snapshot exit = %d, stderr = %s", exit2, stderr2)
	}
	if got := readFile(t, snapPath); got != first {
		t.Errorf("snapshot bytes changed on same-content save:\n got %s\nwant %s", got, first)
	}
	if strings.TrimSpace(stdout2) == "" {
		t.Errorf("stdout = %q, want content identifier", stdout2)
	}
}

func TestCLISnapshotRejectsDifferentContent(t *testing.T) {
	graphPath := writeGraphFile(t, "graph.json")
	snapPath := filepath.Join(t.TempDir(), "snapshot.json")

	// Save the first graph.
	_, stderr, exit := captureStdout(t, func() int {
		return run([]string{"snapshot", graphPath, snapPath})
	})
	if exit != 0 {
		t.Fatalf("first snapshot exit = %d, stderr = %s", exit, stderr)
	}
	original := readFile(t, snapPath)

	// Change the graph and save to the same target: must be rejected.
	graphPath2 := writeFile(t, "graph2.json", `{"datasets":[{"name":"A","upstreams":[]}]}`)
	stdout, stderr2, exit2 := captureStdout(t, func() int {
		return run([]string{"snapshot", graphPath2, snapPath})
	})
	if exit2 == 0 {
		t.Fatalf("snapshot expected non-zero exit for different content, got 0")
	}
	if !strings.Contains(stderr2, "different content or is corrupt") {
		t.Errorf("stderr = %q, want conflict error", stderr2)
	}
	if stdout != "" {
		t.Errorf("stdout = %q, want empty on rejection", stdout)
	}
	if got := readFile(t, snapPath); got != original {
		t.Errorf("snapshot file overwritten despite rejection:\n got %s\nwant %s", got, original)
	}
}

func TestCLISnapshotRejectsCorruptFile(t *testing.T) {
	graphPath := writeGraphFile(t, "graph.json")
	// A corrupt file at the target must not be overwritten.
	snapPath := writeFile(t, "snapshot.json", `{not json`)
	original := readFile(t, snapPath)

	stdout, stderr, exit := captureStdout(t, func() int {
		return run([]string{"snapshot", graphPath, snapPath})
	})
	if exit == 0 {
		t.Fatalf("snapshot expected non-zero exit for corrupt target, got 0")
	}
	if !strings.Contains(stderr, "different content or is corrupt") {
		t.Errorf("stderr = %q, want conflict error", stderr)
	}
	if stdout != "" {
		t.Errorf("stdout = %q, want empty on rejection", stdout)
	}
	if got := readFile(t, snapPath); got != original {
		t.Errorf("corrupt target overwritten despite rejection:\n got %s\nwant %s", got, original)
	}
}

func TestCLISnapshotRejectsInvalidGraph(t *testing.T) {
	graphPath := writeFile(t, "bad.json", `{"datasets":[{"name":"A","upstreams":["B"]},{"name":"B","upstreams":["A"]}]}`)
	snapPath := filepath.Join(t.TempDir(), "snapshot.json")

	stdout, stderr, exit := captureStdout(t, func() int {
		return run([]string{"snapshot", graphPath, snapPath})
	})
	if exit == 0 {
		t.Fatalf("snapshot expected non-zero exit for invalid graph, got 0")
	}
	if !strings.Contains(stderr, "invalid graph file") {
		t.Errorf("stderr = %q, want invalid graph error", stderr)
	}
	if stdout != "" {
		t.Errorf("stdout = %q, want empty on rejection", stdout)
	}
	if _, err := os.Stat(snapPath); !os.IsNotExist(err) {
		t.Errorf("snapshot file created despite invalid graph, stat err = %v", err)
	}
}

func TestCLICompareOutput(t *testing.T) {
	oldPath := writeFile(t, "old.json", `{"datasets":[{"name":"A","upstreams":[]},{"name":"B","upstreams":["A"]},{"name":"C","upstreams":["B"]}]}`)
	newPath := writeFile(t, "new.json", `{"datasets":[{"name":"X","upstreams":[]},{"name":"B","upstreams":["X"]},{"name":"C","upstreams":["B"]}]}`)
	oldSnap := filepath.Join(t.TempDir(), "old.snap.json")
	newSnap := filepath.Join(t.TempDir(), "new.snap.json")

	_, stderr, exit := captureStdout(t, func() int {
		return run([]string{"snapshot", oldPath, oldSnap})
	})
	if exit != 0 {
		t.Fatalf("snapshot old exit = %d, stderr = %s", exit, stderr)
	}
	_, stderr, exit = captureStdout(t, func() int {
		return run([]string{"snapshot", newPath, newSnap})
	})
	if exit != 0 {
		t.Fatalf("snapshot new exit = %d, stderr = %s", exit, stderr)
	}

	stdout, stderr, exit := captureStdout(t, func() int {
		return run([]string{"compare", oldSnap, newSnap})
	})
	if exit != 0 {
		t.Fatalf("compare exit = %d, stderr = %s", exit, stderr)
	}
	if !strings.Contains(stdout, `"rootSourceChanges"`) {
		t.Errorf("compare stdout missing report: %s", stdout)
	}
	if !strings.Contains(stdout, `"B"`) || !strings.Contains(stdout, `"C"`) {
		t.Errorf("compare stdout missing root source changes for B and C: %s", stdout)
	}
	if !strings.Contains(stdout, `"A"`) || !strings.Contains(stdout, `"X"`) {
		t.Errorf("compare stdout missing old/new roots A and X: %s", stdout)
	}
}

func TestCLICompareSelfIsEmpty(t *testing.T) {
	graphPath := writeGraphFile(t, "graph.json")
	snapPath := filepath.Join(t.TempDir(), "snapshot.json")
	_, stderr, exit := captureStdout(t, func() int {
		return run([]string{"snapshot", graphPath, snapPath})
	})
	if exit != 0 {
		t.Fatalf("snapshot exit = %d, stderr = %s", exit, stderr)
	}

	stdout, stderr, exit := captureStdout(t, func() int {
		return run([]string{"compare", snapPath, snapPath})
	})
	if exit != 0 {
		t.Fatalf("compare exit = %d, stderr = %s", exit, stderr)
	}
	if !strings.Contains(stdout, `"addedDatasets": []`) {
		t.Errorf("compare stdout = %s, want empty addedDatasets", stdout)
	}
	if !strings.Contains(stdout, `"rootSourceChanges": []`) {
		t.Errorf("compare stdout = %s, want empty rootSourceChanges", stdout)
	}
}

func TestCLICompareRejectsInvalidSnapshot(t *testing.T) {
	goodPath := writeGraphFile(t, "graph.json")
	goodSnap := filepath.Join(t.TempDir(), "good.snap.json")
	_, stderr, exit := captureStdout(t, func() int {
		return run([]string{"snapshot", goodPath, goodSnap})
	})
	if exit != 0 {
		t.Fatalf("snapshot exit = %d, stderr = %s", exit, stderr)
	}
	badSnap := writeFile(t, "bad.snap.json", `{"formatVersion":1,"contentId":"deadbeef","graph":{"datasets":[]}}`)

	stdout, stderr, exit := captureStdout(t, func() int {
		return run([]string{"compare", goodSnap, badSnap})
	})
	if exit == 0 {
		t.Fatalf("compare expected non-zero exit for invalid snapshot, got 0")
	}
	if !strings.Contains(stderr, "invalid snapshot file") {
		t.Errorf("stderr = %q, want invalid snapshot error", stderr)
	}
	if stdout != "" {
		t.Errorf("stdout = %q, want empty on rejection", stdout)
	}
}

func TestCLIHelpMentionsSnapshotAndCompare(t *testing.T) {
	stdout, _, exit := captureStdout(t, func() int {
		return run([]string{"help"})
	})
	if exit != 0 {
		t.Fatalf("help exit = %d", exit)
	}
	if !strings.Contains(stdout, "snapshot") {
		t.Errorf("help missing snapshot command: %s", stdout)
	}
	if !strings.Contains(stdout, "compare") {
		t.Errorf("help missing compare command: %s", stdout)
	}
}
