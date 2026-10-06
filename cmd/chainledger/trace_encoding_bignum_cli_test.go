package main

import (
	"encoding/json"
	"os"
	"reflect"
	"strings"
	"testing"

	"github.com/asdhoaiqqq/chainledger-governance/chainledger"
)

// End-to-end regression coverage for source tracing over frozen snapshots
// that carry additional information in unknown fields. A legal JSON number
// outside the float64 range (1e400) — syntactically valid but too large for
// an ordinary float — may appear at the snapshot top level, inside the
// embedded graph, inside a dataset record's own unknown fields, or nested in
// unknown objects and arrays. It carries no lineage, so it must never abort
// the raw name-encoding scan and mask a corrupt dataset name or upstream
// later in the same file: trace (and compare, which reads snapshots through
// the same gate) must still reject the whole snapshot with a non-zero exit,
// empty stdout, a precise stderr naming the file/field/positions, and the
// snapshot file's bytes untouched. For a legal graph the same additions
// must leave the content identifier and every trace report byte unchanged.

// cliSnapshot builds a version-1 snapshot document around graphJSON with the
// declared content id and one placement of ignored big-number content.
func cliSnapshot(contentID, graphJSON, placement string) string {
	envelope := `{"formatVersion":1,"contentId":"` + contentID + `","graph":` + graphJSON + `}`
	switch placement {
	case "plain":
		return envelope
	case "big number at the snapshot top level":
		return `{"note":1e400,` + envelope[1:]
	case "big numbers nested in top-level unknown fields":
		return `{"meta":{"x":[1e400,{"y":1e999}]},"bag":[-1e400,2e999],` + envelope[1:]
	case "big number in an unknown graph field":
		return `{"formatVersion":1,"contentId":"` + contentID +
			`","graph":{"meta":[1e999,{"z":-1e400}],` + graphJSON[1:] + `}`
	case "big numbers inside every dataset record":
		records := strings.ReplaceAll(graphJSON, `{"name"`,
			`{"vals":[1e999,{"z":-1e400}],"name"`)
		return `{"formatVersion":1,"contentId":"` + contentID + `","graph":` + records + `}`
	default:
		panic("unknown placement " + placement)
	}
}

// contentIDOfGraph computes the genuine content identifier of graphJSON the
// way the snapshot command would, so a hand-written decorated snapshot can
// still carry the identifier of its graph.
func contentIDOfGraph(t *testing.T, graphJSON string) string {
	t.Helper()
	graph, err := chainledger.UnmarshalGraphFile([]byte(graphJSON))
	if err != nil {
		t.Fatalf("seed graph: %v", err)
	}
	snap, err := chainledger.BuildSnapshot(graph)
	if err != nil {
		t.Fatalf("BuildSnapshot: %v", err)
	}
	return snap.ContentID
}

