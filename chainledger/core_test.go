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

// A derived dataset already has its own downstream; a request mixing an
// independent, non-cyclic source with that downstream must be refused as a
// whole, naming the downstream that closes the multi-level loop. Shared
// ancestry between candidate upstreams must neither hide that loop nor turn a
// benign merge into one.
func TestRegisterIndependentSourceMixedWithOwnDownstreamRejected(t *testing.T) {
	graph := map[string]*Lineage{}
	mustRegister(t, graph, "src")
	mustRegister(t, graph, "extra")
	mustRegister(t, graph, "mid", "src")
	mustRegister(t, graph, "leaf", "mid")
	before := snapshot(graph)

	// Independent source first, cyclic own-downstream second.
	err := Register(graph, Dataset{Name: "mid"}, []string{"extra", "leaf"})
	if err == nil || !strings.Contains(err.Error(), "cycle") || !strings.Contains(err.Error(), "leaf") {
		t.Fatalf("want cycle error naming leaf, got %v", err)
	}
	if !reflect.DeepEqual(snapshot(graph), before) {
		t.Fatalf("rejected registration changed graph: before=%v after=%v", before, snapshot(graph))
	}
	assertEntry(t, graph, "mid", []string{"src"}, []string{"leaf"})
	assertEntry(t, graph, "extra", nil, nil)
	assertConsistent(t, graph)

	// The same offending downstream listed first reports the same named error;
	// input order must not swap it for any other complaint.
	err = Register(graph, Dataset{Name: "mid"}, []string{"leaf", "extra"})
	if err == nil || !strings.Contains(err.Error(), "cycle") || !strings.Contains(err.Error(), "leaf") {
		t.Fatalf("want cycle error naming leaf, got %v", err)
	}
	if !reflect.DeepEqual(snapshot(graph), before) {
		t.Fatalf("rejected registration changed graph: before=%v after=%v", before, snapshot(graph))
	}

	// Without the cyclic upstream the replacement succeeds, keeping mid's own
	// downstream leaf and gaining no new child for the refused attempts.
	mustRegister(t, graph, "mid", "extra")
	assertEntry(t, graph, "mid", []string{"extra"}, []string{"leaf"})
	assertEntry(t, graph, "extra", nil, []string{"mid"})
	assertEntry(t, graph, "src", nil, nil)
	assertConsistent(t, graph)

	// Later registrations judge cycles against the current edges, not any
	// conclusion cached before mid moved: extra -> mid now closes a loop.
	if err := Register(graph, Dataset{Name: "extra"}, []string{"mid"}); err == nil ||
		!strings.Contains(err.Error(), "cycle") || !strings.Contains(err.Error(), "mid") {
		t.Fatalf("want cycle error naming mid under the new edges, got %v", err)
	}
}

