package chainledger

import (
	"reflect"
	"slices"
	"strings"
	"testing"
)

func mustRegister(t *testing.T, graph map[string]*Lineage, name string, parents ...string) {
	t.Helper()
	if err := Register(graph, Dataset{Name: name}, parents); err != nil {
		t.Fatalf("Register(%s, %v): %v", name, parents, err)
	}
}

func sameStrings(got, want []string) bool {
	if len(got) != len(want) {
		return false
	}
	for i := range got {
		if got[i] != want[i] {
			return false
		}
	}
	return true
}

func assertEntry(t *testing.T, graph map[string]*Lineage, name string, wantParents, wantChildren []string) {
	t.Helper()
	entry, ok := graph[name]
	if !ok {
		t.Fatalf("dataset %q missing from graph", name)
	}
	if !sameStrings(entry.Parents, wantParents) {
		t.Errorf("%s parents = %v, want %v", name, entry.Parents, wantParents)
	}
	if !sameStrings(entry.Children, wantChildren) {
		t.Errorf("%s children = %v, want %v", name, entry.Children, wantChildren)
	}
}

// Every parent edge must be mirrored by exactly one matching child edge.
func assertConsistent(t *testing.T, graph map[string]*Lineage) {
	t.Helper()
	for name, entry := range graph {
		counts := map[string]int{}
		for _, c := range entry.Children {
			counts[c]++
			child, ok := graph[c]
			if !ok {
				t.Errorf("%s lists unknown child %s", name, c)
				continue
			}
			if !slices.Contains(child.Parents, name) {
				t.Errorf("%s is child of %s but has no matching parent edge", c, name)
			}
		}
		for c, n := range counts {
			if n != 1 {
				t.Errorf("%s lists child %s %d times", name, c, n)
			}
		}
		for _, p := range entry.Parents {
			parent, ok := graph[p]
			if !ok {
				t.Errorf("%s lists unknown parent %s", name, p)
				continue
			}
			if !slices.Contains(parent.Children, name) {
				t.Errorf("%s is parent of %s but has no matching child edge", p, name)
			}
		}
	}
}

func TestRegisterBasicChainAndConsistency(t *testing.T) {
	graph := map[string]*Lineage{}
	mustRegister(t, graph, "raw")
	mustRegister(t, graph, "detail", "raw")
	mustRegister(t, graph, "summary", "detail")

	assertEntry(t, graph, "raw", nil, []string{"detail"})
	assertEntry(t, graph, "detail", []string{"raw"}, []string{"summary"})
	assertEntry(t, graph, "summary", []string{"detail"}, nil)
	assertConsistent(t, graph)

	if got, want := Roots(graph), []string{"raw"}; !reflect.DeepEqual(got, want) {
		t.Errorf("Roots = %v, want %v", got, want)
	}
}

// The scenario from the spec: re-registering detail while still using raw must
// succeed even though raw is detail's existing upstream.
func TestReregisterKeepsExistingUpstream(t *testing.T) {
	graph := map[string]*Lineage{}
	mustRegister(t, graph, "raw")
	mustRegister(t, graph, "detail", "raw")
	mustRegister(t, graph, "summary", "detail")

	mustRegister(t, graph, "detail", "raw")
	assertEntry(t, graph, "raw", nil, []string{"detail"})
	assertEntry(t, graph, "detail", []string{"raw"}, []string{"summary"})
	assertEntry(t, graph, "summary", []string{"detail"}, nil)
	assertConsistent(t, graph)

	// Same registration again: still successful, nothing changes.
	before := snapshot(graph)
	mustRegister(t, graph, "detail", "raw")
	if !reflect.DeepEqual(snapshot(graph), before) {
		t.Fatalf("identical re-registration changed graph: before=%v after=%v", before, snapshot(graph))
	}
}

