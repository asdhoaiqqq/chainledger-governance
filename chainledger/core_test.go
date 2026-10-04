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

// Regression for replacing a middle dataset's direct upstreams so that the
// shortest route from a source to the report is deleted while a longer route
// keeps the source involved. raw feeds a and b; a feeds z; b feeds c; middle
// depends on raw, z and c together; report depends on middle. Initially raw
// reaches report at distance 2 via raw -> middle -> report. Re-registering
// middle with only z and c deletes that direct edge, but raw still derives
// report through raw -> a -> z -> middle -> report and raw -> b -> c -> middle
// -> report, both of length 4: raw must stay listed exactly once, at distance
// 4, and must not keep the stale distance-2 explanation. The two surviving
// routes have equal length, so the full path is compared name by name from the
// source: the paths first differ at hop 1 where a < b, hence the a/z route
// wins even though middle's direct upstream c sorts ahead of z. report's own
// direct dependency is untouched, so it still resolves every source through
// middle.
func TestUpstreamsAfterRewireLongerRouteSurvives(t *testing.T) {
	// Two builds of the same lineage with the independent branches registered
	// in opposite orders and middle's upstream list written differently; the
	// rewire likewise lists z and c in opposite orders. Identical final
	// relationships must give identical results.
	builds := []struct {
		registrations [][]string
		newParents    []string
	}{
		{
			registrations: [][]string{
				{"raw"},
				{"a", "raw"},
				{"z", "a"},
				{"b", "raw"},
				{"c", "b"},
				{"middle", "raw", "z", "c"},
				{"report", "middle"},
			},
			newParents: []string{"z", "c"},
		},
		{
			registrations: [][]string{
				{"raw"},
				{"b", "raw"}, // b-side branch registered first
				{"c", "b"},
				{"a", "raw"},
				{"z", "a"},
				{"middle", "c", "z", "raw"}, // upstream list order flipped
				{"report", "middle"},
			},
			newParents: []string{"c", "z"},
		},
	}

	wantBefore := []Upstream{
		{Dataset: "middle", Distance: 1, Path: []string{"middle", "report"}},
		{Dataset: "c", Distance: 2, Path: []string{"c", "middle", "report"}},
		{Dataset: "raw", Distance: 2, Path: []string{"raw", "middle", "report"}},
		{Dataset: "z", Distance: 2, Path: []string{"z", "middle", "report"}},
		{Dataset: "a", Distance: 3, Path: []string{"a", "z", "middle", "report"}},
		{Dataset: "b", Distance: 3, Path: []string{"b", "c", "middle", "report"}},
	}
	wantAfter := []Upstream{
		{Dataset: "middle", Distance: 1, Path: []string{"middle", "report"}},
		{Dataset: "c", Distance: 2, Path: []string{"c", "middle", "report"}},
		{Dataset: "z", Distance: 2, Path: []string{"z", "middle", "report"}},
		{Dataset: "a", Distance: 3, Path: []string{"a", "z", "middle", "report"}},
		{Dataset: "b", Distance: 3, Path: []string{"b", "c", "middle", "report"}},
		{Dataset: "raw", Distance: 4, Path: []string{"raw", "a", "z", "middle", "report"}},
	}

	for i, build := range builds {
		graph := buildRegisteredGraph(t, build.registrations)
		assertConsistent(t, graph)

		before := mustUpstreams(t, graph, "report")
		if !reflect.DeepEqual(before, wantBefore) {
			t.Fatalf("build %d: upstreams before rewire = %v, want %v", i, before, wantBefore)
		}
		// Anchor: the direct raw -> middle edge makes raw a distance-2 source.
		assertUpstreamOnce(t, before, "raw", 2, []string{"raw", "middle", "report"})
		beforeCopy := make([]Upstream, len(before))
		for j, up := range before {
			beforeCopy[j] = Upstream{up.Dataset, up.Distance, append([]string(nil), up.Path...)}
		}

		// Replace middle's direct upstreams, dropping the raw -> middle edge.
		// report's own direct dependency is not part of the request.
		mustRegister(t, graph, "middle", build.newParents...)
		assertConsistent(t, graph)
		assertEntry(t, graph, "middle", build.newParents, []string{"report"})
		assertEntry(t, graph, "report", []string{"middle"}, nil)
		graphBeforeQuery := snapshot(graph)

		after := mustUpstreams(t, graph, "report")
		if !reflect.DeepEqual(after, wantAfter) {
			t.Fatalf("build %d: upstreams after rewire = %v, want %v", i, after, wantAfter)
		}
		// Anchors with explicit messages: raw survives exactly once at the
		// longer distance, explained from the source hop by hop (a < b decides
		// at hop 1; middle's direct upstreams c < z must not decide it), and
		// the stale distance-2 route is gone.
		assertUpstreamOnce(t, after, "raw", 4, []string{"raw", "a", "z", "middle", "report"})
		assertUpstreamOnce(t, after, "middle", 1, []string{"middle", "report"})

		// The target never lists itself.
		if names := upstreamNames(after); slices.Contains(names, "report") {
			t.Fatalf("build %d: target leaked into its own upstreams %v", i, names)
		}

		// The query changed no node, relationship or stored list order.
		if !reflect.DeepEqual(snapshot(graph), graphBeforeQuery) {
			t.Fatalf("build %d: query changed graph: before=%v after=%v", i, graphBeforeQuery, snapshot(graph))
		}

		// The result obtained before the rewire kept its original content.
		if !reflect.DeepEqual(before, beforeCopy) {
			t.Fatalf("build %d: earlier result changed after re-register/new query: before=%v snapshot=%v",
				i, before, beforeCopy)
		}
	}
}

// Regression for detaching a middle dataset from ALL of its direct upstreams
// by re-registering it with an empty upstream list, where the report still has
// a second branch reaching back to the old source. raw derives a and z; middle
// depends on raw and a; report depends on middle and z. Before the detach, raw
// reaches report over two shortest routes of length 2 (raw -> middle -> report
// and raw -> z -> report), and the lexicographically smaller middle route is
// the explanation. Detaching middle makes it a new root without deleting it or
// breaking its downstream edge to report: middle stays a distance-1 source, a
// leaves report's sources entirely, but raw survives exactly once at distance
// 2 through the remaining z branch, with the explanation re-derived as
// raw -> z -> report instead of the removed middle route.
func TestUpstreamsAfterDetachAllUpstreamsSecondBranchSurvives(t *testing.T) {
	graph := buildRegisteredGraph(t, [][]string{
		{"raw"},
		{"a", "raw"},
		{"z", "raw"},
		{"middle", "raw", "a"},
		{"report", "middle", "z"},
	})
	assertConsistent(t, graph)

	wantBefore := []Upstream{
		{Dataset: "middle", Distance: 1, Path: []string{"middle", "report"}},
		{Dataset: "z", Distance: 1, Path: []string{"z", "report"}},
		{Dataset: "a", Distance: 2, Path: []string{"a", "middle", "report"}},
		{Dataset: "raw", Distance: 2, Path: []string{"raw", "middle", "report"}},
	}
	before := mustUpstreams(t, graph, "report")
	if !reflect.DeepEqual(before, wantBefore) {
		t.Fatalf("upstreams before detach = %v, want %v", before, wantBefore)
	}
	// Anchor: of the two length-2 routes from raw, the middle route explains.
	assertUpstreamOnce(t, before, "raw", 2, []string{"raw", "middle", "report"})
	beforeCopy := make([]Upstream, len(before))
	for i, up := range before {
		beforeCopy[i] = Upstream{up.Dataset, up.Distance, append([]string(nil), up.Path...)}
	}

	// Detach middle from every direct upstream. report's own direct
	// dependencies are not part of the request and must not change.
	mustRegister(t, graph, "middle")
	assertConsistent(t, graph)
	assertEntry(t, graph, "middle", nil, []string{"report"})
	assertEntry(t, graph, "report", []string{"middle", "z"}, nil)
	assertEntry(t, graph, "raw", nil, []string{"a", "z"})
	assertEntry(t, graph, "a", []string{"raw"}, nil)
	assertEntry(t, graph, "z", []string{"raw"}, []string{"report"})
	graphBeforeQuery := snapshot(graph)

	wantAfter := []Upstream{
		{Dataset: "middle", Distance: 1, Path: []string{"middle", "report"}},
		{Dataset: "z", Distance: 1, Path: []string{"z", "report"}},
		{Dataset: "raw", Distance: 2, Path: []string{"raw", "z", "report"}},
	}
	after := mustUpstreams(t, graph, "report")
	if !reflect.DeepEqual(after, wantAfter) {
		t.Fatalf("upstreams after detach = %v, want %v", after, wantAfter)
	}
	// Anchors with explicit messages: middle stays a direct source, a is gone,
	// and raw survives exactly once via z — it must not follow a out of the
	// result, nor keep the detached raw -> middle -> report explanation.
	assertUpstreamOnce(t, after, "middle", 1, []string{"middle", "report"})
	assertUpstreamOnce(t, after, "raw", 2, []string{"raw", "z", "report"})
	if names := upstreamNames(after); slices.Contains(names, "a") || slices.Contains(names, "report") {
		t.Fatalf("detached upstream or target leaked into %v", names)
	}

	// The query is read-only and does not resurrect middle's detached
	// upstreams.
	if !reflect.DeepEqual(snapshot(graph), graphBeforeQuery) {
		t.Fatalf("query changed graph: before=%v after=%v", graphBeforeQuery, snapshot(graph))
	}
	assertEntry(t, graph, "middle", nil, []string{"report"})

	// The result obtained before the detach kept its original content.
	if !reflect.DeepEqual(before, beforeCopy) {
		t.Fatalf("earlier result changed after detach/new query: before=%v snapshot=%v", before, beforeCopy)
	}
}

