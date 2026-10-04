package chainledger

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

// This file pins down the independence between the three things a batch
// caller can hold at once:
//
//   - the submitted Plan (which the caller may keep editing for display or
//     reuse after PreviewBatch/ApplyBatch has returned);
//   - the current in-memory graph (which later batches keep adjusting);
//   - the already returned *BatchReport values (which describe the graph as it
//     was when that call returned, never anything that happened afterwards).
//
// Every scenario is built around replacing one existing dataset's direct
// upstreams: the graph contains the old upstream, the new upstream(s), and a
// downstream that keeps depending on the adjusted dataset. The focus is the
// direct-upstream list of every dataset in the report's final graph.

// independenceGraph builds: roots A, D, E; B depends on A; C depends on B.
func independenceGraph(t *testing.T) map[string]*Lineage {
	t.Helper()
	return buildGraph(t, P("A"), P("D"), P("E"), P("B", "A"), P("C", "B"))
}

// replacementPlan is deliberately out of order and duplicated
// (E, D, D, E): the result must normalize to [D E], while the submitted plan
// keeps exactly these bytes.
func replacementPlan() Plan {
	return planOf(change("B", "E", "D", "D", "E"))
}

// clonePlan returns a deep copy of p so a test can compare the whole plan
// value after the caller mutates every slice it owns.
func clonePlan(p Plan) Plan {
	c := Plan{
		Changes:  make([]PlanChange, len(p.Changes)),
		Removals: append([]string(nil), p.Removals...),
	}
	for i, ch := range p.Changes {
		c.Changes[i] = PlanChange{
			Name:      ch.Name,
			Upstreams: append([]string(nil), ch.Upstreams...),
		}
	}
	return c
}

// datasetIndex returns the index of name in the report's final graph, or -1.
func datasetIndex(r *BatchReport, name string) int {
	for i := range r.FinalGraph.Datasets {
		if r.FinalGraph.Datasets[i].Name == name {
			return i
		}
	}
	return -1
}

// mustReportDataset returns the final-graph record for name (a value copy).
func mustReportDataset(t *testing.T, r *BatchReport, name string) GraphDataset {
	t.Helper()
	if i := datasetIndex(r, name); i >= 0 {
		return r.FinalGraph.Datasets[i]
	}
	t.Fatalf("report has no dataset record %q", name)
	return GraphDataset{}
}

// mustMarshalReport renders a report the way a caller displaying it would.
func mustMarshalReport(t *testing.T, r *BatchReport) string {
	t.Helper()
	b, err := json.Marshal(r)
	if err != nil {
		t.Fatalf("json.Marshal report unexpected error: %v", err)
	}
	return string(b)
}

