package chainledger

import (
	"reflect"
	"slices"
	"strings"
	"testing"
)

// mustImpactsWithCutoffs runs a cutoff downstream query, failing the test on
// error.
func mustImpactsWithCutoffs(t *testing.T, graph map[string]*Lineage, origin string, cutoffs ...string) []Impact {
	t.Helper()
	impacts, err := ImpactsWithCutoffs(graph, origin, cutoffs)
	if err != nil {
		t.Fatalf("ImpactsWithCutoffs(%s, %v): %v", origin, cutoffs, err)
	}
	return impacts
}

// The worked example from the spec: source derives a and b; a derives report
// directly while b reaches report through mid; report derives view. With a as
// the cutoff, a is still reported at distance 1 but nothing propagates through
// it, so report is reached around it via b and mid at distance 3 and view at 4,
// both explained by the route that avoided a.
func TestImpactsWithCutoffsSpecExampleOneCutoff(t *testing.T) {
	graph := buildRegisteredGraph(t, [][]string{
		{"source"},
		{"a", "source"},
		{"b", "source"},
		{"mid", "b"},
		{"report", "a", "mid"},
		{"view", "report"},
	})
	assertConsistent(t, graph)

	// Baseline without cutoffs: report is distance 2 through a.
	full := mustImpacts(t, graph, "source")
	assertImpactOnce(t, full, "report", 2, []string{"source", "a", "report"})
	assertImpactOnce(t, full, "view", 3, []string{"source", "a", "report", "view"})

	impacts := mustImpactsWithCutoffs(t, graph, "source", "a")
	want := []Impact{
		{Dataset: "a", Distance: 1, Path: []string{"source", "a"}},
		{Dataset: "b", Distance: 1, Path: []string{"source", "b"}},
		{Dataset: "mid", Distance: 2, Path: []string{"source", "b", "mid"}},
		{Dataset: "report", Distance: 3, Path: []string{"source", "b", "mid", "report"}},
		{Dataset: "view", Distance: 4, Path: []string{"source", "b", "mid", "report", "view"}},
	}
	if !reflect.DeepEqual(impacts, want) {
		t.Fatalf("cutoff a: impacts = %v, want %v", impacts, want)
	}
	// The cutoff itself is present, but its truncated route to report/view is
	// not reused: the bypass distances and paths come from the allowed routes.
	assertImpactOnce(t, impacts, "a", 1, []string{"source", "a"})
	assertImpactOnce(t, impacts, "report", 3, []string{"source", "b", "mid", "report"})
	assertImpactOnce(t, impacts, "view", 4, []string{"source", "b", "mid", "report", "view"})
	if names := impactNames(impacts); !slices.Contains(names, "a") {
		t.Fatalf("reachable cutoff a missing from %v", names)
	}
}

// With both a and b as cutoffs, both stay in the result at distance 1 but
// nothing beyond them is reachable: mid, report and view disappear together.
func TestImpactsWithCutoffsSpecExampleTwoCutoffs(t *testing.T) {
	graph := buildRegisteredGraph(t, [][]string{
		{"source"},
		{"a", "source"},
		{"b", "source"},
		{"mid", "b"},
		{"report", "a", "mid"},
		{"view", "report"},
	})

	impacts := mustImpactsWithCutoffs(t, graph, "source", "a", "b")
	want := []Impact{
		{Dataset: "a", Distance: 1, Path: []string{"source", "a"}},
		{Dataset: "b", Distance: 1, Path: []string{"source", "b"}},
	}
	if !reflect.DeepEqual(impacts, want) {
		t.Fatalf("cutoffs a,b: impacts = %v, want %v", impacts, want)
	}
	for _, gone := range []string{"mid", "report", "view"} {
		if slices.Contains(impactNames(impacts), gone) {
			t.Fatalf("%s must not propagate past cutoffs, got %v", gone, impacts)
		}
	}
}