// Regression for detaching a middle dataset from ALL of its direct upstreams
// where the report has no second branch back to the old sources. raw derives
// a; middle depends on raw and a; report depends on middle alone. After the
// detach, report's only remaining source is middle at distance 1: raw and a
// stay registered in the graph but leave report's upstream list. Querying the
// detached middle itself succeeds with a non-nil empty list — it is a root,
// not a missing dataset, and never lists itself.
func TestUpstreamsAfterDetachAllUpstreamsNoOtherBranch(t *testing.T) {
	graph := buildRegisteredGraph(t, [][]string{
		{"raw"},
		{"a", "raw"},
		{"middle", "raw", "a"},
		{"report", "middle"},
	})
	assertConsistent(t, graph)

	wantBefore := []Upstream{
		{Dataset: "middle", Distance: 1, Path: []string{"middle", "report"}},
		{Dataset: "a", Distance: 2, Path: []string{"a", "middle", "report"}},
		{Dataset: "raw", Distance: 2, Path: []string{"raw", "middle", "report"}},
	}
	before := mustUpstreams(t, graph, "report")
	if !reflect.DeepEqual(before, wantBefore) {
		t.Fatalf("upstreams before detach = %v, want %v", before, wantBefore)
	}

	// Detach middle from every direct upstream; report keeps depending on it.
	mustRegister(t, graph, "middle")
	assertConsistent(t, graph)
	assertEntry(t, graph, "middle", nil, []string{"report"})
	assertEntry(t, graph, "report", []string{"middle"}, nil)
	assertEntry(t, graph, "raw", nil, []string{"a"})
	assertEntry(t, graph, "a", []string{"raw"}, nil)
	graphBeforeQuery := snapshot(graph)

	// report's sources shrink to middle alone: raw and a are still registered
	// but no longer connected to report.
	wantAfter := []Upstream{
		{Dataset: "middle", Distance: 1, Path: []string{"middle", "report"}},
	}
	after := mustUpstreams(t, graph, "report")
	if !reflect.DeepEqual(after, wantAfter) {
		t.Fatalf("upstreams after detach = %v, want %v", after, wantAfter)
	}
	assertUpstreamOnce(t, after, "middle", 1, []string{"middle", "report"})
	if names := upstreamNames(after); slices.Contains(names, "raw") || slices.Contains(names, "a") {
		t.Fatalf("detached sources leaked into %v", names)
	}
	if _, ok := graph["raw"]; !ok {
		t.Fatal("raw must stay registered after the detach")
	}
	if _, ok := graph["a"]; !ok {
		t.Fatal("a must stay registered after the detach")
	}

	// The detached middle is a valid query target: a successful non-nil empty
	// list, no not-found error, and it never lists itself.
	middleUpstreams, err := Upstreams(graph, "middle")
	if err != nil {
		t.Fatalf("Upstreams(middle) after detach: %v", err)
	}
	if middleUpstreams == nil || len(middleUpstreams) != 0 {
		t.Fatalf("want empty non-nil list from detached middle, got %v", middleUpstreams)
	}

	// The queries changed no node, relationship or stored list order, and did
	// not resurrect middle's detached upstreams.
	if !reflect.DeepEqual(snapshot(graph), graphBeforeQuery) {
		t.Fatalf("queries changed graph: before=%v after=%v", graphBeforeQuery, snapshot(graph))
	}
	assertEntry(t, graph, "middle", nil, []string{"report"})
}

// A rewire of middle whose new upstream list contains report would close the
// cycle middle -> report -> middle, so the registration must be refused with
// an error naming report. The refusal is atomic: afterwards Upstreams(report)
// returns exactly the sources, distances and explanation paths it returned
// before the attempt, with no trace of the partially requested replacement.
func TestUpstreamsRejectedCycleRewireLeavesResultsUntouched(t *testing.T) {
	graph := buildRegisteredGraph(t, [][]string{
		{"raw"},
		{"a", "raw"},
		{"z", "a"},
		{"b", "raw"},
		{"c", "b"},
		{"middle", "raw", "z", "c"},
		{"report", "middle"},
	})
	assertConsistent(t, graph)

	before := mustUpstreams(t, graph, "report")
	assertUpstreamOnce(t, before, "raw", 2, []string{"raw", "middle", "report"})
	graphBefore := snapshot(graph)

	// report is middle's own downstream: adding it as an upstream closes a
	// cycle, and the error must say so naming report.
	err := Register(graph, Dataset{Name: "middle"}, []string{"z", "c", "report"})
	if err == nil || !strings.Contains(err.Error(), "cycle") || !strings.Contains(err.Error(), "report") {
		t.Fatalf("want cycle error naming report, got %v", err)
	}

	if !reflect.DeepEqual(snapshot(graph), graphBefore) {
		t.Fatalf("rejected rewire changed graph: before=%v after=%v", graphBefore, snapshot(graph))
	}
	assertEntry(t, graph, "middle", []string{"raw", "z", "c"}, []string{"report"})
	assertEntry(t, graph, "report", []string{"middle"}, nil)
	assertConsistent(t, graph)

	// The query after the rejected attempt sees exactly the pre-request
	// lineage: same sources, distances and explanation paths, nothing partial.
	after := mustUpstreams(t, graph, "report")
	if !reflect.DeepEqual(after, before) {
		t.Fatalf("upstreams after rejected rewire = %v, want %v", after, before)
	}
	assertUpstreamOnce(t, after, "raw", 2, []string{"raw", "middle", "report"})
	assertUpstreamOnce(t, after, "a", 3, []string{"a", "z", "middle", "report"})
	assertUpstreamOnce(t, after, "b", 3, []string{"b", "c", "middle", "report"})
}

// impactRecord returns a pointer to the impact record for name, so a test can
// mutate that returned record directly.
func impactRecord(t *testing.T, impacts []Impact, name string) *Impact {
	t.Helper()
	for i := range impacts {
		if impacts[i].Dataset == name {
			return &impacts[i]
		}
	}
	t.Fatalf("impact %q missing from %v", name, impactNames(impacts))
	return nil
}

// upstreamRecord returns a pointer to the upstream record for name, so a test
// can mutate that returned record directly.
func upstreamRecord(t *testing.T, upstreams []Upstream, name string) *Upstream {
	t.Helper()
	for i := range upstreams {
		if upstreams[i].Dataset == name {
			return &upstreams[i]
		}
	}
	t.Fatalf("upstream %q missing from %v", name, upstreamNames(upstreams))
	return nil
}

