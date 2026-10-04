package chainledger

import (
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
)

// These tests pin the rule that a plan's raw name literals must decode
// exactly as written. encoding/json silently rewrites invalid UTF-8 bytes
// and unpaired \u surrogate escapes to U+FFFD, so a corrupted name could
// collide with a genuinely different dataset ("源" plus a broken byte would
// delete the dataset really named "源�"). The reader must refuse the whole
// plan instead of guessing which dataset the user meant.

// TestUnmarshalPlanRejectsCorruptNameBytes: a raw string literal carrying
// bytes that are not valid UTF-8 rejects the whole plan, in every position
// that takes part in the adjustment — a change record's name, any upstreams
// entry, and any removals entry. The error names the field and the
// zero-based record or array position and says the bytes are invalid.
func TestUnmarshalPlanRejectsCorruptNameBytes(t *testing.T) {
	cases := map[string]struct {
		data     string
		field    string
		location string
	}{
		"change name": {
			`{"changes":[{"name":"source` + "\xff" + `","upstreams":[]}]}`,
			`"name"`, `index 0 of "changes"`,
		},
		"change name in second record": {
			`{"changes":[{"name":"A","upstreams":[]},{"name":"b` + "\xfe" + `","upstreams":[]}]}`,
			`"name"`, `index 1 of "changes"`,
		},
		"upstream entry": {
			`{"changes":[{"name":"A","upstreams":["ok","g` + "\xfe" + `"]}]}`,
			`"upstreams"`, `index 1`,
		},
		"upstream entry in second record": {
			`{"changes":[{"name":"A","upstreams":[]},{"name":"B","upstreams":["A","x` + "\xff" + `"]}]}`,
			`"upstreams"`, `index 1`,
		},
		"removals entry": {
			`{"removals":["a","b","c` + "\xc3" + `"]}`,
			`"removals"`, `index 2`,
		},
		"removals entry naming a dataset that does not exist": {
			// Even a name that would only delete a nonexistent dataset is
			// refused: the reader must not treat the corruption as a no-op.
			`{"removals":["ghost` + "\xff" + `"]}`,
			`"removals"`, `index 0`,
		},
		"case-folded known field is still checked": {
			`{"CHANGES":[{"NAME":"a` + "\xff" + `"}]}`,
			`"name"`, `index 0 of "changes"`,
		},
		"escaped key spelling is still checked": {
			`{"changes":[{"na\u006de":"a` + "\xff" + `"}]}`,
			`"name"`, `index 0 of "changes"`,
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := UnmarshalPlan([]byte(tc.data))
			if !errors.Is(err, ErrInvalidArgument) {
				t.Fatalf("err = %v, want ErrInvalidArgument", err)
			}
			msg := err.Error()
			if !strings.Contains(msg, "invalid UTF-8 bytes") {
				t.Errorf("err = %q, want it to say the bytes are invalid UTF-8", msg)
			}
			if strings.Contains(msg, "surrogate") {
				t.Errorf("err = %q, byte corruption must not be reported as a surrogate problem", msg)
			}
			if !strings.Contains(msg, tc.field) {
				t.Errorf("err = %q, want it to name field %s", msg, tc.field)
			}
			if !strings.Contains(msg, tc.location) {
				t.Errorf("err = %q, want it to locate %s", msg, tc.location)
			}
			if strings.Contains(msg, "�") {
				t.Errorf("err = %q, must not contain the replacement character", msg)
			}
		})
	}
}

