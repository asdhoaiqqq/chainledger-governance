package chainledger

import (
	"reflect"
	"testing"
)

// This file pins the decoupling of snapshot comparison from source tracing:
// CompareSnapshots computes only the reachable root-name SET and never builds
// trace paths it does not report, while TraceSources still reports one real
// shortest path per root. The comparison outcomes below must stay identical to
// what a path-based projection would produce, and the two versions must trace
// to their own actually existing paths.

// TestCompareDecoupledRootSetExamples walks the three headline cases from the
// decoupling change:
//
//  1. Old: D -> M -> R. New: D -> R directly. D's DIRECT relation changes, but
//     it reaches the same single root R in both versions, so it must not enter
//     rootSourceChanges; tracing each version still yields its real path.
//  2. M is repointed from root R to another root S while D keeps its only
//     direct upstream M. D is not a directly changed dataset, yet its source
//     set moves R -> S and it must be listed.
//  3. D's upstreams are cleared entirely. It becomes a root and its own source.
func TestCompareDecoupledRootSetExamples(t *testing.T) {
	t.Run("shortcut replaces chain, same root", func(t *testing.T) {
		oldSnap := snapshotOf(t, `{"datasets":[
			{"name":"R","upstreams":[]},
			{"name":"M","upstreams":["R"]},
			{"name":"D","upstreams":["M"]}
		]}`)
		newSnap := snapshotOf(t, `{"datasets":[
			{"name":"R","upstreams":[]},
			{"name":"M","upstreams":["R"]},
			{"name":"D","upstreams":["R"]}
		]}`)

		report := CompareSnapshots(oldSnap, newSnap)
		if got, want := report.ChangedDatasets, []string{"D"}; !reflect.DeepEqual(got, want) {
			t.Errorf("ChangedDatasets = %v, want %v", got, want)
		}
		if got, want := report.AddedRelations, []Relation{{Upstream: "R", Downstream: "D"}}; !reflect.DeepEqual(got, want) {
			t.Errorf("AddedRelations = %v, want %v", got, want)
		}
		if got, want := report.RemovedRelations, []Relation{{Upstream: "M", Downstream: "D"}}; !reflect.DeepEqual(got, want) {
			t.Errorf("RemovedRelations = %v, want %v", got, want)
		}
		if len(report.RootSourceChanges) != 0 {
			t.Fatalf("same-root shortcut must not be a source change, got %v", report.RootSourceChanges)
		}

		// Tracing each version independently still returns the path that
		// actually exists in that version.
		if got := traceReportSources(t, oldSnap, "D"); !reflect.DeepEqual(got, []SourceTrace{
			{Root: "R", Path: []string{"D", "M", "R"}},
		}) {
			t.Errorf("old trace D = %+v, want the chain path", got)
		}
		if got := traceReportSources(t, newSnap, "D"); !reflect.DeepEqual(got, []SourceTrace{
			{Root: "R", Path: []string{"D", "R"}},
		}) {
			t.Errorf("new trace D = %+v, want the direct path", got)
		}
	})

	t.Run("upstream repointed at another root moves the downstream set", func(t *testing.T) {
		oldSnap := snapshotOf(t, `{"datasets":[
			{"name":"R","upstreams":[]},
			{"name":"S","upstreams":[]},
			{"name":"M","upstreams":["R"]},
			{"name":"D","upstreams":["M"]}
		]}`)
		newSnap := snapshotOf(t, `{"datasets":[
			{"name":"R","upstreams":[]},
			{"name":"S","upstreams":[]},
			{"name":"M","upstreams":["S"]},
			{"name":"D","upstreams":["M"]}
		]}`)

		report := CompareSnapshots(oldSnap, newSnap)
		// Only M is directly repointed; D keeps the single direct upstream M.
		if got, want := report.ChangedDatasets, []string{"M"}; !reflect.DeepEqual(got, want) {
			t.Errorf("ChangedDatasets = %v, want %v", got, want)
		}
		// Both M and D move R -> S; D's direct upstream never changed.
		wantChanges := []RootSourceChange{
			{Dataset: "D", OldRoots: []string{"R"}, NewRoots: []string{"S"}},
			{Dataset: "M", OldRoots: []string{"R"}, NewRoots: []string{"S"}},
		}
		if got := report.RootSourceChanges; !reflect.DeepEqual(got, wantChanges) {
			t.Fatalf("RootSourceChanges = %v, want %v", got, wantChanges)
		}
		if got := traceReportSources(t, newSnap, "D"); !reflect.DeepEqual(got, []SourceTrace{
			{Root: "S", Path: []string{"D", "M", "S"}},
		}) {
			t.Errorf("new trace D = %+v, want path through M to S", got)
		}
	})

	t.Run("clearing all upstreams makes the dataset its own root", func(t *testing.T) {
		oldSnap := snapshotOf(t, `{"datasets":[
			{"name":"R","upstreams":[]},
			{"name":"D","upstreams":["R"]}
		]}`)
		newSnap := snapshotOf(t, `{"datasets":[
			{"name":"R","upstreams":[]},
			{"name":"D","upstreams":[]}
		]}`)

		report := CompareSnapshots(oldSnap, newSnap)
		wantChanges := []RootSourceChange{
			{Dataset: "D", OldRoots: []string{"R"}, NewRoots: []string{"D"}},
		}
		if got := report.RootSourceChanges; !reflect.DeepEqual(got, wantChanges) {
			t.Fatalf("RootSourceChanges = %v, want %v", got, wantChanges)
		}
		if got := rootSources("D", adjacencyFromValidFile(newSnap.Graph)); !reflect.DeepEqual(got, []string{"D"}) {
			t.Errorf("rootSources(D) after clearing = %v, want [D]", got)
		}
		if got := traceReportSources(t, newSnap, "D"); !reflect.DeepEqual(got, []SourceTrace{
			{Root: "D", Path: []string{"D"}},
		}) {
			t.Errorf("new trace D = %+v, want single-element self path", got)
		}
	})
}

// TestRootSourcesSetDoesNotBuildPaths is the direct unit pin on the decoupled
// finder: multi-root and multi-path reachability yields one fresh, sorted
// name-only list, independent across calls, with the queried root counting
// itself once and disconnected roots excluded.
func TestRootSourcesSetDoesNotBuildPaths(t *testing.T) {
	// D reaches rootA by two routes (direct and via m) and rootB via m; u/v
	// form a disconnected component that must never join.
	adj := adjacency{
		"rootA": nil,
		"rootB": nil,
		"m":     {"rootA", "rootB"},
		"D":     {"m", "rootA"},
		"u":     nil,
		"v":     {"u"},
	}
	got := rootSources("D", adj)
	want := []string{"rootA", "rootB"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("rootSources(D) = %v, want %v", got, want)
	}

	// A second call returns an independent slice: editing one never moves the
	// other.
	again := rootSources("D", adj)
	got[0] = "HACK"
	got = append(got, "GHOST")
	if !reflect.DeepEqual(again, want) {
		t.Errorf("second rootSources result aliases the first: %v, want %v", again, want)
	}

	// A root is its own source exactly once, and a disconnected root sees only
	// itself.
	if got := rootSources("rootA", adj); !reflect.DeepEqual(got, []string{"rootA"}) {
		t.Errorf("rootSources(rootA) = %v, want [rootA]", got)
	}
	if got := rootSources("v", adj); !reflect.DeepEqual(got, []string{"u"}) {
		t.Errorf("rootSources(v) = %v, want disconnected [u]", got)
	}
}