// Regression for the independence of returned explanation paths that share a
// prefix. The chain is source -> detail -> report -> view, so after querying
// source, report's path [source detail report] and view's path
// [source detail report view] run through the same source, detail and report
// names. The README lets callers mutate the returned Path for display labels;
// rewriting a shared name or appending a display marker on report's record must
// change that one record only: view keeps its full original source-to-view
// sequence, detail keeps its path, every Dataset and Distance keeps its value,
// the graph's registered edges are untouched, and the next query again returns
// names, distances and paths determined by the current lineage.
func TestImpactsEditReturnedPathSharedPrefixIsolated(t *testing.T) {
	graph := buildRegisteredGraph(t, [][]string{
		{"source"},
		{"detail", "source"},
		{"report", "detail"},
		{"view", "report"},
	})
	assertConsistent(t, graph)

	want := []Impact{
		{Dataset: "detail", Distance: 1, Path: []string{"source", "detail"}},
		{Dataset: "report", Distance: 2, Path: []string{"source", "detail", "report"}},
		{Dataset: "view", Distance: 3, Path: []string{"source", "detail", "report", "view"}},
	}
	impacts := mustImpacts(t, graph, "source")
	if !reflect.DeepEqual(impacts, want) {
		t.Fatalf("impacts = %v, want %v", impacts, want)
	}
	before := snapshot(graph)

	// Display relabeling: rewrite names on the prefix shared with view's path
	// in report's returned record only.
	report := impactRecord(t, impacts, "report")
	report.Path[0] = "source display label"
	report.Path[1] = "detail display label"
	assertImpact(t, impacts, "report", 2,
		[]string{"source display label", "detail display label", "report"})

	// view's explanation runs through the very same names, but its path must
	// keep the full original sequence; detail's record is untouched as well.
	assertImpact(t, impacts, "view", 3, []string{"source", "detail", "report", "view"})
	assertImpact(t, impacts, "detail", 1, []string{"source", "detail"})

	// Appending a display marker extends report's path alone. view and detail
	// must neither gain the marker nor lose an original name (a shared backing
	// array could let this append overwrite view's final hop instead).
	report.Path = append(report.Path, "display-marker")
	assertImpact(t, impacts, "report", 2,
		[]string{"source display label", "detail display label", "report", "display-marker"})
	assertImpact(t, impacts, "view", 3, []string{"source", "detail", "report", "view"})
	assertImpact(t, impacts, "detail", 1, []string{"source", "detail"})

	// Dataset names and distances of every record keep their queried values.
	if got, wantNames := impactNames(impacts), []string{"detail", "report", "view"}; !reflect.DeepEqual(got, wantNames) {
		t.Fatalf("dataset names/order = %v, want %v", got, wantNames)
	}
	wantDistance := map[string]int{"detail": 1, "report": 2, "view": 3}
	for _, im := range impacts {
		if im.Distance != wantDistance[im.Dataset] {
			t.Errorf("%s distance = %d, want %d", im.Dataset, im.Distance, wantDistance[im.Dataset])
		}
	}

	// The graph still records the original lineage, not the display labels.
	if !reflect.DeepEqual(snapshot(graph), before) {
		t.Fatalf("editing returned paths changed graph: before=%v after=%v", before, snapshot(graph))
	}
	assertEntry(t, graph, "source", nil, []string{"detail"})
	assertEntry(t, graph, "detail", []string{"source"}, []string{"report"})
	assertEntry(t, graph, "report", []string{"detail"}, []string{"view"})
	assertEntry(t, graph, "view", []string{"report"}, nil)
	assertConsistent(t, graph)

	// A subsequent query is recomputed from the current lineage: original
	// names, distances and paths, inheriting neither label nor marker.
	fresh := mustImpacts(t, graph, "source")
	if !reflect.DeepEqual(fresh, want) {
		t.Fatalf("fresh impacts = %v, want %v", fresh, want)
	}
}

// Upstream counterpart of the shared-prefix case: querying view on
// source -> detail -> report -> view gives source, detail and report records
// whose explanation paths all end with [report view]. Editing that shared tail
// on one returned record must leave the other sources' original name sequences,
// distances and ordering untouched.
func TestUpstreamsEditReturnedPathSharedSuffixIsolated(t *testing.T) {
	graph := buildRegisteredGraph(t, [][]string{
		{"source"},
		{"detail", "source"},
		{"report", "detail"},
		{"view", "report"},
	})
	assertConsistent(t, graph)

	want := []Upstream{
		{Dataset: "report", Distance: 1, Path: []string{"report", "view"}},
		{Dataset: "detail", Distance: 2, Path: []string{"detail", "report", "view"}},
		{Dataset: "source", Distance: 3, Path: []string{"source", "detail", "report", "view"}},
	}
	upstreams := mustUpstreams(t, graph, "view")
	if !reflect.DeepEqual(upstreams, want) {
		t.Fatalf("upstreams = %v, want %v", upstreams, want)
	}
	before := snapshot(graph)

	// Rewrite the shared ending [report view] on the source record only.
	source := upstreamRecord(t, upstreams, "source")
	source.Path[2] = "report display label"
	source.Path[3] = "view display label"
	assertUpstream(t, upstreams, "source", 3,
		[]string{"source", "detail", "report display label", "view display label"})

	// report and detail still explain themselves with their original sequences.
	assertUpstream(t, upstreams, "report", 1, []string{"report", "view"})
	assertUpstream(t, upstreams, "detail", 2, []string{"detail", "report", "view"})

	// Append a display marker to detail's record alone; the other records keep
	// exactly their obtained content.
	detail := upstreamRecord(t, upstreams, "detail")
	detail.Path = append(detail.Path, "display-marker")
	assertUpstream(t, upstreams, "detail", 2, []string{"detail", "report", "view", "display-marker"})
	assertUpstream(t, upstreams, "report", 1, []string{"report", "view"})
	assertUpstream(t, upstreams, "source", 3,
		[]string{"source", "detail", "report display label", "view display label"})

	// Order (distance ascending) and all distances/names keep their values.
	if got, wantNames := upstreamNames(upstreams), []string{"report", "detail", "source"}; !reflect.DeepEqual(got, wantNames) {
		t.Fatalf("upstream names/order = %v, want %v", got, wantNames)
	}
	wantDistance := map[string]int{"report": 1, "detail": 2, "source": 3}
	for _, up := range upstreams {
		if up.Distance != wantDistance[up.Dataset] {
			t.Errorf("%s distance = %d, want %d", up.Dataset, up.Distance, wantDistance[up.Dataset])
		}
	}

	// The graph keeps the registered upstream/downstream relationships.
	if !reflect.DeepEqual(snapshot(graph), before) {
		t.Fatalf("editing returned paths changed graph: before=%v after=%v", before, snapshot(graph))
	}
	assertEntry(t, graph, "source", nil, []string{"detail"})
	assertEntry(t, graph, "detail", []string{"source"}, []string{"report"})
	assertEntry(t, graph, "report", []string{"detail"}, []string{"view"})
	assertEntry(t, graph, "view", []string{"report"}, nil)
	assertConsistent(t, graph)

	// A fresh query reflects the current lineage, not the edited return value.
	fresh := mustUpstreams(t, graph, "view")
	if !reflect.DeepEqual(fresh, want) {
		t.Fatalf("fresh upstreams = %v, want %v", fresh, want)
	}
}

