package chainledger

import (
	"reflect"
	"slices"
	"strings"
	"testing"
)

// Regressions for two cutoffs named on the same lineage route. The earlier
// cutoff shields every cutoff behind it: a cutoff that the unrestricted query
// can reach is not automatically owed a record, because the route the
// unrestricted query would take may already have been truncated. These tests
// pin down:
//
//   - a cutoff reached on an allowed route is returned but is a dead end, so a
//     second cutoff lying past it disappears together with everything beyond;
//   - a second cutoff is still returned when a separate cutoff-free branch
//     reaches it, explained by that branch's distance and path, and is then a
//     dead end itself;
//   - cutoff input order and repeated names cannot change either result;
//   - stopping propagation is distinct from validating the list: an
//     unregistered name fails the whole query with nil results even after an
//     earlier cutoff has truncated every route;
//   - successful and failed queries leave the graph byte-for-byte untouched.

// The linear lineage used by the first two scenarios:
//
//	source -> a -> b -> report
//
// All four datasets are registered.
func buildTwoCutoffChainGraph(t *testing.T) map[string]*Lineage {
	t.Helper()
	return buildRegisteredGraph(t, [][]string{
		{"source"},
		{"a", "source"},
		{"b", "a"},
		{"report", "b"},
	})
}

// With both a and b named as cutoffs on the single route source -> a -> b ->
// report, only a is returned: a stops propagation, so b is not reached in this
// query even though the unrestricted query reaches it, and report is likewise
// unreachable. The cutoffs stay registered and no lineage is dissolved.
func TestImpactsWithCutoffsEarlierCutoffShieldsLaterOne(t *testing.T) {
	graph := buildTwoCutoffChainGraph(t)
	assertConsistent(t, graph)

	// Baseline: without cutoffs the full query reaches all three descendants,
	// with b at distance 2 through a and report at distance 3.
	full := mustImpacts(t, graph, "source")
	assertImpactOnce(t, full, "a", 1, []string{"source", "a"})
	assertImpactOnce(t, full, "b", 2, []string{"source", "a", "b"})
	assertImpactOnce(t, full, "report", 3, []string{"source", "a", "b", "report"})

	want := []Impact{
		{Dataset: "a", Distance: 1, Path: []string{"source", "a"}},
	}
	impacts := mustImpactsWithCutoffs(t, graph, "source", "a", "b")
	if !reflect.DeepEqual(impacts, want) {
		t.Fatalf("cutoffs a,b: impacts = %v, want %v", impacts, want)
	}
	assertImpactOnce(t, impacts, "a", 1, []string{"source", "a"})

	names := impactNames(impacts)
	for _, gone := range []string{"b", "report"} {
		if slices.Contains(names, gone) {
			t.Fatalf("%s lies past cutoff a and must be absent, got %v", gone, names)
		}
	}

	// The two cutoffs remain registered with their relationships intact: the
	// read-only query neither unregistered them nor detached any edge.
	assertEntry(t, graph, "source", nil, []string{"a"})
	assertEntry(t, graph, "a", []string{"source"}, []string{"b"})
	assertEntry(t, graph, "b", []string{"a"}, []string{"report"})
	assertEntry(t, graph, "report", []string{"b"}, nil)
	assertConsistent(t, graph)

	// The unrestricted query still reaches everything exactly as before.
	after := mustImpacts(t, graph, "source")
	if !reflect.DeepEqual(after, full) {
		t.Fatalf("full query changed after cutoff query: got %v, want %v", after, full)
	}
}

// Swapping the two cutoff names, or repeating either one, must produce the same
// single-record result: cutoff handling is set-based and input-order-free.
func TestImpactsWithCutoffsEarlierShieldsLaterOrderAndDuplicatesInvariant(t *testing.T) {
	graph := buildTwoCutoffChainGraph(t)

	want := []Impact{
		{Dataset: "a", Distance: 1, Path: []string{"source", "a"}},
	}
	for _, cutoffs := range [][]string{
		{"a", "b"},
		{"b", "a"}, // reversed input order
		{"a", "a", "b"},
		{"b", "b", "a"},
		{"a", "b", "b"},
		{"a", "b", "a", "b"},
	} {
		got, err := ImpactsWithCutoffs(graph, "source", cutoffs)
		if err != nil {
			t.Fatalf("cutoffs %v: %v", cutoffs, err)
		}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("cutoffs %v: got %v, want %v", cutoffs, got, want)
		}
	}
}

