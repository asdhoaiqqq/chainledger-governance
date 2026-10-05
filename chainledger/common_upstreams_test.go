package chainledger

import (
	"reflect"
	"slices"
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

func findCommon(t *testing.T, found []CommonUpstream, name string) CommonUpstream {
	t.Helper()
	for _, c := range found {
		if c.Dataset == name {
			return c
		}
	}
	t.Fatalf("common source %q missing from %v", name, commonNames(found))
	return CommonUpstream{}
}

func assertCommonOnce(t *testing.T, found []CommonUpstream, name string) {
	t.Helper()
	count := 0
	for _, c := range found {
		if c.Dataset == name {
			count++
		}
	}
	if count != 1 {
		t.Fatalf("common source %q appears %d times in %v, want exactly once", name, count, commonNames(found))
	}
}

// The worked scenario from the spec: raw derives a and b, and both feed left
// and right. a and b are the nearest common sources; raw is excluded even
// though both targets derive from it.
func TestCommonUpstreamsDiamondExcludesOlderSource(t *testing.T) {
	graph := buildRegisteredGraph(t, [][]string{
		{"raw"},
		{"b", "raw"},
		{"a", "raw"},
		{"left", "b", "a"}, // list order must not decide anything
		{"right", "a", "b"},
	})
	assertConsistent(t, graph)

	found, err := CommonUpstreams(graph, "left", "right")
	if err != nil {
		t.Fatalf("CommonUpstreams(left, right): %v", err)
	}
	if got, want := commonNames(found), []string{"a", "b"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("common sources = %v, want %v", got, want)
	}
	assertCommonOnce(t, found, "a")
	assertCommonOnce(t, found, "b")
	a := findCommon(t, found, "a")
	if a.DistanceToFirst != 1 || a.DistanceToSecond != 1 {
		t.Errorf("a distances = (%d, %d), want (1, 1)", a.DistanceToFirst, a.DistanceToSecond)
	}
	if !sameStrings(a.PathToFirst, []string{"a", "left"}) || !sameStrings(a.PathToSecond, []string{"a", "right"}) {
		t.Errorf("a paths = %v, %v; want [a left], [a right]", a.PathToFirst, a.PathToSecond)
	}
	b := findCommon(t, found, "b")
	if b.DistanceToFirst != 1 || b.DistanceToSecond != 1 {
		t.Errorf("b distances = (%d, %d), want (1, 1)", b.DistanceToFirst, b.DistanceToSecond)
	}
	if !sameStrings(b.PathToFirst, []string{"b", "left"}) || !sameStrings(b.PathToSecond, []string{"b", "right"}) {
		t.Errorf("b paths = %v, %v; want [b left], [b right]", b.PathToFirst, b.PathToSecond)
	}

	// Even with raw feeding both targets directly over shorter edges, raw must
	// stay excluded: a and b are common sources strictly downstream of it.
	mustRegister(t, graph, "left", "raw", "b", "a")
	mustRegister(t, graph, "right", "raw", "a", "b")
	found, err = CommonUpstreams(graph, "left", "right")
	if err != nil {
		t.Fatalf("CommonUpstreams after direct raw edges: %v", err)
	}
	if got, want := commonNames(found), []string{"a", "b"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("with direct raw edges: common sources = %v, want %v", got, want)
	}
}

// Swapping the target arguments swaps the two sides of each record but keeps
// the source set and its order.
func TestCommonUpstreamsTargetArgumentOrderSwapsSides(t *testing.T) {
	graph := buildRegisteredGraph(t, [][]string{
		{"raw"},
		{"a", "raw"},
		{"b", "raw"},
		{"left", "b", "a"},
		{"right", "a", "b"},
	})

	forward, err := CommonUpstreams(graph, "left", "right")
	if err != nil {
		t.Fatalf("CommonUpstreams: %v", err)
	}
	reverse, err := CommonUpstreams(graph, "right", "left")
	if err != nil {
		t.Fatalf("CommonUpstreams reversed: %v", err)
	}
	if !reflect.DeepEqual(commonNames(forward), commonNames(reverse)) {
		t.Fatalf("source set changed with argument order: %v vs %v", commonNames(forward), commonNames(reverse))
	}
	for i := range forward {
		f, r := forward[i], reverse[i]
		if f.Dataset != r.Dataset ||
			f.DistanceToFirst != r.DistanceToSecond || f.DistanceToSecond != r.DistanceToFirst ||
			!reflect.DeepEqual(f.PathToFirst, r.PathToSecond) || !reflect.DeepEqual(f.PathToSecond, r.PathToFirst) {
			t.Fatalf("record %s does not mirror under swapped targets:\n%+v\n%+v", f.Dataset, f, r)
		}
	}
}

// The two targets name the same dataset: it alone is returned at distance 0
// on both sides even when it sits deep in a shared lineage.
func TestCommonUpstreamsSameTargetIsSoleZeroDistanceResult(t *testing.T) {
	graph := buildRegisteredGraph(t, [][]string{
		{"raw"},
		{"a", "raw"},
		{"b", "raw"},
		{"left", "b", "a"},
	})

	found, err := CommonUpstreams(graph, "left", "left")
	if err != nil {
		t.Fatalf("CommonUpstreams(left, left): %v", err)
	}
	if got, want := commonNames(found), []string{"left"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("same target: common sources = %v, want %v", got, want)
	}
	only := found[0]
	if only.DistanceToFirst != 0 || only.DistanceToSecond != 0 {
		t.Errorf("same target distances = (%d, %d), want (0, 0)", only.DistanceToFirst, only.DistanceToSecond)
	}
	if !sameStrings(only.PathToFirst, []string{"left"}) || !sameStrings(only.PathToSecond, []string{"left"}) {
		t.Errorf("same target paths = %v, %v; want [left] twice", only.PathToFirst, only.PathToSecond)
	}
}

// One target is upstream of the other: the upstream target is the sole result,
// at distance 0 on its own side; older shared sources stay excluded.
func TestCommonUpstreamsOneTargetUpstreamOfOther(t *testing.T) {
	graph := buildRegisteredGraph(t, [][]string{
		{"raw"},
		{"a", "raw"},
		{"b", "raw"},
		{"mid", "a", "b"},
		{"leaf", "mid"},
	})

	found, err := CommonUpstreams(graph, "mid", "leaf")
	if err != nil {
		t.Fatalf("CommonUpstreams(mid, leaf): %v", err)
	}
	if got, want := commonNames(found), []string{"mid"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("upstream target: common sources = %v, want %v", got, want)
	}
	up := found[0]
	if up.DistanceToFirst != 0 || up.DistanceToSecond != 1 {
		t.Errorf("distances = (%d, %d), want (0, 1)", up.DistanceToFirst, up.DistanceToSecond)
	}
	if !sameStrings(up.PathToFirst, []string{"mid"}) || !sameStrings(up.PathToSecond, []string{"mid", "leaf"}) {
		t.Errorf("paths = %v, %v; want [mid] and [mid leaf]", up.PathToFirst, up.PathToSecond)
	}

	// Reversed arguments move the zero distance to the other side.
	found, err = CommonUpstreams(graph, "leaf", "mid")
	if err != nil {
		t.Fatalf("CommonUpstreams(leaf, mid): %v", err)
	}
	if got, want := commonNames(found), []string{"mid"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("reversed: common sources = %v, want %v", got, want)
	}
	up = found[0]
	if up.DistanceToFirst != 1 || up.DistanceToSecond != 0 {
		t.Errorf("reversed distances = (%d, %d), want (1, 0)", up.DistanceToFirst, up.DistanceToSecond)
	}
	if !sameStrings(up.PathToFirst, []string{"mid", "leaf"}) || !sameStrings(up.PathToSecond, []string{"mid"}) {
		t.Errorf("reversed paths = %v, %v", up.PathToFirst, up.PathToSecond)
	}
}

// Two registered targets sharing no ancestry succeed with a non-nil empty
// list. Independent branches feeding separate leaves, and two bare roots,
// both qualify.
func TestCommonUpstreamsNoSharedSourceReturnsEmpty(t *testing.T) {
	graph := buildRegisteredGraph(t, [][]string{
		{"p"},
		{"q"},
		{"left", "p"},
		{"right", "q"},
		{"lonely"},
	})

	for _, pair := range [][2]string{{"left", "right"}, {"p", "q"}, {"left", "lonely"}, {"lonely", "p"}} {
		found, err := CommonUpstreams(graph, pair[0], pair[1])
		if err != nil {
			t.Fatalf("CommonUpstreams(%s, %s): %v", pair[0], pair[1], err)
		}
		if found == nil || len(found) != 0 {
			t.Fatalf("CommonUpstreams(%s, %s) = %v, want non-nil empty list", pair[0], pair[1], found)
		}
	}
}

// Nearest sources keep their independent shortest distances per target, and
// results order by source name regardless of the two distances.
func TestCommonUpstreamsPerTargetDistancesAndNameOrder(t *testing.T) {
	graph := buildRegisteredGraph(t, [][]string{
		{"raw"},
		{"a", "raw"},
		{"b", "raw"},
		{"k", "a"},
		{"zeta", "a", "b"}, // a reaches zeta directly ...
		{"alpha", "k", "b"},
	})
	// Targets are alpha and zeta. a reaches both (alpha via k), b reaches both
	// directly; raw sits behind the two of them and is excluded. Names sort
	// alpha-source before zeta-source only by dataset name, not by distance.

	found, err := CommonUpstreams(graph, "alpha", "zeta")
	if err != nil {
		t.Fatalf("CommonUpstreams: %v", err)
	}
	if got, want := commonNames(found), []string{"a", "b"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("common sources = %v, want %v", got, want)
	}
	a := findCommon(t, found, "a")
	if a.DistanceToFirst != 2 || a.DistanceToSecond != 1 {
		t.Errorf("a distances = (%d, %d), want (2, 1)", a.DistanceToFirst, a.DistanceToSecond)
	}
	if !sameStrings(a.PathToFirst, []string{"a", "k", "alpha"}) || !sameStrings(a.PathToSecond, []string{"a", "zeta"}) {
		t.Errorf("a paths = %v, %v", a.PathToFirst, a.PathToSecond)
	}
	b := findCommon(t, found, "b")
	if b.DistanceToFirst != 1 || b.DistanceToSecond != 1 {
		t.Errorf("b distances = (%d, %d), want (1, 1)", b.DistanceToFirst, b.DistanceToSecond)
	}
	if !sameStrings(b.PathToFirst, []string{"b", "alpha"}) || !sameStrings(b.PathToSecond, []string{"b", "zeta"}) {
		t.Errorf("b paths = %v, %v", b.PathToFirst, b.PathToSecond)
	}
}

// When a common source reaches one target over several equally short routes,
// the same full-path tie break as Upstreams applies: names compared hop by hop
// from the source, never just the target's direct upstream.
func TestCommonUpstreamsFullPathTieBreak(t *testing.T) {
	graph := buildRegisteredGraph(t, [][]string{
		{"s"},
		{"x2", "s"}, // larger name registered/listed first on purpose
		{"x1", "s"},
		{"left", "x2", "x1"},
		{"right", "s"},
	})
	// Only s is common. left sees it at distance 2 through either x; right at
	// distance 1. The explanation to left must take the x1 route even though
	// x2 is listed ahead of x1.

	found, err := CommonUpstreams(graph, "left", "right")
	if err != nil {
		t.Fatalf("CommonUpstreams: %v", err)
	}
	if got, want := commonNames(found), []string{"s"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("common sources = %v, want %v", got, want)
	}
	s := found[0]
	if s.DistanceToFirst != 2 || s.DistanceToSecond != 1 {
		t.Errorf("distances = (%d, %d), want (2, 1)", s.DistanceToFirst, s.DistanceToSecond)
	}
	if !sameStrings(s.PathToFirst, []string{"s", "x1", "left"}) {
		t.Errorf("path to left = %v, want [s x1 left] (full-path tie break)", s.PathToFirst)
	}
	if !sameStrings(s.PathToSecond, []string{"s", "right"}) {
		t.Errorf("path to right = %v, want [s right]", s.PathToSecond)
	}
}

// The same final graph built through different registration and upstream-list
// orders yields byte-identical results.
func TestCommonUpstreamsOrderInvariance(t *testing.T) {
	sequences := [][][]string{
		{
			{"raw"},
			{"b", "raw"},
			{"a", "raw"},
			{"left", "b", "a"},
			{"right", "a", "b"},
			{"unrelated"},
		},
		{
			{"unrelated"},
			{"raw"},
			{"a", "raw"},
			{"b", "raw"},
			{"right", "b", "a"},
			{"left", "a", "b"},
		},
	}
	var want []CommonUpstream
	for i, seq := range sequences {
		graph := buildRegisteredGraph(t, seq)
		found, err := CommonUpstreams(graph, "left", "right")
		if err != nil {
			t.Fatalf("sequence %d: %v", i, err)
		}
		if i == 0 {
			want = found
			continue
		}
		if !reflect.DeepEqual(found, want) {
			t.Fatalf("sequence %d: %v, want %v", i, found, want)
		}
	}
}

// Targets validate in argument order: an empty name is a missing-name error,
// an unregistered name is reported by value; failures return nil results and
// touch nothing, including against empty or nil graphs.
func TestCommonUpstreamsErrors(t *testing.T) {
	graph := buildRegisteredGraph(t, [][]string{
		{"a"},
		{"b"},
	})

	cases := []struct {
		name        string
		first       string
		second      string
		graph       map[string]*Lineage
		wantMessage string
	}{
		{"first empty", "", "b", graph, "name is required"},
		{"second empty", "a", "", graph, "name is required"},
		{"both empty", "", "", graph, "name is required"},
		{"first unknown", "ghost", "b", graph, "dataset not found: ghost"},
		{"second unknown", "a", "ghost", graph, "dataset not found: ghost"},
		{"unknown first checked before empty second", "ghost", "", graph, "dataset not found: ghost"},
		{"empty first checked before unknown second", "", "ghost", graph, "name is required"},
		{"unknown against empty graph", "ghost", "b", map[string]*Lineage{}, "dataset not found: ghost"},
		{"unknown first against nil graph", "ghost", "b", nil, "dataset not found: ghost"},
		{"empty second against nil graph", "ghost", "", nil, "dataset not found: ghost"},
		{"empty against empty graph", "", "b", map[string]*Lineage{}, "name is required"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			found, err := CommonUpstreams(tc.graph, tc.first, tc.second)
			if err == nil || !strings.Contains(err.Error(), tc.wantMessage) {
				t.Fatalf("CommonUpstreams(%q, %q): want error containing %q, got %v",
					tc.first, tc.second, tc.wantMessage, err)
			}
			if found != nil {
				t.Fatalf("want nil results on error, got %v", found)
			}
		})
	}
}