// TestBatchPreviewKeepsCurrentGraphAndDescribesReplacement pins the preview
// contract: a successful preview leaves every current-graph relationship
// exactly where it was, while the returned report describes the post-replace
// graph (new upstreams adopted, downstream kept).
func TestBatchPreviewKeepsCurrentGraphAndDescribesReplacement(t *testing.T) {
	graph := independenceGraph(t)
	plan := replacementPlan()
	before := dump(graph)

	report, err := PreviewBatch(graph, plan)
	if err != nil {
		t.Fatalf("PreviewBatch unexpected error: %v", err)
	}

	// The current graph is untouched: B still points at the old upstream A and
	// nothing has been linked to D or E.
	if got := dump(graph); got != before {
		t.Fatalf("preview mutated the current graph:\n got %s\nwant %s", got, before)
	}
	if got := graph["B"].Parents; !reflect.DeepEqual(got, []string{"A"}) {
		t.Errorf("B.Parents = %v, want [A] (preview must not replace upstreams)", got)
	}
	if got := graph["B"].Children; !reflect.DeepEqual(got, []string{"C"}) {
		t.Errorf("B.Children = %v, want [C]", got)
	}
	if got := graph["A"].Children; !reflect.DeepEqual(got, []string{"B"}) {
		t.Errorf("A.Children = %v, want [B] (old edge still present after preview)", got)
	}
	if got := graph["D"].Children; len(got) != 0 {
		t.Errorf("D.Children = %v, want empty (preview must not create edges)", got)
	}
	if got := graph["C"].Parents; !reflect.DeepEqual(got, []string{"B"}) {
		t.Errorf("C.Parents = %v, want [B]", got)
	}

	// The report describes the replaced graph: B normalized to [D E], the old
	// upstream A is now a root, and the downstream C is untouched.
	if got := mustReportDataset(t, report, "B").Upstreams; !reflect.DeepEqual(got, []string{"D", "E"}) {
		t.Errorf("report B.upstreams = %v, want [D E]", got)
	}
	if got := mustReportDataset(t, report, "A").Upstreams; !reflect.DeepEqual(got, []string{}) {
		t.Errorf("report A.upstreams = %v, want [] (old upstream stays in graph as a root)", got)
	}
	if got := mustReportDataset(t, report, "C").Upstreams; !reflect.DeepEqual(got, []string{"B"}) {
		t.Errorf("report C.upstreams = %v, want [B] (downstream relationship kept)", got)
	}
	if got := mustReportDataset(t, report, "D").Upstreams; !reflect.DeepEqual(got, []string{}) {
		t.Errorf("report D.upstreams = %v, want []", got)
	}
	if !reflect.DeepEqual(report.ChangedDatasets, []string{"B"}) {
		t.Errorf("ChangedDatasets = %v, want [B]", report.ChangedDatasets)
	}
	wantAdded := []Relation{{Upstream: "D", Downstream: "B"}, {Upstream: "E", Downstream: "B"}}
	if !reflect.DeepEqual(report.AddedRelations, wantAdded) {
		t.Errorf("AddedRelations = %v, want %v", report.AddedRelations, wantAdded)
	}
	wantRemoved := []Relation{{Upstream: "A", Downstream: "B"}}
	if !reflect.DeepEqual(report.RemovedRelations, wantRemoved) {
		t.Errorf("RemovedRelations = %v, want %v", report.RemovedRelations, wantRemoved)
	}
	if !reflect.DeepEqual(report.AffectedDownstreams, []string{"C"}) {
		t.Errorf("AffectedDownstreams = %v, want [C]", report.AffectedDownstreams)
	}
}

