package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/asdhoaiqqq/chainledger-governance/chainledger"
)

// traceViaCLI runs the trace command and returns stdout, stderr, exit.
func traceViaCLI(t *testing.T, snapshotPath, dataset string) (string, string, int) {
	t.Helper()
	return captureStdout(t, func() int {
		return run([]string{"trace", snapshotPath, dataset})
	})
}

func TestCLITraceBasic(t *testing.T) {
	dir := t.TempDir()
	snapPath := filepath.Join(dir, "snap.json")
	g := writeFile(t, "g.json", `{"datasets":[{"name":"A","upstreams":[]},{"name":"B","upstreams":["A"]},{"name":"C","upstreams":["B"]}]}`)
	if _, stderr, exit := captureStdout(t, func() int { return run([]string{"snapshot", g, snapPath}) }); exit != 0 {
		t.Fatalf("snapshot: %s", stderr)
	}

	stdout, stderr, exit := traceViaCLI(t, snapPath, "C")
	if exit != 0 {
		t.Fatalf("trace exit = %d, stderr = %s", exit, stderr)
	}
	var report chainledger.TraceReport
	if err := json.Unmarshal([]byte(stdout), &report); err != nil {
		t.Fatalf("trace stdout is not valid JSON: %v\n%s", err, stdout)
	}
	if report.Dataset != "C" {
		t.Errorf("dataset = %q, want C", report.Dataset)
	}
	if !strings.HasPrefix(report.ContentID, "sha256:") {
		t.Errorf("contentId = %q, want sha256:...", report.ContentID)
	}
	want := []chainledger.TraceSource{{Root: "A", Path: []string{"C", "B", "A"}}}
	if !reflect.DeepEqual(report.Sources, want) {
		t.Errorf("sources = %v, want %v", report.Sources, want)
	}
}

func TestCLITraceDirectPathWins(t *testing.T) {
	dir := t.TempDir()
	snapPath := filepath.Join(dir, "snap.json")
	g := writeFile(t, "g.json", `{"datasets":[{"name":"R","upstreams":[]},{"name":"B","upstreams":["R"]},{"name":"T","upstreams":["R","B"]}]}`)
	if _, stderr, exit := captureStdout(t, func() int { return run([]string{"snapshot", g, snapPath}) }); exit != 0 {
		t.Fatalf("snapshot: %s", stderr)
	}
	stdout, _, exit := traceViaCLI(t, snapPath, "T")
	if exit != 0 {
		t.Fatalf("trace exit = %d", exit)
	}
	var report chainledger.TraceReport
	if err := json.Unmarshal([]byte(stdout), &report); err != nil {
		t.Fatalf("json: %v", err)
	}
	want := []chainledger.TraceSource{{Root: "R", Path: []string{"T", "R"}}}
	if !reflect.DeepEqual(report.Sources, want) {
		t.Errorf("sources = %v, want %v", report.Sources, want)
	}
}

func TestCLITraceTieBreakByName(t *testing.T) {
	dir := t.TempDir()
	snapPath := filepath.Join(dir, "snap.json")
	g := writeFile(t, "g.json", `{"datasets":[{"name":"R","upstreams":[]},{"name":"A","upstreams":["R"]},{"name":"B","upstreams":["R"]},{"name":"T","upstreams":["A","B"]}]}`)
	if _, stderr, exit := captureStdout(t, func() int { return run([]string{"snapshot", g, snapPath}) }); exit != 0 {
		t.Fatalf("snapshot: %s", stderr)
	}
	stdout, _, exit := traceViaCLI(t, snapPath, "T")
	if exit != 0 {
		t.Fatalf("trace exit = %d", exit)
	}
	var report chainledger.TraceReport
	if err := json.Unmarshal([]byte(stdout), &report); err != nil {
		t.Fatalf("json: %v", err)
	}
	want := []chainledger.TraceSource{{Root: "R", Path: []string{"T", "A", "R"}}}
	if !reflect.DeepEqual(report.Sources, want) {
		t.Errorf("sources = %v, want %v", report.Sources, want)
	}
}

func TestCLITraceQueryIsRoot(t *testing.T) {
	dir := t.TempDir()
	snapPath := filepath.Join(dir, "snap.json")
	g := writeFile(t, "g.json", `{"datasets":[{"name":"A","upstreams":[]},{"name":"B","upstreams":["A"]}]}`)
	if _, stderr, exit := captureStdout(t, func() int { return run([]string{"snapshot", g, snapPath}) }); exit != 0 {
		t.Fatalf("snapshot: %s", stderr)
	}
	stdout, _, exit := traceViaCLI(t, snapPath, "A")
	if exit != 0 {
		t.Fatalf("trace exit = %d", exit)
	}
	var report chainledger.TraceReport
	if err := json.Unmarshal([]byte(stdout), &report); err != nil {
		t.Fatalf("json: %v", err)
	}
	want := []chainledger.TraceSource{{Root: "A", Path: []string{"A"}}}
	if !reflect.DeepEqual(report.Sources, want) {
		t.Errorf("sources = %v, want %v", report.Sources, want)
	}
}

