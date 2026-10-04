package chainledger

import (
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
	"unicode/utf8"
)

// These tests pin the in-memory counterpart of the plan reader's raw-name
// gate (see plan_name_encoding_test.go). A Plan constructed directly in Go
// never passes through UnmarshalPlan, so without a gate inside the batch
// itself PreviewBatch and ApplyBatch could register a dataset whose name
// carries invalid UTF-8 bytes: the apply succeeded, the applied graph could
// no longer be exported, and marshaling the success report rewrote every bad
// byte to U+FFFD — two names differing only in the bad byte printed as one.
// Both preview and apply must refuse the whole plan instead.

// TestBatchRejectsCorruptPlanNameBytes: a name with invalid UTF-8 bytes
// rejects the whole batch in every position that takes part in the
// adjustment — a change record's name, any direct upstream, and any removal.
// The error is recognized as ErrInvalidArgument, says the bytes are invalid,
// names the field, and gives the zero-based record or array position (an
// upstream also names its position inside the record). The original bytes
// stay quoted (\xff, not U+FFFD).
func TestBatchRejectsCorruptPlanNameBytes(t *testing.T) {
	cases := map[string]struct {
		plan   Plan
		field  string
		wantIn []string // location phrases the message must contain
	}{
		"change name at record 0": {
			Plan{Changes: []PlanChange{{Name: "p\xff", Upstreams: []string{}}}},
			`"name"`, []string{`change record at index 0 of "changes"`},
		},
		"change name at record 1": {
			Plan{Changes: []PlanChange{
				{Name: "A", Upstreams: nil},
				{Name: "b\xfe", Upstreams: nil},
			}},
			`"name"`, []string{`change record at index 1 of "changes"`},
		},
		"upstream at index 0 of record 0": {
			Plan{Changes: []PlanChange{{Name: "A", Upstreams: []string{"g\xff"}}}},
			`"upstreams"`, []string{"index 0", `change record at index 0 of "changes"`},
		},
		"upstream at index 1 of record 0": {
			Plan{Changes: []PlanChange{{Name: "A", Upstreams: []string{"ok", "g\xfe"}}}},
			`"upstreams"`, []string{"index 1", `change record at index 0 of "changes"`},
		},
		"upstream in the second record": {
			Plan{Changes: []PlanChange{
				{Name: "A", Upstreams: nil},
				{Name: "B", Upstreams: []string{"A", "x\xff"}},
			}},
			`"upstreams"`, []string{"index 1", `change record at index 1 of "changes"`},
		},
		"removal at index 2": {
			Plan{Removals: []string{"a", "b", "c\xc3"}},
			`"removals"`, []string{"at index 2"},
		},
		"removal naming a dataset that does not exist": {
			// A corrupted name used only to delete a nonexistent dataset must
			// not be treated as a no-op success: the name itself is unusable.
			Plan{Removals: []string{"ghost\xff"}},
			`"removals"`, []string{"at index 0"},
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			graph := buildGraph(t, P("A"), P("B", "A"))

			report, err := PreviewBatch(graph, tc.plan)
			if err == nil {
				t.Fatalf("PreviewBatch accepted a corrupt plan and returned report %#v", report)
			}
			if report != nil {
				t.Fatalf("rejected PreviewBatch returned a non-nil report %#v", report)
			}
			assertBatchCorruptNameError(t, err, tc.field, tc.wantIn)

			// Apply must refuse identically and leave the graph alone.
			before := snapshotGraph(graph)
			applied, applyErr := ApplyBatch(graph, tc.plan)
			if applyErr == nil {
				t.Fatalf("ApplyBatch accepted a corrupt plan and returned report %#v", applied)
			}
			assertBatchCorruptNameError(t, applyErr, tc.field, tc.wantIn)
			if applied != nil {
				t.Fatalf("rejected ApplyBatch returned a non-nil report %#v", applied)
			}
			if after := snapshotGraph(graph); !reflect.DeepEqual(after, before) {
				t.Fatalf("ApplyBatch mutated the graph on rejection:\nbefore %#v\nafter  %#v", before, after)
			}
			if err.Error() != applyErr.Error() {
				t.Fatalf("preview and apply report different errors:\n%q\n%q", err, applyErr)
			}
		})
	}
}

