package chainledger

import (
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
)

// Regression coverage for a batch that deletes a SHARED intermediate dataset
// while the retained downstream is rewired onto surviving upstreams within the
// same plan.
//
// The fixture keeps two converging branches:
//
//	R -> M -> B -> H -> T
//	     \-> C -/     (H also depends on C, so the branches meet at H)
//	S -> (root, initially unrelated)
//	U -> V            (an isolated component that must never be touched)
//
// The batch deletes M and B, repoints C wholesale at R, and repoints H
// wholesale at C and S:
//
//	R -> C -> H -> T
//	S ----/
//
// It must succeed: H and T are retained even though both of H's old upstreams
// (B, and the path through M) disappear, because the plan reconnects H inside
// the same batch. These tests pin the report's three-way classification
// (deleted / directly adjusted / indirectly affected), guard the deleted-edge
// list against missing relations and the affected list against double-counting
// the converging downstream, and guard the atomicity of a rejected batch.

// convergenceBatchGraph builds R,S roots; M depends on R; B and C both depend
// on M; H depends on B and C; T depends on H; plus the isolated U -> V pair.
func convergenceBatchGraph(t *testing.T) map[string]*Lineage {
	t.Helper()
	return buildGraph(t,
		P("R"),
		P("S"),
		P("M", "R"),
		P("B", "M"),
		P("C", "M"),
		P("H", "B", "C"),
		P("T", "H"),
		P("U"),
		P("V", "U"),
	)
}

// convergenceBatchPlan is the successful plan: remove M and B, repoint C at R,
// repoint H at C and S.
func convergenceBatchPlan() Plan {
	return Plan{
		Changes: []PlanChange{
			change("C", "R"),
			change("H", "C", "S"),
		},
		Removals: []string{"M", "B"},
	}
}

// wantConvergenceFinal is the exact expected final graph, sorted by name with
// sorted upstream lists.
var wantConvergenceFinal = []GraphDataset{
	{Name: "C", Upstreams: []string{"R"}},
	{Name: "H", Upstreams: []string{"C", "S"}},
	{Name: "R", Upstreams: []string{}},
	{Name: "S", Upstreams: []string{}},
	{Name: "T", Upstreams: []string{"H"}},
	{Name: "U", Upstreams: []string{}},
	{Name: "V", Upstreams: []string{"U"}},
}

// TestBatchDeleteSharedIntermediateSucceeds is the headline scenario: the
// shared intermediate M and one converging branch B are deleted, while the
// retained downstream H is rewired onto C and S inside the same batch. H and T
// survive; the isolated U/V component is untouched.
func TestBatchDeleteSharedIntermediateSucceeds(t *testing.T) {
	graph := convergenceBatchGraph(t)
	plan := convergenceBatchPlan()

	report, err := PreviewBatch(graph, plan)
	if err != nil {
		t.Fatalf("PreviewBatch unexpected error: %v", err)
	}

	// Deleted datasets are exactly M and B.
	if got, want := report.RemovedDatasets, []string{"B", "M"}; !reflect.DeepEqual(got, want) {
		t.Errorf("RemovedDatasets = %v, want %v", got, want)
	}
	// Directly adjusted datasets are exactly C and H; T must not join them.
	if got, want := report.ChangedDatasets, []string{"C", "H"}; !reflect.DeepEqual(got, want) {
		t.Errorf("ChangedDatasets = %v, want %v", got, want)
	}
	// The plan registers no dataset.
	if got := report.NewDatasets; !reflect.DeepEqual(got, []string{}) {
		t.Errorf("NewDatasets = %v, want []", got)
	}
	// T is the sole indirectly affected downstream, once; H (directly
	// adjusted) and every root must be absent from this list.
	if got, want := report.AffectedDownstreams, []string{"T"}; !reflect.DeepEqual(got, want) {
		t.Errorf("AffectedDownstreams = %v, want %v (converging T counted once, adjusted H excluded)", got, want)
	}

	// Direct relations removed together with the deleted nodes and repoints:
	// R->M, M->B, M->C, B->H. The edge C->H survives and must not appear here.
	if got, want := report.RemovedRelations, []Relation{
		{Upstream: "B", Downstream: "H"},
		{Upstream: "M", Downstream: "B"},
		{Upstream: "M", Downstream: "C"},
		{Upstream: "R", Downstream: "M"},
	}; !reflect.DeepEqual(got, want) {
		t.Errorf("RemovedRelations = %v, want %v", got, want)
	}
	// Only genuinely new direct edges are R->C and S->H. Reachable paths that
	// merely still exist (C->H, H->T) must not be invented as added edges.
	if got, want := report.AddedRelations, []Relation{
		{Upstream: "R", Downstream: "C"},
		{Upstream: "S", Downstream: "H"},
	}; !reflect.DeepEqual(got, want) {
		t.Errorf("AddedRelations = %v, want %v", got, want)
	}

	// The final graph is exactly the rewired one: M and B gone, no retained
	// dataset references them, U/V and their edge untouched.
	if got := report.FinalGraph.Datasets; !reflect.DeepEqual(got, wantConvergenceFinal) {
		t.Errorf("FinalGraph.Datasets = %v, want %v", got, wantConvergenceFinal)
	}
	for _, ds := range report.FinalGraph.Datasets {
		for _, upstream := range ds.Upstreams {
			if upstream == "M" || upstream == "B" {
				t.Errorf("final dataset %q still references deleted upstream %q", ds.Name, upstream)
			}
		}
	}
}

