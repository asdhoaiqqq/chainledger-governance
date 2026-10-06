package chainledger

import (
	"reflect"
	"strings"
	"testing"
)

// scopedReregisterScenario builds the regression lineage: source derives merge
// directly, through the short branch short, and through the longer branch
// longa -> longb; merge additionally depends on the independent root indie.
// merge derives target, target derives view, and spare stands alone.
func scopedReregisterScenario(t *testing.T) map[string]*Lineage {
	t.Helper()
	return buildRegisteredGraph(t, [][]string{
		{"source"},
		{"short", "source"},
		{"longa", "source"},
		{"longb", "longa"},
		{"indie"},
		{"merge", "source", "short", "longb", "indie"},
		{"target", "merge"},
		{"view", "target"},
		{"spare"},
	})
}

// The exact JSON texts of the source -> target scoped export before the
// replacement and after each replacement variant. Both orderings are fully
// determined by the names.
const (
	scopedReregisterBefore = `{"nodes":["longa","longb","merge","short","source","target"],` +
		`"edges":[{"from":"longa","to":"longb"},{"from":"longb","to":"merge"},` +
		`{"from":"merge","to":"target"},{"from":"short","to":"merge"},` +
		`{"from":"source","to":"longa"},{"from":"source","to":"merge"},` +
		`{"from":"source","to":"short"}]}`
	scopedReregisterLongOnly = `{"nodes":["longa","longb","merge","source","target"],` +
		`"edges":[{"from":"longa","to":"longb"},{"from":"longb","to":"merge"},` +
		`{"from":"merge","to":"target"},{"from":"source","to":"longa"}]}`
	scopedReregisterBothBranches = `{"nodes":["longa","longb","merge","short","source","target"],` +
		`"edges":[{"from":"longa","to":"longb"},{"from":"longb","to":"merge"},` +
		`{"from":"merge","to":"target"},{"from":"short","to":"merge"},` +
		`{"from":"source","to":"longa"},{"from":"source","to":"short"}]}`
)

// Focus regression: replacing merge's direct-upstream list drops the direct
// source -> merge edge and the short branch while keeping the longer
// source -> longa -> longb -> merge route and the independent upstream indie.
// The scoped export must keep every node and direct dependency on the
// surviving long route — source and merge included — while short leaves the
// document even though it stays registered and is still a downstream of
// source. indie and the indie -> merge edge, target's downstream view, and
// the standalone spare never enter the document.
func TestExportSourceTargetLineageAfterReregisterKeepsLongRoute(t *testing.T) {
	graph := scopedReregisterScenario(t)
	assertConsistent(t, graph)

	// Before the replacement all three routes plus the merge node are present;
	// indie, view and spare stay out.
	if out := mustExportScoped(t, graph, "source", "target"); out != scopedReregisterBefore {
		t.Errorf("export before re-register =\n%s\nwant:\n%s", out, scopedReregisterBefore)
	}

	// Replace merge's whole direct-upstream list: drop source (direct) and
	// short, keep longb and indie.
	mustRegister(t, graph, "merge", "longb", "indie")
	assertConsistent(t, graph)
	assertEntry(t, graph, "merge", []string{"longb", "indie"}, []string{"target"})
	// short left merge's upstream list but keeps its own registration and its
	// surviving source -> short dependency; it is still a downstream of source.
	assertEntry(t, graph, "short", []string{"source"}, nil)
	assertEntry(t, graph, "source", nil, []string{"short", "longa"})
	assertEntry(t, graph, "longa", []string{"source"}, []string{"longb"})
	assertEntry(t, graph, "longb", []string{"longa"}, []string{"merge"})
	assertEntry(t, graph, "indie", nil, []string{"merge"})
	assertEntry(t, graph, "target", []string{"merge"}, []string{"view"})

	before := snapshotExportGraph(graph)
	out := mustExportScoped(t, graph, "source", "target")
	if out != scopedReregisterLongOnly {
		t.Errorf("export after re-register =\n%s\nwant:\n%s", out, scopedReregisterLongOnly)
	}
	doc := parseExport(t, out)

	// The surviving long route is complete: source, longa, longb, merge and
	// target each appear exactly once with every direct dependency along it.
	wantNodes := []string{"longa", "longb", "merge", "source", "target"}
	if !reflect.DeepEqual(doc.Nodes, wantNodes) {
		t.Errorf("nodes = %v, want %v", doc.Nodes, wantNodes)
	}
	wantEdges := [][2]string{
		{"longa", "longb"},
		{"longb", "merge"},
		{"merge", "target"},
		{"source", "longa"},
	}
	if got := exportEdgeNames(doc); !reflect.DeepEqual(got, wantEdges) {
		t.Errorf("edges = %v, want %v", got, wantEdges)
	}

	// short can no longer reach target, so it leaves the document; indie (and
	// indie -> merge), view and spare were never on a source -> target route.
	for _, excluded := range []string{"short", "indie", "view", "spare"} {
		for _, node := range doc.Nodes {
			if node == excluded {
				t.Errorf("nodes must not contain %q: %v", excluded, doc.Nodes)
			}
		}
	}
	for _, e := range doc.Edges {
		if e.From == "indie" {
			t.Errorf("independent upstream edge %v must stay out of %v", e, doc.Edges)
		}
	}

	// The export is read-only over the post-replacement state.
	assertGraphUnchanged(t, before, graph)
}

