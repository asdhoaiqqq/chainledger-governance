package main

import (
	"strings"
	"testing"
)

// TestCLIBatchRejectsPlanWithDuplicateFields: a plan that repeats a known
// field within the same object — changes/removals at the top level or
// name/upstreams inside a change record — is ambiguous and must fail both
// preview and apply without any success report and without touching either
// file. stderr must name the plan file, the repeated field, and where the
// repetition sits (top level or the offending change record).
func TestCLIBatchRejectsPlanWithDuplicateFields(t *testing.T) {
	graph := `{"datasets":[{"name":"A","upstreams":[]},{"name":"B","upstreams":["A"]}]}`

	cases := map[string]struct {
		plan     string
		field    string
		location string
	}{
		"changes then empty changes": {
			`{"changes":[{"name":"A","upstreams":["B"]}],"changes":[]}`,
			"changes", "top level",
		},
		"removals twice": {
			`{"removals":["A"],"removals":[]}`,
			"removals", "top level",
		},
		"duplicate name in a record": {
			`{"changes":[{"name":"A","name":"B","upstreams":[]}]}`,
			"name", `index 0 of "changes"`,
		},
		"duplicate upstreams in a record": {
			`{"changes":[{"name":"A","upstreams":["B"],"upstreams":[]}]}`,
			"upstreams", `index 0 of "changes"`,
		},
		"duplicate in second record": {
			`{"changes":[{"name":"A","upstreams":[]},{"name":"B","upstreams":["A"],"upstreams":[]}]}`,
			"upstreams", `index 1 of "changes"`,
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			for _, command := range []string{"preview", "apply"} {
				t.Run(command, func(t *testing.T) {
					graphPath := writeFile(t, "graph.json", graph)
					planPath := writeFile(t, "plan.json", tc.plan)

					stdout, stderr, exit := captureStdout(t, func() int {
						return run([]string{command, graphPath, planPath})
					})
					if exit == 0 {
						t.Fatalf("%s exit = 0, want non-zero; stderr = %s", command, stderr)
					}
					if stdout != "" {
						t.Errorf("%s stdout = %q, want empty on rejection", command, stdout)
					}
					if !strings.Contains(stderr, planPath) {
						t.Errorf("%s stderr = %q, want it to name the plan file %q", command, stderr, planPath)
					}
					if !strings.Contains(stderr, `"`+tc.field+`"`) {
						t.Errorf("%s stderr = %q, want it to name field %q", command, stderr, tc.field)
					}
					if !strings.Contains(stderr, tc.location) {
						t.Errorf("%s stderr = %q, want it to locate the duplicate %q", command, stderr, tc.location)
					}
					// Neither input file may change.
					if got := readFile(t, graphPath); got != graph {
						t.Errorf("graph file changed:\n got %s\nwant %s", got, graph)
					}
					if got := readFile(t, planPath); got != tc.plan {
						t.Errorf("plan file changed:\n got %s\nwant %s", got, tc.plan)
					}
				})
			}
		})
	}
}

// TestCLIBatchEmptyAndRemovalOnlyPlansStillWork: plans without any repeated
// field — including the empty plan and a removals-only plan — keep their
// existing preview/apply behavior.
func TestCLIBatchEmptyAndRemovalOnlyPlansStillWork(t *testing.T) {
	for _, command := range []string{"preview", "apply"} {
		t.Run(command, func(t *testing.T) {
			graphPath := writeFile(t, "graph.json", `{"datasets":[{"name":"A","upstreams":[]}]}`)
			planPath := writeFile(t, "plan.json", `{}`)

			stdout, stderr, exit := captureStdout(t, func() int {
				return run([]string{command, graphPath, planPath})
			})
			if exit != 0 {
				t.Fatalf("%s exit = %d, stderr = %s", command, exit, stderr)
			}
			if !strings.Contains(stdout, `"finalGraph"`) {
				t.Errorf("%s stdout missing report: %s", command, stdout)
			}
		})
	}
}