// Two result batches obtained from one graph must be independent of each other:
// display edits (both rewriting path elements and appending names, even when
// the edited path no longer names a legal lineage) applied to one batch cannot
// reach the other batch, the graph, or any result fetched afterwards.
func TestReturnedPathEditsStayWithinOneResultBatch(t *testing.T) {
	graph := buildRegisteredGraph(t, [][]string{
		{"source"},
		{"detail", "source"},
		{"report", "detail"},
		{"view", "report"},
	})
	assertConsistent(t, graph)

	downWant := []Impact{
		{Dataset: "detail", Distance: 1, Path: []string{"source", "detail"}},
		{Dataset: "report", Distance: 2, Path: []string{"source", "detail", "report"}},
		{Dataset: "view", Distance: 3, Path: []string{"source", "detail", "report", "view"}},
	}
	upWant := []Upstream{
		{Dataset: "report", Distance: 1, Path: []string{"report", "view"}},
		{Dataset: "detail", Distance: 2, Path: []string{"detail", "report", "view"}},
		{Dataset: "source", Distance: 3, Path: []string{"source", "detail", "report", "view"}},
	}

	// Two batches fetched from the same graph, one in each query direction.
	batchDown := mustImpacts(t, graph, "source")
	batchUp := mustUpstreams(t, graph, "view")
	if !reflect.DeepEqual(batchDown, downWant) || !reflect.DeepEqual(batchUp, upWant) {
		t.Fatalf("initial batches: down=%v want=%v; up=%v want=%v", batchDown, downWant, batchUp, upWant)
	}
	before := snapshot(graph)

	// Edit ONLY the downstream batch: rewrite an element to a name that is not
	// a registered dataset at all, and append a display marker.
	downReport := impactRecord(t, batchDown, "report")
	downReport.Path[1] = "relabeled-not-a-dataset"
	downView := impactRecord(t, batchDown, "view")
	downView.Path = append(downView.Path, "down-marker")
	assertImpact(t, batchDown, "report", 2, []string{"source", "relabeled-not-a-dataset", "report"})
	assertImpact(t, batchDown, "view", 3, []string{"source", "detail", "report", "view", "down-marker"})

	// The other batch immediately retains the full content it was fetched with.
	assertUpstream(t, batchUp, "report", 1, []string{"report", "view"})
	assertUpstream(t, batchUp, "detail", 2, []string{"detail", "report", "view"})
	assertUpstream(t, batchUp, "source", 3, []string{"source", "detail", "report", "view"})

	// The illegal path content never reaches the graph.
	if !reflect.DeepEqual(snapshot(graph), before) {
		t.Fatalf("editing one batch changed graph: before=%v after=%v", before, snapshot(graph))
	}
	assertConsistent(t, graph)

	// New results fetched after the edits inherit neither the replaced name
	// nor the appended marker, in either direction.
	if fresh := mustImpacts(t, graph, "source"); !reflect.DeepEqual(fresh, downWant) {
		t.Fatalf("fresh downstream batch = %v, want %v", fresh, downWant)
	}
	if fresh := mustUpstreams(t, graph, "view"); !reflect.DeepEqual(fresh, upWant) {
		t.Fatalf("fresh upstream batch = %v, want %v", fresh, upWant)
	}

	// Reverse: edit ONLY the upstream batch. The downstream batch keeps its
	// own earlier edits and gains nothing new; fresh queries stay pristine.
	upSource := upstreamRecord(t, batchUp, "source")
	upSource.Path[0] = "relabeled-source"
	upReport := upstreamRecord(t, batchUp, "report")
	upReport.Path = append(upReport.Path, "up-marker")
	assertUpstream(t, batchUp, "source", 3, []string{"relabeled-source", "detail", "report", "view"})
	assertUpstream(t, batchUp, "report", 1, []string{"report", "view", "up-marker"})
	assertUpstream(t, batchUp, "detail", 2, []string{"detail", "report", "view"})

	assertImpact(t, batchDown, "detail", 1, []string{"source", "detail"})
	assertImpact(t, batchDown, "report", 2, []string{"source", "relabeled-not-a-dataset", "report"})
	assertImpact(t, batchDown, "view", 3, []string{"source", "detail", "report", "view", "down-marker"})

	if fresh := mustImpacts(t, graph, "source"); !reflect.DeepEqual(fresh, downWant) {
		t.Fatalf("later downstream batch = %v, want %v", fresh, downWant)
	}
	if fresh := mustUpstreams(t, graph, "view"); !reflect.DeepEqual(fresh, upWant) {
		t.Fatalf("later upstream batch = %v, want %v", fresh, upWant)
	}
	if !reflect.DeepEqual(snapshot(graph), before) {
		t.Fatalf("editing the second batch changed graph: before=%v after=%v", before, snapshot(graph))
	}
}

