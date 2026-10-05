package chainledger

import (
	"reflect"
	"strings"
	"testing"
)

func commonNames(found []CommonUpstream) []string {
	names := make([]string, len(found))
	for i, c := range found {
		names[i] = c.Dataset
	}
	return names
}

func mustCommon(t *testing.T, graph map[string]*Lineage, first, second string) []CommonUpstream {
	t.Helper()
	found, err := CommonUpstreams(graph, first, second)
	if err != nil {
		t.Fatalf("CommonUpstreams(%s, %s): %v", first, second, err)
	}
	return found
}

func assertCommon(t *testing.T, found []CommonUpstream, name string,
	wantDistFirst int, wantPathFirst []string, wantDistSecond int, wantPathSecond []string) {
	t.Helper()
	for _, c := range found {
		if c.Dataset != name {
			continue
		}
		if c.DistanceToFirst != wantDistFirst || c.DistanceToSecond != wantDistSecond {
			t.Errorf("common %s distances = (%d,%d), want (%d,%d)",
				name, c.DistanceToFirst, c.DistanceToSecond, wantDistFirst, wantDistSecond)
		}
		if !sameStrings(c.PathToFirst, wantPathFirst) {
			t.Errorf("common %s path-to-first = %v, want %v", name, c.PathToFirst, wantPathFirst)
		}
		if !sameStrings(c.PathToSecond, wantPathSecond) {
			t.Errorf("common %s path-to-second = %v, want %v", name, c.PathToSecond, wantPathSecond)
		}
		return
	}
	t.Fatalf("common source %q missing from %v", name, commonNames(found))
}

// assertEachCommonOnce checks every source appears exactly once in the result.
func assertEachCommonOnce(t *testing.T, found []CommonUpstream) {
	t.Helper()
	seen := map[string]int{}
	for _, c := range found {
		seen[c.Dataset]++
	}
	for name, count := range seen {
		if count > 1 {
			t.Fatalf("common source %q appears %d times in %v, want exactly once",
				name, count, commonNames(found))
		}
	}
}

// The worked example from the spec: raw derives a and b; a and b both take
// part in deriving left and right. Querying left and right returns a and b and
// excludes raw, because a and b are later common sources standing downstream of
// raw.
func TestCommonUpstreamsFrontierExcludesEarlierSharedRoot(t *testing.T) {
	graph := buildRegisteredGraph(t, [][]string{
		{"raw"},
		{"a", "raw"},
		{"b", "raw"},
		{"left", "a", "b"},
		{"right", "a", "b"},
	})

	found := mustCommon(t, graph, "left", "right")
	if got, want := commonNames(found), []string{"a", "b"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("common sources = %v, want %v (raw must be hidden behind a and b)", got, want)
	}
	assertEachCommonOnce(t, found)
	assertCommon(t, found, "a", 1, []string{"a", "left"}, 1, []string{"a", "right"})
	assertCommon(t, found, "b", 1, []string{"b", "left"}, 1, []string{"b", "right"})
}

// Even when raw participates directly in both targets' derivation and is fewer
// edges away, it is still excluded: distance never overrides the frontier rule.
func TestCommonUpstreamsDirectShorterEdgeDoesNotRescueHiddenSource(t *testing.T) {
	graph := buildRegisteredGraph(t, [][]string{
		{"raw"},
		{"a", "raw"},
		{"b", "raw"},
		// Both targets take raw directly (distance 1) AND through a and b.
		{"left", "raw", "a", "b"},
		{"right", "raw", "a", "b"},
	})

	found := mustCommon(t, graph, "left", "right")
	if got, want := commonNames(found), []string{"a", "b"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("common sources = %v, want %v (shorter raw edge must not un-hide it)", got, want)
	}
	assertEachCommonOnce(t, found)
	assertCommon(t, found, "a", 1, []string{"a", "left"}, 1, []string{"a", "right"})
	assertCommon(t, found, "b", 1, []string{"b", "left"}, 1, []string{"b", "right"})
}