// TestUnmarshalPlanRejectsUnpairedSurrogates: a \u escape forming an
// unpaired surrogate — a lone high or low surrogate, a low surrogate before
// its high surrogate, or a high surrogate not immediately followed by its
// low surrogate — rejects the whole plan, in every name position. The error
// says the surrogate escape is unpaired and locates the field.
func TestUnmarshalPlanRejectsUnpairedSurrogates(t *testing.T) {
	cases := map[string]struct {
		data     string
		field    string
		location string
	}{
		"lone high surrogate in name": {
			`{"changes":[{"name":"a\ud800x","upstreams":[]}]}`,
			`"name"`, `index 0 of "changes"`,
		},
		"high surrogate at end of name": {
			`{"changes":[{"name":"a\ud800","upstreams":[]}]}`,
			`"name"`, `index 0 of "changes"`,
		},
		"lone low surrogate in name": {
			`{"changes":[{"name":"a\udc00","upstreams":[]}]}`,
			`"name"`, `index 0 of "changes"`,
		},
		"low surrogate before high surrogate": {
			`{"changes":[{"name":"a\udc00\ud800","upstreams":[]}]}`,
			`"name"`, `index 0 of "changes"`,
		},
		"high surrogate then non-surrogate escape": {
			`{"changes":[{"name":"a\ud800\u0041","upstreams":[]}]}`,
			`"name"`, `index 0 of "changes"`,
		},
		"high surrogate then another high surrogate": {
			`{"changes":[{"name":"a\ud800\ud801","upstreams":[]}]}`,
			`"name"`, `index 0 of "changes"`,
		},
		"surrogate in second change record": {
			`{"changes":[{"name":"A","upstreams":[]},{"name":"\ud800","upstreams":[]}]}`,
			`"name"`, `index 1 of "changes"`,
		},
		"surrogate in upstream entry": {
			`{"changes":[{"name":"A","upstreams":["ok","\udfff"]}]}`,
			`"upstreams"`, `index 1`,
		},
		"surrogate in removals entry": {
			`{"removals":["ok","\ud800"]}`,
			`"removals"`, `index 1`,
		},
		"surrogate naming a dataset that does not exist": {
			`{"removals":["ghost\ud800"]}`,
			`"removals"`, `index 0`,
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			// Premise: the decoder really would silently rewrite these to
			// U+FFFD instead of failing, which is why the raw check exists.
			var decoded struct {
				Changes []struct {
					Name string `json:"name"`
				} `json:"changes"`
			}
			_ = json.Unmarshal([]byte(tc.data), &decoded)

			_, err := UnmarshalPlan([]byte(tc.data))
			if !errors.Is(err, ErrInvalidArgument) {
				t.Fatalf("err = %v, want ErrInvalidArgument", err)
			}
			msg := err.Error()
			if !strings.Contains(msg, "unpaired surrogate") {
				t.Errorf("err = %q, want it to say the surrogate escape is unpaired", msg)
			}
			if strings.Contains(msg, "invalid UTF-8 bytes") {
				t.Errorf("err = %q, a surrogate problem must not be reported as invalid bytes", msg)
			}
			if !strings.Contains(msg, tc.field) {
				t.Errorf("err = %q, want it to name field %s", msg, tc.field)
			}
			if !strings.Contains(msg, tc.location) {
				t.Errorf("err = %q, want it to locate %s", msg, tc.location)
			}
		})
	}
}

// TestUnmarshalPlanCorruptNameRejectsWholePlan: one corrupt name refuses
// the entire plan — the other, perfectly legal adjustments in the same
// batch must not take effect either.
func TestUnmarshalPlanCorruptNameRejectsWholePlan(t *testing.T) {
	data := `{"changes":[{"name":"A","upstreams":[]},{"name":"b` + "\xff" + `","upstreams":[]}],"removals":["C"]}`
	if _, err := UnmarshalPlan([]byte(data)); err == nil {
		t.Fatal("a plan mixing legal records with one corrupt name must be rejected as a whole")
	}
}

// TestUnmarshalPlanAcceptsGenuineUnicodeNames: names that are legal stay
// byte-for-byte intact — a genuine replacement character (written directly
// or as its escape), correctly paired surrogate escapes, CJK, combining
// marks, casing, surrounding spaces, and names that merely look like
// escapes (a backslash before an ordinary letter).
func TestUnmarshalPlanAcceptsGenuineUnicodeNames(t *testing.T) {
	cases := map[string]struct {
		data string
		want string
	}{
		"genuine replacement character": {
			`{"changes":[{"name":"源�","upstreams":[]}]}`,
			"源�",
		},
		"replacement character via escape": {
			`{"changes":[{"name":"源\ufffd","upstreams":[]}]}`,
			"源�",
		},
		"paired surrogate escape": {
			`{"changes":[{"name":"ledger\ud83d\ude00","upstreams":[]}]}`,
			"ledger😀",
		},
		"paired surrogate at string start and end": {
			`{"changes":[{"name":"\ud83d\ude00x\ud83c\udf10","upstreams":[]}]}`,
			"😀x🌐",
		},
		"cjk combining marks casing and spaces": {
			`{"changes":[{"name":" 数据 Éé ","upstreams":[]}]}`,
			" 数据 Éé ",
		},
		"backslash before ordinary letter": {
			`{"changes":[{"name":"a\\nb","upstreams":[]}]}`,
			`a\nb`,
		},
		"backslash before what looks like a surrogate escape": {
			// The name really contains backslash, 'u', 'd', '8', '0', '0';
			// it must not be rejected for looking like an escape.
			`{"changes":[{"name":"x\\ud800","upstreams":[]}]}`,
			`x\ud800`,
		},
		"escaped backslash then real escape": {
			`{"changes":[{"name":"x\\\nud800","upstreams":[]}]}`,
			"x\\\nud800",
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			plan, err := UnmarshalPlan([]byte(tc.data))
			if err != nil {
				t.Fatalf("UnmarshalPlan rejected a legal name: %v", err)
			}
			if len(plan.Changes) != 1 || plan.Changes[0].Name != tc.want {
				t.Fatalf("name decoded to %q, want %q kept verbatim", plan.Changes, tc.want)
			}
		})
	}
}