// Product regression for re-judging an upstream replacement that was once
// rejected as cyclic. source feeds left and right; both feed report; report
// feeds view; independent stands alone. Re-registering source with the single
// upstream report must be rejected while report can still reach source through
// its existing parents, and the error must name report as the cycle-closing
// upstream. A rejection is a verdict about the current graph only: detaching
// one of source's two routes (by re-registering that dataset with no parents,
// which removes its upstream edge without deleting its own downstream edge to
// report) must not make the identical replacement legal while the other route
// still closes the loop, nor must the refusal leave report with a new source
// child or resurrect the detached edge. Once the last route is detached the
// same request succeeds: source's direct upstream becomes report, report keeps
// view and gains source at the end of its children, left and right stay
// report's direct upstreams (now roots themselves), every dataset survives, and
// the public queries describe the new lineage. After every rejection the
// queryable lineage must be byte-for-byte what it was before that request.
func TestRegisterRejectedUpstreamReplaceRejudgedAfterDetach(t *testing.T) {
	graph := buildRegisteredGraph(t, [][]string{
		{"source"},
		{"left", "source"},
		{"right", "source"},
		{"report", "left", "right"},
		{"view", "report"},
		{"independent"},
	})
	assertConsistent(t, graph)

	// Initial lineage, edge by edge.
	assertEntry(t, graph, "source", nil, []string{"left", "right"})
	assertEntry(t, graph, "left", []string{"source"}, []string{"report"})
	assertEntry(t, graph, "right", []string{"source"}, []string{"report"})
	assertEntry(t, graph, "report", []string{"left", "right"}, []string{"view"})
	assertEntry(t, graph, "view", []string{"report"}, nil)
	assertEntry(t, graph, "independent", nil, nil)
	if got, want := Roots(graph), []string{"independent", "source"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("Roots = %v, want %v", got, want)
	}

	// Query baselines before any cyclic request. report can reach source
	// through both left and right, so its downstream view is three levels out.
	wantSourceImpacts := []Impact{
		{Dataset: "left", Distance: 1, Path: []string{"source", "left"}},
		{Dataset: "right", Distance: 1, Path: []string{"source", "right"}},
		{Dataset: "report", Distance: 2, Path: []string{"source", "left", "report"}},
		{Dataset: "view", Distance: 3, Path: []string{"source", "left", "report", "view"}},
	}
	if got := mustImpacts(t, graph, "source"); !reflect.DeepEqual(got, wantSourceImpacts) {
		t.Fatalf("initial Impacts(source) = %v, want %v", got, wantSourceImpacts)
	}
	wantReportImpacts := []Impact{
		{Dataset: "view", Distance: 1, Path: []string{"report", "view"}},
	}
	if got := mustImpacts(t, graph, "report"); !reflect.DeepEqual(got, wantReportImpacts) {
		t.Fatalf("initial Impacts(report) = %v, want %v", got, wantReportImpacts)
	}
	wantReportUpstreams := []Upstream{
		{Dataset: "left", Distance: 1, Path: []string{"left", "report"}},
		{Dataset: "right", Distance: 1, Path: []string{"right", "report"}},
		{Dataset: "source", Distance: 2, Path: []string{"source", "left", "report"}},
	}
	if got := mustUpstreams(t, graph, "report"); !reflect.DeepEqual(got, wantReportUpstreams) {
		t.Fatalf("initial Upstreams(report) = %v, want %v", got, wantReportUpstreams)
	}
	rootUpstreams := mustUpstreams(t, graph, "source")
	if rootUpstreams == nil || len(rootUpstreams) != 0 {
		t.Fatalf("initial Upstreams(source) = %v, want non-nil empty list", rootUpstreams)
	}

	// First attempt: report still derives from source through both branches, so
	// source -> report closes a loop. The whole replacement is refused.
	before := snapshot(graph)
	err := Register(graph, Dataset{Name: "source"}, []string{"report"})
	if err == nil || !strings.Contains(err.Error(), "cycle") || !strings.Contains(err.Error(), "report") {
		t.Fatalf("want cycle error naming report, got %v", err)
	}
	if !reflect.DeepEqual(snapshot(graph), before) {
		t.Fatalf("first rejection changed graph: before=%v after=%v", before, snapshot(graph))
	}
	// source is still a root and every original edge is intact; the refused
	// edge source -> report exists on neither side.
	assertEntry(t, graph, "source", nil, []string{"left", "right"})
	assertEntry(t, graph, "report", []string{"left", "right"}, []string{"view"})
	if slices.Contains(graph["source"].Parents, "report") {
		t.Fatal("rejected request left the source -> report parent edge behind")
	}
	if slices.Contains(graph["report"].Children, "source") {
		t.Fatal("rejected request left report with source as a downstream")
	}
	assertConsistent(t, graph)

	// Queries after the refusal answer exactly as before the request.
	if got := mustImpacts(t, graph, "source"); !reflect.DeepEqual(got, wantSourceImpacts) {
		t.Fatalf("Impacts(source) after first rejection = %v, want %v", got, wantSourceImpacts)
	}
	if got := mustImpacts(t, graph, "report"); !reflect.DeepEqual(got, wantReportImpacts) {
		t.Fatalf("Impacts(report) after first rejection = %v, want %v", got, wantReportImpacts)
	}
	if got := mustUpstreams(t, graph, "report"); !reflect.DeepEqual(got, wantReportUpstreams) {
		t.Fatalf("Upstreams(report) after first rejection = %v, want %v", got, wantReportUpstreams)
	}
	if got := mustUpstreams(t, graph, "source"); !reflect.DeepEqual(got, rootUpstreams) {
		t.Fatalf("Upstreams(source) after first rejection = %v, want %v", got, rootUpstreams)
	}

	// Detach left from source. This rewrites left's own upstream list only:
	// the source -> left edge disappears on both sides, while left's downstream
	// edge to report is retained, so left is now a root that still feeds report.
	mustRegister(t, graph, "left")
	assertConsistent(t, graph)
	assertEntry(t, graph, "left", nil, []string{"report"})
	assertEntry(t, graph, "source", nil, []string{"right"})
	assertEntry(t, graph, "report", []string{"left", "right"}, []string{"view"})
	assertEntry(t, graph, "independent", nil, nil)

	// Fresh baselines under the current lineage: source reaches report only via
	// right now; left stays a direct upstream of report but no longer leads back
	// to source.
	wantSourceImpacts = []Impact{
		{Dataset: "right", Distance: 1, Path: []string{"source", "right"}},
		{Dataset: "report", Distance: 2, Path: []string{"source", "right", "report"}},
		{Dataset: "view", Distance: 3, Path: []string{"source", "right", "report", "view"}},
	}
	if got := mustImpacts(t, graph, "source"); !reflect.DeepEqual(got, wantSourceImpacts) {
		t.Fatalf("Impacts(source) after left detach = %v, want %v", got, wantSourceImpacts)
	}
	wantReportUpstreams = []Upstream{
		{Dataset: "left", Distance: 1, Path: []string{"left", "report"}},
		{Dataset: "right", Distance: 1, Path: []string{"right", "report"}},
		{Dataset: "source", Distance: 2, Path: []string{"source", "right", "report"}},
	}
	if got := mustUpstreams(t, graph, "report"); !reflect.DeepEqual(got, wantReportUpstreams) {
		t.Fatalf("Upstreams(report) after left detach = %v, want %v", got, wantReportUpstreams)
	}
	if got := mustImpacts(t, graph, "report"); !reflect.DeepEqual(got, wantReportImpacts) {
		t.Fatalf("Impacts(report) changed after left detach: got %v, want %v", got, wantReportImpacts)
	}
	if got := mustUpstreams(t, graph, "source"); !reflect.DeepEqual(got, rootUpstreams) {
		t.Fatalf("Upstreams(source) changed after left detach: got %v, want %v", got, rootUpstreams)
	}

	// Second attempt of the identical replacement: still cyclic, because report
	// reaches source through the surviving right branch. One route being gone
	// must not blind the check to the other route.
	before = snapshot(graph)
	err = Register(graph, Dataset{Name: "source"}, []string{"report"})
	if err == nil || !strings.Contains(err.Error(), "cycle") || !strings.Contains(err.Error(), "report") {
		t.Fatalf("want cycle error naming report via the right branch, got %v", err)
	}
	if !reflect.DeepEqual(snapshot(graph), before) {
		t.Fatalf("second rejection changed graph: before=%v after=%v", before, snapshot(graph))
	}
	// The refusal neither adds report as source's upstream / source as report's
	// downstream, nor restores left's detached edge.
	assertEntry(t, graph, "left", nil, []string{"report"})
	assertEntry(t, graph, "source", nil, []string{"right"})
	assertEntry(t, graph, "report", []string{"left", "right"}, []string{"view"})
	if slices.Contains(graph["source"].Parents, "report") {
		t.Fatal("second rejection left the source -> report parent edge behind")
	}
	if slices.Contains(graph["report"].Children, "source") {
		t.Fatal("second rejection left report with source as a downstream")
	}
	if slices.Contains(graph["left"].Parents, "source") {
		t.Fatal("second rejection resurrected left's detached dependency on source")
	}
	assertConsistent(t, graph)

	// Every query still matches the state immediately before this request.
	if got := mustImpacts(t, graph, "source"); !reflect.DeepEqual(got, wantSourceImpacts) {
		t.Fatalf("Impacts(source) after second rejection = %v, want %v", got, wantSourceImpacts)
	}
	if got := mustImpacts(t, graph, "report"); !reflect.DeepEqual(got, wantReportImpacts) {
		t.Fatalf("Impacts(report) after second rejection = %v, want %v", got, wantReportImpacts)
	}
	if got := mustUpstreams(t, graph, "report"); !reflect.DeepEqual(got, wantReportUpstreams) {
		t.Fatalf("Upstreams(report) after second rejection = %v, want %v", got, wantReportUpstreams)
	}
	if got := mustUpstreams(t, graph, "source"); !reflect.DeepEqual(got, rootUpstreams) {
		t.Fatalf("Upstreams(source) after second rejection = %v, want %v", got, rootUpstreams)
	}

	// Detach right from source as well: the last route from report back to
	// source is gone. right likewise keeps its own downstream edge to report.
	mustRegister(t, graph, "right")
	assertConsistent(t, graph)
	assertEntry(t, graph, "right", nil, []string{"report"})
	assertEntry(t, graph, "source", nil, nil)
	assertEntry(t, graph, "report", []string{"left", "right"}, []string{"view"})

	// The same replacement is now legal: judged against the current lineage,
	// report no longer reaches source.
	mustRegister(t, graph, "source", "report")
	assertConsistent(t, graph)

	// Final lineage: source's direct upstream set was replaced wholesale by
	// report; report kept view and gained source appended at the end; left and
	// right remain report's direct upstreams (detaching an upstream edge did not
	// delete their downstream edge); the independent dataset is untouched.
	assertEntry(t, graph, "source", []string{"report"}, nil)
	assertEntry(t, graph, "report", []string{"left", "right"}, []string{"view", "source"})
	assertEntry(t, graph, "view", []string{"report"}, nil)
	assertEntry(t, graph, "left", nil, []string{"report"})
	assertEntry(t, graph, "right", nil, []string{"report"})
	assertEntry(t, graph, "independent", nil, nil)
	if got, want := Roots(graph), []string{"independent", "left", "right"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("Roots = %v, want %v", got, want)
	}
	if len(graph) != 6 {
		t.Fatalf("graph holds %d datasets %v, want all six to survive", len(graph), graph)
	}
	for _, name := range []string{"source", "left", "right", "report", "view", "independent"} {
		if _, ok := graph[name]; !ok {
			t.Errorf("dataset %q missing after the sequence", name)
		}
	}

	// Public queries describe the new orientation: report derives from left and
	// right; both source and view derive from report.
	reportImpacts := mustImpacts(t, graph, "report")
	wantReportImpacts = []Impact{
		{Dataset: "source", Distance: 1, Path: []string{"report", "source"}},
		{Dataset: "view", Distance: 1, Path: []string{"report", "view"}},
	}
	if !reflect.DeepEqual(reportImpacts, wantReportImpacts) {
		t.Fatalf("Impacts(report) after success = %v, want %v", reportImpacts, wantReportImpacts)
	}
	sourceUpstreams := mustUpstreams(t, graph, "source")
	wantSourceUpstreams := []Upstream{
		{Dataset: "report", Distance: 1, Path: []string{"report", "source"}},
		{Dataset: "left", Distance: 2, Path: []string{"left", "report", "source"}},
		{Dataset: "right", Distance: 2, Path: []string{"right", "report", "source"}},
	}
	if !reflect.DeepEqual(sourceUpstreams, wantSourceUpstreams) {
		t.Fatalf("Upstreams(source) after success = %v, want %v", sourceUpstreams, wantSourceUpstreams)
	}
	// source now points upward and has no downstream: a successful query with a
	// non-nil empty list, not a not-found error.
	sourceImpacts, err := Impacts(graph, "source")
	if err != nil {
		t.Fatalf("Impacts(source) after success: %v", err)
	}
	if sourceImpacts == nil || len(sourceImpacts) != 0 {
		t.Fatalf("Impacts(source) after success = %v, want non-nil empty list", sourceImpacts)
	}
}

