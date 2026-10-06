package chainledger

import (
	"reflect"
	"strings"
	"testing"
)

// exportRenameScenario builds the regression lineage: raw derives a and b, b
// derives mid, and report depends directly on raw, a and mid — so raw reaches
// report over the direct edge AND over the longer raw -> a -> report and
// raw -> b -> mid -> report branches. report derives view, mid also derives
// side (a downstream that takes no part in report's derivation), and isolated
// stands alone. mid is the intermediate dataset that gets renamed.
func exportRenameScenario(t *testing.T) map[string]*Lineage {
	t.Helper()
	return buildRegisteredGraph(t, [][]string{
		{"raw"},
		{"b", "raw"},
		{"a", "raw"},
		{"mid", "b"},
		{"report", "raw", "a", "mid"},
		{"view", "report"},
		{"side", "mid"},
		{"isolated"},
	})
}

// The exact JSON texts before and after mid is renamed to zz. Both orderings
// are fully determined by the names: zz sorts past every other node, so it
// leaves mid's old slot in the nodes array for the end, and the b -> mid /
// mid -> report edges move to their new from/to positions instead of keeping
// mid's old ones.
const (
	exportRenameBefore = `{"nodes":["a","b","mid","raw","report"],` +
		`"edges":[{"from":"a","to":"report"},{"from":"b","to":"mid"},` +
		`{"from":"mid","to":"report"},{"from":"raw","to":"a"},` +
		`{"from":"raw","to":"b"},{"from":"raw","to":"report"}]}`
	exportRenameAfter = `{"nodes":["a","b","raw","report","zz"],` +
		`"edges":[{"from":"a","to":"report"},{"from":"b","to":"zz"},` +
		`{"from":"raw","to":"a"},{"from":"raw","to":"b"},` +
		`{"from":"raw","to":"report"},{"from":"zz","to":"report"}]}`
)

// Regression for exporting after an intermediate dataset in the target's
// derivation is renamed. Renaming mid to zz changes no dependency: report
// still depends on raw directly AND indirectly through a and through b -> zz,
// so the export must keep all five nodes exactly once and all six direct
// dependencies exactly once — not just the shortest raw -> report branch —
// with every reference to the renamed dataset written under its new name, in
// the node list and in every edge where it is the from or the to end. The new
// name sorts past the others, so zz and its edges take new positions in the
// name-determined ordering rather than mid's old slots. report's downstream
// view, mid's other downstream side and the independent isolated never enter
// report's document, and the export itself reorders nothing in the graph.
func TestExportUpstreamLineageAfterRename(t *testing.T) {
	graph := exportRenameScenario(t)
	assertConsistent(t, graph)

	// The export before the rename pins the pre-rename document: mid present,
	// every branch of report's derivation preserved.
	if out := mustExport(t, graph, "report"); out != exportRenameBefore {
		t.Errorf("export before rename =\n%s\nwant:\n%s", out, exportRenameBefore)
	}

	// Rename the intermediate dataset; no edge is added, removed or reordered.
	mustRename(t, graph, "mid", "zz")
	assertConsistent(t, graph)
	if _, ok := graph["mid"]; ok {
		t.Error("old name mid still present in graph")
	}
	if got, want := len(graph), 8; got != want {
		t.Fatalf("dataset count = %d, want %d (rename must not merge or drop nodes)", got, want)
	}
	// zz sits in mid's old slot everywhere: same parents, same children, same
	// list positions in every neighbor — including side, which is downstream
	// of the renamed dataset but outside report's derivation.
	assertEntry(t, graph, "raw", nil, []string{"b", "a", "report"})
	assertEntry(t, graph, "a", []string{"raw"}, []string{"report"})
	assertEntry(t, graph, "b", []string{"raw"}, []string{"zz"})
	assertEntry(t, graph, "zz", []string{"b"}, []string{"report", "side"})
	assertEntry(t, graph, "report", []string{"raw", "a", "zz"}, []string{"view"})
	assertEntry(t, graph, "side", []string{"zz"}, nil)
	assertEntry(t, graph, "view", []string{"report"}, nil)
	assertEntry(t, graph, "isolated", nil, nil)

	before := snapshotExportGraph(graph)
	out := mustExport(t, graph, "report")
	if out != exportRenameAfter {
		t.Errorf("export after rename =\n%s\nwant:\n%s", out, exportRenameAfter)
	}
	doc := parseExport(t, out)

	// The target and its whole ancestor closure each appear exactly once,
	// under current names.
	wantNodes := []string{"a", "b", "raw", "report", "zz"}
	if !reflect.DeepEqual(doc.Nodes, wantNodes) {
		t.Errorf("nodes = %v, want %v", doc.Nodes, wantNodes)
	}
	// Every direct dependency survives exactly once, still written from the
	// upstream to the derived dataset: the direct raw -> report edge AND the
	// longer branches through a and through b -> zz.
	wantEdges := [][2]string{
		{"a", "report"},
		{"b", "zz"},
		{"raw", "a"},
		{"raw", "b"},
		{"raw", "report"},
		{"zz", "report"},
	}
	if got := exportEdgeNames(doc); !reflect.DeepEqual(got, wantEdges) {
		t.Errorf("edges = %v, want %v", got, wantEdges)
	}

	// The old name appears nowhere in the document.
	if strings.Contains(out, `"mid"`) {
		t.Errorf("old name mid leaked into post-rename export: %s", out)
	}
	// view and side are downstreams that take no part in report's derivation;
	// isolated is unrelated. None of them may enter the document.
	for _, excluded := range []string{"view", "side", "isolated"} {
		for _, node := range doc.Nodes {
			if node == excluded {
				t.Errorf("nodes must not contain %q: %v", excluded, doc.Nodes)
			}
		}
		for _, e := range doc.Edges {
			if e.From == excluded || e.To == excluded {
				t.Errorf("edges must not involve %q: %v", excluded, doc.Edges)
			}
		}
	}

	// The export is read-only over the post-rename state: no node, edge or
	// stored list order was reshuffled to produce the sorted document.
	assertGraphUnchanged(t, before, graph)
}

