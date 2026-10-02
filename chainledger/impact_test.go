package chainledger

import (
	"reflect"
	"strings"
	"testing"
)

func impactNames(results []Impact) []string {
	names := make([]string, len(results))
	for i, r := range results {
		names[i] = r.Name
	}
	return names
}

func findImpact(t *testing.T, results []Impact, name string) Impact {
	t.Helper()
	for _, r := range results {
		if r.Name == name {
			return r
		}
	}
	t.Fatalf("impact for %q not found in %v", name, impactNames(results))
	return Impact{}
}

// The spec example: raw -> a, raw -> b, a -> report, b -> report,
// report -> view. a and b at distance 1, report once at distance 2 with the
// lexicographically smallest route raw,a,report, view at distance 3.
func TestImpactDiamondExample(t *testing.T) {
	graph := map[string]*Lineage{}
	mustRegister(t, graph, "raw")
	mustRegister(t, graph, "a", "raw")
	mustRegister(t, graph, "b", "raw")
	mustRegister(t, graph, "report", "a", "b")
	mustRegister(t, graph, "view", "report")

	results, err := ImpactOf(graph, "raw")
	if err != nil {
		t.Fatalf("Impact(raw): %v", err)
	}

	if got, want := impactNames(results), []string{"a", "b", "report", "view"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("order = %v, want %v", got, want)
	}

	cases := []struct {
		name     string
		distance int
		path     []string
	}{
		{"a", 1, []string{"raw", "a"}},
		{"b", 1, []string{"raw", "b"}},
		{"report", 2, []string{"raw", "a", "report"}},
		{"view", 3, []string{"raw", "a", "report", "view"}},
	}
	for _, tc := range cases {
		r := findImpact(t, results, tc.name)
		if r.Distance != tc.distance {
			t.Errorf("%s distance = %d, want %d", tc.name, r.Distance, tc.distance)
		}
		if !reflect.DeepEqual(r.Path, tc.path) {
			t.Errorf("%s path = %v, want %v", tc.name, r.Path, tc.path)
		}
	}
}

// Re-registering report as a direct child of raw shortens report to distance 1
// and view to distance 2; the earlier returned values keep their content.
func TestImpactReregisterChangesDistances(t *testing.T) {
	graph := map[string]*Lineage{}
	mustRegister(t, graph, "raw")
	mustRegister(t, graph, "a", "raw")
	mustRegister(t, graph, "b", "raw")
	mustRegister(t, graph, "report", "a", "b")
	mustRegister(t, graph, "view", "report")

	before, err := ImpactOf(graph, "raw")
	if err != nil {
		t.Fatalf("Impact(raw): %v", err)
	}
	if r := findImpact(t, before, "report"); r.Distance != 2 {
		t.Fatalf("before re-register report distance = %d, want 2", r.Distance)
	}
	if r := findImpact(t, before, "view"); r.Distance != 3 {
		t.Fatalf("before re-register view distance = %d, want 3", r.Distance)
	}

	// report now directly depends on raw.
	mustRegister(t, graph, "report", "raw")

	after, err := ImpactOf(graph, "raw")
	if err != nil {
		t.Fatalf("Impact(raw) after re-register: %v", err)
	}
	if r := findImpact(t, after, "report"); r.Distance != 1 {
		t.Errorf("report distance = %d, want 1", r.Distance)
	} else if want := []string{"raw", "report"}; !reflect.DeepEqual(r.Path, want) {
		t.Errorf("report path = %v, want %v", r.Path, want)
	}
	if r := findImpact(t, after, "view"); r.Distance != 2 {
		t.Errorf("view distance = %d, want 2", r.Distance)
	} else if want := []string{"raw", "report", "view"}; !reflect.DeepEqual(r.Path, want) {
		t.Errorf("view path = %v, want %v", r.Path, want)
	}

	// The earlier result is untouched.
	if r := findImpact(t, before, "report"); r.Distance != 2 {
		t.Errorf("earlier report result changed to distance %d", r.Distance)
	}
	if r := findImpact(t, before, "view"); r.Distance != 3 {
		t.Errorf("earlier view result changed to distance %d", r.Distance)
	}
}

// A start with no registered downstream succeeds with an empty list.
func TestImpactNoDownstreams(t *testing.T) {
	graph := map[string]*Lineage{}
	mustRegister(t, graph, "leaf")
	mustRegister(t, graph, "other", "leaf") // other is a downstream of leaf
	mustRegister(t, graph, "solo")          // nothing depends on solo

	results, err := ImpactOf(graph, "solo")
	if err != nil {
		t.Fatalf("Impact(solo): %v", err)
	}
	if len(results) != 0 {
		t.Fatalf("Impact(solo) = %v, want empty", results)
	}

	// leaf does have a downstream, so it must not be empty.
	results, err = ImpactOf(graph, "leaf")
	if err != nil {
		t.Fatalf("Impact(leaf): %v", err)
	}
	if len(results) != 1 || results[0].Name != "other" {
		t.Fatalf("Impact(leaf) = %v, want just [other]", results)
	}
}

