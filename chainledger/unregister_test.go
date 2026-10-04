package chainledger

import (
	"reflect"
	"slices"
	"strings"
	"testing"
)

func mustUnregister(t *testing.T, graph map[string]*Lineage, name string) {
	t.Helper()
	if err := Unregister(graph, name); err != nil {
		t.Fatalf("Unregister(%s): %v", name, err)
	}
}

// The worked example from the spec: raw derives detail; detail derives report
// and view. Removing the leaf report deletes its registration, removes it from
// detail's downstream list while keeping view's slot and dependencies, and
// leaves the remaining lineage queryable exactly as the surviving edges
// describe.
func TestUnregisterSpecExample(t *testing.T) {
	graph := map[string]*Lineage{}
	mustRegister(t, graph, "raw")
	mustRegister(t, graph, "detail", "raw")
	mustRegister(t, graph, "report", "detail")
	mustRegister(t, graph, "view", "detail")
	// report also depends on a second source with its own other downstream.
	mustRegister(t, graph, "extra")
	mustRegister(t, graph, "otherchild", "extra")
	mustRegister(t, graph, "report", "detail", "extra")
	assertConsistent(t, graph)
	assertEntry(t, graph, "report", []string{"detail", "extra"}, nil)
	assertEntry(t, graph, "detail", []string{"raw"}, []string{"report", "view"})
	assertEntry(t, graph, "extra", nil, []string{"otherchild", "report"})

	before := snapshot(graph)
	mustUnregister(t, graph, "report")

	// The registration is gone from the map; no empty node remains.
	if _, ok := graph["report"]; ok {
		t.Fatal("report registration still present after Unregister")
	}
	if got, want := len(graph), 5; got != want {
		t.Fatalf("dataset count = %d, want %d; graph=%v", got, want, graph)
	}

	// detail loses exactly report from its children, view keeps its position.
	assertEntry(t, graph, "detail", []string{"raw"}, []string{"view"})
	// The second source loses the reverse reference to report but keeps its
	// own other downstream, in place.
	assertEntry(t, graph, "extra", nil, []string{"otherchild"})
	// view's position and dependencies are retained.
	assertEntry(t, graph, "view", []string{"detail"}, nil)
	assertEntry(t, graph, "raw", nil, []string{"detail"})
	assertEntry(t, graph, "otherchild", []string{"extra"}, nil)
	assertConsistent(t, graph)

	// No list of a node not directly connected to report gained or lost
	// anything: raw's children match the pre-call snapshot, and view's own
	// upstream list is unchanged.
	if !slices.Equal(graph["raw"].Children, before.children["raw"]) {
		t.Errorf("raw children changed: got %v, want %v", graph["raw"].Children, before.children["raw"])
	}
	if got, want := graph["view"].Parents, []string{"detail"}; !slices.Equal(got, want) {
		t.Errorf("view parents = %v, want %v", got, want)
	}

	// From raw the impact scope still reaches detail and view, never report.
	impacts := mustImpacts(t, graph, "raw")
	if got, want := impactNames(impacts), []string{"detail", "view"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("Impacts(raw) = %v, want %v", got, want)
	}
	assertImpact(t, impacts, "detail", 1, []string{"raw", "detail"})
	assertImpact(t, impacts, "view", 2, []string{"raw", "detail", "view"})

	// From view the provenance trace still reaches detail and raw, with the
	// same shortest distances and explanation paths as before.
	upstreams := mustUpstreams(t, graph, "view")
	if got, want := upstreamNames(upstreams), []string{"detail", "raw"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("Upstreams(view) = %v, want %v", got, want)
	}
	assertUpstream(t, upstreams, "detail", 1, []string{"detail", "view"})
	assertUpstream(t, upstreams, "raw", 2, []string{"raw", "detail", "view"})

	// The removed name is treated as unregistered by both lineage queries.
	if _, err := Impacts(graph, "report"); err == nil || !strings.Contains(err.Error(), "report") {
		t.Fatalf("Impacts(report) after removal: want not-found error naming report, got %v", err)
	}
	if _, err := Upstreams(graph, "report"); err == nil || !strings.Contains(err.Error(), "report") {
		t.Fatalf("Upstreams(report) after removal: want not-found error naming report, got %v", err)
	}

	// Removing it again reports it as unregistered and changes nothing.
	again := snapshot(graph)
	if err := Unregister(graph, "report"); err == nil || !strings.Contains(err.Error(), "report") {
		t.Fatalf("second Unregister(report): want not-found error naming report, got %v", err)
	}
	if !reflect.DeepEqual(snapshot(graph), again) {
		t.Fatalf("second removal attempt changed graph: before=%v after=%v", again, snapshot(graph))
	}
}

