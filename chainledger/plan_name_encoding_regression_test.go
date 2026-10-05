package chainledger

import (
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
)

// Regression coverage for the raw name-encoding rule on adjustment plans:
// ignoring the additional information an unknown field carries must still
// leave every name that takes part in the adjustment fully validated. In
// particular a legal JSON number outside the float64 range (1e400), whether
// it opens the plan, hides inside an unknown object or array, or precedes a
// name inside a change record, must never abort the raw scan or the regular
// parse in a way that masks a corrupt name later in the document: the plan
// must still be rejected for that name — never mistaken for a missing
// upstream, an invalid number, or a successful no-op.

// assertNameCorruption rejects a plan with the name-encoding error for the
// given field/position and corruption kind, and — just as importantly — with
// none of the errors a masked scan could surface instead.
func assertNameCorruption(t *testing.T, data, field, location, reason string) {
	t.Helper()
	_, err := UnmarshalPlan([]byte(data))
	if !errors.Is(err, ErrInvalidArgument) {
		t.Fatalf("UnmarshalPlan err = %v, want ErrInvalidArgument naming the corrupt name", err)
	}
	msg := err.Error()
	if !strings.Contains(msg, reason) {
		t.Errorf("err = %q, want it to say %q", msg, reason)
	}
	if !strings.Contains(msg, field) {
		t.Errorf("err = %q, want it to name field %q", msg, field)
	}
	if !strings.Contains(msg, location) {
		t.Errorf("err = %q, want it to locate %q", msg, location)
	}
	switch reason {
	case "invalid UTF-8 bytes":
		if strings.Contains(msg, "surrogate") {
			t.Errorf("err = %q, byte corruption must not be reported as a surrogate problem", msg)
		}
	case "unpaired surrogate":
		if strings.Contains(msg, "invalid UTF-8 bytes") {
			t.Errorf("err = %q, a surrogate problem must not be reported as invalid bytes", msg)
		}
	}
	// A large number in a skipped unknown field must not be mistaken for the
	// problem, and the plan must not fall through to later validation: the
	// name defect is the only reported reason.
	for _, masked := range []string{"invalid plan JSON", "cannot unmarshal number", "does not exist in the final graph", "no change"} {
		if strings.Contains(msg, masked) {
			t.Errorf("err = %q, name corruption was masked by %q", msg, masked)
		}
	}
	if strings.Contains(msg, "�") {
		t.Errorf("err = %q, must not contain the replacement character", msg)
	}
}

