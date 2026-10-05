package chainledger

import (
	"reflect"
	"slices"
	"strings"
	"testing"
)

// The lineage shared by the rename-plus-cutoff regressions:
//
//	source -> cut ------------------> merged -> down -> tail
//	  |                                ^
//	  |                                |
//	  b1 -> z1 ------------------------+   (bypass branch 1)
//	  |                                |
//	  b2 -> a2 ------------------------+   (bypass branch 2)
//
// source derives cut, b1 and b2; cut derives merged directly (the short route,
// length 2); b1 and b2 each reach merged through one intermediate (the two
// bypass routes, both length 3); merged derives down and down derives tail.
// The branch names are chosen so the full-path rule and a last-hop-only rule
// disagree: at hop 1 b1 < b2 selects branch 1, while the hops just before the
// merge order as a2 < z1 and would wrongly select branch 2.
//
// Each sequence below builds this same graph with a different legal
// registration order and a different declaration order for merged's upstream
// list; names, distances, paths and result ordering must come out identical.
var cutoffRenameSequences = [][][]string{
	{
		{"source"},
		{"cut", "source"},
		{"b1", "source"},
		{"z1", "b1"},
		{"b2", "source"},
		{"a2", "b2"},
		{"merged", "cut", "z1", "a2"},
		{"down", "merged"},
		{"tail", "down"},
	},
	{
		{"source"},
		{"b2", "source"}, // b2-side branch registered first
		{"a2", "b2"},
		{"cut", "source"},
		{"b1", "source"},
		{"z1", "b1"},
		{"merged", "a2", "z1", "cut"}, // upstream list flipped, cutoff last
		{"down", "merged"},
		{"tail", "down"},
	},
	{
		{"source"},
		{"b1", "source"},
		{"b2", "source"},
		{"z1", "b1"},
		{"a2", "b2"},
		{"cut", "source"}, // cutoff registered after both bypass branches
		{"merged", "z1", "a2", "cut"},
		{"down", "merged"},
		{"tail", "down"},
	},
}