// A leaf with several direct upstreams is removed from every one of their
// downstream lists; each remaining list keeps its relative order, and
// upstreams not connected to the leaf are byte-for-byte untouched.
func TestUnregisterMultiParentLeafCleansAllReverseEdges(t *testing.T) {
	graph := map[string]*Lineage{}
	mustRegister(t, graph, "p1")
	mustRegister(t, graph, "p2")
	mustRegister(t, graph, "p3")
	mustRegister(t, graph, "a", "p1")
	mustRegister(t, graph, "leaf", "p1", "p2") // leaf between two children in p1's list
	mustRegister(t, graph, "b", "p1")
	mustRegister(t, graph, "c", "p2")
	mustRegister(t, graph, "unrelated", "p3")
	assertConsistent(t, graph)

	mustUnregister(t, graph, "leaf")

	if _, ok := graph["leaf"]; ok {
		t.Fatal("leaf still present after Unregister")
	}
	assertEntry(t, graph, "p1", nil, []string{"a", "b"})
	assertEntry(t, graph, "p2", nil, []string{"c"})
	assertEntry(t, graph, "p3", nil, []string{"unrelated"})
	assertEntry(t, graph, "a", []string{"p1"}, nil)
	assertEntry(t, graph, "b", []string{"p1"}, nil)
	assertEntry(t, graph, "c", []string{"p2"}, nil)
	assertEntry(t, graph, "unrelated", []string{"p3"}, nil)
	assertConsistent(t, graph)
}

// Relative order inside each affected list survives when the removed name sits
// at the start, middle or end, and unaffected lists stay exactly as they were.
func TestUnregisterPreservesListOrder(t *testing.T) {
	build := func() map[string]*Lineage {
		graph := map[string]*Lineage{}
		mustRegister(t, graph, "p")
		mustRegister(t, graph, "x")
		mustRegister(t, graph, "first", "p", "x")
		mustRegister(t, graph, "gone", "p", "x")
		mustRegister(t, graph, "second", "p")
		mustRegister(t, graph, "third", "p")
		return graph
	}

	positions := []struct {
		name  string
		child string
	}{
		{"first child", "first"},
		{"middle child", "gone"},
		{"last child", "third"},
	}
	for _, pos := range positions {
		graph := build()
		before := snapshot(graph)
		mustUnregister(t, graph, pos.child)

		var wantP []string
		for _, c := range before.children["p"] {
			if c != pos.child {
				wantP = append(wantP, c)
			}
		}
		assertEntry(t, graph, "p", nil, wantP)

		var wantX []string
		for _, c := range before.children["x"] {
			if c != pos.child {
				wantX = append(wantX, c)
			}
		}
		assertEntry(t, graph, "x", nil, wantX)

		// Every surviving node's lists match the pre-call lists except for the
		// removed name's deletion from its direct upstreams.
		for name, entry := range graph {
			wantParents := before.parents[name]
			if !slices.Equal(entry.Parents, wantParents) {
				t.Errorf("%s parents = %v, want %v", name, entry.Parents, wantParents)
			}
			wantChildren := before.children[name]
			if name == "p" {
				wantChildren = wantP
			}
			if name == "x" {
				wantChildren = wantX
			}
			if !slices.Equal(entry.Children, wantChildren) {
				t.Errorf("%s children = %v, want %v", name, entry.Children, wantChildren)
			}
		}
		assertConsistent(t, graph)
	}
}