// TestPlanBigNumberInUnknownFieldDoesNotMaskCorruptNames: an oversized but
// perfectly legal JSON number carried by an unknown field — directly at the
// top of the plan, nested in unknown objects and arrays, or inside a change
// record — precedes a corrupt business name in every position that takes
// part in the adjustment (a change record's name, an upstreams entry, a
// removals entry). The corruption must still be reported, with the error
// naming the name defect rather than the number or a missing upstream.
func TestPlanBigNumberInUnknownFieldDoesNotMaskCorruptNames(t *testing.T) {
	const (
		nameField     = `"name"`
		upField       = `"upstreams"`
		removalsField = `"removals"`
		firstChange   = `index 0 of "changes"`
		secondChange  = `index 1 of "changes"`
	)

	// Where the big number sits before the corrupt name. Each placement
	// requires the walk to skip the unknown value without consuming it as a
	// float64 (UseNumber), even when it nests behind objects and arrays.
	bigPrefixes := map[string]string{
		"big number opens the plan":           `{"note":1e400,`,
		"signed big number opens the plan":    `{"note":-1e400,`,
		"big number nested in unknown object": `{"meta":{"x":[1e400,{"y":1e999}]},`,
		"big number among unknown array data": `{"bag":[1e400,"z",[2e999,{"k":-1e400}]],`,
		"repeated unknown field with numbers": `{"note":1e400,"note":2e999,`,
	}

	corruptSuffixes := map[string]struct {
		data     string
		field    string
		location string
		reason   string
	}{
		"invalid bytes in change name": {
			`"changes":[{"name":"a` + "\xff" + `","upstreams":[]}]}`,
			nameField, firstChange, "invalid UTF-8 bytes",
		},
		"lone surrogate in change name": {
			`"changes":[{"name":"a\ud800","upstreams":[]}]}`,
			nameField, firstChange, "unpaired surrogate",
		},
		"lone low surrogate in change name": {
			`"changes":[{"name":"\udc00","upstreams":[]}]}`,
			nameField, firstChange, "unpaired surrogate",
		},
		"invalid bytes in second change record after a legal one": {
			`"changes":[{"name":"A","upstreams":[]},{"name":"b` + "\xfe" + `","upstreams":[]}]}`,
			nameField, secondChange, "invalid UTF-8 bytes",
		},
		"surrogate in second change record after a legal one": {
			`"changes":[{"name":"A","upstreams":[]},{"name":"b\ud800","upstreams":[]}]}`,
			nameField, secondChange, "unpaired surrogate",
		},
		"invalid bytes in upstream entry": {
			// The corrupt reference must be reported as a corrupt name, never
			// as "upstream does not exist in the final graph".
			`"changes":[{"name":"A","upstreams":["ok","g` + "\xfe" + `"]}]}`,
			upField, `index 1`, "invalid UTF-8 bytes",
		},
		"surrogate upstream entry": {
			`"changes":[{"name":"A","upstreams":["\udfff"]}]}`,
			upField, `index 0`, "unpaired surrogate",
		},
		"invalid bytes in removals entry": {
			`"removals":["ok","b","c` + "\xc3" + `"]}`,
			removalsField, `index 2`, "invalid UTF-8 bytes",
		},
		"surrogate in removals entry": {
			`"removals":["ok","\ud800"]}`,
			removalsField, `index 1`, "unpaired surrogate",
		},
	}

	for prefixName, prefix := range bigPrefixes {
		for suffixName, tc := range corruptSuffixes {
			t.Run(prefixName+" / "+suffixName, func(t *testing.T) {
				assertNameCorruption(t, prefix+tc.data, tc.field, tc.location, tc.reason)
			})
		}
	}

	// The big number may also sit INSIDE the change record, before the name
	// it must not mask, including in a record before the one carrying the
	// corrupt name.
	t.Run("big number inside record before corrupt name", func(t *testing.T) {
		data := `{"changes":[{"note":1e400,"name":"a` + "\xff" + `","upstreams":[]}]}`
		assertNameCorruption(t, data, nameField, firstChange, "invalid UTF-8 bytes")
	})
	t.Run("big numbers inside first record before corrupt upstream in second", func(t *testing.T) {
		data := `{"changes":[` +
			`{"name":"A","upstreams":[],"vals":[1e999,{"z":-1e400}]},` +
			`{"name":"B","upstreams":["x` + "\xfe" + `"]}]}`
		assertNameCorruption(t, data, upField, `index 0`, "invalid UTF-8 bytes")
	})
	t.Run("big number inside record before surrogate name", func(t *testing.T) {
		data := `{"changes":[{"meta":{"keep":[1e400]},"name":"a\ud800","upstreams":[]}]}`
		assertNameCorruption(t, data, nameField, firstChange, "unpaired surrogate")
	})
	t.Run("big number after a corrupt removals still rejects the removals", func(t *testing.T) {
		// Control for ordering: a big number anywhere else keeps being
		// ignored while the earlier corruption is what gets reported.
		data := `{"removals":["g` + "\xff" + `"],"note":1e400}`
		assertNameCorruption(t, data, removalsField, `index 0`, "invalid UTF-8 bytes")
	})
}

