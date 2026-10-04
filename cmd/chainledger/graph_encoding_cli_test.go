package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// These tests pin the CLI behavior for a graph whose raw name literals are
// corrupt (invalid UTF-8 bytes or unpaired \u surrogate escapes), in both
// places a graph is read: a standalone graph file (preview, apply, snapshot)
// and the graph embedded in a snapshot (compare, trace, and the save-time
// re-validation of an existing target). Every path must fail with a non-zero
// exit code and empty stdout before any report is printed, leave the source
// graph and any existing target byte-for-byte untouched, create no snapshot
// and no lock file, and name on stderr the file, the field (name versus
// upstreams), the zero-based record (and upstream array) position, and which
// corruption occurred.

// corruptGraphCases covers every corrupt spelling in every graph-name
// position, each with the field and position the error must locate.
var corruptGraphCases = map[string]struct {
	graph    string
	field    string
	location string
	reason   string
}{
	"invalid bytes in first record name": {
		`{"datasets":[{"name":"source` + "\xff" + `","upstreams":[]}]}`,
		`"name"`, `dataset record at index 0`, "invalid UTF-8 bytes",
	},
	"invalid bytes in second record name": {
		`{"datasets":[{"name":"A","upstreams":[]},{"name":"b` + "\xfe" + `","upstreams":[]}]}`,
		`"name"`, `dataset record at index 1`, "invalid UTF-8 bytes",
	},
	"invalid bytes in upstream entry": {
		`{"datasets":[{"name":"A","upstreams":[]},{"name":"B","upstreams":["A","g` + "\xfe" + `"]}]}`,
		`"upstreams"`, `index 1 of the dataset record at index 1`, "invalid UTF-8 bytes",
	},
	"surrogate in first record name": {
		`{"datasets":[{"name":"a\ud800","upstreams":[]}]}`,
		`"name"`, `dataset record at index 0`, "unpaired surrogate",
	},
	"lone low surrogate in record name": {
		`{"datasets":[{"name":"a\udc00","upstreams":[]}]}`,
		`"name"`, `dataset record at index 0`, "unpaired surrogate",
	},
	"low-before-high surrogate in record name": {
		`{"datasets":[{"name":"a\udc00\ud800","upstreams":[]}]}`,
		`"name"`, `dataset record at index 0`, "unpaired surrogate",
	},
	"surrogate in upstream entry": {
		`{"datasets":[{"name":"A","upstreams":[]},{"name":"B","upstreams":["A","x\ud800"]}]}`,
		`"upstreams"`, `index 1 of the dataset record at index 1`, "unpaired surrogate",
	},
	"corrupt upstream colliding with a genuine root": {
		`{"datasets":[{"name":"源�","upstreams":[]},{"name":"down","upstreams":["源\ud800"]}]}`,
		`"upstreams"`, `index 0 of the dataset record at index 1`, "unpaired surrogate",
	},
	"corrupt name with nothing to collide with": {
		`{"datasets":[{"name":"only` + "\xff" + `","upstreams":[]}]}`,
		`"name"`, `dataset record at index 0`, "invalid UTF-8 bytes",
	},
}

// TestCLIBatchAndSnapshotRejectCorruptGraph runs every corrupt standalone
// graph through preview, apply, and snapshot and requires the same rejection
// from all three.
func TestCLIBatchAndSnapshotRejectCorruptGraph(t *testing.T) {
	planPath := writeFile(t, "plan.json", `{"changes":[],"removals":[]}`)
	for name, tc := range corruptGraphCases {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			graphPath := writeFile(t, "graph.json", tc.graph)
			graphBefore := readFile(t, graphPath)
			snapPath := filepath.Join(dir, "out.json")

			check := func(verb string, args []string) {
				t.Helper()
				stdout, stderr, exit := captureStdout(t, func() int {
					return run(append([]string{verb}, args...))
				})
				if exit == 0 {
					t.Fatalf("%s exit = 0, want non-zero; stdout = %s", verb, stdout)
				}
				if stdout != "" {
					t.Errorf("%s stdout = %q, want empty on rejection", verb, stdout)
				}
				if !strings.Contains(stderr, graphPath) {
					t.Errorf("%s stderr = %q, want it to name the graph file %q", verb, stderr, graphPath)
				}
				for _, want := range []string{tc.field, tc.location, tc.reason} {
					if !strings.Contains(stderr, want) {
						t.Errorf("%s stderr = %q, want it to contain %q", verb, stderr, want)
					}
				}
				if got := readFile(t, graphPath); got != graphBefore {
					t.Errorf("graph file changed after rejected %s:\n got %s\nwant %s", verb, got, graphBefore)
				}
				if _, err := os.Stat(snapPath); !os.IsNotExist(err) {
					t.Errorf("%s must not create a snapshot, stat err = %v", verb, err)
				}
				if _, err := os.Stat(snapPath + ".lock"); !os.IsNotExist(err) {
					t.Errorf("%s must not leave a lock file: %v", verb, err)
				}
			}

			check("preview", []string{graphPath, planPath})
			check("apply", []string{graphPath, planPath})
			check("snapshot", []string{graphPath, snapPath})
		})
	}
}