// Pointing raw at summary closes raw -> summary -> detail -> raw.
func TestCycleAcrossMultipleLevelsRejected(t *testing.T) {
	graph := map[string]*Lineage{}
	mustRegister(t, graph, "raw")
	mustRegister(t, graph, "detail", "raw")
	mustRegister(t, graph, "summary", "detail")

	before := snapshot(graph)
	err := Register(graph, Dataset{Name: "raw"}, []string{"summary"})
	if err == nil {
		t.Fatal("expected cycle error, got nil")
	}
	if !strings.Contains(err.Error(), "cycle") || !strings.Contains(err.Error(), "summary") {
		t.Errorf("error %q should mention a cycle and the related name summary", err.Error())
	}
	if !reflect.DeepEqual(snapshot(graph), before) {
		t.Fatalf("failed registration changed graph: before=%v after=%v", before, snapshot(graph))
	}
	assertEntry(t, graph, "raw", nil, []string{"detail"})
	assertConsistent(t, graph)
}

// Multiple upstreams plus a diamond shape are legitimate and must not look like
// a cycle, while a genuinely cyclic multi-parent request is refused.
func TestMultipleUpstreamsAndDiamond(t *testing.T) {
	graph := map[string]*Lineage{}
	mustRegister(t, graph, "x")
	mustRegister(t, graph, "y")
	mustRegister(t, graph, "a", "x")
	mustRegister(t, graph, "b", "x", "y")
	mustRegister(t, graph, "d", "a", "b")
	assertConsistent(t, graph)
	assertEntry(t, graph, "x", nil, []string{"a", "b"})
	assertEntry(t, graph, "y", nil, []string{"b"})
	assertEntry(t, graph, "d", []string{"a", "b"}, nil)

	// x -> d closes through both a and b, several levels deep.
	before := snapshot(graph)
	if err := Register(graph, Dataset{Name: "x"}, []string{"d"}); err == nil {
		t.Fatal("expected transitive cycle through diamond to be rejected")
	} else if !strings.Contains(err.Error(), "d") {
		t.Errorf("error %q should name parent d", err.Error())
	}
	if !reflect.DeepEqual(snapshot(graph), before) {
		t.Fatal("failed cycle registration mutated the graph")
	}

	// Direct mutual reference is the simple case and must also fail.
	if err := Register(graph, Dataset{Name: "a"}, []string{"d"}); err == nil {
		t.Fatal("expected direct mutual-reference cycle to be rejected")
	}
	assertConsistent(t, graph)
}

// Replacing upstreams: old ones lose the reverse edge, new ones gain it at the
// end, retained ones keep a single entry in place; own children survive.
func TestReregisterReplacesUpstreamsKeepsDownstreams(t *testing.T) {
	graph := map[string]*Lineage{}
	mustRegister(t, graph, "old1")
	mustRegister(t, graph, "old2")
	mustRegister(t, graph, "keep")
	mustRegister(t, graph, "new1")
	mustRegister(t, graph, "new2")
	mustRegister(t, graph, "center", "old1", "keep", "old2")
	// Existing children in a fixed order, with another unrelated child interleaved.
	mustRegister(t, graph, "childX", "center", "keep")
	mustRegister(t, graph, "childY", "center")

	mustRegister(t, graph, "center", "keep", "new1", "new2")

	assertEntry(t, graph, "center",
		[]string{"keep", "new1", "new2"},
		[]string{"childX", "childY"})
	assertEntry(t, graph, "old1", nil, nil)
	assertEntry(t, graph, "old2", nil, nil)
	assertEntry(t, graph, "keep", nil, []string{"center", "childX"})
	assertEntry(t, graph, "new1", nil, []string{"center"})
	assertEntry(t, graph, "new2", nil, []string{"center"})
	assertConsistent(t, graph)
}

