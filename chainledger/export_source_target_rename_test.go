package chainledger

import (
	"reflect"
	"strings"
	"testing"
)

// scopedRenameScenario builds the scoped-export rename regression lineage.
// source derives target directly and also through the intermediate dataset
// mid; mid additionally depends on the independent root extra and has a
// downstream deadend that cannot reach target. The later rename moves mid to
// a name that contains the old name's text and sorts after target, so the
// document order changes while the graph's stored list positions stay put.
//
//	extra ──────┐
//	            ├──> mid ──> deadend   (mid downstream that never reaches target)
//	source ─────┘      │
//	source ────────────┴──> target     (source also derives target directly)
func scopedRenameScenario(t *testing.T) map[string]*Lineage {
	t.Helper()
	return buildRegisteredGraph(t, [][]string{
		{"extra"},
		{"source"},
		{"mid", "source", "extra"},
		{"target", "source", "mid"},
		{"deadend", "mid"},
	})
}

// The exact scoped JSON text before any rename: nodes in Go string order,
// edges from upstream to derived ordered by from then to. extra (target's
// independent upstream through mid), the extra -> mid edge and deadend are
// all outside the source -> target routes and stay out.
const scopedRenameBefore = `{"nodes":["mid","source","target"],` +
	`"edges":[{"from":"mid","to":"target"},{"from":"source","to":"mid"},` +
	`{"from":"source","to":"target"}]}`

// After mid is renamed to zmid: the node set and the direct dependencies are
// the same three and three, only name-substituted. zmid contains the old
// name's text yet sorts after target, so the node moves to the end of the
// nodes array and its two edges move behind the source -> target edge.
const scopedRenameAfterMid = `{"nodes":["source","target","zmid"],` +
	`"edges":[{"from":"source","to":"target"},{"from":"source","to":"zmid"},` +
	`{"from":"zmid","to":"target"}]}`

