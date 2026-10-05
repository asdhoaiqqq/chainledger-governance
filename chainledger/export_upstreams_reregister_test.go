package chainledger

import (
	"reflect"
	"strings"
	"testing"
)

// The lineage shared by the re-registration export scenarios: raw derives a
// and b, b derives c; mid depends directly on raw, a, c and old; old depends
// on the independent source oldroot; report depends on mid and derives view;
// isolated stands alone.
func buildReregisterExportGraph(t *testing.T) map[string]*Lineage {
	t.Helper()
	return buildRegisteredGraph(t, [][]string{
		{"raw"},
		{"a", "raw"},
		{"b", "raw"},
		{"c", "b"},
		{"oldroot"},
		{"old", "oldroot"},
		{"mid", "raw", "a", "c", "old"},
		{"report", "mid"},
		{"view", "report"},
		{"isolated"},
	})
}

// Before any replacement, the export of report spans both the raw and the
// oldroot branches: every node from raw, a, b, c, mid, old, oldroot and
// report itself, with each direct dependency among them — including the
// direct raw -> mid edge next to the longer raw -> a -> mid and
// raw -> b -> c -> mid routes. view and isolated stay out.
func TestExportUpstreamLineageBeforeReregister(t *testing.T) {
	graph := buildReregisterExportGraph(t)
	before := snapshotExportGraph(graph)

	out := mustExport(t, graph, "report")
	want := `{"nodes":["a","b","c","mid","old","oldroot","raw","report"],` +
		`"edges":[{"from":"a","to":"mid"},{"from":"b","to":"c"},` +
		`{"from":"c","to":"mid"},{"from":"mid","to":"report"},` +
		`{"from":"old","to":"mid"},{"from":"oldroot","to":"old"},` +
		`{"from":"raw","to":"a"},{"from":"raw","to":"b"},` +
		`{"from":"raw","to":"mid"}]}`
	if out != want {
		t.Errorf("export before replacement =\n%s\nwant:\n%s", out, want)
	}
	assertGraphUnchanged(t, before, graph)
}

// Regression: re-registering mid replaces its whole direct upstream list with
// a and c. The new export of report must be computed from the current
// dependencies: the direct raw -> mid edge disappears, but raw stays in the
// document through BOTH surviving branches — the short raw -> a -> mid route
// and the longer raw -> b -> c -> mid route, which must be kept in full, not
// collapsed into the a branch. old and oldroot no longer take part in
// report's derivation, so they and their edges leave the export, while their
// registrations and the oldroot -> old dependency stay in the graph. report's
// own registration is untouched, and view and isolated never appear.
func TestExportUpstreamLineageAfterUpstreamReplacement(t *testing.T) {
	graph := buildReregisterExportGraph(t)

	// Replace mid's direct upstreams; report's own registration is not part
	// of the request.
	mustRegister(t, graph, "mid", "a", "c")
	assertConsistent(t, graph)

	// The replacement rewired exactly the reverse edges of the dropped and
	// kept upstreams; old and oldroot keep their registration and their
	// mutual dependency.
	assertEntry(t, graph, "mid", []string{"a", "c"}, []string{"report"})
	assertEntry(t, graph, "report", []string{"mid"}, []string{"view"})
	assertEntry(t, graph, "raw", nil, []string{"a", "b"})
	assertEntry(t, graph, "a", []string{"raw"}, []string{"mid"})
	assertEntry(t, graph, "b", []string{"raw"}, []string{"c"})
	assertEntry(t, graph, "c", []string{"b"}, []string{"mid"})
	assertEntry(t, graph, "old", []string{"oldroot"}, nil)
	assertEntry(t, graph, "oldroot", nil, []string{"old"})
	assertEntry(t, graph, "view", []string{"report"}, nil)
	assertEntry(t, graph, "isolated", nil, nil)

	before := snapshotExportGraph(graph)
	out := mustExport(t, graph, "report")
	doc := parseExport(t, out)

	wantNodes := []string{"a", "b", "c", "mid", "raw", "report"}
	if !reflect.DeepEqual(doc.Nodes, wantNodes) {
		t.Errorf("nodes = %v, want %v", doc.Nodes, wantNodes)
	}
	wantEdges := [][2]string{
		{"a", "mid"},
		{"b", "c"},
		{"c", "mid"},
		{"mid", "report"},
		{"raw", "a"},
		{"raw", "b"},
	}
	if got := exportEdgeNames(doc); !reflect.DeepEqual(got, wantEdges) {
		t.Errorf("edges = %v, want %v", got, wantEdges)
	}

	// Anchors with explicit messages: the dropped direct edge and the
	// detached branch must be gone, while raw survives through both remaining
	// branches — the longer one through b and c included.
	for _, gone := range [][2]string{{"raw", "mid"}, {"old", "mid"}, {"oldroot", "old"}} {
		for _, e := range exportEdgeNames(doc) {
			if e == gone {
				t.Errorf("edge %v must have left the export: %v", gone, exportEdgeNames(doc))
			}
		}
	}
	for _, excluded := range []string{"old", "oldroot", "view", "isolated"} {
		for _, node := range doc.Nodes {
			if node == excluded {
				t.Errorf("nodes must not contain %q: %v", excluded, doc.Nodes)
			}
		}
	}

	// The exact JSON text is pinned: nodes by name, edges by from then to.
	want := `{"nodes":["a","b","c","mid","raw","report"],` +
		`"edges":[{"from":"a","to":"mid"},{"from":"b","to":"c"},` +
		`{"from":"c","to":"mid"},{"from":"mid","to":"report"},` +
		`{"from":"raw","to":"a"},{"from":"raw","to":"b"}]}`
	if out != want {
		t.Errorf("export after replacement =\n%s\nwant:\n%s", out, want)
	}

	assertGraphUnchanged(t, before, graph)
}