// The merge lineage: source reaches b both through a (length 2) and through a
// separate c -> d branch (length 3) that never touches a; b derives report.
//
//	source -> a -> b ---------> report
//	  |               ^
//	  +-> c -> d -----+
//
// With a and b both named as cutoffs, a shields the short route but the
// c/d branch is untouched: a, c, d and b all appear, b exactly once at the
// branch distance 3 with the branch path, and report stays unreachable because
// b itself is a dead end. The unrestricted distance 2 / [source a b]
// explanation must not be reused.
func TestImpactsWithCutoffsLaterCutoffReachedAroundEarlierOne(t *testing.T) {
	graph := buildRegisteredGraph(t, [][]string{
		{"source"},
		{"a", "source"},
		{"c", "source"},
		{"d", "c"},
		{"b", "a", "d"}, // short route via a, long via c/d
		{"report", "b"},
	})
	assertConsistent(t, graph)

	// Baseline unrestricted query: b is distance 2 through a.
	full := mustImpacts(t, graph, "source")
	assertImpactOnce(t, full, "b", 2, []string{"source", "a", "b"})
	assertImpactOnce(t, full, "report", 3, []string{"source", "a", "b", "report"})

	want := []Impact{
		{Dataset: "a", Distance: 1, Path: []string{"source", "a"}},
		{Dataset: "c", Distance: 1, Path: []string{"source", "c"}},
		{Dataset: "d", Distance: 2, Path: []string{"source", "c", "d"}},
		{Dataset: "b", Distance: 3, Path: []string{"source", "c", "d", "b"}},
	}
	impacts := mustImpactsWithCutoffs(t, graph, "source", "a", "b")
	if !reflect.DeepEqual(impacts, want) {
		t.Fatalf("cutoffs a,b: impacts = %v, want %v", impacts, want)
	}

	// b appears exactly once, at the longer branch distance and path: being
	// downstream of another cutoff does not exclude it when a cutoff-free route
	// reaches it, but the truncated short route must not explain it.
	assertImpactOnce(t, impacts, "b", 3, []string{"source", "c", "d", "b"})
	// Ordering is distance ascending then dataset name: a,c at distance 1,
	// d at 2, b at 3 — not registration order.
	if got, wantNames := impactNames(impacts), []string{"a", "c", "d", "b"}; !reflect.DeepEqual(got, wantNames) {
		t.Fatalf("order = %v, want %v", got, wantNames)
	}
	// Reaching b still stops propagation there: report never appears.
	if names := impactNames(impacts); slices.Contains(names, "report") {
		t.Fatalf("report lies past cutoff b and must be absent, got %v", names)
	}

	// No relationship was dissolved by the query.
	assertEntry(t, graph, "source", nil, []string{"a", "c"})
	assertEntry(t, graph, "a", []string{"source"}, []string{"b"})
	assertEntry(t, graph, "c", []string{"source"}, []string{"d"})
	assertEntry(t, graph, "d", []string{"c"}, []string{"b"})
	assertEntry(t, graph, "b", []string{"a", "d"}, []string{"report"})
	assertEntry(t, graph, "report", []string{"b"}, nil)
	assertConsistent(t, graph)
}

// The merge result is likewise invariant under cutoff input order and repeated
// cutoff names.
func TestImpactsWithCutoffsBypassOrderAndDuplicatesInvariant(t *testing.T) {
	graph := buildRegisteredGraph(t, [][]string{
		{"source"},
		{"a", "source"},
		{"c", "source"},
		{"d", "c"},
		{"b", "a", "d"},
		{"report", "b"},
	})

	want := []Impact{
		{Dataset: "a", Distance: 1, Path: []string{"source", "a"}},
		{Dataset: "c", Distance: 1, Path: []string{"source", "c"}},
		{Dataset: "d", Distance: 2, Path: []string{"source", "c", "d"}},
		{Dataset: "b", Distance: 3, Path: []string{"source", "c", "d", "b"}},
	}
	for _, cutoffs := range [][]string{
		{"a", "b"},
		{"b", "a"}, // reversed input order
		{"a", "a", "b"},
		{"b", "b", "a"},
		{"a", "b", "b"},
		{"b", "a", "b", "a"},
	} {
		got, err := ImpactsWithCutoffs(graph, "source", cutoffs)
		if err != nil {
			t.Fatalf("cutoffs %v: %v", cutoffs, err)
		}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("cutoffs %v: got %v, want %v", cutoffs, got, want)
		}
		if names := impactNames(got); slices.Contains(names, "report") {
			t.Fatalf("cutoffs %v: report must stay past cutoff b, got %v", cutoffs, names)
		}
	}
}