// TestBatchDeleteSharedIntermediatePreviewIsReadOnly: preview reports the
// result but leaves every node and bidirectional (parent/child) record of the
// original graph exactly as submitted.
func TestBatchDeleteSharedIntermediatePreviewIsReadOnly(t *testing.T) {
	graph := convergenceBatchGraph(t)
	before := dump(graph)

	if _, err := PreviewBatch(graph, convergenceBatchPlan()); err != nil {
		t.Fatalf("PreviewBatch unexpected error: %v", err)
	}
	if got := dump(graph); got != before {
		t.Fatalf("preview mutated the graph:\n got %s\nwant %s", got, before)
	}
	// Pin the bidirectional state explicitly: M and B are still present, and
	// the children derived from the original edges are intact.
	for name, parents := range map[string][]string{
		"M": {"R"}, "B": {"M"}, "C": {"M"}, "H": {"B", "C"}, "T": {"H"}, "V": {"U"},
	} {
		if got := graph[name].Parents; !reflect.DeepEqual(got, parents) {
			t.Errorf("after preview %s.Parents = %v, want %v", name, got, parents)
		}
	}
	if got := graph["M"].Children; !reflect.DeepEqual(got, []string{"B", "C"}) {
		t.Errorf("after preview M.Children = %v, want [B C]", got)
	}
	if got := graph["B"].Children; !reflect.DeepEqual(got, []string{"H"}) {
		t.Errorf("after preview B.Children = %v, want [H]", got)
	}
}

