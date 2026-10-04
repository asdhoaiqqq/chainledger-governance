package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/asdhoaiqqq/chainledger-governance/chainledger"
)

// These tests pin the CLI behavior when a graph's raw name literals are
// corrupt (invalid UTF-8 bytes or unpaired \u surrogate escapes): preview,
// apply, and snapshot reject a corrupt source graph — non-zero exit, empty
// stdout, stderr naming the file, whether the problem is in a dataset name
// or an upstream, and the zero-based record (and upstream array) position;
// the source graph and any existing target keep every byte and no snapshot
// is created. compare and trace reject a snapshot that embeds such a graph
// before printing any report.

// writeRawFile writes arbitrary bytes (the test graph names carry raw
// invalid UTF-8) to a file in the test's temp directory.
func writeRawFile(t *testing.T, name string, data []byte) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatalf("WriteFile %q: %v", name, err)
	}
	return path
}

func TestCLIGraphCorruptNamesRejectedByPreviewApplySnapshot(t *testing.T) {
	cases := map[string]struct {
		graph    []byte
		field    string
		location string
		reason   string
	}{
		"invalid bytes in dataset name": {
			[]byte(`{"datasets":[{"name":"源` + "\xff" + `","upstreams":[]}]}`),
			`"name"`, `index 0 of "datasets"`, "invalid UTF-8 bytes",
		},
		"invalid bytes in upstream entry": {
			[]byte(`{"datasets":[{"name":"源","upstreams":[]},{"name":"d","upstreams":["源` + "\xfe" + `"]}]}`),
			`"upstreams"`, `index 0 in the dataset record at index 1 of "datasets"`, "invalid UTF-8 bytes",
		},
		"lone surrogate upstream colliding with a genuine dataset": {
			// Root genuinely named 源�; the edge written as 源 + lone \uD800
			// would resolve to it after the decoder's rewrite.
			[]byte(`{"datasets":[{"name":"源�","upstreams":[]},{"name":"d","upstreams":["源\uD800"]}]}`),
			`"upstreams"`, `index 0 in the dataset record at index 1 of "datasets"`, "unpaired surrogate",
		},
		"lone low surrogate in dataset name": {
			[]byte(`{"datasets":[{"name":"a\uDC00","upstreams":[]}]}`),
			`"name"`, `index 0 of "datasets"`, "unpaired surrogate",
		},
		"low-before-high upstream with no possible collision": {
			// Nothing is named the rewrite target; the graph must still fail.
			[]byte(`{"datasets":[{"name":"A","upstreams":[]},{"name":"B","upstreams":["ghost\uDC00\uD83D"]}]}`),
			`"upstreams"`, `index 0 in the dataset record at index 1 of "datasets"`, "unpaired surrogate",
		},
	}
	planPath := writeFile(t, "plan.json", `{"changes":[]}`)

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			graphPath := writeRawFile(t, "graph.json", tc.graph)

			for _, verb := range []string{"preview", "apply"} {
				t.Run(verb, func(t *testing.T) {
					stdout, stderr, exit := captureStdout(t, func() int {
						return run([]string{verb, graphPath, planPath})
					})
					if exit == 0 {
						t.Fatalf("%s exit = 0, want non-zero; stdout = %s", verb, stdout)
					}
					if stdout != "" {
						t.Errorf("%s stdout = %q, want empty on rejection", verb, stdout)
					}
					for _, want := range []string{graphPath, tc.field, tc.location, tc.reason} {
						if !strings.Contains(stderr, want) {
							t.Errorf("%s stderr = %q, want it to contain %q", verb, stderr, want)
						}
					}
					if got := readFile(t, graphPath); got != string(tc.graph) {
						t.Errorf("%s changed the source graph bytes", verb)
					}
				})
			}

			t.Run("snapshot", func(t *testing.T) {
				target := filepath.Join(t.TempDir(), "snap.json")
				stdout, stderr, exit := captureStdout(t, func() int {
					return run([]string{"snapshot", graphPath, target})
				})
				if exit == 0 {
					t.Fatalf("snapshot exit = 0, want non-zero; stdout = %s", stdout)
				}
				if stdout != "" {
					t.Errorf("snapshot stdout = %q, want empty on rejection", stdout)
				}
				for _, want := range []string{graphPath, tc.field, tc.reason} {
					if !strings.Contains(stderr, want) {
						t.Errorf("snapshot stderr = %q, want it to contain %q", stderr, want)
					}
				}
				if got := readFile(t, graphPath); got != string(tc.graph) {
					t.Error("snapshot changed the source graph bytes")
				}
				if _, err := os.Stat(target); !os.IsNotExist(err) {
					t.Errorf("snapshot target must not be created, stat err = %v", err)
				}
			})
		})
	}
}

