package chainledger

import (
	"errors"
	"reflect"
	"strings"
	"testing"
)

// TestUnmarshalPlanRejectsDuplicateFields: a known plan field declared twice
// within the same object makes the whole plan ambiguous and must be rejected —
// changes and removals at the top level, name and upstreams inside a change
// record. The reader would otherwise silently keep only one declaration, so an
// earlier adjustment could vanish, target a different dataset, or lose its
// upstreams. Rejection holds even when the duplicates carry identical values,
// one is null, or the surviving value is a legal empty list.
func TestUnmarshalPlanRejectsDuplicateFields(t *testing.T) {
	cases := map[string]struct {
		data     string
		field    string
		location string
	}{
		"real changes then empty changes": {
			`{"changes":[{"name":"A","upstreams":[]}],"changes":[]}`,
			"changes", "top level",
		},
		"empty changes then real changes": {
			`{"changes":[],"changes":[{"name":"A","upstreams":[]}]}`,
			"changes", "top level",
		},
		"identical changes twice": {
			`{"changes":[{"name":"A","upstreams":[]}],"changes":[{"name":"A","upstreams":[]}]}`,
			"changes", "top level",
		},
		"null changes then real changes": {
			`{"changes":null,"changes":[{"name":"A","upstreams":[]}]}`,
			"changes", "top level",
		},
		"real changes then null changes": {
			`{"changes":[{"name":"A","upstreams":[]}],"changes":null}`,
			"changes", "top level",
		},
		"case variant changes": {
			`{"Changes":[],"changes":[{"name":"A","upstreams":[]}]}`,
			"changes", "top level",
		},
		"long-s fold variant changes": {
			`{"changeſ":[],"changes":[{"name":"A","upstreams":[]}]}`,
			"changes", "top level",
		},
		"escaped changes key": {
			`{"changes":[],"\u0063hanges":[{"name":"A","upstreams":[]}]}`,
			"changes", "top level",
		},
		"identical removals twice": {
			`{"removals":["A"],"removals":["A"]}`,
			"removals", "top level",
		},
		"removals then empty removals": {
			`{"removals":["A"],"removals":[]}`,
			"removals", "top level",
		},
		"null removals then removals": {
			`{"removals":null,"removals":["A"]}`,
			"removals", "top level",
		},
		"case variant removals": {
			`{"Removals":["A"],"removals":["A"]}`,
			"removals", "top level",
		},
		"name twice same value": {
			`{"changes":[{"name":"A","name":"A","upstreams":[]}]}`,
			"name", `index 0 of "changes"`,
		},
		"name twice swaps target": {
			`{"changes":[{"name":"A","upstreams":[],"name":"B"}]}`,
			"name", `index 0 of "changes"`,
		},
		"case variant name": {
			`{"changes":[{"Name":"A","name":"A","upstreams":[]}]}`,
			"name", `index 0 of "changes"`,
		},
		"upstreams twice identical": {
			`{"changes":[{"name":"A","upstreams":[],"upstreams":[]}]}`,
			"upstreams", `index 0 of "changes"`,
		},
		"upstreams then empty upstreams": {
			`{"changes":[{"name":"A","upstreams":["B"],"upstreams":[]}]}`,
			"upstreams", `index 0 of "changes"`,
		},
		"null upstreams then real upstreams": {
			`{"changes":[{"name":"A","upstreams":null,"upstreams":["B"]}]}`,
			"upstreams", `index 0 of "changes"`,
		},
		"duplicate in second record": {
			`{"changes":[{"name":"A","upstreams":[]},{"name":"B","name":"C","upstreams":[]}]}`,
			"name", `index 1 of "changes"`,
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := UnmarshalPlan([]byte(tc.data))
			if !errors.Is(err, ErrInvalidArgument) {
				t.Fatalf("err = %v, want ErrInvalidArgument", err)
			}
			if !strings.Contains(err.Error(), `"`+tc.field+`"`) {
				t.Errorf("err = %q, want it to name duplicated field %q", err, tc.field)
			}
			if !strings.Contains(err.Error(), tc.location) {
				t.Errorf("err = %q, want it to locate the duplicate %q", err, tc.location)
			}
		})
	}
}