// raw is hidden even when only ONE of the two later shared points lies on a
// path through it — a single later common source downstream of raw is enough.
func TestCommonUpstreamsOneLaterSharedPointStillHidesRoot(t *testing.T) {
	graph := buildRegisteredGraph(t, [][]string{
		{"raw"},
		{"a", "raw"},
		// left depends on a directly; right reaches a through mid.
		{"mid", "a"},
		{"left", "a"},
		{"right", "a", "mid"},
	})
	// right's parents a and mid: mid is downstream of a, but that does not make
	// the graph cyclic (a -> mid -> right, a -> left).

	found := mustCommon(t, graph, "left", "right")
	if got, want := commonNames(found), []string{"a"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("common sources = %v, want %v (raw hidden behind a alone)", got, want)
	}
	assertCommon(t, found, "a", 1, []string{"a", "left"}, 1, []string{"a", "right"})
}

// Querying a dataset with itself returns just that dataset, both distances
// zero and both paths only its own name.
func TestCommonUpstreamsSameTargetIsSelf(t *testing.T) {
	graph := buildRegisteredGraph(t, [][]string{
		{"raw"},
		{"a", "raw"},
		{"b", "raw"},
		{"only", "a", "b"},
	})

	found := mustCommon(t, graph, "only", "only")
	if got, want := commonNames(found), []string{"only"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("same target: common sources = %v, want %v", got, want)
	}
	assertCommon(t, found, "only", 0, []string{"only"}, 0, []string{"only"})

	// A target with ancestors queried against itself still reports only itself:
	// a and raw are common by closure but hidden behind the target.
	found = mustCommon(t, graph, "a", "a")
	if got, want := commonNames(found), []string{"a"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("same non-root target: common sources = %v, want %v", got, want)
	}
	assertCommon(t, found, "a", 0, []string{"a"}, 0, []string{"a"})

	// A root queried against itself is the same single zero-distance record.
	found = mustCommon(t, graph, "raw", "raw")
	if got, want := commonNames(found), []string{"raw"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("same root target: common sources = %v, want %v", got, want)
	}
	assertCommon(t, found, "raw", 0, []string{"raw"}, 0, []string{"raw"})
}

// When one target is an upstream of the other, the sole result is that
// upstream target with distance zero on its own side; its own ancestors are
// hidden behind it.
func TestCommonUpstreamsOneTargetUpstreamOfOther(t *testing.T) {
	graph := buildRegisteredGraph(t, [][]string{
		{"raw"},
		{"mid", "raw"},
		{"leaf", "mid"},
	})

	// First target is the upstream.
	found := mustCommon(t, graph, "mid", "leaf")
	if got, want := commonNames(found), []string{"mid"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("upstream first: common sources = %v, want %v", got, want)
	}
	assertCommon(t, found, "mid", 0, []string{"mid"}, 1, []string{"mid", "leaf"})

	// Swapped inputs swap the two sides; the result is otherwise the same.
	found = mustCommon(t, graph, "leaf", "mid")
	if got, want := commonNames(found), []string{"mid"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("upstream second: common sources = %v, want %v", got, want)
	}
	assertCommon(t, found, "mid", 1, []string{"mid", "leaf"}, 0, []string{"mid"})

	// Several levels apart: raw is hidden behind the nearer target mid.
	found = mustCommon(t, graph, "raw", "leaf")
	if got, want := commonNames(found), []string{"raw"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("root vs descendant: common sources = %v, want %v", got, want)
	}
	assertCommon(t, found, "raw", 0, []string{"raw"}, 2, []string{"raw", "mid", "leaf"})
}

// Two lineages with no shared source succeed with a non-nil empty list.
func TestCommonUpstreamsNoSharedSourcesEmptyButNonNil(t *testing.T) {
	graph := buildRegisteredGraph(t, [][]string{
		{"rootA"},
		{"a1", "rootA"},
		{"rootB"},
		{"b1", "rootB"},
		{"lonely"},
	})

	// Two independent roots.
	found, err := CommonUpstreams(graph, "rootA", "rootB")
	if err != nil {
		t.Fatalf("unrelated roots: %v", err)
	}
	if found == nil || len(found) != 0 {
		t.Fatalf("unrelated roots: want empty non-nil list, got %v", found)
	}

	// A root and an unrelated derived dataset.
	found, err = CommonUpstreams(graph, "lonely", "b1")
	if err != nil {
		t.Fatalf("lonely vs derived: %v", err)
	}
	if found == nil || len(found) != 0 {
		t.Fatalf("lonely vs derived: want empty non-nil list, got %v", found)
	}

	// Two leaves on separate branches.
	found = mustCommon(t, graph, "a1", "b1")
	if found == nil || len(found) != 0 {
		t.Fatalf("separate branches: want empty non-nil list, got %v", found)
	}
}

