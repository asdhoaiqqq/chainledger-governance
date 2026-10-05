package main

import (
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"github.com/asdhoaiqqq/chainledger-governance/chainledger"
)

// mustReadGraphCLI re-reads a graph file as the in-memory graph so a test can
// assert which datasets survived a rejected or applied batch.
func mustReadGraphCLI(t *testing.T, path string) map[string]*chainledger.Lineage {
	t.Helper()
	graph, err := chainledger.UnmarshalGraphFile([]byte(readFile(t, path)))
	if err != nil {
		t.Fatalf("re-read graph %q: %v", path, err)
	}
	return graph
}

// mustParseReport parses a preview/apply JSON report from stdout.
func mustParseReport(t *testing.T, stdout string) *chainledger.BatchReport {
	t.Helper()
	var report chainledger.BatchReport
	if err := json.Unmarshal([]byte(stdout), &report); err != nil {
		t.Fatalf("stdout is not a batch report: %v\n%s", err, stdout)
	}
	return &report
}

// End-to-end regression coverage for the plan name-encoding gate when the
// plan also carries additional information in unknown fields. A legal JSON
// number outside the float64 range (1e400) — at the start of the plan, in
// unknown objects/arrays, or inside a change record — must not interfere
// with the names that actually take part in the adjustment: preview and
// apply must still reject a corrupt name (invalid UTF-8 bytes or an unpaired
// surrogate) with a non-zero exit, empty stdout, and stderr naming the plan
// file, the field, and the zero-based record/array position.

// graphWithReplacementCharRoot holds a dataset genuinely named "源�" plus an
// unrelated root: a corrupted removal must never delete the former, and a
// legal one must.
const graphWithReplacementCharRoot = `{"datasets":[{"name":"源�","upstreams":[]},{"name":"keep","upstreams":[]}]}`

// runBatchCLI executes one preview/apply invocation through run(), the same
// entry point the command dispatcher uses.
func runBatchCLI(t *testing.T, verb, graphPath, planPath string) (stdout, stderr string, exit int) {
	t.Helper()
	return captureStdout(t, func() int {
		return run([]string{verb, graphPath, planPath})
	})
}