// Regression for the source-scoped export after an intermediate dataset on
// one of the routes is renamed. The direct source -> target relation and the
// longer source -> mid -> target route both survive under the new name: the
// selected nodes and direct dependencies only change names, never count. The
// independent source extra, its edge into the renamed node, and the dead-end
// downstream stay excluded. The new name sorts by its own value (it happens
// to contain the old name's text, which must not confuse matching), the
// stored up/downstream lists keep their original positions, and the export
// itself stays read-only.
func TestExportSourceTargetLineageAfterRename(t *testing.T) {
	graph := scopedRenameScenario(t)
	assertConsistent(t, graph)

	if out := mustExportScoped(t, graph, "source", "target"); out != scopedRenameBefore {
		t.Fatalf("scoped export before rename =\n%s\nwant:\n%s", out, scopedRenameBefore)
	}
	beforeDoc := parseExport(t, scopedRenameBefore)

	// Rename the intermediate dataset mid to zmid. The node keeps both of its
	// lists untouched; every neighbor's reference is rewritten in place.
	mustRename(t, graph, "mid", "zmid")
	assertConsistent(t, graph)
	if _, ok := graph["mid"]; ok {
		t.Fatal("old name mid still present in graph")
	}
	if got, want := len(graph), 5; got != want {
		t.Fatalf("dataset count = %d, want %d (rename must not merge nodes)", got, want)
	}
	assertEntry(t, graph, "zmid", []string{"source", "extra"}, []string{"target", "deadend"})
	assertEntry(t, graph, "source", nil, []string{"zmid", "target"})
	assertEntry(t, graph, "extra", nil, []string{"zmid"})
	assertEntry(t, graph, "target", []string{"source", "zmid"}, nil)
	assertEntry(t, graph, "deadend", []string{"zmid"}, nil)

	before := snapshotExportGraph(graph)
	out := mustExportScoped(t, graph, "source", "target")
	if out != scopedRenameAfterMid {
		t.Errorf("scoped export after rename =\n%s\nwant:\n%s", out, scopedRenameAfterMid)
	}
	doc := parseExport(t, out)

	// Same selection as before the rename, only name-substituted: the node
	// and edge counts are unchanged, and both the direct relation and the
	// longer route through the renamed node are complete.
	if got, want := len(doc.Nodes), len(beforeDoc.Nodes); got != want {
		t.Errorf("node count changed across rename: %d, want %d", got, want)
	}
	if got, want := len(doc.Edges), len(beforeDoc.Edges); got != want {
		t.Errorf("edge count changed across rename: %d, want %d", got, want)
	}
	wantNodes := []string{"source", "target", "zmid"}
	if !reflect.DeepEqual(doc.Nodes, wantNodes) {
		t.Errorf("nodes = %v, want %v", doc.Nodes, wantNodes)
	}
	wantEdges := [][2]string{
		{"source", "target"},
		{"source", "zmid"},
		{"zmid", "target"},
	}
	if got := exportEdgeNames(doc); !reflect.DeepEqual(got, wantEdges) {
		t.Errorf("edges = %v, want %v", got, wantEdges)
	}

	// The old name appears neither as a node nor as an edge endpoint. Match
	// by full name: the new name zmid legitimately contains the text "mid".
	for _, node := range doc.Nodes {
		if node == "mid" {
			t.Errorf("old name mid still exported as a node: %v", doc.Nodes)
		}
	}
	for _, e := range wantEdges {
		if e[0] == "mid" || e[1] == "mid" {
			t.Errorf("old name mid still exported as an edge endpoint: %v", e)
		}
	}

	// The independent source extra, its edge into the renamed node, and the
	// dead-end downstream deadend remain excluded.
	for _, excluded := range []string{"extra", "deadend"} {
		for _, node := range doc.Nodes {
			if node == excluded {
				t.Errorf("nodes must not contain %q: %v", excluded, doc.Nodes)
			}
		}
	}
	for _, e := range wantEdges {
		if e[0] == "extra" || e[1] == "extra" {
			t.Errorf("edge through the independent source leaked into export: %v", e)
		}
		if e[0] == "deadend" || e[1] == "deadend" {
			t.Errorf("edge through the dead-end downstream leaked into export: %v", e)
		}
	}

	// The export only reads the graph: no node, edge or stored list order
	// (including source's non-name-sorted children [zmid target]) changes.
	assertGraphUnchanged(t, before, graph)

	// The complete upstream export of the same target keeps working on the
	// renamed graph and now also brings along the independent source extra.
	wantFull := `{"nodes":["extra","source","target","zmid"],` +
		`"edges":[{"from":"extra","to":"zmid"},{"from":"source","to":"target"},` +
		`{"from":"source","to":"zmid"},{"from":"zmid","to":"target"}]}`
	if full := mustExport(t, graph, "target"); full != wantFull {
		t.Errorf("full upstream export after rename =\n%s\nwant:\n%s", full, wantFull)
	}

	// Other lineage queries see the renamed graph consistently: target's
	// upstreams include the renamed node and the independent source, and the
	// renamed node's impacts still reach both of its downstreams.
	upstreams, err := Upstreams(graph, "target")
	if err != nil {
		t.Fatalf("Upstreams(target) after rename: %v", err)
	}
	gotUpstreams := make([]string, len(upstreams))
	for i, u := range upstreams {
		gotUpstreams[i] = u.Dataset
	}
	// Distance 1: source and zmid (by name); distance 2: extra through zmid.
	if want := []string{"source", "zmid", "extra"}; !reflect.DeepEqual(gotUpstreams, want) {
		t.Errorf("Upstreams(target) after rename = %v, want %v", gotUpstreams, want)
	}
	impacts, err := Impacts(graph, "zmid")
	if err != nil {
		t.Fatalf("Impacts(zmid) after rename: %v", err)
	}
	gotImpacts := make([]string, len(impacts))
	for i, im := range impacts {
		gotImpacts[i] = im.Dataset
	}
	if want := []string{"deadend", "target"}; !reflect.DeepEqual(gotImpacts, want) {
		t.Errorf("Impacts(zmid) after rename = %v, want %v", gotImpacts, want)
	}
	assertGraphUnchanged(t, before, graph)
}

