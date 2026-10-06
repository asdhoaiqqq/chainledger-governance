package chainledger

import (
	"reflect"
	"strings"
	"testing"
)

// scopedRenameReregisterFixture builds the rename-then-reregister regression
// lineage:
//
//	extra ──> detail            (detail also depends on an unrelated root)
//	source ──> detail ──> report (the longer route)
//	source ──> report           (the direct relation)
//	report ──> view             (target downstream)
//
// source therefore participates in report's derivation twice: by the direct
// source -> report edge and through detail (source -> detail -> report). detail
// additionally depends on extra, which has no relation to source and must never
// enter a source-scoped document; view is downstream of the target and stays
// out as well.
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

// The scoped document under the original name: the direct edge and the longer
// route through detail are both present; extra, extra -> detail and view stay
// out.
const scopedRenameReregisterBefore = `{"nodes":["detail","report","source"],` +
	`"edges":[{"from":"detail","to":"report"},{"from":"source","to":"detail"},` +
	`{"from":"source","to":"report"}]}`

// After source is renamed to renamed, the same original source is expressed
// entirely under its new name: the direct renamed -> report relation and the
// longer renamed -> detail -> report route both survive, and the old name
// appears nowhere. The text is the same again after the old name is
// re-registered as a different dataset, because that new dataset takes no part
// in this derivation.
const scopedRenameReregisterRenamed = `{"nodes":["detail","renamed","report"],` +
	`"edges":[{"from":"detail","to":"report"},{"from":"renamed","to":"detail"},` +
	`{"from":"renamed","to":"report"}]}`

// The document scoped to the re-registered name source: the new dataset and
// report, joined only by the new direct edge. It inherits none of the original
// source's routes through detail.
const scopedRenameReregisterNewSource = `{"nodes":["report","source"],` +
	`"edges":[{"from":"source","to":"report"}]}`

// assertScopedRenamedDocument pins the export of the ORIGINAL source under its
// new name: exactly renamed, detail and report, with the direct edge and the
// complete longer route — never collapsed to the single shortest edge — and no
// trace of the old name, the unrelated extra root or the target's downstream.
func assertScopedRenamedDocument(t *testing.T, out string) {
	t.Helper()
	if out != scopedRenameReregisterRenamed {
		t.Errorf("renamed -> report export =\n%s\nwant:\n%s", out, scopedRenameReregisterRenamed)
	}
	doc := parseExport(t, out)
	if !reflect.DeepEqual(doc.Nodes, []string{"detail", "renamed", "report"}) {
		t.Errorf("nodes = %v, want [detail renamed report]", doc.Nodes)
	}
	wantEdges := [][2]string{
		{"detail", "report"},
		{"renamed", "detail"},
		{"renamed", "report"},
	}
	if got := exportEdgeNames(doc); !reflect.DeepEqual(got, wantEdges) {
		t.Errorf("edges = %v, want %v", got, wantEdges)
	}

	// Both routes must be present at once: the direct relation must not make
	// the export drop the longer route through detail, and vice versa.
	got := exportEdgeNames(doc)
	edgeSet := map[[2]string]bool{}
	for _, e := range got {
		edgeSet[e] = true
	}
	for _, want := range [][2]string{
		{"renamed", "report"}, // direct: one edge
		{"renamed", "detail"}, // longer route, hop 1
		{"detail", "report"},  // longer route, hop 2
	} {
		if !edgeSet[want] {
			t.Errorf("route edge %v missing from %v: a branch was dropped", want, got)
		}
	}

	// The original source is expressed only under its current name. The old
	// name source, the unrelated root extra and the target downstream view
	// appear neither as nodes nor as edge endpoints.
	assertOldNameAbsent(t, doc, "source")
	for _, excluded := range []string{"extra", "view"} {
		for _, node := range doc.Nodes {
			if node == excluded {
				t.Errorf("nodes must not contain %q: %v", excluded, doc.Nodes)
			}
		}
		for _, e := range doc.Edges {
			if e.From == excluded || e.To == excluded {
				t.Errorf("unrelated dependency on %q leaked into the scoped export: %+v", excluded, e)
			}
		}
	}
}