// The export's ordering is fully determined by the names: replacing mid's
// upstreams with [a c] or [c a] — and building the same lineage in a
// different registration order — yields byte-identical documents.
func TestExportUpstreamLineageReplacementOrderIndependent(t *testing.T) {
	first := buildReregisterExportGraph(t)
	mustRegister(t, first, "mid", "a", "c")

	second := buildRegisteredGraph(t, [][]string{
		{"isolated"},
		{"oldroot"},
		{"old", "oldroot"},
		{"raw"},
		{"b", "raw"},
		{"c", "b"},
		{"a", "raw"},
		{"mid", "old", "c", "a", "raw"}, // upstream list order flipped
		{"report", "mid"},
		{"view", "report"},
	})
	mustRegister(t, second, "mid", "c", "a") // replacement order flipped too

	if got, want := mustExport(t, first, "report"), mustExport(t, second, "report"); got != want {
		t.Errorf("replacement list order changed the export:\nfirst:  %s\nsecond: %s", got, want)
	}
}

// A rejected replacement is atomic: re-registering mid with the registered
// isolated followed by the unregistered ghost fails naming ghost, the valid
// first entry does not partially apply, and mid's and every related node's
// stored relationships and list orders stay exactly as they were. A later
// export of report is byte-identical to the pre-request text and contains
// neither isolated nor any fragment of the refused request.
func TestExportUpstreamLineageRejectedReplacementKeepsExport(t *testing.T) {
	graph := buildReregisterExportGraph(t)
	mustRegister(t, graph, "mid", "a", "c")

	exportBefore := mustExport(t, graph, "report")
	graphBefore := snapshotExportGraph(graph)

	err := Register(graph, Dataset{Name: "mid"}, []string{"isolated", "ghost"})
	if err == nil {
		t.Fatal("expected unknown-parent error for ghost, got nil")
	}
	if !strings.Contains(err.Error(), "ghost") {
		t.Errorf("error %q must name the unregistered upstream ghost", err)
	}

	// Nothing from the failed request applied: mid kept its replaced list,
	// isolated gained no child, and every other node is untouched.
	assertGraphUnchanged(t, graphBefore, graph)
	assertEntry(t, graph, "mid", []string{"a", "c"}, []string{"report"})
	assertEntry(t, graph, "isolated", nil, nil)
	assertConsistent(t, graph)

	exportAfter := mustExport(t, graph, "report")
	if exportAfter != exportBefore {
		t.Errorf("export changed after rejected replacement:\nbefore: %s\nafter:  %s", exportBefore, exportAfter)
	}
	if strings.Contains(exportAfter, "isolated") || strings.Contains(exportAfter, "ghost") {
		t.Errorf("export after rejected replacement must not mention isolated or ghost: %s", exportAfter)
	}
}
