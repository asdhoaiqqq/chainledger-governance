package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"

	"github.com/asdhoaiqqq/chainledger-governance/chainledger"
)

const abcGraph = `{"datasets":[{"name":"A","upstreams":[]},{"name":"B","upstreams":["A"]},{"name":"C","upstreams":["B"]}]}`

// snapshotViaCLI runs the snapshot command and returns stdout, stderr, exit.
func snapshotViaCLI(t *testing.T, graphJSON string, target string) (string, string, int) {
	t.Helper()
	graphPath := writeFile(t, "graph-"+filepath.Base(target)+".json", graphJSON)
	return captureStdout(t, func() int {
		return run([]string{"snapshot", graphPath, target})
	})
}

func TestCLISnapshotCreatesAndPrintsIDAfterSuccess(t *testing.T) {
	target := filepath.Join(t.TempDir(), "snap.json")
	stdout, stderr, exit := snapshotViaCLI(t, abcGraph, target)
	if exit != 0 {
		t.Fatalf("snapshot exit = %d, stderr = %s", exit, stderr)
	}
	if !strings.HasPrefix(strings.TrimSpace(stdout), "sha256:") {
		t.Errorf("stdout = %q, want content id", stdout)
	}
	// The saved file parses as a valid snapshot whose id matches stdout.
	data := readFile(t, target)
	snap, err := chainledger.ParseSnapshot([]byte(data))
	if err != nil {
		t.Fatalf("saved snapshot invalid: %v", err)
	}
	if strings.TrimSpace(stdout) != snap.ContentID {
		t.Errorf("printed id %q != snapshot id %q", strings.TrimSpace(stdout), snap.ContentID)
	}
}

func TestCLISnapshotDoesNotModifyGraph(t *testing.T) {
	dir := t.TempDir()
	graphPath := filepath.Join(dir, "graph.json")
	if err := os.WriteFile(graphPath, []byte(abcGraph), 0o644); err != nil {
		t.Fatalf("write graph: %v", err)
	}
	target := filepath.Join(dir, "snap.json")
	_, stderr, exit := captureStdout(t, func() int {
		return run([]string{"snapshot", graphPath, target})
	})
	if exit != 0 {
		t.Fatalf("exit = %d, stderr = %s", exit, stderr)
	}
	if got := readFile(t, graphPath); got != abcGraph {
		t.Errorf("graph file changed:\n got %s\nwant %s", got, abcGraph)
	}
}

func TestCLISnapshotEmptyGraph(t *testing.T) {
	target := filepath.Join(t.TempDir(), "empty.json")
	stdout, stderr, exit := snapshotViaCLI(t, `{"datasets":[]}`, target)
	if exit != 0 {
		t.Fatalf("empty graph snapshot exit = %d, stderr = %s", exit, stderr)
	}
	if strings.TrimSpace(stdout) == "" {
		t.Error("empty graph snapshot must still print its content id")
	}
	snap, err := chainledger.ParseSnapshot([]byte(readFile(t, target)))
	if err != nil {
		t.Fatalf("parse empty snapshot: %v", err)
	}
	if len(snap.Graph.Datasets) != 0 {
		t.Errorf("empty snapshot has datasets: %v", snap.Graph.Datasets)
	}
}

func TestCLISnapshotEquivalentGraphsSameBytes(t *testing.T) {
	dir := t.TempDir()
	shuffled := `{ "datasets": [ {"upstreams":["B","B"],"name":"C"}, {"upstreams":[],"name":"A"}, {"name":"B","upstreams":["A"]} ] }`

	p1 := writeFile(t, "g1.json", abcGraph)
	p2 := writeFile(t, "g2.json", shuffled)
	t1 := filepath.Join(dir, "s1.json")
	t2 := filepath.Join(dir, "s2.json")

	_, stderr, exit := captureStdout(t, func() int { return run([]string{"snapshot", p1, t1}) })
	if exit != 0 {
		t.Fatalf("snapshot s1 exit = %d, stderr = %s", exit, stderr)
	}
	_, stderr, exit = captureStdout(t, func() int { return run([]string{"snapshot", p2, t2}) })
	if exit != 0 {
		t.Fatalf("snapshot s2 exit = %d, stderr = %s", exit, stderr)
	}
	if readFile(t, t1) != readFile(t, t2) {
		t.Errorf("equivalent graphs produced different snapshot bytes:\n%s\n%s", readFile(t, t1), readFile(t, t2))
	}
}