func mustRename(t *testing.T, graph map[string]*Lineage, oldName, newName string) {
	t.Helper()
	if err := Rename(graph, oldName, newName); err != nil {
		t.Fatalf("Rename(%s, %s): %v", oldName, newName, err)
	}
}

// Renaming a middle dataset keeps its own upstream/downstream lists untouched
// and rewires every neighbor's reference in its original position. The old
// name disappears from the graph; the dataset count is unchanged.
func TestRenameMiddleNode(t *testing.T) {
	graph := map[string]*Lineage{}
	mustRegister(t, graph, "raw")
	mustRegister(t, graph, "mid", "raw")
	mustRegister(t, graph, "leaf", "mid")
	mustRegister(t, graph, "side", "mid")

	mustRename(t, graph, "mid", "core")

	if _, ok := graph["mid"]; ok {
		t.Error("old name mid still present in graph")
	}
	if got, want := len(graph), 4; got != want {
		t.Fatalf("dataset count = %d, want %d", got, want)
	}
	assertEntry(t, graph, "core", []string{"raw"}, []string{"leaf", "side"})
	assertEntry(t, graph, "raw", nil, []string{"core"})
	assertEntry(t, graph, "leaf", []string{"core"}, nil)
	assertEntry(t, graph, "side", []string{"core"}, nil)
	assertConsistent(t, graph)

	if got, want := Roots(graph), []string{"raw"}; !reflect.DeepEqual(got, want) {
		t.Errorf("Roots = %v, want %v", got, want)
	}
}

// Roots and leaves rename the same way: a root keeps no upstreams and its
// children see the new name; a leaf keeps its parents and its position in
// each parent's child list.
func TestRenameRootAndLeaf(t *testing.T) {
	graph := map[string]*Lineage{}
	mustRegister(t, graph, "root")
	mustRegister(t, graph, "other")
	mustRegister(t, graph, "first", "root")
	mustRegister(t, graph, "leaf", "root", "other")

	mustRename(t, graph, "root", "origin")
	assertEntry(t, graph, "origin", nil, []string{"first", "leaf"})
	assertEntry(t, graph, "first", []string{"origin"}, nil)
	assertEntry(t, graph, "leaf", []string{"origin", "other"}, nil)
	assertConsistent(t, graph)

	mustRename(t, graph, "leaf", "tip")
	assertEntry(t, graph, "tip", []string{"origin", "other"}, nil)
	// The replacement stays in the renamed node's original slot.
	assertEntry(t, graph, "origin", nil, []string{"first", "tip"})
	assertEntry(t, graph, "other", nil, []string{"tip"})
	assertConsistent(t, graph)

	if got, want := len(graph), 4; got != want {
		t.Fatalf("dataset count = %d, want %d", got, want)
	}
	if got, want := Roots(graph), []string{"origin", "other"}; !reflect.DeepEqual(got, want) {
		t.Errorf("Roots = %v, want %v", got, want)
	}
}

// The spec's worked example: source derives a and b, report depends on both.
// Renaming a to z puts z in a's old slot in report's direct upstream list, and
// the shortest explanation path for report from source is recomputed by name:
// [source b report] now beats [source z report].
func TestRenameSpecExample(t *testing.T) {
	graph := map[string]*Lineage{}
	mustRegister(t, graph, "source")
	mustRegister(t, graph, "a", "source")
	mustRegister(t, graph, "b", "source")
	mustRegister(t, graph, "report", "a", "b")

	mustRename(t, graph, "a", "z")

	assertEntry(t, graph, "z", []string{"source"}, []string{"report"})
	assertEntry(t, graph, "source", nil, []string{"z", "b"})
	assertEntry(t, graph, "report", []string{"z", "b"}, nil)
	assertConsistent(t, graph)

	impacts := mustImpacts(t, graph, "source")
	assertImpact(t, impacts, "report", 2, []string{"source", "b", "report"})
	assertImpact(t, impacts, "z", 1, []string{"source", "z"})

	upstreams := mustUpstreams(t, graph, "report")
	assertUpstream(t, upstreams, "source", 2, []string{"source", "b", "report"})
	assertUpstream(t, upstreams, "z", 1, []string{"z", "report"})
}

// After a rename the old name is treated as unregistered everywhere, while
// the new name inherits the renamed node's exact reachability. Results
// returned before the rename keep their original content.
func TestRenameQueriesUseNewName(t *testing.T) {
	graph := map[string]*Lineage{}
	mustRegister(t, graph, "raw")
	mustRegister(t, graph, "mid", "raw")
	mustRegister(t, graph, "leaf", "mid")

	beforeImpacts := mustImpacts(t, graph, "mid")
	beforeImpactsCopy := make([]Impact, len(beforeImpacts))
	for i, im := range beforeImpacts {
		beforeImpactsCopy[i] = Impact{im.Dataset, im.Distance, append([]string(nil), im.Path...)}
	}

	mustRename(t, graph, "mid", "hub")

	// The old name is gone: queries against it fail as unregistered.
	if _, err := Impacts(graph, "mid"); err == nil || !strings.Contains(err.Error(), "mid") {
		t.Fatalf("Impacts(mid) after rename: want not-found error naming mid, got %v", err)
	}
	if _, err := Upstreams(graph, "mid"); err == nil || !strings.Contains(err.Error(), "mid") {
		t.Fatalf("Upstreams(mid) after rename: want not-found error naming mid, got %v", err)
	}

	// The new name has exactly the renamed node's old reachability.
	impacts := mustImpacts(t, graph, "hub")
	assertImpact(t, impacts, "leaf", 1, []string{"hub", "leaf"})
	upstreams := mustUpstreams(t, graph, "hub")
	assertUpstream(t, upstreams, "raw", 1, []string{"raw", "hub"})
	assertImpact(t, mustImpacts(t, graph, "raw"), "hub", 1, []string{"raw", "hub"})
	assertImpact(t, mustImpacts(t, graph, "raw"), "leaf", 2, []string{"raw", "hub", "leaf"})
	assertUpstream(t, mustUpstreams(t, graph, "leaf"), "raw", 2, []string{"raw", "hub", "leaf"})

	// The result returned before the rename is untouched.
	if !reflect.DeepEqual(beforeImpacts, beforeImpactsCopy) {
		t.Fatalf("earlier result changed after rename: before=%v snapshot=%v", beforeImpacts, beforeImpactsCopy)
	}
}