// TestPlanCorruptNameErrorIdenticalWithAndWithoutBigNumber: the error
// reported for one fixed corrupt name must be byte-for-byte the same whether
// ignored additional information precedes it or not — the skip must not leak
// positions, numbers, or any other trace into the name error.
func TestPlanCorruptNameErrorIdenticalWithAndWithoutBigNumber(t *testing.T) {
	corrupt := []struct {
		name string
		plan string
	}{
		{"change name bytes", `{"changes":[{"name":"a` + "\xff" + `","upstreams":[]}]}`},
		{"change name surrogate", `{"changes":[{"name":"a\ud800","upstreams":[]}]}`},
		{"upstream bytes", `{"changes":[{"name":"A","upstreams":["g` + "\xfe" + `"]}]}`},
		{"upstream surrogate", `{"changes":[{"name":"A","upstreams":["\udfff"]}]}`},
		{"removals bytes", `{"removals":["源` + "\xff" + `"]}`},
		{"removals surrogate", `{"removals":["源\ud800"]}`},
	}
	// Each decoration wraps the same plan with ignored big-number content at
	// a different place the walk must skip.
	decorate := map[string]func(string) string{
		"plain": func(p string) string { return p },
		"top-level big number before": func(p string) string {
			return `{"note":1e400,` + p[1:]
		},
		"nested unknown big numbers before": func(p string) string {
			return `{"meta":[1e999,{"z":-1e400}],` + p[1:]
		},
	}

	for _, c := range corrupt {
		t.Run(c.name, func(t *testing.T) {
			var wantMsg string
			for decoName, deco := range decorate {
				_, err := UnmarshalPlan([]byte(deco(c.plan)))
				if err == nil {
					t.Fatalf("%s: corrupt plan accepted", decoName)
				}
				if wantMsg == "" {
					wantMsg = err.Error()
				} else if err.Error() != wantMsg {
					t.Errorf("%s error:\n%q\nwant:\n%q", decoName, err.Error(), wantMsg)
				}
			}
		})
	}
}

// TestPlanBigNumberBeforeCorruptRemovalNeverDeletes: the removal case keeps
// the user's original intent — a removal written as "源" plus a broken byte
// or a lone surrogate must not be rewritten into the genuinely existing
// dataset "源�" and delete it, and a corrupted name matching no dataset at
// all must not be accepted as the no-op "delete a nonexistent name" success.
// A big unknown field placed before the removal changes none of this.
func TestPlanBigNumberBeforeCorruptRemovalNeverDeletes(t *testing.T) {
	graph := buildGraph(t, P("源�"), P("keep"))
	before := snapshotGraph(graph)

	cases := map[string]string{
		"broken byte would collide with real 源�":    `{"note":1e400,"removals":["源` + "\xff" + `"]}`,
		"lone surrogate would collide with real 源�": `{"meta":[1e999],"removals":["源\ud800"]}`,
		"low-before-high surrogate would collide":   `{"note":{"x":1e400},"removals":["源\udc00\ud800"]}`,
		"broken byte names no dataset at all":       `{"note":1e400,"removals":["ghost` + "\xff" + `"]}`,
		"lone surrogate names no dataset at all":    `{"bag":[1e400],"removals":["ghost\ud800"]}`,
		"broken byte after a legal change": `{"note":1e400,"changes":[{"name":"keep","upstreams":[]}],` +
			`"removals":["源` + "\xff" + `"]}`,
	}
	for name, data := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := UnmarshalPlan([]byte(data)); err == nil {
				t.Fatal("corrupt removal name must reject the plan at parse time")
			}
			if after := snapshotGraph(graph); !reflect.DeepEqual(after, before) {
				t.Fatalf("graph changed while rejecting a corrupt removal:\nbefore %#v\nafter  %#v", before, after)
			}
			if _, ok := graph["源�"]; !ok {
				t.Fatal("the dataset really named 源� was deleted by a corrupted removal")
			}
		})
	}
}