// Names match by exact registered value.
func TestCommonUpstreamsExactNameMatch(t *testing.T) {
	graph := buildRegisteredGraph(t, [][]string{
		{"raw"},
		{"RAW", "raw"},
		{"leaf", "RAW"},
	})

	if _, err := CommonUpstreams(graph, "RaW", "leaf"); err == nil ||
		!strings.Contains(err.Error(), "RaW") {
		t.Fatalf("case-insensitive first target: want error naming RaW, got %v", err)
	}
	if _, err := CommonUpstreams(graph, "leaf", "RaW"); err == nil ||
		!strings.Contains(err.Error(), "RaW") {
		t.Fatalf("case-insensitive second target: want error naming RaW, got %v", err)
	}

	found, err := CommonUpstreams(graph, "RAW", "leaf")
	if err != nil {
		t.Fatalf("CommonUpstreams(RAW, leaf): %v", err)
	}
	if got, want := commonNames(found), []string{"RAW"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("common sources = %v, want %v", got, want)
	}
	up := found[0]
	if up.DistanceToFirst != 0 || up.DistanceToSecond != 1 {
		t.Errorf("distances = (%d, %d), want (0, 1)", up.DistanceToFirst, up.DistanceToSecond)
	}
	if !sameStrings(up.PathToFirst, []string{"RAW"}) || !sameStrings(up.PathToSecond, []string{"RAW", "leaf"}) {
		t.Errorf("paths = %v, %v", up.PathToFirst, up.PathToSecond)
	}
}