// The ancestor probe resolves shared lineage once per registration: several
// candidate starts that come from the same source and merge along the way reuse
// cached conclusions instead of re-walking the common nodes.
func TestAncestorProbeSharesWorkAcrossCandidates(t *testing.T) {
	graph := map[string]*Lineage{}
	mustRegister(t, graph, "src")
	mustRegister(t, graph, "a", "src")
	mustRegister(t, graph, "b", "src")
	mustRegister(t, graph, "merge", "a", "b")
	mustRegister(t, graph, "other")

	probe := newAncestorProbe(graph, "src")
	if !probe.canReach("merge") {
		t.Fatal("merge should reach src through either branch")
	}
	// merge's walk already resolved a and b; asking about them again must be
	// answered from the cache, adding no new traversal.
	if !probe.canReach("a") || !probe.canReach("b") {
		t.Fatal("memoized answers disagree: a and b both reach src")
	}
	if probe.canReach("other") {
		t.Fatal("other does not reach src")
	}
	if got, want := len(probe.resolved), 4; got != want {
		t.Fatalf("resolved %d nodes %v, want %d (merge,a,b,other); shared ancestry must not be re-walked",
			got, probe.resolved, want)
	}

	// A node disconnected from the target probed from two starts that merge on
	// a common non-reaching ancestor is likewise traversed once.
	probe = newAncestorProbe(graph, "other")
	if probe.canReach("merge") || probe.canReach("a") || probe.canReach("b") {
		t.Fatal("nothing derived from src reaches other")
	}
	if got, want := len(probe.resolved), 4; got != want {
		t.Fatalf("resolved %d nodes %v, want %d (merge,a,b,src)", got, probe.resolved, want)
	}
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

func mustImpacts(t *testing.T, graph map[string]*Lineage, origin string) []Impact {
	t.Helper()
	impacts, err := Impacts(graph, origin)
	if err != nil {
		t.Fatalf("Impacts(%s): %v", origin, err)
	}
	return impacts
}

// assertImpactOnce checks the entry for name and that name appears exactly once
// in the result, even where several branches merge into it.
func assertImpactOnce(t *testing.T, impacts []Impact, name string, wantDistance int, wantPath []string) {
	t.Helper()
	count := 0
	for _, im := range impacts {
		if im.Dataset == name {
			count++
		}
	}
	if count > 1 {
		t.Fatalf("impact %q appears %d times in %v, want exactly once", name, count, impactNames(impacts))
	}
	assertImpact(t, impacts, name, wantDistance, wantPath)
}

// Re-wiring report from [a b] to [b] removes the short raw -> a -> report
// path, but the longer raw -> a -> b -> report path still connects everything.
// Distances and explanation paths must be recomputed under the current edges,
// not inherited from before the re-wire.
func TestImpactsAfterRewireLongerPathSurvives(t *testing.T) {
	graph := map[string]*Lineage{}
	mustRegister(t, graph, "raw")
	mustRegister(t, graph, "a", "raw")
	mustRegister(t, graph, "b", "a")
	mustRegister(t, graph, "report", "a", "b")
	mustRegister(t, graph, "view", "report")

	before := mustImpacts(t, graph, "raw")
	assertImpact(t, before, "report", 2, []string{"raw", "a", "report"})
	assertImpact(t, before, "view", 3, []string{"raw", "a", "report", "view"})

	// Drop the direct a -> report edge; only the route through b remains.
	mustRegister(t, graph, "report", "b")
	assertConsistent(t, graph)

	impacts := mustImpacts(t, graph, "raw")
	if got, want := impactNames(impacts), []string{"a", "b", "report", "view"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("impact order = %v, want %v", got, want)
	}
	assertImpactOnce(t, impacts, "report", 3, []string{"raw", "a", "b", "report"})
	assertImpactOnce(t, impacts, "view", 4, []string{"raw", "a", "b", "report", "view"})

	// From the dropped upstream a, report was distance 1 before; now only the
	// longer current path may be reported.
	fromOld := mustImpacts(t, graph, "a")
	if got, want := impactNames(fromOld), []string{"b", "report", "view"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("impacts from old upstream = %v, want %v", got, want)
	}
	assertImpactOnce(t, fromOld, "report", 2, []string{"a", "b", "report"})
	assertImpactOnce(t, fromOld, "view", 3, []string{"a", "b", "report", "view"})
}

// When the re-wire severs the last lineage path from the old source, the
// re-wired dataset and the downstreams reachable only through it leave the old
// source's scope together, while the old source's other branches still return.
// The new source sees the moved subtree with paths rooted at itself.
func TestImpactsAfterRewireLastPathRemoved(t *testing.T) {
	graph := map[string]*Lineage{}
	mustRegister(t, graph, "raw")
	mustRegister(t, graph, "mid", "raw")
	mustRegister(t, graph, "leaf", "mid")
	mustRegister(t, graph, "side", "raw")
	mustRegister(t, graph, "sideleaf", "side")
	mustRegister(t, graph, "newsrc")

	// mid moves from raw to newsrc, keeping its own downstream leaf.
	mustRegister(t, graph, "mid", "newsrc")
	assertConsistent(t, graph)

	fromOld := mustImpacts(t, graph, "raw")
	if got, want := impactNames(fromOld), []string{"side", "sideleaf"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("impacts from old source = %v, want %v (mid and leaf must be gone)", got, want)
	}
	assertImpact(t, fromOld, "side", 1, []string{"raw", "side"})
	assertImpact(t, fromOld, "sideleaf", 2, []string{"raw", "side", "sideleaf"})

	fromNew := mustImpacts(t, graph, "newsrc")
	if got, want := impactNames(fromNew), []string{"mid", "leaf"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("impacts from new source = %v, want %v", got, want)
	}
	assertImpactOnce(t, fromNew, "mid", 1, []string{"newsrc", "mid"})
	assertImpactOnce(t, fromNew, "leaf", 2, []string{"newsrc", "mid", "leaf"})

	// The moved subtree is still intact when queried from itself.
	fromMid := mustImpacts(t, graph, "mid")
	assertImpact(t, fromMid, "leaf", 1, []string{"mid", "leaf"})

	// Cutting mid loose entirely (no upstreams) empties the new source's scope:
	// the source remains a valid origin and answers with an empty list.
	mustRegister(t, graph, "mid")
	assertConsistent(t, graph)
	impacts, err := Impacts(graph, "newsrc")
	if err != nil {
		t.Fatalf("Impacts(newsrc) with no remaining downstream: %v", err)
	}
	if impacts == nil || len(impacts) != 0 {
		t.Fatalf("want empty non-nil list from newsrc, got %v", impacts)
	}
	if got, want := impactNames(mustImpacts(t, graph, "raw")), []string{"side", "sideleaf"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("old source other branches = %v, want %v", got, want)
	}
}

// An old source whose only downstream was re-wired away stays a legal query
// origin and returns a successful empty list.
func TestImpactsOldSourceLeftWithNoDownstream(t *testing.T) {
	graph := map[string]*Lineage{}
	mustRegister(t, graph, "old")
	mustRegister(t, graph, "solo", "old")
	mustRegister(t, graph, "newsrc")

	mustRegister(t, graph, "solo", "newsrc")
	assertConsistent(t, graph)

	impacts, err := Impacts(graph, "old")
	if err != nil {
		t.Fatalf("Impacts(old) after losing its only downstream: %v", err)
	}
	if impacts == nil || len(impacts) != 0 {
		t.Fatalf("want empty non-nil list from old, got %v", impacts)
	}
	assertImpact(t, mustImpacts(t, graph, "newsrc"), "solo", 1, []string{"newsrc", "solo"})
}

// A merge node reachable over two equal paths from the same origin: removing
// one of them must not remove the dataset (and it still appears exactly once),
// while removing the last one must drop it and its exclusive downstream.
func TestImpactsRewireDiamondOnePathRemovedVsAllLost(t *testing.T) {
	graph := map[string]*Lineage{}
	mustRegister(t, graph, "old")
	mustRegister(t, graph, "p1", "old")
	mustRegister(t, graph, "p2", "old")
	mustRegister(t, graph, "joined", "p1", "p2")
	mustRegister(t, graph, "down", "joined")
	mustRegister(t, graph, "outside")

	before := mustImpacts(t, graph, "old")
	assertImpactOnce(t, before, "joined", 2, []string{"old", "p1", "joined"})
	assertImpactOnce(t, before, "down", 3, []string{"old", "p1", "joined", "down"})

	// Delete one of the two paths: joined survives at the same distance via p2.
	mustRegister(t, graph, "joined", "p2")
	assertConsistent(t, graph)
	oneLeft := mustImpacts(t, graph, "old")
	if got, want := impactNames(oneLeft), []string{"p1", "p2", "joined", "down"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("after dropping one path, impacts = %v, want %v", got, want)
	}
	assertImpactOnce(t, oneLeft, "joined", 2, []string{"old", "p2", "joined"})
	assertImpactOnce(t, oneLeft, "down", 3, []string{"old", "p2", "joined", "down"})

	// Delete the last path: joined and its exclusive downstream down leave the
	// old source's scope; the p1/p2 branches remain.
	mustRegister(t, graph, "joined", "outside")
	assertConsistent(t, graph)
	noneLeft := mustImpacts(t, graph, "old")
	if got, want := impactNames(noneLeft), []string{"p1", "p2"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("after dropping all paths, impacts = %v, want %v", got, want)
	}
	fromOutside := mustImpacts(t, graph, "outside")
	assertImpactOnce(t, fromOutside, "joined", 1, []string{"outside", "joined"})
	assertImpactOnce(t, fromOutside, "down", 2, []string{"outside", "joined", "down"})
}

// After a re-wire leaves two equal-length shortest paths, the lexicographically
// smallest full name sequence wins regardless of the stored parent list order,
// and neither registration order nor upstream list order affects the result.
func TestImpactsRewireTieBreakAndOrderInvariance(t *testing.T) {
	build := func(parents ...string) map[string]*Lineage {
		graph := map[string]*Lineage{}
		mustRegister(t, graph, "o")
		mustRegister(t, graph, "x2", "o") // larger name registered first on purpose
		mustRegister(t, graph, "x1", "o")
		mustRegister(t, graph, "d", parents...)
		mustRegister(t, graph, "e", "d")
		return graph
	}

	// d ends up with parents [x2 x1] (list order disagreeing with name order)
	// either by direct registration or by re-wiring from a single parent.
	direct := build("x2", "x1")
	rewired := build("x1")
	mustRegister(t, rewired, "d", "x2", "x1")
	assertConsistent(t, rewired)

	want := mustImpacts(t, direct, "o")
	assertImpactOnce(t, want, "d", 2, []string{"o", "x1", "d"})
	assertImpactOnce(t, want, "e", 3, []string{"o", "x1", "d", "e"})
	if got, wantNames := impactNames(want), []string{"x1", "x2", "d", "e"}; !reflect.DeepEqual(got, wantNames) {
		t.Fatalf("impact order = %v, want %v", got, wantNames)
	}

	if got := mustImpacts(t, rewired, "o"); !reflect.DeepEqual(got, want) {
		t.Fatalf("re-wired graph impacts = %v, want %v (registration history must not matter)", got, want)
	}

	// Same final graph, but children registered in the opposite order and the
	// upstream list flipped: the query result must be identical.
	flipped := map[string]*Lineage{}
	mustRegister(t, flipped, "o")
	mustRegister(t, flipped, "x1", "o")
	mustRegister(t, flipped, "x2", "o")
	mustRegister(t, flipped, "d", "x1", "x2")
	mustRegister(t, flipped, "e", "d")
	if got := mustImpacts(t, flipped, "o"); !reflect.DeepEqual(got, want) {
		t.Fatalf("flipped build impacts = %v, want %v (registration and list order must not matter)", got, want)
	}
}

// buildRegisteredGraph applies a sequence of [name, parents...] registrations
// in the given order, failing the test if any registration is rejected.
func buildRegisteredGraph(t *testing.T, registrations [][]string) map[string]*Lineage {
	t.Helper()
	graph := map[string]*Lineage{}
	for _, r := range registrations {
		mustRegister(t, graph, r[0], r[1:]...)
	}
	return graph
}

// Regression for a multi-path merge where comparing the full explanation path
// and comparing only the merge node's direct upstream name give different
// answers. source feeds a and b; a feeds z; b feeds c; report depends on z and
// c together (c declared ahead of z on purpose); view depends on report. From
// source both source->a->z->report and source->b->c->report have length 3. A
// rule that only compares the final upstream would wrongly prefer the c route
// (c < z); the full-path rule must compare from the origin and pick the a
// route, because the paths first differ at hop 1 where a < b. view inherits
// that explanation one hop further on.
func TestImpactsMergedBranchesFullPathTieBreak(t *testing.T) {
	// Each sequence builds the same graph with a legal but different
	// registration order; the second also flips report's upstream list, and the
	// third registers the isolated node early. Names, distances and paths must
	// come out identical regardless.
	sequences := [][][]string{
		{
			{"source"},
			{"b", "source"}, // b-side branch registered first
			{"a", "source"},
			{"c", "b"},
			{"z", "a"},
			{"report", "c", "z"}, // c listed ahead of z on purpose
			{"view", "report"},
			{"isolated"},
		},
		{
			{"source"},
			{"a", "source"}, // opposite branch registered first
			{"z", "a"},
			{"b", "source"},
			{"c", "b"},
			{"report", "z", "c"}, // direct-upstream order flipped
			{"view", "report"},
			{"isolated"},
		},
		{
			{"source"},
			{"isolated"},
			{"b", "source"},
			{"a", "source"},
			{"z", "a"},
			{"c", "b"},
			{"report", "c", "z"},
			{"view", "report"},
		},
	}

	want := []Impact{
		{Dataset: "a", Distance: 1, Path: []string{"source", "a"}},
		{Dataset: "b", Distance: 1, Path: []string{"source", "b"}},
		{Dataset: "c", Distance: 2, Path: []string{"source", "b", "c"}},
		{Dataset: "z", Distance: 2, Path: []string{"source", "a", "z"}},
		{Dataset: "report", Distance: 3, Path: []string{"source", "a", "z", "report"}},
		{Dataset: "view", Distance: 4, Path: []string{"source", "a", "z", "report", "view"}},
	}

	for i, seq := range sequences {
		graph := buildRegisteredGraph(t, seq)
		assertConsistent(t, graph)
		before := snapshot(graph)

		impacts := mustImpacts(t, graph, "source")
		if !reflect.DeepEqual(impacts, want) {
			t.Fatalf("sequence %d: impacts = %v, want %v", i, impacts, want)
		}

		// Anchors with explicit messages: the merge node is explained exactly
		// once through a/z even though c sorts ahead of z, and view continues
		// along that same route.
		assertImpactOnce(t, impacts, "report", 3, []string{"source", "a", "z", "report"})
		assertImpactOnce(t, impacts, "view", 4, []string{"source", "a", "z", "report", "view"})

		// The origin itself and an unconnected dataset never appear.
		if names := impactNames(impacts); slices.Contains(names, "source") || slices.Contains(names, "isolated") {
			t.Fatalf("sequence %d: origin or isolated dataset leaked into %v", i, names)
		}

		// The query changes no node, relationship or stored list order.
		if !reflect.DeepEqual(snapshot(graph), before) {
			t.Fatalf("sequence %d: query changed graph: before=%v after=%v", i, before, snapshot(graph))
		}
	}
}

// With a direct source->report edge added on top of the merge scenario, edge
// count takes priority over lexicographic order: report shortens to distance 1
// and view to 2, both via the new edge, and the lexicographically smaller
// length-3 route through a must not survive. The a/z and b/c branches remain
// reachable on their own. The new upstream's position in report's declared
// list must not change the result.
func TestImpactsMergedBranchesDirectEdgeShortensMerge(t *testing.T) {
	// Two builds with opposite registration and upstream-list orders; the
	// direct edge is inserted at a different position in each.
	builds := []struct {
		registrations [][]string
		newParents    []string
	}{
		{
			registrations: [][]string{
				{"source"},
				{"b", "source"},
				{"a", "source"},
				{"c", "b"},
				{"z", "a"},
				{"report", "c", "z"},
				{"view", "report"},
				{"isolated"},
			},
			newParents: []string{"source", "c", "z"}, // direct edge first
		},
		{
			registrations: [][]string{
				{"source"},
				{"a", "source"},
				{"z", "a"},
				{"b", "source"},
				{"c", "b"},
				{"report", "z", "c"},
				{"view", "report"},
				{"isolated"},
			},
			newParents: []string{"c", "z", "source"}, // direct edge last
		},
	}

	want := []Impact{
		{Dataset: "a", Distance: 1, Path: []string{"source", "a"}},
		{Dataset: "b", Distance: 1, Path: []string{"source", "b"}},
		{Dataset: "report", Distance: 1, Path: []string{"source", "report"}},
		{Dataset: "c", Distance: 2, Path: []string{"source", "b", "c"}},
		{Dataset: "view", Distance: 2, Path: []string{"source", "report", "view"}},
		{Dataset: "z", Distance: 2, Path: []string{"source", "a", "z"}},
	}

	for i, build := range builds {
		graph := buildRegisteredGraph(t, build.registrations)
		mustRegister(t, graph, "report", build.newParents...)
		assertConsistent(t, graph)
		before := snapshot(graph)

		impacts := mustImpacts(t, graph, "source")
		if !reflect.DeepEqual(impacts, want) {
			t.Fatalf("build %d: impacts = %v, want %v", i, impacts, want)
		}

		// The short route wins even though the old a-route is lexicographically
		// smaller than any other length-3 explanation; both appear once.
		assertImpactOnce(t, impacts, "report", 1, []string{"source", "report"})
		assertImpactOnce(t, impacts, "view", 2, []string{"source", "report", "view"})

		// Both merged branches are still listed as affected datasets.
		assertImpact(t, impacts, "z", 2, []string{"source", "a", "z"})
		assertImpact(t, impacts, "c", 2, []string{"source", "b", "c"})

		if names := impactNames(impacts); slices.Contains(names, "source") || slices.Contains(names, "isolated") {
			t.Fatalf("build %d: origin or isolated dataset leaked into %v", i, names)
		}
		if !reflect.DeepEqual(snapshot(graph), before) {
			t.Fatalf("build %d: query changed graph: before=%v after=%v", i, before, snapshot(graph))
		}
	}
}

// A rejected re-wire (unknown upstream, or an upstream that would close a
// cycle through the dataset's own downstream) must name the offending upstream
// and leave every previously queryable relationship exactly as it was.
func TestFailedRewireLeavesImpactsUntouched(t *testing.T) {
	graph := map[string]*Lineage{}
	mustRegister(t, graph, "raw")
	mustRegister(t, graph, "mid", "raw")
	mustRegister(t, graph, "leaf", "mid")
	mustRegister(t, graph, "newsrc")
	mustRegister(t, graph, "newleaf", "newsrc")

	rawBefore := mustImpacts(t, graph, "raw")
	newBefore := mustImpacts(t, graph, "newsrc")
	before := snapshot(graph)

	// Unknown upstream: the error names the missing dataset.
	err := Register(graph, Dataset{Name: "mid"}, []string{"newsrc", "ghost"})
	if err == nil || !strings.Contains(err.Error(), "ghost") {
		t.Fatalf("want unknown-parent error naming ghost, got %v", err)
	}

	// Cyclic upstream: pointing mid at its own downstream leaf must name leaf.
	err = Register(graph, Dataset{Name: "mid"}, []string{"leaf"})
	if err == nil || !strings.Contains(err.Error(), "cycle") || !strings.Contains(err.Error(), "leaf") {
		t.Fatalf("want cycle error naming leaf, got %v", err)
	}

	if !reflect.DeepEqual(snapshot(graph), before) {
		t.Fatalf("rejected re-wires changed graph: before=%v after=%v", before, snapshot(graph))
	}
	if got := mustImpacts(t, graph, "raw"); !reflect.DeepEqual(got, rawBefore) {
		t.Fatalf("raw impacts changed after rejected re-wires: got %v, want %v", got, rawBefore)
	}
	if got := mustImpacts(t, graph, "newsrc"); !reflect.DeepEqual(got, newBefore) {
		t.Fatalf("newsrc impacts changed after rejected re-wires: got %v, want %v", got, newBefore)
	}
	assertImpact(t, rawBefore, "mid", 1, []string{"raw", "mid"})
	assertImpact(t, rawBefore, "leaf", 2, []string{"raw", "mid", "leaf"})
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

func upstreamNames(upstreams []Upstream) []string {
	names := make([]string, len(upstreams))
	for i, up := range upstreams {
		names[i] = up.Dataset
	}
	return names
}

func assertUpstream(t *testing.T, upstreams []Upstream, name string, wantDistance int, wantPath []string) {
	t.Helper()
	for _, up := range upstreams {
		if up.Dataset != name {
			continue
		}
		if up.Distance != wantDistance {
			t.Errorf("upstream %s distance = %d, want %d", name, up.Distance, wantDistance)
		}
		if !sameStrings(up.Path, wantPath) {
			t.Errorf("upstream %s path = %v, want %v", name, up.Path, wantPath)
		}
		return
	}
	t.Fatalf("upstream %q missing from %v", name, upstreamNames(upstreams))
}

func mustUpstreams(t *testing.T, graph map[string]*Lineage, target string) []Upstream {
	t.Helper()
	upstreams, err := Upstreams(graph, target)
	if err != nil {
		t.Fatalf("Upstreams(%s): %v", target, err)
	}
	return upstreams
}

// assertUpstreamOnce checks the entry for name and that name appears exactly
// once in the result, even where it feeds the target through several branches.
func assertUpstreamOnce(t *testing.T, upstreams []Upstream, name string, wantDistance int, wantPath []string) {
	t.Helper()
	count := 0
	for _, up := range upstreams {
		if up.Dataset == name {
			count++
		}
	}
	if count > 1 {
		t.Fatalf("upstream %q appears %d times in %v, want exactly once", name, count, upstreamNames(upstreams))
	}
	assertUpstream(t, upstreams, name, wantDistance, wantPath)
}

// The provenance counterpart of the downstream merge scenario: source feeds a
// and b; a feeds z; b feeds c; report depends on z and c together. Querying
// report walks the parent edges, so source appears once at distance 3. Both
// source->a->z->report and source->b->c->report have length 3; a rule that
// only compares report's direct upstreams would wrongly prefer the c route
// (c < z), but the full-path rule compares from the upstream onward and picks
// the a route, because the paths first differ at hop 1 where a < b.
func TestUpstreamsMergedBranchesFullPathTieBreak(t *testing.T) {
	// Each sequence builds the same graph with a legal but different
	// registration order; the second also flips report's upstream list. Names,
	// distances and paths must come out identical regardless.
	sequences := [][][]string{
		{
			{"source"},
			{"b", "source"}, // b-side branch registered first
			{"a", "source"},
			{"c", "b"},
			{"z", "a"},
			{"report", "c", "z"}, // c listed ahead of z on purpose
			{"view", "report"},
			{"isolated"},
		},
		{
			{"source"},
			{"a", "source"}, // opposite branch registered first
			{"z", "a"},
			{"b", "source"},
			{"c", "b"},
			{"report", "z", "c"}, // direct-upstream order flipped
			{"view", "report"},
			{"isolated"},
		},
	}

	want := []Upstream{
		{Dataset: "c", Distance: 1, Path: []string{"c", "report"}},
		{Dataset: "z", Distance: 1, Path: []string{"z", "report"}},
		{Dataset: "a", Distance: 2, Path: []string{"a", "z", "report"}},
		{Dataset: "b", Distance: 2, Path: []string{"b", "c", "report"}},
		{Dataset: "source", Distance: 3, Path: []string{"source", "a", "z", "report"}},
	}

	for i, seq := range sequences {
		graph := buildRegisteredGraph(t, seq)
		assertConsistent(t, graph)
		before := snapshot(graph)

		upstreams := mustUpstreams(t, graph, "report")
		if !reflect.DeepEqual(upstreams, want) {
			t.Fatalf("sequence %d: upstreams = %v, want %v", i, upstreams, want)
		}

		// Anchor with an explicit message: the shared source is explained
		// through a/z even though c sorts ahead of z.
		assertUpstreamOnce(t, upstreams, "source", 3, []string{"source", "a", "z", "report"})

		// The target itself, its downstream, and an unconnected dataset never
		// appear.
		names := upstreamNames(upstreams)
		if slices.Contains(names, "report") || slices.Contains(names, "view") || slices.Contains(names, "isolated") {
			t.Fatalf("sequence %d: target, downstream or isolated dataset leaked into %v", i, names)
		}

		// The query changes no node, relationship or stored list order.
		if !reflect.DeepEqual(snapshot(graph), before) {
			t.Fatalf("sequence %d: query changed graph: before=%v after=%v", i, before, snapshot(graph))
		}
	}
}

// With a direct source->report edge added on top of the merge scenario, edge
// count takes priority over lexicographic order: source shortens to distance 1
// with path [source report], and the length-3 route through a must not
// survive. The direct edge's position in report's declared upstream list must
// not change the result.
func TestUpstreamsMergedBranchesDirectEdgeShortensSource(t *testing.T) {
	builds := [][]string{
		{"source", "c", "z"}, // direct edge first
		{"c", "z", "source"}, // direct edge last
	}

	want := []Upstream{
		{Dataset: "c", Distance: 1, Path: []string{"c", "report"}},
		{Dataset: "source", Distance: 1, Path: []string{"source", "report"}},
		{Dataset: "z", Distance: 1, Path: []string{"z", "report"}},
		{Dataset: "a", Distance: 2, Path: []string{"a", "z", "report"}},
		{Dataset: "b", Distance: 2, Path: []string{"b", "c", "report"}},
	}

	for i, newParents := range builds {
		graph := buildRegisteredGraph(t, [][]string{
			{"source"},
			{"b", "source"},
			{"a", "source"},
			{"c", "b"},
			{"z", "a"},
			{"report", "c", "z"},
		})
		mustRegister(t, graph, "report", newParents...)
		assertConsistent(t, graph)

		upstreams := mustUpstreams(t, graph, "report")
		if !reflect.DeepEqual(upstreams, want) {
			t.Fatalf("build %d: upstreams = %v, want %v", i, upstreams, want)
		}
		assertUpstreamOnce(t, upstreams, "source", 1, []string{"source", "report"})
	}
}

// A registered target without upstreams succeeds with an empty (but non-nil)
// list; the target's own downstreams and independent datasets never appear.
func TestUpstreamsEmptyAndScope(t *testing.T) {
	graph := map[string]*Lineage{}
	mustRegister(t, graph, "lonely")
	mustRegister(t, graph, "other")
	mustRegister(t, graph, "p")
	mustRegister(t, graph, "c", "p")

	upstreams, err := Upstreams(graph, "lonely")
	if err != nil {
		t.Fatalf("Upstreams(lonely): %v", err)
	}
	if upstreams == nil || len(upstreams) != 0 {
		t.Fatalf("want empty non-nil list, got %v", upstreams)
	}

	// A root has no upstreams even though it has a downstream.
	upstreams, err = Upstreams(graph, "p")
	if err != nil {
		t.Fatalf("Upstreams(p): %v", err)
	}
	if upstreams == nil || len(upstreams) != 0 {
		t.Fatalf("want empty non-nil list for root p, got %v", upstreams)
	}

	// The leaf sees only its own upstream chain, not unrelated datasets.
	upstreams = mustUpstreams(t, graph, "c")
	if got, want := upstreamNames(upstreams), []string{"p"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("independent datasets leaked: got %v, want %v", got, want)
	}
	assertUpstream(t, upstreams, "p", 1, []string{"p", "c"})
}

func TestUpstreamsErrors(t *testing.T) {
	graph := map[string]*Lineage{}
	mustRegister(t, graph, "a")

	upstreams, err := Upstreams(graph, "")
	if err == nil || !strings.Contains(err.Error(), "name is required") {
		t.Fatalf("empty target: want required-name error, got %v", err)
	}
	if upstreams != nil {
		t.Fatalf("empty target: want nil results, got %v", upstreams)
	}

	upstreams, err = Upstreams(graph, "ghost")
	if err == nil || !strings.Contains(err.Error(), "ghost") {
		t.Fatalf("unknown target: want error naming ghost, got %v", err)
	}
	if upstreams != nil {
		t.Fatalf("unknown target: want nil results, got %v", upstreams)
	}

	// Non-empty name against an initialized-but-empty graph, and against nil.
	upstreams, err = Upstreams(map[string]*Lineage{}, "ghost")
	if err == nil || !strings.Contains(err.Error(), "ghost") || upstreams != nil {
		t.Fatalf("empty graph: want naming error and nil results, got %v, %v", upstreams, err)
	}
	upstreams, err = Upstreams(nil, "ghost")
	if err == nil || !strings.Contains(err.Error(), "ghost") || upstreams != nil {
		t.Fatalf("nil graph: want naming error and nil results, got %v, %v", upstreams, err)
	}
}

// Names match by exact registered value.
func TestUpstreamsExactNameMatch(t *testing.T) {
	graph := map[string]*Lineage{}
	mustRegister(t, graph, "raw")
	mustRegister(t, graph, "RAW", "raw")

	if _, err := Upstreams(graph, "RaW"); err == nil || !strings.Contains(err.Error(), "RaW") {
		t.Fatalf("case-insensitive match: want error naming RaW, got %v", err)
	}
	upstreams, err := Upstreams(graph, "RAW")
	if err != nil {
		t.Fatalf("Upstreams(RAW): %v", err)
	}
	assertUpstream(t, upstreams, "raw", 1, []string{"raw", "RAW"})
}

// A query never mutates the graph, and mutating the returned list or paths
// cannot reach back into the graph, into other records, or into later queries.
func TestUpstreamsReadOnlyAndIsolated(t *testing.T) {
	graph := map[string]*Lineage{}
	mustRegister(t, graph, "raw")
	mustRegister(t, graph, "a", "raw")
	mustRegister(t, graph, "b", "raw")
	mustRegister(t, graph, "report", "a", "b")
	before := snapshot(graph)

	upstreams, err := Upstreams(graph, "report")
	if err != nil {
		t.Fatalf("Upstreams(report): %v", err)
	}
	if !reflect.DeepEqual(snapshot(graph), before) {
		t.Fatalf("query changed graph: before=%v after=%v", before, snapshot(graph))
	}

	// Abuse the returned slices, then confirm the graph and a repeated query
	// are both untouched.
	upstreams[0].Path[0] = "tampered"
	upstreams[0].Path = append(upstreams[0].Path, "extra")
	upstreams[0].Dataset = "tampered"
	upstreams[0].Distance = 99
	if !reflect.DeepEqual(snapshot(graph), before) {
		t.Fatalf("mutating results changed graph: before=%v after=%v", before, snapshot(graph))
	}
	fresh := mustUpstreams(t, graph, "report")
	assertUpstream(t, fresh, "raw", 2, []string{"raw", "a", "report"})

	// A failed query is also read-only.
	if _, err := Upstreams(graph, "missing"); err == nil {
		t.Fatal("expected not-found error")
	}
	if !reflect.DeepEqual(snapshot(graph), before) {
		t.Fatalf("failed query changed graph: before=%v after=%v", before, snapshot(graph))
	}
	assertConsistent(t, graph)
}

// After report's direct upstreams are replaced by a same-name Register, a new
// query reflects the current edges, while the previously returned result keeps
// its original content.
func TestUpstreamsReflectReregisterAndOldResultStable(t *testing.T) {
	graph := map[string]*Lineage{}
	mustRegister(t, graph, "raw")
	mustRegister(t, graph, "a", "raw")
	mustRegister(t, graph, "b", "raw")
	mustRegister(t, graph, "report", "a", "b")

	before, err := Upstreams(graph, "report")
	if err != nil {
		t.Fatalf("Upstreams(report): %v", err)
	}
	assertUpstreamOnce(t, before, "raw", 2, []string{"raw", "a", "report"})
	beforeCopy := make([]Upstream, len(before))
	for i, up := range before {
		beforeCopy[i] = Upstream{up.Dataset, up.Distance, append([]string(nil), up.Path...)}
	}

	// report now depends on b only: a leaves its upstream set entirely, raw
	// remains reachable through b at distance 2 via the surviving route.
	mustRegister(t, graph, "report", "b")

	after, err := Upstreams(graph, "report")
	if err != nil {
		t.Fatalf("Upstreams(report) after re-register: %v", err)
	}
	if got, want := upstreamNames(after), []string{"b", "raw"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("upstream order = %v, want %v", got, want)
	}
	assertUpstreamOnce(t, after, "b", 1, []string{"b", "report"})
	assertUpstreamOnce(t, after, "raw", 2, []string{"raw", "b", "report"})

	if !reflect.DeepEqual(before, beforeCopy) {
		t.Fatalf("earlier result changed after re-register/new query: before=%v snapshot=%v", before, beforeCopy)
	}
}

// Regression for replacing a middle dataset's direct upstreams so that a
// source's shortest route to a report disappears while a longer route keeps the
// source participating. raw derives a and b; a derives z; b derives c; middle
// depends on raw, z and c; report depends on middle. Initially raw reaches
// report at distance 2 via raw -> middle -> report. Replacing middle's direct
// upstreams with [z c] deletes that short route, but raw still derives both
// branches, so it must remain listed exactly once, now at distance 4. The two
// surviving routes raw -> a -> z -> middle -> report and
// raw -> b -> c -> middle -> report have equal length; comparison starts at the
// source and goes name by name, so the a/z route wins even though c sorts ahead
// of z among middle's direct upstreams.
func TestUpstreamsAfterMiddleRewireLongerRouteSurvives(t *testing.T) {
	// Same final relationships built in different orders: the independent
	// branches may be registered in either order, and middle's upstream lists
	// may be declared in either order. Results must be identical.
	builds := []struct {
		name string
		seq  [][]string
	}{
		{
			name: "a-branch first, raw declared ahead of z/c",
			seq: [][]string{
				{"raw"},
				{"a", "raw"},
				{"z", "a"},
				{"b", "raw"},
				{"c", "b"},
				{"middle", "raw", "z", "c"},
				{"report", "middle"},
			},
		},
		{
			name: "b-branch first, raw declared last among middle's parents",
			seq: [][]string{
				{"raw"},
				{"b", "raw"},
				{"c", "b"},
				{"a", "raw"},
				{"z", "a"},
				{"middle", "c", "z", "raw"},
				{"report", "middle"},
			},
		},
		{
			name: "branches interleaved, raw placed between z and c",
			seq: [][]string{
				{"raw"},
				{"a", "raw"},
				{"b", "raw"},
				{"z", "a"},
				{"c", "b"},
				{"middle", "z", "raw", "c"},
				{"report", "middle"},
			},
		},
	}

	initialWant := []Upstream{
		{Dataset: "middle", Distance: 1, Path: []string{"middle", "report"}},
		{Dataset: "c", Distance: 2, Path: []string{"c", "middle", "report"}},
		{Dataset: "raw", Distance: 2, Path: []string{"raw", "middle", "report"}},
		{Dataset: "z", Distance: 2, Path: []string{"z", "middle", "report"}},
		{Dataset: "a", Distance: 3, Path: []string{"a", "z", "middle", "report"}},
		{Dataset: "b", Distance: 3, Path: []string{"b", "c", "middle", "report"}},
	}

	// After the rewire: middle stays at 1, c/z stay at 2, a/b stay at 3, and raw
	// moves to distance 4 with the source-anchored tie broken through a/z.
	rewiredWant := []Upstream{
		{Dataset: "middle", Distance: 1, Path: []string{"middle", "report"}},
		{Dataset: "c", Distance: 2, Path: []string{"c", "middle", "report"}},
		{Dataset: "z", Distance: 2, Path: []string{"z", "middle", "report"}},
		{Dataset: "a", Distance: 3, Path: []string{"a", "z", "middle", "report"}},
		{Dataset: "b", Distance: 3, Path: []string{"b", "c", "middle", "report"}},
		{Dataset: "raw", Distance: 4, Path: []string{"raw", "a", "z", "middle", "report"}},
	}

	// Replacement orders for middle must not change the outcome either; c sorts
	// before z, so this deliberately tries both list directions.
	replacements := [][]string{{"z", "c"}, {"c", "z"}}

	for bi, build := range builds {
		for _, replacement := range replacements {
			t.Run(build.name+"/replace-"+strings.Join(replacement, ","), func(t *testing.T) {
				graph := buildRegisteredGraph(t, build.seq)
				assertConsistent(t, graph)

				beforeQuery := snapshot(graph)
				before := mustUpstreams(t, graph, "report")
				if !reflect.DeepEqual(before, initialWant) {
					t.Fatalf("build %d: initial upstreams = %v, want %v", bi, before, initialWant)
				}
				assertUpstreamOnce(t, before, "raw", 2, []string{"raw", "middle", "report"})
				// Querying changes no node, edge or list order.
				if !reflect.DeepEqual(snapshot(graph), beforeQuery) {
					t.Fatalf("initial query changed graph: before=%v after=%v", beforeQuery, snapshot(graph))
				}

				// Replace middle's direct upstreams with the same-name Register.
				mustRegister(t, graph, "middle", replacement...)
				assertConsistent(t, graph)
				assertEntry(t, graph, "middle", replacement, []string{"report"})
				assertEntry(t, graph, "report", []string{"middle"}, nil)
				// raw loses its direct edge to middle but keeps exactly its two
				// branches (their stored order depends on registration order).
				rawChildren := graph["raw"].Children
				if len(rawChildren) != 2 ||
					!slices.Contains(rawChildren, "a") || !slices.Contains(rawChildren, "b") {
					t.Fatalf("raw children = %v, want exactly {a, b} after losing middle", rawChildren)
				}

				after := mustUpstreams(t, graph, "report")
				if !reflect.DeepEqual(after, rewiredWant) {
					t.Fatalf("build %d replacement %v: upstreams = %v, want %v",
						bi, replacement, after, rewiredWant)
				}

				// The source neither disappears nor keeps the stale short path:
				// it appears once at the longer distance with the full-path
				// tie broken from the source (a < b at the first differing hop),
				// not by middle's direct-upstream order (c < z).
				assertUpstreamOnce(t, after, "raw", 4,
					[]string{"raw", "a", "z", "middle", "report"})
				for _, up := range after {
					if up.Dataset == "raw" && reflect.DeepEqual(up.Path,
						[]string{"raw", "middle", "report"}) {
						t.Fatalf("stale short path %v still reported for raw after rewire", up.Path)
					}
				}

				// report's own direct dependency was never touched: it still
				// reaches the full source set through middle, and never lists
				// itself.
				if names := upstreamNames(after); slices.Contains(names, "report") {
					t.Fatalf("target leaked into its own upstreams: %v", names)
				}
				if got, want := len(after), 6; got != want {
					t.Fatalf("got %d upstreams %v, want %d", got, upstreamNames(after), want)
				}

				// The result captured before the rewire keeps its old content.
				if !reflect.DeepEqual(before, initialWant) {
					t.Fatalf("earlier result changed after rewire: got %v, want %v", before, initialWant)
				}
				// And the new query itself left the graph untouched.
				mustRegister(t, graph, "middle", replacement...) // idempotent no-op re-register
				again := mustUpstreams(t, graph, "report")
				if !reflect.DeepEqual(again, rewiredWant) {
					t.Fatalf("repeat query differs: %v, want %v", again, rewiredWant)
				}
			})
		}
	}
}

// Replacing middle's upstreams with a list that includes report would close a
// loop (middle -> report -> middle). Registration must report a cycle naming
// report, refuse the whole request without partial replacement, and a following
// Upstreams(report) must return exactly the pre-request sources, distances and
// paths.
func TestUpstreamsAfterRejectedCyclicMiddleRewire(t *testing.T) {
	graph := map[string]*Lineage{}
	mustRegister(t, graph, "raw")
	mustRegister(t, graph, "a", "raw")
	mustRegister(t, graph, "b", "raw")
	mustRegister(t, graph, "z", "a")
	mustRegister(t, graph, "c", "b")
	mustRegister(t, graph, "middle", "raw", "z", "c")
	mustRegister(t, graph, "report", "middle")
	assertConsistent(t, graph)

	before := mustUpstreams(t, graph, "report")
	want := []Upstream{
		{Dataset: "middle", Distance: 1, Path: []string{"middle", "report"}},
		{Dataset: "c", Distance: 2, Path: []string{"c", "middle", "report"}},
		{Dataset: "raw", Distance: 2, Path: []string{"raw", "middle", "report"}},
		{Dataset: "z", Distance: 2, Path: []string{"z", "middle", "report"}},
		{Dataset: "a", Distance: 3, Path: []string{"a", "z", "middle", "report"}},
		{Dataset: "b", Distance: 3, Path: []string{"b", "c", "middle", "report"}},
	}
	if !reflect.DeepEqual(before, want) {
		t.Fatalf("initial upstreams = %v, want %v", before, want)
	}
	graphBefore := snapshot(graph)

	// The cyclic parent appears first in one attempt and later in another; both
	// must be rejected with a cycle error naming report.
	attempts := [][]string{{"report", "z", "c"}, {"z", "c", "report"}, {"z", "report", "c"}}
	for attempt, parents := range attempts {
		err := Register(graph, Dataset{Name: "middle"}, parents)
		if err == nil || !strings.Contains(err.Error(), "cycle") || !strings.Contains(err.Error(), "report") {
			t.Fatalf("attempt %d parents %v: want cycle error naming report, got %v", attempt, parents, err)
		}
		// No partial replacement: raw is still a direct parent of middle and the
		// declared order is unchanged.
		assertEntry(t, graph, "middle", []string{"raw", "z", "c"}, []string{"report"})
		assertEntry(t, graph, "report", []string{"middle"}, nil)
		assertEntry(t, graph, "raw", nil, []string{"a", "b", "middle"})
		assertConsistent(t, graph)
		if !reflect.DeepEqual(snapshot(graph), graphBefore) {
			t.Fatalf("attempt %d: rejected registration changed graph: before=%v after=%v",
				attempt, graphBefore, snapshot(graph))
		}

		// The query after the refusal must be indistinguishable from before.
		after := mustUpstreams(t, graph, "report")
		if !reflect.DeepEqual(after, before) {
			t.Fatalf("attempt %d: upstreams after rejected rewire = %v, want %v", attempt, after, before)
		}
		assertUpstreamOnce(t, after, "raw", 2, []string{"raw", "middle", "report"})
	}
}
