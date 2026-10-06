package chainledger

import (
	"reflect"
	"strings"
	"testing"
)

// The scoped fixture's worked graph, re-used here: source derives report
// directly, through the short branch source -> a -> report and through the
// longer branch source -> b -> mid -> report; report also depends on the
// independent root lone and derives view. extra feeds a, pre feeds source and
// sidetrack is a source downstream that never reaches report.
//
//	pre ──> source ──┬──> a ──────────┐
//	                │   ^            ├──> report ──> view
//	                │   └── extra    │
//	                ├──> b ──> mid ──┘
//	                ├──> sidetrack   (source downstream that never reaches report)
//	                └──> report (direct)
//	                      lone ──────┘
//
// The exact scoped export texts before and after each replacement of report's
// direct-upstream list. Both orderings are fully determined by the names.
const (
	scopedReregisterBefore = `{"nodes":["a","b","mid","report","source"],` +
		`"edges":[{"from":"a","to":"report"},{"from":"b","to":"mid"},` +
		`{"from":"mid","to":"report"},{"from":"source","to":"a"},` +
		`{"from":"source","to":"b"},{"from":"source","to":"report"}]}`
	scopedReregisterLongRouteOnly = `{"nodes":["b","mid","report","source"],` +
		`"edges":[{"from":"b","to":"mid"},{"from":"mid","to":"report"},` +
		`{"from":"source","to":"b"}]}`
	scopedReregisterBothBranches = `{"nodes":["a","b","mid","report","source"],` +
		`"edges":[{"from":"a","to":"report"},{"from":"b","to":"mid"},` +
		`{"from":"mid","to":"report"},{"from":"source","to":"a"},` +
		`{"from":"source","to":"b"}]}`
)

// Regression for the scoped export after a same-name Register replaced the
// join dataset's whole direct-upstream list. Re-registering report with mid
// and lone drops the direct source -> report edge and the short branch's
// a -> report edge, but source still derives report through the surviving
// longer route source -> b -> mid -> report, so the scoped export must keep
// source, b, mid and report with every surviving direct dependency on that
// route. The dropped branch's a leaves the document even though it stays
// registered and stays a downstream of source; the independent lone and its
// lone -> report edge never enter, and neither does report's downstream view.
func TestExportSourceTargetLineageAfterReregisterKeepsLongRoute(t *testing.T) {
	graph := scopedFixture(t)
	assertConsistent(t, graph)

	// The export before the replacement carries all three routes complete.
	if out := mustExportScoped(t, graph, "source", "report"); out != scopedReregisterBefore {
		t.Errorf("export before re-register =\n%s\nwant:\n%s", out, scopedReregisterBefore)
	}

	// Replace report's whole direct-upstream list: drop the direct source edge
	// and the short branch's a edge, keep the longer route via mid and the
	// independent lone. report's own downstream view is not part of the
	// request and must stay as it was.
	mustRegister(t, graph, "report", "mid", "lone")
	assertConsistent(t, graph)
	assertEntry(t, graph, "report", []string{"mid", "lone"}, []string{"view"})
	// a left report's derivation but keeps its registration and its remaining
	// dependencies; it only lost the removed reverse edge to report.
	assertEntry(t, graph, "a", []string{"source", "extra"}, nil)
	assertEntry(t, graph, "source", []string{"pre"}, []string{"a", "b", "sidetrack"})
	assertEntry(t, graph, "mid", []string{"b"}, []string{"report"})
	assertEntry(t, graph, "lone", nil, []string{"report"})

	before := snapshotExportGraph(graph)
	out := mustExportScoped(t, graph, "source", "report")
	if out != scopedReregisterLongRouteOnly {
		t.Errorf("export after re-register =\n%s\nwant:\n%s", out, scopedReregisterLongRouteOnly)
	}
	doc := parseExport(t, out)

	// The surviving longer route participates in full: source reaches report
	// through b -> mid, so source, b, mid and report each stay listed once.
	wantNodes := []string{"b", "mid", "report", "source"}
	if !reflect.DeepEqual(doc.Nodes, wantNodes) {
		t.Errorf("nodes = %v, want %v", doc.Nodes, wantNodes)
	}
	wantEdges := [][2]string{
		{"b", "mid"},
		{"mid", "report"},
		{"source", "b"},
	}
	if got := exportEdgeNames(doc); !reflect.DeepEqual(got, wantEdges) {
		t.Errorf("edges = %v, want %v", got, wantEdges)
	}

	// a can no longer reach report, so it left the document even though it is
	// still registered and still a downstream of source; lone, view and the
	// other out-of-scope datasets never enter.
	for _, excluded := range []string{"a", "pre", "extra", "sidetrack", "lone", "view"} {
		for _, node := range doc.Nodes {
			if node == excluded {
				t.Errorf("nodes must not contain %q: %v", excluded, doc.Nodes)
			}
		}
	}
	for _, e := range doc.Edges {
		if e.From == "lone" || e.To == "view" {
			t.Errorf("out-of-scope edge %v survived in %v", e, doc.Edges)
		}
	}

	// The export is read-only over the post-replacement state.
	assertGraphUnchanged(t, before, graph)
}