// TestBatchDeleteSharedIntermediateApplyMatchesPreview: apply commits exactly
// the final graph the preview reported, with an identical report; parents and
// the derived children are both rebuilt.
func TestBatchDeleteSharedIntermediateApplyMatchesPreview(t *testing.T) {
	graph := convergenceBatchGraph(t)

	preview, err := PreviewBatch(graph, convergenceBatchPlan())
	if err != nil {
		t.Fatalf("PreviewBatch unexpected error: %v", err)
	}
	applied, err := ApplyBatch(graph, convergenceBatchPlan())
	if err != nil {
		t.Fatalf("ApplyBatch unexpected error: %v", err)
	}
	if !reflect.DeepEqual(preview, applied) {
		t.Fatalf("preview and apply reports differ:\n preview=%#v\n applied=%#v", preview, applied)
	}

	// M and B are gone, with no dangling references.
	if _, ok := graph["M"]; ok {
		t.Errorf("M still in graph after apply")
	}
	if _, ok := graph["B"]; ok {
		t.Errorf("B still in graph after apply")
	}
	for name, entry := range graph {
		for _, parent := range entry.Parents {
			if parent == "M" || parent == "B" {
				t.Errorf("applied dataset %q still references deleted %q", name, parent)
			}
		}
	}
	// Parents of the retained nodes match the reported final graph, and the
	// derived children were rebuilt from the rewired edges.
	wantParents := map[string][]string{
		"R": nil, "S": nil, "C": {"R"}, "H": {"C", "S"}, "T": {"H"}, "U": nil, "V": {"U"},
	}
	for name, parents := range wantParents {
		if got := graph[name].Parents; !reflect.DeepEqual(got, parents) {
			t.Errorf("after apply %s.Parents = %v, want %v", name, got, parents)
		}
	}
	for name, children := range map[string][]string{
		"R": {"C"}, "S": {"H"}, "C": {"H"}, "H": {"T"}, "T": nil, "U": {"V"}, "V": nil,
	} {
		if got := graph[name].Children; !reflect.DeepEqual(got, children) {
			t.Errorf("after apply %s.Children = %v, want %v", name, got, children)
		}
	}

	// The applied graph exports exactly the final graph carried in the report.
	exported, err := MarshalGraphFile(graph)
	if err != nil {
		t.Fatalf("MarshalGraphFile unexpected error: %v", err)
	}
	finalBytes, err := json.MarshalIndent(preview.FinalGraph, "", "  ")
	if err != nil {
		t.Fatalf("marshal reported final graph: %v", err)
	}
	if string(exported) != string(finalBytes) {
		t.Fatalf("applied graph differs from the preview final graph:\n applied:\n%s\n preview:\n%s", exported, finalBytes)
	}
	// The exported graph must round-trip through the file reader unchanged.
	parsed, err := UnmarshalGraphFile(exported)
	if err != nil {
		t.Fatalf("UnmarshalGraphFile on applied graph: %v", err)
	}
	if !reflect.DeepEqual(parsed, graph) {
		t.Fatalf("applied graph does not round-trip:\n parsed %#v\n graph  %#v", parsed, graph)
	}
}

// TestBatchDeleteSharedIntermediateRejectsMissingDownstreamAdjustment is the
// refusal counterpart: the plan still deletes M and B and repoints C, but
// forgets to repoint H. H keeps a direct reference to deleted B, so both
// preview and apply must reject the whole batch, naming H (the referrer) and B
// (the referenced name), and no success report may be produced.
func TestBatchDeleteSharedIntermediateRejectsMissingDownstreamAdjustment(t *testing.T) {
	plan := Plan{
		Changes:  []PlanChange{change("C", "R")},
		Removals: []string{"M", "B"},
	}

	for _, mode := range []string{"preview", "apply"} {
		t.Run(mode, func(t *testing.T) {
			graph := convergenceBatchGraph(t)
			before := dump(graph)

			var report *BatchReport
			var err error
			if mode == "preview" {
				report, err = PreviewBatch(graph, plan)
			} else {
				report, err = ApplyBatch(graph, plan)
			}
			if err == nil {
				t.Fatalf("%s succeeded on a dangling reference, report = %+v", mode, report)
			}
			if !errors.Is(err, ErrNotFound) {
				t.Errorf("err = %v, want ErrNotFound", err)
			}
			if !strings.Contains(err.Error(), "H") || !strings.Contains(err.Error(), "B") {
				t.Errorf("error %q must name referrer H and deleted upstream B", err)
			}
			if report != nil {
				t.Errorf("rejected %s returned a success report: %+v", mode, report)
			}

			// Atomicity: every dataset, every direct-upstream list, and every
			// derived downstream list must remain as submitted. In particular
			// C's otherwise-legal repoint to R must not have taken effect.
			if got := dump(graph); got != before {
				t.Fatalf("rejected %s mutated the graph:\n got  %s\nwant %s", mode, got, before)
			}
			if _, ok := graph["M"]; !ok {
				t.Errorf("M disappeared on rejected %s", mode)
			}
			if _, ok := graph["B"]; !ok {
				t.Errorf("B disappeared on rejected %s", mode)
			}
			if got := graph["C"].Parents; !reflect.DeepEqual(got, []string{"M"}) {
				t.Errorf("C.Parents = %v, want [M] (legal repoint must not apply early)", got)
			}
			if got := graph["H"].Parents; !reflect.DeepEqual(got, []string{"B", "C"}) {
				t.Errorf("H.Parents = %v, want [B C]", got)
			}
			if got := graph["M"].Children; !reflect.DeepEqual(got, []string{"B", "C"}) {
				t.Errorf("M.Children = %v, want [B C]", got)
			}
			if got := graph["R"].Children; !reflect.DeepEqual(got, []string{"M"}) {
				t.Errorf("R.Children = %v, want [M] (R->C must not appear early)", got)
			}
			if got := graph["S"].Children; len(got) != 0 {
				t.Errorf("S.Children = %v, want empty", got)
			}
			// The isolated component is completely unaffected as well.
			if got := graph["V"].Parents; !reflect.DeepEqual(got, []string{"U"}) {
				t.Errorf("V.Parents = %v, want [U]", got)
			}
		})
	}
}

