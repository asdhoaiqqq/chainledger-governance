package chainledger

import (
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
)

// removalPlan builds a Plan carrying both changes and removals.
func removalPlan(removals []string, changes ...PlanChange) Plan {
	return Plan{Changes: changes, Removals: removals}
}

// TestRemovalDeletesLeafDataset verifies the simplest removal: a leaf dataset
// disappears from the final graph together with its incident relation, and is
// reported as removed rather than changed.
func TestRemovalDeletesLeafDataset(t *testing.T) {
	// A <- B <- C. Delete C.
	graph := buildGraph(t, P("A"), P("B", "A"), P("C", "B"))
	plan := removalPlan([]string{"C"})

	before := dump(graph)
	report, err := PreviewBatch(graph, plan)
	if err != nil {
		t.Fatalf("PreviewBatch unexpected error: %v", err)
	}
	if got := dump(graph); got != before {
		t.Fatalf("preview mutated graph:\n got %s\nwant %s", got, before)
	}

	if !reflect.DeepEqual(report.RemovedDatasets, []string{"C"}) {
		t.Errorf("RemovedDatasets = %v, want [C]", report.RemovedDatasets)
	}
	if len(report.NewDatasets) != 0 || len(report.ChangedDatasets) != 0 {
		t.Errorf("removal leaked into new/changed lists: new=%v changed=%v", report.NewDatasets, report.ChangedDatasets)
	}
	wantRemoved := []Relation{{Upstream: "B", Downstream: "C"}}
	if !reflect.DeepEqual(report.RemovedRelations, wantRemoved) {
		t.Errorf("RemovedRelations = %v, want %v", report.RemovedRelations, wantRemoved)
	}
	if len(report.AddedRelations) != 0 {
		t.Errorf("AddedRelations = %v, want empty", report.AddedRelations)
	}
	if len(report.AffectedDownstreams) != 0 {
		t.Errorf("AffectedDownstreams = %v, want empty (removed leaf has no retained dependents)", report.AffectedDownstreams)
	}
	if names := datasetNames(report.FinalGraph); !reflect.DeepEqual(names, []string{"A", "B"}) {
		t.Errorf("final graph datasets = %v, want [A B]", names)
	}

	applied, err := ApplyBatch(graph, plan)
	if err != nil {
		t.Fatalf("ApplyBatch unexpected error: %v", err)
	}
	if !reflect.DeepEqual(applied, report) {
		t.Fatalf("apply report differs from preview:\n apply=%#v\n preview=%#v", applied, report)
	}
	if _, ok := graph["C"]; ok {
		t.Errorf("dataset C still present in graph after apply")
	}
	if got := graph["B"].Children; len(got) != 0 {
		t.Errorf("B.Children = %v, want empty after C was removed", got)
	}
}

// TestRemovalWithDownstreamRepointed is the headline scenario from the spec:
// B depends on A, C depends on B. Removing A succeeds when B is repointed in
// the same batch to newly registered D; unadjusted C is still an affected
// downstream.
func TestRemovalWithDownstreamRepointed(t *testing.T) {
	graph := buildGraph(t, P("A"), P("B", "A"), P("C", "B"))
	plan := removalPlan([]string{"A"}, change("D"), change("B", "D"))

	report, err := PreviewBatch(graph, plan)
	if err != nil {
		t.Fatalf("PreviewBatch unexpected error: %v", err)
	}
	if !reflect.DeepEqual(report.RemovedDatasets, []string{"A"}) {
		t.Errorf("RemovedDatasets = %v, want [A]", report.RemovedDatasets)
	}
	if !reflect.DeepEqual(report.NewDatasets, []string{"D"}) {
		t.Errorf("NewDatasets = %v, want [D]", report.NewDatasets)
	}
	if !reflect.DeepEqual(report.ChangedDatasets, []string{"B"}) {
		t.Errorf("ChangedDatasets = %v, want [B]", report.ChangedDatasets)
	}
	wantRemoved := []Relation{{Upstream: "A", Downstream: "B"}}
	if !reflect.DeepEqual(report.RemovedRelations, wantRemoved) {
		t.Errorf("RemovedRelations = %v, want %v", report.RemovedRelations, wantRemoved)
	}
	wantAdded := []Relation{{Upstream: "D", Downstream: "B"}}
	if !reflect.DeepEqual(report.AddedRelations, wantAdded) {
		t.Errorf("AddedRelations = %v, want %v", report.AddedRelations, wantAdded)
	}
	// C was not adjusted: it stays on B and is affected because its ancestor
	// chain changed (A deleted, B repointed).
	if !reflect.DeepEqual(report.AffectedDownstreams, []string{"C"}) {
		t.Errorf("AffectedDownstreams = %v, want [C]", report.AffectedDownstreams)
	}

	if _, err := ApplyBatch(graph, plan); err != nil {
		t.Fatalf("ApplyBatch unexpected error: %v", err)
	}
	if _, ok := graph["A"]; ok {
		t.Errorf("dataset A still present after apply")
	}
	if got := graph["B"].Parents; !reflect.DeepEqual(got, []string{"D"}) {
		t.Errorf("B.Parents = %v, want [D]", got)
	}
	if got := graph["C"].Parents; !reflect.DeepEqual(got, []string{"B"}) {
		t.Errorf("C.Parents = %v, want [B] (unadjusted downstream untouched)", got)
	}
	if roots := Roots(graph); !reflect.DeepEqual(roots, []string{"D"}) {
		t.Errorf("Roots = %v, want [D]", roots)
	}
}

