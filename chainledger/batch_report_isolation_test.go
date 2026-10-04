package chainledger

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

// reportUpstreams returns the direct upstream list of one dataset in a
// report's final graph, failing the test if the dataset is absent.
func reportUpstreams(t *testing.T, report *BatchReport, name string) []string {
	t.Helper()
	for _, ds := range report.FinalGraph.Datasets {
		if ds.Name == name {
			return ds.Upstreams
		}
	}
	t.Fatalf("dataset %q not in report final graph: %+v", name, report.FinalGraph.Datasets)
	return nil
}

// mustMarshalJSON marshals v to JSON, failing the test on error. Comparing
// marshaled bytes pins a value's observable content at a point in time.
func mustMarshalJSON(t *testing.T, v any) string {
	t.Helper()
	data, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("json.Marshal unexpected error: %v", err)
	}
	return string(data)
}

// TestBatchReplaceUpstreamPreviewAndApplyIsolation is the headline isolation
// scenario: the current graph has the original upstream (old), the new
// upstreams (n1, n2), and a downstream (down) that keeps depending on the
// adjusted dataset (mid). A successful preview must leave the current graph
// and the submitted plan untouched while the report describes the replaced
// graph; a successful apply must move the current graph to the new direct
// upstreams, keep the downstream relation, and return a report consistent
// with that apply. The plan carries shuffled, duplicated upstreams: the
// results follow the existing sort/dedupe rules while the caller's plan
// keeps its original shape.
func TestBatchReplaceUpstreamPreviewAndApplyIsolation(t *testing.T) {
	graph := buildGraph(t,
		P("old"),
		P("n1"),
		P("n2"),
		P("mid", "old"),
		P("down", "mid"),
	)
	// Shuffled upstream order with a duplicate; the caller's record must
	// survive the calls verbatim.
	plan := planOf(change("mid", "n2", "n1", "n2"))
	planBefore := mustMarshalJSON(t, plan)
	graphBefore := dump(graph)

	preview, err := PreviewBatch(graph, plan)
	if err != nil {
		t.Fatalf("PreviewBatch unexpected error: %v", err)
	}

	// Preview must not rewrite the current graph or the submitted plan.
	if got := dump(graph); got != graphBefore {
		t.Fatalf("preview mutated the current graph:\n got %s\nwant %s", got, graphBefore)
	}
	if got := mustMarshalJSON(t, plan); got != planBefore {
		t.Fatalf("preview mutated the submitted plan:\n got %s\nwant %s", got, planBefore)
	}

	// The preview report describes the replaced graph: mid's direct
	// upstreams are sorted and deduped, down still depends on mid, and old
	// is still registered (now a root).
	if got := reportUpstreams(t, preview, "mid"); !reflect.DeepEqual(got, []string{"n1", "n2"}) {
		t.Errorf("preview final graph mid.Upstreams = %v, want [n1 n2]", got)
	}
	if got := reportUpstreams(t, preview, "down"); !reflect.DeepEqual(got, []string{"mid"}) {
		t.Errorf("preview final graph down.Upstreams = %v, want [mid]", got)
	}
	if got := reportUpstreams(t, preview, "old"); len(got) != 0 {
		t.Errorf("preview final graph old.Upstreams = %v, want empty (root)", got)
	}

	applied, err := ApplyBatch(graph, plan)
	if err != nil {
		t.Fatalf("ApplyBatch unexpected error: %v", err)
	}
	if !reflect.DeepEqual(applied, preview) {
		t.Fatalf("apply report differs from preview:\n apply=%#v\n preview=%#v", applied, preview)
	}

	// The current graph adopts the new direct upstreams and keeps the
	// original downstream relation.
	if got := graph["mid"].Parents; !reflect.DeepEqual(got, []string{"n1", "n2"}) {
		t.Errorf("mid.Parents = %v, want [n1 n2]", got)
	}
	if got := graph["down"].Parents; !reflect.DeepEqual(got, []string{"mid"}) {
		t.Errorf("down.Parents = %v, want [mid] (downstream relation must be kept)", got)
	}
	if got := graph["n1"].Children; !reflect.DeepEqual(got, []string{"mid"}) {
		t.Errorf("n1.Children = %v, want [mid]", got)
	}
	if got := graph["old"].Children; len(got) != 0 {
		t.Errorf("old.Children = %v, want empty (old upstream relation replaced)", got)
	}

	// The submitted plan is still exactly what the caller wrote.
	if got := mustMarshalJSON(t, plan); got != planBefore {
		t.Fatalf("apply mutated the submitted plan:\n got %s\nwant %s", got, planBefore)
	}
	if !reflect.DeepEqual(plan.Changes[0].Upstreams, []string{"n2", "n1", "n2"}) {
		t.Errorf("plan upstreams = %v, want the submitted [n2 n1 n2] (unsorted, duplicated)", plan.Changes[0].Upstreams)
	}
}