// Duplicate parent names collapse to one entry, first occurrence wins, and the
// declared list order is the stored order.
func TestDuplicateUpstreamsDedupedFirstWins(t *testing.T) {
	graph := map[string]*Lineage{}
	mustRegister(t, graph, "a")
	mustRegister(t, graph, "b")
	mustRegister(t, graph, "c")
	mustRegister(t, graph, "d", "b", "a", "b", "c", "a")

	assertEntry(t, graph, "d", []string{"b", "a", "c"}, nil)
	assertEntry(t, graph, "a", nil, []string{"d"})
	assertEntry(t, graph, "b", nil, []string{"d"})
	assertEntry(t, graph, "c", nil, []string{"d"})
	assertConsistent(t, graph)

	// Re-registering with duplicates is repeatedly idempotent.
	before := snapshot(graph)
	mustRegister(t, graph, "d", "b", "a", "b", "c", "a")
	mustRegister(t, graph, "d", "b", "a", "b", "c", "a")
	if !reflect.DeepEqual(snapshot(graph), before) {
		t.Fatal("idempotent re-registration changed the graph")
	}
}

// New children land at the end of an upstream's child list; existing relative
// order is untouched.
func TestNewDownstreamAppendedAtEnd(t *testing.T) {
	graph := map[string]*Lineage{}
	mustRegister(t, graph, "p")
	mustRegister(t, graph, "first", "p")
	mustRegister(t, graph, "second", "p")

	mustRegister(t, graph, "late", "p")
	assertEntry(t, graph, "p", nil, []string{"first", "second", "late"})

	// Re-registering first must not move it.
	mustRegister(t, graph, "first", "p")
	assertEntry(t, graph, "p", nil, []string{"first", "second", "late"})
	assertConsistent(t, graph)
}

// Empty upstream list turns the dataset into a root: reverse edges removed,
// downstreams retained; Roots updates and stays name-sorted.
func TestEmptyUpstreamsBecomesRoot(t *testing.T) {
	graph := map[string]*Lineage{}
	mustRegister(t, graph, "raw")
	mustRegister(t, graph, "other")
	mustRegister(t, graph, "mid", "raw", "other")
	mustRegister(t, graph, "leaf", "mid")

	mustRegister(t, graph, "mid") // no upstreams now
	assertEntry(t, graph, "mid", nil, []string{"leaf"})
	assertEntry(t, graph, "raw", nil, nil)
	assertEntry(t, graph, "other", nil, nil)
	assertEntry(t, graph, "leaf", []string{"mid"}, nil)
	assertConsistent(t, graph)

	if got, want := Roots(graph), []string{"mid", "other", "raw"}; !reflect.DeepEqual(got, want) {
		t.Errorf("Roots = %v, want %v", got, want)
	}
}

// A failure partway through the list leaves no new node and no partial reverse
// edges, even when a valid parent precedes the offending one.
func TestFailureIsAtomicWithValidParentFirst(t *testing.T) {
	graph := map[string]*Lineage{}
	mustRegister(t, graph, "p")
	mustRegister(t, graph, "p2")
	mustRegister(t, graph, "existing", "p")
	before := snapshot(graph)

	err := Register(graph, Dataset{Name: "newkid"}, []string{"p", "ghost"})
	if err == nil || !strings.Contains(err.Error(), "ghost") {
		t.Fatalf("want unknown-parent error naming ghost, got %v", err)
	}
	if _, ok := graph["newkid"]; ok {
		t.Error("failed registration left new node newkid in graph")
	}
	if !reflect.DeepEqual(snapshot(graph), before) {
		t.Fatalf("graph changed after failure: before=%v after=%v", before, snapshot(graph))
	}
	assertEntry(t, graph, "p", nil, []string{"existing"})
	assertConsistent(t, graph)

	// Self reference after a valid parent is likewise atomic.
	err = Register(graph, Dataset{Name: "loop"}, []string{"p2", "loop"})
	if err == nil || !strings.Contains(err.Error(), "its own parent") {
		t.Fatalf("want self-parent error, got %v", err)
	}
	if _, ok := graph["loop"]; ok {
		t.Error("failed self-parent registration left node in graph")
	}
	if !reflect.DeepEqual(snapshot(graph), before) {
		t.Fatalf("graph changed after self-parent failure")
	}

	// After fixing the request, the dataset registers normally.
	mustRegister(t, graph, "newkid", "p", "p2")
	assertEntry(t, graph, "newkid", []string{"p", "p2"}, nil)
	assertConsistent(t, graph)
}