// Stopping propagation is not stopping validation: even when the first cutoff
// already truncates every route, an unregistered name later in the list fails
// the whole query. The error names that dataset, results are nil, and none of
// the nodes reached before the truncation are returned as partial results.
func TestImpactsWithCutoffsUnregisteredNameFailsAfterBlockingCutoff(t *testing.T) {
	graph := buildTwoCutoffChainGraph(t)
	before := snapshot(graph)

	// The bad name follows the cutoff that alone would end the traversal at a.
	impacts, err := ImpactsWithCutoffs(graph, "source", []string{"a", "ghost"})
	if err == nil || !strings.Contains(err.Error(), "cutoff dataset not found: ghost") {
		t.Fatalf("cutoffs [a ghost]: want not-found error naming ghost, got %v", err)
	}
	if impacts != nil {
		t.Fatalf("cutoffs [a ghost]: want nil results, got %v", impacts)
	}

	// Same outcome in the merge lineage, where a still leaves c/d reachable:
	// validation failure must discard even that larger partial scope.
	merge := buildRegisteredGraph(t, [][]string{
		{"source"},
		{"a", "source"},
		{"c", "source"},
		{"d", "c"},
		{"b", "a", "d"},
		{"report", "b"},
	})
	mergeBefore := snapshot(merge)
	impacts, err = ImpactsWithCutoffs(merge, "source", []string{"a", "b", "ghost"})
	if err == nil || !strings.Contains(err.Error(), "cutoff dataset not found: ghost") {
		t.Fatalf("cutoffs [a b ghost]: want not-found error naming ghost, got %v", err)
	}
	if impacts != nil {
		t.Fatalf("cutoffs [a b ghost]: want nil results, got %v", impacts)
	}

	// A bad name repeated alongside good cutoffs is still the reported failure.
	if _, err = ImpactsWithCutoffs(graph, "source", []string{"a", "ghost", "a", "b"}); err == nil ||
		!strings.Contains(err.Error(), "cutoff dataset not found: ghost") {
		t.Fatalf("cutoffs [a ghost a b]: want not-found error naming ghost, got %v", err)
	}

	// Both failed queries leave nodes, bidirectional edges and list orders
	// exactly as they were.
	if !reflect.DeepEqual(snapshot(graph), before) {
		t.Fatalf("failed query changed graph: before=%v after=%v", before, snapshot(graph))
	}
	if !reflect.DeepEqual(snapshot(merge), mergeBefore) {
		t.Fatalf("failed query changed merge graph: before=%v after=%v", mergeBefore, snapshot(merge))
	}
	assertConsistent(t, graph)
	assertConsistent(t, merge)

	// A successful query immediately afterward still returns the proper scope.
	if got := mustImpactsWithCutoffs(t, graph, "source", "a", "b"); len(got) != 1 ||
		got[0].Dataset != "a" {
		t.Fatalf("valid query after failed one = %v, want only a", got)
	}
}

// Successful two-cutoff queries are read-only: nodes, bidirectional edges and
// stored list orders are unchanged, and the returned paths are independent
// copies.
func TestImpactsWithCutoffsTwoCutoffsReadOnly(t *testing.T) {
	graph := buildRegisteredGraph(t, [][]string{
		{"source"},
		{"a", "source"},
		{"c", "source"},
		{"d", "c"},
		{"b", "a", "d"},
		{"report", "b"},
	})
	before := snapshot(graph)

	impacts := mustImpactsWithCutoffs(t, graph, "source", "a", "b")

	// Mutate the returned records; the graph must stay pristine.
	for i := range impacts {
		impacts[i].Path[0] = "tampered"
		impacts[i].Path = append(impacts[i].Path, "extra")
	}
	if !reflect.DeepEqual(snapshot(graph), before) {
		t.Fatalf("mutating results changed graph: before=%v after=%v", before, snapshot(graph))
	}

	// The unrestricted query and the cutoff query after the tampering are both
	// unchanged.
	assertImpactOnce(t, mustImpacts(t, graph, "source"), "b", 2, []string{"source", "a", "b"})
	fresh := mustImpactsWithCutoffs(t, graph, "source", "b", "a")
	assertImpactOnce(t, fresh, "b", 3, []string{"source", "c", "d", "b"})
	assertConsistent(t, graph)
}