// TestRemovalWithoutRepointingRejected verifies that deleting an upstream
// without adjusting the retained downstream rejects the whole batch, naming
// both the referrer and the referenced name; the dataset is not silently
// turned into a root.
func TestRemovalWithoutRepointingRejected(t *testing.T) {
	// A <- B. Deleting A alone must fail: B would dangle.
	graph := buildGraph(t, P("A"), P("B", "A"))
	plan := removalPlan([]string{"A"})

	_, err := PreviewBatch(graph, plan)
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("err = %v, want ErrNotFound", err)
	}
	for _, want := range []string{"A", "B"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q must name both referrer B and referenced A; missing %q", err, want)
		}
	}

	snapshot := dump(graph)
	if _, err := ApplyBatch(graph, plan); !errors.Is(err, ErrNotFound) {
		t.Fatalf("apply err = %v, want ErrNotFound", err)
	}
	if got := dump(graph); got != snapshot {
		t.Fatalf("rejected apply mutated graph:\n got %s\nwant %s", got, snapshot)
	}
}

// TestRemovalAllowsDeletingDownstreamsToo verifies the other legal way to
// remove an upstream: list the dependent datasets in removals as well.
func TestRemovalAllowsDeletingDownstreamsToo(t *testing.T) {
	// A <- B <- C. Removing B and C together is legal; A survives.
	graph := buildGraph(t, P("A"), P("B", "A"), P("C", "B"))
	plan := removalPlan([]string{"B", "C"})

	report, err := PreviewBatch(graph, plan)
	if err != nil {
		t.Fatalf("PreviewBatch unexpected error: %v", err)
	}
	if !reflect.DeepEqual(report.RemovedDatasets, []string{"B", "C"}) {
		t.Errorf("RemovedDatasets = %v, want [B C]", report.RemovedDatasets)
	}
	wantRemoved := []Relation{
		{Upstream: "A", Downstream: "B"},
		{Upstream: "B", Downstream: "C"},
	}
	if !reflect.DeepEqual(report.RemovedRelations, wantRemoved) {
		t.Errorf("RemovedRelations = %v, want %v", report.RemovedRelations, wantRemoved)
	}
	// Both removed nodes are seeds; nothing retained lies downstream.
	if len(report.AffectedDownstreams) != 0 {
		t.Errorf("AffectedDownstreams = %v, want empty", report.AffectedDownstreams)
	}
	if names := datasetNames(report.FinalGraph); !reflect.DeepEqual(names, []string{"A"}) {
		t.Errorf("final graph datasets = %v, want [A]", names)
	}
}