// TestUnmarshalPlanIgnoresCorruptStringsInUnknownFields: unknown fields and
// everything nested inside them keep their ignore-everything behavior —
// their strings never take part in the adjustment and are not checked, even
// when they spell a known field name one level down.
func TestUnmarshalPlanIgnoresCorruptStringsInUnknownFields(t *testing.T) {
	cases := map[string]string{
		"corrupt string in unknown top-level field": `{"meta":"a\ud800","changes":[{"name":"A","upstreams":[]}]}`,
		"corrupt bytes in unknown record field":     `{"changes":[{"name":"A","note":"x` + "\xff" + `","upstreams":[]}]}`,
		"known field names nested inside unknown field": `{"also":{"changes":[{"name":"\ud800"}],"removals":["g` + "\xff" + `"]},` +
			`"changes":[{"name":"A","upstreams":[]}]}`,
		"deeply nested corrupt strings in unknown field": `{"changes":[{"name":"A","upstreams":[],"extra":{"deep":["\udc00",{"k":"v` + "\xfe" + `"}]}}]}`,
		"unknown field that merely prefixes a known one": `{"changesX":[{"name":"\ud800"}],"removals2":["a` + "\xff" + `"],"changes":[]}`,
	}
	for name, data := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := UnmarshalPlan([]byte(data)); err != nil {
				t.Fatalf("unknown-field strings must not be checked, got %v", err)
			}
		})
	}
}

// TestUnmarshalPlanEquivalentEscapesSameDataset: legal text written
// directly and the same text written with equivalent JSON escapes name the
// same dataset and produce the same report.
func TestUnmarshalPlanEquivalentEscapesSameDataset(t *testing.T) {
	graphData := `{"datasets":[{"name":"数据集","upstreams":[]},{"name":"源�","upstreams":[]}]}`
	graph, err := UnmarshalGraphFile([]byte(graphData))
	if err != nil {
		t.Fatalf("UnmarshalGraphFile: %v", err)
	}

	direct := `{"changes":[{"name":"数据集","upstreams":["源�"]}],"removals":["旧�"]}`
	escaped := `{"changes":[{"name":"\u6570\u636e\u96c6","upstreams":["\u6e90\ufffd"]}],"removals":["\u65e7\ufffd"]}`
	if direct == escaped {
		t.Fatal("test premise broken: the two spellings must differ as text")
	}

	planDirect, err := UnmarshalPlan([]byte(direct))
	if err != nil {
		t.Fatalf("direct spelling rejected: %v", err)
	}
	planEscaped, err := UnmarshalPlan([]byte(escaped))
	if err != nil {
		t.Fatalf("escaped spelling rejected: %v", err)
	}
	if !reflect.DeepEqual(planDirect, planEscaped) {
		t.Fatalf("equivalent spellings decoded differently:\n%+v\n%+v", planDirect, planEscaped)
	}

	reportDirect, err := PreviewBatch(graph, planDirect)
	if err != nil {
		t.Fatalf("PreviewBatch direct: %v", err)
	}
	reportEscaped, err := PreviewBatch(graph, planEscaped)
	if err != nil {
		t.Fatalf("PreviewBatch escaped: %v", err)
	}
	jDirect, _ := json.Marshal(reportDirect)
	jEscaped, _ := json.Marshal(reportEscaped)
	if string(jDirect) != string(jEscaped) {
		t.Fatalf("reports differ between spellings:\n%s\n%s", jDirect, jEscaped)
	}
	if len(reportDirect.ChangedDatasets) != 1 || reportDirect.ChangedDatasets[0] != "数据集" {
		t.Fatalf("expected 数据集 to be the changed dataset, got %v", reportDirect.ChangedDatasets)
	}
}

// TestUnmarshalPlanCorruptNameNeverTouchesGraph: a plan rejected for a
// corrupt name leaves the graph it would have been applied to completely
// unchanged — including when the corrupt name could have deleted a real
// dataset after the decoder's silent rewrite.
func TestUnmarshalPlanCorruptNameNeverTouchesGraph(t *testing.T) {
	graphData := `{"datasets":[{"name":"源�","upstreams":[]},{"name":"down","upstreams":["源�"]}]}`
	graph, err := UnmarshalGraphFile([]byte(graphData))
	if err != nil {
		t.Fatalf("UnmarshalGraphFile: %v", err)
	}
	before := snapshotGraph(graph)

	// "源" plus one broken byte decodes to the real dataset "源�": accepting
	// it would delete a dataset the user never named.
	if _, err := UnmarshalPlan([]byte(`{"removals":["源` + "\xff" + `"]}`)); err == nil {
		t.Fatal("corrupt removal name must be rejected")
	}
	if after := snapshotGraph(graph); !reflect.DeepEqual(after, before) {
		t.Fatalf("graph changed while reading a rejected plan:\nbefore %#v\nafter  %#v", before, after)
	}
}
