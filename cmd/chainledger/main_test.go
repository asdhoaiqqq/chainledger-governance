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