// An empty or nil cutoff list is exactly the full Impacts query.
func TestImpactsWithCutoffsEmptyEqualsFull(t *testing.T) {
	graph := buildRegisteredGraph(t, [][]string{
		{"source"},
		{"a", "source"},
		{"b", "source"},
		{"mid", "b"},
		{"report", "a", "mid"},
		{"view", "report"},
		{"isolated"},
	})

	full := mustImpacts(t, graph, "source")

	// The varargs no-arg form passes a nil cutoff slice.
	nilCut := mustImpactsWithCutoffs(t, graph, "source")
	if !reflect.DeepEqual(nilCut, full) {
		t.Fatalf("nil cutoffs = %v, want full Impacts %v", nilCut, full)
	}
	// An explicit empty slice behaves the same.
	if got, err := ImpactsWithCutoffs(graph, "source", []string{}); err != nil || !reflect.DeepEqual(got, full) {
		t.Fatalf("empty cutoffs = %v, %v; want full Impacts %v", got, err, full)
	}
}

// A cutoff listed repeatedly acts a single time and gives the same result.
func TestImpactsWithCutoffsRepeatedNameActsOnce(t *testing.T) {
	graph := buildRegisteredGraph(t, [][]string{
		{"source"},
		{"a", "source"},
		{"b", "source"},
		{"report", "a", "b"},
	})

	once := mustImpactsWithCutoffs(t, graph, "source", "a")
	repeated := mustImpactsWithCutoffs(t, graph, "source", "a", "a", "a")
	if !reflect.DeepEqual(repeated, once) {
		t.Fatalf("repeated cutoff = %v, want %v", repeated, once)
	}
	// a appears exactly once even though named three times.
	assertImpactOnce(t, repeated, "a", 1, []string{"source", "a"})
	assertImpactOnce(t, repeated, "b", 1, []string{"source", "b"})
	// report still survives via b, and only once.
	assertImpactOnce(t, repeated, "report", 2, []string{"source", "b", "report"})
}

// A registered cutoff that is not reachable from the origin changes nothing.
func TestImpactsWithCutoffsUnreachableCutoffIgnored(t *testing.T) {
	graph := buildRegisteredGraph(t, [][]string{
		{"source"},
		{"a", "source"},
		{"b", "source"},
		{"report", "a", "b"},
		{"view", "report"},
		{"isolated"},
		{"far", "isolated"},
	})

	full := mustImpacts(t, graph, "source")
	got := mustImpactsWithCutoffs(t, graph, "source", "isolated", "far")
	if !reflect.DeepEqual(got, full) {
		t.Fatalf("unreachable cutoffs changed result: got %v, want %v", got, full)
	}
}

// When the origin itself is a cutoff, the query succeeds with a non-nil empty
// list: nothing propagates, and the origin is never listed as affected.
func TestImpactsWithCutoffsOriginIsCutoff(t *testing.T) {
	graph := buildRegisteredGraph(t, [][]string{
		{"source"},
		{"a", "source"},
		{"b", "a"},
	})

	impacts, err := ImpactsWithCutoffs(graph, "source", []string{"source"})
	if err != nil {
		t.Fatalf("origin as cutoff: unexpected error %v", err)
	}
	if impacts == nil || len(impacts) != 0 {
		t.Fatalf("want non-nil empty list when origin is cutoff, got %v", impacts)
	}
	if slices.Contains(impactNames(impacts), "source") {
		t.Fatalf("origin must never list itself, got %v", impacts)
	}

	// Origin cutoff combined with another downstream cutoff is still empty.
	impacts, err = ImpactsWithCutoffs(graph, "source", []string{"source", "a"})
	if err != nil || impacts == nil || len(impacts) != 0 {
		t.Fatalf("origin+downstream cutoff: want non-nil empty list, got %v, %v", impacts, err)
	}
}

// A node reachable on a short route through a cutoff and on a longer route that
// avoids every cutoff stays in the result at the LONGER distance with the
// bypass path; it must not keep the truncated short path from the full query.
func TestImpactsWithCutoffsBypassLongerThanTruncatedRoute(t *testing.T) {
	// source -> a -> x (short, length 2) and source -> b -> c -> x (length 3).
	graph := buildRegisteredGraph(t, [][]string{
		{"source"},
		{"a", "source"},
		{"b", "source"},
		{"c", "b"},
		{"x", "a", "c"},
	})
	assertConsistent(t, graph)

	// Full query: x is distance 2 via the a route.
	full := mustImpacts(t, graph, "source")
	assertImpactOnce(t, full, "x", 2, []string{"source", "a", "x"})

	// Cut off a: the a route is truncated, so x survives only via b, c at
	// distance 3, explained by that bypass.
	impacts := mustImpactsWithCutoffs(t, graph, "source", "a")
	assertImpactOnce(t, impacts, "a", 1, []string{"source", "a"})
	assertImpactOnce(t, impacts, "b", 1, []string{"source", "b"})
	assertImpactOnce(t, impacts, "c", 2, []string{"source", "b", "c"})
	assertImpactOnce(t, impacts, "x", 3, []string{"source", "b", "c", "x"})
}