// Multiple frontier sources are returned together, not chosen by distance: a
// shared source that is itself a root and a separate shared intermediate point
// can coexist as long as no common node lies strictly downstream of another.
func TestCommonUpstreamsMultipleFrontierSourcesNotPickedByDistance(t *testing.T) {
	// Both targets share the deep branch through d -> c -> a, and independently
	// share the root b directly. Neither frontier point is downstream of the
	// other, so both are returned.
	graph := buildRegisteredGraph(t, [][]string{
		{"a"},
		{"b"},
		{"c", "a"},
		{"d", "c"},
		{"left", "b", "d"},
		{"right", "b", "d"},
	})

	found := mustCommon(t, graph, "left", "right")
	if got, want := commonNames(found), []string{"b", "d"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("common sources = %v, want %v", got, want)
	}
	assertEachCommonOnce(t, found)
	// b is at distance 1; d at distance 1 too, but a and c are hidden behind d.
	assertCommon(t, found, "b", 1, []string{"b", "left"}, 1, []string{"b", "right"})
	assertCommon(t, found, "d", 1, []string{"d", "left"}, 1, []string{"d", "right"})
}

// Two later common sources can have crossing routes: each one reaches the
// targets over different edges, and neither lies downstream of the other on a
// common path. Both are then kept (with asymmetric distances) while the shared
// root behind them is hidden.
func TestCommonUpstreamsCrossingFrontierBothKept(t *testing.T) {
	// s derives c and d. c reaches left via x and right directly; d reaches
	// left directly and right via y.
	//
	//   s ──> c ──> x ──> left       c ──> right
	//    └──> d ──────────> left     d ──> y ──> right
	graph := buildRegisteredGraph(t, [][]string{
		{"s"},
		{"c", "s"},
		{"d", "s"},
		{"x", "c"},
		{"y", "d"},
		{"left", "x", "d"},
		{"right", "c", "y"},
	})

	found := mustCommon(t, graph, "left", "right")
	if got, want := commonNames(found), []string{"c", "d"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("common sources = %v, want %v (s hidden behind both, c and d kept)", got, want)
	}
	assertEachCommonOnce(t, found)
	// No common child edge hides c or d: c's children (x, right) and d's
	// children (left, y) are all non-common.
	assertCommon(t, found, "c", 2, []string{"c", "x", "left"}, 1, []string{"c", "right"})
	assertCommon(t, found, "d", 1, []string{"d", "left"}, 2, []string{"d", "y", "right"})
}

// Results are ordered by source name in Go string order, never by distance or
// registration order.
func TestCommonUpstreamsOrderedByName(t *testing.T) {
	graph := buildRegisteredGraph(t, [][]string{
		{"zeta"},
		{"alpha"},
		{"mid"},
		{"left", "zeta", "alpha", "mid"},
		{"right", "mid", "zeta", "alpha"},
	})

	found := mustCommon(t, graph, "left", "right")
	if got, want := commonNames(found), []string{"alpha", "mid", "zeta"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("common source order = %v, want %v", got, want)
	}
	assertEachCommonOnce(t, found)
}

// The two sides can have different distances and different explanation paths:
// each side is traced independently from its own target.
func TestCommonUpstreamsAsymmetricSides(t *testing.T) {
	// shared reaches left over one hop and right over a longer detour.
	graph := buildRegisteredGraph(t, [][]string{
		{"shared"},
		{"x", "shared"},
		{"left", "shared"},
		{"right", "x"},
	})

	found := mustCommon(t, graph, "left", "right")
	if got, want := commonNames(found), []string{"shared"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("common sources = %v, want %v", got, want)
	}
	assertCommon(t, found, "shared",
		1, []string{"shared", "left"},
		2, []string{"shared", "x", "right"})

	// Swapping the query swaps both sides field by field.
	found = mustCommon(t, graph, "right", "left")
	assertCommon(t, found, "shared",
		2, []string{"shared", "x", "right"},
		1, []string{"shared", "left"})
}