func TestCLISnapshotIdempotentKeepsFileBytes(t *testing.T) {
	target := filepath.Join(t.TempDir(), "snap.json")
	_, stderr, exit := snapshotViaCLI(t, abcGraph, target)
	if exit != 0 {
		t.Fatalf("first snapshot exit = %d, stderr = %s", exit, stderr)
	}
	first := readFile(t, target)
	infoBefore, err := os.Stat(target)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}

	// Re-save identical content; the file must not be rewritten.
	graphPath := writeFile(t, "g2.json", abcGraph)
	stdout, stderr, exit := captureStdout(t, func() int {
		return run([]string{"snapshot", graphPath, target})
	})
	if exit != 0 {
		t.Fatalf("idempotent snapshot exit = %d, stderr = %s", exit, stderr)
	}
	if strings.TrimSpace(stdout) == "" {
		t.Error("idempotent snapshot must still print the content id")
	}
	if got := readFile(t, target); got != first {
		t.Errorf("idempotent resave changed bytes:\n got %s\nwant %s", got, first)
	}
	infoAfter, _ := os.Stat(target)
	if !infoAfter.ModTime().Equal(infoBefore.ModTime()) {
		t.Errorf("idempotent resave rewrote the file: mtime %s -> %s", infoBefore.ModTime(), infoAfter.ModTime())
	}
}

func TestCLISnapshotRefusesDifferentContentAndPreservesTarget(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "snap.json")

	// Seed with the A<-B<-C graph.
	p1 := writeFile(t, "g1.json", abcGraph)
	if _, stderr, exit := captureStdout(t, func() int { return run([]string{"snapshot", p1, target}) }); exit != 0 {
		t.Fatalf("seed snapshot exit = %d, stderr = %s", exit, stderr)
	}
	original := readFile(t, target)

	// Attempt to save a different graph: must fail and preserve every byte.
	p2 := writeFile(t, "g2.json", `{"datasets":[{"name":"X","upstreams":[]}]}`)
	stdout, stderr, exit := captureStdout(t, func() int { return run([]string{"snapshot", p2, target}) })
	if exit == 0 {
		t.Fatalf("different-content overwrite succeeded, want failure")
	}
	if !strings.Contains(stderr, "refusing to overwrite") {
		t.Errorf("stderr = %q, want refusal", stderr)
	}
	if stdout != "" {
		t.Errorf("stdout = %q, want empty on refusal", stdout)
	}
	if got := readFile(t, target); got != original {
		t.Errorf("target changed after refusal:\n got %s\nwant %s", got, original)
	}
}

func TestCLISnapshotRefusesCorruptTarget(t *testing.T) {
	for _, corrupt := range map[string]string{
		"not json":       `garbage{`,
		"missing fields": `{}`,
		"bad version":    `{"formatVersion":9,"contentId":"x","graph":{"datasets":[]}}`,
		"bad graph":      `{"formatVersion":1,"contentId":"x","graph":{"datasets":[{"name":"A","upstreams":["ghost"]}]}}`,
	} {
		t.Run(corrupt, func(t *testing.T) {
			target := writeFile(t, "snap.json", corrupt)
			original := readFile(t, target)
			graphPath := writeFile(t, "g.json", abcGraph)
			stdout, stderr, exit := captureStdout(t, func() int { return run([]string{"snapshot", graphPath, target}) })
			if exit == 0 {
				t.Fatalf("corrupt target overwrite succeeded")
			}
			if !strings.Contains(stderr, target) || !strings.Contains(stderr, "valid snapshot") {
				t.Errorf("stderr = %q, must name file and reason", stderr)
			}
			if stdout != "" {
				t.Errorf("stdout = %q, want empty", stdout)
			}
			if got := readFile(t, target); got != original {
				t.Errorf("corrupt target was modified:\n got %s\nwant %s", got, original)
			}
		})
	}
}