// Regression for renaming a dataset on the bypass route that a cutoff query
// had selected. With cut as the cutoff, the short source -> cut -> merged
// route is truncated at the cutoff: cut itself is still reported at distance
// 1, but merged, down and tail must be explained by the length-3 bypass
// routes, never by the truncated short one. The two bypass routes have equal
// length, so the whole path is compared name by name from the origin: b1 < b2
// at hop 1 selects [source b1 z1 merged] even though the last hop before the
// merge orders as a2 < z1. Renaming b1 to zz — a name that sorts past b2 —
// changes no edge, distance or reachable set, but the same full-path
// comparison now selects [source b2 a2 merged], and down and tail inherit
// that route. The old name must not survive in any record or path, the
// distances stay the bypass edge counts (the cutoff short route is not
// re-adopted), and merged is still listed exactly once.
func TestImpactsWithCutoffsRenameOnBypassRouteFlipsMergeExplanation(t *testing.T) {
	wantCutoffBefore := []Impact{
		{Dataset: "b1", Distance: 1, Path: []string{"source", "b1"}},
		{Dataset: "b2", Distance: 1, Path: []string{"source", "b2"}},
		{Dataset: "cut", Distance: 1, Path: []string{"source", "cut"}},
		{Dataset: "a2", Distance: 2, Path: []string{"source", "b2", "a2"}},
		{Dataset: "z1", Distance: 2, Path: []string{"source", "b1", "z1"}},
		{Dataset: "merged", Distance: 3, Path: []string{"source", "b1", "z1", "merged"}},
		{Dataset: "down", Distance: 4, Path: []string{"source", "b1", "z1", "merged", "down"}},
		{Dataset: "tail", Distance: 5, Path: []string{"source", "b1", "z1", "merged", "down", "tail"}},
	}
	wantCutoffAfter := []Impact{
		{Dataset: "b2", Distance: 1, Path: []string{"source", "b2"}},
		{Dataset: "cut", Distance: 1, Path: []string{"source", "cut"}},
		{Dataset: "zz", Distance: 1, Path: []string{"source", "zz"}},
		{Dataset: "a2", Distance: 2, Path: []string{"source", "b2", "a2"}},
		{Dataset: "z1", Distance: 2, Path: []string{"source", "zz", "z1"}},
		{Dataset: "merged", Distance: 3, Path: []string{"source", "b2", "a2", "merged"}},
		{Dataset: "down", Distance: 4, Path: []string{"source", "b2", "a2", "merged", "down"}},
		{Dataset: "tail", Distance: 5, Path: []string{"source", "b2", "a2", "merged", "down", "tail"}},
	}

	for i, seq := range cutoffRenameSequences {
		graph := buildRegisteredGraph(t, seq)
		assertConsistent(t, graph)

		// Baseline unrestricted query: merged is distance 2 through cut, and
		// down and tail ride the same short route. These shorter explanations
		// belong to the unrestricted query alone.
		full := mustImpacts(t, graph, "source")
		assertImpactOnce(t, full, "merged", 2, []string{"source", "cut", "merged"})
		assertImpactOnce(t, full, "down", 3, []string{"source", "cut", "merged", "down"})
		assertImpactOnce(t, full, "tail", 4, []string{"source", "cut", "merged", "down", "tail"})

		// Baseline cutoff query: the cutoff stays in the result at distance 1,
		// but nothing past it is explained through it. The merge node is
		// explained via b1/z1 because the full paths first differ at hop 1
		// (b1 < b2) — a rule comparing only the hop before the merge would
		// wrongly prefer a2 < z1.
		before := mustImpactsWithCutoffs(t, graph, "source", "cut")
		if !reflect.DeepEqual(before, wantCutoffBefore) {
			t.Fatalf("sequence %d: cutoff query before rename = %v, want %v", i, before, wantCutoffBefore)
		}
		assertImpactOnce(t, before, "cut", 1, []string{"source", "cut"})
		assertImpactOnce(t, before, "merged", 3, []string{"source", "b1", "z1", "merged"})
		assertImpactOnce(t, before, "down", 4, []string{"source", "b1", "z1", "merged", "down"})
		assertImpactOnce(t, before, "tail", 5, []string{"source", "b1", "z1", "merged", "down", "tail"})
		beforeSnapshot := copyImpacts(before)

		// Rename the hop-1 dataset of the selected bypass route: zz sorts past
		// b2, so the other allowed route now wins the name comparison. No edge
		// is added, removed or reordered.
		mustRename(t, graph, "b1", "zz")
		assertConsistent(t, graph)
		if _, ok := graph["b1"]; ok {
			t.Errorf("sequence %d: old name b1 still present in graph", i)
		}
		if got, want := len(graph), 9; got != want {
			t.Fatalf("sequence %d: dataset count = %d, want %d", i, got, want)
		}
		assertEntry(t, graph, "zz", []string{"source"}, []string{"z1"})
		assertEntry(t, graph, "z1", []string{"zz"}, []string{"merged"})
		graphBeforeQueries := snapshot(graph)

		// The cutoff query after the rename: the reachable set and every
		// distance keep their pre-rename meaning with b1 replaced by zz, but
		// the merge node and everything behind it is now explained through the
		// b2/a2 route. Distances remain the bypass edge counts (3, 4, 5): the
		// truncated length-2 route through cut must not be re-adopted.
		after := mustImpactsWithCutoffs(t, graph, "source", "cut")
		if !reflect.DeepEqual(after, wantCutoffAfter) {
			t.Fatalf("sequence %d: cutoff query after rename = %v, want %v", i, after, wantCutoffAfter)
		}
		assertImpactOnce(t, after, "merged", 3, []string{"source", "b2", "a2", "merged"})
		assertImpactOnce(t, after, "down", 4, []string{"source", "b2", "a2", "merged", "down"})
		assertImpactOnce(t, after, "tail", 5, []string{"source", "b2", "a2", "merged", "down", "tail"})

		// The released old name appears in no record and no path hop.
		for _, im := range after {
			if im.Dataset == "b1" || slices.Contains(im.Path, "b1") {
				t.Fatalf("sequence %d: old name b1 leaked into post-rename impact %+v", i, im)
			}
		}

		// Only the renamed dataset changed identity: the affected set and the
		// per-dataset distances are exactly the pre-rename ones with b1
		// substituted by zz.
		if len(after) != len(before) {
			t.Fatalf("sequence %d: affected set size changed: before %d, after %d", i, len(before), len(after))
		}
		distanceBefore := map[string]int{}
		for _, im := range before {
			distanceBefore[im.Dataset] = im.Distance
		}
		for _, im := range after {
			name := im.Dataset
			if name == "zz" {
				name = "b1"
			}
			d, ok := distanceBefore[name]
			if !ok {
				t.Fatalf("sequence %d: %s has no pre-rename counterpart (as %s)", i, im.Dataset, name)
			}
			if d != im.Distance {
				t.Fatalf("sequence %d: %s distance = %d, want %d (rename must not change distances)",
					i, im.Dataset, im.Distance, d)
			}
		}

		// The unrestricted query still takes the short route through cut; the
		// cutoff query's longer bypass distances are not a graph change.
		fullAfter := mustImpacts(t, graph, "source")
		assertImpactOnce(t, fullAfter, "merged", 2, []string{"source", "cut", "merged"})
		assertImpactOnce(t, fullAfter, "zz", 1, []string{"source", "zz"})

		// The freed old name is unregistered: naming it as a cutoff fails the
		// whole query with an error naming it and nil results, rather than
		// being ignored while partial impacts are returned.
		got, err := ImpactsWithCutoffs(graph, "source", []string{"b1"})
		if err == nil || !strings.Contains(err.Error(), "cutoff dataset not found: b1") {
			t.Fatalf("sequence %d: stale cutoff b1: want not-found error naming b1, got %v", i, err)
		}
		if got != nil {
			t.Fatalf("sequence %d: stale cutoff b1: want nil results, got %v", i, got)
		}

		// Neither the successful nor the failed queries changed any node, edge
		// or stored list order.
		if !reflect.DeepEqual(snapshot(graph), graphBeforeQueries) {
			t.Fatalf("sequence %d: queries changed graph: before=%v after=%v",
				i, graphBeforeQueries, snapshot(graph))
		}

		// The result returned before the rename kept its original names,
		// distances and paths.
		if !reflect.DeepEqual(before, beforeSnapshot) {
			t.Fatalf("sequence %d: pre-rename cutoff result changed: got %v, snapshot %v",
				i, before, beforeSnapshot)
		}
	}
}

