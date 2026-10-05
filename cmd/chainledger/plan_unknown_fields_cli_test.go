package main

import (
	"strings"
	"testing"
)

// These tests pin the CLI behavior for plans whose ignored unknown fields
// carry legal JSON numbers outside the float64 range (1e400), possibly
// nested in unknown objects and arrays and placed anywhere — at the start of
// the plan or inside a change record right before a name. Such a number must
// never abort the name validation behind it: a corrupt name that follows is
// still reported as corrupt by both preview and apply (non-zero exit, empty
// stdout, stderr naming the plan file, the field, and the zero-based
// position), a deletion can never be redirected onto a dataset the user did
// not write, and a fully legal plan behaves exactly as if the unknown fields
// were absent.

// hugeNumber is a legal JSON number outside the float64 range.
const hugeNumber = `1e400`

// TestCLIBatchHugeNumbersNeverHideCorruptPlanNames runs corrupt plans whose
// corrupt names sit behind oversized unknown numbers through preview and
// apply and requires identical rejection behavior from both.
func TestCLIBatchHugeNumbersNeverHideCorruptPlanNames(t *testing.T) {
	cases := map[string]struct {
		plan     string
		field    string
		location string
		reason   string
	}{
		"huge number at plan start before corrupt change name": {
			`{"meta":` + hugeNumber + `,"changes":[{"name":"source` + "\xff" + `","upstreams":[]}]}`,
			`"name"`, `index 0 of "changes"`, "invalid UTF-8 bytes",
		},
		"huge number nested in unknown containers before corrupt upstream": {
			`{"meta":{"deep":[` + hugeNumber + `,{"n":` + hugeNumber + `}]},"changes":[{"name":"A","upstreams":["ok","\ud800"]}]}`,
			`"upstreams"`, `index 1`, "unpaired surrogate",
		},
		"huge number inside change record before corrupt removals entry": {
			`{"changes":[{"name":"A","upstreams":[],"stats":[` + hugeNumber + `]}],"removals":["ok","源` + "\xff" + `"]}`,
			`"removals"`, `index 1`, "invalid UTF-8 bytes",
		},
		"huge number before corrupt removal naming no dataset": {
			// Accepting this would look like a harmless no-op deletion of a
			// name nothing uses; it must still be refused.
			`{"meta":` + hugeNumber + `,"removals":["ghost\udc00\ud800"]}`,
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
					// The corruption must be reported as what it is — never as
					// a missing upstream or a number problem.
					if strings.Contains(stderr, "does not exist") {
						t.Errorf("stderr = %q, a corrupt name must not be misreported as a missing upstream", stderr)
					}
					if strings.Contains(stderr, hugeNumber) {
						t.Errorf("stderr = %q, the ignored unknown number must not be blamed", stderr)
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

// TestCLIApplyHugeNumberCorruptRemovalKeepsRealDataset: the graph really
// contains a dataset named "源�". A removals entry of "源" plus one corrupt
// byte — which the decoder's silent rewrite would turn into that real name —
// must not delete it, even with an oversized unknown number riding in front
// of it. The rejected apply leaves both files byte-for-byte untouched.
func TestCLIApplyHugeNumberCorruptRemovalKeepsRealDataset(t *testing.T) {
	graphContent := `{"datasets":[{"name":"源�","upstreams":[]},{"name":"down","upstreams":["源�"]}]}`
	graphPath := writeFile(t, "graph.json", graphContent)
	planPath := writeFile(t, "plan.json", `{"meta":[`+hugeNumber+`],"changes":[{"name":"down","upstreams":[]}],"removals":["源`+"\xff"+`"]}`)
	planBefore := readFile(t, planPath)

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
	if !strings.Contains(stderr, `"removals"`) {
		t.Errorf("stderr = %q, want it to name the removals field", stderr)
	}
	got := readFile(t, graphPath)
	if got != graphContent {
		t.Errorf("graph file changed after rejected apply:\n got %s\nwant %s", got, graphContent)
	}
	if !strings.Contains(got, "源�") {
		t.Errorf("the real 源� dataset must survive the rejected apply: %s", got)
	}
	if got := readFile(t, planPath); got != planBefore {
		t.Errorf("plan file changed after rejected apply")
	}
}

// TestCLIBatchUnknownFieldsDoNotChangeReportOrFinalGraph: for a fully legal
// plan, attaching unknown fields — oversized numbers, nested containers,
// repeated keys and corrupt strings inside them, content that merely looks
// like change records — changes nothing: preview prints the identical
// report, and apply prints the identical report and writes the identical
// graph file.
func TestCLIBatchUnknownFieldsDoNotChangeReportOrFinalGraph(t *testing.T) {
	graphContent := `{"datasets":[{"name":"A","upstreams":[]},{"name":"C","upstreams":[]}]}`
	plain := `{"changes":[{"name":"B","upstreams":["A"]}],"removals":["C"]}`
	loaded := `{"meta":` + hugeNumber + `,` +
		`"changes":[{"name":"B","upstreams":["A"],"note":{"n":` + hugeNumber + `,"n":2,"s":"\ud800"}}],` +
		`"removals":["C"],` +
		`"stats":[` + hugeNumber + `,{"changes":[{"name":"\ud800"}],"k":"x` + "\xff" + `"}]}`

	planPlainPath := writeFile(t, "plan-plain.json", plain)
	planLoadedPath := writeFile(t, "plan-loaded.json", loaded)

	runVerb := func(verb, planPath string) (stdout, stderr string, exit int, graphPath string) {
		graphPath = writeFile(t, "graph.json", graphContent)
		stdout, stderr, exit = captureStdout(t, func() int {
			return run([]string{verb, graphPath, planPath})
		})
		return stdout, stderr, exit, graphPath
	}

	previewPlain, stderr, exit, _ := runVerb("preview", planPlainPath)
	if exit != 0 {
		t.Fatalf("preview of plain plan exit = %d, stderr = %s", exit, stderr)
	}
	previewLoaded, stderr, exit, _ := runVerb("preview", planLoadedPath)
	if exit != 0 {
		t.Fatalf("preview of loaded plan exit = %d, stderr = %s", exit, stderr)
	}
	if previewPlain != previewLoaded {
		t.Errorf("unknown fields changed the preview report:\nplain  %s\nloaded %s", previewPlain, previewLoaded)
	}

	applyPlain, stderr, exit, graphPlainPath := runVerb("apply", planPlainPath)
	if exit != 0 {
		t.Fatalf("apply of plain plan exit = %d, stderr = %s", exit, stderr)
	}
	applyLoaded, stderr, exit, graphLoadedPath := runVerb("apply", planLoadedPath)
	if exit != 0 {
		t.Fatalf("apply of loaded plan exit = %d, stderr = %s", exit, stderr)
	}
	if applyPlain != applyLoaded {
		t.Errorf("unknown fields changed the apply report:\nplain  %s\nloaded %s", applyPlain, applyLoaded)
	}
	if got, want := readFile(t, graphLoadedPath), readFile(t, graphPlainPath); got != want {
		t.Errorf("unknown fields changed the final graph file:\nplain  %s\nloaded %s", want, got)
	}
}

// TestCLIBatchGenuineReplacementCharRemovalWithHugeNumbers: written
// correctly — directly or as the equivalent escapes — the real name "源�" is
// genuinely removed, with the same oversized unknown numbers attached.
// Preview and apply both succeed, the real name lands in removedDatasets,
// and both spellings print the same report.
func TestCLIBatchGenuineReplacementCharRemovalWithHugeNumbers(t *testing.T) {
	graphContent := `{"datasets":[{"name":"源�","upstreams":[]},{"name":"down","upstreams":["源�"]}]}`
	spellings := map[string]string{
		"direct":  `{"meta":` + hugeNumber + `,"changes":[{"name":"down","upstreams":[]}],"removals":["源�"]}`,
		"escaped": `{"meta":` + hugeNumber + `,"changes":[{"name":"down","upstreams":[]}],"removals":["\u6e90\ufffd"]}`,
	}

	var wantStdout string
	for name, plan := range spellings {
		t.Run(name, func(t *testing.T) {
			graphPath := writeFile(t, "graph.json", graphContent)
			planPath := writeFile(t, "plan.json", plan)

			stdout, stderr, exit := captureStdout(t, func() int {
				return run([]string{"preview", graphPath, planPath})
			})
			if exit != 0 {
				t.Fatalf("preview exit = %d, stderr = %s", exit, stderr)
			}
			if !strings.Contains(stdout, `"removedDatasets"`) || !strings.Contains(stdout, "源�") {
				t.Errorf("preview stdout missing the real removal of 源�: %s", stdout)
			}
			if got := readFile(t, graphPath); got != graphContent {
				t.Errorf("graph file changed after preview:\n got %s\nwant %s", got, graphContent)
			}
			if wantStdout == "" {
				wantStdout = stdout
			} else if stdout != wantStdout {
				t.Errorf("preview reports differ between spellings:\n%s\n%s", stdout, wantStdout)
			}

			stdout, stderr, exit = captureStdout(t, func() int {
				return run([]string{"apply", graphPath, planPath})
			})
			if exit != 0 {
				t.Fatalf("apply exit = %d, stderr = %s", exit, stderr)
			}
			if !strings.Contains(stdout, "源�") {
				t.Errorf("apply stdout missing the real removal of 源�: %s", stdout)
			}
			got := readFile(t, graphPath)
			if strings.Contains(got, "源�") {
				t.Errorf("源� still in the graph file after apply: %s", got)
			}
			if !strings.Contains(got, "down") {
				t.Errorf("down missing from the graph file after apply: %s", got)
			}
		})
	}
}