// Empty start name is an error naming the problem; no partial results.
func TestImpactEmptyName(t *testing.T) {
	graph := map[string]*Lineage{}
	mustRegister(t, graph, "a")

	results, err := ImpactOf(graph, "")
	if err == nil {
		t.Fatal("expected error for empty name, got nil")
	}
	if results != nil {
		t.Fatalf("empty name returned results %v, want nil", results)
	}
	if !strings.Contains(err.Error(), "name is required") {
		t.Errorf("error %q should mention name is required", err.Error())
	}
}

// A non-empty name that is not registered is an error naming that name.
func TestImpactUnknownName(t *testing.T) {
	graph := map[string]*Lineage{}
	mustRegister(t, graph, "a")
	mustRegister(t, graph, "b", "a")

	results, err := ImpactOf(graph, "ghost")
	if err == nil {
		t.Fatal("expected error for unknown name, got nil")
	}
	if results != nil {
		t.Fatalf("unknown name returned results %v, want nil", results)
	}
	if !strings.Contains(err.Error(), "ghost") {
		t.Errorf("error %q should name the missing dataset ghost", err.Error())
	}
}

// Empty (but initialized) and nil graphs both report the start as missing.
func TestImpactEmptyGraph(t *testing.T) {
	for _, graph := range []map[string]*Lineage{nil, {}} {
		results, err := ImpactOf(graph, "a")
		if err == nil {
			t.Fatalf("graph %v: expected not-found error, got nil", graph)
		}
		if results != nil {
			t.Fatalf("graph %v: got results %v, want nil", graph, results)
		}
		if !strings.Contains(err.Error(), "a") {
			t.Errorf("error %q should name a", err.Error())
		}
	}
}

// The start itself and datasets with no downstream connection never appear.
func TestImpactExcludesStartAndIndependent(t *testing.T) {
	graph := map[string]*Lineage{}
	mustRegister(t, graph, "root")
	mustRegister(t, graph, "mid", "root")
	mustRegister(t, graph, "leaf", "mid")
	mustRegister(t, graph, "isolated")
	mustRegister(t, graph, "alsoIsolated", "isolated")

	results, err := ImpactOf(graph, "root")
	if err != nil {
		t.Fatalf("Impact(root): %v", err)
	}
	names := impactNames(results)
	if !reflect.DeepEqual(names, []string{"mid", "leaf"}) {
		t.Fatalf("Impact(root) = %v, want [mid leaf]", names)
	}
	for _, r := range results {
		if r.Name == "root" {
			t.Error("start dataset must not appear in its own impact list")
		}
		if r.Name == "isolated" || r.Name == "alsoIsolated" {
			t.Errorf("independent dataset %s must not appear", r.Name)
		}
	}
}

// Sorting is by distance ascending, then dataset name descending-independent.
func TestImpactSortsByDistanceThenName(t *testing.T) {
	graph := map[string]*Lineage{}
	mustRegister(t, graph, "start")
	// Three direct downstreams registered out of name order.
	mustRegister(t, graph, "zebra", "start")
	mustRegister(t, graph, "apple", "start")
	mustRegister(t, graph, "mango", "start")
	// Two distance-2 nodes, again registered out of order.
	mustRegister(t, graph, "zChild", "zebra")
	mustRegister(t, graph, "aChild", "apple")

	results, err := ImpactOf(graph, "start")
	if err != nil {
		t.Fatalf("Impact(start): %v", err)
	}
	want := []string{"apple", "mango", "zebra", "aChild", "zChild"}
	if got := impactNames(results); !reflect.DeepEqual(got, want) {
		t.Fatalf("order = %v, want %v", got, want)
	}
	for i := 1; i < len(results); i++ {
		prev, cur := results[i-1], results[i]
		if prev.Distance > cur.Distance {
			t.Errorf("results not distance-sorted: %+v before %+v", prev, cur)
		}
		if prev.Distance == cur.Distance && prev.Name >= cur.Name {
			t.Errorf("tie not name-sorted: %+v before %+v", prev, cur)
		}
	}
}

// When several shortest routes exist, the lexicographically smallest name
// sequence wins, even if it goes through a later-registered child.
func TestImpactShortestPathTieLexicographic(t *testing.T) {
	graph := map[string]*Lineage{}
	mustRegister(t, graph, "start")
	// Registered in an order that would favor the wrong path if registration
	// order decided anything.
	mustRegister(t, graph, "zBranch", "start")
	mustRegister(t, graph, "aBranch", "start")
	mustRegister(t, graph, "target", "zBranch", "aBranch")

	results, err := ImpactOf(graph, "start")
	if err != nil {
		t.Fatalf("Impact(start): %v", err)
	}
	r := findImpact(t, results, "target")
	if r.Distance != 2 {
		t.Fatalf("target distance = %d, want 2", r.Distance)
	}
	if want := []string{"start", "aBranch", "target"}; !reflect.DeepEqual(r.Path, want) {
		t.Errorf("target path = %v, want lexicographically smallest %v", r.Path, want)
	}
}