// Removing only the direct source -> report edge while keeping both branches:
// the short route through a and the longer route through b -> mid must both
// appear complete, and the shared join report and its downstream dependencies
// appear exactly once — the longer route is never dropped because a shorter
// one exists.
func TestExportSourceTargetLineageAfterReregisterKeepsBothBranches(t *testing.T) {
	graph := scopedFixture(t)
	assertConsistent(t, graph)

	// Replace report's direct-upstream list dropping only the direct edge.
	mustRegister(t, graph, "report", "a", "mid", "lone")
	assertConsistent(t, graph)
	assertEntry(t, graph, "report", []string{"a", "mid", "lone"}, []string{"view"})
	assertEntry(t, graph, "source", []string{"pre"}, []string{"a", "b", "sidetrack"})

	before := snapshotExportGraph(graph)
	out := mustExportScoped(t, graph, "source", "report")
	if out != scopedReregisterBothBranches {
		t.Errorf("export after re-register =\n%s\nwant:\n%s", out, scopedReregisterBothBranches)
	}
	doc := parseExport(t, out)

	wantNodes := []string{"a", "b", "mid", "report", "source"}
	if !reflect.DeepEqual(doc.Nodes, wantNodes) {
		t.Errorf("nodes = %v, want %v", doc.Nodes, wantNodes)
	}
	wantEdges := [][2]string{
		{"a", "report"},
		{"b", "mid"},
		{"mid", "report"},
		{"source", "a"},
		{"source", "b"},
	}
	if got := exportEdgeNames(doc); !reflect.DeepEqual(got, wantEdges) {
		t.Errorf("edges = %v, want %v", got, wantEdges)
	}

	// The shared join and every route node appear exactly once: the exact-list
	// comparisons above pin the counts, and the dropped direct edge is gone.
	for _, e := range doc.Edges {
		if e.From == "source" && e.To == "report" {
			t.Errorf("dropped direct edge source -> report survived in %v", doc.Edges)
		}
	}
	assertGraphUnchanged(t, before, graph)
}

// Replacing report's upstreams with only the independent lone leaves source
// and report registered but with no derivation route between them: the scoped
// export succeeds with two empty (non-null) arrays and keeps no isolated
// endpoint, instead of reporting either name as unregistered.
func TestExportSourceTargetLineageAfterReregisterNoRouteLeft(t *testing.T) {
	graph := scopedFixture(t)
	assertConsistent(t, graph)

	mustRegister(t, graph, "report", "lone")
	assertConsistent(t, graph)
	assertEntry(t, graph, "report", []string{"lone"}, []string{"view"})

	before := snapshotExportGraph(graph)
	out := mustExportScoped(t, graph, "source", "report")
	if want := `{"nodes":[],"edges":[]}`; out != want {
		t.Errorf("export = %s, want %s", out, want)
	}
	doc := parseExport(t, out)
	if len(doc.Nodes) != 0 || doc.Nodes == nil {
		t.Errorf("nodes must be a non-nil empty array, got %#v", doc.Nodes)
	}
	if len(doc.Edges) != 0 || doc.Edges == nil {
		t.Errorf("edges must be a non-nil empty array, got %#v", doc.Edges)
	}
	assertGraphUnchanged(t, before, graph)
}

// A rejected replacement must not partially apply: re-registering report with
// the registered a and mid followed by the unregistered ghost fails naming
// ghost, and report — plus every related node — keeps its pre-request
// parents, children and list orders. A later scoped export of source and
// report is byte-identical to the pre-request text and contains no trace of a
// half-applied replacement.
func TestExportSourceTargetLineageFailedReregisterLeavesExportUntouched(t *testing.T) {
	graph := scopedFixture(t)
	assertConsistent(t, graph)

	exportBefore := mustExportScoped(t, graph, "source", "report")
	if exportBefore != scopedReregisterBefore {
		t.Fatalf("export before failed re-register =\n%s\nwant:\n%s", exportBefore, scopedReregisterBefore)
	}
	before := snapshotExportGraph(graph)

	err := Register(graph, Dataset{Name: "report"}, []string{"a", "mid", "ghost"})
	if err == nil || !strings.Contains(err.Error(), "ghost") {
		t.Fatalf("want unknown-parent error naming ghost, got %v", err)
	}

	// No partial effect: the whole graph, list orders included, is exactly as
	// before the request; report never lost the direct source edge nor any
	// other upstream, and a and mid gained nothing new.
	assertGraphUnchanged(t, before, graph)
	assertEntry(t, graph, "report", []string{"source", "a", "mid", "lone"}, []string{"view"})
	assertEntry(t, graph, "a", []string{"source", "extra"}, []string{"report"})
	assertEntry(t, graph, "mid", []string{"b"}, []string{"report"})
	assertEntry(t, graph, "source", []string{"pre"}, []string{"a", "b", "sidetrack", "report"})
	assertConsistent(t, graph)

	// The export after the rejected request matches the pre-request text
	// exactly, with no partial relation from the failed upstream list.
	exportAfter := mustExportScoped(t, graph, "source", "report")
	if exportAfter != exportBefore {
		t.Errorf("export changed after rejected re-register:\nbefore: %s\nafter:  %s",
			exportBefore, exportAfter)
	}
	if strings.Contains(exportAfter, "ghost") {
		t.Errorf("export must not contain ghost after the rejected request: %s", exportAfter)
	}
	assertGraphUnchanged(t, before, graph)
}