// A query never mutates the graph; returned paths are independent of the
// graph, of each other and of later queries.
func TestCommonUpstreamsReadOnlyAndIsolated(t *testing.T) {
	graph := buildRegisteredGraph(t, [][]string{
		{"raw"},
		{"a", "raw"},
		{"b", "raw"},
		{"left", "b", "a"},
		{"right", "a", "b"},
	})
	before := snapshot(graph)

	found, err := CommonUpstreams(graph, "left", "right")
	if err != nil {
		t.Fatalf("CommonUpstreams: %v", err)
	}
	if !reflect.DeepEqual(snapshot(graph), before) {
		t.Fatalf("query changed graph: before=%v after=%v", before, snapshot(graph))
	}

	// Abuse every returned slice, then confirm the graph and a repeated query
	// stay untouched.
	for i := range found {
		found[i].PathToFirst[0] = "tampered"
		found[i].PathToFirst = append(found[i].PathToFirst, "extra")
		found[i].PathToSecond[0] = "tampered"
		found[i].Dataset = "tampered"
		found[i].DistanceToFirst = 99
		found[i].DistanceToSecond = 99
	}
	if !reflect.DeepEqual(snapshot(graph), before) {
		t.Fatalf("mutating results changed graph: before=%v after=%v", before, snapshot(graph))
	}
	fresh, err := CommonUpstreams(graph, "left", "right")
	if err != nil {
		t.Fatalf("repeat CommonUpstreams: %v", err)
	}
	a := findCommon(t, fresh, "a")
	if !sameStrings(a.PathToFirst, []string{"a", "left"}) || !sameStrings(a.PathToSecond, []string{"a", "right"}) {
		t.Fatalf("repeated query paths changed: %v, %v", a.PathToFirst, a.PathToSecond)
	}

	// The two sides of one record do not share a backing array either.
	same, err := CommonUpstreams(graph, "left", "left")
	if err != nil {
		t.Fatalf("CommonUpstreams(left, left): %v", err)
	}
	same[0].PathToFirst[0] = "tampered"
	if same[0].PathToSecond[0] != "left" {
		t.Fatalf("PathToSecond aliases PathToFirst: %v", same[0].PathToSecond)
	}

	// A failed query is also read-only.
	if _, err := CommonUpstreams(graph, "missing", "left"); err == nil {
		t.Fatal("expected not-found error")
	}
	if !reflect.DeepEqual(snapshot(graph), before) {
		t.Fatalf("failed query changed graph: before=%v after=%v", before, snapshot(graph))
	}
	assertConsistent(t, graph)
}