// Regression for renaming the cutoff dataset itself. After cut is renamed to
// gate, the new name is the registered one: used as the cutoff it is still
// reported at distance 1 and still stops propagation, while merged, down and
// tail keep arriving over the bypass routes. The released old name is no
// longer registered, so putting it in the cutoff list fails the whole query
// with an error naming it and nil results — it must not be silently ignored
// while a partial impact scope is returned. Successful and failed queries
// alike leave the lineage and every stored list order untouched.
func TestImpactsWithCutoffsRenamedCutoffNewNameStopsOldNameFails(t *testing.T) {
	graph := buildRegisteredGraph(t, cutoffRenameSequences[0])
	assertConsistent(t, graph)

	// Baseline with the original cutoff name, for contrast.
	baseline := mustImpactsWithCutoffs(t, graph, "source", "cut")
	assertImpactOnce(t, baseline, "cut", 1, []string{"source", "cut"})
	assertImpactOnce(t, baseline, "merged", 3, []string{"source", "b1", "z1", "merged"})

	mustRename(t, graph, "cut", "gate")
	assertConsistent(t, graph)
	if _, ok := graph["cut"]; ok {
		t.Error("old name cut still present in graph")
	}
	assertEntry(t, graph, "gate", []string{"source"}, []string{"merged"})
	assertEntry(t, graph, "merged", []string{"gate", "z1", "a2"}, []string{"down"})
	graphBeforeQueries := snapshot(graph)

	// The new cutoff name works exactly as the old one did: gate is reported
	// at distance 1, propagation stops there, and everything past it that has
	// a bypass still arrives over the bypass routes at the bypass distances.
	want := []Impact{
		{Dataset: "b1", Distance: 1, Path: []string{"source", "b1"}},
		{Dataset: "b2", Distance: 1, Path: []string{"source", "b2"}},
		{Dataset: "gate", Distance: 1, Path: []string{"source", "gate"}},
		{Dataset: "a2", Distance: 2, Path: []string{"source", "b2", "a2"}},
		{Dataset: "z1", Distance: 2, Path: []string{"source", "b1", "z1"}},
		{Dataset: "merged", Distance: 3, Path: []string{"source", "b1", "z1", "merged"}},
		{Dataset: "down", Distance: 4, Path: []string{"source", "b1", "z1", "merged", "down"}},
		{Dataset: "tail", Distance: 5, Path: []string{"source", "b1", "z1", "merged", "down", "tail"}},
	}
	impacts := mustImpactsWithCutoffs(t, graph, "source", "gate")
	if !reflect.DeepEqual(impacts, want) {
		t.Fatalf("cutoff query with renamed cutoff = %v, want %v", impacts, want)
	}
	assertImpactOnce(t, impacts, "gate", 1, []string{"source", "gate"})
	assertImpactOnce(t, impacts, "merged", 3, []string{"source", "b1", "z1", "merged"})
	assertImpactOnce(t, impacts, "down", 4, []string{"source", "b1", "z1", "merged", "down"})
	assertImpactOnce(t, impacts, "tail", 5, []string{"source", "b1", "z1", "merged", "down", "tail"})
	// The released old name appears in no record and no path hop.
	for _, im := range impacts {
		if im.Dataset == "cut" || slices.Contains(im.Path, "cut") {
			t.Fatalf("old name cut leaked into post-rename impact %+v", im)
		}
	}

	// The unrestricted query still uses the short route, now under the new
	// name; the cutoff query's bypass explanations are not a graph change.
	full := mustImpacts(t, graph, "source")
	assertImpactOnce(t, full, "merged", 2, []string{"source", "gate", "merged"})
	assertImpactOnce(t, full, "gate", 1, []string{"source", "gate"})

	// The old name has not been re-registered: naming it as a cutoff fails
	// the whole query. The error names the unregistered cutoff, the results
	// are nil, and no partial impact scope is produced.
	got, err := ImpactsWithCutoffs(graph, "source", []string{"cut"})
	if err == nil || !strings.Contains(err.Error(), "cutoff dataset not found: cut") {
		t.Fatalf("stale cutoff cut: want not-found error naming cut, got %v", err)
	}
	if got != nil {
		t.Fatalf("stale cutoff cut: want nil results, got %v", got)
	}
	// The same failure when the stale name follows a valid cutoff in the list.
	got, err = ImpactsWithCutoffs(graph, "source", []string{"gate", "cut"})
	if err == nil || !strings.Contains(err.Error(), "cutoff dataset not found: cut") {
		t.Fatalf("stale cutoff cut after valid gate: want not-found error naming cut, got %v", err)
	}
	if got != nil {
		t.Fatalf("stale cutoff cut after valid gate: want nil results, got %v", got)
	}
	// The old name is gone for the origin position too.
	if _, err := Impacts(graph, "cut"); err == nil || !strings.Contains(err.Error(), "cut") {
		t.Fatalf("Impacts(cut) after rename: want not-found error naming cut, got %v", err)
	}

	// Successful and failed queries left every node, edge and stored list
	// order exactly as the rename produced them.
	if !reflect.DeepEqual(snapshot(graph), graphBeforeQueries) {
		t.Fatalf("queries changed graph: before=%v after=%v", graphBeforeQueries, snapshot(graph))
	}
	assertConsistent(t, graph)
}