// Among cutoff-free shortest routes the full path is still compared name by
// name from the origin; a lexicographically smaller route that passes the
// cutoff is excluded, and the winner is the smallest route that remains.
func TestImpactsWithCutoffsTieBreakOverAllowedRoutesOnly(t *testing.T) {
	// source derives a, b and c; all three derive m. Without a cutoff m would
	// be explained through a (a < b < c). Cutting a removes that route, so m
	// must be explained through b — the smallest surviving route.
	graph := buildRegisteredGraph(t, [][]string{
		{"source"},
		{"a", "source"},
		{"b", "source"},
		{"c", "source"},
		{"m", "c", "a", "b"}, // order deliberately disagrees with name order
	})
	assertConsistent(t, graph)

	full := mustImpacts(t, graph, "source")
	assertImpactOnce(t, full, "m", 2, []string{"source", "a", "m"})

	impacts := mustImpactsWithCutoffs(t, graph, "source", "a")
	assertImpactOnce(t, impacts, "a", 1, []string{"source", "a"})
	assertImpactOnce(t, impacts, "b", 1, []string{"source", "b"})
	assertImpactOnce(t, impacts, "c", 1, []string{"source", "c"})
	assertImpactOnce(t, impacts, "m", 2, []string{"source", "b", "m"})

	// Ordering stays distance-ascending then name: a,b,c at distance 1, m at 2.
	if got, want := impactNames(impacts), []string{"a", "b", "c", "m"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("order = %v, want %v", got, want)
	}
}

// A merge node directly downstream of a cutoff is still reached when another
// reached dataset also feeds it; its exclusive downstream survives via that
// merge but is explained by the allowed route.
func TestImpactsWithCutoffsMergeReachedViaOtherParent(t *testing.T) {
	graph := buildRegisteredGraph(t, [][]string{
		{"source"},
		{"a", "source"},
		{"b", "source"},
		{"r", "a", "b"},
		{"v", "r"},
	})

	impacts := mustImpactsWithCutoffs(t, graph, "source", "a")
	want := []Impact{
		{Dataset: "a", Distance: 1, Path: []string{"source", "a"}},
		{Dataset: "b", Distance: 1, Path: []string{"source", "b"}},
		{Dataset: "r", Distance: 2, Path: []string{"source", "b", "r"}},
		{Dataset: "v", Distance: 3, Path: []string{"source", "b", "r", "v"}},
	}
	if !reflect.DeepEqual(impacts, want) {
		t.Fatalf("impacts = %v, want %v", impacts, want)
	}
}

// A cutoff reached deep in the graph still halts propagation there, and a node
// reachable only through it (with no bypass) leaves the result together with
// its exclusive downstream.
func TestImpactsWithCutoffsDeepCutoffDropsExclusiveSubtree(t *testing.T) {
	graph := buildRegisteredGraph(t, [][]string{
		{"source"},
		{"a", "source"},
		{"b", "source"},
		{"c", "a"},
		{"d", "b"},
		{"only", "c"},
		{"leaf", "only"},
	})

	impacts := mustImpactsWithCutoffs(t, graph, "source", "c")
	names := impactNames(impacts)
	for _, present := range []string{"a", "b", "c", "d"} {
		if !slices.Contains(names, present) {
			t.Fatalf("%s should be reachable, got %v", present, names)
		}
	}
	for _, gone := range []string{"only", "leaf"} {
		if slices.Contains(names, gone) {
			t.Fatalf("%s lies only past cutoff c and must be gone, got %v", gone, names)
		}
	}
	assertImpactOnce(t, impacts, "c", 2, []string{"source", "a", "c"})
}