// TestBatchPlanEditsAfterReturnDoNotLeak verifies that once a call has
// returned successfully, editing the original plan — upstream names inside a
// record, the record contents, or the record list itself — changes neither
// the report that call produced nor the graph an apply committed.
func TestBatchPlanEditsAfterReturnDoNotLeak(t *testing.T) {
	t.Run("after preview", func(t *testing.T) {
		graph := buildGraph(t, P("old"), P("new"), P("mid", "old"), P("down", "mid"))
		plan := planOf(change("mid", "new"))
		report, err := PreviewBatch(graph, plan)
		if err != nil {
			t.Fatalf("PreviewBatch unexpected error: %v", err)
		}
		reportBefore := mustMarshalJSON(t, report)
		graphBefore := dump(graph)

		plan.Changes[0].Upstreams[0] = "tampered"
		plan.Changes[0].Name = "tampered"
		plan.Changes = append(plan.Changes, change("down"))

		if got := mustMarshalJSON(t, report); got != reportBefore {
			t.Errorf("preview report changed after the plan was edited:\n got %s\nwant %s", got, reportBefore)
		}
		if got := dump(graph); got != graphBefore {
			t.Errorf("graph changed after the plan was edited:\n got %s\nwant %s", got, graphBefore)
		}
	})

	t.Run("after apply", func(t *testing.T) {
		graph := buildGraph(t, P("old"), P("new"), P("mid", "old"), P("down", "mid"))
		plan := planOf(change("mid", "new"))
		report, err := ApplyBatch(graph, plan)
		if err != nil {
			t.Fatalf("ApplyBatch unexpected error: %v", err)
		}
		reportBefore := mustMarshalJSON(t, report)
		graphBefore := dump(graph)

		plan.Changes[0].Upstreams[0] = "tampered"
		plan.Changes[0].Name = "tampered"
		plan.Changes = append(plan.Changes, change("down"))

		if got := mustMarshalJSON(t, report); got != reportBefore {
			t.Errorf("apply report changed after the plan was edited:\n got %s\nwant %s", got, reportBefore)
		}
		if got := dump(graph); got != graphBefore {
			t.Errorf("applied graph changed after the plan was edited:\n got %s\nwant %s", got, graphBefore)
		}
		if got := graph["mid"].Parents; !reflect.DeepEqual(got, []string{"new"}) {
			t.Errorf("mid.Parents = %v, want [new] (applied graph must not follow plan edits)", got)
		}
	})
}

// TestBatchReportEditsDoNotLeak verifies the reverse direction: editing a
// returned report — its final-graph records, the upstream lists inside them,
// or its name lists — affects only that report. The submitted plan, the
// current graph, and another report returned by an earlier call must stay
// intact, and one dataset's record must not share storage with another
// dataset that has the same upstreams.
func TestBatchReportEditsDoNotLeak(t *testing.T) {
	// A and B share the same upstream S; the plan repoints both at S and T.
	graph := buildGraph(t, P("S"), P("T"), P("A", "S"), P("B", "S"))
	plan := planOf(change("A", "T", "S"), change("B", "T", "S", "S"))
	planBefore := mustMarshalJSON(t, plan)

	preview, err := PreviewBatch(graph, plan)
	if err != nil {
		t.Fatalf("PreviewBatch unexpected error: %v", err)
	}
	applied, err := ApplyBatch(graph, plan)
	if err != nil {
		t.Fatalf("ApplyBatch unexpected error: %v", err)
	}
	appliedBefore := mustMarshalJSON(t, applied)
	graphBefore := dump(graph)

	// Tamper with the preview report: a final-graph record's name and
	// upstream list, plus the top-level name and relation lists.
	for i := range preview.FinalGraph.Datasets {
		if preview.FinalGraph.Datasets[i].Name == "A" {
			preview.FinalGraph.Datasets[i].Upstreams[0] = "tampered"
			preview.FinalGraph.Datasets[i].Name = "tampered"
		}
	}
	preview.ChangedDatasets[0] = "tampered"
	preview.AddedRelations[0].Upstream = "tampered"
	preview.AffectedDownstreams = append(preview.AffectedDownstreams, "tampered")

	// The other returned report is untouched.
	if got := mustMarshalJSON(t, applied); got != appliedBefore {
		t.Errorf("apply report changed after the preview report was edited:\n got %s\nwant %s", got, appliedBefore)
	}
	// The current graph is untouched.
	if got := dump(graph); got != graphBefore {
		t.Errorf("graph changed after the report was edited:\n got %s\nwant %s", got, graphBefore)
	}
	if got := graph["A"].Parents; !reflect.DeepEqual(got, []string{"S", "T"}) {
		t.Errorf("A.Parents = %v, want [S T]", got)
	}
	// The submitted plan is untouched.
	if got := mustMarshalJSON(t, plan); got != planBefore {
		t.Errorf("plan changed after the report was edited:\n got %s\nwant %s", got, planBefore)
	}
	// Editing A's record must not drag B's record along even though both
	// datasets have identical upstream lists.
	if got := reportUpstreams(t, preview, "B"); !reflect.DeepEqual(got, []string{"S", "T"}) {
		t.Errorf("preview final graph B.Upstreams = %v, want [S T] (sibling record must not share storage)", got)
	}
	if got := reportUpstreams(t, applied, "A"); !reflect.DeepEqual(got, []string{"S", "T"}) {
		t.Errorf("apply report A.Upstreams = %v, want [S T]", got)
	}
}