// assertBatchCorruptNameError checks the shared shape of every corrupt-name
// rejection from the Go API: ErrInvalidArgument (never ErrNotFound), the
// right field and position, the raw bytes quoted as \x escapes, and no U+FFFD
// substitution.
func assertBatchCorruptNameError(t *testing.T, err error, field string, location []string) {
	t.Helper()
	if !errors.Is(err, ErrInvalidArgument) {
		t.Fatalf("err = %v, want errors.Is ErrInvalidArgument", err)
	}
	if errors.Is(err, ErrNotFound) {
		t.Fatalf("err = %v, a corrupt name must not be reported as a missing dataset", err)
	}
	msg := err.Error()
	if !strings.Contains(msg, "invalid UTF-8 bytes") {
		t.Errorf("err = %q, want it to say the bytes are invalid UTF-8", msg)
	}
	if !strings.Contains(msg, field) {
		t.Errorf("err = %q, want it to name field %s", msg, field)
	}
	for _, want := range location {
		if !strings.Contains(msg, want) {
			t.Errorf("err = %q, want it to locate %q", msg, want)
		}
	}
	if strings.Contains(msg, "�") {
		t.Errorf("err = %q, must show the original bytes, not the replacement character", msg)
	}
	if !strings.Contains(msg, `\x`) {
		t.Errorf("err = %q, must quote the original offending bytes", msg)
	}
}

// TestBatchCorruptUpstreamNotReportedAsMissing: an unusable upstream name is
// rejected for its encoding even when it does not resolve to any dataset —
// it must never surface as ErrNotFound.
func TestBatchCorruptUpstreamNotReportedAsMissing(t *testing.T) {
	graph := buildGraph(t, P("A"))
	plan := planOf(change("A", "ghost\xff"))

	_, err := PreviewBatch(graph, plan)
	if !errors.Is(err, ErrInvalidArgument) {
		t.Fatalf("err = %v, want ErrInvalidArgument to take precedence", err)
	}
	if errors.Is(err, ErrNotFound) {
		t.Fatalf("corrupt upstream %q must not be reported as not found", err)
	}
}

// TestBatchTwoCorruptRootsDifferingOnlyInBadByte: two new roots that differ
// only in an invalid byte must be rejected even though neither depends on the
// other. The test first documents why acceptance was unsafe: the JSON encoder
// turns both names into the identical "p�" literal, so a success report could
// not tell them apart — then pins the rejection and the fact that neither
// root is registered.
func TestBatchTwoCorruptRootsDifferingOnlyInBadByte(t *testing.T) {
	// Premise: marshaling really collapses the two distinct Go strings.
	a, _ := json.Marshal("p\xff")
	b, _ := json.Marshal("p\xfe")
	if string(a) != string(b) {
		t.Fatalf("test premise changed: json.Marshal gives %s and %s", a, b)
	}
	var decoded string
	if err := json.Unmarshal(a, &decoded); err != nil {
		t.Fatalf("decoding marshaled name: %v", err)
	}
	if decoded != "p"+string(utf8.RuneError) {
		t.Fatalf("marshaled name decodes to %q, not the replacement name", decoded)
	}

	graph := buildGraph(t, P("A"))
	before := snapshotGraph(graph)
	plan := planOf(change("p\xff"), change("p\xfe"))

	if _, err := PreviewBatch(graph, plan); !errors.Is(err, ErrInvalidArgument) {
		t.Fatalf("PreviewBatch err = %v, want ErrInvalidArgument", err)
	}
	if report, err := ApplyBatch(graph, plan); err == nil {
		t.Fatalf("ApplyBatch accepted two corrupt roots and returned %#v", report)
	}
	if after := snapshotGraph(graph); !reflect.DeepEqual(after, before) {
		t.Fatalf("graph changed on rejection:\nbefore %#v\nafter  %#v", before, after)
	}
	if _, ok := graph["p\xff"]; ok {
		t.Errorf("corrupt root p\\xff was registered: %v", graph)
	}
	if _, ok := graph["p\xfe"]; ok {
		t.Errorf("corrupt root p\\xfe was registered: %v", graph)
	}
}