// TestCLIApplyCorruptGraphKeepsLegalChangesUnapplied: a valid plan that
// would change a legal record must still take no effect when the source graph
// itself carries a corrupt name — the graph is never read successfully.
func TestCLIApplyCorruptGraphKeepsLegalChangesUnapplied(t *testing.T) {
	graphContent := `{"datasets":[{"name":"A","upstreams":[]},{"name":"B","upstreams":["A` + "\xff" + `"]}]}`
	graphPath := writeFile(t, "graph.json", graphContent)
	planPath := writeFile(t, "plan.json", `{"changes":[{"name":"A","upstreams":[]}]}`)

	stdout, stderr, exit := captureStdout(t, func() int {
		return run([]string{"apply", graphPath, planPath})
	})
	if exit == 0 {
		t.Fatalf("apply exit = 0, want non-zero; stdout = %s", stdout)
	}
	if stdout != "" {
		t.Errorf("stdout = %q, want empty", stdout)
	}
	if !strings.Contains(stderr, "invalid UTF-8 bytes") || !strings.Contains(stderr, `dataset record at index 1`) {
		t.Errorf("stderr = %q, want the corrupt-upstream reason and record 1", stderr)
	}
	if got := readFile(t, graphPath); got != graphContent {
		t.Errorf("graph file changed after rejected apply:\n got %s\nwant %s", got, graphContent)
	}
}

// replacedGraphJSON is the valid graph the decoder silently produces when a
// corrupt upstream ("源" plus one unreadable unit) is rewritten to "源�":
// root 源� with dataset down depending on it.
const replacedGraphJSON = `{"datasets":[{"name":"down","upstreams":["源�"]},{"name":"源�","upstreams":[]}]}`

// corruptNameSnapshot seeds a valid snapshot of the replaced graph, then
// corrupts the upstream reference in record 0 ("源�" -> 源 plus repl, where
// repl is either a lone surrogate escape or an invalid byte). The declared
// content id is left untouched and still matches the replaced graph, so only
// the raw-name check can reject the file. It returns the path and the exact
// bytes written, which the caller must write to disk.
func corruptNameSnapshot(t *testing.T, repl string) (path, content string) {
	t.Helper()
	dir := t.TempDir()
	graphPath := writeFile(t, "graph.json", replacedGraphJSON)
	target := filepath.Join(dir, "snap.json")
	if _, stderr, exit := captureStdout(t, func() int {
		return run([]string{"snapshot", graphPath, target})
	}); exit != 0 {
		t.Fatalf("seed snapshot exit = %d, stderr = %s", exit, stderr)
	}
	valid := readFile(t, target)
	if err := os.Remove(target + ".lock"); err != nil {
		t.Fatalf("remove seed lock: %v", err)
	}
	// The first occurrence of "源�" is inside record 0's upstreams (records
	// are sorted by name, so "down" precedes "源�").
	needle := "源" + "�"
	idx := strings.Index(valid, needle)
	if idx < 0 {
		t.Fatalf("seeded snapshot does not contain %q: %s", needle, valid)
	}
	content = valid[:idx] + "源" + repl + valid[idx+len(needle):]
	return target, content
}

// TestCLICompareAndTraceRejectCorruptSnapshot: compare (in both directions)
// and trace must fail before producing any report when a snapshot's embedded
// graph carries a corrupt name — even though its declared content id matches
// the replaced graph. The snapshot bytes stay untouched and no lock file is
// created.
func TestCLICompareAndTraceRejectCorruptSnapshot(t *testing.T) {
	variants := map[string]struct {
		repl   string
		reason string
	}{
		"lone high surrogate": {`\ud800`, "unpaired surrogate"},
		"lone low surrogate":  {`\udc00`, "unpaired surrogate"},
		"low before high":     {`\udc00\ud800`, "unpaired surrogate"},
		"invalid byte":        {"\xff", "invalid UTF-8 bytes"},
	}
	for name, tc := range variants {
		t.Run(name, func(t *testing.T) {
			badPath, badContent := corruptNameSnapshot(t, tc.repl)
			if err := os.WriteFile(badPath, []byte(badContent), 0o644); err != nil {
				t.Fatalf("write corrupt snapshot: %v", err)
			}
			goodPath := filepath.Join(t.TempDir(), "good.json")
			goodGraph := writeFile(t, "goodgraph.json", replacedGraphJSON)
			if _, stderr, exit := captureStdout(t, func() int {
				return run([]string{"snapshot", goodGraph, goodPath})
			}); exit != 0 {
				t.Fatalf("seed good snapshot: %s", stderr)
			}
			if err := os.Remove(goodPath + ".lock"); err != nil {
				t.Fatalf("remove good lock: %v", err)
			}

			commands := [][]string{
				{"compare", badPath, goodPath},
				{"compare", goodPath, badPath},
				{"trace", badPath, "down"},
			}
			for _, args := range commands {
				cmd := strings.Join(args[:2], " ")
				stdout, stderr, exit := captureStdout(t, func() int { return run(args) })
				if exit == 0 {
					t.Fatalf("%s succeeded on a corrupt-name snapshot; stdout = %s", cmd, stdout)
				}
				if stdout != "" {
					t.Errorf("%s stdout = %q, want empty on rejection", cmd, stdout)
				}
				for _, want := range []string{badPath, `"upstreams"`, `index 0 of the dataset record at index 0`, tc.reason, "embedded in the snapshot"} {
					if !strings.Contains(stderr, want) {
						t.Errorf("%s stderr = %q, want it to contain %q", cmd, stderr, want)
					}
				}
				if got := readFile(t, badPath); got != badContent {
					t.Errorf("%s modified the corrupt snapshot:\n got %s\nwant %s", cmd, got, badContent)
				}
				if _, err := os.Stat(badPath + ".lock"); !os.IsNotExist(err) {
					t.Errorf("%s created a lock file: %v", cmd, err)
				}
			}
		})
	}
}

