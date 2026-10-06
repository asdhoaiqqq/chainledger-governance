package chainledger

import (
	"reflect"
	"sort"
	"strings"
	"testing"
	"unicode/utf8"
)

// exportRepairBadName is a registered dataset name that carries a lone 0xff
// byte: registration accepts it (names are raw Go strings), but it cannot be
// carried by a JSON export losslessly.
const exportRepairBadName = "mid\xff"

// exportRepairFixedName is the legal, unoccupied name the dataset is repaired
// with in the main scenario.
const exportRepairFixedName = "mended"

// exportRepairScenario builds the rename-repair regression lineage:
//
//	raw -> a -> mid\xff -> join -> report
//	raw also feeds join directly, so the same source participates in join's
//	derivation both directly and through the badly-named intermediate dataset
//	(a branch merge). sidedown also derives from the bad-name node but takes no
//	part in report's derivation. other\xff/otherchild hold invalid names
//	outside report's closure; occupied is a registered dataset whose name the
//	conflicting rename attempt tries to take.
func exportRepairScenario(t *testing.T) map[string]*Lineage {
	t.Helper()
	return buildRegisteredGraph(t, [][]string{
		{"raw"},
		{"a", "raw"},
		{exportRepairBadName, "a"},
		{"sidedown", exportRepairBadName}, // registered before join on purpose
		{"join", "raw", exportRepairBadName},
		{"report", "join"},
		{"other\xff"}, // invalid name outside report's closure
		{"otherchild", "other\xff"},
		{"occupied"}, // name a conflicting rename will try to take
	})
}

// The exact document expected after mid\xff is repaired to mended: the same
// five nodes and five direct dependencies as before the rename, with every
// reference to the repaired node carrying its new name.
const exportRepairAfter = `{"nodes":["a","join","mended","raw","report"],` +
	`"edges":[{"from":"a","to":"mended"},{"from":"join","to":"report"},` +
	`{"from":"mended","to":"join"},{"from":"raw","to":"a"},` +
	`{"from":"raw","to":"join"}]}`

// upstreamClosureShape independently walks target's ancestor closure and
// reports the node set and the direct dependencies among it, sorted exactly
// the way an export presents them. It is a separate implementation of the
// closure walk used to prove that renaming changes names only: the repaired
// export must equal this shape with the old name substituted, node and edge
// counts included.
func upstreamClosureShape(graph map[string]*Lineage, target string) ([]string, [][2]string) {
	included := map[string]bool{target: true}
	edgeSet := map[[2]string]bool{}
	queue := []string{target}
	for len(queue) > 0 {
		node := queue[0]
		queue = queue[1:]
		for _, parent := range graph[node].Parents {
			edgeSet[[2]string{parent, node}] = true
			if !included[parent] {
				included[parent] = true
				queue = append(queue, parent)
			}
		}
	}
	nodes := make([]string, 0, len(included))
	for name := range included {
		nodes = append(nodes, name)
	}
	sort.Strings(nodes)
	edges := make([][2]string, 0, len(edgeSet))
	for edge := range edgeSet {
		edges = append(edges, edge)
	}
	sort.Slice(edges, func(i, j int) bool {
		if edges[i][0] != edges[j][0] {
			return edges[i][0] < edges[j][0]
		}
		return edges[i][1] < edges[j][1]
	})
	return nodes, edges
}

func renameShape(nodes []string, edges [][2]string, oldName, newName string) ([]string, [][2]string) {
	renamedNodes := append([]string(nil), nodes...)
	for i := range renamedNodes {
		if renamedNodes[i] == oldName {
			renamedNodes[i] = newName
		}
	}
	sort.Strings(renamedNodes)
	renamedEdges := make([][2]string, len(edges))
	for i, edge := range edges {
		if edge[0] == oldName {
			edge[0] = newName
		}
		if edge[1] == oldName {
			edge[1] = newName
		}
		renamedEdges[i] = edge
	}
	sort.Slice(renamedEdges, func(i, j int) bool {
		if renamedEdges[i][0] != renamedEdges[j][0] {
			return renamedEdges[i][0] < renamedEdges[j][0]
		}
		return renamedEdges[i][1] < renamedEdges[j][1]
	})
	return renamedNodes, renamedEdges
}