// TestBatchCorruptNameRejectsSiblingLegalAdjustments: one corrupt name refuses
// the entire batch — the perfectly legal repoint and delete submitted in the
// same plan must not take effect either, and neither preview nor apply may
// touch the caller's plan (its names, order, or duplicated upstreams).
func TestBatchCorruptNameRejectsSiblingLegalAdjustments(t *testing.T) {
	t.Run("legal repoint and delete beside a corrupt new root", func(t *testing.T) {
		// A<-B<-C. Legal parts: B becomes a root (edge to A dropped) and leaf C
		// is deleted; corrupt part: a new root whose name carries bad bytes.
		graph := buildGraph(t, P("A"), P("B", "A"), P("C", "B"))
		before := snapshotGraph(graph)
		plan := Plan{
			Changes:  []PlanChange{change("B"), change("x\xff")},
			Removals: []string{"C"},
		}
		planBefore := copyPlan(plan)

		if _, err := PreviewBatch(graph, plan); !errors.Is(err, ErrInvalidArgument) {
			t.Fatalf("PreviewBatch err = %v, want ErrInvalidArgument", err)
		}
		if _, err := ApplyBatch(graph, plan); !errors.Is(err, ErrInvalidArgument) {
			t.Fatalf("ApplyBatch err = %v, want ErrInvalidArgument", err)
		}
		if after := snapshotGraph(graph); !reflect.DeepEqual(after, before) {
			t.Fatalf("legal sibling adjustments took effect:\nbefore %#v\nafter  %#v", before, after)
		}
		if got := graph["B"].Parents; !reflect.DeepEqual(got, []string{"A"}) {
			t.Errorf("B.Parents = %v, want [A] (legal repoint must not apply)", got)
		}
		if _, ok := graph["C"]; !ok {
			t.Errorf("C was deleted although the batch was rejected")
		}
		if !reflect.DeepEqual(copyPlan(plan), planBefore) {
			t.Fatalf("caller's plan was modified:\nbefore %#v\nafter  %#v", planBefore, plan)
		}
	})

	t.Run("legal delete beside a corrupt removal", func(t *testing.T) {
		graph := buildGraph(t, P("A"), P("B", "A"))
		before := snapshotGraph(graph)
		// Deleting leaf B is legal; the second removal is corrupt and names no
		// one, but it still rejects everything.
		plan := Plan{Removals: []string{"B", "ghost\xff"}}

		if _, err := ApplyBatch(graph, plan); !errors.Is(err, ErrInvalidArgument) {
			t.Fatalf("err = %v, want ErrInvalidArgument", err)
		}
		if after := snapshotGraph(graph); !reflect.DeepEqual(after, before) {
			t.Fatalf("legal delete took effect beside a corrupt removal:\nbefore %#v\nafter  %#v", before, after)
		}
	})

	t.Run("submitted upstream order and duplicates are left as written", func(t *testing.T) {
		graph := buildGraph(t, P("A"), P("B"))
		plan := Plan{Changes: []PlanChange{
			{Name: "bad\xff", Upstreams: []string{"B", "A", "A"}},
		}}
		planBefore := copyPlan(plan)
		if _, err := ApplyBatch(graph, plan); !errors.Is(err, ErrInvalidArgument) {
			t.Fatalf("err = %v, want ErrInvalidArgument", err)
		}
		if !reflect.DeepEqual(copyPlan(plan), planBefore) {
			t.Fatalf("the rejected plan's upstream list was normalized:\nbefore %#v\nafter  %#v", planBefore, plan)
		}
	})
}

// TestBatchChecksNamesInPlanOrder: within a record the name is checked before
// its upstreams, and all change records (name then upstreams, in record
// order) are checked before removals — so the reported position is the
// earliest offending name the caller submitted.
func TestBatchChecksNamesInPlanOrder(t *testing.T) {
	graph := buildGraph(t, P("A"))

	// Corrupt name and corrupt upstream in the same record: name wins.
	plan := planOf(change("n\xff", "u\xfe"))
	err := mustReject(t, graph, plan)
	if !strings.Contains(err.Error(), `"name"`) || strings.Contains(err.Error(), `"upstreams"`) {
		t.Fatalf("expected the record name to be reported first, got %q", err)
	}

	// A corrupt upstream in an earlier record beats a corrupt later removal.
	plan = Plan{
		Changes:  []PlanChange{change("B", "A"), change("C", "u\xff")},
		Removals: []string{"z\xfe"},
	}
	err = mustReject(t, graph, plan)
	if !strings.Contains(err.Error(), `"upstreams"`) || !strings.Contains(err.Error(), `change record at index 1`) {
		t.Fatalf("expected the earlier corrupt upstream, got %q", err)
	}
	if strings.Contains(err.Error(), `"removals"`) {
		t.Fatalf("removals must not be reported before the earlier change, got %q", err)
	}
}

func mustReject(t *testing.T, graph map[string]*Lineage, plan Plan) error {
	t.Helper()
	_, err := PreviewBatch(graph, plan)
	if !errors.Is(err, ErrInvalidArgument) {
		t.Fatalf("err = %v, want ErrInvalidArgument", err)
	}
	return err
}