// TestUnmarshalPlanDuplicateRejectsBeforeBatch: the plan is refused before any
// adjustment or removal is considered. In particular, real changes followed by
// an empty changes must not be accepted as an empty plan (which would report
// success and, for apply, write the original graph back).
func TestUnmarshalPlanDuplicateRejectsBeforeBatch(t *testing.T) {
	graph := buildGraph(t, P("A"), P("B", "A"))

	plan, err := UnmarshalPlan([]byte(`{"changes":[{"name":"A","upstreams":["B"]}],"changes":[]}`))
	if err == nil {
		t.Fatalf("UnmarshalPlan accepted repeated changes: %+v", plan)
	}

	// Defense in depth: callers must never reach the batch with a duplicate
	// plan, but feeding the zero Plan that UnmarshalPlan returns on error must
	// not be what happened either — assert the error directly above is the
	// rejection point.
	if _, err := PreviewBatch(graph, Plan{}); err != nil {
		t.Errorf("unrelated empty-plan preview failed: %v", err)
	}
}

// TestUnmarshalPlanApplyRejectionLeavesGraphUntouched: when an apply caller
// parses a plan that repeats a known field, UnmarshalPlan fails and no graph
// mutation path runs.
func TestUnmarshalPlanApplyRejectionLeavesGraphUntouched(t *testing.T) {
	graph := buildGraph(t, P("A"), P("B", "A"))
	before := graph["A"].Parents

	_, err := UnmarshalPlan([]byte(`{"changes":[{"name":"A","upstreams":["B"],"upstreams":[]}]}`))
	if err == nil {
		t.Fatal("UnmarshalPlan accepted repeated upstreams")
	}
	if got := graph["A"].Parents; !reflect.DeepEqual(got, before) {
		t.Errorf("graph changed after rejected parse: A.Parents = %v, want %v", got, before)
	}
}

// TestUnmarshalPlanLargeJSONNumberCannotMaskDuplicates is the plan analogue of
// the snapshot regression: a legal JSON number outside float64 range (1e400),
// even one buried in an ignored unknown field's value at any depth, must not
// abort the scan and hide a repeated known field placed after it.
func TestUnmarshalPlanLargeJSONNumberCannotMaskDuplicates(t *testing.T) {
	cases := map[string]struct {
		data     string
		field    string
		location string
	}{
		"big direct value before duplicated changes": {
			`{"note":1e400,"changes":[{"name":"A","upstreams":[]}],"changes":[]}`,
			"changes", "top level",
		},
		"big number nested before duplicated removals": {
			`{"meta":{"keep":[1e400,{"deep":1e999}]},"removals":["A"],"removals":[]}`,
			"removals", "top level",
		},
		"duplicate before the big number": {
			`{"changes":[],"changes":[{"name":"A","upstreams":[]}],"note":1e400}`,
			"changes", "top level",
		},
		"big value in record before duplicated name": {
			`{"changes":[{"note":1e400,"name":"A","name":"A","upstreams":[]}]}`,
			"name", `index 0 of "changes"`,
		},
		"big values nested in record before duplicated upstreams": {
			`{"changes":[{"name":"A","vals":[[1e400],{"z":1e999}],"upstreams":[],"upstreams":["B"]}]}`,
			"upstreams", `index 0 of "changes"`,
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := UnmarshalPlan([]byte(tc.data))
			if !errors.Is(err, ErrInvalidArgument) {
				t.Fatalf("err = %v, want ErrInvalidArgument", err)
			}
			if !strings.Contains(err.Error(), `"`+tc.field+`"`) {
				t.Errorf("err = %q, want it to name duplicated field %q", err, tc.field)
			}
			if !strings.Contains(err.Error(), tc.location) {
				t.Errorf("err = %q, want it to locate the duplicate %q", err, tc.location)
			}
		})
	}
}

