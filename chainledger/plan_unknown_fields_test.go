package chainledger

import (
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
)

// These tests pin the interaction between the plan reader's name-encoding
// rule (name_encoding.go) and the unknown fields a plan is allowed to carry.
// Unknown fields are additional information the reader ignores — but their
// VALUES can be legal JSON numbers outside the float64 range (such as
// 1e400), possibly buried in unknown objects and arrays. A scan that decoded
// numbers to float64 would error out on such a value and abort, so a big
// number placed before a corrupt name would hide the corruption: the plan
// would fall through to the regular parse, the decoder would silently
// rewrite the name to U+FFFD, and a removals entry could then delete a
// dataset the user never named. Wherever the big number appears — at the
// start of the plan, nested in unknown containers, or inside a change record
// just before a name — every name that takes part in the adjustment must
// still be validated against its raw literal, and a fully legal plan must
// behave exactly as if the unknown fields were not there.

// hugeNumber is a legal JSON number outside the float64 range.
const hugeNumber = `1e400`

// TestUnmarshalPlanHugeNumberPremise pins why the raw scans must decode with
// UseNumber: 1e400 is perfectly legal JSON, yet any walk that converts
// numbers to float64 fails on it. If the name-encoding scan ever dropped
// UseNumber, every test in this file that places the number before a corrupt
// name would start failing because the corruption would no longer be
// reported.
func TestUnmarshalPlanHugeNumberPremise(t *testing.T) {
	if !json.Valid([]byte(`{"meta":` + hugeNumber + `}`)) {
		t.Fatal("test premise broken: 1e400 must be legal JSON")
	}
	var f float64
	if err := json.Unmarshal([]byte(hugeNumber), &f); err == nil {
		t.Fatal("test premise broken: 1e400 must overflow float64 decoding")
	}
	var v interface{}
	if err := json.Unmarshal([]byte(`{"meta":`+hugeNumber+`}`), &v); err == nil {
		t.Fatal("test premise broken: 1e400 must fail a float64-converting walk")
	}
}