// TestBatchAcceptsGenuineUnicodeNames: legal names keep being handled by the
// existing rules with no substitution, case folding, trimming, or Unicode
// normalization — a genuine replacement character, CJK, emoji, a combining
// character (distinct from its precomposed form), and a literal backslash.
// The applied graph exports and re-reads faithfully, and the success report
// marshals to JSON carrying the genuine U+FFFD name.
func TestBatchAcceptsGenuineUnicodeNames(t *testing.T) {
	root := "�" // genuine U+FFFD replacement character, valid UTF-8
	// combiningForm is e + U+0301 combining acute accent; precomposedForm is
	// the single rune U+00E9. They display alike but are different names; both
	// are built from rune constants so the source bytes cannot be normalized.
	combiningForm := "caf" + string(rune(0x0065)) + string(rune(0x0301))
	precomposedForm := "caf" + string(rune(0x00E9))
	names := []string{
		root,
		"数据集",
		"ledger😀",
		combiningForm,
		precomposedForm,
		`back\slash`,
		" Ledger ",
	}
	changes := []PlanChange{change(names[0])}
	for _, name := range names[1:] {
		changes = append(changes, change(name, root))
	}
	plan := Plan{Changes: changes}

	graph := map[string]*Lineage{}
	report, err := ApplyBatch(graph, plan)
	if err != nil {
		t.Fatalf("ApplyBatch rejected legal Unicode names: %v", err)
	}
	for _, name := range names {
		if _, ok := graph[name]; !ok {
			t.Errorf("dataset %q was not registered", name)
		}
	}
	if graph[names[3]].Parents[0] != root || graph[names[4]].Parents[0] != root {
		t.Errorf("both combining and precomposed forms must stay distinct names pointing at the root")
	}
	if names[3] == names[4] {
		t.Fatal("test premise broken: combining and precomposed forms must be different strings")
	}

	// An applied graph with legal names must export, and re-reading must be a
	// faithful copy.
	data, err := MarshalGraphFile(graph)
	if err != nil {
		t.Fatalf("MarshalGraphFile rejected legal Unicode names: %v", err)
	}
	reRead, err := UnmarshalGraphFile(data)
	if err != nil {
		t.Fatalf("re-reading exported graph: %v", err)
	}
	gotAdj := map[string][]string{}
	for name, entry := range reRead {
		gotAdj[name] = entry.Parents
	}
	wantAdj := map[string][]string{}
	for _, name := range names {
		if name == root {
			wantAdj[name] = nil
		} else {
			wantAdj[name] = []string{root}
		}
	}
	if !reflect.DeepEqual(gotAdj, wantAdj) {
		t.Fatalf("export round trip changed names/relations:\n got %#v\nwant %#v", gotAdj, wantAdj)
	}

	// The success report must marshal, and it carries the genuine replacement
	// character rather than escaping it away.
	j, err := json.Marshal(report)
	if err != nil {
		t.Fatalf("marshaling success report: %v", err)
	}
	if !strings.Contains(string(j), root) {
		t.Fatalf("report JSON %s does not carry the genuine U+FFFD dataset", j)
	}
}

// TestBatchExistingNameRulesUnchanged: the other public rules around names
// stay exactly as they were — a legal removal of a nonexistent dataset is a
// no-op success, duplicate upstreams count as one relationship, and new
// datasets in the same batch may reference each other regardless of order.
func TestBatchExistingNameRulesUnchanged(t *testing.T) {
	t.Run("legal nonexistent removal stays a no-op", func(t *testing.T) {
		graph := buildGraph(t, P("A"))
		report, err := PreviewBatch(graph, Plan{Removals: []string{"ghost"}})
		if err != nil {
			t.Fatalf("a legal nonexistent removal must stay a no-op, got %v", err)
		}
		if len(report.RemovedDatasets) != 0 || len(report.NewDatasets) != 0 ||
			len(report.ChangedDatasets) != 0 || len(report.AddedRelations) != 0 ||
			len(report.RemovedRelations) != 0 || len(report.AffectedDownstreams) != 0 {
			t.Fatalf("no-op removal produced a non-empty report: %#v", report)
		}
	})

	t.Run("duplicate upstreams stay one relation", func(t *testing.T) {
		graph := buildGraph(t, P("A"), P("B"))
		report, err := PreviewBatch(graph, planOf(change("C", "A", "B", "A", "B")))
		if err != nil {
			t.Fatalf("PreviewBatch: %v", err)
		}
		if len(report.AddedRelations) != 2 {
			t.Fatalf("AddedRelations = %v, want the two distinct edges once each", report.AddedRelations)
		}
	})

	t.Run("new datasets may reference each other", func(t *testing.T) {
		graph := buildGraph(t, P("A"))
		report, err := PreviewBatch(graph, planOf(change("E", "D"), change("D", "C"), change("C")))
		if err != nil {
			t.Fatalf("PreviewBatch: %v", err)
		}
		want := []string{"C", "D", "E"}
		if !reflect.DeepEqual(report.NewDatasets, want) {
			t.Fatalf("NewDatasets = %v, want %v", report.NewDatasets, want)
		}
	})
}

// copyPlan deep-copies a plan so a test can prove the batch never rewrites the
// caller's names and slices.
func copyPlan(p Plan) Plan {
	q := Plan{
		Changes:  make([]PlanChange, len(p.Changes)),
		Removals: append([]string(nil), p.Removals...),
	}
	for i, c := range p.Changes {
		q.Changes[i] = PlanChange{
			Name:      c.Name,
			Upstreams: append([]string(nil), c.Upstreams...),
		}
	}
	return q
}