// TestBatchApplyAdoptsUpstreamsAndPreservesDownstream pins the apply contract:
// the current graph adopts the new direct upstreams (sorted, deduplicated, and
// copied off the caller's slices), the downstream relationship survives, and
// the returned report matches that application.
func TestBatchApplyAdoptsUpstreamsAndPreservesDownstream(t *testing.T) {
	graph := independenceGraph(t)
	plan := replacementPlan()

	applied, err := ApplyBatch(graph, plan)
	if err != nil {
		t.Fatalf("ApplyBatch unexpected error: %v", err)
	}

	if got := graph["B"].Parents; !reflect.DeepEqual(got, []string{"D", "E"}) {
		t.Errorf("B.Parents = %v, want [D E]", got)
	}
	if got := graph["B"].Children; !reflect.DeepEqual(got, []string{"C"}) {
		t.Errorf("B.Children = %v, want [C] (downstream must survive the replacement)", got)
	}
	if got := graph["C"].Parents; !reflect.DeepEqual(got, []string{"B"}) {
		t.Errorf("C.Parents = %v, want [B]", got)
	}
	if got := graph["A"].Children; len(got) != 0 {
		t.Errorf("A.Children = %v, want empty (old edge dissolved)", got)
	}
	if got := graph["D"].Children; !reflect.DeepEqual(got, []string{"B"}) {
		t.Errorf("D.Children = %v, want [B]", got)
	}
	if got := graph["E"].Children; !reflect.DeepEqual(got, []string{"B"}) {
		t.Errorf("E.Children = %v, want [B]", got)
	}
	if roots := Roots(graph); !reflect.DeepEqual(roots, []string{"A", "D", "E"}) {
		t.Errorf("Roots = %v, want [A D E]", roots)
	}

	// The report returned by apply matches a preview against the same
	// pre-application inputs.
	preview, err := PreviewBatch(independenceGraph(t), replacementPlan())
	if err != nil {
		t.Fatalf("PreviewBatch unexpected error: %v", err)
	}
	if !reflect.DeepEqual(applied, preview) {
		t.Errorf("apply report differs from preview:\n apply=%#v\n preview=%#v", applied, preview)
	}
	if got := mustReportDataset(t, applied, "B").Upstreams; !reflect.DeepEqual(got, []string{"D", "E"}) {
		t.Errorf("applied report B.upstreams = %v, want [D E]", got)
	}
	if got := mustReportDataset(t, applied, "C").Upstreams; !reflect.DeepEqual(got, []string{"B"}) {
		t.Errorf("applied report C.upstreams = %v, want [B]", got)
	}

	// The applied graph keeps no backing memory from the submitted plan:
	// rewriting and appending to the plan's slices must not move the graph.
	plan.Changes[0].Upstreams[0] = "A"
	plan.Changes[0].Upstreams = append(plan.Changes[0].Upstreams, "ghost")
	if got := graph["B"].Parents; !reflect.DeepEqual(got, []string{"D", "E"}) {
		t.Fatalf("editing the submitted plan altered applied graph B.Parents: %v", got)
	}
	if got := graph["B"].Children; !reflect.DeepEqual(got, []string{"C"}) {
		t.Fatalf("editing the submitted plan altered applied graph B.Children: %v", got)
	}

	// The applied report keeps a frozen copy: in-place edits of the graph's
	// stored parents after apply must not move the returned report.
	graph["B"].Parents[0] = "HACK"
	graph["B"].Parents = append(graph["B"].Parents, "ghost")
	if got := mustReportDataset(t, applied, "B").Upstreams; !reflect.DeepEqual(got, []string{"D", "E"}) {
		t.Fatalf("applied report followed a later graph edit: got %v, want frozen [D E]", got)
	}
	graph["B"].Parents = []string{"D", "E"}

	// The reverse direction on a fresh preview: editing a returned report for
	// display never reaches the graph it was computed from.
	displayGraph := independenceGraph(t)
	displayReport, err := PreviewBatch(displayGraph, replacementPlan())
	if err != nil {
		t.Fatalf("display PreviewBatch unexpected error: %v", err)
	}
	iDisplay := datasetIndex(displayReport, "B")
	displayReport.FinalGraph.Datasets[iDisplay].Upstreams[0] = "HACK"
	displayReport.FinalGraph.Datasets[iDisplay].Upstreams = append(displayReport.FinalGraph.Datasets[iDisplay].Upstreams, "ghost")
	if got := displayGraph["B"].Parents; !reflect.DeepEqual(got, []string{"A"}) {
		t.Fatalf("editing a preview report altered graph B.Parents: %v", got)
	}
	if got := displayGraph["D"].Children; len(got) != 0 {
		t.Fatalf("editing a preview report created edges: D.Children = %v", got)
	}
}

// TestBatchPlanSubmittedVerbatimAfterCall verifies that preview and apply only
// read the plan: shuffled order, duplicates, and record order survive both
// calls unchanged even though the report normalizes the upstreams.
func TestBatchPlanSubmittedVerbatimAfterCall(t *testing.T) {
	plan := replacementPlan()
	wantPlan := clonePlan(plan)

	previewGraph := independenceGraph(t)
	if _, err := PreviewBatch(previewGraph, plan); err != nil {
		t.Fatalf("PreviewBatch unexpected error: %v", err)
	}
	if !reflect.DeepEqual(plan, wantPlan) {
		t.Errorf("preview rewrote the submitted plan:\n got %#v\nwant %#v", plan, wantPlan)
	}

	applyGraph := independenceGraph(t)
	report, err := ApplyBatch(applyGraph, plan)
	if err != nil {
		t.Fatalf("ApplyBatch unexpected error: %v", err)
	}
	if !reflect.DeepEqual(plan, wantPlan) {
		t.Errorf("apply rewrote the submitted plan:\n got %#v\nwant %#v", plan, wantPlan)
	}
	if got := plan.Changes[0].Upstreams; !reflect.DeepEqual(got, []string{"E", "D", "D", "E"}) {
		t.Errorf("submitted upstreams = %v, want verbatim [E D D E]", got)
	}
	if got := mustReportDataset(t, report, "B").Upstreams; !reflect.DeepEqual(got, []string{"D", "E"}) {
		t.Errorf("report B.upstreams = %v, want sorted/deduped [D E]", got)
	}
}