func TestCLITraceMissingSnapshotFile(t *testing.T) {
	stdout, stderr, exit := traceViaCLI(t, filepath.Join(t.TempDir(), "missing.json"), "A")
	if exit == 0 {
		t.Fatalf("trace succeeded, want failure")
	}
	if stdout != "" {
		t.Errorf("stdout = %q, want empty", stdout)
	}
	if !strings.Contains(stderr, "cannot read snapshot file") {
		t.Errorf("stderr = %q, want read error naming file", stderr)
	}
}

func TestCLITraceInvalidSnapshot(t *testing.T) {
	// The WHOLE snapshot must be validated: a corrupt graph anywhere rejects the
	// trace even if the queried dataset is reachable.
	for name, bad := range map[string]string{
		"invalid json":      `{not json`,
		"missing fields":    `{}`,
		"bad version":       `{"formatVersion":9,"contentId":"x","graph":{"datasets":[]}}`,
		"bad content id":    `{"formatVersion":1,"contentId":"sha256:0000","graph":{"datasets":[]}}`,
		"empty name":        `{"formatVersion":1,"contentId":"x","graph":{"datasets":[{"name":"","upstreams":[]}]}}`,
		"duplicate dataset": `{"formatVersion":1,"contentId":"x","graph":{"datasets":[{"name":"A","upstreams":[]},{"name":"A","upstreams":[]}]}}`,
		"missing upstream":  `{"formatVersion":1,"contentId":"x","graph":{"datasets":[{"name":"A","upstreams":["ghost"]}]}}`,
		"cycle":             `{"formatVersion":1,"contentId":"x","graph":{"datasets":[{"name":"A","upstreams":["B"]},{"name":"B","upstreams":["A"]}]}}`,
	} {
		t.Run(name, func(t *testing.T) {
			snapPath := writeFile(t, "bad.json", bad)
			stdout, stderr, exit := traceViaCLI(t, snapPath, "A")
			if exit == 0 {
				t.Fatalf("trace succeeded for %s, want failure", name)
			}
			if stdout != "" {
				t.Errorf("stdout = %q, want empty", stdout)
			}
			if !strings.Contains(stderr, "invalid snapshot") {
				t.Errorf("stderr = %q, want invalid snapshot error", stderr)
			}
		})
	}
}

func TestCLITraceMissingDataset(t *testing.T) {
	dir := t.TempDir()
	snapPath := filepath.Join(dir, "snap.json")
	g := writeFile(t, "g.json", `{"datasets":[{"name":"A","upstreams":[]}]}`)
	if _, stderr, exit := captureStdout(t, func() int { return run([]string{"snapshot", g, snapPath}) }); exit != 0 {
		t.Fatalf("snapshot: %s", stderr)
	}
	stdout, stderr, exit := traceViaCLI(t, snapPath, "ghost")
	if exit == 0 {
		t.Fatalf("trace succeeded, want failure")
	}
	if stdout != "" {
		t.Errorf("stdout = %q, want empty", stdout)
	}
	if !strings.Contains(stderr, "ghost") {
		t.Errorf("stderr = %q, must name the missing dataset", stderr)
	}
}

func TestCLITraceEmptyDatasetName(t *testing.T) {
	dir := t.TempDir()
	snapPath := filepath.Join(dir, "snap.json")
	g := writeFile(t, "g.json", `{"datasets":[{"name":"A","upstreams":[]}]}`)
	if _, stderr, exit := captureStdout(t, func() int { return run([]string{"snapshot", g, snapPath}) }); exit != 0 {
		t.Fatalf("snapshot: %s", stderr)
	}
	stdout, stderr, exit := traceViaCLI(t, snapPath, "")
	if exit == 0 {
		t.Fatalf("trace with empty dataset name succeeded, want failure")
	}
	if stdout != "" {
		t.Errorf("stdout = %q, want empty", stdout)
	}
	if !strings.Contains(stderr, "dataset name is required") {
		t.Errorf("stderr = %q, want empty-name error", stderr)
	}
}

func TestCLITraceWrongArgCount(t *testing.T) {
	for _, args := range [][]string{
		{"trace"},
		{"trace", "only-snapshot"},
		{"trace", "snap.json", "A", "extra"},
	} {
		stdout, stderr, exit := captureStdout(t, func() int { return run(args) })
		if exit == 0 {
			t.Fatalf("%v succeeded, want non-zero exit", args)
		}
		if stdout != "" {
			t.Errorf("%v wrote stdout on usage error: %q", args, stdout)
		}
		if !strings.Contains(stderr, "usage") {
			t.Errorf("%v stderr = %q, want usage", args, stderr)
		}
	}
}