// Among equally short routes on one side, the full-path lexicographic rule used
// by Upstreams applies: compare name by name from the source, not just the
// target's direct upstream.
func TestCommonUpstreamsFullPathTieBreak(t *testing.T) {
	// left reaches the shared root through a->z and b->c (both length 3);
	// right depends on the root alone. The shared frontier is the root, and its
	// path toward left must be the a/z route because a < b at hop 1.
	graph := buildRegisteredGraph(t, [][]string{
		{"source"},
		{"b", "source"}, // b-side registered first on purpose
		{"a", "source"},
		{"c", "b"},
		{"z", "a"},
		{"left", "c", "z"},
		{"right", "source"},
	})

	found := mustCommon(t, graph, "left", "right")
	if got, want := commonNames(found), []string{"source"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("common sources = %v, want %v", got, want)
	}
	assertCommon(t, found, "source",
		3, []string{"source", "a", "z", "left"},
		1, []string{"source", "right"})
}

// Paths are computed against the current edges: replacing a direct upstream
// lengthens the distance and re-derives the explanation on that side, while
// the other side and the frontier membership stay consistent.
func TestCommonUpstreamsReflectReregister(t *testing.T) {
	graph := buildRegisteredGraph(t, [][]string{
		{"raw"},
		{"a", "raw"},
		{"left", "raw", "a"},
		{"right", "raw"},
	})

	// Initially raw is a direct parent of both, but a is a later common source
	// on the left side only: a is not upstream of right, so raw is the sole
	// frontier (a is not even common).
	found := mustCommon(t, graph, "left", "right")
	if got, want := commonNames(found), []string{"raw"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("common sources = %v, want %v", got, want)
	}
	assertCommon(t, found, "raw", 1, []string{"raw", "left"}, 1, []string{"raw", "right"})

	// right now also derives through a: a becomes the later common source and
	// raw is hidden, even though the direct raw edges remain.
	mustRegister(t, graph, "right", "raw", "a")
	found = mustCommon(t, graph, "left", "right")
	if got, want := commonNames(found), []string{"a"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("after rewire common sources = %v, want %v", got, want)
	}
	assertCommon(t, found, "a", 1, []string{"a", "left"}, 1, []string{"a", "right"})
}

// Registration order and stored upstream-list order must not change results:
// two builds of the same graph with shuffled orders agree exactly.
func TestCommonUpstreamsOrderInvariance(t *testing.T) {
	builds := []map[string]*Lineage{
		buildRegisteredGraph(t, [][]string{
			{"raw"},
			{"b", "raw"}, // b-side first
			{"a", "raw"},
			{"right", "b", "a"}, // list order disagrees with name order
			{"left", "a", "b"},
		}),
		buildRegisteredGraph(t, [][]string{
			{"raw"},
			{"a", "raw"},
			{"b", "raw"},
			{"left", "b", "a"},
			{"right", "a", "b"},
		}),
	}

	first := mustCommon(t, builds[0], "left", "right")
	second := mustCommon(t, builds[1], "left", "right")
	if !reflect.DeepEqual(first, second) {
		t.Fatalf("equivalent graphs differ: %v vs %v", first, second)
	}
	if got, want := commonNames(first), []string{"a", "b"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("common sources = %v, want %v", got, want)
	}
}