// TestRemovalOfEveryDatasetYieldsEmptyGraph verifies that deleting the full
// graph succeeds and yields a legal empty graph, and that the empty graph can
// be used again afterwards.
func TestRemovalOfEveryDatasetYieldsEmptyGraph(t *testing.T) {
	graph := buildGraph(t, P("A"), P("B", "A"), P("C", "B"))
	plan := removalPlan([]string{"C", "A", "B"}) // shuffled on purpose

	report, err := ApplyBatch(graph, plan)
	if err != nil {
		t.Fatalf("ApplyBatch unexpected error: %v", err)
	}
	if !reflect.DeepEqual(report.RemovedDatasets, []string{"A", "B", "C"}) {
		t.Errorf("RemovedDatasets = %v, want sorted [A B C]", report.RemovedDatasets)
	}
	if len(report.FinalGraph.Datasets) != 0 {
		t.Errorf("final graph = %+v, want empty datasets", report.FinalGraph)
	}
	if len(graph) != 0 {
		t.Errorf("in-memory graph still has %d datasets, want 0", len(graph))
	}
	data, err := MarshalGraphFile(graph)
	if err != nil {
		t.Fatalf("MarshalGraphFile unexpected error: %v", err)
	}
	if !strings.Contains(string(data), `"datasets": []`) {
		t.Errorf("empty graph JSON = %s, want datasets: []", data)
	}

	// The empty graph accepts new registrations through another batch.
	next := planOf(change("X"))
	if _, err := ApplyBatch(graph, next); err != nil {
		t.Fatalf("registering after full removal unexpected error: %v", err)
	}
	if roots := Roots(graph); !reflect.DeepEqual(roots, []string{"X"}) {
		t.Errorf("Roots = %v, want [X]", roots)
	}
}

// TestRemovalOfMissingNameIsNoOp verifies that removing a name that does not
// exist is not an error and not an actual removal, so re-applying a successful
// removal plan still succeeds with empty change and impact lists.
func TestRemovalOfMissingNameIsNoOp(t *testing.T) {
	graph := buildGraph(t, P("A"), P("B", "A"))

	report, err := PreviewBatch(graph, removalPlan([]string{"ghost"}))
	if err != nil {
		t.Fatalf("PreviewBatch unexpected error: %v", err)
	}
	if len(report.RemovedDatasets) != 0 {
		t.Errorf("RemovedDatasets = %v, want empty for missing name", report.RemovedDatasets)
	}
	if len(report.RemovedRelations) != 0 || len(report.AffectedDownstreams) != 0 ||
		len(report.NewDatasets) != 0 || len(report.ChangedDatasets) != 0 || len(report.AddedRelations) != 0 {
		t.Errorf("removing a missing name produced a non-empty diff: %+v", report)
	}

	// First apply actually deletes A (a leaf here after adjusting nothing:
	// build A<-B, so delete B first), then re-apply the same plan.
	plan := removalPlan([]string{"A", "B"})
	first, err := ApplyBatch(graph, plan)
	if err != nil {
		t.Fatalf("first ApplyBatch unexpected error: %v", err)
	}
	if !reflect.DeepEqual(first.RemovedDatasets, []string{"A", "B"}) {
		t.Fatalf("first RemovedDatasets = %v, want [A B]", first.RemovedDatasets)
	}
	second, err := ApplyBatch(graph, plan)
	if err != nil {
		t.Fatalf("second ApplyBatch unexpected error: %v", err)
	}
	if len(second.RemovedDatasets) != 0 || len(second.RemovedRelations) != 0 ||
		len(second.AffectedDownstreams) != 0 {
		t.Errorf("re-applied removal plan produced non-empty results: %+v", second)
	}
}

// TestRemovalRejectsMalformedRemovalList verifies empty names, duplicates
// within removals, and overlap with changes all reject the whole batch.
func TestRemovalRejectsMalformedRemovalList(t *testing.T) {
	graph := buildGraph(t, P("A"), P("B", "A"))

	t.Run("empty removal name", func(t *testing.T) {
		_, err := PreviewBatch(graph, removalPlan([]string{""}))
		if !errors.Is(err, ErrInvalidArgument) {
			t.Fatalf("err = %v, want ErrInvalidArgument", err)
		}
	})

	t.Run("duplicate removal name", func(t *testing.T) {
		_, err := PreviewBatch(graph, removalPlan([]string{"A", "A"}))
		if !errors.Is(err, ErrInvalidArgument) {
			t.Fatalf("err = %v, want ErrInvalidArgument", err)
		}
		if !strings.Contains(err.Error(), "A") {
			t.Errorf("error %q does not name dataset A", err)
		}
	})

	t.Run("name in both changes and removals", func(t *testing.T) {
		plan := removalPlan([]string{"B"}, change("B"))
		_, err := PreviewBatch(graph, plan)
		if !errors.Is(err, ErrInvalidArgument) {
			t.Fatalf("err = %v, want ErrInvalidArgument", err)
		}
		if !strings.Contains(err.Error(), "B") {
			t.Errorf("error %q does not name dataset B", err)
		}
	})

	t.Run("overlap rejected even when the name does not exist", func(t *testing.T) {
		plan := removalPlan([]string{"ghost"}, change("ghost"))
		_, err := PreviewBatch(graph, plan)
		if !errors.Is(err, ErrInvalidArgument) {
			t.Fatalf("err = %v, want ErrInvalidArgument", err)
		}
	})

	t.Run("rejection leaves graph unchanged", func(t *testing.T) {
		snapshot := dump(graph)
		if _, err := ApplyBatch(graph, removalPlan([]string{"A", "A"})); !errors.Is(err, ErrInvalidArgument) {
			t.Fatalf("err = %v, want ErrInvalidArgument", err)
		}
		if got := dump(graph); got != snapshot {
			t.Fatalf("graph mutated on rejection:\n got %s\nwant %s", got, snapshot)
		}
	})
}