// Regression for the lifecycle where a source is renamed and its old name is
// then registered again as a different dataset. The rename preserves the
// original source's exact lineage position under the new name, while the
// re-registered old name is a brand-new dataset with no ancestry of its own:
// the two registrations must never be merged just because they share a name at
// different times. The source-scoped export has to tell them apart by the
// current name and the actual relationships — keeping the original source's
// direct and detail-routed branches for the renamed export, and only the new
// direct edge for the re-registered name's export.
func TestExportSourceTargetLineageRenameThenReregisterOldName(t *testing.T) {
	graph := scopedRenameReregisterFixture(t)
	assertConsistent(t, graph)
	assertEntry(t, graph, "source", nil, []string{"detail", "report"})
	assertEntry(t, graph, "extra", nil, []string{"detail"})
	assertEntry(t, graph, "detail", []string{"source", "extra"}, []string{"report"})
	assertEntry(t, graph, "report", []string{"source", "detail"}, []string{"view"})
	assertEntry(t, graph, "view", []string{"report"}, nil)

	// Baseline under the original name.
	if out := mustExportScoped(t, graph, "source", "report"); out != scopedRenameReregisterBefore {
		t.Fatalf("source -> report export before rename =\n%s\nwant:\n%s",
			out, scopedRenameReregisterBefore)
	}

	// Rename source to renamed in place: the node keeps both lists and its
	// position, and every neighbor reference is rewritten in its slot.
	mustRename(t, graph, "source", "renamed")
	assertConsistent(t, graph)
	if _, ok := graph["source"]; ok {
		t.Fatal("old name source still present in graph after rename")
	}
	if got, want := len(graph), 5; got != want {
		t.Fatalf("dataset count = %d, want %d (rename must not merge or add nodes)", got, want)
	}
	assertEntry(t, graph, "renamed", nil, []string{"detail", "report"})
	assertEntry(t, graph, "detail", []string{"renamed", "extra"}, []string{"report"})
	assertEntry(t, graph, "extra", nil, []string{"detail"})
	assertEntry(t, graph, "report", []string{"renamed", "detail"}, []string{"view"})
	assertEntry(t, graph, "view", []string{"report"}, nil)

	// The export under the new name keeps the original source's position: the
	// direct edge and the longer route through detail, under the new name only.
	before := snapshotExportGraph(graph)
	assertScopedRenamedDocument(t, mustExportScoped(t, graph, "renamed", "report"))

	// The old name is unregistered now: it must fail with a not-found error
	// naming it, return the empty string rather than the previous document or
	// any partial content, and change nothing.
	out, err := ExportSourceTargetLineage(graph, "source", "report")
	if err == nil {
		t.Fatalf("export from the freed old name succeeded with %q, want a not-found error", out)
	}
	if !strings.Contains(err.Error(), "dataset not found: source") {
		t.Errorf("error = %q, want it to name the unregistered source", err)
	}
	if out != "" {
		t.Errorf("failed export returned %q, want empty string (never the old document)", out)
	}
	assertGraphUnchanged(t, before, graph)

	// Register the freed name as a brand-new dataset with no upstreams — a
	// second, independent dataset — and let report depend on it directly while
	// keeping every existing dependency. report's retained parents keep their
	// positions and the new parent is appended; source gains report as its only
	// child. Nothing links the new source to detail.
	mustRegister(t, graph, "source")
	mustRegister(t, graph, "report", "renamed", "detail", "source")
	assertConsistent(t, graph)
	if got, want := len(graph), 6; got != want {
		t.Fatalf("dataset count = %d, want %d (reused name is a separate dataset)", got, want)
	}
	assertEntry(t, graph, "source", nil, []string{"report"})
	assertEntry(t, graph, "renamed", nil, []string{"detail", "report"})
	assertEntry(t, graph, "detail", []string{"renamed", "extra"}, []string{"report"})
	assertEntry(t, graph, "extra", nil, []string{"detail"})
	assertEntry(t, graph, "report", []string{"renamed", "detail", "source"}, []string{"view"})
	assertEntry(t, graph, "view", []string{"report"}, nil)

	// renamed -> report is unchanged by the old name's return: renamed, detail
	// and report with the same three direct dependencies of the original
	// source. The new source, its new edge, extra and view all stay out.
	before = snapshotExportGraph(graph)
	assertScopedRenamedDocument(t, mustExportScoped(t, graph, "renamed", "report"))

	// source -> report sees only the new dataset: source and report joined by
	// the new direct edge. It inherits none of the original source's route
	// through detail, and the two same-name-at-different-times sources are not
	// merged into one document.
	out = mustExportScoped(t, graph, "source", "report")
	if out != scopedRenameReregisterNewSource {
		t.Errorf("source -> report export =\n%s\nwant:\n%s", out, scopedRenameReregisterNewSource)
	}
	newDoc := parseExport(t, out)
	if !reflect.DeepEqual(newDoc.Nodes, []string{"report", "source"}) {
		t.Errorf("new-source nodes = %v, want [report source]", newDoc.Nodes)
	}
	if got := exportEdgeNames(newDoc); !reflect.DeepEqual(got, [][2]string{{"source", "report"}}) {
		t.Errorf("new-source edges = %v, want only [source report]", got)
	}
	for _, excluded := range []string{"renamed", "detail", "extra", "view"} {
		for _, node := range newDoc.Nodes {
			if node == excluded {
				t.Errorf("new-source export must not contain %q: %v", excluded, newDoc.Nodes)
			}
		}
		for _, e := range newDoc.Edges {
			if e.From == excluded || e.To == excluded {
				t.Errorf("edge touching %q leaked into the new-source export: %+v", excluded, e)
			}
		}
	}

	// The two exports genuinely describe different derivations, not one merged
	// graph under two names.
	renamedDoc := parseExport(t, mustExportScoped(t, graph, "renamed", "report"))
	if reflect.DeepEqual(renamedDoc.Nodes, newDoc.Nodes) {
		t.Errorf("the two sources were merged: both exports have nodes %v", renamedDoc.Nodes)
	}

	// The full upstream export and the other lineage queries keep their public
	// behavior on the same graph: report now has both sources in its ancestry
	// (the unrelated extra included there), and the impact scopes of the two
	// same-name-history datasets stay disjoint.
	full := parseExport(t, mustExport(t, graph, "report"))
	if !reflect.DeepEqual(full.Nodes, []string{"detail", "extra", "renamed", "report", "source"}) {
		t.Errorf("full export nodes = %v, want [detail extra renamed report source]", full.Nodes)
	}
	if got := exportEdgeNames(full); !reflect.DeepEqual(got, [][2]string{
		{"detail", "report"},
		{"extra", "detail"},
		{"renamed", "detail"},
		{"renamed", "report"},
		{"source", "report"},
	}) {
		t.Errorf("full export edges = %v", got)
	}

	fromRenamed := mustImpacts(t, graph, "renamed")
	assertImpactOnce(t, fromRenamed, "detail", 1, []string{"renamed", "detail"})
	assertImpactOnce(t, fromRenamed, "report", 1, []string{"renamed", "report"})
	assertImpactOnce(t, fromRenamed, "view", 2, []string{"renamed", "report", "view"})
	for _, im := range fromRenamed {
		if im.Dataset == "source" || im.Dataset == "extra" {
			t.Errorf("impact scope of renamed must not contain the new source or extra, got %q", im.Dataset)
		}
	}
	fromNew := mustImpacts(t, graph, "source")
	assertImpactOnce(t, fromNew, "report", 1, []string{"source", "report"})
	assertImpactOnce(t, fromNew, "view", 2, []string{"source", "report", "view"})
	for _, im := range fromNew {
		if im.Dataset == "renamed" || im.Dataset == "detail" || im.Dataset == "extra" {
			t.Errorf("impact scope of the new source inherited old lineage dataset %q", im.Dataset)
		}
	}

	// Every export above is read-only: names, both edge directions and stored
	// list orders are exactly as the registrations left them.
	assertGraphUnchanged(t, before, graph)
	assertConsistent(t, graph)
}

