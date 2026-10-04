package main

import (
	"strings"
	"testing"
)

// The CLI must refuse a plan whose adjustment names cannot be read
// faithfully: a raw invalid UTF-8 byte or an unpaired \u surrogate escape
// would be silently rewritten to U+FFFD by the decoder and could collapse
// onto the name of a real dataset. Preview and apply reject identically —
// non-zero exit, empty stdout, stderr naming the plan file, the field, and
// the zero-based record or item index — and neither the graph file nor the
// plan file is touched.

// TestCLIPlanCorruptNamesRejected runs preview and apply over plans whose
// names carry invalid UTF-8 bytes or unpaired surrogate escapes.
func TestCLIPlanCorruptNamesRejected(t *testing.T) {
	graphData := `{"datasets":[{"name":"源�","upstreams":[]},{"name":"keep","upstreams":[]}]}`
	plans := map[string]struct {
		data     string
		wantText []string
	}{
		// The scenario the gate exists for: the removals entry decodes to
		// "源�", which IS a dataset in the graph; the raw bytes are corrupt
		// and the plan must be refused instead of deleting that dataset.
		"invalid byte in removals collapses onto real dataset": {
			"{\"removals\":[\"源\xff\"]}",
			[]string{`field "removals"`, "item at index 0", "invalid UTF-8 byte 0xff"},
		},
		"invalid byte in change name": {
			"{\"changes\":[{\"name\":\"a\xfe\",\"upstreams\":[]}]}",
			[]string{`field "name"`, "change record at index 0", "invalid UTF-8 byte 0xfe"},
		},
		"invalid byte in upstream item": {
			"{\"changes\":[{\"name\":\"keep\",\"upstreams\":[\"ok\",\"up\xff\"]}]}",
			[]string{`field "upstreams"`, "item at index 1", "change record at index 0", "invalid UTF-8 byte"},
		},
		"lone high surrogate escape": {
			`{"changes":[{"name":"源\ud800","upstreams":[]}]}`,
			[]string{`field "name"`, "change record at index 0", "unpaired high surrogate", `\ud800`},
		},
		"lone low surrogate escape in removals": {
			`{"removals":["ok","\udc00"]}`,
			[]string{`field "removals"`, "item at index 1", "unpaired low surrogate"},
		},
		"low surrogate before high": {
			`{"removals":["\udc00\ud800"]}`,
			[]string{"unpaired low surrogate"},
		},
	}

	for name, tc := range plans {
		t.Run(name, func(t *testing.T) {
			graphPath := writeFile(t, "graph.json", graphData)
			planPath := writeFile(t, "plan.json", tc.data)
			graphBefore := readFile(t, graphPath)
			planBefore := readFile(t, planPath)

			for _, verb := range []string{"preview", "apply"} {
				stdout, stderr, exit := captureStdout(t, func() int {
					return run([]string{verb, graphPath, planPath})
				})
				if exit == 0 {
					t.Fatalf("%s succeeded on a corrupted plan, stdout = %s", verb, stdout)
				}
				if stdout != "" {
					t.Errorf("%s stdout = %q, want empty on rejection", verb, stdout)
				}
				if !strings.Contains(stderr, planPath) {
					t.Errorf("%s stderr = %q, want it to name the plan file %q", verb, stderr, planPath)
				}
				for _, want := range tc.wantText {
					if !strings.Contains(stderr, want) {
						t.Errorf("%s stderr = %q, want it to contain %q", verb, stderr, want)
					}
				}
			}

			if got := readFile(t, graphPath); got != graphBefore {
				t.Errorf("graph file changed after rejected plan:\n got %s\nwant %s", got, graphBefore)
			}
			if got := readFile(t, planPath); got != planBefore {
				t.Errorf("plan file changed after rejected plan:\n got %s\nwant %s", got, planBefore)
			}
		})
	}
}