func TestCLISnapshotInvalidGraphRejected(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "snap.json")
	for name, bad := range map[string]string{
		"cycle": `{"datasets":[{"name":"A","upstreams":["B"]},{"name":"B","upstreams":["A"]}]}`,
		"json":  `{bad`,
	} {
		t.Run(name, func(t *testing.T) {
			graphPath := writeFile(t, "bad.json", bad)
			_, stderr, exit := captureStdout(t, func() int { return run([]string{"snapshot", graphPath, target}) })
			if exit == 0 {
				t.Fatalf("invalid graph snapshot succeeded for %s", name)
			}
			if stderr == "" {
				t.Error("expected error on stderr")
			}
			if _, err := os.Stat(target); !os.IsNotExist(err) {
				t.Errorf("no snapshot file should be created, got %v", err)
			}
		})
	}
}

func TestCLICompareProducesReport(t *testing.T) {
	dir := t.TempDir()
	oldPath := filepath.Join(dir, "old.json")
	newPath := filepath.Join(dir, "new.json")

	pOld := writeFile(t, "oldg.json", `{"datasets":[{"name":"A","upstreams":[]},{"name":"B","upstreams":["A"]},{"name":"C","upstreams":["B"]}]}`)
	pNew := writeFile(t, "newg.json", `{"datasets":[{"name":"A","upstreams":[]},{"name":"X","upstreams":[]},{"name":"B","upstreams":["X"]},{"name":"C","upstreams":["B"]}]}`)
	if _, stderr, exit := captureStdout(t, func() int { return run([]string{"snapshot", pOld, oldPath}) }); exit != 0 {
		t.Fatalf("old snapshot: %s", stderr)
	}
	if _, stderr, exit := captureStdout(t, func() int { return run([]string{"snapshot", pNew, newPath}) }); exit != 0 {
		t.Fatalf("new snapshot: %s", stderr)
	}

	oldBytes := readFile(t, oldPath)
	newBytes := readFile(t, newPath)

	stdout, stderr, exit := captureStdout(t, func() int { return run([]string{"compare", oldPath, newPath}) })
	if exit != 0 {
		t.Fatalf("compare exit = %d, stderr = %s", exit, stderr)
	}
	var report chainledger.CompareReport
	if err := json.Unmarshal([]byte(stdout), &report); err != nil {
		t.Fatalf("compare stdout is not valid JSON: %v\n%s", err, stdout)
	}
	if got, want := report.NewDatasets, []string{"X"}; !reflect.DeepEqual(got, want) {
		t.Errorf("NewDatasets = %v, want %v", got, want)
	}
	if got, want := report.ChangedDatasets, []string{"B"}; !reflect.DeepEqual(got, want) {
		t.Errorf("ChangedDatasets = %v, want %v", got, want)
	}
	if len(report.RootSourceChanges) != 2 {
		t.Errorf("RootSourceChanges = %v, want B and C", report.RootSourceChanges)
	}

	// compare is read-only: neither snapshot changed.
	if got := readFile(t, oldPath); got != oldBytes {
		t.Errorf("compare modified old snapshot:\n%s", got)
	}
	if got := readFile(t, newPath); got != newBytes {
		t.Errorf("compare modified new snapshot:\n%s", got)
	}
}