// TestBatchEditingPlanAfterSuccessLeavesReportAndGraphUntouched verifies that
// once a successful call has returned, no later edit to the caller's plan —
// renaming the target, rewriting upstream names, appending entries or whole
// records — can change that report or the already applied graph.
func TestBatchEditingPlanAfterSuccessLeavesReportAndGraphUntouched(t *testing.T) {
	plan := replacementPlan()

	preview, err := PreviewBatch(independenceGraph(t), plan)
	if err != nil {
		t.Fatalf("PreviewBatch unexpected error: %v", err)
	}
	applyGraph := independenceGraph(t)
	applied, err := ApplyBatch(applyGraph, plan)
	if err != nil {
		t.Fatalf("ApplyBatch unexpected error: %v", err)
	}
	// A plan whose upstreams arrive already sorted and duplicate-free is the
	// tempting shortcut case (a normalizer could return the caller's slice
	// verbatim): its report must still own an independent copy.
	cleanPlan := planOf(change("B", "D", "E"))
	clean, err := PreviewBatch(independenceGraph(t), cleanPlan)
	if err != nil {
		t.Fatalf("clean PreviewBatch unexpected error: %v", err)
	}
	previewJSON := mustMarshalReport(t, preview)
	appliedJSON := mustMarshalReport(t, applied)
	cleanJSON := mustMarshalReport(t, clean)
	graphDump := dump(applyGraph)

	// Rewrite the plan in place as thoroughly as a caller might: target name,
	// upstream contents, upstream capacity, and the change list itself.
	plan.Changes[0].Name = "HACK"
	plan.Changes[0].Upstreams[0] = "ghost"
	plan.Changes[0].Upstreams[1] = "ghost"
	plan.Changes[0].Upstreams = append(plan.Changes[0].Upstreams, "A", "B", "C")
	plan.Changes = append(plan.Changes, change("C"))
	plan.Removals = append(plan.Removals, "A")
	// And the already-normalized variant, rewritten in place and via append.
	cleanPlan.Changes[0].Upstreams[0] = "ghost"
	cleanPlan.Changes[0].Upstreams = append(cleanPlan.Changes[0].Upstreams, "ghost")

	if got := mustMarshalReport(t, preview); got != previewJSON {
		t.Errorf("preview report changed after the plan was edited:\n got %s\nwant %s", got, previewJSON)
	}
	if got := mustMarshalReport(t, applied); got != appliedJSON {
		t.Errorf("apply report changed after the plan was edited:\n got %s\nwant %s", got, appliedJSON)
	}
	if got := mustMarshalReport(t, clean); got != cleanJSON {
		t.Errorf("sorted/unique-input report changed after its plan was edited:\n got %s\nwant %s", got, cleanJSON)
	}
	if got := mustReportDataset(t, clean, "B").Upstreams; !reflect.DeepEqual(got, []string{"D", "E"}) {
		t.Errorf("clean report B.upstreams = %v, want frozen [D E]", got)
	}
	if got := mustReportDataset(t, preview, "B").Upstreams; !reflect.DeepEqual(got, []string{"D", "E"}) {
		t.Errorf("preview B.upstreams = %v, want frozen [D E]", got)
	}
	if got := dump(applyGraph); got != graphDump {
		t.Errorf("applied graph changed after the plan was edited:\n got %s\nwant %s", got, graphDump)
	}
	if got := applyGraph["C"].Parents; !reflect.DeepEqual(got, []string{"B"}) {
		t.Errorf("C.Parents = %v, want [B] (appended plan change must not reach the graph)", got)
	}
	if _, removed := applyGraph["A"]; !removed {
		t.Errorf("dataset A vanished from the applied graph after a plan removal edit")
	}
}