// The descendant check follows child edges through datasets that are not
// themselves common: a common source with a common descendant at any depth,
// however many non-common datasets lie between them, is dropped.
func TestCommonUpstreamsDescendantCheckCrossesNonCommonNodes(t *testing.T) {
	// x and b merge into m; m alone feeds both targets. Every ancestor of m is
	// common, but a and b each reach the common node m (a through the non-common
	// x, b directly), so only m survives.
	graph := buildRegisteredGraph(t, [][]string{
		{"raw"},
		{"a", "raw"},
		{"x", "a"},
		{"b", "raw"},
		{"m", "x", "b"},
		{"left", "m"},
		{"right", "m"},
	})

	found, err := CommonUpstreams(graph, "left", "right")
	if err != nil {
		t.Fatalf("CommonUpstreams: %v", err)
	}
	if got, want := commonNames(found), []string{"m"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("common sources = %v, want %v", got, want)
	}
	m := found[0]
	if m.DistanceToFirst != 1 || m.DistanceToSecond != 1 {
		t.Errorf("m distances = (%d, %d), want (1, 1)", m.DistanceToFirst, m.DistanceToSecond)
	}

	// Mirror case where a reaches the first target only through the non-common
	// node x, while neither x nor that target is common. a must stay: walking
	// past x must not conjure a disqualifying descendant.
	graph = buildRegisteredGraph(t, [][]string{
		{"raw"},
		{"a", "raw"},
		{"x", "a"},
		{"b", "raw"},
		{"left", "x", "b"},
		{"right", "a", "b"},
	})
	found, err = CommonUpstreams(graph, "left", "right")
	if err != nil {
		t.Fatalf("CommonUpstreams: %v", err)
	}
	if got, want := commonNames(found), []string{"a", "b"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("common sources = %v, want %v (non-common x must not disqualify a)", got, want)
	}
	a := findCommon(t, found, "a")
	if a.DistanceToFirst != 2 || a.DistanceToSecond != 1 {
		t.Errorf("a distances = (%d, %d), want (2, 1)", a.DistanceToFirst, a.DistanceToSecond)
	}
	if !sameStrings(a.PathToFirst, []string{"a", "x", "left"}) || !sameStrings(a.PathToSecond, []string{"a", "right"}) {
		t.Errorf("a paths = %v, %v", a.PathToFirst, a.PathToSecond)
	}
}