// A standalone dataset with neither upstreams nor downstreams is removed
// successfully.
func TestUnregisterIsolatedDataset(t *testing.T) {
	graph := map[string]*Lineage{}
	mustRegister(t, graph, "lonely")
	mustRegister(t, graph, "keep")
	mustRegister(t, graph, "child", "keep")

	mustUnregister(t, graph, "lonely")
	if _, ok := graph["lonely"]; ok {
		t.Fatal("lonely still present after Unregister")
	}
	if got, want := len(graph), 2; got != want {
		t.Fatalf("dataset count = %d, want %d", got, want)
	}
	assertEntry(t, graph, "keep", nil, []string{"child"})
	assertEntry(t, graph, "child", []string{"keep"}, nil)
	assertConsistent(t, graph)

	// The last remaining datasets can be peeled off leaf first, then the root.
	mustUnregister(t, graph, "child")
	mustUnregister(t, graph, "keep")
	if len(graph) != 0 {
		t.Fatalf("graph = %v, want empty graph", graph)
	}
}

// A dataset with a direct downstream cannot be removed, no matter how many
// upstreams it has; the error names it and says it still has downstreams, and
// the refusal is atomic.
func TestUnregisterRejectedWhileDownstreamsRemain(t *testing.T) {
	graph := map[string]*Lineage{}
	mustRegister(t, graph, "raw")
	mustRegister(t, graph, "src2")
	mustRegister(t, graph, "detail", "raw", "src2")
	mustRegister(t, graph, "report", "detail")
	mustRegister(t, graph, "view", "report")
	before := snapshot(graph)

	err := Unregister(graph, "detail")
	if err == nil {
		t.Fatal("expected rejection for dataset with downstream, got nil")
	}
	if !strings.Contains(err.Error(), "detail") {
		t.Errorf("error %q should name the dataset detail", err.Error())
	}
	if !strings.Contains(err.Error(), "downstream") {
		t.Errorf("error %q should state that direct downstreams remain", err.Error())
	}

	// No partial modification: node present, all edges and list orders intact.
	if !reflect.DeepEqual(snapshot(graph), before) {
		t.Fatalf("rejected unregister changed graph: before=%v after=%v", before, snapshot(graph))
	}
	assertEntry(t, graph, "detail", []string{"raw", "src2"}, []string{"report"})
	assertEntry(t, graph, "raw", nil, []string{"detail"})
	assertEntry(t, graph, "src2", nil, []string{"detail"})
	assertEntry(t, graph, "report", []string{"detail"}, []string{"view"})
	assertEntry(t, graph, "view", []string{"report"}, nil)
	assertConsistent(t, graph)

	// A root that still feeds a dataset is refused the same way.
	err = Unregister(graph, "raw")
	if err == nil || !strings.Contains(err.Error(), "raw") || !strings.Contains(err.Error(), "downstream") {
		t.Fatalf("root with child: want error naming raw and its downstream, got %v", err)
	}
	if !reflect.DeepEqual(snapshot(graph), before) {
		t.Fatalf("second rejection changed graph: before=%v after=%v", before, snapshot(graph))
	}

	// After the child chain is gone, the refusals become legal removals.
	mustUnregister(t, graph, "view")
	mustUnregister(t, graph, "report")
	mustUnregister(t, graph, "detail")
	assertEntry(t, graph, "raw", nil, nil)
	assertEntry(t, graph, "src2", nil, nil)
	assertConsistent(t, graph)
}

// In a diamond, the merge node and its exclusive downstream leave together only
// after the downstream is removed; until then the merge stays and the shared
// sources' child lists are untouched by the refused request.
func TestUnregisterDiamondLeafThenMerge(t *testing.T) {
	graph := buildRegisteredGraph(t, [][]string{
		{"raw"},
		{"a", "raw"},
		{"b", "raw"},
		{"merge", "a", "b"},
		{"down", "merge"},
	})
	before := snapshot(graph)

	// merge has a direct downstream: refused, nothing changes.
	if err := Unregister(graph, "merge"); err == nil ||
		!strings.Contains(err.Error(), "merge") || !strings.Contains(err.Error(), "downstream") {
		t.Fatalf("want downstream error naming merge, got %v", err)
	}
	if !reflect.DeepEqual(snapshot(graph), before) {
		t.Fatalf("rejected removal changed graph: before=%v after=%v", before, snapshot(graph))
	}

	mustUnregister(t, graph, "down")
	assertEntry(t, graph, "merge", []string{"a", "b"}, nil)
	assertConsistent(t, graph)

	mustUnregister(t, graph, "merge")
	if _, ok := graph["merge"]; ok {
		t.Fatal("merge still present after Unregister")
	}
	// Both parents lose merge; raw keeps both branches in their order.
	assertEntry(t, graph, "a", []string{"raw"}, nil)
	assertEntry(t, graph, "b", []string{"raw"}, nil)
	assertEntry(t, graph, "raw", nil, []string{"a", "b"})
	assertConsistent(t, graph)
}

