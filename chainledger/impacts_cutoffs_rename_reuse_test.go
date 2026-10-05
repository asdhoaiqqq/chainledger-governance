package chainledger

import (
	"reflect"
	"slices"
	"strings"
	"testing"
)

// The cutoff regressions here pin down that a cutoff list acts on the names
// registered at query time, after a cutoff dataset has been renamed and its old
// name has then been registered again for a brand-new dataset.
//
// The lineage starts as a single chain:
//
//	source -> cut -> report -> view
//
// cut is then renamed to gate; the renamed node keeps cut's exact dependency
// position (source -> gate -> report -> view), and the old name cut is
// unregistered. Only afterwards is cut registered again as a brand-new dataset
// under source and given its own downstream bridge, and report is made to
// depend on both gate and bridge. The final lineage is:
//
//	source -> gate ---> report -> view
//	  |                  ^
//	  +-> cut -> bridge -+
//
// gate is the renamed original node (its old dependency position, including
// its direct downstream report, moved with it); cut is a fresh registration
// whose only downstream is bridge. The two identities must never be confused:
// a cutoff named cut stops the new cut subtree, not the gate subtree, and
// propagation into report over the gate branch must not stop merely because the
// gate node used to carry the name cut.

// buildRenamedCutoffThenReuseGraph builds the initial source -> cut -> report
// -> view chain, renames cut to gate keeping its dependency position, then
// registers cut afresh under source with its own downstream bridge and makes
// report depend on both gate and bridge. It stops at the requested phase so the
// unregistered-old-name window (after the rename, before the re-registration)
// can be tested on its own.
func buildRenamedCutoffThenReuseGraph(t *testing.T, phase string) map[string]*Lineage {
	t.Helper()
	graph := buildRegisteredGraph(t, [][]string{
		{"source"},
		{"cut", "source"},
		{"report", "cut"},
		{"view", "report"},
	})
	assertConsistent(t, graph)

	mustRename(t, graph, "cut", "gate")
	assertConsistent(t, graph)
	// The rename keeps the original dependency position: gate is now the node
	// between source and report, and the old name is gone from the registry.
	assertEntry(t, graph, "gate", []string{"source"}, []string{"report"})
	assertEntry(t, graph, "source", nil, []string{"gate"})
	assertEntry(t, graph, "report", []string{"gate"}, []string{"view"})
	assertEntry(t, graph, "view", []string{"report"}, nil)
	if _, ok := graph["cut"]; ok {
		t.Fatalf("old name cut still registered after rename")
	}

	if phase == "renamed" {
		return graph
	}
	if phase != "reregistered" {
		t.Fatalf("unknown phase %q", phase)
	}

	// Register cut as a brand-new dataset under source, with its own downstream
	// bridge. It inherits nothing from gate: gate's direct downstream report
	// must not be copied onto the fresh cut node.
	mustRegister(t, graph, "cut", "source")
	mustRegister(t, graph, "bridge", "cut")
	// report now derives from both the renamed gate and the new bridge; its own
	// downstream view is retained.
	mustRegister(t, graph, "report", "gate", "bridge")
	assertConsistent(t, graph)
	assertEntry(t, graph, "cut", []string{"source"}, []string{"bridge"})
	assertEntry(t, graph, "bridge", []string{"cut"}, []string{"report"})
	assertEntry(t, graph, "gate", []string{"source"}, []string{"report"})
	assertEntry(t, graph, "source", nil, []string{"gate", "cut"})
	assertEntry(t, graph, "report", []string{"gate", "bridge"}, []string{"view"})
	assertEntry(t, graph, "view", []string{"report"}, nil)
	if got, want := len(graph), 6; got != want {
		t.Fatalf("dataset count = %d, want %d", got, want)
	}
	return graph
}