// TestRemovalCannotPaperOverCorruptOriginalGraph verifies that deleting the
// very nodes involved in pre-existing corruption does not let the batch
// through: the original graph is validated before the plan is applied.
func TestRemovalCannotPaperOverCorruptOriginalGraph(t *testing.T) {
	t.Run("cycle broken by deletion still rejected", func(t *testing.T) {
		graph := map[string]*Lineage{
			"A": {Dataset: "A", Parents: []string{"B"}},
			"B": {Dataset: "B", Parents: []string{"A"}},
		}
		_, err := PreviewBatch(graph, removalPlan([]string{"A"}))
		if !errors.Is(err, ErrCycle) {
			t.Fatalf("err = %v, want ErrCycle", err)
		}
	})

	t.Run("dangling upstream fixed only by deletion still rejected", func(t *testing.T) {
		// A references a missing upstream "ghost". Deleting A must not
		// launder the corrupt input graph into validity.
		graph := map[string]*Lineage{
			"A": {Dataset: "A", Parents: []string{"ghost"}},
		}
		_, err := PreviewBatch(graph, removalPlan([]string{"A"}))
		if !errors.Is(err, ErrNotFound) {
			t.Fatalf("err = %v, want ErrNotFound", err)
		}
	})
}

// TestRemovalAffectedDownstreams checks impact propagation starting from
// removed seeds across both the pre- and post-change graphs.
func TestRemovalAffectedDownstreams(t *testing.T) {
	t.Run("removed ancestor with retained chain", func(t *testing.T) {
		// A <- B <- C <- D. Delete A and repoint B to root. Seeds are A
		// (removed) and B (changed); C and D are retained and affected.
		graph := buildGraph(t, P("A"), P("B", "A"), P("C", "B"), P("D", "C"))
		plan := removalPlan([]string{"A"}, change("B"))
		report, err := PreviewBatch(graph, plan)
		if err != nil {
			t.Fatalf("PreviewBatch unexpected error: %v", err)
		}
		if !reflect.DeepEqual(report.AffectedDownstreams, []string{"C", "D"}) {
			t.Errorf("AffectedDownstreams = %v, want [C D]", report.AffectedDownstreams)
		}
	})

	t.Run("dependent only in original graph still counts", func(t *testing.T) {
		// A <- B <- C. Delete B and repoint C to root in the same batch. B is
		// a removed seed; C is a changed seed. In the final graph B is gone,
		// but the impact walk over the original graph must not surface
		// anything extra — the point is the walk considers both graphs and
		// still excludes every seed. Here the only retained downstream set is
		// empty, so assert that precisely.
		graph := buildGraph(t, P("A"), P("B", "A"), P("C", "B"))
		plan := removalPlan([]string{"B"}, change("C"))
		report, err := PreviewBatch(graph, plan)
		if err != nil {
			t.Fatalf("PreviewBatch unexpected error: %v", err)
		}
		if len(report.AffectedDownstreams) != 0 {
			t.Errorf("AffectedDownstreams = %v, want empty (B removed, C changed, A upstream)", report.AffectedDownstreams)
		}
	})
}

