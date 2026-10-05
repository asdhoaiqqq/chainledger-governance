package chainledger

import (
	"reflect"
	"slices"
	"strings"
	"testing"
)

// Regression for re-registering a cutoff's old name as a brand-new dataset
// after the original cutoff dataset was renamed. The lineage starts as a
// simple chain:
//
//	source -> cut -> report -> view
//
// and cut is renamed to gate in place, keeping its exact position:
//
//	source -> gate -> report -> view
//
// While the old name is still unregistered, naming cut as a cutoff fails the
// whole query with an error naming it and nil results, while gate stops
// propagation exactly as cut did. The freed name is then registered as an
// independent dataset under source with its own downstream bridge, and report
// is re-registered to depend on gate and bridge together:
//
//	source -> gate ------+--> report -> view
//	  |                  |
//	  +-> cut -> bridge -+
//
// The two registrations that share the history of the name cut must stay
// independent: the new cut has only bridge as its direct downstream (it does
// not inherit report from gate), and the renamed gate keeps report. Cutting
// the new cut stops only its own branch, so report and view still arrive
// through gate; cutting gate leaves the cut -> bridge branch open, so report
// and view arrive one edge later through cut and bridge. The unrestricted
// query sees all five datasets and explains report through the shorter gate
// route.
func TestImpactsWithCutoffsRenamedCutoffOldNameReregistered(t *testing.T) {
	graph := buildRegisteredGraph(t, [][]string{
		{"source"},
		{"cut", "source"},
		{"report", "cut"},
		{"view", "report"},
	})
	assertConsistent(t, graph)

	// Baseline before the rename: the cutoff stays in the result at distance 1
	// and everything past it is gone; the unrestricted query walks the chain.
	baselineCut := mustImpactsWithCutoffs(t, graph, "source", "cut")
	if want := []Impact{{Dataset: "cut", Distance: 1, Path: []string{"source", "cut"}}}; !reflect.DeepEqual(baselineCut, want) {
		t.Fatalf("baseline cutoff query = %v, want %v", baselineCut, want)
	}
	baselineFull := mustImpacts(t, graph, "source")
	assertImpactOnce(t, baselineFull, "report", 2, []string{"source", "cut", "report"})
	assertImpactOnce(t, baselineFull, "view", 3, []string{"source", "cut", "report", "view"})

	// Rename the cutoff dataset in place: the node keeps its position and
	// every neighbor reference is rewired, no edge added, removed or reordered.
	mustRename(t, graph, "cut", "gate")
	assertConsistent(t, graph)
	if _, ok := graph["cut"]; ok {
		t.Fatal("old name cut still present in graph after rename")
	}
	assertEntry(t, graph, "source", nil, []string{"gate"})
	assertEntry(t, graph, "gate", []string{"source"}, []string{"report"})
	assertEntry(t, graph, "report", []string{"gate"}, []string{"view"})
	assertEntry(t, graph, "view", []string{"report"}, nil)
	graphBeforeQueries := snapshot(graph)

	// The old name has not been re-registered: naming it as a cutoff fails the
	// whole query. The error names the unregistered cutoff and the results are
	// nil — no partial impact scope is produced.
	got, err := ImpactsWithCutoffs(graph, "source", []string{"cut"})
	if err == nil || !strings.Contains(err.Error(), "cutoff dataset not found: cut") {
		t.Fatalf("stale cutoff cut: want not-found error naming cut, got %v", err)
	}
	if got != nil {
		t.Fatalf("stale cutoff cut: want nil results, got %v", got)
	}

	// The new name is the registered one: gate is reported at distance 1 and
	// stops propagation, so report and view leave the result together.
	renamed := mustImpactsWithCutoffs(t, graph, "source", "gate")
	wantRenamed := []Impact{
		{Dataset: "gate", Distance: 1, Path: []string{"source", "gate"}},
	}
	if !reflect.DeepEqual(renamed, wantRenamed) {
		t.Fatalf("cutoff query with renamed cutoff = %v, want %v", renamed, wantRenamed)
	}
	if names := impactNames(renamed); slices.Contains(names, "report") || slices.Contains(names, "view") {
		t.Fatalf("report and view lie only past cutoff gate and must be gone, got %v", names)
	}

	// Neither the failed nor the successful query touched the graph.
	if !reflect.DeepEqual(snapshot(graph), graphBeforeQueries) {
		t.Fatalf("queries changed graph: before=%v after=%v", graphBeforeQueries, snapshot(graph))
	}

	// Register the freed name as a brand-new dataset under source, derive
	// bridge from it, and re-register report to depend on gate and bridge
	// together. report keeps its own downstream view. The new cut is an
	// independent registration: it must not inherit gate's direct downstream
	// report, and gate must not lose it.
	mustRegister(t, graph, "cut", "source")
	mustRegister(t, graph, "bridge", "cut")
	mustRegister(t, graph, "report", "gate", "bridge")
	assertConsistent(t, graph)
	assertEntry(t, graph, "source", nil, []string{"gate", "cut"})
	assertEntry(t, graph, "gate", []string{"source"}, []string{"report"})
	assertEntry(t, graph, "cut", []string{"source"}, []string{"bridge"})
	assertEntry(t, graph, "bridge", []string{"cut"}, []string{"report"})
	assertEntry(t, graph, "report", []string{"gate", "bridge"}, []string{"view"})
	assertEntry(t, graph, "view", []string{"report"}, nil)
	graphBeforeQueries = snapshot(graph)

	// Cutting the re-registered cut stops only its own branch: cut and gate
	// are both distance-1 results, bridge is gone because every route to it
	// passes the cutoff, but report and view still arrive through gate at
	// distances 2 and 3. Propagation must not stop at gate just because that
	// node once carried the name cut.
	wantCutCutoff := []Impact{
		{Dataset: "cut", Distance: 1, Path: []string{"source", "cut"}},
		{Dataset: "gate", Distance: 1, Path: []string{"source", "gate"}},
		{Dataset: "report", Distance: 2, Path: []string{"source", "gate", "report"}},
		{Dataset: "view", Distance: 3, Path: []string{"source", "gate", "report", "view"}},
	}
	cutCutoff := mustImpactsWithCutoffs(t, graph, "source", "cut")
	if !reflect.DeepEqual(cutCutoff, wantCutCutoff) {
		t.Fatalf("cutoff query with re-registered cut = %v, want %v", cutCutoff, wantCutCutoff)
	}
	assertImpactOnce(t, cutCutoff, "cut", 1, []string{"source", "cut"})
	assertImpactOnce(t, cutCutoff, "gate", 1, []string{"source", "gate"})
	assertImpactOnce(t, cutCutoff, "report", 2, []string{"source", "gate", "report"})
	assertImpactOnce(t, cutCutoff, "view", 3, []string{"source", "gate", "report", "view"})
	if names := impactNames(cutCutoff); slices.Contains(names, "bridge") {
		t.Fatalf("bridge lies only past cutoff cut and must be gone, got %v", names)
	}

	// Cutting gate instead leaves the new cut's branch open: gate itself is
	// still reported at distance 1, cut and bridge propagate, and the merged
	// report and its downstream view are reached through cut and bridge at
	// distances 3 and 4 — each exactly once, ordered by distance then name.
	wantGateCutoff := []Impact{
		{Dataset: "cut", Distance: 1, Path: []string{"source", "cut"}},
		{Dataset: "gate", Distance: 1, Path: []string{"source", "gate"}},
		{Dataset: "bridge", Distance: 2, Path: []string{"source", "cut", "bridge"}},
		{Dataset: "report", Distance: 3, Path: []string{"source", "cut", "bridge", "report"}},
		{Dataset: "view", Distance: 4, Path: []string{"source", "cut", "bridge", "report", "view"}},
	}
	gateCutoff := mustImpactsWithCutoffs(t, graph, "source", "gate")
	if !reflect.DeepEqual(gateCutoff, wantGateCutoff) {
		t.Fatalf("cutoff query with gate after re-registration = %v, want %v", gateCutoff, wantGateCutoff)
	}
	assertImpactOnce(t, gateCutoff, "gate", 1, []string{"source", "gate"})
	assertImpactOnce(t, gateCutoff, "bridge", 2, []string{"source", "cut", "bridge"})
	assertImpactOnce(t, gateCutoff, "report", 3, []string{"source", "cut", "bridge", "report"})
	assertImpactOnce(t, gateCutoff, "view", 4, []string{"source", "cut", "bridge", "report", "view"})

	// The unrestricted query returns all five datasets and explains report
	// through the shorter gate route; the longer cut -> bridge route is never
	// offered as the explanation.
	wantFull := []Impact{
		{Dataset: "cut", Distance: 1, Path: []string{"source", "cut"}},
		{Dataset: "gate", Distance: 1, Path: []string{"source", "gate"}},
		{Dataset: "bridge", Distance: 2, Path: []string{"source", "cut", "bridge"}},
		{Dataset: "report", Distance: 2, Path: []string{"source", "gate", "report"}},
		{Dataset: "view", Distance: 3, Path: []string{"source", "gate", "report", "view"}},
	}
	full := mustImpacts(t, graph, "source")
	if !reflect.DeepEqual(full, wantFull) {
		t.Fatalf("unrestricted query = %v, want %v", full, wantFull)
	}
	assertImpactOnce(t, full, "report", 2, []string{"source", "gate", "report"})
	// A nil cutoff list remains exactly the unrestricted query.
	if nilCutoffs, err := ImpactsWithCutoffs(graph, "source", nil); err != nil || !reflect.DeepEqual(nilCutoffs, full) {
		t.Fatalf("nil cutoffs = %v, %v; want %v", nilCutoffs, err, full)
	}

	// Every query above is read-only: nodes, edges and stored list orders are
	// exactly as the registrations left them.
	if !reflect.DeepEqual(snapshot(graph), graphBeforeQueries) {
		t.Fatalf("queries changed graph: before=%v after=%v", graphBeforeQueries, snapshot(graph))
	}
	assertConsistent(t, graph)
}
