package chainledger

import (
	"reflect"
	"strings"
	"testing"
)

// scopedRenameReregisterFixture builds the rename-then-reregister regression
// lineage:
//
//	extra ──> detail                 (detail depends on an independent source)
//	source ──> detail ──> report     (the route through detail)
//	source ──> report                (the direct relation)
//	report ──> view                  (target downstream)
//
// source participates in report's derivation both directly and through
// detail, so a scoped source -> report export must keep both routes; extra,
// extra -> detail and view never take part and stay out.
func scopedRenameReregisterFixture(t *testing.T) map[string]*Lineage {
	t.Helper()
	return buildRegisteredGraph(t, [][]string{
		{"source"},
		{"extra"},
		{"detail", "source", "extra"},
		{"report", "source", "detail"},
		{"view", "report"},
	})
}

// The exact scoped JSON texts. All orderings are fully determined by the
// names: nodes in Go string order, edges upstream -> derived ordered by from
// then to.
const (
	// Before the rename: source -> report scoped export holds the direct edge
	// and the route through detail, nothing else.
	scopedRenameReregisterBefore = `{"nodes":["detail","report","source"],` +
		`"edges":[{"from":"detail","to":"report"},{"from":"source","to":"detail"},` +
		`{"from":"source","to":"report"}]}`
	// After the rename, the renamed -> report export is the pre-rename
	// document with the new name substituted: same three nodes, same three
	// direct dependencies. The re-registration of the old name must not
	// change this text either.
	scopedRenameReregisterRenamed = `{"nodes":["detail","renamed","report"],` +
		`"edges":[{"from":"detail","to":"report"},{"from":"renamed","to":"detail"},` +
		`{"from":"renamed","to":"report"}]}`
	// After the old name is registered again as a new source that report
	// depends on directly: the source -> report export holds exactly the two
	// endpoints and their new direct edge.
	scopedRenameReregisterNewSource = `{"nodes":["report","source"],` +
		`"edges":[{"from":"source","to":"report"}]}`
	// The full upstream export of report after the re-registration sees both
	// registrations at once — the ancestry the two scoped exports each take
	// their own slice of.
	scopedRenameReregisterFullReport = `{"nodes":["detail","extra","renamed","report","source"],` +
		`"edges":[{"from":"detail","to":"report"},{"from":"extra","to":"detail"},` +
		`{"from":"renamed","to":"detail"},{"from":"renamed","to":"report"},` +
		`{"from":"source","to":"report"}]}`
)