// Targets are validated in input order: first target first, then second.
func TestCommonUpstreamsValidationOrder(t *testing.T) {
	graph := buildRegisteredGraph(t, [][]string{{"a"}})

	// Empty first target reports the missing name before the second is looked
	// at, even though the second is also invalid.
	found, err := CommonUpstreams(graph, "", "ghost")
	if err == nil || !strings.Contains(err.Error(), "name is required") {
		t.Fatalf("empty first: want required-name error, got %v", err)
	}
	if found != nil {
		t.Fatalf("empty first: want nil results, got %v", found)
	}

	// Unregistered first target reports that name before an invalid second.
	found, err = CommonUpstreams(graph, "ghost", "")
	if err == nil || !strings.Contains(err.Error(), "ghost") {
		t.Fatalf("unknown first: want error naming ghost, got %v", err)
	}
	if found != nil {
		t.Fatalf("unknown first: want nil results, got %v", found)
	}

	// Valid first, empty second.
	found, err = CommonUpstreams(graph, "a", "")
	if err == nil || !strings.Contains(err.Error(), "name is required") {
		t.Fatalf("empty second: want required-name error, got %v", err)
	}
	if found != nil {
		t.Fatalf("empty second: want nil results, got %v", found)
	}

	// Valid first, unregistered second: error names the second target.
	found, err = CommonUpstreams(graph, "a", "ghost")
	if err == nil || !strings.Contains(err.Error(), "ghost") {
		t.Fatalf("unknown second: want error naming ghost, got %v", err)
	}
	if found != nil {
		t.Fatalf("unknown second: want nil results, got %v", found)
	}
}

// Empty and nil graphs behave like unregistered-name errors for non-empty
// targets, and an empty name is still a missing-name error.
func TestCommonUpstreamsEmptyAndNilGraph(t *testing.T) {
	for name, graph := range map[string]map[string]*Lineage{
		"empty": {},
		"nil":   nil,
	} {
		found, err := CommonUpstreams(graph, "x", "y")
		if err == nil || !strings.Contains(err.Error(), "x") {
			t.Fatalf("%s graph: want error naming first target x, got %v", name, err)
		}
		if found != nil {
			t.Fatalf("%s graph: want nil results, got %v", name, found)
		}

		// Empty first name against an empty/nil graph is still the missing-name
		// error, never a not-found error.
		found, err = CommonUpstreams(graph, "", "y")
		if err == nil || !strings.Contains(err.Error(), "name is required") {
			t.Fatalf("%s graph, empty first: want required-name error, got %v", name, err)
		}
		if found != nil {
			t.Fatalf("%s graph, empty first: want nil results, got %v", name, found)
		}
	}
}

// Names match by exact registered value, case-sensitive.
func TestCommonUpstreamsExactNameMatch(t *testing.T) {
	graph := buildRegisteredGraph(t, [][]string{
		{"raw"},
		{"RAW", "raw"},
		{"raw-other", "raw"},
	})

	if _, err := CommonUpstreams(graph, "RaW", "raw"); err == nil ||
		!strings.Contains(err.Error(), "RaW") {
		t.Fatalf("case-insensitive first target: want error naming RaW, got %v", err)
	}
	if _, err := CommonUpstreams(graph, "RAW", "raW"); err == nil ||
		!strings.Contains(err.Error(), "raW") {
		t.Fatalf("case-insensitive second target: want error naming raW, got %v", err)
	}

	// RAW and raw-other share only raw; the differently-cased names are distinct.
	found := mustCommon(t, graph, "RAW", "raw-other")
	if got, want := commonNames(found), []string{"raw"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("common sources = %v, want %v", got, want)
	}
	assertCommon(t, found, "raw", 1, []string{"raw", "RAW"}, 1, []string{"raw", "raw-other"})
}