// TestPlanGenuineReplacementCharWithBigNumbersSucceeds: when the removal
// really names the existing dataset "源�" — written directly or with
// equivalent JSON escapes — and nothing would still reference it, the same
// big-number additions stay legal and the real name enters the actual
// removal result for both preview and apply.
func TestPlanGenuineReplacementCharWithBigNumbersSucceeds(t *testing.T) {
	direct := []byte(`{"note":1e400,"meta":[1e999,{"z":-1e400}],"removals":["源�"]}`)
	// The same removal written with literal JSON \u escapes for both 源 and
	// � — different bytes on disk, the same dataset once decoded.
	escaped := []byte(`{"note":1e400,"meta":[1e999,{"z":-1e400}],"removals":["\u6e90\ufffd"]}`)

	planDirect, err := UnmarshalPlan(direct)
	if err != nil {
		t.Fatalf("direct spelling rejected: %v", err)
	}
	planEscaped, err := UnmarshalPlan(escaped)
	if err != nil {
		t.Fatalf("escaped spelling rejected: %v", err)
	}
	if !reflect.DeepEqual(planDirect, planEscaped) {
		t.Fatalf("equivalent spellings decoded differently:\n%+v\n%+v", planDirect, planEscaped)
	}

	for name, data := range map[string][]byte{"direct": direct, "escaped": escaped} {
		t.Run(name, func(t *testing.T) {
			graph := buildGraph(t, P("源�"), P("keep"))
			plan, err := UnmarshalPlan(data)
			if err != nil {
				t.Fatalf("UnmarshalPlan: %v", err)
			}
			report, err := PreviewBatch(graph, plan)
			if err != nil {
				t.Fatalf("PreviewBatch: %v", err)
			}
			if !reflect.DeepEqual(report.RemovedDatasets, []string{"源�"}) {
				t.Fatalf("RemovedDatasets = %v, want [源�]", report.RemovedDatasets)
			}
			if _, err := ApplyBatch(graph, plan); err != nil {
				t.Fatalf("ApplyBatch: %v", err)
			}
			if _, ok := graph["源�"]; ok {
				t.Fatal("the dataset really named 源� must be gone after apply")
			}
			if _, ok := graph["keep"]; !ok {
				t.Fatal("the unrelated dataset keep must survive")
			}
		})
	}
}

// TestPlanUnknownFieldsDoNotChangeReport: for a plan whose business names
// are all legal, adding or removing the additional information — oversized
// numbers, unknown objects and arrays, at the top level or inside records —
// changes neither the decoded Plan nor the marshaled report nor the graph an
// apply produces.
func TestPlanUnknownFieldsDoNotChangeReport(t *testing.T) {
	graph := func() map[string]*Lineage {
		return buildGraph(t, P("A"), P("B", "A"), P("源�"))
	}
	plain := `{"changes":[{"name":"B","upstreams":["源�"]}],"removals":["A"]}`
	variants := map[string]string{
		"plain": plain,
		"big number at top": `{"note":1e400,` +
			`"changes":[{"name":"B","upstreams":["源�"]}],"removals":["A"]}`,
		"big numbers nested everywhere": `{"meta":{"x":[1e400,{"y":1e999}]},"changes":[` +
			`{"name":"B","upstreams":["源�"],"vals":[1e999,-1e400],"note":{}}],` +
			`"removals":["A"],"tail":[2e400]}`,
		"equivalent escapes plus big numbers": `{"note":1e400,"changes":[` +
			`{"name":"\u0042","upstreams":["\u6e90\ufffd"]}],"removals":["\u0041"]}`,
	}

	basePlan, err := UnmarshalPlan([]byte(plain))
	if err != nil {
		t.Fatalf("plain plan rejected: %v", err)
	}
	baseGraph := graph()
	baseReport, err := PreviewBatch(baseGraph, basePlan)
	if err != nil {
		t.Fatalf("PreviewBatch plain: %v", err)
	}
	baseJSON, _ := json.Marshal(baseReport)

	if _, err := ApplyBatch(baseGraph, basePlan); err != nil {
		t.Fatalf("ApplyBatch plain: %v", err)
	}
	baseGraphBytes, err := MarshalGraphFile(baseGraph)
	if err != nil {
		t.Fatalf("MarshalGraphFile plain: %v", err)
	}

	for name, data := range variants {
		t.Run(name, func(t *testing.T) {
			plan, err := UnmarshalPlan([]byte(data))
			if err != nil {
				t.Fatalf("UnmarshalPlan: %v", err)
			}
			if !reflect.DeepEqual(plan, basePlan) {
				t.Fatalf("decoded plan differs from plain:\n%+v\n%+v", plan, basePlan)
			}
			g := graph()
			report, err := PreviewBatch(g, plan)
			if err != nil {
				t.Fatalf("PreviewBatch: %v", err)
			}
			j, _ := json.Marshal(report)
			if string(j) != string(baseJSON) {
				t.Fatalf("report differs from plain:\n%s\n%s", j, baseJSON)
			}
			if _, err := ApplyBatch(g, plan); err != nil {
				t.Fatalf("ApplyBatch: %v", err)
			}
			b, err := MarshalGraphFile(g)
			if err != nil {
				t.Fatalf("MarshalGraphFile: %v", err)
			}
			if string(b) != string(baseGraphBytes) {
				t.Fatalf("final graph differs from plain:\n%s\n%s", b, baseGraphBytes)
			}
		})
	}
}