// TestBatchEditingReportLeavesPlanGraphAndOtherReportUntouched verifies the
// display direction: a caller that edits a returned report for presentation
// (rewriting an upstream in place, appending an entry, or adding a whole
// record) affects only that report copy.
func TestBatchEditingReportLeavesPlanGraphAndOtherReportUntouched(t *testing.T) {
	graph := independenceGraph(t)
	plan := replacementPlan()
	wantPlan := clonePlan(plan)

	first, err := PreviewBatch(graph, plan)
	if err != nil {
		t.Fatalf("first PreviewBatch unexpected error: %v", err)
	}
	// A second, independently returned report of the same call inputs.
	other, err := PreviewBatch(graph, plan)
	if err != nil {
		t.Fatalf("second PreviewBatch unexpected error: %v", err)
	}
	otherJSON := mustMarshalReport(t, other)
	graphDump := dump(graph)

	// Mutate the first report for display: overwrite in place, append into the
	// same backing array, and add a fabricated record.
	i := datasetIndex(first, "B")
	if i < 0 {
		t.Fatalf("first report has no dataset B")
	}
	first.FinalGraph.Datasets[i].Upstreams[0] = "HACK"
	first.FinalGraph.Datasets[i].Upstreams = append(first.FinalGraph.Datasets[i].Upstreams, "DISPLAY")
	first.FinalGraph.Datasets = append(first.FinalGraph.Datasets, GraphDataset{Name: "FAKE", Upstreams: []string{}})
	first.ChangedDatasets[0] = "HACK"
	first.NewDatasets = append(first.NewDatasets, "DISPLAY")

	if !reflect.DeepEqual(plan, wantPlan) {
		t.Errorf("editing the report rewrote the submitted plan:\n got %#v\nwant %#v", plan, wantPlan)
	}
	if got := dump(graph); got != graphDump {
		t.Errorf("editing the report mutated the current graph:\n got %s\nwant %s", got, graphDump)
	}
	if got := graph["B"].Parents; !reflect.DeepEqual(got, []string{"A"}) {
		t.Errorf("B.Parents = %v, want [A] (report edits must not reach the graph)", got)
	}
	if got := mustMarshalReport(t, other); got != otherJSON {
		t.Errorf("the other returned report changed:\n got %s\nwant %s", got, otherJSON)
	}
	if got := mustReportDataset(t, other, "B").Upstreams; !reflect.DeepEqual(got, []string{"D", "E"}) {
		t.Errorf("other report B.upstreams = %v, want untouched [D E]", got)
	}

	// Sanity: the edited report really did change locally.
	if got := first.FinalGraph.Datasets[i].Upstreams; reflect.DeepEqual(got, []string{"D", "E"}) {
		t.Errorf("editing the report had no local effect: B.upstreams = %v", got)
	}
}

