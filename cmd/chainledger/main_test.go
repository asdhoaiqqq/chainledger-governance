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

func TestCLIRemovalsPreviewKeepsGraphFile(t *testing.T) {
	graphPath := writeFile(t, "graph.json", `{"datasets":[{"name":"A","upstreams":[]},{"name":"B","upstreams":["A"]},{"name":"C","upstreams":["B"]}]}`)
	planPath := writeFile(t, "plan.json", `{"changes":[{"name":"D","upstreams":[]},{"name":"B","upstreams":["D"]}],"removals":["A"]}`)

	original := readFile(t, graphPath)
	stdout, stderr, exit := captureStdout(t, func() int {
		return run([]string{"preview", graphPath, planPath})
	})
	if exit != 0 {
		t.Fatalf("preview exit = %d, stderr = %s", exit, stderr)
	}
	if !strings.Contains(stdout, `"removedDatasets"`) {
		t.Errorf("preview stdout missing removedDatasets: %s", stdout)
	}
	if !strings.Contains(stdout, `"affectedDownstreams"`) {
		t.Errorf("preview stdout missing affectedDownstreams: %s", stdout)
	}
	// The graph file must be byte-for-byte unchanged after preview.
	got := readFile(t, graphPath)
	if got != original {
		t.Errorf("graph file changed after preview:\n got %s\nwant %s", got, original)
	}
}

func TestCLIRemovalsApplyWritesGraphBack(t *testing.T) {
	graphPath := writeFile(t, "graph.json", `{"datasets":[{"name":"A","upstreams":[]},{"name":"B","upstreams":["A"]},{"name":"C","upstreams":["B"]}]}`)
	planPath := writeFile(t, "plan.json", `{"changes":[{"name":"D","upstreams":[]},{"name":"B","upstreams":["D"]}],"removals":["A"]}`)

	stdout, stderr, exit := captureStdout(t, func() int {
		return run([]string{"apply", graphPath, planPath})
	})
	if exit != 0 {
		t.Fatalf("apply exit = %d, stderr = %s", exit, stderr)
	}
	if !strings.Contains(stdout, `"removedDatasets"`) {
		t.Errorf("apply stdout missing removedDatasets: %s", stdout)
	}
	// The graph file must now reflect the final graph: A gone, B depends on D.
	got := readFile(t, graphPath)
	if strings.Contains(got, `"A"`) {
		t.Errorf("A still in graph file after apply: %s", got)
	}
	if !strings.Contains(got, `"D"`) {
		t.Errorf("D missing from graph file after apply: %s", got)
	}
	// Re-read and unmarshal to verify the structure is correct.
	graph, err := chainledger.UnmarshalGraphFile([]byte(got))
	if err != nil {
		t.Fatalf("written graph file is invalid: %v", err)
	}
	if _, ok := graph["A"]; ok {
		t.Errorf("A still in graph after apply")
	}
	if got := graph["B"].Parents; len(got) != 1 || got[0] != "D" {
		t.Errorf("B.Parents = %v, want [D]", got)
	}
	if got := graph["C"].Parents; len(got) != 1 || got[0] != "B" {
		t.Errorf("C.Parents = %v, want [B]", got)
	}
}