// A registered origin that reaches nothing downstream succeeds with an empty
// non-nil list, whether the cutoffs are absent/unreachable or the list is nil.
// (A reachable cutoff always appears itself, so the empty-list outcome with
// cutoffs comes from a leaf origin with only unreachable cutoffs named.)
func TestImpactsWithCutoffsNoRemainingDownstream(t *testing.T) {
	graph := buildRegisteredGraph(t, [][]string{
		{"lonely"},
		{"other"},
		{"child", "other"},
	})

	// other and child are registered but not downstream of lonely, so they
	// change nothing and lonely still answers empty.
	impacts, err := ImpactsWithCutoffs(graph, "lonely", []string{"other", "child"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if impacts == nil || len(impacts) != 0 {
		t.Fatalf("want non-nil empty list for leaf origin, got %v", impacts)
	}
}

func TestImpactsWithCutoffsErrors(t *testing.T) {
	graph := buildRegisteredGraph(t, [][]string{
		{"a"},
		{"b", "a"},
	})

	// Empty origin keeps the Impacts error behavior and takes precedence over
	// any cutoff problem.
	impacts, err := ImpactsWithCutoffs(graph, "", []string{"b"})
	if err == nil || !strings.Contains(err.Error(), "name is required") {
		t.Fatalf("empty origin: want required-name error, got %v", err)
	}
	if impacts != nil {
		t.Fatalf("empty origin: want nil results, got %v", impacts)
	}

	// Empty origin even when a cutoff is also bad: origin error wins.
	_, err = ImpactsWithCutoffs(graph, "", []string{"ghost"})
	if err == nil || !strings.Contains(err.Error(), "name is required") {
		t.Fatalf("empty origin with bad cutoff: want origin error, got %v", err)
	}

	// Unknown origin keeps the Impacts behavior, naming it, and wins over a bad
	// cutoff.
	impacts, err = ImpactsWithCutoffs(graph, "ghost", []string{"ghost2"})
	if err == nil || !strings.Contains(err.Error(), "dataset not found: ghost") {
		t.Fatalf("unknown origin: want error naming ghost, got %v", err)
	}
	if impacts != nil {
		t.Fatalf("unknown origin: want nil results, got %v", impacts)
	}

	// Empty cutoff name: the whole query fails and names the reason.
	impacts, err = ImpactsWithCutoffs(graph, "a", []string{"b", ""})
	if err == nil || !strings.Contains(err.Error(), "cutoff dataset name is required") {
		t.Fatalf("empty cutoff: want required-name cutoff error, got %v", err)
	}
	if impacts != nil {
		t.Fatalf("empty cutoff: want nil results, got %v", impacts)
	}

	// Unregistered cutoff name: the whole query fails and names the dataset.
	impacts, err = ImpactsWithCutoffs(graph, "a", []string{"missing"})
	if err == nil || !strings.Contains(err.Error(), "cutoff dataset not found: missing") {
		t.Fatalf("unknown cutoff: want error naming missing, got %v", err)
	}
	if impacts != nil {
		t.Fatalf("unknown cutoff: want nil results, got %v", impacts)
	}

	// The first bad name in cutoff input order is reported.
	if _, err = ImpactsWithCutoffs(graph, "a", []string{"missing1", "missing2"}); err == nil ||
		!strings.Contains(err.Error(), "cutoff dataset not found: missing1") {
		t.Fatalf("want first unknown cutoff missing1, got %v", err)
	}
	if _, err = ImpactsWithCutoffs(graph, "a", []string{"", "missing"}); err == nil ||
		!strings.Contains(err.Error(), "cutoff dataset name is required") {
		t.Fatalf("want empty-name cutoff error first, got %v", err)
	}

	// On an empty graph the origin itself is absent, so the origin error is
	// reported before cutoffs are inspected.
	if _, err = ImpactsWithCutoffs(map[string]*Lineage{}, "a", []string{"x"}); err == nil ||
		!strings.Contains(err.Error(), "dataset not found: a") {
		t.Fatalf("empty graph: origin is validated first, got %v", err)
	}
	// With an existing origin but an absent cutoff, the cutoff error is named.
	single := map[string]*Lineage{"a": {Dataset: "a"}}
	if _, err = ImpactsWithCutoffs(single, "a", []string{"x"}); err == nil ||
		!strings.Contains(err.Error(), "cutoff dataset not found: x") {
		t.Fatalf("single-node graph: want unknown cutoff x, got %v", err)
	}
}

// Cutoff names match by exact registered value (case-sensitive).
func TestImpactsWithCutoffsExactNameMatch(t *testing.T) {
	graph := buildRegisteredGraph(t, [][]string{
		{"raw"},
		{"RAW", "raw"},
		{"raw2", "raw"},
	})

	// "RaW" is not registered, so naming it as a cutoff fails.
	if _, err := ImpactsWithCutoffs(graph, "raw", []string{"RaW"}); err == nil ||
		!strings.Contains(err.Error(), "cutoff dataset not found: RaW") {
		t.Fatalf("case-insensitive cutoff: want not-found error naming RaW, got %v", err)
	}

	// "RAW" is a distinct registered dataset and stops propagation exactly at it.
	impacts := mustImpactsWithCutoffs(t, graph, "raw", "RAW")
	assertImpactOnce(t, impacts, "RAW", 1, []string{"raw", "RAW"})
	assertImpactOnce(t, impacts, "raw2", 1, []string{"raw", "raw2"})
}

// The cutoff query never mutates the graph; mutating the returned records
// cannot reach the graph or later queries, and failed queries are read-only.
func TestImpactsWithCutoffsReadOnlyAndIsolated(t *testing.T) {
	graph := buildRegisteredGraph(t, [][]string{
		{"source"},
		{"a", "source"},
		{"b", "source"},
		{"mid", "b"},
		{"report", "a", "mid"},
		{"view", "report"},
	})
	before := snapshot(graph)

	impacts, err := ImpactsWithCutoffs(graph, "source", []string{"a"})
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	if !reflect.DeepEqual(snapshot(graph), before) {
		t.Fatalf("query changed graph: before=%v after=%v", before, snapshot(graph))
	}

	// Abuse the returned slices; the graph and a later query must stay pristine.
	impacts[0].Path[0] = "tampered"
	impacts[0].Path = append(impacts[0].Path, "extra")
	impacts[0].Dataset = "tampered"
	impacts[0].Distance = 99
	if !reflect.DeepEqual(snapshot(graph), before) {
		t.Fatalf("mutating results changed graph: before=%v after=%v", before, snapshot(graph))
	}
	fresh := mustImpactsWithCutoffs(t, graph, "source", "a")
	assertImpactOnce(t, fresh, "report", 3, []string{"source", "b", "mid", "report"})
	assertImpactOnce(t, fresh, "view", 4, []string{"source", "b", "mid", "report", "view"})

	// Different cutoff lists do not interfere with one another's results.
	full := mustImpacts(t, graph, "source")
	assertImpactOnce(t, full, "report", 2, []string{"source", "a", "report"})

	// A failed cutoff query is also read-only.
	if _, err := ImpactsWithCutoffs(graph, "source", []string{"ghost"}); err == nil {
		t.Fatal("expected not-found cutoff error")
	}
	if !reflect.DeepEqual(snapshot(graph), before) {
		t.Fatalf("failed query changed graph: before=%v after=%v", before, snapshot(graph))
	}
	assertConsistent(t, graph)
}

// A rich graph queried repeatedly: a nil cutoff slice and the variadic no-arg
// form both reproduce Impacts exactly, and every cutoff result is a subset
// whose paths only traverse allowed nodes. Reachability and distances are
// cross-checked against an independent BFS oracle (cutoffReachability) rather
// than against the unrestricted query: a cutoff the full query can reach is
// NOT required in the cutoff result when another cutoff blocks every route to
// it.
func TestImpactsWithCutoffsPathsNeverTraverseCutoff(t *testing.T) {
	graph := buildRegisteredGraph(t, [][]string{
		{"source"},
		{"a", "source"},
		{"b", "source"},
		{"z", "a"},
		{"c", "b"},
		{"report", "z", "c"},
		{"view", "report"},
	})

	full := mustImpacts(t, graph, "source")
	for _, cutoffs := range [][]string{
		nil, {},
		{"z"}, {"c"}, {"z", "c"}, {"report"}, {"view"}, {"source"},
		// Two cutoffs on the SAME lineage route: every route from source to z
		// passes through a (source -> a -> z -> report), so with both named the
		// traversal stops at a and never reaches z. z IS reachable in the full
		// query but must not appear in this result, while report and view are
		// still reached around both via b and c. Input order cannot matter.
		{"a", "z"}, {"z", "a"},
		// a still hides z behind it, and c is an independently reached cutoff:
		// its own frontier drops, so report and view leave too.
		{"a", "z", "c"},
	} {
		got, err := ImpactsWithCutoffs(graph, "source", cutoffs)
		if err != nil {
			t.Fatalf("cutoffs %v: %v", cutoffs, err)
		}
		// With no cutoffs the result is byte-for-byte the unrestricted query.
		if len(cutoffs) == 0 && !reflect.DeepEqual(got, full) {
			t.Fatalf("cutoffs %v: %v, want full Impacts %v", cutoffs, got, full)
		}
		cutset := map[string]bool{}
		for _, c := range cutoffs {
			cutset[c] = true
		}
		seen := map[string]bool{}
		for _, im := range got {
			if seen[im.Dataset] {
				t.Fatalf("cutoffs %v: %s listed more than once", cutoffs, im.Dataset)
			}
			seen[im.Dataset] = true
			// The path must start at the origin and end at the dataset.
			if len(im.Path) != im.Distance+1 || im.Path[0] != "source" || im.Path[len(im.Path)-1] != im.Dataset {
				t.Fatalf("cutoffs %v: malformed record %v", cutoffs, im)
			}
			// Every interior hop must be a non-cutoff; the dataset itself may be
			// a cutoff (it is the terminal, traversed-to but not past).
			for _, hop := range im.Path[:len(im.Path)-1] {
				if cutset[hop] && hop != "source" {
					t.Fatalf("cutoffs %v: path for %s traverses cutoff %s: %v",
						cutoffs, im.Dataset, hop, im.Path)
				}
			}
			if im.Dataset == "source" {
				t.Fatalf("origin leaked into results for cutoffs %v", cutoffs)
			}
		}
		// Independent oracle over routes that leave every cutoff behind: the
		// reached set and BFS distances must match exactly. A cutoff hidden
		// behind another cutoff (z behind a above) is absent from the oracle
		// set even though the unrestricted query reaches it — that is the
		// judgment the full-query comparison got wrong.
		wantNames, wantDistance := cutoffReachability(graph, "source", cutoffs)
		if len(seen) != len(wantNames) {
			t.Fatalf("cutoffs %v: got %d datasets %v, want %d %v",
				cutoffs, len(seen), seen, len(wantNames), wantNames)
		}
		for name := range wantNames {
			if !seen[name] {
				t.Fatalf("cutoffs %v: %s reachable without passing a cutoff but missing from %v",
					cutoffs, name, impactNames(got))
			}
		}
		for _, im := range got {
			if d := wantDistance[im.Dataset]; d != im.Distance {
				t.Fatalf("cutoffs %v: %s distance = %d, want %d over cutoff-free routes (full-query distance must not be reused)",
					cutoffs, im.Dataset, im.Distance, d)
			}
		}
		// Sanity anchor for the corrected judgment itself: with a and z both
		// named, z must be full-query reachable yet absent here.
		if cutset["a"] && cutset["z"] {
			if !slices.Contains(impactNames(full), "z") {
				t.Fatalf("setup: z should be reachable in the full query, got %v", impactNames(full))
			}
			if slices.Contains(impactNames(got), "z") {
				t.Fatalf("cutoffs %v: z lies only past cutoff a and must not appear, got %v",
					cutoffs, impactNames(got))
			}
		}
	}
}

// cutoffReachability independently answers the reachability and distance part
// of the cutoff contract: a plain level-by-level BFS from origin over child
// edges that records a node once and then never expands a reached cutoff. It
// knows nothing about the production traversal's path tie-break, so the
// regression compares its conclusions against the query instead of restating
// the query's own logic. A cutoff that sits behind another cutoff on every
// route is never reached and is therefore absent from the expected set, even
// though the unrestricted graph reaches it.
func cutoffReachability(graph map[string]*Lineage, origin string, cutoffs []string) (map[string]bool, map[string]int) {
	blocked := make(map[string]bool, len(cutoffs))
	for _, c := range cutoffs {
		blocked[c] = true
	}
	reached := map[string]bool{origin: true}
	distance := map[string]int{origin: 0}
	frontier := []string{origin}
	for level := 0; len(frontier) > 0; level++ {
		var next []string
		for _, node := range frontier {
			if blocked[node] {
				continue // reached itself, but propagation stops there
			}
			for _, child := range graph[node].Children {
				if !reached[child] {
					reached[child] = true
					distance[child] = level + 1
					next = append(next, child)
				}
			}
		}
		frontier = next
	}
	delete(reached, origin)
	delete(distance, origin)
	return reached, distance
}
