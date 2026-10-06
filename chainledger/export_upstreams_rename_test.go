package chainledger

import (
	"reflect"
	"strings"
	"testing"
)

// exportRenameScenario builds the rename regression lineage. raw feeds a and b,
// and feeds join directly as well; a feeds mid, b feeds c; join depends on mid
// and c together (c declared ahead of mid on purpose); report depends on join
// and derives view; sidedown depends on mid but takes no part in report's
// derivation; isolated stands alone. The later renames move nodes across the
// document's sort order, so the renamed node itself moves in both arrays.
func exportRenameScenario(t *testing.T) map[string]*Lineage {
	t.Helper()
	return buildRegisteredGraph(t, [][]string{
		{"raw"},
		{"b", "raw"},
		{"a", "raw"},
		{"mid", "a"},
		{"c", "b"},
		{"join", "raw", "c", "mid"}, // merge: raw arrives directly and through both branches
		{"report", "join"},
		{"view", "report"},
		{"sidedown", "mid"}, // downstream of mid that does not derive report
		{"isolated"},
	})
}

// The exact JSON text before any rename; nodes in Go string order and edges
// ordered by from then to.
const exportRenameBefore = `{"nodes":["a","b","c","join","mid","raw","report"],` +
	`"edges":[{"from":"a","to":"mid"},{"from":"b","to":"c"},` +
	`{"from":"c","to":"join"},{"from":"join","to":"report"},` +
	`{"from":"mid","to":"join"},{"from":"raw","to":"a"},` +
	`{"from":"raw","to":"b"},{"from":"raw","to":"join"}]}`

// After mid is renamed to zeta: it sorts to the end of the node list, a ->
// zeta stays first under "from", and zeta -> join moves to the document's end
// behind the raw edges. join -> report keeps its position between the c and
// raw edges because "join" still sorts before "raw". Every relationship is
// otherwise unchanged; view, sidedown and isolated stay out.
const exportRenameAfterMid = `{"nodes":["a","b","c","join","raw","report","zeta"],` +
	`"edges":[{"from":"a","to":"zeta"},{"from":"b","to":"c"},` +
	`{"from":"c","to":"join"},{"from":"join","to":"report"},` +
	`{"from":"raw","to":"a"},{"from":"raw","to":"b"},` +
	`{"from":"raw","to":"join"},{"from":"zeta","to":"join"}]}`

// Regression for exporting report after an intermediate dataset in its
// derivation is renamed. Renaming changes only names, never dependencies: the
// target and all its upstreams still appear exactly once, every original direct
// dependency survives exactly once in the upstream -> derived direction, and
// the old name appears nowhere. The merge node join has several direct
// upstreams at once (raw, c, mid), and the renamed mid has a downstream
// sidedown that does not derive report: every reference into or out of the
// renamed node must move, while sidedown, view and isolated never enter the
// document. raw reaches join BOTH directly and through a -> mid and b -> c, so
// renaming mid must not collapse the export onto just the shortest raw -> join
// branch. The new name reorders the JSON positions rather than keeping the old
// name's slots, and neither the rename nor the export may reorder the stored
// parent/child lists.
func TestExportUpstreamLineageAfterRename(t *testing.T) {
	graph := exportRenameScenario(t)
	assertConsistent(t, graph)

	if out := mustExport(t, graph, "report"); out != exportRenameBefore {
		t.Fatalf("export before rename =\n%s\nwant:\n%s", out, exportRenameBefore)
	}

	// Rename the intermediate dataset mid to zeta. The node keeps both lists,
	// and a and join keep their list positions with the reference rewritten in
	// place; sidedown references mid too and must be rewired even though it
	// never enters report's export.
	mustRename(t, graph, "mid", "zeta")
	assertConsistent(t, graph)
	if _, ok := graph["mid"]; ok {
		t.Fatal("old name mid still present in graph")
	}
	if got, want := len(graph), 10; got != want {
		t.Fatalf("dataset count = %d, want %d (rename must not merge nodes)", got, want)
	}
	assertEntry(t, graph, "zeta", []string{"a"}, []string{"join", "sidedown"})
	assertEntry(t, graph, "a", []string{"raw"}, []string{"zeta"})
	assertEntry(t, graph, "join", []string{"raw", "c", "zeta"}, []string{"report"})
	assertEntry(t, graph, "sidedown", []string{"zeta"}, nil)
	assertEntry(t, graph, "raw", nil, []string{"b", "a", "join"})
	assertEntry(t, graph, "report", []string{"join"}, []string{"view"})

	before := snapshotExportGraph(graph)
	out := mustExport(t, graph, "report")
	if out != exportRenameAfterMid {
		t.Errorf("export after rename =\n%s\nwant:\n%s", out, exportRenameAfterMid)
	}
	doc := parseExport(t, out)

	// Same seven datasets as before the rename, each exactly once, under the
	// current names and in Go string order: mid used to sit between join and raw;
	// zeta sorts to the end.
	wantNodes := []string{"a", "b", "c", "join", "raw", "report", "zeta"}
	if !reflect.DeepEqual(doc.Nodes, wantNodes) {
		t.Errorf("nodes = %v, want %v", doc.Nodes, wantNodes)
	}

	// All eight direct dependencies survive exactly once, still written from
	// the upstream toward the derived dataset; both edges touching the renamed
	// node (a -> zeta and zeta -> join) carry the new name.
	wantEdges := [][2]string{
		{"a", "zeta"},
		{"b", "c"},
		{"c", "join"},
		{"join", "report"},
		{"raw", "a"},
		{"raw", "b"},
		{"raw", "join"},
		{"zeta", "join"},
	}
	if got := exportEdgeNames(doc); !reflect.DeepEqual(got, wantEdges) {
		t.Errorf("edges = %v, want %v", got, wantEdges)
	}

	// The direct raw -> join relationship and the indirect raw -> a -> zeta ->
	// join route both survive: the export keeps every branch, not only the
	// shortest path. zeta and its incoming edge prove the longer branch stayed.
	edgeSet := map[[2]string]bool{}
	for _, e := range wantEdges {
		edgeSet[e] = true
	}
	for _, want := range [][2]string{
		{"raw", "join"}, // short, direct
		{"a", "zeta"},   // longer branch, edge into the renamed node
		{"zeta", "join"},
		{"raw", "a"},
	} {
		if !edgeSet[want] {
			t.Errorf("branch edge %v missing from %v", want, wantEdges)
		}
	}

	// The old name occurs nowhere in the exported text, while view (a
	// downstream of report), sidedown (a downstream of the renamed node that
	// does not derive report) and isolated never enter report's document.
	if strings.Contains(out, "mid") {
		t.Errorf("old name mid leaked into export: %s", out)
	}
	for _, excluded := range []string{"view", "sidedown", "isolated"} {
		if strings.Contains(out, excluded) {
			t.Errorf("unrelated dataset %q leaked into report's export: %s", excluded, out)
		}
	}

	// The export must sort into its own output without reordering the stored
	// up/downstream lists: every list keeps its pre-export order (including the
	// non-name-sorted raw children [b a join]).
	assertGraphUnchanged(t, before, graph)
}