// Baseline on the starting chain: before the rename, cut is a normal
// registered cutoff — it stays in the result at distance 1 and stops
// propagation, so report and view past it do not appear. This is the behavior
// the rename then moves onto the name gate; the reuse tests that follow prove
// it never lingers on the freed name cut.
func TestImpactsWithCutoffsInitialChainCutoffBaseline(t *testing.T) {
	graph := buildRegisteredGraph(t, [][]string{
		{"source"},
		{"cut", "source"},
		{"report", "cut"},
		{"view", "report"},
	})
	assertConsistent(t, graph)

	impacts := mustImpactsWithCutoffs(t, graph, "source", "cut")
	want := []Impact{
		{Dataset: "cut", Distance: 1, Path: []string{"source", "cut"}},
	}
	if !reflect.DeepEqual(impacts, want) {
		t.Fatalf("cutoff cut on initial chain = %v, want %v", impacts, want)
	}
	assertImpactOnce(t, impacts, "cut", 1, []string{"source", "cut"})
	if names := impactNames(impacts); slices.Contains(names, "report") || slices.Contains(names, "view") {
		t.Fatalf("report and view lie past cutoff cut and must be gone, got %v", names)
	}
}

// While the old name is still unregistered after the rename, using it as a
// cutoff fails the whole query: nil results and an error naming cut as
// unregistered. The cutoff acts on the query-time registry, so it must not
// resolve to the renamed gate node, nor be silently ignored while a partial
// impact scope comes back. The new name gate works as the cutoff instead: gate
// itself stays in the result at distance 1, and report and view past it do not
// appear.
func TestImpactsWithCutoffsRenamedCutoffOldNameUnregisteredNewNameStops(t *testing.T) {
	graph := buildRenamedCutoffThenReuseGraph(t, "renamed")
	before := snapshot(graph)

	// cutoff cut while cut is unregistered: the whole request fails, results
	// are nil, and the error names cut exactly as any other unregistered
	// cutoff would be named.
	got, err := ImpactsWithCutoffs(graph, "source", []string{"cut"})
	if err == nil || !strings.Contains(err.Error(), "cutoff dataset not found: cut") {
		t.Fatalf("stale cutoff cut: want not-found error naming cut, got %v", err)
	}
	if got != nil {
		t.Fatalf("stale cutoff cut: want nil results, got %v", got)
	}
	// The stale name after a valid cutoff fails the whole request the same way;
	// the reachable gate must not come back as a partial result.
	got, err = ImpactsWithCutoffs(graph, "source", []string{"gate", "cut"})
	if err == nil || !strings.Contains(err.Error(), "cutoff dataset not found: cut") {
		t.Fatalf("stale cutoff cut after valid gate: want not-found error naming cut, got %v", err)
	}
	if got != nil {
		t.Fatalf("stale cutoff cut after valid gate: want nil results, got %v", got)
	}
	// A duplicate stale name changes nothing about the failure.
	if got, err := ImpactsWithCutoffs(graph, "source", []string{"cut", "cut"}); err == nil ||
		!strings.Contains(err.Error(), "cutoff dataset not found: cut") || got != nil {
		t.Fatalf("duplicate stale cutoff cut: want not-found error and nil results, got %v, %v", got, err)
	}

	// The new registered name is the one that acts as the cutoff: gate stays in
	// the result at distance 1 and nothing propagates past it, so report and
	// view disappear together.
	impacts := mustImpactsWithCutoffs(t, graph, "source", "gate")
	want := []Impact{
		{Dataset: "gate", Distance: 1, Path: []string{"source", "gate"}},
	}
	if !reflect.DeepEqual(impacts, want) {
		t.Fatalf("cutoff gate before re-registration = %v, want %v", impacts, want)
	}
	assertImpactOnce(t, impacts, "gate", 1, []string{"source", "gate"})
	if names := impactNames(impacts); slices.Contains(names, "report") || slices.Contains(names, "view") {
		t.Fatalf("report and view lie past cutoff gate and must be gone, got %v", names)
	}

	// The unrestricted query still walks the renamed chain under the new name.
	full := mustImpacts(t, graph, "source")
	wantFull := []Impact{
		{Dataset: "gate", Distance: 1, Path: []string{"source", "gate"}},
		{Dataset: "report", Distance: 2, Path: []string{"source", "gate", "report"}},
		{Dataset: "view", Distance: 3, Path: []string{"source", "gate", "report", "view"}},
	}
	if !reflect.DeepEqual(full, wantFull) {
		t.Fatalf("unrestricted query after rename = %v, want %v", full, wantFull)
	}
	assertImpactOnce(t, full, "report", 2, []string{"source", "gate", "report"})
	assertImpactOnce(t, full, "view", 3, []string{"source", "gate", "report", "view"})

	// Successful and failed queries leave every node, edge and stored list
	// order exactly as the rename produced them.
	if !reflect.DeepEqual(snapshot(graph), before) {
		t.Fatalf("queries changed graph: before=%v after=%v", before, snapshot(graph))
	}
	assertConsistent(t, graph)
}