// While the old name is unregistered, neither a failure nor a repeated
// successful scoped export may touch the graph, and the failed query must
// return empty text — never the document that used to be produced under that
// name.
func TestExportSourceTargetLineageRenameGapFailureIsEmptyAndReadOnly(t *testing.T) {
	graph := scopedRenameReregisterFixture(t)
	previous := mustExportScoped(t, graph, "source", "report")
	if previous != scopedRenameReregisterBefore {
		t.Fatalf("baseline export =\n%s\nwant:\n%s", previous, scopedRenameReregisterBefore)
	}

	mustRename(t, graph, "source", "renamed")
	assertConsistent(t, graph)
	before := snapshotExportGraph(graph)

	// The query that would have succeeded before the rename now names the
	// missing dataset and yields no content at all.
	if out, err := ExportSourceTargetLineage(graph, "source", "report"); err == nil {
		t.Fatalf("freed-name export succeeded with %q", out)
	} else if out != "" {
		t.Errorf("freed-name export returned %q, want empty string", out)
	} else if !strings.Contains(err.Error(), "source") {
		t.Errorf("error %q must name source", err)
	}

	// The new-name export still works and leaves the graph untouched.
	if out := mustExportScoped(t, graph, "renamed", "report"); out != scopedRenameReregisterRenamed {
		t.Errorf("renamed export = %s, want %s", out, scopedRenameReregisterRenamed)
	}

	// Repeating the failed query still returns nothing, and the old document is
	// not resurrected.
	if out, err := ExportSourceTargetLineage(graph, "source", "report"); err == nil || out != "" {
		t.Fatalf("repeated freed-name export = %q, %v; want empty string and an error", out, err)
	}
	assertGraphUnchanged(t, before, graph)
}