// TestUnmarshalPlanHugeNumbersNeverHideCorruptNames: a legal but oversized
// number in an ignored unknown field — at the start of the plan, nested in
// unknown objects and arrays, or inside a change record right before the
// name — must not abort the name-encoding scan. A corrupt name appearing
// after it is still reported as corrupt: the error names the field and the
// zero-based position, distinguishes invalid bytes from an unpaired
// surrogate, and is never misreported as a missing upstream, a number
// problem, or a successful no-change plan.
func TestUnmarshalPlanHugeNumbersNeverHideCorruptNames(t *testing.T) {
	cases := map[string]struct {
		data     string
		field    string
		location string
		reason   string
	}{
		"huge number at plan start before corrupt change name": {
			`{"meta":` + hugeNumber + `,"changes":[{"name":"source` + "\xff" + `","upstreams":[]}]}`,
			`"name"`, `index 0 of "changes"`, "invalid UTF-8 bytes",
		},
		"huge number nested in unknown object and array before corrupt upstream": {
			`{"meta":{"deep":[` + hugeNumber + `,{"n":` + hugeNumber + `}]},"changes":[{"name":"A","upstreams":["ok","\ud800"]}]}`,
			`"upstreams"`, `index 1`, "unpaired surrogate",
		},
		"huge number inside change record before the name": {
			`{"changes":[{"meta":` + hugeNumber + `,"name":"b` + "\xfe" + `","upstreams":[]}]}`,
			`"name"`, `index 0 of "changes"`, "invalid UTF-8 bytes",
		},
		"huge number inside change record before corrupt upstream": {
			`{"changes":[{"name":"A","stats":[` + hugeNumber + `],"upstreams":["g` + "\xff" + `"]}]}`,
			`"upstreams"`, `index 0`, "invalid UTF-8 bytes",
		},
		"huge number before removals with corrupt entry": {
			`{"stats":[` + hugeNumber + `],"removals":["ok","c\udc00\ud800"]}`,
			`"removals"`, `index 1`, "unpaired surrogate",
		},
		"huge number before corrupt removal naming no dataset": {
			// The corrupt name matches nothing, so accepting the plan would
			// look like a harmless no-op deletion; it must still be refused.
			`{"meta":` + hugeNumber + `,"removals":["ghost` + "\xff" + `"]}`,
			`"removals"`, `index 0`, "invalid UTF-8 bytes",
		},
		"huge number in earlier record before corrupt name in later record": {
			`{"changes":[{"name":"A","upstreams":[],"meta":` + hugeNumber + `},{"name":"\udfff","upstreams":[]}]}`,
			`"name"`, `index 1 of "changes"`, "unpaired surrogate",
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			if !strings.Contains(tc.data, hugeNumber) {
				t.Fatal("test premise broken: the plan must carry the huge number")
			}
			_, err := UnmarshalPlan([]byte(tc.data))
			if !errors.Is(err, ErrInvalidArgument) {
				t.Fatalf("err = %v, want ErrInvalidArgument (the corrupt name, not a number or syntax problem)", err)
			}
			if errors.Is(err, ErrNotFound) {
				t.Fatalf("err = %v, a corrupt name must not be misreported as a missing upstream", err)
			}
			msg := err.Error()
			if !strings.Contains(msg, tc.reason) {
				t.Errorf("err = %q, want it to say %q", msg, tc.reason)
			}
			if !strings.Contains(msg, tc.field) {
				t.Errorf("err = %q, want it to name field %s", msg, tc.field)
			}
			if !strings.Contains(msg, tc.location) {
				t.Errorf("err = %q, want it to locate %s", msg, tc.location)
			}
			if strings.Contains(msg, hugeNumber) || strings.Contains(msg, "number") {
				t.Errorf("err = %q, the ignored unknown number must not be blamed", msg)
			}
			if strings.Contains(msg, "�") {
				t.Errorf("err = %q, must not contain the replacement character", msg)
			}
		})
	}
}

// TestUnmarshalPlanHugeNumbersLegalPlanUnchanged: for a plan whose business
// names are all legal, unknown fields carrying oversized numbers — at the
// top level, nested in objects and arrays, and inside change records — are
// pure additional information: the decoded plan, the preview and apply
// reports, and the final graph are exactly those of the same plan without
// the unknown fields.
func TestUnmarshalPlanHugeNumbersLegalPlanUnchanged(t *testing.T) {
	plain := `{"changes":[{"name":"B","upstreams":["A"]}],"removals":["C"]}`
	loaded := `{"meta":` + hugeNumber + `,"changes":[{"name":"B","upstreams":["A"],"note":{"n":` + hugeNumber + `}}],"removals":["C"],"stats":[` + hugeNumber + `,{"k":` + hugeNumber + `}]}`

	planPlain, err := UnmarshalPlan([]byte(plain))
	if err != nil {
		t.Fatalf("plain plan rejected: %v", err)
	}
	planLoaded, err := UnmarshalPlan([]byte(loaded))
	if err != nil {
		t.Fatalf("plan with unknown huge-number fields rejected: %v", err)
	}
	if !reflect.DeepEqual(planPlain, planLoaded) {
		t.Fatalf("unknown fields changed the decoded plan:\nplain  %+v\nloaded %+v", planPlain, planLoaded)
	}

	newGraph := func() map[string]*Lineage {
		graph, err := UnmarshalGraphFile([]byte(`{"datasets":[{"name":"A","upstreams":[]},{"name":"C","upstreams":[]}]}`))
		if err != nil {
			t.Fatalf("UnmarshalGraphFile: %v", err)
		}
		return graph
	}

	reportPlain, err := PreviewBatch(newGraph(), planPlain)
	if err != nil {
		t.Fatalf("PreviewBatch plain: %v", err)
	}
	reportLoaded, err := PreviewBatch(newGraph(), planLoaded)
	if err != nil {
		t.Fatalf("PreviewBatch loaded: %v", err)
	}
	jPlain, _ := json.Marshal(reportPlain)
	jLoaded, _ := json.Marshal(reportLoaded)
	if string(jPlain) != string(jLoaded) {
		t.Fatalf("preview reports differ:\nplain  %s\nloaded %s", jPlain, jLoaded)
	}

	graphPlain := newGraph()
	graphLoaded := newGraph()
	if _, err := ApplyBatch(graphPlain, planPlain); err != nil {
		t.Fatalf("ApplyBatch plain: %v", err)
	}
	if _, err := ApplyBatch(graphLoaded, planLoaded); err != nil {
		t.Fatalf("ApplyBatch loaded: %v", err)
	}
	outPlain, err := MarshalGraphFile(graphPlain)
	if err != nil {
		t.Fatalf("MarshalGraphFile plain: %v", err)
	}
	outLoaded, err := MarshalGraphFile(graphLoaded)
	if err != nil {
		t.Fatalf("MarshalGraphFile loaded: %v", err)
	}
	if string(outPlain) != string(outLoaded) {
		t.Fatalf("final graphs differ:\nplain  %s\nloaded %s", outPlain, outLoaded)
	}
}