// After cut is registered again as a brand-new dataset under source with its
// own downstream bridge, and report depends on gate and bridge together, a
// cutoff named cut acts on the NEW cut node only. cut and gate are both
// distance-1 results; bridge lies past cutoff cut and must not appear; report
// and view still arrive over the gate branch at distances 2 and 3 — the route
// goes through gate, so propagation must not stop at report merely because gate
// once carried the name cut. The fresh cut must not inherit gate's direct
// downstream report.
func TestImpactsWithCutoffsReusedOldNameCutsNewDatasetNotRenamedGate(t *testing.T) {
	graph := buildRenamedCutoffThenReuseGraph(t, "reregistered")
	before := snapshot(graph)

	impacts := mustImpactsWithCutoffs(t, graph, "source", "cut")
	want := []Impact{
		{Dataset: "cut", Distance: 1, Path: []string{"source", "cut"}},
		{Dataset: "gate", Distance: 1, Path: []string{"source", "gate"}},
		{Dataset: "report", Distance: 2, Path: []string{"source", "gate", "report"}},
		{Dataset: "view", Distance: 3, Path: []string{"source", "gate", "report", "view"}},
	}
	if !reflect.DeepEqual(impacts, want) {
		t.Fatalf("cutoff cut after re-registration = %v, want %v", impacts, want)
	}
	// Both distance-1 nodes appear once, name-sorted within the level.
	assertImpactOnce(t, impacts, "cut", 1, []string{"source", "cut"})
	assertImpactOnce(t, impacts, "gate", 1, []string{"source", "gate"})
	// bridge is the fresh cut's own downstream: it lies past cutoff cut and is
	// unreachable any other way, so it must be gone.
	if names := impactNames(impacts); slices.Contains(names, "bridge") {
		t.Fatalf("bridge lies past cutoff cut and must not appear, got %v", names)
	}
	// report and view survive over the gate route; the explanation must traverse
	// gate (never the node that used to be called cut), and no surviving path
	// may pass through cutoff cut.
	assertImpactOnce(t, impacts, "report", 2, []string{"source", "gate", "report"})
	assertImpactOnce(t, impacts, "view", 3, []string{"source", "gate", "report", "view"})
	for _, im := range impacts {
		if im.Dataset == "cut" {
			continue
		}
		if slices.Contains(im.Path, "cut") {
			t.Fatalf("path past cutoff cut routes through it: %v", im.Path)
		}
		if im.Path[0] != "source" || im.Path[len(im.Path)-1] != im.Dataset || len(im.Path) != im.Distance+1 {
			t.Fatalf("malformed record: %v", im)
		}
	}
	if got, wantNames := impactNames(impacts), []string{"cut", "gate", "report", "view"}; !reflect.DeepEqual(got, wantNames) {
		t.Fatalf("order = %v, want %v (distance then name)", got, wantNames)
	}

	// The fresh cut did not inherit gate's direct downstream: its only child is
	// bridge, while gate alone still points at report.
	assertEntry(t, graph, "cut", []string{"source"}, []string{"bridge"})
	assertEntry(t, graph, "gate", []string{"source"}, []string{"report"})

	// The successful query changed no node, edge or stored list order.
	if !reflect.DeepEqual(snapshot(graph), before) {
		t.Fatalf("cutoff query changed graph: before=%v after=%v", before, snapshot(graph))
	}
	assertConsistent(t, graph)
}