// Every explanation path runs along real derivation edges from the source to
// the target and includes both endpoints.
func TestCommonUpstreamsPathsFollowEdges(t *testing.T) {
	graph := buildRegisteredGraph(t, [][]string{
		{"raw"},
		{"a", "raw"},
		{"b", "raw"},
		{"left", "b", "a"},
		{"right", "a", "b"},
	})

	found, err := CommonUpstreams(graph, "left", "right")
	if err != nil {
		t.Fatalf("CommonUpstreams: %v", err)
	}
	for _, c := range found {
		for _, path := range [][]string{c.PathToFirst, c.PathToSecond} {
			if len(path) == 0 || path[0] != c.Dataset {
				t.Fatalf("%s path %v does not start at the source", c.Dataset, path)
			}
			for i := 1; i < len(path); i++ {
				parent, child := path[i-1], path[i]
				if !slices.Contains(graph[child].Parents, parent) {
					t.Fatalf("%s path %v uses a non-existent edge %s -> %s", c.Dataset, path, parent, child)
				}
			}
		}
		if end := c.PathToFirst[len(c.PathToFirst)-1]; end != "left" {
			t.Fatalf("%s path to first target ends at %q, want left", c.Dataset, end)
		}
		if end := c.PathToSecond[len(c.PathToSecond)-1]; end != "right" {
			t.Fatalf("%s path to second target ends at %q, want right", c.Dataset, end)
		}
	}
}
