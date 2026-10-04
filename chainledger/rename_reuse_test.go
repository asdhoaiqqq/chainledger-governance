package chainledger

import (
	"reflect"
	"strings"
	"testing"
)

// Regression for reusing a freed name as an independent registration after a
// rename, then pointing the renamed dataset at it. raw feeds mid; mid feeds
// detail; detail feeds report; raw also feeds the unrelated side. Renaming mid
// to hub frees the name mid, which is then registered as a new source with no
// upstreams and given its own downstream midchild first. Re-registering hub
// with mid as its only direct upstream must succeed: mid and hub are two
// independent registrations, hub keeps its original downstream chain, raw
// drops hub from its children but keeps side, and hub lands at the end of
// mid's child list behind midchild. Afterwards raw's impact scope is just
// side, while mid reaches hub, detail and report at distances 1, 2 and 3, and
// report's provenance runs detail <- hub <- mid with raw gone.
func TestRenameThenReuseFreedNameAsUpstream(t *testing.T) {
	graph := buildRegisteredGraph(t, [][]string{
		{"raw"},
		{"mid", "raw"},
		{"detail", "mid"},
		{"report", "detail"},
		{"side", "raw"}, // unrelated downstream of raw
	})
	assertConsistent(t, graph)

	// Rename the middle dataset: the name mid is freed, the node keeps its
	// exact position, and every neighbor reference is rewired in place.
	mustRename(t, graph, "mid", "hub")
	assertConsistent(t, graph)
	assertEntry(t, graph, "raw", nil, []string{"hub", "side"})
	assertEntry(t, graph, "hub", []string{"raw"}, []string{"detail"})
	assertEntry(t, graph, "detail", []string{"hub"}, []string{"report"})
	assertEntry(t, graph, "report", []string{"detail"}, nil)
	assertEntry(t, graph, "side", []string{"raw"}, nil)

	// While mid is still unregistered it cannot be hub's upstream: the error
	// names mid, and the rejection is atomic.
	beforeRejected := snapshot(graph)
	rawImpactsBeforeRejected := mustImpacts(t, graph, "raw")
	err := Register(graph, Dataset{Name: "hub"}, []string{"mid"})
	if err == nil || !strings.Contains(err.Error(), "unknown parent") || !strings.Contains(err.Error(), "mid") {
		t.Fatalf("want unknown-parent error naming mid, got %v", err)
	}
	if !reflect.DeepEqual(snapshot(graph), beforeRejected) {
		t.Fatalf("rejected registration changed graph: before=%v after=%v", beforeRejected, snapshot(graph))
	}
	assertEntry(t, graph, "hub", []string{"raw"}, []string{"detail"})
	assertEntry(t, graph, "raw", nil, []string{"hub", "side"})
	if got := mustImpacts(t, graph, "raw"); !reflect.DeepEqual(got, rawImpactsBeforeRejected) {
		t.Fatalf("impacts after rejected registration = %v, want %v", got, rawImpactsBeforeRejected)
	}
	assertConsistent(t, graph)

	// Register mid as a brand-new source with no upstreams, and let it own
	// another downstream first. It is an independent registration, unrelated
	// to the renamed hub.
	mustRegister(t, graph, "mid")
	mustRegister(t, graph, "midchild", "mid")
	assertConsistent(t, graph)
	assertEntry(t, graph, "mid", nil, []string{"midchild"})
	assertEntry(t, graph, "midchild", []string{"mid"}, nil)

	// Mixing the legitimate new source with hub's own downstream report must
	// be refused as a whole: report reaches hub through detail, so the edge
	// hub -> report would close a cycle. The error names report and the cycle,
	// and the valid mid listed first must not have changed anything.
	beforeCycle := snapshot(graph)
	rawImpactsBeforeCycle := mustImpacts(t, graph, "raw")
	reportUpstreamsBeforeCycle := mustUpstreams(t, graph, "report")
	err = Register(graph, Dataset{Name: "hub"}, []string{"mid", "report"})
	if err == nil || !strings.Contains(err.Error(), "cycle") || !strings.Contains(err.Error(), "report") {
		t.Fatalf("want cycle error naming report, got %v", err)
	}
	if !reflect.DeepEqual(snapshot(graph), beforeCycle) {
		t.Fatalf("rejected cyclic registration changed graph: before=%v after=%v", beforeCycle, snapshot(graph))
	}
	assertEntry(t, graph, "hub", []string{"raw"}, []string{"detail"})
	assertEntry(t, graph, "mid", nil, []string{"midchild"})
	assertEntry(t, graph, "raw", nil, []string{"hub", "side"})
	if got := mustImpacts(t, graph, "raw"); !reflect.DeepEqual(got, rawImpactsBeforeCycle) {
		t.Fatalf("impacts after rejected cyclic registration = %v, want %v", got, rawImpactsBeforeCycle)
	}
	if got := mustUpstreams(t, graph, "report"); !reflect.DeepEqual(got, reportUpstreamsBeforeCycle) {
		t.Fatalf("report upstreams after rejected cyclic registration = %v, want %v", got, reportUpstreamsBeforeCycle)
	}
	assertConsistent(t, graph)

	// Replace hub's direct upstreams wholesale with the reused name mid. This
	// must succeed: mid and hub are independent registrations, and the edge
	// mid -> hub closes no cycle.
	mustRegister(t, graph, "hub", "mid")
	assertConsistent(t, graph)

	// The graph now holds two independent registrations under the old and new
	// names. raw released hub but keeps its unrelated downstream side; mid
	// keeps its own downstream midchild and gains hub appended at the end;
	// hub keeps its original downstream chain untouched.
	if got, want := len(graph), 7; got != want {
		t.Fatalf("dataset count = %d, want %d", got, want)
	}
	assertEntry(t, graph, "raw", nil, []string{"side"})
	assertEntry(t, graph, "mid", nil, []string{"midchild", "hub"})
	assertEntry(t, graph, "hub", []string{"mid"}, []string{"detail"})
	assertEntry(t, graph, "detail", []string{"hub"}, []string{"report"})
	assertEntry(t, graph, "report", []string{"detail"}, nil)
	assertEntry(t, graph, "midchild", []string{"mid"}, nil)
	assertEntry(t, graph, "side", []string{"raw"}, nil)

	// From raw the whole moved chain is gone; only the unrelated side remains.
	wantRawImpacts := []Impact{
		{Dataset: "side", Distance: 1, Path: []string{"raw", "side"}},
	}
	rawImpacts := mustImpacts(t, graph, "raw")
	if !reflect.DeepEqual(rawImpacts, wantRawImpacts) {
		t.Fatalf("impacts from raw = %v, want %v", rawImpacts, wantRawImpacts)
	}

	// From mid the chain unfolds along mid -> hub -> detail -> report at
	// distances 1, 2 and 3, with midchild as mid's other direct downstream.
	wantMidImpacts := []Impact{
		{Dataset: "hub", Distance: 1, Path: []string{"mid", "hub"}},
		{Dataset: "midchild", Distance: 1, Path: []string{"mid", "midchild"}},
		{Dataset: "detail", Distance: 2, Path: []string{"mid", "hub", "detail"}},
		{Dataset: "report", Distance: 3, Path: []string{"mid", "hub", "detail", "report"}},
	}
	midImpacts := mustImpacts(t, graph, "mid")
	if !reflect.DeepEqual(midImpacts, wantMidImpacts) {
		t.Fatalf("impacts from mid = %v, want %v", midImpacts, wantMidImpacts)
	}
	assertImpactOnce(t, midImpacts, "hub", 1, []string{"mid", "hub"})
	assertImpactOnce(t, midImpacts, "detail", 2, []string{"mid", "hub", "detail"})
	assertImpactOnce(t, midImpacts, "report", 3, []string{"mid", "hub", "detail", "report"})

	// Tracing report's provenance walks detail, hub, mid in order; raw is no
	// longer a source of report. Paths are written along the actual
	// derivation direction, from each source toward report.
	wantReportUpstreams := []Upstream{
		{Dataset: "detail", Distance: 1, Path: []string{"detail", "report"}},
		{Dataset: "hub", Distance: 2, Path: []string{"hub", "detail", "report"}},
		{Dataset: "mid", Distance: 3, Path: []string{"mid", "hub", "detail", "report"}},
	}
	reportUpstreams := mustUpstreams(t, graph, "report")
	if !reflect.DeepEqual(reportUpstreams, wantReportUpstreams) {
		t.Fatalf("upstreams of report = %v, want %v", reportUpstreams, wantReportUpstreams)
	}
	assertUpstreamOnce(t, reportUpstreams, "mid", 3, []string{"mid", "hub", "detail", "report"})
	for _, up := range reportUpstreams {
		if up.Dataset == "raw" {
			t.Fatalf("raw leaked into report upstreams after replacement: %v", reportUpstreams)
		}
	}
}