// Rejected re-registration of an existing node preserves its old content and
// every other node.
func TestFailedReregisterPreservesOldEdges(t *testing.T) {
	graph := map[string]*Lineage{}
	mustRegister(t, graph, "raw")
	mustRegister(t, graph, "mid", "raw")
	mustRegister(t, graph, "top", "mid")
	before := snapshot(graph)

	if err := Register(graph, Dataset{Name: "mid"}, []string{"top"}); err == nil {
		t.Fatal("expected cycle rejection on re-register")
	}
	if !reflect.DeepEqual(snapshot(graph), before) {
		t.Fatalf("failed re-registration changed graph: before=%v after=%v", before, snapshot(graph))
	}

	if err := Register(graph, Dataset{Name: "mid"}, []string{"missing"}); err == nil {
		t.Fatal("expected unknown-parent rejection on re-register")
	}
	if !reflect.DeepEqual(snapshot(graph), before) {
		t.Fatalf("failed re-registration changed graph: before=%v after=%v", before, snapshot(graph))
	}
	assertConsistent(t, graph)
}

func TestValidationErrors(t *testing.T) {
	graph := map[string]*Lineage{}
	mustRegister(t, graph, "a")

	if err := Register(graph, Dataset{}, []string{"a"}); err == nil ||
		!strings.Contains(err.Error(), "name is required") {
		t.Fatalf("empty name: want required-name error, got %v", err)
	}

	err := Register(graph, Dataset{Name: "b"}, []string{"a", "nope"})
	if err == nil || !strings.Contains(err.Error(), "unknown parent nope") {
		t.Fatalf("unknown parent: want error naming nope, got %v", err)
	}

	err = Register(graph, Dataset{Name: "b"}, []string{"b"})
	if err == nil || !strings.Contains(err.Error(), "b") {
		t.Fatalf("self parent: want error naming b, got %v", err)
	}
	if _, ok := graph["b"]; ok {
		t.Error("self-parent failure must not create the node")
	}
}

// With several problems in one list, the first in input order is reported.
func TestFirstProblemInInputOrderReported(t *testing.T) {
	graph := map[string]*Lineage{}
	mustRegister(t, graph, "a")
	// top -> down, so registering top with parent down closes a cycle.
	mustRegister(t, graph, "top")
	mustRegister(t, graph, "down", "top")

	cases := []struct {
		name    string
		dataset string
		parents []string
		want    string
	}{
		{"unknown before self", "n", []string{"ghost", "n"}, "ghost"},
		{"self before unknown", "n", []string{"n", "ghost"}, "its own parent"},
		{"cycle before unknown", "top", []string{"down", "ghost"}, "cycle"},
		{"unknown before cycle", "top", []string{"ghost", "down"}, "ghost"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := Register(graph, Dataset{Name: tc.dataset}, tc.parents)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("parents %v: want error containing %q, got %v", tc.parents, tc.want, err)
			}
		})
	}
}

// Mutating the caller's slice after a successful Register must not affect the
// stored graph.
func TestCallerSliceMutationIsolated(t *testing.T) {
	graph := map[string]*Lineage{}
	mustRegister(t, graph, "a")
	mustRegister(t, graph, "b")

	parents := []string{"a", "b"}
	mustRegister(t, graph, "d", parents...)
	parents[0] = "tampered"
	parents = append(parents, "extra")

	assertEntry(t, graph, "d", []string{"a", "b"}, nil)
	assertConsistent(t, graph)
}

// Roots is sorted by name even when datasets were registered out of order.
func TestRootsSorted(t *testing.T) {
	graph := map[string]*Lineage{}
	mustRegister(t, graph, "zeta")
	mustRegister(t, graph, "alpha")
	mustRegister(t, graph, "mid", "alpha")
	mustRegister(t, graph, "beta")

	if got, want := Roots(graph), []string{"alpha", "beta", "zeta"}; !reflect.DeepEqual(got, want) {
		t.Errorf("Roots = %v, want %v", got, want)
	}
}