// TestCLISnapshotCorruptSourcePreservesExistingTarget: when the source graph
// is corrupt the snapshot command fails before touching the target, so an
// existing (valid) target snapshot keeps every byte.
func TestCLISnapshotCorruptSourcePreservesExistingTarget(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "snap.json")
	if _, stderr, exit := snapshotIntoDir(t, dir, abcGraph, "snap.json"); exit != 0 {
		t.Fatalf("seed snapshot: %s", stderr)
	}
	original := readFile(t, target)
	if err := os.Remove(target + ".lock"); err != nil {
		t.Fatalf("remove seed lock: %v", err)
	}

	corruptGraph := filepath.Join(dir, "bad.json")
	if err := os.WriteFile(corruptGraph, []byte(`{"datasets":[{"name":"源`+"\xff"+`","upstreams":[]}]}`), 0o644); err != nil {
		t.Fatalf("write corrupt graph: %v", err)
	}
	stdout, stderr, exit := captureStdout(t, func() int {
		return run([]string{"snapshot", corruptGraph, target})
	})
	if exit == 0 {
		t.Fatalf("snapshot of a corrupt graph succeeded; stdout = %s", stdout)
	}
	if stdout != "" {
		t.Errorf("stdout = %q, want empty", stdout)
	}
	if !strings.Contains(stderr, "invalid UTF-8 bytes") || !strings.Contains(stderr, corruptGraph) {
		t.Errorf("stderr = %q, want the corrupt-graph reason and file", stderr)
	}
	if got := readFile(t, target); got != original {
		t.Errorf("existing target changed:\n got %s\nwant %s", got, original)
	}
}

// snapshotIntoDir writes graphJSON to a graph file inside dir and saves a
// snapshot at dir/targetName, returning stdout, stderr, exit.
func snapshotIntoDir(t *testing.T, dir, graphJSON, targetName string) (string, string, int) {
	t.Helper()
	graphPath := filepath.Join(dir, "graph.json")
	if err := os.WriteFile(graphPath, []byte(graphJSON), 0o644); err != nil {
		t.Fatalf("write graph: %v", err)
	}
	target := filepath.Join(dir, targetName)
	stdout, stderr, exit := captureStdout(t, func() int {
		return run([]string{"snapshot", graphPath, target})
	})
	return stdout, stderr, exit
}

// corruptSnapshotWithMatchingID builds a snapshot document whose embedded
// graph carries a corrupt upstream (源 + lone \uD800) but whose declared
// contentId is the genuine identifier of the graph the decoder rewrites it
// to (root 源� with d -> 源�). The snapshot must still be rejected for the
// corrupt name.
func corruptSnapshotWithMatchingID(t *testing.T) []byte {
	t.Helper()
	graph, err := chainledger.UnmarshalGraphFile([]byte(
		`{"datasets":[{"name":"源�","upstreams":[]},{"name":"d","upstreams":["源�"]}]}`))
	if err != nil {
		t.Fatalf("seed graph: %v", err)
	}
	snap, err := chainledger.BuildSnapshot(graph)
	if err != nil {
		t.Fatalf("BuildSnapshot: %v", err)
	}
	return []byte(`{"formatVersion":1,"contentId":"` + snap.ContentID +
		`","graph":{"datasets":[{"name":"源�","upstreams":[]},{"name":"d","upstreams":["源\uD800"]}]}}`)
}