// TestRemovalPreservesNameCasingAndSpaces verifies names are matched
// case- and space-sensitively, including in removals.
func TestRemovalPreservesNameCasingAndSpaces(t *testing.T) {
	graph := buildGraph(t, P(" Raw-Blocks "), P("RAW-BLOCKS", " Raw-Blocks "))
	// A differently-cased/spelled removal name does not match.
	report, err := PreviewBatch(graph, removalPlan([]string{"raw-blocks"}))
	if err != nil {
		t.Fatalf("PreviewBatch unexpected error: %v", err)
	}
	if len(report.RemovedDatasets) != 0 {
		t.Errorf("RemovedDatasets = %v, want empty for case-mismatched name", report.RemovedDatasets)
	}

	// Deleting the real root requires repointing its retained downstream;
	// exact spacing must round-trip.
	plan := removalPlan([]string{" Raw-Blocks "}, change("RAW-BLOCKS"))
	if _, err := ApplyBatch(graph, plan); err != nil {
		t.Fatalf("ApplyBatch unexpected error: %v", err)
	}
	if _, ok := graph[" Raw-Blocks "]; ok {
		t.Errorf("spaced name was not deleted (match was folded?)")
	}
	if got := graph["RAW-BLOCKS"].Parents; len(got) != 0 {
		t.Errorf("RAW-BLOCKS.Parents = %v, want root", got)
	}
}

// TestRemovalDeterministicAcrossOrder verifies that shuffling the removals
// list (and the change record order) cannot change the successful output
// bytes.
func TestRemovalDeterministicAcrossOrder(t *testing.T) {
	build := func() map[string]*Lineage {
		return buildGraph(t, P("A"), P("B", "A"), P("C", "B"))
	}
	plan1 := removalPlan([]string{"A"}, change("D"), change("B", "D"))
	plan2 := removalPlan([]string{"A"}, change("B", "D"), change("D"))

	g1, g2 := build(), build()
	r1, err := ApplyBatch(g1, plan1)
	if err != nil {
		t.Fatalf("ApplyBatch plan1 unexpected error: %v", err)
	}
	r2, err := ApplyBatch(g2, plan2)
	if err != nil {
		t.Fatalf("ApplyBatch plan2 unexpected error: %v", err)
	}

	b1, err := MarshalGraphFile(g1)
	if err != nil {
		t.Fatalf("MarshalGraphFile g1 unexpected error: %v", err)
	}
	b2, err := MarshalGraphFile(g2)
	if err != nil {
		t.Fatalf("MarshalGraphFile g2 unexpected error: %v", err)
	}
	if string(b1) != string(b2) {
		t.Fatalf("graph bytes differ:\n%s\nvs\n%s", b1, b2)
	}
	rb1, _ := json.Marshal(r1)
	rb2, _ := json.Marshal(r2)
	if string(rb1) != string(rb2) {
		t.Fatalf("report bytes differ:\n%s\nvs\n%s", rb1, rb2)
	}
}

// TestRemovalReportJSONShape verifies the report keeps its existing fields and
// adds removedDatasets in a fixed position, with empty lists as [].
func TestRemovalReportJSONShape(t *testing.T) {
	graph := map[string]*Lineage{}
	report, err := PreviewBatch(graph, Plan{})
	if err != nil {
		t.Fatalf("PreviewBatch unexpected error: %v", err)
	}
	data, err := json.Marshal(report)
	if err != nil {
		t.Fatalf("json.Marshal unexpected error: %v", err)
	}
	text := string(data)
	for _, key := range []string{
		`"finalGraph"`, `"newDatasets":[]`, `"changedDatasets":[]`,
		`"removedDatasets":[]`, `"addedRelations":[]`, `"removedRelations":[]`,
		`"affectedDownstreams":[]`,
	} {
		if !strings.Contains(text, key) {
			t.Errorf("report JSON %s missing %s", text, key)
		}
	}
	// removedDatasets must follow changedDatasets.
	cIdx := strings.Index(text, `"changedDatasets"`)
	rIdx := strings.Index(text, `"removedDatasets"`)
	if !(cIdx >= 0 && rIdx > cIdx) {
		t.Errorf("removedDatasets must be emitted after changedDatasets: %s", text)
	}
}

// TestRemovalPlanJSONRoundTrip verifies removals parse from plan JSON.
func TestRemovalPlanJSONRoundTrip(t *testing.T) {
	var p Plan
	if err := json.Unmarshal([]byte(`{"removals":["A"," B "],"changes":[]}`), &p); err != nil {
		t.Fatalf("Unmarshal unexpected error: %v", err)
	}
	if !reflect.DeepEqual(p.Removals, []string{"A", " B "}) {
		t.Errorf("Removals = %v, want [A ' B ']", p.Removals)
	}
}

// datasetNames extracts the sorted dataset names from a graph file.
func datasetNames(gf GraphFile) []string {
	names := make([]string, 0, len(gf.Datasets))
	for _, ds := range gf.Datasets {
		names = append(names, ds.Name)
	}
	return names
}