func assertSortedStrings(t *testing.T, got []string, what string) {
	t.Helper()
	want := append([]string(nil), got...)
	sort.Strings(want)
	if !reflect.DeepEqual(got, want) {
		t.Errorf("%s = %v, want Go-string-order %v", what, got, want)
	}
}

func assertSortedEdges(t *testing.T, got [][2]string) {
	t.Helper()
	want := append([][2]string(nil), got...)
	sort.Slice(want, func(i, j int) bool {
		if want[i][0] != want[j][0] {
			return want[i][0] < want[j][0]
		}
		return want[i][1] < want[j][1]
	})
	if !reflect.DeepEqual(got, want) {
		t.Errorf("edges = %v, want from/to-sorted %v", got, want)
	}
}

// End-to-end regression for the user workflow: an invalid name sitting in a
// target's direct or indirect upstream blocks the full upstream export, the
// user renames that dataset to a legal unoccupied name, and the same derived
// data exports completely with every original dependency preserved — direct
// relationship and longer branch alike.
func TestExportUpstreamLineageRepairedByRename(t *testing.T) {
	graph := exportRepairScenario(t)
	assertConsistent(t, graph)

	// Record report's original closure shape (under the bad name) and the
	// up/downstream listings that must survive every attempt below.
	origNodes, origEdges := upstreamClosureShape(graph, "report")
	if got, want := len(origNodes), 5; got != want {
		t.Fatalf("original closure has %d nodes %v, want %d", got, origNodes, want)
	}
	if got, want := len(origEdges), 5; got != want {
		t.Fatalf("original closure has %d edges %v, want %d", got, origEdges, want)
	}
	reportUpstreamsBefore := mustUpstreams(t, graph, "report")
	if got, want := upstreamNames(reportUpstreamsBefore),
		[]string{"join", exportRepairBadName, "raw", "a"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("report upstreams before repair = %v, want %v", got, want)
	}
	assertUpstreamOnce(t, reportUpstreamsBefore, exportRepairBadName, 2,
		[]string{exportRepairBadName, "join", "report"})
	badImpactsBefore := mustImpacts(t, graph, exportRepairBadName)
	if got, want := impactNames(badImpactsBefore), []string{"join", "sidedown", "report"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("impacts of the bad name = %v, want %v", got, want)
	}

	// Before the rename, exporting fails whether the invalid name is a DIRECT
	// upstream (join) or an INDIRECT one (report): empty string, no partial
	// JSON, encoding error rendering the original bytes in Go-quoted form.
	before := snapshotExportGraph(graph)
	for _, target := range []string{"join", "report"} {
		out, err := ExportUpstreamLineage(graph, target)
		if err == nil {
			t.Fatalf("export %q with invalid upstream succeeded with %q", target, out)
		}
		if out != "" {
			t.Errorf("export %q returned partial content %q, want empty string", target, out)
		}
		if strings.ContainsAny(out, "{}\"") {
			t.Errorf("failed export %q left partial JSON: %q", target, out)
		}
		if !strings.Contains(err.Error(), `"mid\xff"`) {
			t.Errorf("export %q error %q must quote the raw bytes as %q", target, err, `"mid\xff"`)
		}
		if !strings.Contains(err.Error(), "UTF-8") {
			t.Errorf("export %q error %q must be an encoding error", target, err)
		}
	}

	// A failed export changes no registration and no up/downstream listing.
	assertGraphUnchanged(t, before, graph)
	if got := mustUpstreams(t, graph, "report"); !reflect.DeepEqual(got, reportUpstreamsBefore) {
		t.Fatalf("report upstreams changed after failed export: %v, want %v", got, reportUpstreamsBefore)
	}
	if got := mustImpacts(t, graph, exportRepairBadName); !reflect.DeepEqual(got, badImpactsBefore) {
		t.Fatalf("impacts changed after failed export: %v, want %v", got, badImpactsBefore)
	}

	// A rename onto a name already registered to another dataset must fail by
	// the existing rule and name the conflict; it merges nothing and rewrites
	// no reference.
	err := Rename(graph, exportRepairBadName, "occupied")
	if err == nil {
		t.Fatal("rename onto an occupied name succeeded")
	}
	if !strings.Contains(err.Error(), "already in use") || !strings.Contains(err.Error(), "occupied") {
		t.Errorf("conflict error = %q, must name the occupied name", err)
	}
	assertGraphUnchanged(t, before, graph)
	if got, want := len(graph), 9; got != want {
		t.Fatalf("dataset count = %d, want %d (failed rename must not merge nodes)", got, want)
	}
	if _, ok := graph[exportRepairBadName]; !ok {
		t.Fatal("bad-name node vanished after a rejected rename")
	}
	if entry := graph["occupied"]; len(entry.Parents) != 0 || len(entry.Children) != 0 {
		t.Fatalf("occupied node was altered by the rejected rename: %+v", entry)
	}
	assertEntry(t, graph, "join", []string{"raw", exportRepairBadName}, []string{"report"})
	assertEntry(t, graph, exportRepairBadName, []string{"a"}, []string{"sidedown", "join"})
	assertConsistent(t, graph)

	// Exporting the original target still reports the ORIGINAL bad name and
	// returns nothing; the conflict attempt changed no name, relation or order.
	out, err := ExportUpstreamLineage(graph, "report")
	if err == nil {
		t.Fatalf("export after rejected rename succeeded with %q", out)
	}
	if out != "" {
		t.Errorf("export after rejected rename returned %q, want empty string", out)
	}
	if !strings.Contains(err.Error(), `"mid\xff"`) {
		t.Errorf("error %q must still quote the original bad name", err)
	}
	if strings.Contains(err.Error(), "occupied") {
		t.Errorf("error %q must not mention the rejected new name", err)
	}
	assertGraphUnchanged(t, before, graph)
	if got := mustUpstreams(t, graph, "report"); !reflect.DeepEqual(got, reportUpstreamsBefore) {
		t.Fatalf("report upstreams changed after rejected rename: %v, want %v", got, reportUpstreamsBefore)
	}
	if got := mustImpacts(t, graph, exportRepairBadName); !reflect.DeepEqual(got, badImpactsBefore) {
		t.Fatalf("impacts changed after rejected rename: %v, want %v", got, badImpactsBefore)
	}

	// The user repairs the dataset with a legal name nobody holds. The node
	// keeps its own lists and its slot in every neighbor's list.
	if err := Rename(graph, exportRepairBadName, exportRepairFixedName); err != nil {
		t.Fatalf("repair rename failed: %v", err)
	}
	assertConsistent(t, graph)
	if got, want := len(graph), 9; got != want {
		t.Fatalf("dataset count = %d, want %d", got, want)
	}
	if _, ok := graph[exportRepairBadName]; ok {
		t.Fatal("old bad name still keyed in graph")
	}
	if _, ok := graph[exportRepairFixedName]; !ok {
		t.Fatal("repaired name missing from graph")
	}
	assertEntry(t, graph, exportRepairFixedName, []string{"a"}, []string{"sidedown", "join"})
	assertEntry(t, graph, "join", []string{"raw", exportRepairFixedName}, []string{"report"})
	assertEntry(t, graph, "sidedown", []string{exportRepairFixedName}, nil)
	assertEntry(t, graph, "a", []string{"raw"}, []string{exportRepairFixedName})
	assertEntry(t, graph, "raw", nil, []string{"a", "join"})
	assertEntry(t, graph, "report", []string{"join"}, nil)

	// Upstream/downstream queries now speak the new name in the same positions.
	reportUpstreams := mustUpstreams(t, graph, "report")
	if got, want := upstreamNames(reportUpstreams), []string{"join", exportRepairFixedName, "raw", "a"}; !reflect.DeepEqual(got, want) {
		t.Errorf("report upstreams after repair = %v, want %v", got, want)
	}
	assertUpstreamOnce(t, reportUpstreams, exportRepairFixedName, 2,
		[]string{exportRepairFixedName, "join", "report"})
	for _, up := range reportUpstreams {
		if strings.Contains(up.Dataset, "\xff") {
			t.Errorf("bad bytes survive in upstream listing: %q", up.Dataset)
		}
	}
	if got, want := impactNames(mustImpacts(t, graph, "raw")),
		[]string{"a", "join", exportRepairFixedName, "report", "sidedown"}; !reflect.DeepEqual(got, want) {
		t.Errorf("raw impacts after repair = %v, want %v", got, want)
	}

	// The same derived data now exports. The document is byte-pinned and, after
	// a JSON read, carries the accurate new name and the upstream -> derived
	// direction; counts match the original closure exactly.
	exportBefore := snapshotExportGraph(graph)
	out = mustExport(t, graph, "report")
	if out != exportRepairAfter {
		t.Errorf("export after repair =\n%s\nwant:\n%s", out, exportRepairAfter)
	}
	doc := parseExport(t, out)
	wantNodes, wantEdges := renameShape(origNodes, origEdges, exportRepairBadName, exportRepairFixedName)
	if !reflect.DeepEqual(doc.Nodes, wantNodes) {
		t.Errorf("nodes = %v, want original closure renamed %v", doc.Nodes, wantNodes)
	}
	if got := exportEdgeNames(doc); !reflect.DeepEqual(got, wantEdges) {
		t.Errorf("edges = %v, want original dependencies renamed %v", got, wantEdges)
	}
	if got, want := len(doc.Nodes), len(origNodes); got != want {
		t.Errorf("node count = %d, want original %d", got, want)
	}
	if got, want := len(doc.Edges), len(origEdges); got != want {
		t.Errorf("edge count = %d, want original %d", got, want)
	}
	assertSortedStrings(t, doc.Nodes, "nodes")
	assertSortedEdges(t, exportEdgeNames(doc))

	// The branch merge is fully preserved: raw feeds join directly AND reaches
	// it through a -> mended; the repaired intermediate must not be dropped just
	// because the shorter route already exists.
	for _, want := range wantEdges {
		switch want {
		case [2]string{"raw", "join"}, [2]string{"a", "mended"}, [2]string{"mended", "join"},
			[2]string{"raw", "a"}, [2]string{"join", "report"}:
		default:
			t.Errorf("unexpected edge in repaired export: %v", want)
		}
	}
	edgeSet := map[[2]string]bool{}
	for _, e := range exportEdgeNames(doc) {
		edgeSet[e] = true
	}
	for _, required := range [][2]string{
		{"raw", "join"}, // short, direct
		{"a", "mended"}, // longer branch through the repaired node
		{"mended", "join"},
	} {
		if !edgeSet[required] {
			t.Errorf("required branch edge %v missing from %v", required, exportEdgeNames(doc))
		}
	}

	// The old name (and its raw bad byte) appears nowhere; datasets outside
	// report's derivation stay out even though other\xff is still invalid.
	if strings.Contains(out, "\xff") || strings.Contains(out, exportRepairBadName) {
		t.Errorf("old bad name leaked into repaired export: %s", out)
	}
	for _, excluded := range []string{"sidedown", "occupied", "other"} {
		if strings.Contains(out, excluded) {
			t.Errorf("unrelated dataset %q leaked into report's export: %s", excluded, out)
		}
	}

	// An invalid name elsewhere in the graph does NOT have to be cleaned up:
	// report exports while otherchild's own lineage still cannot.
	if _, err := ExportUpstreamLineage(graph, "otherchild"); err == nil ||
		!strings.Contains(err.Error(), `"other\xff"`) {
		t.Errorf("export of otherchild must fail naming other\\xff, got %v", err)
	}
	if got := mustExport(t, graph, "raw"); got != `{"nodes":["raw"],"edges":[]}` {
		t.Errorf("raw alone must still export despite invalid names elsewhere: %s", got)
	}
	// sidedown derives through the repaired branch and now exports too.
	sideDoc := parseExport(t, mustExport(t, graph, "sidedown"))
	if !reflect.DeepEqual(sideDoc.Nodes, []string{"a", "mended", "raw", "sidedown"}) {
		t.Errorf("sidedown nodes = %v, want repaired branch", sideDoc.Nodes)
	}
	if got := exportEdgeNames(sideDoc); !reflect.DeepEqual(got, [][2]string{
		{"a", "mended"},
		{"mended", "sidedown"},
		{"raw", "a"},
	}) {
		t.Errorf("sidedown edges = %v, want repaired branch", got)
	}

	// Exporting rewrites nothing.
	assertGraphUnchanged(t, exportBefore, graph)
}