// TestBatchDeleteSharedIntermediateDeterministic: reordering graph records,
// plan records, removal entries, and upstream names — or repeating one of H's
// upstreams — carries no semantics, so the applied graph and the success report
// must be byte-identical in every variant.
func TestBatchDeleteSharedIntermediateDeterministic(t *testing.T) {
	canonicalGraph := func() map[string]*Lineage { return convergenceBatchGraph(t) }
	canonicalPlan := convergenceBatchPlan()

	variants := []struct {
		name  string
		graph func() map[string]*Lineage
		plan  Plan
	}{
		{
			name:  "canonical",
			graph: canonicalGraph,
			plan:  canonicalPlan,
		},
		{
			name: "shuffled graph records",
			graph: func() map[string]*Lineage {
				// Same graph, records in a different on-disk order (the file
				// reader accepts any order and normalizes).
				graph, err := UnmarshalGraphFile([]byte(`{"datasets":[
					{"name":"V","upstreams":["U"]},
					{"name":"T","upstreams":["H"]},
					{"name":"U","upstreams":[]},
					{"name":"H","upstreams":["C","B"]},
					{"name":"C","upstreams":["M"]},
					{"name":"B","upstreams":["M"]},
					{"name":"S","upstreams":[]},
					{"name":"M","upstreams":["R"]},
					{"name":"R","upstreams":[]}
				]}`))
				if err != nil {
					t.Fatalf("UnmarshalGraphFile unexpected error: %v", err)
				}
				return graph
			},
			plan: canonicalPlan,
		},
		{
			name:  "shuffled change and removal order",
			graph: canonicalGraph,
			plan: Plan{
				Changes:  []PlanChange{change("H", "S", "C"), change("C", "R")},
				Removals: []string{"B", "M"},
			},
		},
		{
			name:  "shuffled and duplicated upstreams",
			graph: canonicalGraph,
			plan: Plan{
				Changes: []PlanChange{
					change("C", "R", "R"),
					change("H", "S", "C", "S", "C"),
				},
				Removals: []string{"M", "B"},
			},
		},
	}

	var wantGraph, wantReport []byte
	for i, tc := range variants {
		graph := tc.graph()
		report, err := ApplyBatch(graph, tc.plan)
		if err != nil {
			t.Fatalf("%s: ApplyBatch unexpected error: %v", tc.name, err)
		}
		gotGraph, err := MarshalGraphFile(graph)
		if err != nil {
			t.Fatalf("%s: MarshalGraphFile unexpected error: %v", tc.name, err)
		}
		gotReport, err := json.Marshal(report)
		if err != nil {
			t.Fatalf("%s: marshal report: %v", tc.name, err)
		}
		if i == 0 {
			wantGraph, wantReport = gotGraph, gotReport
			continue
		}
		if string(gotGraph) != string(wantGraph) {
			t.Fatalf("%s: graph bytes differ:\n--- got ---\n%s\n--- want ---\n%s", tc.name, gotGraph, wantGraph)
		}
		if string(gotReport) != string(wantReport) {
			t.Fatalf("%s: report bytes differ:\n--- got ---\n%s\n--- want ---\n%s", tc.name, gotReport, wantReport)
		}
	}
}