// TestBatchReturnedReportsFreezeAcrossLaterBatches verifies that after a later
// legal batch adjusts the current graph, the earlier preview and apply reports
// still describe the relationships at the moment each returned, and a report
// the caller never edited re-emits as byte-identical JSON.
func TestBatchReturnedReportsFreezeAcrossLaterBatches(t *testing.T) {
	graph := independenceGraph(t)

	preview, err := PreviewBatch(graph, replacementPlan())
	if err != nil {
		t.Fatalf("first PreviewBatch unexpected error: %v", err)
	}
	applied, err := ApplyBatch(graph, replacementPlan())
	if err != nil {
		t.Fatalf("first ApplyBatch unexpected error: %v", err)
	}
	previewJSON := mustMarshalReport(t, preview)
	appliedJSON := mustMarshalReport(t, applied)

	// A second, legal replacement through the existing API: C switches from B
	// to D. This removes the B->C downstream relationship from the current
	// graph entirely.
	second := planOf(change("C", "D"))
	if _, err := ApplyBatch(graph, second); err != nil {
		t.Fatalf("second ApplyBatch unexpected error: %v", err)
	}
	if got := graph["C"].Parents; !reflect.DeepEqual(got, []string{"D"}) {
		t.Fatalf("C.Parents = %v, want [D] after the second batch", got)
	}
	if got := graph["B"].Parents; !reflect.DeepEqual(got, []string{"D", "E"}) {
		t.Fatalf("B.Parents = %v, want [D E]", got)
	}
	if got := graph["B"].Children; len(got) != 0 {
		t.Fatalf("B.Children = %v, want empty after C was repointed", got)
	}
	if got := graph["D"].Children; !reflect.DeepEqual(got, []string{"B", "C"}) {
		t.Fatalf("D.Children = %v, want [B C]", got)
	}

	// Both earlier reports still describe their return-time graph: C depended
	// on B then, B already depended on D and E.
	for name, r := range map[string]*BatchReport{"preview": preview, "apply": applied} {
		if got := mustReportDataset(t, r, "B").Upstreams; !reflect.DeepEqual(got, []string{"D", "E"}) {
			t.Errorf("%s report B.upstreams = %v, want frozen [D E]", name, got)
		}
		if got := mustReportDataset(t, r, "C").Upstreams; !reflect.DeepEqual(got, []string{"B"}) {
			t.Errorf("%s report C.upstreams = %v, want frozen [B]", name, got)
		}
	}
	if got := mustMarshalReport(t, preview); got != previewJSON {
		t.Errorf("preview report JSON drifted after a later batch:\n got %s\nwant %s", got, previewJSON)
	}
	if got := mustMarshalReport(t, applied); got != appliedJSON {
		t.Errorf("apply report JSON drifted after a later batch:\n got %s\nwant %s", got, appliedJSON)
	}
}

// TestBatchSharedUpstreamRecordsAreIndependentInReport verifies that when two
// datasets end up with the same direct-upstream list, each report record owns
// that list: editing one dataset's displayed upstreams must not drag the
// other dataset's record along, in either the report or the applied graph.
func TestBatchSharedUpstreamRecordsAreIndependentInReport(t *testing.T) {
	// Roots A, D, E; both B and F currently depend on A; C depends on B.
	graph := buildGraph(t, P("A"), P("D"), P("E"), P("B", "A"), P("F", "A"), P("C", "B"))
	plan := planOf(
		change("B", "E", "D", "D", "E"), // normalizes to [D E]
		change("F", "D", "E"),           // same final upstreams
	)

	report, err := PreviewBatch(graph, plan)
	if err != nil {
		t.Fatalf("PreviewBatch unexpected error: %v", err)
	}
	iB := datasetIndex(report, "B")
	iF := datasetIndex(report, "F")
	if iB < 0 || iF < 0 {
		t.Fatalf("report missing records: B=%d F=%d", iB, iF)
	}
	if got := report.FinalGraph.Datasets[iB].Upstreams; !reflect.DeepEqual(got, []string{"D", "E"}) {
		t.Fatalf("report B.upstreams = %v, want [D E]", got)
	}
	if got := report.FinalGraph.Datasets[iF].Upstreams; !reflect.DeepEqual(got, []string{"D", "E"}) {
		t.Fatalf("report F.upstreams = %v, want [D E]", got)
	}

	// Edit only B's displayed list, in place and via append.
	report.FinalGraph.Datasets[iB].Upstreams[0] = "X"
	report.FinalGraph.Datasets[iB].Upstreams = append(report.FinalGraph.Datasets[iB].Upstreams, "Y")

	if got := report.FinalGraph.Datasets[iF].Upstreams; !reflect.DeepEqual(got, []string{"D", "E"}) {
		t.Errorf("F.upstreams = %v, want untouched [D E] after editing B's record", got)
	}
	if got := mustReportDataset(t, report, "C").Upstreams; !reflect.DeepEqual(got, []string{"B"}) {
		t.Errorf("C.upstreams = %v, want untouched [B]", got)
	}

	// Applying the still-verbatim plan commits independent lists to the graph
	// as well: F must not have inherited B's display edits.
	if _, err := ApplyBatch(graph, plan); err != nil {
		t.Fatalf("ApplyBatch unexpected error: %v", err)
	}
	if got := graph["B"].Parents; !reflect.DeepEqual(got, []string{"D", "E"}) {
		t.Errorf("graph B.Parents = %v, want [D E]", got)
	}
	if got := graph["F"].Parents; !reflect.DeepEqual(got, []string{"D", "E"}) {
		t.Errorf("graph F.Parents = %v, want [D E] (must not follow B's report edit)", got)
	}
	if got := graph["D"].Children; !reflect.DeepEqual(got, []string{"B", "F"}) {
		t.Errorf("D.Children = %v, want [B F]", got)
	}
	if got := graph["B"].Children; !reflect.DeepEqual(got, []string{"C"}) {
		t.Errorf("B.Children = %v, want [C]", got)
	}
}