// Legal replacement names containing multibyte Chinese text or a genuine
// U+FFFD replacement rune are kept verbatim: that rune is valid UTF-8 itself
// and must never be mistaken for a bad byte. Nodes and edges keep the
// established sort rules, and a JSON reader recovers the name exactly.
func TestExportUpstreamLineageRenameToUnusualValidNames(t *testing.T) {
	for _, tc := range []struct {
		name    string
		newName string
	}{
		{"chinese name", "修复的数据集"},
		{"real replacement rune", "fix�name"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if !utf8.ValidString(tc.newName) {
				t.Fatalf("test setup: %q must be valid UTF-8", tc.newName)
			}
			graph := exportRepairScenario(t)
			if err := Rename(graph, exportRepairBadName, tc.newName); err != nil {
				t.Fatalf("Rename to %q failed: %v", tc.newName, err)
			}
			assertConsistent(t, graph)

			out := mustExport(t, graph, "report")
			if strings.Contains(out, "\xff") {
				t.Errorf("repaired export still carries a bad byte: %s", out)
			}
			doc := parseExport(t, out)
			if got, want := len(doc.Nodes), 5; got != want {
				t.Fatalf("node count = %d, want %d", got, want)
			}
			if got, want := len(doc.Edges), 5; got != want {
				t.Fatalf("edge count = %d, want %d", got, want)
			}
			assertSortedStrings(t, doc.Nodes, "nodes")
			assertSortedEdges(t, exportEdgeNames(doc))

			// The new name itself round-trips exactly, rune for rune.
			found := false
			for _, node := range doc.Nodes {
				if node == tc.newName {
					found = true
				}
				if !utf8.ValidString(node) {
					t.Errorf("node %q is not valid UTF-8 after JSON round trip", node)
				}
			}
			if !found {
				t.Errorf("new name %q missing from nodes %v", tc.newName, doc.Nodes)
			}

			// Both direct dependencies touching the repaired node use the new
			// name and keep the upstream -> derived direction. Order follows
			// the export's own from/to rule (multibyte names sort after ASCII).
			wantEdges := [][2]string{
				{"a", tc.newName},
				{tc.newName, "join"},
				{"join", "report"},
				{"raw", "a"},
				{"raw", "join"},
			}
			sort.Slice(wantEdges, func(i, j int) bool {
				if wantEdges[i][0] != wantEdges[j][0] {
					return wantEdges[i][0] < wantEdges[j][0]
				}
				return wantEdges[i][1] < wantEdges[j][1]
			})
			if got := exportEdgeNames(doc); !reflect.DeepEqual(got, wantEdges) {
				t.Errorf("edges = %v, want %v", got, wantEdges)
			}
		})
	}
}

