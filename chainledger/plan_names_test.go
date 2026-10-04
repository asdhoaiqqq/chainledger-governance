package chainledger

import (
	"errors"
	"reflect"
	"strings"
	"testing"
)

// These tests pin the faithful-reading gate for adjustment plan names: the
// JSON decoder silently rewrites invalid UTF-8 bytes and unpaired \u
// surrogate escapes to U+FFFD, so a corrupted plan name could collapse onto
// the name of a real dataset (a removals entry of "源" plus one invalid byte
// would decode to "源�" and delete that dataset). UnmarshalPlan must refuse
// such plans as a whole instead of guessing which dataset was meant.

// mustParsePlan parses a plan that must be accepted.
func mustParsePlan(t *testing.T, data []byte) Plan {
	t.Helper()
	p, err := UnmarshalPlan(data)
	if err != nil {
		t.Fatalf("UnmarshalPlan(%s) rejected a legal plan: %v", data, err)
	}
	return p
}

// mustRejectPlan parses a plan that must be refused, returning the error.
func mustRejectPlan(t *testing.T, data []byte) error {
	t.Helper()
	p, err := UnmarshalPlan(data)
	if err == nil {
		t.Fatalf("UnmarshalPlan(%s) accepted a corrupted plan: %+v", data, p)
	}
	if !errors.Is(err, ErrInvalidArgument) {
		t.Fatalf("UnmarshalPlan(%s) err = %v, want errors.Is ErrInvalidArgument", data, err)
	}
	return err
}

// Invalid UTF-8 bytes are rejected wherever an adjustment name appears, and
// the error names the field and the zero-based record or item index.
func TestUnmarshalPlanRejectsInvalidUTF8Names(t *testing.T) {
	cases := []struct {
		name     string
		plan     []byte
		wantText []string
	}{
		{
			name:     "change name, first record",
			plan:     []byte("{\"changes\": [{\"name\": \"源\xff\", \"upstreams\": []}]}"),
			wantText: []string{`field "name"`, `change record at index 0`, `"changes"`, `invalid UTF-8 byte 0xff`},
		},
		{
			name:     "change name, later record",
			plan:     []byte("{\"changes\": [{\"name\": \"ok\", \"upstreams\": []}, {\"name\": \"a\xfe\", \"upstreams\": []}]}"),
			wantText: []string{`field "name"`, `change record at index 1`, `0xfe`},
		},
		{
			name:     "upstream item",
			plan:     []byte("{\"changes\": [{\"name\": \"down\", \"upstreams\": [\"ok\", \"up\xc0\xaf\"]}]}"),
			wantText: []string{`item at index 1`, `field "upstreams"`, `change record at index 0`, `0xc0`},
		},
		{
			name:     "removals item",
			plan:     []byte("{\"removals\": [\"gone\", \"源\xff\"]}"),
			wantText: []string{`item at index 1`, `field "removals"`, `0xff`},
		},
		{
			name:     "removals item that would be a no-op delete",
			plan:     []byte("{\"removals\": [\"ghost\xff\"]}"),
			wantText: []string{`field "removals"`, `invalid UTF-8 byte`},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := mustRejectPlan(t, tc.plan)
			for _, want := range tc.wantText {
				if !strings.Contains(err.Error(), want) {
					t.Fatalf("error %q does not contain %q", err, want)
				}
			}
			// The raw byte must be reported, never its U+FFFD replacement.
			if strings.Contains(err.Error(), "�") {
				t.Fatalf("error shows the replacement character instead of the raw byte: %q", err)
			}
		})
	}
}

// Unpaired surrogate escapes are rejected and reported as surrogate errors,
// distinct from invalid bytes; correctly paired escapes stay legal.
func TestUnmarshalPlanRejectsUnpairedSurrogates(t *testing.T) {
	rejected := []struct {
		name string
		plan string
		want []string
	}{
		{"lone high surrogate", `{"changes": [{"name": "源\ud800", "upstreams": []}]}`, []string{"unpaired high surrogate", `\ud800`}},
		{"high surrogate at end of removals name", `{"removals": ["\udbff"]}`, []string{"unpaired high surrogate", `\udbff`, `field "removals"`}},
		{"high surrogate followed by ordinary escape", `{"removals": ["\ud800\n"]}`, []string{"unpaired high surrogate"}},
		{"high surrogate followed by non-surrogate escape", `{"removals": ["\ud800A"]}`, []string{"unpaired high surrogate"}},
		{"high surrogate followed by another high", `{"removals": ["\ud800\ud801"]}`, []string{"unpaired high surrogate"}},
		{"lone low surrogate", `{"changes": [{"name": "ok", "upstreams": ["\udc00"]}]}`, []string{"unpaired low surrogate", `\udc00`, `field "upstreams"`}},
		{"low surrogate before high", `{"removals": ["\udc00\ud800"]}`, []string{"unpaired low surrogate"}},
	}
	for _, tc := range rejected {
		t.Run(tc.name, func(t *testing.T) {
			err := mustRejectPlan(t, []byte(tc.plan))
			for _, want := range tc.want {
				if !strings.Contains(err.Error(), want) {
					t.Fatalf("error %q does not contain %q", err, want)
				}
			}
			if strings.Contains(err.Error(), "invalid UTF-8 byte") {
				t.Fatalf("surrogate corruption must not be reported as an invalid byte: %q", err)
			}
		})
	}
}