// TestUnmarshalPlanUnknownFieldContentStaysIgnored: an unknown field's own
// content never widens the name-validation scope — corrupt strings, repeated
// keys, oversized numbers, and nested content that merely LOOKS like change
// records or removals all stay ignored additional information, so a plan
// whose real names are legal is accepted.
func TestUnmarshalPlanUnknownFieldContentStaysIgnored(t *testing.T) {
	cases := map[string]string{
		"duplicate keys inside unknown field": `{"meta":{"k":1,"k":2,"k":` + hugeNumber + `},"changes":[{"name":"A","upstreams":[]}]}`,
		"corrupt string and huge number inside unknown field": `{"meta":["\ud800",` + hugeNumber + `,"x` + "\xff" + `"],"changes":[{"name":"A","upstreams":[]}]}`,
		"lookalike changes and removals nested in unknown field": `{"also":{"changes":[{"name":"\ud800","upstreams":["g` + "\xff" + `"]}],"removals":["\udc00"]},"changes":[{"name":"A","upstreams":[]}]}`,
		"lookalike record fields nested in unknown record field": `{"changes":[{"name":"A","upstreams":[],"extra":{"name":"\ud800","upstreams":["x` + "\xfe" + `"],"n":` + hugeNumber + `}}]}`,
		"unknown field between the known ones": `{"changes":[{"name":"A","upstreams":[]}],"between":{"deep":[` + hugeNumber + `]},"removals":["ghost"]}`,
	}
	for name, data := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := UnmarshalPlan([]byte(data)); err != nil {
				t.Fatalf("unknown-field content must stay ignored, got %v", err)
			}
		})
	}
}