// Regression for the source-scoped export after the source itself is renamed.
// The rename keeps the dataset's exact lineage position, so the renamed ->
// report export preserves both the direct relation and the longer route
// through detail under the new name — the longer route is not collapsed into
// the direct edge — while the old name, the independent upstream extra and
// the downstream view stay out. Asking for the old name afterwards is a
// not-found error with an empty result, never the previous document.
func TestExportSourceTargetLineageAfterSourceRename(t *testing.T) {
	graph := scopedRenameReregisterFixture(t)
	assertConsistent(t, graph)

	// Baseline: the direct edge and the detail route are both present.
	if out := mustExportScoped(t, graph, "source", "report"); out != scopedRenameReregisterBefore {
		t.Fatalf("scoped export before rename =\n%s\nwant:\n%s", out, scopedRenameReregisterBefore)
	}

	// Rename the source itself: the node keeps both lists, and every neighbor
	// reference is rewritten in its original list position.
	mustRename(t, graph, "source", "renamed")
	assertConsistent(t, graph)
	if _, ok := graph["source"]; ok {
		t.Fatal("old name source still present in graph")
	}
	if got, want := len(graph), 5; got != want {
		t.Fatalf("dataset count = %d, want %d (rename must not merge nodes)", got, want)
	}
	assertEntry(t, graph, "renamed", nil, []string{"detail", "report"})
	assertEntry(t, graph, "detail", []string{"renamed", "extra"}, []string{"report"})
	assertEntry(t, graph, "report", []string{"renamed", "detail"}, []string{"view"})
	assertEntry(t, graph, "extra", nil, []string{"detail"})
	assertEntry(t, graph, "view", []string{"report"}, nil)

	before := snapshotExportGraph(graph)
	out := mustExportScoped(t, graph, "renamed", "report")
	if out != scopedRenameReregisterRenamed {
		t.Errorf("scoped export after rename =\n%s\nwant:\n%s", out, scopedRenameReregisterRenamed)
	}
	doc := parseExport(t, out)

	// The original source's three datasets, each exactly once, under the
	// current name.
	wantNodes := []string{"detail", "renamed", "report"}
	if !reflect.DeepEqual(doc.Nodes, wantNodes) {
		t.Errorf("nodes = %v, want %v", doc.Nodes, wantNodes)
	}

	// Both routes survive complete: the direct renamed -> report edge and the
	// longer renamed -> detail -> report route. The direct relation must not
	// swallow the longer route into a single shortest path.
	wantEdges := [][2]string{
		{"detail", "report"},
		{"renamed", "detail"},
		{"renamed", "report"},
	}
	if got := exportEdgeNames(doc); !reflect.DeepEqual(got, wantEdges) {
		t.Errorf("edges = %v, want %v", got, wantEdges)
	}

	// The old name occurs nowhere as a node or edge endpoint, and neither
	// extra (detail's independent upstream) nor view (report's downstream)
	// enters the document.
	assertOldNameAbsent(t, doc, "source")
	for _, excluded := range []string{"extra", "view"} {
		for _, node := range doc.Nodes {
			if node == excluded {
				t.Errorf("nodes must not contain %q: %v", excluded, doc.Nodes)
			}
		}
	}
	for _, e := range doc.Edges {
		if e.From == "extra" || e.To == "extra" {
			t.Errorf("independent upstream edge leaked into scoped export: %+v", e)
		}
	}

	// The export is read-only over the post-rename state.
	assertGraphUnchanged(t, before, graph)

	// The old name is no longer registered: the scoped export from source
	// fails with an error naming it and returns an empty string — not the
	// pre-rename document and not any part of it.
	before = snapshotExportGraph(graph)
	out, err := ExportSourceTargetLineage(graph, "source", "report")
	if err == nil {
		t.Fatalf("scoped export from renamed-away source succeeded with %q, want a not-found error", out)
	}
	if !strings.Contains(err.Error(), "source") {
		t.Errorf("error %q must name the queried old name", err)
	}
	if out != "" {
		t.Errorf("failed export returned %q, want an empty string", out)
	}
	assertGraphUnchanged(t, before, graph)
}

