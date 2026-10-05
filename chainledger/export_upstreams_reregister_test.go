package chainledger

import (
	"reflect"
	"strings"
	"testing"
)

// exportReregisterScenario builds the regression lineage: raw derives a and b,
// b derives c; mid depends directly on raw, a, c and old; old depends on the
// independent source oldroot. report depends on mid and derives view, while
// isolated stands alone.
func exportReregisterScenario(t *testing.T) map[string]*Lineage {
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

// The exact JSON texts before and after mid's direct upstreams are replaced by
// a and c. Both orderings are fully determined by the names.
const (
	exportReregisterBefore = `{"nodes":["a","b","c","mid","old","oldroot","raw","report"],` +
		`"edges":[{"from":"a","to":"mid"},{"from":"b","to":"c"},` +
		`{"from":"c","to":"mid"},{"from":"mid","to":"report"},` +
		`{"from":"old","to":"mid"},{"from":"oldroot","to":"old"},` +
		`{"from":"raw","to":"a"},{"from":"raw","to":"b"},` +
		`{"from":"raw","to":"mid"}]}`
	exportReregisterAfter = `{"nodes":["a","b","c","mid","raw","report"],` +
		`"edges":[{"from":"a","to":"mid"},{"from":"b","to":"c"},` +
		`{"from":"c","to":"mid"},{"from":"mid","to":"report"},` +
		`{"from":"raw","to":"a"},{"from":"raw","to":"b"}]}`
)

// Regression for exporting after a same-name Register replaced an intermediate
// dataset's whole direct-upstream list. Re-registering mid with a and c drops
// the direct raw -> mid and old -> mid edges, but raw still derives report
// through TWO surviving branches — the short raw -> a -> mid -> report route
// and the longer raw -> b -> c -> mid -> report route — so the export must
// keep raw, a, b, c, mid and report with every surviving direct dependency,
// not just the shortest path. old and oldroot no longer take part in report's
// derivation and leave the document together with their edges, while their
// registrations and the oldroot -> old dependency stay in the graph. view and
// isolated never enter report's upstream document.
func TestExportUpstreamLineageAfterReregister(t *testing.T) {
	graph := exportReregisterScenario(t)
	assertConsistent(t, graph)

	// The export before the replacement covers the full ancestor closure,
	// old and oldroot included.
	if out := mustExport(t, graph, "report"); out != exportReregisterBefore {
		t.Errorf("export before re-register =\n%s\nwant:\n%s", out, exportReregisterBefore)
	}

	// Replace mid's whole direct-upstream list; report's own registration is
	// not part of the request and must stay as it was.
	mustRegister(t, graph, "mid", "a", "c")
	assertConsistent(t, graph)
	assertEntry(t, graph, "mid", []string{"a", "c"}, []string{"report"})
	assertEntry(t, graph, "report", []string{"mid"}, []string{"view"})
	// old and oldroot keep their registrations and their mutual dependency;
	// old only lost the removed reverse edge to mid.
	assertEntry(t, graph, "old", []string{"oldroot"}, nil)
	assertEntry(t, graph, "oldroot", nil, []string{"old"})
	assertEntry(t, graph, "raw", nil, []string{"a", "b"})

	before := snapshotExportGraph(graph)
	out := mustExport(t, graph, "report")
	if out != exportReregisterAfter {
		t.Errorf("export after re-register =\n%s\nwant:\n%s", out, exportReregisterAfter)
	}
	doc := parseExport(t, out)

	// Every surviving branch participates: raw reaches report through a AND
	// through b -> c, so all of raw, a, b, c, mid stay listed exactly once.
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

	// The dropped raw -> mid direct edge is gone, but raw itself must not
	// follow it out of the document.
	for _, e := range doc.Edges {
		if e.From == "raw" && e.To == "mid" {
			t.Errorf("dropped direct edge raw -> mid survived in %v", doc.Edges)
		}
	}
	// old and oldroot left report's derivation entirely; view and isolated
	// were never part of it.
	for _, excluded := range []string{"old", "oldroot", "view", "isolated"} {
		for _, node := range doc.Nodes {
			if node == excluded {
				t.Errorf("nodes must not contain %q: %v", excluded, doc.Nodes)
			}
		}
	}

	// The export is read-only over the post-replacement state.
	assertGraphUnchanged(t, before, graph)
}

// The replacement's declared upstream order cannot change the exported text:
// re-registering mid with [a c] or [c a] yields byte-identical documents, as
// does building the same final lineage with a different registration order.
func TestExportUpstreamLineageAfterReregisterOrderIndependent(t *testing.T) {
	first := exportReregisterScenario(t)
	mustRegister(t, first, "mid", "a", "c")

	second := buildRegisteredGraph(t, [][]string{
		{"isolated"},
		{"oldroot"},
		{"old", "oldroot"},
		{"raw"},
		{"b", "raw"},
		{"c", "b"},
		{"a", "raw"},
		{"mid", "old", "c", "raw", "a"}, // different list order before replacement
		{"report", "mid"},
		{"view", "report"},
	})
	mustRegister(t, second, "mid", "c", "a") // replacement list flipped

	if got, want := mustExport(t, first, "report"), mustExport(t, second, "report"); got != want {
		t.Errorf("replacement list order changed the export:\nfirst:  %s\nsecond: %s", got, want)
	}
	if got := mustExport(t, first, "report"); got != exportReregisterAfter {
		t.Errorf("export =\n%s\nwant:\n%s", got, exportReregisterAfter)
	}
}

// A rejected replacement must not partially apply: re-registering mid with the
// registered isolated followed by the unregistered ghost fails naming ghost,
// and mid — plus every related node — keeps its pre-request parents, children
// and list orders. A later export of report is byte-identical to the
// pre-request text and contains neither isolated nor any partial new relation.
func TestExportUpstreamLineageFailedReregisterLeavesExportUntouched(t *testing.T) {
	graph := exportReregisterScenario(t)
	assertConsistent(t, graph)

	exportBefore := mustExport(t, graph, "report")
	if exportBefore != exportReregisterBefore {
		t.Fatalf("export before failed re-register =\n%s\nwant:\n%s", exportBefore, exportReregisterBefore)
	}
	before := snapshotExportGraph(graph)

	err := Register(graph, Dataset{Name: "mid"}, []string{"isolated", "ghost"})
	if err == nil || !strings.Contains(err.Error(), "ghost") {
		t.Fatalf("want unknown-parent error naming ghost, got %v", err)
	}

	// No partial effect: the whole graph, list orders included, is exactly as
	// before the request; mid never gained isolated nor lost any upstream.
	assertGraphUnchanged(t, before, graph)
	assertEntry(t, graph, "mid", []string{"raw", "a", "c", "old"}, []string{"report"})
	assertEntry(t, graph, "isolated", nil, nil)
	assertEntry(t, graph, "raw", nil, []string{"a", "b", "mid"})
	assertEntry(t, graph, "old", []string{"oldroot"}, []string{"mid"})
	assertConsistent(t, graph)

	// The export after the rejected request matches the pre-request text
	// exactly, with no trace of isolated or a half-applied replacement.
	exportAfter := mustExport(t, graph, "report")
	if exportAfter != exportBefore {
		t.Errorf("export changed after rejected re-register:\nbefore: %s\nafter:  %s",
			exportBefore, exportAfter)
	}
	if strings.Contains(exportAfter, "isolated") {
		t.Errorf("export must not contain isolated after the rejected request: %s", exportAfter)
	}
	assertGraphUnchanged(t, before, graph)
}