// TestPlanUnknownFieldConfusionStaysIgnoredWithBigNumbers: even with
// oversized numbers present, anything nested inside an unknown field stays
// additional information: its own corrupt strings, repeated field names, and
// content shaped exactly like adjustment records must not widen the
// name-encoding check to those strings — the business names around them are
// the only ones judged.
func TestPlanUnknownFieldConfusionStaysIgnoredWithBigNumbers(t *testing.T) {
	cases := map[string]string{
		"corrupt strings nested next to big numbers": `{"note":1e400,"meta":["a` + "\xff" + `",{"k":"\ud800"}],"changes":[{"name":"A","upstreams":[]}]}`,
		"duplicate keys inside unknown field": `{"note":1e400,"meta":{"name":1,"name":2,"changes":[1e999,1e999]},` +
			`"changes":[{"name":"A","upstreams":[]}]}`,
		"change-record lookalike nested in unknown field": `{"note":1e400,"also":{` +
			`"changes":[{"name":"x` + "\xff" + `","upstreams":["y\ud800"]}],` +
			`"removals":["z` + "\xfe" + `"]},"changes":[{"name":"A","upstreams":[]}],"removals":[]}`,
		"unknown array holding record lookalikes and big numbers": `{"extra":[1e400,` +
			`{"name":"x` + "\xff" + `","upstreams":["y\ud800"]}],"changes":[{"name":"A","upstreams":[]}]}`,
		"unknown field prefixing a known field name": `{"changesX":1e400,"removals2":[1e999,"a` + "\xff" + `"],` +
			`"changes":[{"name":"A","upstreams":[]}]}`,
	}
	for name, data := range cases {
		t.Run(name, func(t *testing.T) {
			plan, err := UnmarshalPlan([]byte(data))
			if err != nil {
				t.Fatalf("unknown-field content must stay ignored, got %v", err)
			}
			if len(plan.Changes) != 1 || plan.Changes[0].Name != "A" {
				t.Fatalf("the real change record must still be read: %+v", plan.Changes)
			}
			g := buildGraph(t, P("A"))
			report, err := PreviewBatch(g, plan)
			if err != nil {
				t.Fatalf("PreviewBatch must ignore the nested content: %v", err)
			}
			if len(report.NewDatasets) != 0 || len(report.RemovedDatasets) != 0 {
				t.Fatalf("nested lookalike content leaked into the business plan: %+v", report)
			}
		})
	}
}