// A corrupted name anywhere refuses the whole batch: the legal adjustments in
// the same plan must not take effect either, and the graph keeps every node —
// including the real "源�" dataset the corrupted removals entry decoded to.
func TestCLIApplyCorruptPlanAppliesNothing(t *testing.T) {
	graphPath := writeFile(t, "graph.json", `{"datasets":[{"name":"源�","upstreams":[]},{"name":"keep","upstreams":[]}]}`)
	// One legal change (keep derives from 源�) plus one corrupted removals
	// entry; the whole batch must be refused.
	planPath := writeFile(t, "plan.json", "{\"changes\":[{\"name\":\"keep\",\"upstreams\":[\"源�\"]}],\"removals\":[\"源\xff\"]}")
	graphBefore := readFile(t, graphPath)

	stdout, stderr, exit := captureStdout(t, func() int {
		return run([]string{"apply", graphPath, planPath})
	})
	if exit == 0 {
		t.Fatalf("apply succeeded on a corrupted plan, stdout = %s", stdout)
	}
	if stdout != "" {
		t.Errorf("apply stdout = %q, want empty", stdout)
	}
	if !strings.Contains(stderr, "invalid UTF-8 byte") {
		t.Errorf("apply stderr = %q, want invalid-byte error", stderr)
	}
	if got := readFile(t, graphPath); got != graphBefore {
		t.Errorf("graph file changed although the batch was refused:\n got %s\nwant %s", got, graphBefore)
	}
}

// Legal names keep working end to end: a genuine U+FFFD written literally or
// as the "�" escape targets the same dataset and yields the same report,
// and a backslash followed by an ordinary "u" is just a name.
func TestCLIPlanLegalUnicodeNamesAccepted(t *testing.T) {
	graphPath := writeFile(t, "graph.json", `{"datasets":[{"name":"源�","upstreams":[]},{"name":"a\\u","upstreams":[]}]}`)

	literalPlan := writeFile(t, "literal.json", `{"changes":[{"name":"源�","upstreams":["a\\u"]}]}`)
	escapedPlan := writeFile(t, "escaped.json", `{"changes":[{"name":"源\ufffd","upstreams":["a\\u"]}]}`)

	literalOut, literalErr, literalExit := captureStdout(t, func() int {
		return run([]string{"preview", graphPath, literalPlan})
	})
	escapedOut, escapedErr, escapedExit := captureStdout(t, func() int {
		return run([]string{"preview", graphPath, escapedPlan})
	})
	if literalExit != 0 {
		t.Fatalf("preview of literal-� plan failed: exit %d, stderr %s", literalExit, literalErr)
	}
	if escapedExit != 0 {
		t.Fatalf("preview of escaped-� plan failed: exit %d, stderr %s", escapedExit, escapedErr)
	}
	if literalOut != escapedOut {
		t.Errorf("literal and escaped spellings produced different reports:\n%s\n%s", literalOut, escapedOut)
	}
	if !strings.Contains(literalOut, `"changedDatasets"`) {
		t.Errorf("report missing the change to 源�: %s", literalOut)
	}
}

// Strings inside unknown fields are not adjustment names: corrupt bytes
// there are ignored exactly as before and the plan still applies.
func TestCLIPlanUnknownFieldStringsNotInspected(t *testing.T) {
	graphPath := writeFile(t, "graph.json", `{"datasets":[{"name":"A","upstreams":[]}]}`)
	planPath := writeFile(t, "plan.json", "{\"changes\":[{\"name\":\"A\",\"upstreams\":[],\"note\":\"源\xff\"}],\"unknown\":[\"\\ud800\"]}")

	stdout, stderr, exit := captureStdout(t, func() int {
		return run([]string{"preview", graphPath, planPath})
	})
	if exit != 0 {
		t.Fatalf("preview rejected a plan whose only corrupt strings sit in unknown fields: exit %d, stderr %s", exit, stderr)
	}
	if !strings.Contains(stdout, `"finalGraph"`) {
		t.Errorf("preview stdout missing report: %s", stdout)
	}
}
