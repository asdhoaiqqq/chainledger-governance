package main

import (
	"os"
	"strings"
	"testing"
)

// TestCLIGraphDuplicateFieldRejected: preview, apply, and snapshot read the
// same graph file and must all reject a graph in which a known field
// (datasets, name, upstreams) is declared twice within one object — non-zero
// exit, empty stdout, and a stderr message naming the graph file, the
// repeated field, and the object it appears in. A rejected apply leaves the
// graph file untouched; a rejected snapshot creates nothing.
func TestCLIGraphDuplicateFieldRejected(t *testing.T) {
	// The second record declares upstreams twice; keeping only one would
	// silently change whether B is a root.
	graphJSON := `{"datasets":[{"name":"A","upstreams":[]},{"name":"B","upstreams":["A"],"upstreams":[]}]}`
	planPath := writeFile(t, "plan.json", `{"changes":[]}`)

	t.Run("preview", func(t *testing.T) {
		graphPath := writeFile(t, "graph.json", graphJSON)
		stdout, stderr, exit := captureStdout(t, func() int {
			return run([]string{"preview", graphPath, planPath})
		})
		if exit == 0 {
			t.Fatalf("preview exit = 0, want non-zero")
		}
		if stdout != "" {
			t.Errorf("stdout = %q, want empty on rejection", stdout)
		}
		for _, want := range []string{graphPath, `"upstreams"`, "index 1"} {
			if !strings.Contains(stderr, want) {
				t.Errorf("stderr = %q, want it to contain %q", stderr, want)
			}
		}
	})

	t.Run("apply keeps graph file", func(t *testing.T) {
		graphPath := writeFile(t, "graph.json", graphJSON)
		stdout, stderr, exit := captureStdout(t, func() int {
			return run([]string{"apply", graphPath, planPath})
		})
		if exit == 0 {
			t.Fatalf("apply exit = 0, want non-zero")
		}
		if stdout != "" {
			t.Errorf("stdout = %q, want empty on rejection", stdout)
		}
		if !strings.Contains(stderr, graphPath) || !strings.Contains(stderr, `"upstreams"`) {
			t.Errorf("stderr = %q, want the graph file and the repeated field", stderr)
		}
		if got := readFile(t, graphPath); got != graphJSON {
			t.Errorf("graph file changed after rejected apply:\n got %s\nwant %s", got, graphJSON)
		}
	})

	t.Run("snapshot creates nothing", func(t *testing.T) {
		graphPath := writeFile(t, "graph.json", graphJSON)
		snapshotPath := graphPath + ".snap"
		stdout, stderr, exit := captureStdout(t, func() int {
			return run([]string{"snapshot", graphPath, snapshotPath})
		})
		if exit == 0 {
			t.Fatalf("snapshot exit = 0, want non-zero")
		}
		if stdout != "" {
			t.Errorf("stdout = %q, want empty on rejection", stdout)
		}
		if !strings.Contains(stderr, graphPath) || !strings.Contains(stderr, `"upstreams"`) {
			t.Errorf("stderr = %q, want the graph file and the repeated field", stderr)
		}
		if _, err := os.Stat(snapshotPath); !os.IsNotExist(err) {
			t.Errorf("snapshot target exists after rejected save: %v", err)
		}
	})

	t.Run("datasets declared twice", func(t *testing.T) {
		dup := `{"datasets":[],"datasets":[{"name":"A","upstreams":[]}]}`
		graphPath := writeFile(t, "graph.json", dup)
		stdout, stderr, exit := captureStdout(t, func() int {
			return run([]string{"preview", graphPath, planPath})
		})
		if exit == 0 {
			t.Fatalf("preview exit = 0, want non-zero")
		}
		if stdout != "" {
			t.Errorf("stdout = %q, want empty on rejection", stdout)
		}
		if !strings.Contains(stderr, graphPath) || !strings.Contains(stderr, `"datasets"`) {
			t.Errorf("stderr = %q, want the graph file and the repeated field", stderr)
		}
	})
}

// TestCLIGraphDuplicateFieldDoesNotMaskOtherErrors: the new duplicate-field
// validation must not let any previously rejected graph through — missing
// upstreams, duplicate datasets, and cycles are still refused.
func TestCLIGraphDuplicateFieldDoesNotMaskOtherErrors(t *testing.T) {
	planPath := writeFile(t, "plan.json", `{"changes":[]}`)
	cases := map[string]string{
		"missing upstream":  `{"datasets":[{"name":"A","upstreams":["ghost"]}]}`,
		"duplicate dataset": `{"datasets":[{"name":"A","upstreams":[]},{"name":"A","upstreams":[]}]}`,
		"cycle":             `{"datasets":[{"name":"A","upstreams":["B"]},{"name":"B","upstreams":["A"]}]}`,
	}
	for name, graphJSON := range cases {
		t.Run(name, func(t *testing.T) {
			graphPath := writeFile(t, "graph.json", graphJSON)
			stdout, _, exit := captureStdout(t, func() int {
				return run([]string{"preview", graphPath, planPath})
			})
			if exit == 0 {
				t.Fatalf("preview exit = 0, want non-zero")
			}
			if stdout != "" {
				t.Errorf("stdout = %q, want empty on rejection", stdout)
			}
		})
	}
}
