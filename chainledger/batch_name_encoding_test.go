package chainledger

import (
	"errors"
	"reflect"
	"strings"
	"testing"
)

// These tests pin the rule that a plan constructed directly in Go is held to
// the same name-encoding rule as a plan read from a file (see
// plan_name_encoding_test.go): any name that takes part in the adjustment —
// a change record's name, any upstreams entry, any removals entry — must be
// valid UTF-8, or PreviewBatch and ApplyBatch reject the whole plan. Without
// the check, a corrupt name would be stored in the graph and silently
// rewritten to U+FFFD on export, so two distinct corrupt names ("p\xff" and
// "p\xfe") could collapse into one dataset in the exported JSON.

// TestBatchRejectsCorruptPlanNames: a change name, upstream entry, or
// removals entry carrying bytes that are not valid UTF-8 rejects the whole
// plan in both preview and apply. The error is ErrInvalidArgument, names the
// field and the zero-based record or array position (for an upstream, its
// position inside the record too), and quotes the raw bytes so distinct
// corruptions stay distinguishable — never rewritten to "�".
func TestBatchRejectsCorruptPlanNames(t *testing.T) {
	cases := map[string]struct {
		plan     Plan
		field    string
		location string
	}{
		"change name": {
			Plan{Changes: []PlanChange{change("source\xff")}},
			`"name"`, `index 0 of "changes"`,
		},
		"change name in second record": {
			Plan{Changes: []PlanChange{change("A"), change("b\xfe")}},
			`"name"`, `index 1 of "changes"`,
		},
		"upstream entry": {
			Plan{Changes: []PlanChange{change("A", "ok", "g\xfe")}},
			`"upstreams"`, `index 1`,
		},
		"upstream entry in second record": {
			Plan{Changes: []PlanChange{change("A"), change("B", "A", "x\xff")}},
			`"upstreams"`, `index 1`,
		},
		"removals entry": {
			Plan{Removals: []string{"a", "b", "c\xc3"}},
			`"removals"`, `index 2`,
		},
		"removals entry naming a dataset that does not exist": {
			// Even a name that would only delete a nonexistent dataset is
			// refused: the corruption must not pass as a no-op.
			Plan{Removals: []string{"ghost\xff"}},
			`"removals"`, `index 0`,
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			graph := buildGraph(t, P("A"), P("ok"), P("a"), P("b"))
			before := snapshotGraph(graph)

			for _, run := range []struct {
				label string
				call  func() error
			}{
				{"preview", func() error { _, err := PreviewBatch(graph, tc.plan); return err }},
				{"apply", func() error { _, err := ApplyBatch(graph, tc.plan); return err }},
			} {
				err := run.call()
				if !errors.Is(err, ErrInvalidArgument) {
					t.Fatalf("%s: err = %v, want ErrInvalidArgument", run.label, err)
				}
				if errors.Is(err, ErrNotFound) {
					t.Fatalf("%s: err = %v, a corrupt name must not be reported as not found", run.label, err)
				}
				msg := err.Error()
				if !strings.Contains(msg, "invalid UTF-8 bytes") {
					t.Errorf("%s: err = %q, want it to say the bytes are invalid UTF-8", run.label, msg)
				}
				if !strings.Contains(msg, tc.field) {
					t.Errorf("%s: err = %q, want it to name field %s", run.label, msg, tc.field)
				}
				if !strings.Contains(msg, tc.location) {
					t.Errorf("%s: err = %q, want it to locate %s", run.label, msg, tc.location)
				}
				if strings.Contains(msg, "�") {
					t.Errorf("%s: err = %q, must not contain the replacement character", run.label, msg)
				}
			}
			if after := snapshotGraph(graph); !reflect.DeepEqual(after, before) {
				t.Fatalf("graph changed by a rejected plan:\nbefore %#v\nafter  %#v", before, after)
			}
		})
	}
}

// TestBatchCorruptNameErrorKeepsRawBytes: the error message quotes the
// offending name with its original bytes, so two names that differ only in
// their corrupt bytes produce distinguishable errors instead of collapsing
// onto the same replacement character.
func TestBatchCorruptNameErrorKeepsRawBytes(t *testing.T) {
	graph := buildGraph(t, P("root"))
	_, errFF := PreviewBatch(graph, Plan{Changes: []PlanChange{change("p\xff")}})
	_, errFE := PreviewBatch(graph, Plan{Changes: []PlanChange{change("p\xfe")}})
	if errFF == nil || errFE == nil {
		t.Fatalf("corrupt names must be rejected, got %v and %v", errFF, errFE)
	}
	if !strings.Contains(errFF.Error(), `\xff`) || !strings.Contains(errFE.Error(), `\xfe`) {
		t.Errorf("errors must quote the raw bytes, got %q and %q", errFF, errFE)
	}
	if errFF.Error() == errFE.Error() {
		t.Errorf("distinct corrupt names must not collapse to one message: %q", errFF)
	}
}

// TestBatchCorruptUpstreamNotReportedAsMissing: a corrupt upstream is
// rejected for its encoding, not misreported as an unregistered dataset.
func TestBatchCorruptUpstreamNotReportedAsMissing(t *testing.T) {
	graph := buildGraph(t, P("A"))
	_, err := PreviewBatch(graph, Plan{Changes: []PlanChange{change("B", "gone\xff")}})
	if !errors.Is(err, ErrInvalidArgument) {
		t.Fatalf("err = %v, want ErrInvalidArgument", err)
	}
	if errors.Is(err, ErrNotFound) {
		t.Fatalf("err = %v, corrupt upstream must not be reported as a missing dataset", err)
	}
	if strings.Contains(err.Error(), "not registered") || strings.Contains(err.Error(), "does not exist") {
		t.Errorf("err = %q, must not read as a missing-upstream error", err)
	}
}