// Registration and rename keep accepting raw (possibly non-UTF-8) names; the
// encoding restriction belongs solely to the export of the selected lineage.
// A repaired-looking rename onto raw bytes makes THAT node unexportable while
// unrelated lineages still export.
func TestRenameAndRegisterStillAcceptRawNames(t *testing.T) {
	graph := buildRegisteredGraph(t, [][]string{
		{"src"},
		{"node", "src"},
	})

	// Registration continues to accept an invalid name as a raw Go string.
	if err := Register(graph, Dataset{Name: "fresh\xff"}, nil); err != nil {
		t.Fatalf("Register with invalid name rejected: %v", err)
	}
	// Rename does too: the restriction is not enforced at rename time.
	if err := Rename(graph, "node", "node\xff"); err != nil {
		t.Fatalf("Rename to an invalid name rejected: %v", err)
	}
	assertConsistent(t, graph)
	assertEntry(t, graph, "node\xff", []string{"src"}, nil)

	out, err := ExportUpstreamLineage(graph, "node\xff")
	if err == nil {
		t.Fatalf("export of badly-named node succeeded with %q", out)
	}
	if out != "" {
		t.Errorf("failed export returned %q, want empty string", out)
	}
	if !strings.Contains(err.Error(), `"node\xff"`) {
		t.Errorf("error %q must quote the raw bytes", err)
	}

	// Selecting a lineage that does not include the invalid names still works,
	// proving the check is per-export rather than graph-wide.
	if got := mustExport(t, graph, "src"); got != `{"nodes":["src"],"edges":[]}` {
		t.Errorf("src export = %s, want only src", got)
	}
	if _, err := ExportUpstreamLineage(graph, "fresh\xff"); err == nil {
		t.Error("export selecting the other invalid name must fail")
	}
}