// TestUnmarshalPlanTolerances: repetitions and spellings that do NOT create
// ambiguity stay legal. Unknown fields may repeat anywhere, including nested
// duplicate keys and big numbers inside them; each change record declares its
// own name; a dataset repeated inside one upstreams array is still one
// relationship (deduplicated later by the batch); a field written once in any
// casing keeps working; dataset name values stay case-sensitive and keep
// surrounding whitespace.
func TestUnmarshalPlanTolerances(t *testing.T) {
	cases := map[string]string{
		"empty object":                          `{}`,
		"empty changes and removals":            `{"changes":[],"removals":[]}`,
		"only removals":                         `{"removals":["A"]}`,
		"unknown top-level field repeats":       `{"note":1,"note":2,"changes":[]}`,
		"unknown field repeats with big number": `{"note":1e400,"note":2e400,"changes":[]}`,
		"duplicate keys nested in unknown field": `{"meta":{"changes":1,"changes":2,"big":[1e400,{"x":1e999}]},` +
			`"changes":[{"name":"A","upstreams":[]}],"extra":{"name":1e400,"upstreams":1e400}}`,
		"unknown record field repeats":         `{"changes":[{"name":"A","note":"x","note":"y","upstreams":[]}]}`,
		"one name per record":                  `{"changes":[{"name":"A","upstreams":[]},{"name":"A","upstreams":["B"]}]}`,
		"duplicate dataset in upstreams array": `{"changes":[{"name":"A","upstreams":["B","B"]}]}`,
		"case variants written once": `{"CHANGES":[{"Name":"A","Upstreams":[]},{"NAME":"B","UPSTREAMS":["A"]}],` +
			`"REMOVALS":["C"]}`,
		"case-sensitive and whitespace-preserving name values": `{"changes":[{"name":" A ","upstreams":[]},{"name":"a","upstreams":[" A "]}]}`,
	}
	for name, data := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := UnmarshalPlan([]byte(data)); err != nil {
				t.Fatalf("UnmarshalPlan rejected a legal plan: %v", err)
			}
		})
	}

	// Dataset name values survive verbatim: only field-name matching is
	// case-folded.
	plan, err := UnmarshalPlan([]byte(`{"changes":[{"name":" A ","upstreams":[]},{"name":"a","upstreams":[" A "]}]}`))
	if err != nil {
		t.Fatalf("UnmarshalPlan: %v", err)
	}
	if got := plan.Changes[0].Name; got != " A " {
		t.Errorf("change[0].Name = %q, want %q", got, " A ")
	}
	if got := plan.Changes[1].Name; got != "a" {
		t.Errorf("change[1].Name = %q, want %q", got, "a")
	}

	// A duplicate-free plan with a repeated dataset inside one upstreams array
	// still batches exactly as before the fix: one relationship, no rejection.
	graph := buildGraph(t, P("A"), P("B"))
	report, err := PreviewBatch(graph, planOf(change("A", "B", "B")))
	if err != nil {
		t.Fatalf("PreviewBatch: %v", err)
	}
	if len(report.AddedRelations) != 1 {
		t.Errorf("AddedRelations = %v, want exactly one B->A relation", report.AddedRelations)
	}
}

// TestUnmarshalPlanMalformedJSONStillFails: a duplicate-field scanner must not
// turn genuinely malformed documents into successes; syntax errors surface
// from the regular parse, just as before.
func TestUnmarshalPlanMalformedJSONStillFails(t *testing.T) {
	for name, broken := range map[string]string{
		"truncated":              `{"changes":[`,
		"malformed number":       `{"note":1e,"changes":[]}`,
		"truncated after big":    `{"note":1e400,"changes":`,
		"unterminated duplicate": `{"changes":[],"changes":[{"name":`,
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := UnmarshalPlan([]byte(broken)); err == nil {
				t.Fatalf("UnmarshalPlan accepted malformed JSON %q", broken)
			}
		})
	}
}
