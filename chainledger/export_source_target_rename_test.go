package chainledger

import (
	"reflect"
	"strings"
	"testing"
)

// scopedRenameFixture builds the scoped-export rename regression lineage:
//
//	extra ──> mid (mid depends on an independent source as well)
//	source ──> mid ──> step ──> report (the long branch)
//	source ──> report                (the direct relation)
//	mid ──> report                   (mid also reaches report in one hop)
//	mid ──> sidedown                 (a mid downstream that never reaches report)
//	report ──> view                  (target downstream)
//	isolated                         (standalone)
//
// source derives report directly and through mid (both via mid -> report and
// via mid -> step -> report), so mid and report each lie on several routes and
// must still appear once each. extra, extra -> mid, sidedown, view and
// isolated never take part in source's derivation of report and stay out.
func scopedRenameFixture(t *testing.T) map[string]*Lineage {
	t.Helper()
	return buildRegisteredGraph(t, [][]string{
		{"source"},
		{"extra"},
		{"mid", "source", "extra"},
		{"step", "mid"},
		{"report", "source", "mid", "step"},
		{"sidedown", "mid"},
		{"view", "report"},
		{"isolated"},
	})
}

// The exact scoped JSON text before any rename: nodes in Go string order,
// edges upstream -> derived ordered by from then to.
const scopedRenameBefore = `{"nodes":["mid","report","source","step"],` +
	`"edges":[{"from":"mid","to":"report"},{"from":"mid","to":"step"},` +
	`{"from":"source","to":"mid"},{"from":"source","to":"report"},` +
	`{"from":"step","to":"report"}]}`

// After mid is renamed to zmid: the node moves from the front of the node
// list to the end, and its three edges (source -> zmid in, zmid -> report and
// zmid -> step out) move to their new from-then-to positions. The selected
// set and the direct dependencies are otherwise exactly the pre-rename ones —
// same four nodes, same five edges, only the name substituted.
const scopedRenameAfterZmid = `{"nodes":["report","source","step","zmid"],` +
	`"edges":[{"from":"source","to":"report"},{"from":"source","to":"zmid"},` +
	`{"from":"step","to":"report"},{"from":"zmid","to":"report"},` +
	`{"from":"zmid","to":"step"}]}`

// After zmid is renamed to amid: the node moves back to the front of the node
// list and its edges lead the edge array again. amid contains the old name's
// text ("mid") as a substring, yet matching is by full name: the document
// holds amid, and mid itself appears neither as a node nor as an edge
// endpoint.
const scopedRenameAfterAmid = `{"nodes":["amid","report","source","step"],` +
	`"edges":[{"from":"amid","to":"report"},{"from":"amid","to":"step"},` +
	`{"from":"source","to":"amid"},{"from":"source","to":"report"},` +
	`{"from":"step","to":"report"}]}`

// assertOldNameAbsent fails if oldName occurs as a node or as an edge
// endpoint in the document. The comparison is by full name, not substring:
// the new name may legitimately contain the old name's text.
func assertOldNameAbsent(t *testing.T, doc exportDocument, oldName string) {
	t.Helper()
	for _, node := range doc.Nodes {
		if node == oldName {
			t.Errorf("old name %q still present as a node: %v", oldName, doc.Nodes)
		}
	}
	for _, e := range doc.Edges {
		if e.From == oldName || e.To == oldName {
			t.Errorf("old name %q still present as an edge endpoint: %+v", oldName, e)
		}
	}
}