// TestCLISnapshotRefusesCorruptNameTarget: saving over an existing target
// whose embedded graph has a corrupt name is refused even when the current
// graph is exactly the replaced graph (so the content ids would match). The
// target keeps every byte and no content id is printed.
func TestCLISnapshotRefusesCorruptNameTarget(t *testing.T) {
	target, corrupt := corruptNameSnapshot(t, `\ud800`)
	if err := os.WriteFile(target, []byte(corrupt), 0o644); err != nil {
		t.Fatalf("write corrupt target: %v", err)
	}
	infoBefore, err := os.Stat(target)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	graphPath := writeFile(t, "current.json", replacedGraphJSON)

	stdout, stderr, exit := captureStdout(t, func() int {
		return run([]string{"snapshot", graphPath, target})
	})
	if exit == 0 {
		t.Fatalf("snapshot over a corrupt-name target succeeded; stdout = %s", stdout)
	}
	if stdout != "" {
		t.Errorf("stdout = %q, want no content id on refusal", stdout)
	}
	if !strings.Contains(stderr, "refusing to overwrite") || !strings.Contains(stderr, target) {
		t.Errorf("stderr = %q, want a refusal naming the target", stderr)
	}
	if !strings.Contains(stderr, "unpaired surrogate") {
		t.Errorf("stderr = %q, want the encoding reason", stderr)
	}
	if got := readFile(t, target); got != corrupt {
		t.Errorf("target changed after refusal:\n got %s\nwant %s", got, corrupt)
	}
	infoAfter, _ := os.Stat(target)
	if !infoAfter.ModTime().Equal(infoBefore.ModTime()) {
		t.Errorf("refusal rewrote the target: mtime %s -> %s", infoBefore.ModTime(), infoAfter.ModTime())
	}
}

// TestCLIGraphAcceptsGenuineUnicodeNames: a genuine replacement character
// and correctly paired surrogate escapes stay legal end to end — the graph
// snapshots, compares, and traces with the names kept verbatim.
func TestCLIGraphAcceptsGenuineUnicodeNames(t *testing.T) {
	graphPath := writeFile(t, "graph.json",
		`{"datasets":[{"name":"源\ufffd","upstreams":[]},{"name":"ledger\ud83d\ude00","upstreams":["源\ufffd"]}]}`)
	target := filepath.Join(t.TempDir(), "snap.json")

	stdout, stderr, exit := captureStdout(t, func() int {
		return run([]string{"snapshot", graphPath, target})
	})
	if exit != 0 {
		t.Fatalf("snapshot rejected legal Unicode names: %s", stderr)
	}
	if !strings.HasPrefix(strings.TrimSpace(stdout), "sha256:") {
		t.Errorf("stdout = %q, want content id", stdout)
	}

	stdout, stderr, exit = captureStdout(t, func() int {
		return run([]string{"trace", target, "ledger😀"})
	})
	if exit != 0 {
		t.Fatalf("trace exit = %d, stderr = %s", exit, stderr)
	}
	if !strings.Contains(stdout, `"源�"`) {
		t.Errorf("trace report must name the genuine 源� root verbatim: %s", stdout)
	}

	// Self-comparison of a legal Unicode snapshot reports no differences.
	stdout, stderr, exit = captureStdout(t, func() int {
		return run([]string{"compare", target, target})
	})
	if exit != 0 {
		t.Fatalf("compare exit = %d, stderr = %s", exit, stderr)
	}
	if !strings.Contains(stdout, `"newDatasets": []`) {
		t.Errorf("self compare of a legal Unicode snapshot should be empty: %s", stdout)
	}
}