func impactNames(impacts []Impact) []string {
	names := make([]string, len(impacts))
	for i, im := range impacts {
		names[i] = im.Dataset
	}
	return names
}

func assertImpact(t *testing.T, impacts []Impact, name string, wantDistance int, wantPath []string) {
	t.Helper()
	for _, im := range impacts {
		if im.Dataset != name {
			continue
		}
		if im.Distance != wantDistance {
			t.Errorf("impact %s distance = %d, want %d", name, im.Distance, wantDistance)
		}
		if !sameStrings(im.Path, wantPath) {
			t.Errorf("impact %s path = %v, want %v", name, im.Path, wantPath)
		}
		return
	}
	t.Fatalf("impact %q missing from %v", name, impactNames(impacts))
}

// The worked example from the spec: raw fans out to a and b, both feed report,
// report feeds view. The merge node appears once at distance 2 with the
// lexicographically smallest explanation path.
func TestImpactsDiamondMerge(t *testing.T) {
	graph := map[string]*Lineage{}
	mustRegister(t, graph, "raw")
	mustRegister(t, graph, "a", "raw")
	mustRegister(t, graph, "b", "raw")
	mustRegister(t, graph, "report", "a", "b")
	mustRegister(t, graph, "view", "report")

	impacts, err := Impacts(graph, "raw")
	if err != nil {
		t.Fatalf("Impacts(raw): %v", err)
	}
	if got, want := impactNames(impacts), []string{"a", "b", "report", "view"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("impact order = %v, want %v", got, want)
	}
	assertImpact(t, impacts, "a", 1, []string{"raw", "a"})
	assertImpact(t, impacts, "b", 1, []string{"raw", "b"})
	assertImpact(t, impacts, "report", 2, []string{"raw", "a", "report"})
	assertImpact(t, impacts, "view", 3, []string{"raw", "a", "report", "view"})

	// Querying an interior node starts a fresh scope: only its own downstream.
	impacts, err = Impacts(graph, "report")
	if err != nil {
		t.Fatalf("Impacts(report): %v", err)
	}
	if got, want := impactNames(impacts), []string{"view"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("impact order = %v, want %v", got, want)
	}
	assertImpact(t, impacts, "view", 1, []string{"report", "view"})
}

// After report is re-registered directly under raw, distances shorten and the
// previously returned result keeps its original content because paths are copies.
func TestImpactsReflectReregisterAndOldResultStable(t *testing.T) {
	graph := map[string]*Lineage{}
	mustRegister(t, graph, "raw")
	mustRegister(t, graph, "a", "raw")
	mustRegister(t, graph, "b", "raw")
	mustRegister(t, graph, "report", "a", "b")
	mustRegister(t, graph, "view", "report")

	before, err := Impacts(graph, "raw")
	if err != nil {
		t.Fatalf("Impacts(raw): %v", err)
	}
	beforeCopy := make([]Impact, len(before))
	for i, im := range before {
		beforeCopy[i] = Impact{im.Dataset, im.Distance, append([]string(nil), im.Path...)}
	}

	// report now depends directly on raw as well; a and b remain its parents.
	mustRegister(t, graph, "report", "raw", "a", "b")

	after, err := Impacts(graph, "raw")
	if err != nil {
		t.Fatalf("Impacts(raw) after re-register: %v", err)
	}
	if got, want := impactNames(after), []string{"a", "b", "report", "view"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("impact order = %v, want %v", got, want)
	}
	assertImpact(t, after, "report", 1, []string{"raw", "report"})
	assertImpact(t, after, "view", 2, []string{"raw", "report", "view"})

	if !reflect.DeepEqual(before, beforeCopy) {
		t.Fatalf("earlier result changed after re-register/new query: before=%v snapshot=%v", before, beforeCopy)
	}
}