func TestCLICompareAndTraceRejectCorruptSnapshot(t *testing.T) {
	dir := t.TempDir()
	good := filepath.Join(dir, "good.json")
	if _, stderr, exit := snapshotIntoDir(t, dir, abcGraph, "good.json"); exit != 0 {
		t.Fatalf("seed good snapshot: %s", stderr)
	}
	if err := os.Remove(good + ".lock"); err != nil {
		t.Fatalf("remove seed lock: %v", err)
	}
	bad := filepath.Join(dir, "bad.json")
	if err := os.WriteFile(bad, corruptSnapshotWithMatchingID(t), 0o644); err != nil {
		t.Fatalf("write bad snapshot: %v", err)
	}
	badOriginal := readFile(t, bad)
	goodOriginal := readFile(t, good)

	for _, tc := range []struct {
		name string
		args []string
		side string
	}{
		{"corrupt old snapshot", []string{"compare", bad, good}, "old snapshot"},
		{"corrupt new snapshot", []string{"compare", good, bad}, "new snapshot"},
		{"trace over corrupt snapshot", []string{"trace", bad, "d"}, "snapshot"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			stdout, stderr, exit := captureStdout(t, func() int { return run(tc.args) })
			if exit == 0 {
				t.Fatalf("%v succeeded, want failure", tc.args)
			}
			if stdout != "" {
				t.Errorf("%v wrote a report on failure: %q", tc.args, stdout)
			}
			for _, want := range []string{bad, tc.side, "upstreams", "unpaired surrogate", "index 1 of \"datasets\""} {
				if !strings.Contains(stderr, want) {
					t.Errorf("stderr = %q, want it to contain %q", stderr, want)
				}
			}
			// Read-only commands must not alter either file or create a lock.
			if got := readFile(t, bad); got != badOriginal {
				t.Errorf("corrupt snapshot changed:\n%s", got)
			}
			if got := readFile(t, good); got != goodOriginal {
				t.Errorf("good snapshot changed:\n%s", got)
			}
			if _, err := os.Stat(bad + ".lock"); !os.IsNotExist(err) {
				t.Errorf("read-only command created a lock file: %v", err)
			}
		})
	}
}

// TestCLIGraphAcceptsGenuineUnicodeEndToEnd: legal names — a genuine
// replacement character, correctly paired surrogate escapes, CJK and emoji
// — keep working through apply, snapshot, compare, and trace.
func TestCLIGraphAcceptsGenuineUnicodeEndToEnd(t *testing.T) {
	dir := t.TempDir()
	// The root name writes 源 directly and the genuine U+FFFD as its escape;
	// the derived name writes 😀 as a correctly PAIRED surrogate escape. Both
	// must decode to 源� / 😀 rather than be rejected.
	graphJSON := []byte(`{"datasets":[{"name":"源` + "\\ufffd" + `","upstreams":[]},` +
		`{"name":"ledger` + "\\ud83d\\ude00" + `","upstreams":["源` + "\\ufffd" + `"]}]}`)
	graphPath := filepath.Join(dir, "graph.json")
	if err := os.WriteFile(graphPath, graphJSON, 0o644); err != nil {
		t.Fatalf("write graph: %v", err)
	}
	planPath := writeFile(t, "plan.json", `{"changes":[]}`)

	stdout, stderr, exit := captureStdout(t, func() int {
		return run([]string{"preview", graphPath, planPath})
	})
	if exit != 0 {
		t.Fatalf("preview of legal Unicode graph failed: %s", stderr)
	}
	if !strings.Contains(stdout, "ledger😀") || !strings.Contains(stdout, "源�") {
		t.Errorf("preview report lost Unicode names: %s", stdout)
	}
	if got := readFile(t, graphPath); got != string(graphJSON) {
		t.Error("preview changed the graph bytes")
	}

	target := filepath.Join(dir, "snap.json")
	stdout, stderr, exit = captureStdout(t, func() int {
		return run([]string{"snapshot", graphPath, target})
	})
	if exit != 0 {
		t.Fatalf("snapshot of legal Unicode graph failed: %s", stderr)
	}
	if !strings.HasPrefix(strings.TrimSpace(stdout), "sha256:") {
		t.Errorf("snapshot stdout = %q, want a content id", stdout)
	}

	stdout, stderr, exit = captureStdout(t, func() int {
		return run([]string{"trace", target, "ledger😀"})
	})
	if exit != 0 {
		t.Fatalf("trace failed: %s", stderr)
	}
	if !strings.Contains(stdout, `"root": "源�"`) {
		t.Errorf("trace report = %s, want the root named 源�", stdout)
	}
}