func TestCLICompareSelfAllEmpty(t *testing.T) {
	target := filepath.Join(t.TempDir(), "self.json")
	p := writeFile(t, "g.json", abcGraph)
	if _, stderr, exit := captureStdout(t, func() int { return run([]string{"snapshot", p, target}) }); exit != 0 {
		t.Fatalf("snapshot: %s", stderr)
	}
	stdout, stderr, exit := captureStdout(t, func() int { return run([]string{"compare", target, target}) })
	if exit != 0 {
		t.Fatalf("compare self exit = %d, stderr = %s", exit, stderr)
	}
	var report chainledger.CompareReport
	if err := json.Unmarshal([]byte(stdout), &report); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}
	if len(report.NewDatasets) != 0 || len(report.RemovedDatasets) != 0 || len(report.ChangedDatasets) != 0 ||
		len(report.AddedRelations) != 0 || len(report.RemovedRelations) != 0 || len(report.RootSourceChanges) != 0 {
		t.Errorf("self compare produced non-empty diff: %+v", report)
	}
}

func TestCLICompareRejectsBadSnapshot(t *testing.T) {
	dir := t.TempDir()
	good := filepath.Join(dir, "good.json")
	bad := filepath.Join(dir, "bad.json")
	pg := writeFile(t, "g.json", abcGraph)
	if _, stderr, exit := captureStdout(t, func() int { return run([]string{"snapshot", pg, good}) }); exit != 0 {
		t.Fatalf("snapshot good: %s", stderr)
	}
	if err := os.WriteFile(bad, []byte(`{"formatVersion":1,"contentId":"x","graph":{"datasets":[]}}`), 0o644); err != nil {
		t.Fatalf("write bad: %v", err)
	}

	for _, args := range [][]string{
		{"compare", bad, good}, // invalid old
		{"compare", good, bad}, // invalid new
		{"compare", filepath.Join(dir, "missing.json"), good},
	} {
		stdout, stderr, exit := captureStdout(t, func() int { return run(args) })
		if exit == 0 {
			t.Fatalf("%v succeeded, want failure", args)
		}
		if stdout != "" {
			t.Errorf("%v wrote a success report on failure: %q", args, stdout)
		}
		if !strings.Contains(stderr, "snapshot") {
			t.Errorf("%v stderr = %q, must mention the snapshot file/problem", args, stderr)
		}
	}
}

func TestCLICompareDirectionIsOldToNew(t *testing.T) {
	dir := t.TempDir()
	onlyA := filepath.Join(dir, "a.json")
	aB := filepath.Join(dir, "ab.json")
	pA := writeFile(t, "a.json", `{"datasets":[{"name":"A","upstreams":[]}]}`)
	pAB := writeFile(t, "ab.json", `{"datasets":[{"name":"A","upstreams":[]},{"name":"B","upstreams":["A"]}]}`)
	if _, _, exit := captureStdout(t, func() int { return run([]string{"snapshot", pA, onlyA}) }); exit != 0 {
		t.Fatalf("snapshot a")
	}
	if _, _, exit := captureStdout(t, func() int { return run([]string{"snapshot", pAB, aB}) }); exit != 0 {
		t.Fatalf("snapshot ab")
	}

	stdout, _, exit := captureStdout(t, func() int { return run([]string{"compare", onlyA, aB}) })
	if exit != 0 {
		t.Fatalf("compare")
	}
	var forward chainledger.CompareReport
	if err := json.Unmarshal([]byte(stdout), &forward); err != nil {
		t.Fatalf("json: %v", err)
	}
	if !reflect.DeepEqual(forward.NewDatasets, []string{"B"}) || len(forward.RemovedDatasets) != 0 {
		t.Errorf("old->new direction wrong: new=%v removed=%v", forward.NewDatasets, forward.RemovedDatasets)
	}

	reverse, _, exit := captureStdout(t, func() int { return run([]string{"compare", aB, onlyA}) })
	if exit != 0 {
		t.Fatalf("compare reverse")
	}
	var backward chainledger.CompareReport
	if err := json.Unmarshal([]byte(reverse), &backward); err != nil {
		t.Fatalf("json: %v", err)
	}
	if !reflect.DeepEqual(backward.RemovedDatasets, []string{"B"}) || len(backward.NewDatasets) != 0 {
		t.Errorf("new->old direction wrong: new=%v removed=%v", backward.NewDatasets, backward.RemovedDatasets)
	}
}