// A correctly paired surrogate escape expresses a legal astral name and is
// accepted, decoding to the same dataset as the literal character.
func TestUnmarshalPlanAcceptsPairedSurrogateEscape(t *testing.T) {
	literal := mustParsePlan(t, []byte(`{"changes": [{"name": "ledger😀", "upstreams": []}]}`))
	escaped := mustParsePlan(t, []byte(`{"changes": [{"name": "ledger\ud83d\ude00", "upstreams": []}]}`))
	if !reflect.DeepEqual(literal, escaped) {
		t.Fatalf("paired escape decoded differently from the literal:\n%#v\n%#v", literal, escaped)
	}
	if escaped.Changes[0].Name != "ledger😀" {
		t.Fatalf("paired surrogate escape decoded to %q, want %q", escaped.Changes[0].Name, "ledger😀")
	}
}

// A genuine U+FFFD — literal or "�" — is valid UTF-8 and stays a legal
// name; both spellings denote the same dataset.
func TestUnmarshalPlanAcceptsGenuineReplacementChar(t *testing.T) {
	literal := mustParsePlan(t, []byte(`{"removals": ["源�"]}`))
	escaped := mustParsePlan(t, []byte(`{"removals": ["源\ufffd"]}`))
	if literal.Removals[0] != "源�" || escaped.Removals[0] != "源�" {
		t.Fatalf("genuine replacement char not kept: %q, %q", literal.Removals[0], escaped.Removals[0])
	}
	if !reflect.DeepEqual(literal, escaped) {
		t.Fatalf("literal and � escape decoded differently: %#v vs %#v", literal, escaped)
	}
}

// Legal names keep every other Unicode shape verbatim: CJK, emoji, combining
// marks, casing, and surrounding spaces, with no replacement or
// normalization. Literal text and its equivalent JSON escapes denote the same
// dataset, and a backslash followed by an ordinary "u" is just text.
func TestUnmarshalPlanKeepsLegalNamesVerbatim(t *testing.T) {
	plan := mustParsePlan(t, []byte(`{"changes": [{"name": "数据集", "upstreams": ["é", "é", "Ledger", "ledger", " spaced ", "a\\u"]}], "removals": ["ledger😀"]}`))
	wantUpstreams := []string{"é", "é", "Ledger", "ledger", " spaced ", `a\u`}
	if !reflect.DeepEqual(plan.Changes[0].Upstreams, wantUpstreams) {
		t.Fatalf("upstreams changed: got %#v, want %#v", plan.Changes[0].Upstreams, wantUpstreams)
	}
	if plan.Changes[0].Name != "数据集" || plan.Removals[0] != "ledger😀" {
		t.Fatalf("names changed: %#v, %#v", plan.Changes[0].Name, plan.Removals[0])
	}

	// "é" (escaped) and "é" (literal) are the same dataset: the two
	// spellings must resolve to one identical name.
	escaped := mustParsePlan(t, []byte(`{"changes": [{"name": "\u00e9", "upstreams": []}]}`))
	literal := mustParsePlan(t, []byte(`{"changes": [{"name": "é", "upstreams": []}]}`))
	if escaped.Changes[0].Name != literal.Changes[0].Name {
		t.Fatalf("escaped and literal spellings decoded to different names: %q vs %q",
			escaped.Changes[0].Name, literal.Changes[0].Name)
	}
}

// Strings inside unknown fields are not adjustment names: they keep the
// ignore-everything behavior and are never inspected by the gate, however
// corrupt their bytes are.
func TestUnmarshalPlanIgnoresUnknownFieldStrings(t *testing.T) {
	plan := mustParsePlan(t, []byte("{\"changes\": [{\"name\": \"A\", \"upstreams\": [], \"note\": \"源\xff\", \"meta\": {\"bad\": \"\\ud800\", \"list\": [\"\xfe\"]}}], \"unknown\": [\"\xff\"], \"removals\": []}"))
	if plan.Changes[0].Name != "A" {
		t.Fatalf("plan changed: %#v", plan)
	}
}

// A corrupted name anywhere refuses the whole plan; the legal adjustments in
// the same batch must not take effect either.
func TestUnmarshalPlanCorruptNameRefusesWholeBatch(t *testing.T) {
	graph, err := UnmarshalGraphFile([]byte(`{"datasets": [{"name": "源�", "upstreams": []}, {"name": "keep", "upstreams": []}]}`))
	if err != nil {
		t.Fatalf("UnmarshalGraphFile: %v", err)
	}
	before := snapshotGraph(graph)

	// One legal change plus one removals entry whose raw bytes are invalid:
	// the batch must be refused at read time, so nothing can be applied.
	_, err = UnmarshalPlan([]byte("{\"changes\": [{\"name\": \"keep\", \"upstreams\": [\"源�\"]}], \"removals\": [\"源\xff\"]}"))
	if err == nil {
		t.Fatal("plan with a corrupted removals name was accepted")
	}
	if _, err := PreviewBatch(graph, Plan{Changes: []PlanChange{{Name: "keep", Upstreams: []string{"源�"}}}}); err != nil {
		t.Fatalf("sanity: the legal change alone previews fine: %v", err)
	}
	if after := snapshotGraph(graph); !reflect.DeepEqual(after, before) {
		t.Fatalf("graph changed while a corrupted plan was refused")
	}
}

// The gate never changes how structurally invalid plans fail: malformed JSON
// and type errors still come from the regular parse.
func TestUnmarshalPlanStructuralErrorsUnchanged(t *testing.T) {
	cases := map[string]string{
		"malformed JSON":         `{"changes": [{"name": "a\ud800"`,
		"name of wrong type":     `{"changes": [{"name": 5, "upstreams": []}]}`,
		"upstream of wrong type": `{"changes": [{"name": "a", "upstreams": [7]}]}`,
		"changes not an array":   `{"changes": {"name": "a"}}`,
	}
	for name, doc := range cases {
		t.Run(name, func(t *testing.T) {
			if p, err := UnmarshalPlan([]byte(doc)); err == nil {
				t.Fatalf("UnmarshalPlan(%s) succeeded: %+v", doc, p)
			}
		})
	}
}