func TestCLITraceEmptyGraph(t *testing.T) {
	dir := t.TempDir()
	snapPath := filepath.Join(dir, "snap.json")
	g := writeFile(t, "g.json", `{"datasets":[]}`)
	if _, stderr, exit := captureStdout(t, func() int { return run([]string{"snapshot", g, snapPath}) }); exit != 0 {
		t.Fatalf("snapshot: %s", stderr)
	}
	// The empty graph is legal, but there is no dataset to query.
	stdout, stderr, exit := traceViaCLI(t, snapPath, "A")
	if exit == 0 {
		t.Fatalf("trace on empty graph succeeded, want failure")
	}
	if stdout != "" {
		t.Errorf("stdout = %q, want empty", stdout)
	}
	if !strings.Contains(stderr, "A") {
		t.Errorf("stderr = %q, must name the queried dataset", stderr)
	}
}

func TestCLITraceReadOnly(t *testing.T) {
	dir := t.TempDir()
	snapPath := filepath.Join(dir, "snap.json")
	g := writeFile(t, "g.json", `{"datasets":[{"name":"A","upstreams":[]},{"name":"B","upstreams":["A"]}]}`)
	if _, stderr, exit := captureStdout(t, func() int { return run([]string{"snapshot", g, snapPath}) }); exit != 0 {
		t.Fatalf("snapshot: %s", stderr)
	}
	snapBytes := readFile(t, snapPath)

	// Record which files exist before trace (the snapshot command may have
	// left its lock file behind); trace must not create any new file.
	before := make(map[string]bool)
	if entries, err := os.ReadDir(dir); err == nil {
		for _, e := range entries {
			before[e.Name()] = true
		}
	}

	_, _, exit := traceViaCLI(t, snapPath, "B")
	if exit != 0 {
		t.Fatalf("trace exit = %d", exit)
	}
	if got := readFile(t, snapPath); got != snapBytes {
		t.Errorf("trace modified the snapshot:\n got %s\nwant %s", got, snapBytes)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read dir: %v", err)
	}
	for _, e := range entries {
		if !before[e.Name()] {
			t.Errorf("trace created an unexpected file: %s", e.Name())
		}
	}
}

func TestCLITraceIndependentOfCurrentGraph(t *testing.T) {
	// The trace must rest on the snapshot alone: after snapshotting, change the
	// current graph file, and the trace still reports the frozen version.
	dir := t.TempDir()
	snapPath := filepath.Join(dir, "snap.json")
	g := writeFile(t, "g.json", `{"datasets":[{"name":"A","upstreams":[]},{"name":"B","upstreams":["A"]}]}`)
	if _, stderr, exit := captureStdout(t, func() int { return run([]string{"snapshot", g, snapPath}) }); exit != 0 {
		t.Fatalf("snapshot: %s", stderr)
	}

	// Rewrite the current graph file with a different structure.
	if err := os.WriteFile(g, []byte(`{"datasets":[{"name":"Z","upstreams":[]},{"name":"B","upstreams":["Z"]}]}`), 0o644); err != nil {
		t.Fatalf("rewrite graph: %v", err)
	}

	stdout, _, exit := traceViaCLI(t, snapPath, "B")
	if exit != 0 {
		t.Fatalf("trace exit = %d", exit)
	}
	var report chainledger.TraceReport
	if err := json.Unmarshal([]byte(stdout), &report); err != nil {
		t.Fatalf("json: %v", err)
	}
	// The frozen snapshot still roots B at A, not Z.
	want := []chainledger.TraceSource{{Root: "A", Path: []string{"B", "A"}}}
	if !reflect.DeepEqual(report.Sources, want) {
		t.Errorf("sources = %v, want %v (must reflect the frozen snapshot, not the current graph)", report.Sources, want)
	}
}

func TestCLITraceDeterministicBytes(t *testing.T) {
	// Semantically equal snapshots (shuffled records/upstreams) and different
	// JSON whitespace must produce byte-identical trace reports.
	dir := t.TempDir()
	snap1 := filepath.Join(dir, "s1.json")
	snap2 := filepath.Join(dir, "s2.json")
	g1 := writeFile(t, "g1.json", `{"datasets":[{"name":"A","upstreams":[]},{"name":"B","upstreams":["A"]},{"name":"C","upstreams":["B","A"]}]}`)
	g2 := writeFile(t, "g2.json", `{ "datasets": [ {"upstreams":["A","B","A"],"name":"C"}, {"upstreams":[],"name":"A"}, {"name":"B","upstreams":["A"]} ] }`)
	if _, stderr, exit := captureStdout(t, func() int { return run([]string{"snapshot", g1, snap1}) }); exit != 0 {
		t.Fatalf("snapshot s1: %s", stderr)
	}
	if _, stderr, exit := captureStdout(t, func() int { return run([]string{"snapshot", g2, snap2}) }); exit != 0 {
		t.Fatalf("snapshot s2: %s", stderr)
	}
	out1, _, exit := traceViaCLI(t, snap1, "C")
	if exit != 0 {
		t.Fatalf("trace s1 exit = %d", exit)
	}
	out2, _, exit := traceViaCLI(t, snap2, "C")
	if exit != 0 {
		t.Fatalf("trace s2 exit = %d", exit)
	}
	if out1 != out2 {
		t.Errorf("trace bytes differ for semantically equal snapshots:\n%s\n%s", out1, out2)
	}
}