// TestCLISnapshotConcurrentSamesContent exercises the flock-guarded
// check-then-write from within one process: every concurrent save of identical
// content succeeds, and the resulting file is a single valid snapshot.
func TestCLISnapshotConcurrentSameContent(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "snap.json")

	// Discard the per-call success lines so concurrent writes do not interleave
	// into the test's own stdout pipe.
	devNull, err := os.OpenFile(os.DevNull, os.O_WRONLY, 0)
	if err != nil {
		t.Fatalf("open devnull: %v", err)
	}
	defer devNull.Close()
	oldOut, oldErr := os.Stdout, os.Stderr
	os.Stdout, os.Stderr = devNull, devNull
	defer func() { os.Stdout, os.Stderr = oldOut, oldErr }()

	const n = 16
	graphPath := writeFile(t, "g.json", abcGraph)
	var wg sync.WaitGroup
	codes := make(chan int, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			codes <- run([]string{"snapshot", graphPath, target})
		}()
	}
	wg.Wait()
	close(codes)
	for code := range codes {
		if code != 0 {
			t.Errorf("concurrent same-content save exit = %d, want 0", code)
		}
	}
	if _, err := chainledger.ParseSnapshot([]byte(readFile(t, target))); err != nil {
		t.Errorf("target invalid after concurrent saves: %v", err)
	}
}

func TestCLIHelpMentionsSnapshotCommands(t *testing.T) {
	stdout, _, exit := captureStdout(t, func() int { return run([]string{"help"}) })
	if exit != 0 {
		t.Fatalf("help exit = %d", exit)
	}
	for _, want := range []string{"snapshot", "compare", "preview", "apply"} {
		if !strings.Contains(stdout, want) {
			t.Errorf("help missing %q:\n%s", want, stdout)
		}
	}
}

// dupFieldSnapshot produces a snapshot of graphJSON whose bytes then have one
// known field declared a second time, so the document is ambiguous even
// though the surviving values still describe the same graph.
func dupFieldSnapshot(t *testing.T, graphJSON string) (path string, content string) {
	t.Helper()
	target := filepath.Join(t.TempDir(), "snap.json")
	if _, stderr, exit := snapshotViaCLI(t, graphJSON, target); exit != 0 {
		t.Fatalf("seed snapshot: %s", stderr)
	}
	valid := readFile(t, target)
	// Drop the lock file left by the seeding save so callers can assert that
	// compare/trace create no lock of their own.
	if err := os.Remove(target + ".lock"); err != nil {
		t.Fatalf("remove seed lock: %v", err)
	}
	// Declare formatVersion twice: first 2, then the valid 1. The reader would
	// silently keep the 1 and every other check would pass.
	dup := strings.Replace(valid, `"formatVersion": 1`, `"formatVersion": 2, "formatVersion": 1`, 1)
	if dup == valid {
		t.Fatalf("could not inject duplicate field into %s", valid)
	}
	return target, dup
}

func TestCLICompareRejectsDuplicateFields(t *testing.T) {
	dir := t.TempDir()
	good := filepath.Join(dir, "good.json")
	if _, stderr, exit := snapshotViaCLI(t, abcGraph, good); exit != 0 {
		t.Fatalf("snapshot good: %s", stderr)
	}
	dupPath, dupContent := dupFieldSnapshot(t, abcGraph)
	if err := os.WriteFile(dupPath, []byte(dupContent), 0o644); err != nil {
		t.Fatalf("write dup: %v", err)
	}

	for _, tc := range []struct {
		name string
		args []string
		side string
	}{
		{"duplicate in old snapshot", []string{"compare", dupPath, good}, "old snapshot"},
		{"duplicate in new snapshot", []string{"compare", good, dupPath}, "new snapshot"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			stdout, stderr, exit := captureStdout(t, func() int { return run(tc.args) })
			if exit == 0 {
				t.Fatalf("%v succeeded, want failure", tc.args)
			}
			if stdout != "" {
				t.Errorf("stdout = %q, want empty on rejection", stdout)
			}
			if !strings.Contains(stderr, tc.side) || !strings.Contains(stderr, dupPath) {
				t.Errorf("stderr = %q, must name the %s file", stderr, tc.side)
			}
			if !strings.Contains(stderr, "formatVersion") {
				t.Errorf("stderr = %q, must name the duplicated field", stderr)
			}
			// compare stays read-only and never creates a lock file.
			if got := readFile(t, dupPath); got != dupContent {
				t.Errorf("compare modified the snapshot:\n%s", got)
			}
			if _, err := os.Stat(dupPath + ".lock"); !os.IsNotExist(err) {
				t.Errorf("compare created a lock file: %v", err)
			}
		})
	}
}