// A rejected rename must not partially apply: zz's candidate new name a is
// already registered to another dataset, so the rename fails naming a, the
// two nodes are NOT merged and no reference is replaced. A later export of
// report is byte-identical to the pre-request text, in the same JSON shape
// callers already read.
func TestExportUpstreamLineageFailedRenameLeavesExportUntouched(t *testing.T) {
	graph := exportRenameScenario(t)
	assertConsistent(t, graph)

	exportBefore := mustExport(t, graph, "report")
	if exportBefore != exportRenameBefore {
		t.Fatalf("export before failed rename =\n%s\nwant:\n%s", exportBefore, exportRenameBefore)
	}
	before := snapshotExportGraph(graph)

	// a is already registered: the rename must be refused with an error
	// naming the taken name.
	err := Rename(graph, "mid", "a")
	if err == nil || !strings.Contains(err.Error(), "already in use") || !strings.Contains(err.Error(), "a") {
		t.Fatalf("want name-in-use error naming a, got %v", err)
	}

	// No merge and no partial rewiring: both datasets keep their own nodes,
	// and the whole graph — list orders included — is exactly as before.
	if got, want := len(graph), 8; got != want {
		t.Fatalf("dataset count = %d, want %d (rejected rename must not merge nodes)", got, want)
	}
	assertGraphUnchanged(t, before, graph)
	assertEntry(t, graph, "mid", []string{"b"}, []string{"report", "side"})
	assertEntry(t, graph, "a", []string{"raw"}, []string{"report"})
	assertEntry(t, graph, "report", []string{"raw", "a", "mid"}, []string{"view"})
	assertEntry(t, graph, "b", []string{"raw"}, []string{"mid"})
	assertConsistent(t, graph)

	// The export after the rejected request matches the pre-request text
	// exactly: same two arrays, same fields, no trace of the failed rename.
	exportAfter := mustExport(t, graph, "report")
	if exportAfter != exportBefore {
		t.Errorf("export changed after rejected rename:\nbefore: %s\nafter:  %s",
			exportBefore, exportAfter)
	}
	doc := parseExport(t, exportAfter)
	if doc.Edges == nil {
		t.Errorf("edges must still decode as an array from %s", exportAfter)
	}
	assertGraphUnchanged(t, before, graph)
}