// Regression for the source-scoped export after an intermediate dataset on
// the derivation routes is renamed. The direct source -> report relation and
// the longer branches through the renamed node all survive: the export keeps
// answering "which existing relations let source participate in report's
// derivation", with every selected node and every direct dependency appearing
// exactly once under the current names. The independent source extra and its
// edge into the renamed node, the dead-end downstream sidedown, the target's
// downstream view and the standalone isolated stay excluded. The document
// order follows the new names, while the graph's stored upstream/downstream
// lists keep their original positions and the export itself changes nothing.
func TestExportSourceTargetLineageAfterRename(t *testing.T) {
	graph := scopedRenameFixture(t)
	assertConsistent(t, graph)

	if out := mustExportScoped(t, graph, "source", "report"); out != scopedRenameBefore {
		t.Fatalf("scoped export before rename =\n%s\nwant:\n%s", out, scopedRenameBefore)
	}

	// Rename the intermediate dataset mid to zmid, a name that sorts past every
	// other selected name. The node keeps both lists; every neighbor's
	// reference is rewritten in its original list position.
	mustRename(t, graph, "mid", "zmid")
	assertConsistent(t, graph)
	if _, ok := graph["mid"]; ok {
		t.Fatal("old name mid still present in graph")
	}
	if got, want := len(graph), 8; got != want {
		t.Fatalf("dataset count = %d, want %d (rename must not merge nodes)", got, want)
	}
	assertEntry(t, graph, "zmid", []string{"source", "extra"}, []string{"step", "report", "sidedown"})
	assertEntry(t, graph, "source", nil, []string{"zmid", "report"})
	assertEntry(t, graph, "extra", nil, []string{"zmid"})
	assertEntry(t, graph, "step", []string{"zmid"}, []string{"report"})
	assertEntry(t, graph, "report", []string{"source", "zmid", "step"}, []string{"view"})
	assertEntry(t, graph, "sidedown", []string{"zmid"}, nil)

	before := snapshotExportGraph(graph)
	out := mustExportScoped(t, graph, "source", "report")
	if out != scopedRenameAfterZmid {
		t.Errorf("scoped export after rename =\n%s\nwant:\n%s", out, scopedRenameAfterZmid)
	}
	doc := parseExport(t, out)

	// Same four datasets as before the rename, each exactly once, under the
	// current names and in Go string order: mid used to lead the list; zmid
	// sorts to the end.
	wantNodes := []string{"report", "source", "step", "zmid"}
	if !reflect.DeepEqual(doc.Nodes, wantNodes) {
		t.Errorf("nodes = %v, want %v", doc.Nodes, wantNodes)
	}

	// All five direct dependencies survive exactly once, still written from
	// the upstream toward the derived dataset; the three edges touching the
	// renamed node carry the new name on both endpoints.
	wantEdges := [][2]string{
		{"source", "report"},
		{"source", "zmid"},
		{"step", "report"},
		{"zmid", "report"},
		{"zmid", "step"},
	}
	if got := exportEdgeNames(doc); !reflect.DeepEqual(got, wantEdges) {
		t.Errorf("edges = %v, want %v", got, wantEdges)
	}

	// The direct relation and both longer branches through the renamed node
	// are all present: source -> report, source -> zmid -> report and
	// source -> zmid -> step -> report. report is reached over three routes
	// and zmid lies on two of them, yet each appears once.
	edgeSet := map[[2]string]bool{}
	for _, e := range wantEdges {
		edgeSet[e] = true
	}
	for _, want := range [][2]string{
		{"source", "report"}, // direct
		{"zmid", "report"},   // one hop through the renamed node
		{"zmid", "step"},     // two hops through the renamed node
		{"step", "report"},
	} {
		if !edgeSet[want] {
			t.Errorf("route edge %v missing from %v", want, wantEdges)
		}
	}

	// The old name occurs nowhere as a node or edge endpoint, while extra
	// (the renamed node's independent source), the extra -> zmid edge,
	// sidedown (a renamed-node downstream that cannot reach report), view and
	// isolated never enter the scoped document.
	assertOldNameAbsent(t, doc, "mid")
	for _, excluded := range []string{"extra", "sidedown", "view", "isolated"} {
		for _, node := range doc.Nodes {
			if node == excluded {
				t.Errorf("nodes must not contain %q: %v", excluded, doc.Nodes)
			}
		}
	}
	for _, e := range doc.Edges {
		if e.From == "extra" || e.To == "extra" {
			t.Errorf("independent source edge leaked into scoped export: %+v", e)
		}
	}

	// The export sorts into its own output without touching the graph: no
	// node, edge or stored list order changed, including the non-name-sorted
	// zmid children [step report sidedown].
	assertGraphUnchanged(t, before, graph)

	// Rename again to amid: the new name contains the old name's text and
	// sorts ahead of every other selected name. Matching stays by full name,
	// and the document order follows the current names once more.
	mustRename(t, graph, "zmid", "amid")
	assertConsistent(t, graph)
	assertEntry(t, graph, "amid", []string{"source", "extra"}, []string{"step", "report", "sidedown"})
	assertEntry(t, graph, "source", nil, []string{"amid", "report"})
	assertEntry(t, graph, "report", []string{"source", "amid", "step"}, []string{"view"})

	before = snapshotExportGraph(graph)
	out = mustExportScoped(t, graph, "source", "report")
	if out != scopedRenameAfterAmid {
		t.Errorf("scoped export after second rename =\n%s\nwant:\n%s", out, scopedRenameAfterAmid)
	}
	doc = parseExport(t, out)
	if !reflect.DeepEqual(doc.Nodes, []string{"amid", "report", "source", "step"}) {
		t.Errorf("nodes after second rename = %v", doc.Nodes)
	}
	assertOldNameAbsent(t, doc, "mid")
	assertOldNameAbsent(t, doc, "zmid")
	assertGraphUnchanged(t, before, graph)
}

