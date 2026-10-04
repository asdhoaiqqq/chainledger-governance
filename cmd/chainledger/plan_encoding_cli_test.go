package main

import (
	"strings"
	"testing"
)

// These tests pin the CLI behavior for a plan whose raw name literals are
// corrupt (invalid UTF-8 bytes or unpaired \u surrogate escapes): preview
// and apply reject identically — non-zero exit, empty stdout, stderr naming
// the plan file, the field, and the zero-based position — and neither the
// graph file nor the plan file is touched.

// TestCLIBatchRejectsCorruptPlanNames runs the same corrupt plan through
// preview and apply and requires identical rejection behavior from both.
func TestCLIBatchRejectsCorruptPlanNames(t *testing.T) {
	cases := map[string]struct {
		plan     string
		field    string
		location string
		reason   string
	}{
		"invalid bytes in change name": {
			`{"changes":[{"name":"source` + "\xff" + `","upstreams":[]}]}`,
			`"name"`, `index 0 of "changes"`, "invalid UTF-8 bytes",
		},
		"invalid bytes in upstream entry": {
			`{"changes":[{"name":"A","upstreams":["g` + "\xfe" + `"]}]}`,
			`"upstreams"`, `index 0`, "invalid UTF-8 bytes",
		},
		"invalid bytes in removals entry": {
			`{"removals":["ok","源` + "\xff" + `"]}`,
			`"removals"`, `index 1`, "invalid UTF-8 bytes",
		},
		"unpaired surrogate in change name": {
			`{"changes":[{"name":"a\ud800","upstreams":[]}]}`,
			`"name"`, `index 0 of "changes"`, "unpaired surrogate",
		},
		"unpaired surrogate in removals entry": {
			`{"removals":["\udc00\ud800"]}`,
			`"removals"`, `index 0`, "unpaired surrogate",
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			for _, verb := range []string{"preview", "apply"} {
				t.Run(verb, func(t *testing.T) {
					graphPath := writeFile(t, "graph.json", `{"datasets":[{"name":"A","upstreams":[]},{"name":"源�","upstreams":["A"]}]}`)
					planPath := writeFile(t, "plan.json", tc.plan)
					graphBefore := readFile(t, graphPath)
					planBefore := readFile(t, planPath)

					stdout, stderr, exit := captureStdout(t, func() int {
						return run([]string{verb, graphPath, planPath})
					})
					if exit == 0 {
						t.Fatalf("%s exit = 0, want non-zero; stdout = %s", verb, stdout)
					}
					if stdout != "" {
						t.Errorf("%s stdout = %q, want empty on rejection", verb, stdout)
					}
					if !strings.Contains(stderr, planPath) {
						t.Errorf("stderr = %q, want it to name the plan file %q", stderr, planPath)
					}
					for _, want := range []string{tc.field, tc.location, tc.reason} {
						if !strings.Contains(stderr, want) {
							t.Errorf("stderr = %q, want it to contain %q", stderr, want)
						}
					}
					if got := readFile(t, graphPath); got != graphBefore {
						t.Errorf("graph file changed after rejected %s:\n got %s\nwant %s", verb, got, graphBefore)
					}
					if got := readFile(t, planPath); got != planBefore {
						t.Errorf("plan file changed after rejected %s", verb)
					}
				})
			}
		})
	}
}

// TestCLIApplyCorruptPlanBlocksWholeBatch: one corrupt name refuses the
// whole batch — the legal adjustments in the same plan must not take
// effect, and the corrupt removal must not delete the dataset whose real
// name the decoder's silent rewrite would collide with.
func TestCLIApplyCorruptPlanBlocksWholeBatch(t *testing.T) {
	graphContent := `{"datasets":[{"name":"源�","upstreams":[]},{"name":"down","upstreams":["源�"]}]}`
	graphPath := writeFile(t, "graph.json", graphContent)
	// "源" plus one broken byte decodes to the real dataset "源�"; the legal
	// change to "down" in the same batch must not be applied either.
	planPath := writeFile(t, "plan.json", `{"changes":[{"name":"down","upstreams":[]}],"removals":["源`+"\xff"+`"]}`)

	stdout, stderr, exit := captureStdout(t, func() int {
		return run([]string{"apply", graphPath, planPath})
	})
	if exit == 0 {
		t.Fatalf("apply exit = 0, want non-zero; stdout = %s", stdout)
	}
	if stdout != "" {
		t.Errorf("stdout = %q, want empty on rejection", stdout)
	}
	if !strings.Contains(stderr, "invalid UTF-8 bytes") {
		t.Errorf("stderr = %q, want the invalid-bytes reason", stderr)
	}
	if got := readFile(t, graphPath); got != graphContent {
		t.Errorf("graph file changed after rejected apply:\n got %s\nwant %s", got, graphContent)
	}
}

// TestCLIBatchAcceptsGenuineUnicodeNames: a genuine replacement character,
// a correctly paired surrogate escape, and escaped spellings of legal text
// remain legal end to end.
func TestCLIBatchAcceptsGenuineUnicodeNames(t *testing.T) {
	graphPath := writeFile(t, "graph.json", `{"datasets":[{"name":"源�","upstreams":[]}]}`)
	planPath := writeFile(t, "plan.json", `{"changes":[{"name":"ledger\ud83d\ude00","upstreams":["源\ufffd"]}]}`)
	stdout, stderr, exit := captureStdout(t, func() int {
		return run([]string{"apply", graphPath, planPath})
	})
	if exit != 0 {
		t.Fatalf("apply exit = %d, stderr = %s", exit, stderr)
	}
	if !strings.Contains(stdout, `"newDatasets"`) || !strings.Contains(stdout, "ledger😀") {
		t.Errorf("stdout missing the applied report: %s", stdout)
	}
	if got := readFile(t, graphPath); !strings.Contains(got, "ledger😀") {
		t.Errorf("graph file was not updated with the new dataset: %s", got)
	}
}