// Among multiple shortest paths the lexicographically smallest full name
// sequence wins, compared name by name in Go string order, even when child list
// order would suggest otherwise.
func TestImpactsLexicographicallySmallestShortestPath(t *testing.T) {
	graph := map[string]*Lineage{}
	mustRegister(t, graph, "s")
	// Register the lexicographically larger branch first so stored child order
	// disagrees with the expected path choice.
	mustRegister(t, graph, "z", "s")
	mustRegister(t, graph, "a", "s")
	mustRegister(t, graph, "m", "z", "a")

	impacts, err := Impacts(graph, "s")
	if err != nil {
		t.Fatalf("Impacts(s): %v", err)
	}
	assertImpact(t, impacts, "m", 2, []string{"s", "a", "m"})

	// Tie decided at the final hop: both x routes to n have equal length.
	mustRegister(t, graph, "x1", "s")
	mustRegister(t, graph, "x2", "s")
	mustRegister(t, graph, "n", "x2", "x1")
	impacts, err = Impacts(graph, "s")
	if err != nil {
		t.Fatalf("Impacts(s): %v", err)
	}
	assertImpact(t, impacts, "n", 2, []string{"s", "x1", "n"})

	// A longer-looking but lexicographically smaller route must not replace a
	// genuinely shorter path: distance takes priority. q joins at distance 2 via
	// z, and at distance 3 via a -> m.
	mustRegister(t, graph, "q", "m", "z")
	impacts, err = Impacts(graph, "s")
	if err != nil {
		t.Fatalf("Impacts(s): %v", err)
	}
	assertImpact(t, impacts, "q", 2, []string{"s", "z", "q"})
}

// Same-distance results are ordered by dataset name regardless of the order in
// which children were registered.
func TestImpactsOrderedByDistanceThenName(t *testing.T) {
	graph := map[string]*Lineage{}
	mustRegister(t, graph, "root")
	mustRegister(t, graph, "zebra", "root")
	mustRegister(t, graph, "alpha", "root")
	mustRegister(t, graph, "mid", "root")
	mustRegister(t, graph, "leaf-z", "zebra")
	mustRegister(t, graph, "leaf-a", "alpha")

	impacts, err := Impacts(graph, "root")
	if err != nil {
		t.Fatalf("Impacts(root): %v", err)
	}
	want := []struct {
		name string
		dist int
	}{
		{"alpha", 1}, {"mid", 1}, {"zebra", 1},
		{"leaf-a", 2}, {"leaf-z", 2},
	}
	if len(impacts) != len(want) {
		t.Fatalf("got %d impacts %v, want %d", len(impacts), impactNames(impacts), len(want))
	}
	for i, w := range want {
		if impacts[i].Dataset != w.name || impacts[i].Distance != w.dist {
			t.Fatalf("position %d = (%s,%d), want (%s,%d); full=%v",
				i, impacts[i].Dataset, impacts[i].Distance, w.name, w.dist, impacts)
		}
	}
}

// A registered origin without downstreams succeeds with an empty (but non-nil)
// list; independent datasets and the origin itself never appear.
func TestImpactsEmptyAndScope(t *testing.T) {
	graph := map[string]*Lineage{}
	mustRegister(t, graph, "lonely")
	mustRegister(t, graph, "other")
	mustRegister(t, graph, "p")
	mustRegister(t, graph, "c", "p")

	impacts, err := Impacts(graph, "lonely")
	if err != nil {
		t.Fatalf("Impacts(lonely): %v", err)
	}
	if impacts == nil || len(impacts) != 0 {
		t.Fatalf("want empty non-nil list, got %v", impacts)
	}

	impacts, err = Impacts(graph, "p")
	if err != nil {
		t.Fatalf("Impacts(p): %v", err)
	}
	if got, want := impactNames(impacts), []string{"c"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("independent datasets leaked: got %v, want %v", got, want)
	}
	for _, im := range impacts {
		for _, step := range im.Path {
			if step == "p" && im.Dataset == "p" {
				t.Fatal("origin must not appear in its own impact list")
			}
		}
	}
}