// Regression for re-registering the freed old name as a new, independent
// dataset after the rename. The new source has no upstreams and report gains
// a direct dependency on it while keeping its original dependencies. The two
// registrations that have carried the name source at different times must
// stay strictly separate in the scoped exports: renamed -> report keeps
// exactly the original source's nodes and three direct dependencies, source
// -> report holds only the new direct edge, and neither document borrows a
// route from the other.
func TestExportSourceTargetLineageRenameReregisterKeepsSourcesSeparate(t *testing.T) {
	graph := scopedRenameReregisterFixture(t)
	mustRename(t, graph, "source", "renamed")
	assertConsistent(t, graph)

	// Register the freed name as a brand-new root with no upstreams, then let
	// report depend on it directly in addition to its existing upstreams.
	mustRegister(t, graph, "source")
	mustRegister(t, graph, "report", "renamed", "detail", "source")
	assertConsistent(t, graph)

	// Two independent registrations now share the graph: the renamed original
	// keeps its exact position, the new source holds only the fresh direct
	// edge into report, and report's original dependencies are retained ahead
	// of the new one.
	if got, want := len(graph), 6; got != want {
		t.Fatalf("dataset count = %d, want %d", got, want)
	}
	assertEntry(t, graph, "renamed", nil, []string{"detail", "report"})
	assertEntry(t, graph, "source", nil, []string{"report"})
	assertEntry(t, graph, "detail", []string{"renamed", "extra"}, []string{"report"})
	assertEntry(t, graph, "report", []string{"renamed", "detail", "source"}, []string{"view"})
	assertEntry(t, graph, "extra", nil, []string{"detail"})
	assertEntry(t, graph, "view", []string{"report"}, nil)

	// Exporting from the renamed original: the same document as right after
	// the rename. The re-registered name must not pull the new source, its
	// new edge, or any merge of the two into this document.
	before := snapshotExportGraph(graph)
	out := mustExportScoped(t, graph, "renamed", "report")
	if out != scopedRenameReregisterRenamed {
		t.Errorf("renamed export after re-register =\n%s\nwant:\n%s", out, scopedRenameReregisterRenamed)
	}
	doc := parseExport(t, out)
	wantNodes := []string{"detail", "renamed", "report"}
	if !reflect.DeepEqual(doc.Nodes, wantNodes) {
		t.Errorf("renamed export nodes = %v, want %v", doc.Nodes, wantNodes)
	}
	wantEdges := [][2]string{
		{"detail", "report"},
		{"renamed", "detail"},
		{"renamed", "report"},
	}
	if got := exportEdgeNames(doc); !reflect.DeepEqual(got, wantEdges) {
		t.Errorf("renamed export edges = %v, want %v", got, wantEdges)
	}
	// The new source and its direct edge into report belong to the other
	// registration and stay out, as do extra and view.
	assertOldNameAbsent(t, doc, "source")
	for _, excluded := range []string{"extra", "view"} {
		for _, node := range doc.Nodes {
			if node == excluded {
				t.Errorf("renamed export must not contain %q: %v", excluded, doc.Nodes)
			}
		}
	}
	assertGraphUnchanged(t, before, graph)

	// Exporting from the re-registered name: only the new source, report and
	// their new direct edge. The new source must not inherit the original
	// source's route through detail, and the renamed original must not leak
	// in under either name.
	before = snapshotExportGraph(graph)
	out = mustExportScoped(t, graph, "source", "report")
	if out != scopedRenameReregisterNewSource {
		t.Errorf("new source export =\n%s\nwant:\n%s", out, scopedRenameReregisterNewSource)
	}
	doc = parseExport(t, out)
	if !reflect.DeepEqual(doc.Nodes, []string{"report", "source"}) {
		t.Errorf("new source export nodes = %v, want [report source]", doc.Nodes)
	}
	if got := exportEdgeNames(doc); !reflect.DeepEqual(got, [][2]string{{"source", "report"}}) {
		t.Errorf("new source export edges = %v, want [[source report]]", got)
	}
	for _, excluded := range []string{"renamed", "detail", "extra", "view"} {
		for _, node := range doc.Nodes {
			if node == excluded {
				t.Errorf("new source export must not contain %q: %v", excluded, doc.Nodes)
			}
		}
		for _, e := range doc.Edges {
			if e.From == excluded || e.To == excluded {
				t.Errorf("new source export must not carry an edge touching %q: %+v", excluded, e)
			}
		}
	}
	assertGraphUnchanged(t, before, graph)

	// Every node and every edge appears exactly once in each document,
	// however the two registrations' routes meet at report.
	for _, text := range []string{scopedRenameReregisterRenamed, scopedRenameReregisterNewSource} {
		d := parseExport(t, text)
		seenNodes := map[string]int{}
		for _, node := range d.Nodes {
			seenNodes[node]++
		}
		for node, n := range seenNodes {
			if n != 1 {
				t.Errorf("node %q appears %d times in %s", node, n, text)
			}
		}
		seenEdges := map[[2]string]int{}
		for _, e := range exportEdgeNames(d) {
			seenEdges[e]++
		}
		for edge, n := range seenEdges {
			if n != 1 {
				t.Errorf("edge %v appears %d times in %s", edge, n, text)
			}
		}
	}

	// The full upstream export of report keeps seeing both registrations and
	// every direct dependency at once — the existing export entry point is
	// unaffected by the split.
	before = snapshotExportGraph(graph)
	if out := mustExport(t, graph, "report"); out != scopedRenameReregisterFullReport {
		t.Errorf("full export of report =\n%s\nwant:\n%s", out, scopedRenameReregisterFullReport)
	}
	assertGraphUnchanged(t, before, graph)
}