// A query never mutates the graph, and mutating returned records or paths can
// reach neither the graph, the other side's path, other records, nor later
// queries.
func TestCommonUpstreamsReadOnlyAndIsolated(t *testing.T) {
	graph := buildRegisteredGraph(t, [][]string{
		{"raw"},
		{"a", "raw"},
		{"b", "raw"},
		{"x", "a"},
		{"left", "a", "b"},
		{"right", "b", "x"},
	})
	before := snapshot(graph)

	found, err := CommonUpstreams(graph, "left", "right")
	if err != nil {
		t.Fatalf("CommonUpstreams: %v", err)
	}
	if !reflect.DeepEqual(snapshot(graph), before) {
		t.Fatalf("query changed graph: before=%v after=%v", before, snapshot(graph))
	}

	// Abuse every returned slice, then confirm the graph and a repeat query are
	// untouched and the two sides did not share storage.
	found[0].PathToFirst[0] = "tampered-1"
	found[0].PathToFirst = append(found[0].PathToFirst, "extra")
	found[0].PathToSecond[0] = "tampered-2"
	found[0].Dataset = "tampered"
	found[0].DistanceToFirst = 99
	found[0].DistanceToSecond = 99
	if !reflect.DeepEqual(snapshot(graph), before) {
		t.Fatalf("mutating results changed graph: before=%v after=%v", before, snapshot(graph))
	}

	// A failed query is also read-only.
	if _, err := CommonUpstreams(graph, "missing", "left"); err == nil {
		t.Fatal("expected not-found error for first target")
	}
	if _, err := CommonUpstreams(graph, "left", "missing"); err == nil {
		t.Fatal("expected not-found error for second target")
	}
	if !reflect.DeepEqual(snapshot(graph), before) {
		t.Fatalf("failed queries changed graph: before=%v after=%v", before, snapshot(graph))
	}
	assertConsistent(t, graph)

	// A repeat query still returns the original, un-tampered content.
	fresh := mustCommon(t, graph, "left", "right")
	for _, c := range fresh {
		for _, step := range append(append([]string(nil), c.PathToFirst...), c.PathToSecond...) {
			if strings.Contains(step, "tampered") {
				t.Fatalf("tampered path leaked into fresh query: %v", fresh)
			}
		}
		if c.Dataset == "tampered" || c.DistanceToFirst == 99 || c.DistanceToSecond == 99 {
			t.Fatalf("tampered record leaked into fresh query: %v", fresh)
		}
	}
}

// A result taken before a re-registration keeps its original paths; only a
// re-query reflects the new edges.
func TestCommonUpstreamsOldResultStableAfterReregister(t *testing.T) {
	graph := buildRegisteredGraph(t, [][]string{
		{"raw"},
		{"a", "raw"},
		{"b", "raw"},
		{"left", "a", "b"},
		{"right", "a", "b"},
	})

	before := mustCommon(t, graph, "left", "right")
	beforeCopy := make([]CommonUpstream, len(before))
	for i, c := range before {
		beforeCopy[i] = CommonUpstream{
			Dataset:          c.Dataset,
			DistanceToFirst:  c.DistanceToFirst,
			PathToFirst:      append([]string(nil), c.PathToFirst...),
			DistanceToSecond: c.DistanceToSecond,
			PathToSecond:     append([]string(nil), c.PathToSecond...),
		}
	}

	mustRegister(t, graph, "right", "a")
	if !reflect.DeepEqual(before, beforeCopy) {
		t.Fatalf("earlier result changed after re-register: before=%v snapshot=%v",
			before, beforeCopy)
	}
}

// Every returned path starts with the source and ends at the queried target,
// and its length matches the reported distance plus one.
func TestCommonUpstreamsPathShape(t *testing.T) {
	graph := buildRegisteredGraph(t, [][]string{
		{"r"},
		{"a", "r"},
		{"b", "a"},
		{"left", "b"},
		{"x", "r"},
		{"right", "x", "b"},
	})

	found := mustCommon(t, graph, "left", "right")
	for _, c := range found {
		if c.PathToFirst[0] != c.Dataset || c.PathToFirst[len(c.PathToFirst)-1] != "left" {
			t.Errorf("source %s path-to-first = %v must run from source to left", c.Dataset, c.PathToFirst)
		}
		if c.PathToSecond[0] != c.Dataset || c.PathToSecond[len(c.PathToSecond)-1] != "right" {
			t.Errorf("source %s path-to-second = %v must run from source to right", c.Dataset, c.PathToSecond)
		}
		if len(c.PathToFirst) != c.DistanceToFirst+1 {
			t.Errorf("source %s first path length %d != distance %d+1",
				c.Dataset, len(c.PathToFirst), c.DistanceToFirst)
		}
		if len(c.PathToSecond) != c.DistanceToSecond+1 {
			t.Errorf("source %s second path length %d != distance %d+1",
				c.Dataset, len(c.PathToSecond), c.DistanceToSecond)
		}
	}
}