// Every rejection leaves the graph exactly as it was, and the error names the
// offending name where one is involved.
func TestRenameErrors(t *testing.T) {
	graph := map[string]*Lineage{}
	mustRegister(t, graph, "raw")
	mustRegister(t, graph, "mid", "raw")
	mustRegister(t, graph, "leaf", "mid")
	before := snapshot(graph)

	cases := []struct {
		name    string
		oldName string
		newName string
		want    string
	}{
		{"empty old name", "", "fresh", "name is required"},
		{"empty new name", "mid", "", "name is required"},
		{"unknown old name", "ghost", "fresh", "ghost"},
		{"new name taken", "mid", "leaf", "leaf"},
		{"new name taken by root", "mid", "raw", "raw"},
		{"unknown old against empty graph", "ghost", "fresh", "ghost"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			g := graph
			if tc.name == "unknown old against empty graph" {
				g = map[string]*Lineage{}
			}
			err := Rename(g, tc.oldName, tc.newName)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("Rename(%q, %q): want error containing %q, got %v",
					tc.oldName, tc.newName, tc.want, err)
			}
		})
	}

	if !reflect.DeepEqual(snapshot(graph), before) {
		t.Fatalf("rejected renames changed graph: before=%v after=%v", before, snapshot(graph))
	}
	assertEntry(t, graph, "mid", []string{"raw"}, []string{"leaf"})
	assertConsistent(t, graph)

	// A nil graph rejects any old name as not found.
	if err := Rename(nil, "ghost", "fresh"); err == nil || !strings.Contains(err.Error(), "ghost") {
		t.Fatalf("nil graph: want not-found error naming ghost, got %v", err)
	}
}

// Renaming a dataset to its own current name succeeds and changes nothing;
// the same request against an unregistered name still reports not found.
func TestRenameSameName(t *testing.T) {
	graph := map[string]*Lineage{}
	mustRegister(t, graph, "raw")
	mustRegister(t, graph, "mid", "raw")
	before := snapshot(graph)

	mustRename(t, graph, "mid", "mid")
	if !reflect.DeepEqual(snapshot(graph), before) {
		t.Fatalf("same-name rename changed graph: before=%v after=%v", before, snapshot(graph))
	}
	assertConsistent(t, graph)

	if err := Rename(graph, "ghost", "ghost"); err == nil || !strings.Contains(err.Error(), "ghost") {
		t.Fatalf("same-name rename of unknown dataset: want not-found error naming ghost, got %v", err)
	}
}

// Names match by exact registered value: renaming to a name that differs only
// in case from an existing dataset is a new name, not a conflict, and the old
// name does not match case-insensitively.
func TestRenameExactNameMatch(t *testing.T) {
	graph := map[string]*Lineage{}
	mustRegister(t, graph, "raw")
	mustRegister(t, graph, "mid", "raw")

	mustRename(t, graph, "mid", "MID")
	assertEntry(t, graph, "MID", []string{"raw"}, nil)
	assertConsistent(t, graph)

	if err := Rename(graph, "Mid", "fresh"); err == nil || !strings.Contains(err.Error(), "Mid") {
		t.Fatalf("case-insensitive old name: want not-found error naming Mid, got %v", err)
	}
	// "Raw" differs from the registered "raw" only by case: it is a free name,
	// not a conflict, so the rename succeeds and both datasets coexist.
	mustRename(t, graph, "MID", "Raw")
	assertEntry(t, graph, "Raw", []string{"raw"}, nil)
	assertEntry(t, graph, "raw", nil, []string{"Raw"})
	assertConsistent(t, graph)
}

// A rename can be followed by ordinary registrations under either name: the
// old name is free to register as a brand-new dataset, and the renamed node
// accepts new relationships under its new name.
func TestRenameThenRegister(t *testing.T) {
	graph := map[string]*Lineage{}
	mustRegister(t, graph, "raw")
	mustRegister(t, graph, "mid", "raw")
	mustRegister(t, graph, "leaf", "mid")

	mustRename(t, graph, "mid", "hub")

	// The renamed node keeps working under its new name.
	mustRegister(t, graph, "top", "hub")
	assertEntry(t, graph, "hub", []string{"raw"}, []string{"leaf", "top"})

	// The old name is free again and registers as an unrelated new dataset.
	mustRegister(t, graph, "mid")
	assertEntry(t, graph, "mid", nil, nil)
	assertConsistent(t, graph)
	if got, want := len(graph), 5; got != want {
		t.Fatalf("dataset count = %d, want %d", got, want)
	}
}

func mustUnregister(t *testing.T, graph map[string]*Lineage, name string) {
	t.Helper()
	if err := Unregister(graph, name); err != nil {
		t.Fatalf("Unregister(%s): %v", name, err)
	}
}

// The worked example from the spec: raw derives detail, detail derives report
// and view. Removing report deletes its registration, drops it from detail's
// children, and leaves view's position and dependencies intact. From raw the
// impact scope still reaches detail and view but no longer report; from view the
// sources still trace back to detail and raw with unchanged distances and
// explanation paths.
func TestUnregisterSpecExample(t *testing.T) {
	graph := buildRegisteredGraph(t, [][]string{
		{"raw"},
		{"detail", "raw"},
		{"report", "detail"},
		{"view", "detail"},
	})
	assertConsistent(t, graph)

	mustUnregister(t, graph, "report")

	if _, ok := graph["report"]; ok {
		t.Fatal("report registration still present after unregister")
	}
	assertEntry(t, graph, "detail", []string{"raw"}, []string{"view"})
	assertEntry(t, graph, "raw", nil, []string{"detail"})
	assertEntry(t, graph, "view", []string{"detail"}, nil)
	assertConsistent(t, graph)
	if got, want := len(graph), 3; got != want {
		t.Fatalf("dataset count = %d, want %d", got, want)
	}

	// From raw, detail and view are still downstream; report is gone.
	impacts := mustImpacts(t, graph, "raw")
	if got, want := impactNames(impacts), []string{"detail", "view"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("impacts from raw = %v, want %v", got, want)
	}
	assertImpact(t, impacts, "detail", 1, []string{"raw", "detail"})
	assertImpact(t, impacts, "view", 2, []string{"raw", "detail", "view"})

	// From view, detail and raw are still reachable with the same distances
	// and explanation paths as before the removal.
	upstreams := mustUpstreams(t, graph, "view")
	if got, want := upstreamNames(upstreams), []string{"detail", "raw"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("upstreams of view = %v, want %v", got, want)
	}
	assertUpstream(t, upstreams, "detail", 1, []string{"detail", "view"})
	assertUpstream(t, upstreams, "raw", 2, []string{"raw", "detail", "view"})

	// The removed name is answered as unregistered by both lineage queries.
	if _, err := Impacts(graph, "report"); err == nil || !strings.Contains(err.Error(), "report") {
		t.Fatalf("Impacts(report) after removal: want not-found error naming report, got %v", err)
	}
	if _, err := Upstreams(graph, "report"); err == nil || !strings.Contains(err.Error(), "report") {
		t.Fatalf("Upstreams(report) after removal: want not-found error naming report, got %v", err)
	}
}

// When the removed dataset depends on several direct upstreams, every one of
// them loses the reverse reference; the upstreams themselves and their other
// downstreams survive, and the remaining entries keep their relative order.
func TestUnregisterCleansAllDirectUpstreams(t *testing.T) {
	graph := buildRegisteredGraph(t, [][]string{
		{"srcA"},
		{"srcB"},
		{"srcC"},
		// srcA also feeds a sibling of the removed dataset.
		{"sibling", "srcA"},
		// report depends on all three sources.
		{"report", "srcA", "srcB", "srcC"},
	})
	assertConsistent(t, graph)

	mustUnregister(t, graph, "report")

	if _, ok := graph["report"]; ok {
		t.Fatal("report still present after unregister")
	}
	// srcA keeps sibling; report is gone from its children, with sibling's
	// relative position untouched.
	assertEntry(t, graph, "srcA", nil, []string{"sibling"})
	assertEntry(t, graph, "srcB", nil, nil)
	assertEntry(t, graph, "srcC", nil, nil)
	assertEntry(t, graph, "sibling", []string{"srcA"}, nil)
	assertConsistent(t, graph)
}