// TestBatchEmptyUpstreamsMakesRootWhileOldReportKeepsUpstreams verifies the
// root boundary: replacing a dataset's upstreams with an empty list makes it a
// root in both the current graph and this batch's report (emitted as []),
// while a report returned earlier still carries the non-empty list.
func TestBatchEmptyUpstreamsMakesRootWhileOldReportKeepsUpstreams(t *testing.T) {
	graph := buildGraph(t, P("A"), P("D"), P("B", "A"), P("C", "B"))

	oldPlan := planOf(change("B", "D"))
	old, err := PreviewBatch(graph, oldPlan)
	if err != nil {
		t.Fatalf("old PreviewBatch unexpected error: %v", err)
	}
	if got := mustReportDataset(t, old, "B").Upstreams; !reflect.DeepEqual(got, []string{"D"}) {
		t.Fatalf("old report B.upstreams = %v, want [D]", got)
	}
	oldJSON := mustMarshalReport(t, old)

	// Now clear B's upstreams through the same API. B becomes a root but keeps
	// its downstream C.
	emptyPlan := planOf(change("B"))
	report, err := ApplyBatch(graph, emptyPlan)
	if err != nil {
		t.Fatalf("ApplyBatch empty-upstreams unexpected error: %v", err)
	}
	if got := graph["B"].Parents; len(got) != 0 {
		t.Errorf("B.Parents = %v, want empty (root)", got)
	}
	if got := graph["B"].Children; !reflect.DeepEqual(got, []string{"C"}) {
		t.Errorf("B.Children = %v, want [C] (downstream kept when becoming a root)", got)
	}
	if got := graph["A"].Children; len(got) != 0 {
		t.Errorf("A.Children = %v, want empty", got)
	}
	if roots := Roots(graph); !reflect.DeepEqual(roots, []string{"A", "B", "D"}) {
		t.Errorf("Roots = %v, want [A B D]", roots)
	}

	// The new report shows B as a root with a non-nil empty list (JSON []).
	got := mustReportDataset(t, report, "B").Upstreams
	if got == nil || len(got) != 0 {
		t.Errorf("new report B.upstreams = %v, want non-nil empty list", got)
	}
	if b := mustMarshalReport(t, report); !containsEmptyUpstreamsFor(b, "B") {
		t.Errorf("new report JSON does not show B with \"upstreams\":[]:\n%s", b)
	}
	if got := mustReportDataset(t, report, "C").Upstreams; !reflect.DeepEqual(got, []string{"B"}) {
		t.Errorf("new report C.upstreams = %v, want [B]", got)
	}

	// The earlier report is frozen on the non-empty upstream, byte for byte.
	if got := mustReportDataset(t, old, "B").Upstreams; !reflect.DeepEqual(got, []string{"D"}) {
		t.Errorf("old report B.upstreams = %v, want frozen [D]", got)
	}
	if got := mustMarshalReport(t, old); got != oldJSON {
		t.Errorf("old report JSON drifted after B became a root:\n got %s\nwant %s", got, oldJSON)
	}
}

// containsEmptyUpstreamsFor reports whether the report JSON renders name with
// an empty upstreams list (struct field order is name then upstreams).
func containsEmptyUpstreamsFor(reportJSON, name string) bool {
	quoted, err := json.Marshal(name)
	if err != nil {
		return false
	}
	return strings.Contains(reportJSON, `"name":`+string(quoted)+`,"upstreams":[]`)
}