// TestBatchHugeNumberPlanNeverDeletesUnnamedDataset: the graph genuinely
// contains a dataset named "源�". A plan whose removals entry is "源" plus a
// corrupt byte — which the decoder would silently rewrite to that real name
// — is refused at read time, so the deletion can never reach the dataset the
// user did not write; and a Go-built plan carrying the same invalid bytes is
// refused by preview and apply alike, leaving the graph untouched.
func TestBatchHugeNumberPlanNeverDeletesUnnamedDataset(t *testing.T) {
	newGraph := func() map[string]*Lineage {
		graph, err := UnmarshalGraphFile([]byte(`{"datasets":[{"name":"源�","upstreams":[]},{"name":"down","upstreams":["源�"]}]}`))
		if err != nil {
			t.Fatalf("UnmarshalGraphFile: %v", err)
		}
		return graph
	}

	// The corrupt removal rides behind an oversized unknown number: even
	// then the plan must be refused for the name, not read as "源�".
	if _, err := UnmarshalPlan([]byte(`{"meta":` + hugeNumber + `,"removals":["源` + "\xff" + `"]}`)); err == nil {
		t.Fatal("corrupt removal name behind a huge number must be rejected")
	}
	graph := newGraph()
	if _, ok := graph["源�"]; !ok {
		t.Fatal("test premise broken: the graph must really contain 源�")
	}
	if got := len(graph); got != 2 {
		t.Fatalf("graph changed while reading a rejected plan: %d datasets, want 2", got)
	}

	// The same corruption constructed directly in Go bypasses the raw scan,
	// so the batch boundary must refuse it too — as invalid UTF-8, never as
	// a missing-upstream problem or a no-op deletion.
	bad := Plan{Removals: []string{"源\xff"}}
	graph = newGraph()
	before := snapshotGraph(graph)
	if _, err := PreviewBatch(graph, bad); !errors.Is(err, ErrInvalidArgument) {
		t.Fatalf("PreviewBatch err = %v, want ErrInvalidArgument", err)
	}
	if _, err := ApplyBatch(graph, bad); !errors.Is(err, ErrInvalidArgument) {
		t.Fatalf("ApplyBatch err = %v, want ErrInvalidArgument", err)
	}
	if after := snapshotGraph(graph); !reflect.DeepEqual(after, before) {
		t.Fatalf("graph changed by a rejected batch:\nbefore %#v\nafter  %#v", before, after)
	}
}

// TestBatchRemovalsGenuineReplacementCharWithHugeNumbers: written correctly —
// directly or as the equivalent escapes — the real name "源�" is removed for
// real, with the same oversized unknown numbers attached. Preview and apply
// succeed, the genuine name lands in the actual removals, and both spellings
// produce the same report.
func TestBatchRemovalsGenuineReplacementCharWithHugeNumbers(t *testing.T) {
	graphData := `{"datasets":[{"name":"源�","upstreams":[]},{"name":"down","upstreams":["源�"]}]}`
	spellings := map[string]string{
		"direct":  `{"meta":` + hugeNumber + `,"changes":[{"name":"down","upstreams":[],"note":[` + hugeNumber + `]}],"removals":["源�"]}`,
		// Same name written with JSON escapes instead of literal characters.
		"escaped": `{"meta":` + hugeNumber + `,"changes":[{"name":"down","upstreams":[],"note":[` + hugeNumber + `]}],"removals":["\u6e90\ufffd"]}`,
	}
	if spellings["direct"] == spellings["escaped"] {
		t.Fatal("test premise broken: the two spellings must differ as text")
	}

	var wantReport string
	for name, data := range spellings {
		t.Run(name, func(t *testing.T) {
			plan, err := UnmarshalPlan([]byte(data))
			if err != nil {
				t.Fatalf("genuine name with huge-number attachments rejected: %v", err)
			}
			graph, err := UnmarshalGraphFile([]byte(graphData))
			if err != nil {
				t.Fatalf("UnmarshalGraphFile: %v", err)
			}
			report, err := PreviewBatch(graph, plan)
			if err != nil {
				t.Fatalf("PreviewBatch: %v", err)
			}
			if !reflect.DeepEqual(report.RemovedDatasets, []string{"源�"}) {
				t.Fatalf("RemovedDatasets = %v, want the real 源� to be the actual removal", report.RemovedDatasets)
			}
			j, _ := json.Marshal(report)
			if wantReport == "" {
				wantReport = string(j)
			} else if string(j) != wantReport {
				t.Fatalf("reports differ between spellings:\n%s\n%s", j, wantReport)
			}

			graph, err = UnmarshalGraphFile([]byte(graphData))
			if err != nil {
				t.Fatalf("UnmarshalGraphFile: %v", err)
			}
			if _, err := ApplyBatch(graph, plan); err != nil {
				t.Fatalf("ApplyBatch: %v", err)
			}
			if _, ok := graph["源�"]; ok {
				t.Fatal("源� must be gone from the applied graph")
			}
			if got := graph["down"].Parents; len(got) != 0 {
				t.Fatalf("down.Parents = %v, want none after the removal", got)
			}
		})
	}
}