// Removing a leaf must preserve the relative order of the names left in each
// upstream's children list and must leave unrelated nodes' lists exactly as
// they were.
func TestUnregisterPreservesListOrderAndUnrelatedLists(t *testing.T) {
	graph := buildRegisteredGraph(t, [][]string{
		{"raw"},
		{"side"},
		{"keep1", "raw"},
		{"gone", "raw", "side"},
		{"keep2", "raw"},
		{"s1", "side"},
	})
	assertConsistent(t, graph)
	assertEntry(t, graph, "raw", nil, []string{"keep1", "gone", "keep2"})
	assertEntry(t, graph, "side", nil, []string{"gone", "s1"})
	before := snapshot(graph)

	mustUnregister(t, graph, "gone")
	if reflect.DeepEqual(snapshot(graph), before) {
		t.Fatal("removal changed nothing; expected gone to be deleted")
	}

	// raw's children keep their relative order with gone dropped.
	assertEntry(t, graph, "raw", nil, []string{"keep1", "keep2"})
	// side's list loses gone while s1 stays in place; side itself survives.
	assertEntry(t, graph, "side", nil, []string{"s1"})
	assertEntry(t, graph, "s1", []string{"side"}, nil)
	assertEntry(t, graph, "keep1", []string{"raw"}, nil)
	assertEntry(t, graph, "keep2", []string{"raw"}, nil)
	assertConsistent(t, graph)

	// Nodes not directly connected to gone are byte-for-byte unchanged.
	for _, name := range []string{"keep1", "keep2", "s1"} {
		if !sameStrings(graph[name].Parents, before.parents[name]) ||
			!sameStrings(graph[name].Children, before.children[name]) {
			t.Errorf("%s lists changed: parents %v->%v children %v->%v",
				name, before.parents[name], graph[name].Parents,
				before.children[name], graph[name].Children)
		}
	}
}

// An isolated dataset with neither upstreams nor downstreams is removed
// successfully.
func TestUnregisterIsolatedDataset(t *testing.T) {
	graph := map[string]*Lineage{}
	mustRegister(t, graph, "lonely")
	mustRegister(t, graph, "other")

	mustUnregister(t, graph, "lonely")
	if _, ok := graph["lonely"]; ok {
		t.Fatal("isolated dataset still present after unregister")
	}
	assertEntry(t, graph, "other", nil, nil)
	if got, want := len(graph), 1; got != want {
		t.Fatalf("dataset count = %d, want %d", got, want)
	}
	if _, err := Impacts(graph, "lonely"); err == nil || !strings.Contains(err.Error(), "lonely") {
		t.Fatalf("removed isolated dataset must query as unregistered, got %v", err)
	}
}

// A dataset that still has a direct downstream cannot be removed, regardless of
// its own upstreams. The error names it and states that downstreams remain, and
// the refusal is atomic: every node, edge and list order is preserved.
func TestUnregisterRejectedWithDownstreamIsAtomic(t *testing.T) {
	graph := buildRegisteredGraph(t, [][]string{
		{"raw"},
		{"detail", "raw"},
		{"report", "detail"},
		{"view", "report"},
	})
	before := snapshot(graph)

	// detail has report as a direct downstream and cannot be removed.
	err := Unregister(graph, "detail")
	if err == nil {
		t.Fatal("expected rejection for dataset with downstreams, got nil")
	}
	if !strings.Contains(err.Error(), "detail") {
		t.Errorf("error %q should name the dataset detail", err.Error())
	}
	if !strings.Contains(err.Error(), "downstream") {
		t.Errorf("error %q should state that direct downstreams remain", err.Error())
	}
	if !reflect.DeepEqual(snapshot(graph), before) {
		t.Fatalf("rejected unregister changed graph: before=%v after=%v", before, snapshot(graph))
	}
	assertEntry(t, graph, "raw", nil, []string{"detail"})
	assertEntry(t, graph, "detail", []string{"raw"}, []string{"report"})
	assertEntry(t, graph, "report", []string{"detail"}, []string{"view"})
	assertEntry(t, graph, "view", []string{"report"}, nil)
	assertConsistent(t, graph)

	// A root that feeds someone is likewise refused.
	if err := Unregister(graph, "raw"); err == nil ||
		!strings.Contains(err.Error(), "raw") || !strings.Contains(err.Error(), "downstream") {
		t.Fatalf("root with downstream: want naming error, got %v", err)
	}
	if !reflect.DeepEqual(snapshot(graph), before) {
		t.Fatalf("second rejection changed graph: before=%v after=%v", before, snapshot(graph))
	}

	// After the downstream itself is removed first, the formerly refused
	// dataset can now be removed; the refusal left no partial detachment behind.
	mustUnregister(t, graph, "view")
	assertEntry(t, graph, "report", []string{"detail"}, nil)
	mustUnregister(t, graph, "report")
	mustUnregister(t, graph, "detail")
	assertEntry(t, graph, "raw", nil, nil)
	if _, ok := graph["detail"]; ok {
		t.Fatal("detail should be removable once its downstream is gone")
	}
}

// Validation errors: empty name reports a missing name; a non-empty
// unregistered name is named in the error; empty and nil graphs behave the same
// as unregistered.
func TestUnregisterErrors(t *testing.T) {
	graph := map[string]*Lineage{}
	mustRegister(t, graph, "a")

	if err := Unregister(graph, ""); err == nil ||
		!strings.Contains(err.Error(), "name is required") {
		t.Fatalf("empty name: want required-name error, got %v", err)
	}
	if err := Unregister(graph, "ghost"); err == nil ||
		!strings.Contains(err.Error(), "ghost") {
		t.Fatalf("unknown name: want error naming ghost, got %v", err)
	}
	if err := Unregister(map[string]*Lineage{}, "ghost"); err == nil ||
		!strings.Contains(err.Error(), "ghost") {
		t.Fatalf("empty graph: want not-found error naming ghost, got %v", err)
	}
	if err := Unregister(nil, "ghost"); err == nil ||
		!strings.Contains(err.Error(), "ghost") {
		t.Fatalf("nil graph: want not-found error naming ghost, got %v", err)
	}

	// Nothing registered was affected by the failed requests.
	assertEntry(t, graph, "a", nil, nil)
	assertConsistent(t, graph)
}

// Names match by exact registered value and are case-sensitive.
func TestUnregisterExactNameMatch(t *testing.T) {
	graph := map[string]*Lineage{}
	mustRegister(t, graph, "raw")
	mustRegister(t, graph, "RAW")

	if err := Unregister(graph, "RaW"); err == nil ||
		!strings.Contains(err.Error(), "RaW") {
		t.Fatalf("case-insensitive match: want error naming RaW, got %v", err)
	}
	assertEntry(t, graph, "raw", nil, nil)
	assertEntry(t, graph, "RAW", nil, nil)

	mustUnregister(t, graph, "RAW")
	if _, ok := graph["RAW"]; ok {
		t.Fatal("RAW should be gone")
	}
	if _, ok := graph["raw"]; !ok {
		t.Fatal("raw, differing only by case, must remain registered")
	}
	assertConsistent(t, graph)
}

// After removing a leaf, the remaining lineage queries describe exactly the
// surviving graph, including a multi-source leaf whose sources keep their other
// downstreams and whose sibling leaf stays fully connected.
func TestUnregisterLeafQueriesReflectSurvivingGraph(t *testing.T) {
	graph := buildRegisteredGraph(t, [][]string{
		{"raw"},
		{"other"},
		{"detail", "raw"},
		{"report", "detail", "other"},
		{"view", "detail"},
	})
	assertConsistent(t, graph)

	mustUnregister(t, graph, "report")

	// raw still reaches detail and view; report is gone.
	fromRaw := mustImpacts(t, graph, "raw")
	if got, want := impactNames(fromRaw), []string{"detail", "view"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("impacts from raw = %v, want %v", got, want)
	}
	assertImpact(t, fromRaw, "view", 2, []string{"raw", "detail", "view"})

	// other loses report and answers with an empty (non-nil) downstream list.
	fromOther, err := Impacts(graph, "other")
	if err != nil {
		t.Fatalf("Impacts(other): %v", err)
	}
	if fromOther == nil || len(fromOther) != 0 {
		t.Fatalf("want empty non-nil list from other, got %v", fromOther)
	}

	// view still traces both hops back to raw.
	upstreams := mustUpstreams(t, graph, "view")
	assertUpstreamOnce(t, upstreams, "raw", 2, []string{"raw", "detail", "view"})
	if names := upstreamNames(upstreams); slices.Contains(names, "report") || slices.Contains(names, "other") {
		t.Fatalf("removed node or its unrelated source leaked into view's upstreams: %v", names)
	}
	assertConsistent(t, graph)
}