// TestCLIBigNumberUnknownFieldsDoNotMaskCorruptNames drives every
// combination of {big-number placement} x {corrupt business name} through
// BOTH preview and apply, and requires the identical name-encoding failure
// from each: non-zero exit, no report on stdout, and a precise stderr.
func TestCLIBigNumberUnknownFieldsDoNotMaskCorruptNames(t *testing.T) {
	bigPrefixes := map[string]string{
		"big number opens the plan":           `{"note":1e400,`,
		"big number nested before changes":    `{"meta":{"x":[1e400,{"y":1e999}]},`,
		"big numbers in an unknown array":     `{"bag":[1e400,"z",[2e999,{"k":-1e400}]],`,
		"big unknown field inside the record": `{"changes":[{`,
	}

	cases := map[string]struct {
		// plan renders the corrupt plan with a given prefix placement.
		plan     func(prefix string) string
		field    string
		location string
		reason   string
	}{
		"invalid bytes in change name": {
			func(p string) string {
				if strings.HasPrefix(p, `{"changes":[{`) {
					return p + `"note":1e400,"name":"a` + "\xff" + `","upstreams":[]}]}`
				}
				return p + `"changes":[{"name":"a` + "\xff" + `","upstreams":[]}]}`
			},
			`"name"`, `index 0 of "changes"`, "invalid UTF-8 bytes",
		},
		"unpaired surrogate in change name": {
			func(p string) string {
				if strings.HasPrefix(p, `{"changes":[{`) {
					return p + `"note":[1e999],"name":"a\ud800","upstreams":[]}]}`
				}
				return p + `"changes":[{"name":"a\ud800","upstreams":[]}]}`
			},
			`"name"`, `index 0 of "changes"`, "unpaired surrogate",
		},
		"invalid bytes in an upstream entry": {
			// The array position of the upstream entry must be reported, and
			// the failure is the corrupt name — never a missing upstream.
			func(p string) string {
				if strings.HasPrefix(p, `{"changes":[{`) {
					return p + `"name":"A","note":1e400,"upstreams":["ok","g` + "\xfe" + `"]}]}`
				}
				return p + `"changes":[{"name":"A","upstreams":["ok","g` + "\xfe" + `"]}]}`
			},
			`"upstreams"`, `index 1`, "invalid UTF-8 bytes",
		},
		"unpaired surrogate in an upstream entry": {
			func(p string) string {
				if strings.HasPrefix(p, `{"changes":[{`) {
					return p + `"name":"A","meta":[1e400],"upstreams":["\udfff"]}]}`
				}
				return p + `"changes":[{"name":"A","upstreams":["\udfff"]}]}`
			},
			`"upstreams"`, `index 0`, "unpaired surrogate",
		},
		"invalid bytes in a removals entry": {
			func(p string) string {
				if strings.HasPrefix(p, `{"changes":[{`) {
					// The big number lives in a legal change record that
					// precedes the corrupt removals array.
					return p + `"name":"A","note":1e400,"upstreams":[]}],"removals":["ok","b","c` + "\xc3" + `"]}`
				}
				return p + `"removals":["ok","b","c` + "\xc3" + `"]}`
			},
			`"removals"`, `index 2`, "invalid UTF-8 bytes",
		},
		"unpaired surrogate in a removals entry": {
			func(p string) string {
				if strings.HasPrefix(p, `{"changes":[{`) {
					return p + `"name":"A","note":[1e999],"upstreams":[]}],"removals":["ok","\udc00\ud800"]}`
				}
				return p + `"removals":["ok","\udc00\ud800"]}`
			},
			`"removals"`, `index 1`, "unpaired surrogate",
		},
	}

	for prefixName, prefix := range bigPrefixes {
		for caseName, tc := range cases {
			t.Run(prefixName+" / "+caseName, func(t *testing.T) {
				planContent := tc.plan(prefix)
				for _, verb := range []string{"preview", "apply"} {
					t.Run(verb, func(t *testing.T) {
						graphPath := writeFile(t, "graph.json",
							`{"datasets":[{"name":"A","upstreams":[]},{"name":"ok","upstreams":["A"]}]}`)
						planPath := writeFile(t, "plan.json", planContent)
						graphBefore := readFile(t, graphPath)
						planBefore := readFile(t, planPath)

						stdout, stderr, exit := runBatchCLI(t, verb, graphPath, planPath)
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
						switch tc.reason {
						case "invalid UTF-8 bytes":
							if strings.Contains(stderr, "surrogate") {
								t.Errorf("stderr = %q, byte corruption must not read as a surrogate problem", stderr)
							}
						case "unpaired surrogate":
							if strings.Contains(stderr, "invalid UTF-8 bytes") {
								t.Errorf("stderr = %q, a surrogate problem must not read as invalid bytes", stderr)
							}
						}
						// Neither file may change: a rejected apply writes no
						// graph, and the plan is only ever read.
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
}

// TestCLICorruptRemovalKeepsRealReplacementCharDataset: the graph really
// contains a dataset named "源�". A removal written as "源" plus a broken
// byte or a lone surrogate — preceded by big-number additions that must be
// ignored — cannot delete it through the decoder's U+FFFD rewrite, and a
// corrupted name matching no dataset at all cannot pass as the no-op
// "remove a nonexistent name". Both files keep their original bytes after
// preview and after apply.
func TestCLICorruptRemovalKeepsRealReplacementCharDataset(t *testing.T) {
	cases := map[string]struct {
		plan   string
		reason string
	}{
		"broken byte would collide with real 源�": {
			`{"note":1e400,"removals":["源` + "\xff" + `"]}`, "invalid UTF-8 bytes",
		},
		"lone surrogate would collide with real 源�": {
			`{"meta":[1e999,{"z":-1e400}],"removals":["源\ud800"]}`, "unpaired surrogate",
		},
		"low-before-high surrogate would collide": {
			`{"note":{"x":1e400},"removals":["源\udc00\ud800"]}`, "unpaired surrogate",
		},
		"broken byte names no dataset": {
			`{"note":1e400,"removals":["ghost` + "\xff" + `"]}`, "invalid UTF-8 bytes",
		},
		"lone surrogate names no dataset": {
			`{"bag":[1e400],"removals":["ghost\udfff"]}`, "unpaired surrogate",
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			for _, verb := range []string{"preview", "apply"} {
				t.Run(verb, func(t *testing.T) {
					graphPath := writeFile(t, "graph.json", graphWithReplacementCharRoot)
					planPath := writeFile(t, "plan.json", tc.plan)
					graphBefore := readFile(t, graphPath)
					planBefore := readFile(t, planPath)

					stdout, stderr, exit := runBatchCLI(t, verb, graphPath, planPath)
					if exit == 0 {
						t.Fatalf("%s exit = 0, want non-zero; stdout = %s", verb, stdout)
					}
					if stdout != "" {
						t.Errorf("%s stdout = %q, want empty on rejection", verb, stdout)
					}
					for _, want := range []string{planPath, `"removals"`, "index 0", tc.reason} {
						if !strings.Contains(stderr, want) {
							t.Errorf("stderr = %q, want it to contain %q", stderr, want)
						}
					}
					if strings.Contains(stderr, "no change") {
						t.Errorf("stderr = %q, a corrupt name must not pass as a no-change success", stderr)
					}
					if got := readFile(t, graphPath); got != graphBefore {
						t.Errorf("graph file changed:\n got %s\nwant %s", got, graphBefore)
					}
					if got := readFile(t, planPath); got != planBefore {
						t.Errorf("plan file changed")
					}
					graph := mustReadGraphCLI(t, graphPath)
					if _, ok := graph["源�"]; !ok {
						t.Fatal("the dataset really named 源� was deleted by a corrupted removal")
					}
					if _, ok := graph["keep"]; !ok {
						t.Fatal("the unrelated dataset keep disappeared")
					}
				})
			}
		})
	}
}

// TestCLIBigNumbersWholeBatchRejectedBeforeAnyChange: with a big number in
// an ignored field, a legal adjustment that WOULD change the graph, and a
// corrupt removal later in the same plan, apply must reject the whole batch
// — the legal adjustment must not take effect either, and the real "源�"
// must survive. Preview behaves identically and touches neither file.
func TestCLIBigNumbersWholeBatchRejectedBeforeAnyChange(t *testing.T) {
	// down really depends on 源�; the legal change would clear that edge.
	graphContent := `{"datasets":[{"name":"源�","upstreams":[]},{"name":"down","upstreams":["源�"]}]}`
	for _, corrupt := range []struct {
		name    string
		removal string
		reason  string
	}{
		{"broken byte", `"源` + "\xff" + `"`, "invalid UTF-8 bytes"},
		{"lone surrogate", `"源\ud800"`, "unpaired surrogate"},
	} {
		t.Run(corrupt.name, func(t *testing.T) {
			planContent := `{"note":1e400,"changes":[{"name":"down","upstreams":[],"meta":[1e999]}],` +
				`"removals":[` + corrupt.removal + `]}`
			for _, verb := range []string{"preview", "apply"} {
				t.Run(verb, func(t *testing.T) {
					graphPath := writeFile(t, "graph.json", graphContent)
					planPath := writeFile(t, "plan.json", planContent)
					graphBefore := readFile(t, graphPath)

					stdout, stderr, exit := runBatchCLI(t, verb, graphPath, planPath)
					if exit == 0 {
						t.Fatalf("%s exit = 0, want non-zero; stdout = %s", verb, stdout)
					}
					if stdout != "" {
						t.Errorf("%s stdout = %q, want empty", verb, stdout)
					}
					for _, want := range []string{planPath, `"removals"`, "index 0", corrupt.reason} {
						if !strings.Contains(stderr, want) {
							t.Errorf("stderr = %q, want it to contain %q", stderr, want)
						}
					}
					// Graph bytes unchanged: neither the legal change nor the
					// corrupted removal took effect.
					if got := readFile(t, graphPath); got != graphBefore {
						t.Errorf("graph file changed after rejected %s:\n got %s\nwant %s", verb, got, graphBefore)
					}
					graph := mustReadGraphCLI(t, graphPath)
					if _, ok := graph["源�"]; !ok {
						t.Fatal("the dataset really named 源� must survive")
					}
					if parents := graph["down"].Parents; len(parents) != 1 || parents[0] != "源�" {
						t.Fatalf("down.Parents = %v, the legal change must not have taken effect, want [源�]", parents)
					}
				})
			}
		})
	}
}

// TestCLILegalRemovalOfReplacementCharWithBigNumbers: once the removal
// really names "源�" — written directly or with equivalent JSON escapes —
// and carries the same big-number additions, preview and apply succeed: the
// real name shows up in removedDatasets, and apply leaves exactly the
// surviving graph on disk.
func TestCLILegalRemovalOfReplacementCharWithBigNumbers(t *testing.T) {
	direct := `{"note":1e400,"meta":[1e999,{"z":-1e400}],"removals":["源�"]}`
	escaped := `{"note":1e400,"meta":[1e999,{"z":-1e400}],"removals":["\u6e90\ufffd"]}`
	if direct == escaped {
		t.Fatal("test premise broken: the two spellings must differ on disk")
	}
	for name, planContent := range map[string]string{"direct": direct, "escaped": escaped} {
		t.Run(name, func(t *testing.T) {
			// Preview first: report carries the real removal, graph untouched.
			previewGraph := writeFile(t, "graph-preview.json", graphWithReplacementCharRoot)
			planPath := writeFile(t, "plan.json", planContent)
			graphBefore := readFile(t, previewGraph)
			stdout, stderr, exit := runBatchCLI(t, "preview", previewGraph, planPath)
			if exit != 0 {
				t.Fatalf("preview exit = %d, stderr = %s", exit, stderr)
			}
			if got := mustParseReport(t, stdout).RemovedDatasets; len(got) != 1 || got[0] != "源�" {
				t.Errorf("preview removedDatasets = %v, want [源�]", got)
			}
			if got := readFile(t, previewGraph); got != graphBefore {
				t.Errorf("preview changed the graph file")
			}

			// Apply: the real dataset is removed, keep survives, and applying
			// the same plan once more is still a successful no-op.
			applyGraph := writeFile(t, "graph-apply.json", graphWithReplacementCharRoot)
			stdout, stderr, exit = runBatchCLI(t, "apply", applyGraph, planPath)
			if exit != 0 {
				t.Fatalf("apply exit = %d, stderr = %s", exit, stderr)
			}
			if got := mustParseReport(t, stdout).RemovedDatasets; len(got) != 1 || got[0] != "源�" {
				t.Errorf("apply removedDatasets = %v, want [源�]", got)
			}
			graph := mustReadGraphCLI(t, applyGraph)
			if _, ok := graph["源�"]; ok {
				t.Fatal("the dataset really named 源� must be gone after apply")
			}
			if _, ok := graph["keep"]; !ok {
				t.Fatal("the unrelated dataset keep must survive")
			}

			stdout, stderr, exit = runBatchCLI(t, "apply", applyGraph, planPath)
			if exit != 0 {
				t.Fatalf("re-apply exit = %d, stderr = %s", exit, stderr)
			}
			if got := mustParseReport(t, stdout).RemovedDatasets; len(got) != 0 {
				t.Errorf("re-apply must report no removals, got %v", got)
			}
		})
	}
}

// TestCLIUnknownFieldsDoNotChangeReportOrGraph: for a plan whose business
// names are all legal, adding or removing the additional information
// (oversized numbers at the top level, nested in unknown objects/arrays, or
// inside change records) changes neither preview's report bytes nor apply's
// report bytes and the final graph file.
func TestCLIUnknownFieldsDoNotChangeReportOrGraph(t *testing.T) {
	graphContent := `{"datasets":[{"name":"A","upstreams":[]},{"name":"B","upstreams":["A"]},{"name":"源�","upstreams":[]}]}`
	plans := map[string]string{
		"plain": `{"changes":[{"name":"B","upstreams":["源�"]}],"removals":["A"]}`,
		"big number at top": `{"note":1e400,` +
			`"changes":[{"name":"B","upstreams":["源�"]}],"removals":["A"]}`,
		"big numbers nested everywhere": `{"meta":{"x":[1e400,{"y":1e999}]},"changes":[` +
			`{"name":"B","upstreams":["源�"],"vals":[1e999,-1e400],"note":{}}],` +
			`"removals":["A"],"tail":[2e400]}`,
	}

	// Preview reports must be byte-identical across the plan spellings.
	var basePreview string
	for name, planContent := range plans {
		graphPath := writeFile(t, filepath.Join(name+"-preview-graph.json"), graphContent)
		planPath := writeFile(t, filepath.Join(name+"-plan.json"), planContent)
		stdout, stderr, exit := runBatchCLI(t, "preview", graphPath, planPath)
		if exit != 0 {
			t.Fatalf("preview %s exit = %d, stderr = %s", name, exit, stderr)
		}
		if basePreview == "" {
			basePreview = stdout
		} else if stdout != basePreview {
			t.Fatalf("preview %s report differs from plain:\n%s\n%s", name, stdout, basePreview)
		}
	}

	// Apply: identical reports and identical resulting graph bytes.
	var baseApply, baseGraph string
	for name, planContent := range plans {
		graphPath := writeFile(t, filepath.Join(name+"-apply-graph.json"), graphContent)
		planPath := writeFile(t, filepath.Join(name+"-apply-plan.json"), planContent)
		stdout, stderr, exit := runBatchCLI(t, "apply", graphPath, planPath)
		if exit != 0 {
			t.Fatalf("apply %s exit = %d, stderr = %s", name, exit, stderr)
		}
		finalGraph := readFile(t, graphPath)
		if baseApply == "" {
			baseApply, baseGraph = stdout, finalGraph
		} else {
			if stdout != baseApply {
				t.Fatalf("apply %s report differs from plain:\n%s\n%s", name, stdout, baseApply)
			}
			if finalGraph != baseGraph {
				t.Fatalf("apply %s graph bytes differ from plain:\n%s\n%s", name, finalGraph, baseGraph)
			}
		}
	}
}