// A deeper tie: equal-length routes that differ in the middle.
func TestImpactShortestPathTieDeeper(t *testing.T) {
	graph := map[string]*Lineage{}
	mustRegister(t, graph, "start")
	mustRegister(t, graph, "z1", "start")
	mustRegister(t, graph, "a1", "start")
	mustRegister(t, graph, "z2", "z1")
	mustRegister(t, graph, "m2", "a1")
	mustRegister(t, graph, "target", "z2", "m2")

	results, err := ImpactOf(graph, "start")
	if err != nil {
		t.Fatalf("Impact(start): %v", err)
	}
	r := findImpact(t, results, "target")
	if want := []string{"start", "a1", "m2", "target"}; !reflect.DeepEqual(r.Path, want) {
		t.Errorf("target path = %v, want %v", r.Path, want)
	}
}

// Querying must not alter the graph, even in ordering.
func TestImpactDoesNotMutateGraph(t *testing.T) {
	graph := map[string]*Lineage{}
	mustRegister(t, graph, "raw")
	mustRegister(t, graph, "a", "raw")
	mustRegister(t, graph, "b", "raw")
	mustRegister(t, graph, "report", "a", "b")
	mustRegister(t, graph, "view", "report")

	before := snapshot(graph)
	if _, err := ImpactOf(graph, "raw"); err != nil {
		t.Fatalf("Impact(raw): %v", err)
	}
	if after := snapshot(graph); !reflect.DeepEqual(after, before) {
		t.Fatalf("Impact changed the graph: before=%v after=%v", before, after)
	}

	// A failed query must also leave the graph untouched.
	if _, err := ImpactOf(graph, "ghost"); err == nil {
		t.Fatal("expected error")
	}
	if after := snapshot(graph); !reflect.DeepEqual(after, before) {
		t.Fatalf("failed Impact changed the graph: before=%v after=%v", before, after)
	}
}

// Mutating a returned result list or path must not affect the graph or later
// queries.
func TestImpactResultMutationIsolated(t *testing.T) {
	graph := map[string]*Lineage{}
	mustRegister(t, graph, "raw")
	mustRegister(t, graph, "a", "raw")
	mustRegister(t, graph, "report", "a")

	first, err := ImpactOf(graph, "raw")
	if err != nil {
		t.Fatalf("Impact(raw): %v", err)
	}
	first[0].Name = "tampered"
	first[0].Distance = 99
	first[0].Path[0] = "tampered"
	first = append(first, Impact{Name: "fake", Distance: 1, Path: []string{"fake"}})

	second, err := ImpactOf(graph, "raw")
	if err != nil {
		t.Fatalf("second Impact(raw): %v", err)
	}
	if !reflect.DeepEqual(impactNames(second), []string{"a", "report"}) {
		t.Fatalf("mutation leaked into later query: %v", second)
	}
	if r := findImpact(t, second, "a"); r.Distance != 1 || r.Path[0] != "raw" {
		t.Fatalf("mutation leaked into a's result: %+v", r)
	}
	if r := findImpact(t, second, "report"); r.Distance != 2 {
		t.Fatalf("report distance = %d, want 2", r.Distance)
	}

	// The graph itself is untouched.
	assertConsistent(t, graph)
}

// Converging branches report a dataset exactly once.
func TestImpactConvergesOnce(t *testing.T) {
	graph := map[string]*Lineage{}
	mustRegister(t, graph, "raw")
	mustRegister(t, graph, "a", "raw")
	mustRegister(t, graph, "b", "raw")
	mustRegister(t, graph, "c", "raw")
	mustRegister(t, graph, "hub", "a", "b", "c")
	mustRegister(t, graph, "leaf", "hub")

	results, err := ImpactOf(graph, "raw")
	if err != nil {
		t.Fatalf("Impact(raw): %v", err)
	}
	counts := map[string]int{}
	for _, r := range results {
		counts[r.Name]++
	}
	for _, name := range []string{"a", "b", "c", "hub", "leaf"} {
		if counts[name] != 1 {
			t.Errorf("%s appears %d times, want 1", name, counts[name])
		}
	}
	if r := findImpact(t, results, "hub"); r.Distance != 2 {
		t.Errorf("hub distance = %d, want 2", r.Distance)
	}
	if r := findImpact(t, results, "leaf"); r.Distance != 3 {
		t.Errorf("leaf distance = %d, want 3", r.Distance)
	}
}