func TestCLITraceRejectsDuplicateFields(t *testing.T) {
	dupPath, dupContent := dupFieldSnapshot(t, abcGraph)
	if err := os.WriteFile(dupPath, []byte(dupContent), 0o644); err != nil {
		t.Fatalf("write dup: %v", err)
	}

	stdout, stderr, exit := captureStdout(t, func() int {
		return run([]string{"trace", dupPath, "C"})
	})
	if exit == 0 {
		t.Fatalf("trace succeeded on an ambiguous snapshot")
	}
	if stdout != "" {
		t.Errorf("stdout = %q, want empty on rejection", stdout)
	}
	if !strings.Contains(stderr, dupPath) || !strings.Contains(stderr, "formatVersion") {
		t.Errorf("stderr = %q, must name the file and the duplicated field", stderr)
	}
	if got := readFile(t, dupPath); got != dupContent {
		t.Errorf("trace modified the snapshot:\n%s", got)
	}
	if _, err := os.Stat(dupPath + ".lock"); !os.IsNotExist(err) {
		t.Errorf("trace created a lock file: %v", err)
	}
}

// TestCLISnapshotRefusesDuplicateFieldTarget: an existing target whose graph
// only resolves because a repeated field was silently collapsed counts as
// corrupt — the save is refused even when the resolved graph equals the
// current content, and the target keeps every byte and its mtime.
func TestCLISnapshotRefusesDuplicateFieldTarget(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "snap.json")

	// Seed a valid snapshot of the same content, then inject a duplicate
	// graph field: an empty graph first, the real graph second. The resolved
	// graph still matches the current content exactly.
	if _, stderr, exit := snapshotViaCLI(t, abcGraph, target); exit != 0 {
		t.Fatalf("seed snapshot: %s", stderr)
	}
	valid := readFile(t, target)
	dup := strings.Replace(valid, `"graph": {`, `"graph": {"datasets": []}, "graph": {`, 1)
	if dup == valid {
		t.Fatalf("could not inject duplicate graph into %s", valid)
	}
	if err := os.WriteFile(target, []byte(dup), 0o644); err != nil {
		t.Fatalf("write dup: %v", err)
	}
	infoBefore, err := os.Stat(target)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}

	graphPath := writeFile(t, "graph.json", abcGraph)
	stdout, stderr, exit := captureStdout(t, func() int {
		return run([]string{"snapshot", graphPath, target})
	})
	if exit == 0 {
		t.Fatalf("snapshot over a duplicate-field target succeeded")
	}
	if !strings.Contains(stderr, "refusing to overwrite") || !strings.Contains(stderr, target) {
		t.Errorf("stderr = %q, want refusal naming the target", stderr)
	}
	if stdout != "" {
		t.Errorf("stdout = %q, want no content id on refusal", stdout)
	}
	if got := readFile(t, target); got != dup {
		t.Errorf("target changed after refusal:\n got %s\nwant %s", got, dup)
	}
	infoAfter, _ := os.Stat(target)
	if !infoAfter.ModTime().Equal(infoBefore.ModTime()) {
		t.Errorf("refusal rewrote the target: mtime %s -> %s", infoBefore.ModTime(), infoAfter.ModTime())
	}
}