// TestBatchReportsSurviveLaterBatches verifies that reports returned by
// earlier calls keep describing the relations as of their own return: a
// later legal upstream replacement through the same batch feature moves the
// current graph on, but the retained preview and apply reports are
// byte-identical when marshaled again.
func TestBatchReportsSurviveLaterBatches(t *testing.T) {
	graph := buildGraph(t, P("old"), P("new"), P("mid", "old"), P("down", "mid"))
	plan1 := planOf(change("mid", "new"))

	preview1, err := PreviewBatch(graph, plan1)
	if err != nil {
		t.Fatalf("PreviewBatch unexpected error: %v", err)
	}
	applied1, err := ApplyBatch(graph, plan1)
	if err != nil {
		t.Fatalf("ApplyBatch unexpected error: %v", err)
	}
	preview1Before := mustMarshalJSON(t, preview1)
	applied1Before := mustMarshalJSON(t, applied1)

	// A later, legal replacement of the same dataset's upstreams.
	plan2 := planOf(change("mid", "old"))
	if _, err := ApplyBatch(graph, plan2); err != nil {
		t.Fatalf("second ApplyBatch unexpected error: %v", err)
	}
	if got := graph["mid"].Parents; !reflect.DeepEqual(got, []string{"old"}) {
		t.Fatalf("mid.Parents = %v, want [old] (the second batch must have moved the graph on)", got)
	}

	// The retained reports still describe their own return-time relations.
	if got := mustMarshalJSON(t, preview1); got != preview1Before {
		t.Errorf("retained preview report changed after a later batch:\n got %s\nwant %s", got, preview1Before)
	}
	if got := mustMarshalJSON(t, applied1); got != applied1Before {
		t.Errorf("retained apply report changed after a later batch:\n got %s\nwant %s", got, applied1Before)
	}
	if got := reportUpstreams(t, preview1, "mid"); !reflect.DeepEqual(got, []string{"new"}) {
		t.Errorf("retained preview report mid.Upstreams = %v, want [new]", got)
	}
	if got := reportUpstreams(t, applied1, "mid"); !reflect.DeepEqual(got, []string{"new"}) {
		t.Errorf("retained apply report mid.Upstreams = %v, want [new]", got)
	}
}

// TestBatchEmptyUpstreamsRootBoundary pins the empty-upstream boundary:
// replacing a dataset's upstreams with an empty list turns it into a root in
// both the current graph and that call's report, while a previously returned
// report keeps the non-empty upstreams it described.
func TestBatchEmptyUpstreamsRootBoundary(t *testing.T) {
	graph := buildGraph(t, P("A"), P("A2"), P("B", "A"), P("C", "B"))

	plan1 := planOf(change("B", "A2"))
	earlier, err := PreviewBatch(graph, plan1)
	if err != nil {
		t.Fatalf("PreviewBatch unexpected error: %v", err)
	}
	if _, err := ApplyBatch(graph, plan1); err != nil {
		t.Fatalf("ApplyBatch plan1 unexpected error: %v", err)
	}
	if got := reportUpstreams(t, earlier, "B"); !reflect.DeepEqual(got, []string{"A2"}) {
		t.Fatalf("earlier report B.Upstreams = %v, want [A2]", got)
	}

	// Empty upstream list: B becomes a root.
	plan2 := planOf(change("B"))
	report, err := ApplyBatch(graph, plan2)
	if err != nil {
		t.Fatalf("ApplyBatch plan2 unexpected error: %v", err)
	}
	if got := graph["B"].Parents; len(got) != 0 {
		t.Errorf("B.Parents = %v, want empty (root)", got)
	}
	if got := graph["C"].Parents; !reflect.DeepEqual(got, []string{"B"}) {
		t.Errorf("C.Parents = %v, want [B] (downstream relation must be kept)", got)
	}
	roots := Roots(graph)
	foundB := false
	for _, r := range roots {
		if r == "B" {
			foundB = true
		}
	}
	if !foundB {
		t.Errorf("Roots = %v, want B among them", roots)
	}

	// This call's report shows B as a root with a non-nil empty list that
	// encodes as [].
	got := reportUpstreams(t, report, "B")
	if got == nil || len(got) != 0 {
		t.Errorf("report final graph B.Upstreams = %v, want non-nil empty list", got)
	}
	if !strings.Contains(mustMarshalJSON(t, report), `"upstreams":[]`) {
		t.Errorf("report JSON does not encode B's empty upstreams as []: %s", mustMarshalJSON(t, report))
	}

	// The earlier report still shows the non-empty upstreams it described.
	if got := reportUpstreams(t, earlier, "B"); !reflect.DeepEqual(got, []string{"A2"}) {
		t.Errorf("earlier report B.Upstreams = %v, want [A2] (must survive the later root change)", got)
	}
}