func TestCLIRemovalsRejectionPreservesGraphFile(t *testing.T) {
	graphPath := writeFile(t, "graph.json", `{"datasets":[{"name":"A","upstreams":[]},{"name":"B","upstreams":["A"]}]}`)
	// Delete A only: B still references A, so the batch must fail.
	planPath := writeFile(t, "plan.json", `{"removals":["A"]}`)

	original := readFile(t, graphPath)
	stdout, stderr, exit := captureStdout(t, func() int {
		return run([]string{"apply", graphPath, planPath})
	})
	if exit == 0 {
		t.Fatalf("apply expected non-zero exit on dangling reference, got 0")
	}
	if !strings.Contains(stderr, "A") || !strings.Contains(stderr, "B") {
		t.Errorf("stderr = %q, want error naming both A and B", stderr)
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

func TestCLIRemovalsDeleteAll(t *testing.T) {
	graphPath := writeFile(t, "graph.json", `{"datasets":[{"name":"A","upstreams":[]},{"name":"B","upstreams":["A"]}]}`)
	planPath := writeFile(t, "plan.json", `{"removals":["A","B"]}`)

	stdout, stderr, exit := captureStdout(t, func() int {
		return run([]string{"apply", graphPath, planPath})
	})
	if exit != 0 {
		t.Fatalf("apply exit = %d, stderr = %s", exit, stderr)
	}
	got := readFile(t, graphPath)
	if !strings.Contains(got, `"datasets": []`) {
		t.Errorf("graph file = %s, want empty datasets", got)
	}
	_ = stdout
}

// TestCLIGraphFileDuplicateFieldsRejected: a graph file that declares a known
// field twice (here upstreams inside one dataset record, and datasets at the
// top level) is ambiguous, so preview, apply, and snapshot must all refuse
// it: non-zero exit, empty stdout, stderr naming the graph file and the
// repeated field. Apply must leave the graph file untouched and snapshot must
// not create its target.
func TestCLIGraphFileDuplicateFieldsRejected(t *testing.T) {
	graphs := map[string]struct {
		data  string
		field string
	}{
		"upstreams twice in record": {`{"datasets":[{"name":"B","upstreams":["A"],"upstreams":[]},{"name":"A","upstreams":[]}]}`, `"upstreams"`},
		"datasets twice":            {`{"datasets":[{"name":"A","upstreams":[]}],"datasets":[]}`, `"datasets"`},
	}
	planPath := writeFile(t, "plan.json", `{"changes":[],"removals":[]}`)

	for name, tc := range graphs {
		t.Run(name, func(t *testing.T) {
			graphPath := writeFile(t, "graph.json", tc.data)
			original := readFile(t, graphPath)
			snapPath := filepath.Join(t.TempDir(), "snap.json")

			stdout, stderr, exit := captureStdout(t, func() int {
				return run([]string{"preview", graphPath, planPath})
			})
			if exit == 0 {
				t.Fatalf("preview succeeded on duplicate-field graph, stdout = %s", stdout)
			}
			if stdout != "" {
				t.Errorf("preview stdout = %q, want empty", stdout)
			}
			if !strings.Contains(stderr, graphPath) || !strings.Contains(stderr, tc.field) {
				t.Errorf("preview stderr = %q, want it to name %q and field %s", stderr, graphPath, tc.field)
			}

			stdout, stderr, exit = captureStdout(t, func() int {
				return run([]string{"apply", graphPath, planPath})
			})
			if exit == 0 {
				t.Fatalf("apply succeeded on duplicate-field graph, stdout = %s", stdout)
			}
			if stdout != "" {
				t.Errorf("apply stdout = %q, want empty", stdout)
			}
			if !strings.Contains(stderr, graphPath) || !strings.Contains(stderr, tc.field) {
				t.Errorf("apply stderr = %q, want it to name %q and field %s", stderr, graphPath, tc.field)
			}
			if got := readFile(t, graphPath); got != original {
				t.Errorf("apply modified the rejected graph file:\n got %s\nwant %s", got, original)
			}

			stdout, stderr, exit = captureStdout(t, func() int {
				return run([]string{"snapshot", graphPath, snapPath})
			})
			if exit == 0 {
				t.Fatalf("snapshot succeeded on duplicate-field graph, stdout = %s", stdout)
			}
			if stdout != "" {
				t.Errorf("snapshot stdout = %q, want empty", stdout)
			}
			if !strings.Contains(stderr, graphPath) || !strings.Contains(stderr, tc.field) {
				t.Errorf("snapshot stderr = %q, want it to name %q and field %s", stderr, graphPath, tc.field)
			}
			if _, err := os.Stat(snapPath); !os.IsNotExist(err) {
				t.Errorf("snapshot target must not be created for a rejected graph, stat err = %v", err)
			}
		})
	}
}