// Renaming the merge node itself moves every edge that starts or ends at it:
// join -> hub and the three incoming raw/c/zeta -> join edges all use the new
// name, the node count is unchanged, and the unrelated downstreams stay out.
func TestExportUpstreamLineageAfterRenameMergeNode(t *testing.T) {
	graph := exportRenameScenario(t)
	mustRename(t, graph, "mid", "zeta")
	assertConsistent(t, graph)

	mustRename(t, graph, "join", "hub")
	assertConsistent(t, graph)
	if got, want := len(graph), 10; got != want {
		t.Fatalf("dataset count = %d, want %d", got, want)
	}
	assertEntry(t, graph, "hub", []string{"raw", "c", "zeta"}, []string{"report"})
	assertEntry(t, graph, "report", []string{"hub"}, []string{"view"})
	assertEntry(t, graph, "zeta", []string{"a"}, []string{"hub", "sidedown"})
	assertEntry(t, graph, "raw", nil, []string{"b", "a", "hub"})

	before := snapshotExportGraph(graph)
	out := mustExport(t, graph, "report")
	doc := parseExport(t, out)

	wantNodes := []string{"a", "b", "c", "hub", "raw", "report", "zeta"}
	if !reflect.DeepEqual(doc.Nodes, wantNodes) {
		t.Errorf("nodes = %v, want %v", doc.Nodes, wantNodes)
	}
	wantEdges := [][2]string{
		{"a", "zeta"},
		{"b", "c"},
		{"c", "hub"},
		{"hub", "report"},
		{"raw", "a"},
		{"raw", "b"},
		{"raw", "hub"},
		{"zeta", "hub"},
	}
	if got := exportEdgeNames(doc); !reflect.DeepEqual(got, wantEdges) {
		t.Errorf("edges = %v, want %v", got, wantEdges)
	}
	for _, stale := range []string{"mid", "join"} {
		if strings.Contains(out, stale) {
			t.Errorf("old name %q leaked into export: %s", stale, out)
		}
	}
	for _, excluded := range []string{"view", "sidedown", "isolated"} {
		if strings.Contains(out, excluded) {
			t.Errorf("unrelated dataset %q leaked into report's export: %s", excluded, out)
		}
	}
	assertGraphUnchanged(t, before, graph)
}

// A rejected rename (new name already registered to another dataset) returns
// an error naming the taken name, merges nothing and replaces no reference;
// the export of report afterwards is byte-identical to the text produced
// before the failed request.
func TestExportUpstreamLineageFailedRenameLeavesExportUntouched(t *testing.T) {
	graph := exportRenameScenario(t)
	assertConsistent(t, graph)

	exportBefore := mustExport(t, graph, "report")
	if exportBefore != exportRenameBefore {
		t.Fatalf("export before failed rename =\n%s\nwant:\n%s", exportBefore, exportRenameBefore)
	}
	before := snapshotExportGraph(graph)

	// c already names the b-side branch node: mid cannot take it. The error
	// must name the occupied new name.
	err := Rename(graph, "mid", "c")
	if err == nil || !strings.Contains(err.Error(), "c") {
		t.Fatalf("want name-in-use error naming c, got %v", err)
	}

	// No merge, no rewritten reference: both nodes and every list keep their
	// exact pre-request state.
	assertGraphUnchanged(t, before, graph)
	if got, want := len(graph), 10; got != want {
		t.Fatalf("dataset count = %d, want %d", got, want)
	}
	assertEntry(t, graph, "mid", []string{"a"}, []string{"join", "sidedown"})
	assertEntry(t, graph, "c", []string{"b"}, []string{"join"})
	assertEntry(t, graph, "join", []string{"raw", "c", "mid"}, []string{"report"})
	assertEntry(t, graph, "a", []string{"raw"}, []string{"mid"})
	assertConsistent(t, graph)

	// The export text is exactly what it was before the failed request, and
	// still contains the old name mid rather than the rejected new name.
	exportAfter := mustExport(t, graph, "report")
	if exportAfter != exportBefore {
		t.Errorf("export changed after rejected rename:\nbefore: %s\nafter:  %s",
			exportBefore, exportAfter)
	}
	if !strings.Contains(exportAfter, `"mid"`) {
		t.Errorf("export must still name mid after the rejected rename: %s", exportAfter)
	}
	assertGraphUnchanged(t, before, graph)
}