// A rejected rename — the new name is already registered to another dataset —
// fails even when the occupying dataset lies outside the scoped export. The
// error names the taken name, no reference is partially rewritten, and the
// scoped export afterwards is byte-identical to the text produced before the
// failed request.
func TestExportSourceTargetLineageFailedRenameLeavesExportUntouched(t *testing.T) {
	graph := scopedRenameFixture(t)
	assertConsistent(t, graph)

	exportBefore := mustExportScoped(t, graph, "source", "report")
	if exportBefore != scopedRenameBefore {
		t.Fatalf("scoped export before failed rename =\n%s\nwant:\n%s", exportBefore, scopedRenameBefore)
	}
	before := snapshotExportGraph(graph)

	// extra is registered but excluded from the source -> report document:
	// the conflict must still block the rename, with an error naming extra.
	err := Rename(graph, "mid", "extra")
	if err == nil || !strings.Contains(err.Error(), "extra") {
		t.Fatalf("want name-in-use error naming extra, got %v", err)
	}

	// No merge, no rewritten reference: both nodes and every list keep their
	// exact pre-request state.
	assertGraphUnchanged(t, before, graph)
	if got, want := len(graph), 8; got != want {
		t.Fatalf("dataset count = %d, want %d", got, want)
	}
	assertEntry(t, graph, "mid", []string{"source", "extra"}, []string{"step", "report", "sidedown"})
	assertEntry(t, graph, "extra", nil, []string{"mid"})
	assertEntry(t, graph, "report", []string{"source", "mid", "step"}, []string{"view"})
	assertConsistent(t, graph)

	// A conflict with a name registered to the dead-end downstream — also
	// outside the export — is rejected the same way.
	err = Rename(graph, "mid", "sidedown")
	if err == nil || !strings.Contains(err.Error(), "sidedown") {
		t.Fatalf("want name-in-use error naming sidedown, got %v", err)
	}
	assertGraphUnchanged(t, before, graph)

	// The scoped export text is exactly what it was before the failed
	// requests, and still names mid rather than either rejected new name.
	exportAfter := mustExportScoped(t, graph, "source", "report")
	if exportAfter != exportBefore {
		t.Errorf("scoped export changed after rejected rename:\nbefore: %s\nafter:  %s",
			exportBefore, exportAfter)
	}
	if !strings.Contains(exportAfter, `"mid"`) {
		t.Errorf("export must still name mid after the rejected renames: %s", exportAfter)
	}
	assertGraphUnchanged(t, before, graph)
}

// After the rename, the full upstream export and the other lineage queries
// keep working against the same graph: the full export of report still
// includes the independent source extra that the scoped export excludes, and
// the upstream/downstream queries report the renamed dataset under its new
// name at its old distances.
func TestExportSourceTargetLineageAfterRenameCompatibleQueries(t *testing.T) {
	graph := scopedRenameFixture(t)
	mustRename(t, graph, "mid", "zmid")
	assertConsistent(t, graph)

	before := snapshotExportGraph(graph)

	// The full upstream export of report sees report's whole ancestry,
	// including extra and its edge into zmid — the parts the source-scoped
	// document deliberately leaves out.
	full := mustExport(t, graph, "report")
	doc := parseExport(t, full)
	wantNodes := []string{"extra", "report", "source", "step", "zmid"}
	if !reflect.DeepEqual(doc.Nodes, wantNodes) {
		t.Errorf("full export nodes = %v, want %v", doc.Nodes, wantNodes)
	}
	wantEdges := [][2]string{
		{"extra", "zmid"},
		{"source", "report"},
		{"source", "zmid"},
		{"step", "report"},
		{"zmid", "report"},
		{"zmid", "step"},
	}
	if got := exportEdgeNames(doc); !reflect.DeepEqual(got, wantEdges) {
		t.Errorf("full export edges = %v, want %v", got, wantEdges)
	}

	// The upstream query lists the renamed dataset once, at its unchanged
	// shortest distance, with explanation paths written under the new name.
	upstreams := mustUpstreams(t, graph, "report")
	assertUpstreamOnce(t, upstreams, "zmid", 1, []string{"zmid", "report"})
	assertUpstreamOnce(t, upstreams, "source", 1, []string{"source", "report"})
	assertUpstreamOnce(t, upstreams, "extra", 2, []string{"extra", "zmid", "report"})
	for _, up := range upstreams {
		if up.Dataset == "mid" {
			t.Fatalf("old name mid leaked into post-rename upstream %+v", up)
		}
		for _, hop := range up.Path {
			if hop == "mid" {
				t.Fatalf("old name mid leaked into post-rename path %+v", up)
			}
		}
	}

	// The downstream query from source reaches the same datasets as before
	// the rename, with the renamed node at its old distance under its new
	// name.
	impacts := mustImpacts(t, graph, "source")
	assertImpactOnce(t, impacts, "zmid", 1, []string{"source", "zmid"})
	assertImpactOnce(t, impacts, "report", 1, []string{"source", "report"})
	assertImpactOnce(t, impacts, "step", 2, []string{"source", "zmid", "step"})
	assertImpactOnce(t, impacts, "sidedown", 2, []string{"source", "zmid", "sidedown"})

	// The old name is unregistered for the scoped export as well.
	if out, err := ExportSourceTargetLineage(graph, "mid", "report"); err == nil {
		t.Fatalf("scoped export from old name succeeded with %q, want a not-found error", out)
	} else if !strings.Contains(err.Error(), "mid") {
		t.Errorf("error %q must name the queried old name", err)
	}

	// None of the queries changed the graph.
	assertGraphUnchanged(t, before, graph)
}