// All error cases name the problem correctly and never touch the graph.
func TestUnregisterErrors(t *testing.T) {
	graph := map[string]*Lineage{}
	mustRegister(t, graph, "a")
	mustRegister(t, graph, "b", "a")
	before := snapshot(graph)

	// Empty name: missing name.
	if err := Unregister(graph, ""); err == nil || !strings.Contains(err.Error(), "name is required") {
		t.Fatalf("empty name: want required-name error, got %v", err)
	}
	if !reflect.DeepEqual(snapshot(graph), before) {
		t.Fatal("empty-name rejection changed the graph")
	}

	// Non-empty unregistered name against a populated graph.
	if err := Unregister(graph, "ghost"); err == nil || !strings.Contains(err.Error(), "ghost") {
		t.Fatalf("unknown name: want error naming ghost, got %v", err)
	}
	if !reflect.DeepEqual(snapshot(graph), before) {
		t.Fatal("unknown-name rejection changed the graph")
	}

	// An initialized-but-empty graph treats any non-empty name as unregistered.
	if err := Unregister(map[string]*Lineage{}, "ghost"); err == nil ||
		!strings.Contains(err.Error(), "ghost") {
		t.Fatalf("empty graph: want error naming ghost, got %v", err)
	}

	// A nil graph behaves the same.
	if err := Unregister(nil, "ghost"); err == nil || !strings.Contains(err.Error(), "ghost") {
		t.Fatalf("nil graph: want error naming ghost, got %v", err)
	}
	if err := Unregister(nil, ""); err == nil || !strings.Contains(err.Error(), "name is required") {
		t.Fatalf("nil graph empty name: want required-name error, got %v", err)
	}

	assertEntry(t, graph, "a", nil, []string{"b"})
	assertEntry(t, graph, "b", []string{"a"}, nil)
	assertConsistent(t, graph)
}

// Names match by exact registered value, case-sensitive: a differently cased
// request is unregistered-or-not independently, and removing one case variant
// leaves the other in place.
func TestUnregisterExactNameMatch(t *testing.T) {
	graph := map[string]*Lineage{}
	mustRegister(t, graph, "raw")
	mustRegister(t, graph, "report", "raw")
	mustRegister(t, graph, "Report", "raw")
	assertConsistent(t, graph)

	// "Report" with the wrong case is not registered.
	if err := Unregister(graph, "RePoRt"); err == nil || !strings.Contains(err.Error(), "RePoRt") {
		t.Fatalf("case-insensitive match: want not-found error naming RePoRt, got %v", err)
	}

	// Removing the uppercase variant leaves the lowercase report and raw.
	mustUnregister(t, graph, "Report")
	if _, ok := graph["Report"]; ok {
		t.Fatal("Report still present after Unregister")
	}
	assertEntry(t, graph, "report", []string{"raw"}, nil)
	assertEntry(t, graph, "raw", nil, []string{"report"})
	assertConsistent(t, graph)

	mustUnregister(t, graph, "report")
	if _, ok := graph["report"]; ok {
		t.Fatal("report still present after Unregister")
	}
	assertEntry(t, graph, "raw", nil, nil)
}

// After a successful removal, registration under the freed name starts a
// completely fresh dataset, and the rest of the lineage keeps working.
func TestUnregisterThenRegisterFresh(t *testing.T) {
	graph := map[string]*Lineage{}
	mustRegister(t, graph, "raw")
	mustRegister(t, graph, "leaf", "raw")

	mustUnregister(t, graph, "leaf")
	assertEntry(t, graph, "raw", nil, nil)

	// The freed name registers as a brand-new isolated dataset.
	mustRegister(t, graph, "leaf")
	assertEntry(t, graph, "leaf", nil, nil)
	assertEntry(t, graph, "raw", nil, nil)
	assertConsistent(t, graph)

	// And it can take part in new lineage afterwards.
	mustRegister(t, graph, "leaf", "raw")
	assertEntry(t, graph, "leaf", []string{"raw"}, nil)
	assertEntry(t, graph, "raw", nil, []string{"leaf"})
	assertConsistent(t, graph)
}