// A node lying on several routes at once still appears once, and so does
// every direct dependency, after that node is renamed. source reaches join
// through p and through q and also derives target directly; renaming join to
// ajoin (which sorts before every other selected name) must keep exactly one
// node and exactly one copy of each of the four edges touching it.
func TestExportSourceTargetLineageAfterRenameMergesOnce(t *testing.T) {
	graph := buildRegisteredGraph(t, [][]string{
		{"source"},
		{"p", "source"},
		{"q", "source"},
		{"join", "p", "q"},
		{"target", "source", "join"},
	})
	mustRename(t, graph, "join", "ajoin")
	assertConsistent(t, graph)

	before := snapshotExportGraph(graph)
	doc := parseExport(t, mustExportScoped(t, graph, "source", "target"))

	wantNodes := []string{"ajoin", "p", "q", "source", "target"}
	if !reflect.DeepEqual(doc.Nodes, wantNodes) {
		t.Errorf("nodes = %v, want %v", doc.Nodes, wantNodes)
	}
	wantEdges := [][2]string{
		{"ajoin", "target"},
		{"p", "ajoin"},
		{"q", "ajoin"},
		{"source", "p"},
		{"source", "q"},
		{"source", "target"},
	}
	if got := exportEdgeNames(doc); !reflect.DeepEqual(got, wantEdges) {
		t.Errorf("edges = %v, want %v", got, wantEdges)
	}

	// Uniqueness, explicitly: no repeated node, no repeated direct dependency.
	seenNodes := map[string]bool{}
	for _, node := range doc.Nodes {
		if seenNodes[node] {
			t.Errorf("node %q exported more than once: %v", node, doc.Nodes)
		}
		seenNodes[node] = true
	}
	seenEdges := map[[2]string]bool{}
	for _, e := range exportEdgeNames(doc) {
		if seenEdges[e] {
			t.Errorf("edge %v exported more than once", e)
		}
		seenEdges[e] = true
	}
	assertGraphUnchanged(t, before, graph)
}

// A rename whose new name is already taken fails even when the occupying
// dataset lies outside the exported routes: the error names the taken name,
// no reference is partially rewritten, and the scoped export afterwards is
// byte-identical to the text produced before the rejected request.
func TestExportSourceTargetLineageFailedRenameKeepsExport(t *testing.T) {
	graph := scopedRenameScenario(t)
	// The blocker holds the would-be new name but takes no part in the
	// source -> target derivation: it is neither downstream of source nor
	// upstream of target, so it never enters the scoped export.
	mustRegister(t, graph, "zmid")
	assertConsistent(t, graph)

	exportBefore := mustExportScoped(t, graph, "source", "target")
	if exportBefore != scopedRenameBefore {
		t.Fatalf("scoped export before failed rename =\n%s\nwant:\n%s", exportBefore, scopedRenameBefore)
	}
	before := snapshotExportGraph(graph)

	err := Rename(graph, "mid", "zmid")
	if err == nil {
		t.Fatal("rename to an occupied name succeeded")
	}
	if !strings.Contains(err.Error(), "zmid") {
		t.Errorf("error %q must name the occupied new name", err)
	}

	// No partial rewiring: every node, edge and list order is exactly as
	// before the rejected request, and the dataset count is unchanged.
	assertGraphUnchanged(t, before, graph)
	if got, want := len(graph), 6; got != want {
		t.Fatalf("dataset count = %d, want %d", got, want)
	}
	assertEntry(t, graph, "mid", []string{"source", "extra"}, []string{"target", "deadend"})
	assertEntry(t, graph, "zmid", nil, nil)
	assertEntry(t, graph, "target", []string{"source", "mid"}, nil)
	assertConsistent(t, graph)

	// The scoped export text is exactly what it was before the failed
	// request and still names mid under its old name.
	exportAfter := mustExportScoped(t, graph, "source", "target")
	if exportAfter != exportBefore {
		t.Errorf("scoped export changed after rejected rename:\nbefore: %s\nafter:  %s",
			exportBefore, exportAfter)
	}
	doc := parseExport(t, exportAfter)
	foundMid := false
	for _, node := range doc.Nodes {
		if node == "mid" {
			foundMid = true
		}
		if node == "zmid" {
			t.Errorf("rejected new name leaked into export: %v", doc.Nodes)
		}
	}
	if !foundMid {
		t.Errorf("export must still name mid after the rejected rename: %v", doc.Nodes)
	}
	assertGraphUnchanged(t, before, graph)
}