// TestCLITraceBigNumberCannotMaskCorruptNames drives every big-number
// placement against a corrupt name in every business position (a dataset
// name and an upstream, first and second zero-based record, invalid bytes
// and unpaired surrogate escapes). trace must fail with non-zero exit and
// empty stdout; stderr must name the snapshot file, the field, the record
// index, the upstream array index, and the precise corruption kind; the
// snapshot bytes must not change and no lock file may appear. compare reads
// the same snapshot through the same gate and must fail identically on
// either side of the comparison.
func TestCLITraceBigNumberCannotMaskCorruptNames(t *testing.T) {
	const bogusID = "sha256:0000000000000000000000000000000000000000000000000000000000000000"
	placements := []string{
		"plain",
		"big number at the snapshot top level",
		"big numbers nested in top-level unknown fields",
		"big number in an unknown graph field",
		"big numbers inside every dataset record",
	}
	cases := map[string]struct {
		graph    string
		field    string
		location string
		reason   string
	}{
		"invalid bytes in first dataset name": {
			`{"datasets":[{"name":"a` + "\xff" + `","upstreams":[]}]}`,
			`"name"`, `index 0 of "datasets"`, "invalid UTF-8 bytes",
		},
		"unpaired surrogate in first dataset name": {
			`{"datasets":[{"name":"a\ud800","upstreams":[]}]}`,
			`"name"`, `index 0 of "datasets"`, "unpaired surrogate",
		},
		"invalid bytes in second dataset name": {
			`{"datasets":[{"name":"A","upstreams":[]},{"name":"b` + "\xfe" + `","upstreams":[]}]}`,
			`"name"`, `index 1 of "datasets"`, "invalid UTF-8 bytes",
		},
		"unpaired surrogate in second dataset name": {
			`{"datasets":[{"name":"A","upstreams":[]},{"name":"b\udc00\ud800","upstreams":[]}]}`,
			`"name"`, `index 1 of "datasets"`, "unpaired surrogate",
		},
		"invalid bytes in upstream entry": {
			`{"datasets":[{"name":"A","upstreams":[]},{"name":"B","upstreams":["A","g` + "\xfe" + `"]}]}`,
			`"upstreams"`, `index 1 in the dataset record at index 1 of "datasets"`, "invalid UTF-8 bytes",
		},
		"unpaired surrogate in first upstream entry": {
			`{"datasets":[{"name":"A","upstreams":[]},{"name":"B","upstreams":["\udfff"]}]}`,
			`"upstreams"`, `index 0 in the dataset record at index 1 of "datasets"`, "unpaired surrogate",
		},
	}

	for _, placement := range placements {
		for caseName, tc := range cases {
			t.Run(placement+" / "+caseName, func(t *testing.T) {
				content := cliSnapshot(bogusID, tc.graph, placement)
				snapPath := writeRawFile(t, "snap.json", []byte(content))

				stdout, stderr, exit := captureStdout(t, func() int {
					return run([]string{"trace", snapPath, "A"})
				})
				if exit == 0 {
					t.Fatalf("trace exit = 0, want non-zero; stdout = %s", stdout)
				}
				if stdout != "" {
					t.Errorf("trace stdout = %q, want empty on rejection", stdout)
				}
				for _, want := range []string{snapPath, tc.field, tc.location, tc.reason} {
					if !strings.Contains(stderr, want) {
						t.Errorf("trace stderr = %q, want it to contain %q", stderr, want)
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
				for _, masked := range []string{"cannot unmarshal number", "does not match the graph", "does not exist in the final graph"} {
					if strings.Contains(stderr, masked) {
						t.Errorf("stderr = %q, name corruption was masked by %q", stderr, masked)
					}
				}
				if got := readFile(t, snapPath); got != content {
					t.Errorf("snapshot file bytes changed after rejected trace")
				}
				assertNoSideFiles(t, snapPath)

				// compare applies the same snapshot gate on either side.
				good := snapshotForTrace(t, abcGraph)
				for _, side := range []struct {
					name string
					args []string
					want string
				}{
					{"old side", []string{"compare", snapPath, good}, "old snapshot"},
					{"new side", []string{"compare", good, snapPath}, "new snapshot"},
				} {
					t.Run("compare/"+side.name, func(t *testing.T) {
						out, cmpErr, cmpExit := captureStdout(t, func() int { return run(side.args) })
						if cmpExit == 0 {
							t.Fatalf("compare exit = 0, want non-zero; stdout = %s", out)
						}
						if out != "" {
							t.Errorf("compare stdout = %q, want empty", out)
						}
						for _, want := range []string{snapPath, side.want, tc.field, tc.reason} {
							if !strings.Contains(cmpErr, want) {
								t.Errorf("compare stderr = %q, want it to contain %q", cmpErr, want)
							}
						}
						if got := readFile(t, snapPath); got != content {
							t.Errorf("snapshot file bytes changed after rejected compare")
						}
						assertNoSideFiles(t, snapPath)
					})
				}
			})
		}
	}
}

// assertNoSideFiles fails if the read-only commands left a lock file beside
// the snapshot.
func assertNoSideFiles(t *testing.T, snapPath string) {
	t.Helper()
	if _, err := os.Stat(snapPath + ".lock"); !os.IsNotExist(err) {
		t.Errorf("read-only command left a lock file %s.lock", snapPath)
	}
}

// TestCLITraceCorruptUpstreamCollisionWithBigNumbers is the end-to-end
// source-misidentification case: the frozen graph really contains a root
// named "源�", while another dataset's upstream is written as "源" plus a
// broken byte or a lone surrogate. Even with the corruption replaced by
// U+FFFD the edge would resolve to the genuine root AND the declared
// content id happens to equal the rewritten graph's digest — and even with
// legal 1e400 values present in every kind of unknown field — trace must
// report the name-encoding error: non-zero exit, empty stdout (never a
// report rooted at the replaced source or a partial path), and the snapshot
// bytes unchanged. A corrupted name with nothing to collide with is
// rejected on the same terms.
func TestCLITraceCorruptUpstreamCollisionWithBigNumbers(t *testing.T) {
	rewritten := `{"datasets":[{"name":"源�","upstreams":[]},{"name":"d","upstreams":["源�"]}]}`
	matchingID := contentIDOfGraph(t, rewritten)
	const location = `index 0 in the dataset record at index 1 of "datasets"`

	cases := map[string]struct {
		graph  string
		id     string
		reason string
	}{
		"lone high surrogate would collide with real 源�": {
			`{"datasets":[{"name":"源�","upstreams":[]},{"name":"d","upstreams":["源\uD800"]}]}`,
			matchingID, "unpaired surrogate",
		},
		"lone low surrogate would collide with real 源�": {
			`{"datasets":[{"name":"源�","upstreams":[]},{"name":"d","upstreams":["源\uDC00"]}]}`,
			matchingID, "unpaired surrogate",
		},
		"low-before-high surrogate would collide with real 源�": {
			`{"datasets":[{"name":"源�","upstreams":[]},{"name":"d","upstreams":["源\uDC00\uD83D"]}]}`,
			matchingID, "unpaired surrogate",
		},
		"broken byte would collide with real 源�": {
			`{"datasets":[{"name":"源�","upstreams":[]},{"name":"d","upstreams":["源` + "\xff" + `"]}]}`,
			matchingID, "invalid UTF-8 bytes",
		},
		"lone surrogate with no dataset to collide with": {
			`{"datasets":[{"name":"A","upstreams":[]},{"name":"d","upstreams":["ghost\ud800"]}]}`,
			"sha256:0000000000000000000000000000000000000000000000000000000000000000", "unpaired surrogate",
		},
		"broken byte with no dataset to collide with": {
			`{"datasets":[{"name":"A","upstreams":[]},{"name":"d","upstreams":["ghost` + "\xfe" + `"]}]}`,
			"sha256:0000000000000000000000000000000000000000000000000000000000000000", "invalid UTF-8 bytes",
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			for _, placement := range []string{
				"plain",
				"big number at the snapshot top level",
				"big numbers nested in top-level unknown fields",
				"big number in an unknown graph field",
				"big numbers inside every dataset record",
			} {
				t.Run(placement, func(t *testing.T) {
					content := cliSnapshot(tc.id, tc.graph, placement)
					snapPath := writeRawFile(t, "snap.json", []byte(content))

					stdout, stderr, exit := captureStdout(t, func() int {
						return run([]string{"trace", snapPath, "d"})
					})
					if exit == 0 {
						t.Fatalf("trace exit = 0, want non-zero; stdout = %s", stdout)
					}
					if stdout != "" {
						t.Errorf("trace stdout = %q, want empty, never a replaced-root report", stdout)
					}
					for _, want := range []string{snapPath, `"upstreams"`, location, tc.reason} {
						if !strings.Contains(stderr, want) {
							t.Errorf("stderr = %q, want it to contain %q", stderr, want)
						}
					}
					for _, rejected := range []string{"does not match the graph", "does not exist in the final graph"} {
						if strings.Contains(stderr, rejected) {
							t.Errorf("stderr = %q, the snapshot must fail for the name encoding, not %q", stderr, rejected)
						}
					}
					if got := readFile(t, snapPath); got != content {
						t.Errorf("snapshot file bytes changed:\n got %s\nwant %s", got, content)
					}
					assertNoSideFiles(t, snapPath)
				})
			}
		})
	}
}

// TestCLITraceLegalUnicodeWithBigNumbersByteStable covers the legal-name
// guarantees end to end with the same unknown big-number content present:
// the genuine "�" and its equivalent escape, and correctly paired surrogate
// escapes, read as the same names whether written directly or escaped; a
// name that literally contains a backslash followed by ordinary letters is
// kept verbatim. Direct and escaped spellings, with or without the ignored
// numbers, produce byte-identical trace reports and content identifiers;
// compare between any two of them reports no differences.
func TestCLITraceLegalUnicodeWithBigNumbersByteStable(t *testing.T) {
	directGraph := `{"datasets":[{"name":"源�","upstreams":[]},{"name":"ledger😀","upstreams":["源�"]}]}`
	escapedGraph := `{"datasets":[{"name":"` + "\\u6e90\\ufffd" +
		`","upstreams":[]},{"name":"ledger` + "\\ud83d\\ude00" +
		`","upstreams":["` + "\\u6e90\\ufffd" + `"]}]}`
	if directGraph == escapedGraph {
		t.Fatal("test premise broken: direct and escaped spellings must differ on disk")
	}
	id := contentIDOfGraph(t, directGraph)

	placements := []string{
		"plain",
		"big number at the snapshot top level",
		"big numbers nested in top-level unknown fields",
		"big number in an unknown graph field",
		"big numbers inside every dataset record",
	}

	var baseTrace string
	for _, graph := range []struct {
		name  string
		value string
	}{
		{"direct", directGraph},
		{"escaped", escapedGraph},
	} {
		for _, placement := range placements {
			t.Run(graph.name+"/"+placement, func(t *testing.T) {
				content := cliSnapshot(id, graph.value, placement)
				snapPath := writeRawFile(t, "snap.json", []byte(content))

				stdout, stderr, exit := captureStdout(t, func() int {
					return run([]string{"trace", snapPath, "ledger😀"})
				})
				if exit != 0 {
					t.Fatalf("trace exit = %d, stderr = %s", exit, stderr)
				}
				if !strings.Contains(stdout, `"root": "源�"`) {
					t.Errorf("trace report = %s, want the genuine root 源�", stdout)
				}
				var report chainledger.TraceReport
				if err := json.Unmarshal([]byte(stdout), &report); err != nil {
					t.Fatalf("trace stdout is not valid JSON: %v\n%s", err, stdout)
				}
				if !reflect.DeepEqual(report.Sources, []chainledger.SourceTrace{
					{Root: "源�", Path: []string{"ledger😀", "源�"}},
				}) {
					t.Errorf("sources = %+v, want root 源� via [ledger😀 源�]", report.Sources)
				}
				if baseTrace == "" {
					baseTrace = stdout
				} else if stdout != baseTrace {
					t.Errorf("trace report differs from the first legal spelling:\n%s\n%s", stdout, baseTrace)
				}
				if got := readFile(t, snapPath); got != content {
					t.Errorf("trace changed the snapshot bytes")
				}
				assertNoSideFiles(t, snapPath)
			})
		}
	}

	// Every decorated snapshot is semantically identical to the plain one:
	// compare must report empty difference lists in both directions.
	plain := writeRawFile(t, "plain.json", []byte(cliSnapshot(id, directGraph, "plain")))
	for _, placement := range placements[1:] {
		t.Run("compare/"+placement, func(t *testing.T) {
			decorated := writeRawFile(t, "decorated.json", []byte(cliSnapshot(id, directGraph, placement)))
			for _, args := range [][]string{
				{"compare", plain, decorated},
				{"compare", decorated, plain},
			} {
				stdout, stderr, exit := captureStdout(t, func() int { return run(args) })
				if exit != 0 {
					t.Fatalf("%v exit = %d, stderr = %s", args, exit, stderr)
				}
				for _, nonEmpty := range []string{
					`"newDatasets": []`,
					`"removedDatasets": []`,
					`"changedDatasets": []`,
					`"addedRelations": []`,
					`"removedRelations": []`,
					`"rootSourceChanges": []`,
				} {
					if !strings.Contains(stdout, nonEmpty) {
						t.Errorf("%v report = %s, want %s", args, stdout, nonEmpty)
					}
				}
				if got := readFile(t, decorated); got != cliSnapshot(id, directGraph, placement) {
					t.Errorf("compare changed the decorated snapshot")
				}
				assertNoSideFiles(t, decorated)
				assertNoSideFiles(t, plain)
			}
		})
	}

	// A name genuinely containing a backslash and ordinary letters that only
	// looks like a surrogate escape is kept verbatim and is traceable.
	t.Run("literal backslash name", func(t *testing.T) {
		literalGraph := `{"datasets":[{"name":"ledger` + "\\ud83d\\ude00" +
			`","upstreams":[]},{"name":"x\\ud800","upstreams":["ledger` +
			"\\ud83d\\ude00" + `"]}]}`
		literalID := contentIDOfGraph(t, literalGraph)
		snapPath := writeRawFile(t, "snap.json",
			[]byte(cliSnapshot(literalID, literalGraph, "big numbers inside every dataset record")))
		stdout, stderr, exit := captureStdout(t, func() int {
			return run([]string{"trace", snapPath, `x\ud800`})
		})
		if exit != 0 {
			t.Fatalf("trace of a literal-backslash name failed: %s", stderr)
		}
		if !strings.Contains(stdout, `"root": "ledger😀"`) {
			t.Errorf("trace report = %s, want root ledger😀", stdout)
		}
		if !strings.Contains(stdout, `"x\\ud800"`) {
			t.Errorf("trace report = %s, want the literal name x\\ud800 kept verbatim", stdout)
		}
	})
}

// TestCLITraceBigNumbersDoNotChangeReports: over a multi-root diamond, a
// legal snapshot carrying big numbers in every unknown-field placement
// answers the same queries with byte-identical reports, and neither trace
// nor compare alters the snapshot file or creates siblings.
func TestCLITraceBigNumbersDoNotChangeReports(t *testing.T) {
	id := contentIDOfGraph(t, traceGraph)
	placements := []string{
		"plain",
		"big number at the snapshot top level",
		"big numbers nested in top-level unknown fields",
		"big number in an unknown graph field",
		"big numbers inside every dataset record",
	}
	base := make(map[string]string)
	plainPath := writeRawFile(t, "plain.json", []byte(cliSnapshot(id, traceGraph, "plain")))
	for _, query := range []string{"T", "A", "R"} {
		out, stderr, exit := captureStdout(t, func() int { return run([]string{"trace", plainPath, query}) })
		if exit != 0 {
			t.Fatalf("base trace %q: %s", query, stderr)
		}
		base[query] = out
	}

	for _, placement := range placements[1:] {
		t.Run(placement, func(t *testing.T) {
			content := cliSnapshot(id, traceGraph, placement)
			snapPath := writeRawFile(t, "snap.json", []byte(content))
			for _, query := range []string{"T", "A", "R"} {
				out, stderr, exit := captureStdout(t, func() int { return run([]string{"trace", snapPath, query}) })
				if exit != 0 {
					t.Fatalf("trace %q exit = %d, stderr = %s", query, exit, stderr)
				}
				if out != base[query] {
					t.Errorf("trace %q with %s differs:\n got %s\nwant %s", query, placement, out, base[query])
				}
			}
			if got := readFile(t, snapPath); got != content {
				t.Errorf("trace changed the snapshot bytes")
			}
			// Self-compare of the decorated snapshot reports no differences.
			cmpOut, cmpErr, cmpExit := captureStdout(t, func() int {
				return run([]string{"compare", plainPath, snapPath})
			})
			if cmpExit != 0 {
				t.Fatalf("compare plain vs decorated failed: %s", cmpErr)
			}
			if !strings.Contains(cmpOut, `"rootSourceChanges": []`) {
				t.Errorf("compare report = %s, want no root source changes", cmpOut)
			}
			assertNoSideFiles(t, snapPath)
			assertNoSideFiles(t, plainPath)
		})
	}

	// Snapshot produced by the snapshot command itself stays byte-identical
	// to the hand-written plain document's trace: the frozen semantics, not
	// the file's other content, determine the report.
	commandPath := snapshotForTrace(t, traceGraph)
	out, stderr, exit := captureStdout(t, func() int { return run([]string{"trace", commandPath, "T"}) })
	if exit != 0 {
		t.Fatalf("trace over command-produced snapshot: %s", stderr)
	}
	if out != base["T"] {
		t.Errorf("trace over the command-produced snapshot differs:\n%s\n%s", out, base["T"])
	}
}