func TestImpactsErrors(t *testing.T) {
	graph := map[string]*Lineage{}
	mustRegister(t, graph, "a")

	impacts, err := Impacts(graph, "")
	if err == nil || !strings.Contains(err.Error(), "name is required") {
		t.Fatalf("empty origin: want required-name error, got %v", err)
	}
	if impacts != nil {
		t.Fatalf("empty origin: want nil results, got %v", impacts)
	}

	impacts, err = Impacts(graph, "ghost")
	if err == nil || !strings.Contains(err.Error(), "ghost") {
		t.Fatalf("unknown origin: want error naming ghost, got %v", err)
	}
	if impacts != nil {
		t.Fatalf("unknown origin: want nil results, got %v", impacts)
	}

	// Non-empty name against an initialized-but-empty graph, and against nil.
	impacts, err = Impacts(map[string]*Lineage{}, "ghost")
	if err == nil || !strings.Contains(err.Error(), "ghost") || impacts != nil {
		t.Fatalf("empty graph: want naming error and nil results, got %v, %v", impacts, err)
	}
	impacts, err = Impacts(nil, "ghost")
	if err == nil || !strings.Contains(err.Error(), "ghost") || impacts != nil {
		t.Fatalf("nil graph: want naming error and nil results, got %v, %v", impacts, err)
	}
}

// Names match by exact registered value.
func TestImpactsExactNameMatch(t *testing.T) {
	graph := map[string]*Lineage{}
	mustRegister(t, graph, "raw")
	mustRegister(t, graph, "RAW", "raw")

	if _, err := Impacts(graph, "RaW"); err == nil || !strings.Contains(err.Error(), "RaW") {
		t.Fatalf("case-insensitive match: want error naming RaW, got %v", err)
	}
	impacts, err := Impacts(graph, "raw")
	if err != nil {
		t.Fatalf("Impacts(raw): %v", err)
	}
	assertImpact(t, impacts, "RAW", 1, []string{"raw", "RAW"})
}

// A query never mutates the graph, and mutating the returned list or paths
// cannot reach back into the graph.
func TestImpactsReadOnlyAndIsolated(t *testing.T) {
	graph := map[string]*Lineage{}
	mustRegister(t, graph, "raw")
	mustRegister(t, graph, "a", "raw")
	mustRegister(t, graph, "b", "raw")
	mustRegister(t, graph, "report", "a", "b")
	before := snapshot(graph)

	impacts, err := Impacts(graph, "raw")
	if err != nil {
		t.Fatalf("Impacts(raw): %v", err)
	}
	if !reflect.DeepEqual(snapshot(graph), before) {
		t.Fatalf("query changed graph: before=%v after=%v", before, snapshot(graph))
	}

	// Abuse the returned slices, then confirm the graph is still untouched.
	impacts[0].Path[0] = "tampered"
	impacts[0].Path = append(impacts[0].Path, "extra")
	impacts[0].Dataset = "tampered"
	impacts[0].Distance = 99
	if !reflect.DeepEqual(snapshot(graph), before) {
		t.Fatalf("mutating results changed graph: before=%v after=%v", before, snapshot(graph))
	}

	// A failed query is also read-only.
	if _, err := Impacts(graph, "missing"); err == nil {
		t.Fatal("expected not-found error")
	}
	if !reflect.DeepEqual(snapshot(graph), before) {
		t.Fatalf("failed query changed graph: before=%v after=%v", before, snapshot(graph))
	}
	assertConsistent(t, graph)
}

type snap struct {
	parents  map[string][]string
	children map[string][]string
}

func snapshot(graph map[string]*Lineage) snap {
	s := snap{map[string][]string{}, map[string][]string{}}
	for name, e := range graph {
		s.parents[name] = append([]string(nil), e.Parents...)
		s.children[name] = append([]string(nil), e.Children...)
	}
	return s
}