// Removing only the direct source -> merge dependency while keeping both the
// short and the long branch: the scoped export must present both routes
// completely — the longer route is not dropped because the shorter one
// exists — with the shared merge node and the merge -> target dependency
// appearing exactly once.
func TestExportSourceTargetLineageAfterReregisterKeepsBothBranches(t *testing.T) {
	graph := scopedReregisterScenario(t)
	assertConsistent(t, graph)

	mustRegister(t, graph, "merge", "short", "longb", "indie")
	assertConsistent(t, graph)
	assertEntry(t, graph, "merge", []string{"short", "longb", "indie"}, []string{"target"})
	assertEntry(t, graph, "source", nil, []string{"short", "longa"})

	before := snapshotExportGraph(graph)
	out := mustExportScoped(t, graph, "source", "target")
	if out != scopedReregisterBothBranches {
		t.Errorf("export after re-register =\n%s\nwant:\n%s", out, scopedReregisterBothBranches)
	}
	doc := parseExport(t, out)

	wantNodes := []string{"longa", "longb", "merge", "short", "source", "target"}
	if !reflect.DeepEqual(doc.Nodes, wantNodes) {
		t.Errorf("nodes = %v, want %v", doc.Nodes, wantNodes)
	}
	wantEdges := [][2]string{
		{"longa", "longb"},
		{"longb", "merge"},
		{"merge", "target"},
		{"short", "merge"},
		{"source", "longa"},
		{"source", "short"},
	}
	if got := exportEdgeNames(doc); !reflect.DeepEqual(got, wantEdges) {
		t.Errorf("edges = %v, want %v", got, wantEdges)
	}

	// The shared merge node and the merge -> target edge appear once each,
	// however many routes pass through them.
	mergeCount, mergeEdgeCount := 0, 0
	for _, node := range doc.Nodes {
		if node == "merge" {
			mergeCount++
		}
	}
	for _, e := range doc.Edges {
		if e.From == "merge" && e.To == "target" {
			mergeEdgeCount++
		}
	}
	if mergeCount != 1 || mergeEdgeCount != 1 {
		t.Errorf("shared merge appears %d times as node, %d as edge, want 1 each",
			mergeCount, mergeEdgeCount)
	}

	assertGraphUnchanged(t, before, graph)
}

// Replacing merge's upstream list with only the independent upstream indie
// leaves source and target registered but with no forward route between them:
// the scoped export succeeds with two empty (non-null) arrays — no isolated
// endpoint is retained, and no unregistered error is raised.
func TestExportSourceTargetLineageAfterReregisterNoRoute(t *testing.T) {
	graph := scopedReregisterScenario(t)
	assertConsistent(t, graph)

	mustRegister(t, graph, "merge", "indie")
	assertConsistent(t, graph)
	assertEntry(t, graph, "merge", []string{"indie"}, []string{"target"})
	// The replaced-away nodes keep their registrations and their surviving
	// dependencies: source still derives short and longa, longa still derives
	// longb; only the edges into merge are gone.
	assertEntry(t, graph, "source", nil, []string{"short", "longa"})
	assertEntry(t, graph, "longb", []string{"longa"}, nil)
	assertEntry(t, graph, "indie", nil, []string{"merge"})

	before := snapshotExportGraph(graph)
	out := mustExportScoped(t, graph, "source", "target")
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

	// Both endpoints are still registered: the empty document is a route
	// result, not a not-found error, and the graph is untouched by the query.
	assertEntry(t, graph, "source", nil, []string{"short", "longa"})
	assertEntry(t, graph, "target", []string{"merge"}, []string{"view"})
	assertGraphUnchanged(t, before, graph)
}

// A rejected replacement must not partially apply: re-registering merge with
// the registered spare followed by the unregistered ghost fails naming ghost,
// and the source -> target export afterwards is byte-identical to the
// pre-request text — spare never enters the document and no partial new
// relation leaks into the graph.
func TestExportSourceTargetLineageFailedReregisterLeavesExportUntouched(t *testing.T) {
	graph := scopedReregisterScenario(t)
	assertConsistent(t, graph)

	exportBefore := mustExportScoped(t, graph, "source", "target")
	if exportBefore != scopedReregisterBefore {
		t.Fatalf("export before failed re-register =\n%s\nwant:\n%s", exportBefore, scopedReregisterBefore)
	}
	before := snapshotExportGraph(graph)

	err := Register(graph, Dataset{Name: "merge"}, []string{"spare", "ghost"})
	if err == nil || !strings.Contains(err.Error(), "ghost") {
		t.Fatalf("want unknown-parent error naming ghost, got %v", err)
	}

	// No partial effect: merge never gained spare nor lost any upstream, and
	// the whole graph, list orders included, is exactly as before the request.
	assertGraphUnchanged(t, before, graph)
	assertEntry(t, graph, "merge", []string{"source", "short", "longb", "indie"}, []string{"target"})
	assertEntry(t, graph, "spare", nil, nil)
	assertConsistent(t, graph)

	// The export after the rejected request matches the pre-request text
	// exactly, with no trace of spare or a half-applied replacement.
	exportAfter := mustExportScoped(t, graph, "source", "target")
	if exportAfter != exportBefore {
		t.Errorf("export changed after rejected re-register:\nbefore: %s\nafter:  %s",
			exportBefore, exportAfter)
	}
	if strings.Contains(exportAfter, "spare") {
		t.Errorf("export must not contain spare after the rejected request: %s", exportAfter)
	}
	assertGraphUnchanged(t, before, graph)
}