// With gate as the cutoff on the re-registered graph, gate itself still
// appears, but the cut -> bridge branch is a different route and keeps
// propagating: report and view are reached through cut and bridge at distances
// 3 and 4. The merge node report and its downstream view each appear exactly
// once, ordered distance-first and name-tied like every other query.
func TestImpactsWithCutoffsRenamedGateCutsGateBranchReusedCutBranchPropagates(t *testing.T) {
	graph := buildRenamedCutoffThenReuseGraph(t, "reregistered")
	before := snapshot(graph)

	impacts := mustImpactsWithCutoffs(t, graph, "source", "gate")
	want := []Impact{
		{Dataset: "cut", Distance: 1, Path: []string{"source", "cut"}},
		{Dataset: "gate", Distance: 1, Path: []string{"source", "gate"}},
		{Dataset: "bridge", Distance: 2, Path: []string{"source", "cut", "bridge"}},
		{Dataset: "report", Distance: 3, Path: []string{"source", "cut", "bridge", "report"}},
		{Dataset: "view", Distance: 4, Path: []string{"source", "cut", "bridge", "report", "view"}},
	}
	if !reflect.DeepEqual(impacts, want) {
		t.Fatalf("cutoff gate after re-registration = %v, want %v", impacts, want)
	}
	assertImpactOnce(t, impacts, "gate", 1, []string{"source", "gate"})
	assertImpactOnce(t, impacts, "cut", 1, []string{"source", "cut"})
	assertImpactOnce(t, impacts, "bridge", 2, []string{"source", "cut", "bridge"})
	// The merge node and its downstream survive exactly once, explained through
	// the cut/bridge route that avoided gate.
	assertImpactOnce(t, impacts, "report", 3, []string{"source", "cut", "bridge", "report"})
	assertImpactOnce(t, impacts, "view", 4, []string{"source", "cut", "bridge", "report", "view"})
	for _, im := range impacts {
		if im.Dataset == "gate" {
			continue
		}
		if slices.Contains(im.Path, "gate") {
			t.Fatalf("path past cutoff gate routes through it: %v", im.Path)
		}
	}
	if got, wantNames := impactNames(impacts), []string{"cut", "gate", "bridge", "report", "view"}; !reflect.DeepEqual(got, wantNames) {
		t.Fatalf("order = %v, want %v (distance then name)", got, wantNames)
	}

	// The successful query changed no node, edge or stored list order.
	if !reflect.DeepEqual(snapshot(graph), before) {
		t.Fatalf("cutoff query changed graph: before=%v after=%v", before, snapshot(graph))
	}
	assertConsistent(t, graph)
}

// The ordinary no-cutoff query over the final graph reaches all five
// downstreams and explains the merge node report through its shorter route,
// which is the one-edge hop via gate (distance 2), not the two-edge route
// through cut and bridge (distance 3); view inherits the gate route at
// distance 3. The nil and empty cutoff forms stay exactly this query.
func TestImpactsAfterRenameAndReuseShortExplanationViaRenamedGate(t *testing.T) {
	graph := buildRenamedCutoffThenReuseGraph(t, "reregistered")
	before := snapshot(graph)

	want := []Impact{
		{Dataset: "cut", Distance: 1, Path: []string{"source", "cut"}},
		{Dataset: "gate", Distance: 1, Path: []string{"source", "gate"}},
		{Dataset: "bridge", Distance: 2, Path: []string{"source", "cut", "bridge"}},
		{Dataset: "report", Distance: 2, Path: []string{"source", "gate", "report"}},
		{Dataset: "view", Distance: 3, Path: []string{"source", "gate", "report", "view"}},
	}
	full := mustImpacts(t, graph, "source")
	if !reflect.DeepEqual(full, want) {
		t.Fatalf("plain Impacts = %v, want %v", full, want)
	}
	if got, wantNames := impactNames(full), []string{"cut", "gate", "bridge", "report", "view"}; !reflect.DeepEqual(got, wantNames) {
		t.Fatalf("order = %v, want %v", got, wantNames)
	}
	// The shorter gate route explains report even though cut sorts ahead of
	// gate: edge count wins before the name comparison.
	assertImpactOnce(t, full, "report", 2, []string{"source", "gate", "report"})
	assertImpactOnce(t, full, "view", 3, []string{"source", "gate", "report", "view"})
	assertImpactOnce(t, full, "bridge", 2, []string{"source", "cut", "bridge"})

	// Impacts is exactly the nil/empty-cutoff form of ImpactsWithCutoffs.
	if got := mustImpactsWithCutoffs(t, graph, "source"); !reflect.DeepEqual(got, full) {
		t.Fatalf("nil cutoffs = %v, want %v", got, full)
	}
	if got, err := ImpactsWithCutoffs(graph, "source", []string{}); err != nil || !reflect.DeepEqual(got, full) {
		t.Fatalf("empty cutoffs = %v, %v; want %v", got, err, full)
	}

	// Neither query form mutates the graph.
	if !reflect.DeepEqual(snapshot(graph), before) {
		t.Fatalf("queries changed graph: before=%v after=%v", before, snapshot(graph))
	}
	assertConsistent(t, graph)
}