// TestBatchTwoCorruptRootsDifferingOnlyInBytes: two new roots whose names
// differ only in their invalid bytes must fail even though they are distinct
// Go strings and neither depends on the other — exporting the result would
// collapse them onto the same U+FFFD-rewritten name.
func TestBatchTwoCorruptRootsDifferingOnlyInBytes(t *testing.T) {
	graph := buildGraph(t, P("root"))
	plan := Plan{Changes: []PlanChange{change("p\xff"), change("p\xfe")}}
	if _, err := PreviewBatch(graph, plan); !errors.Is(err, ErrInvalidArgument) {
		t.Fatalf("PreviewBatch err = %v, want ErrInvalidArgument", err)
	}
	if _, err := ApplyBatch(graph, plan); !errors.Is(err, ErrInvalidArgument) {
		t.Fatalf("ApplyBatch err = %v, want ErrInvalidArgument", err)
	}
	if len(graph) != 1 {
		t.Fatalf("graph changed by the rejected plan: %v", snapshotGraph(graph))
	}
}

// TestBatchCorruptRemovalOfNonexistentDatasetFails: a corrupt name listed
// only in removals, naming no existing dataset, must not succeed as a
// no-change plan.
func TestBatchCorruptRemovalOfNonexistentDatasetFails(t *testing.T) {
	graph := buildGraph(t, P("A"), P("B", "A"))
	before := snapshotGraph(graph)
	plan := Plan{Removals: []string{"ghost\xff"}}
	if report, err := ApplyBatch(graph, plan); !errors.Is(err, ErrInvalidArgument) {
		t.Fatalf("err = %v, report = %+v, want ErrInvalidArgument and no report", err, report)
	}
	if after := snapshotGraph(graph); !reflect.DeepEqual(after, before) {
		t.Fatalf("graph changed:\nbefore %#v\nafter  %#v", before, after)
	}
}

// TestBatchCorruptNameRejectsWholePlan: one corrupt name refuses the entire
// batch — the legal repointing and removal in the same plan must not take
// effect, and the caller's plan must be left exactly as submitted.
func TestBatchCorruptNameRejectsWholePlan(t *testing.T) {
	graph := buildGraph(t, P("A"), P("B"), P("C", "A"), P("D", "B"))
	before := snapshotGraph(graph)

	plan := Plan{
		Changes:  []PlanChange{change("C", "B"), change("bad\xff")},
		Removals: []string{"D"},
	}
	planBefore := Plan{
		Changes:  []PlanChange{change("C", "B"), change("bad\xff")},
		Removals: []string{"D"},
	}

	if _, err := ApplyBatch(graph, plan); !errors.Is(err, ErrInvalidArgument) {
		t.Fatalf("err = %v, want ErrInvalidArgument", err)
	}
	if after := snapshotGraph(graph); !reflect.DeepEqual(after, before) {
		t.Fatalf("legal changes in a rejected plan took effect:\nbefore %#v\nafter  %#v", before, after)
	}
	if !reflect.DeepEqual(plan, planBefore) {
		t.Fatalf("the plan itself was modified:\nbefore %+v\nafter  %+v", planBefore, plan)
	}
}

// TestBatchAcceptsGenuineUnicodeNames: legal names keep working unchanged
// through preview and apply — a genuine replacement character, CJK, emoji,
// combining marks, surrounding spaces, and a literal backslash are all kept
// verbatim, with no replacement, case folding, trimming, or normalization.
func TestBatchAcceptsGenuineUnicodeNames(t *testing.T) {
	graph := buildGraph(t, P("源�"), P("数据"))
	plan := Plan{Changes: []PlanChange{
		change("ledger😀", "源�"),
		change(" Éé ", "数据"),
		{Name: `a\nb`, Upstreams: []string{"ledger😀"}},
	}}
	report, err := ApplyBatch(graph, plan)
	if err != nil {
		t.Fatalf("ApplyBatch rejected legal names: %v", err)
	}
	wantNew := []string{" Éé ", `a\nb`, "ledger😀"}
	if !reflect.DeepEqual(report.NewDatasets, wantNew) {
		t.Errorf("NewDatasets = %q, want %q (byte-for-byte, sorted)", report.NewDatasets, wantNew)
	}
	if got := graph[`a\nb`].Parents; !reflect.DeepEqual(got, []string{"ledger😀"}) {
		t.Errorf(`a\nb parents = %q, want ["ledger😀"]`, got)
	}
}

// TestBatchLegalPlanRulesUnchanged: the existing plan semantics are
// untouched — a nonexistent removal name stays a no-op, duplicate upstreams
// count as one relationship, and new datasets in the same batch may
// reference each other.
func TestBatchLegalPlanRulesUnchanged(t *testing.T) {
	graph := buildGraph(t, P("A"))
	plan := Plan{
		Changes:  []PlanChange{change("B", "A", "A"), change("C", "B")},
		Removals: []string{"ghost"},
	}
	report, err := ApplyBatch(graph, plan)
	if err != nil {
		t.Fatalf("ApplyBatch: %v", err)
	}
	if len(report.RemovedDatasets) != 0 {
		t.Errorf("RemovedDatasets = %v, removing a nonexistent dataset must stay a no-op", report.RemovedDatasets)
	}
	if got := graph["B"].Parents; !reflect.DeepEqual(got, []string{"A"}) {
		t.Errorf("B.Parents = %v, duplicate upstreams must count once", got)
	}
	if got := graph["C"].Parents; !reflect.DeepEqual(got, []string{"B"}) {
		t.Errorf("C.Parents = %v, same-batch reference must resolve", got)
	}
}
