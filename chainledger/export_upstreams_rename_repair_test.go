package chainledger

import (
	"reflect"
	"slices"
	"sort"
	"strconv"
	"strings"
	"testing"
)

// Names carrying raw, invalid UTF-8 bytes. Registration and Rename accept them
// verbatim; only ExportUpstreamLineage of a closure that contains them rejects.
const (
	repairBadName      = "mid\xff" // intermediate dataset blocking report's export
	repairStrangerName = "stranger\xfe"
)

// repairLineageScenario builds the lineage for the rename-repair regression.
// src participates in merge's derivation twice: directly, and through the
// intermediate dataset whose registered name contains an invalid UTF-8 byte.
// report derives from merge; side derives from the bad intermediate but takes
// no part in report's derivation; a standalone dataset with a different
// invalid name is registered outside report's closure. Registration of every
// invalid name must succeed.
func repairLineageScenario(t *testing.T) map[string]*Lineage {
	t.Helper()
	return buildRegisteredGraph(t, [][]string{
		{"src"},
		{repairBadName, "src"},
		{"merge", "src", repairBadName}, // branch merge: src direct and through the bad node
		{"report", "merge"},
		{"side", repairBadName}, // downstream of the bad node outside report's closure
		{repairStrangerName},    // invalid name, unrelated to report's derivation
	})
}

// assertExportBlockedByInvalidName exports target and requires the documented
// failure shape: an empty string, no partial JSON, and the offending name in
// Go-quoted form so the original illegal bytes stay distinguishable.
func assertExportBlockedByInvalidName(t *testing.T, graph map[string]*Lineage, target, bad string) {
	t.Helper()
	out, err := ExportUpstreamLineage(graph, target)
	if err == nil {
		t.Fatalf("ExportUpstreamLineage(%q) with invalid name %q in its lineage succeeded: %s",
			target, bad, out)
	}
	if out != "" {
		t.Errorf("ExportUpstreamLineage(%q) returned partial content %q, want empty string", target, out)
	}
	if !strings.Contains(err.Error(), "not valid UTF-8") {
		t.Errorf("ExportUpstreamLineage(%q) error %q must be the encoding error", target, err)
	}
	quoted := strconv.Quote(bad)
	if !strings.Contains(err.Error(), quoted) {
		t.Errorf("ExportUpstreamLineage(%q) error %q must show the original bytes %s in Go-quoted form",
			target, err, quoted)
	}
}

// assertEdgesFollowRegisteredParents reads the exported edges as a consumer
// would and confirms every edge runs from a registered direct upstream to the
// dataset derived from it — the documented from -> to direction.
func assertEdgesFollowRegisteredParents(t *testing.T, graph map[string]*Lineage, doc exportDocument) {
	t.Helper()
	for _, e := range doc.Edges {
		if !slices.Contains(graph[e.To].Parents, e.From) {
			t.Errorf("exported edge %q -> %q is not a registered direct dependency; parents of %q are %v",
				e.From, e.To, e.To, graph[e.To].Parents)
		}
	}
}

// End-to-end repair flow: an invalid name anywhere in the target's closure
// blocks the full upstream export until the dataset is renamed to a legal,
// unused name; afterwards the same derived data exports with every original
// dependency intact, including both routes of the branch merge.
func TestExportUpstreamLineageRenameRepairsInvalidName(t *testing.T) {
	graph := repairLineageScenario(t)
	assertConsistent(t, graph)
	assertEntry(t, graph, "src", nil, []string{repairBadName, "merge"})
	assertEntry(t, graph, repairBadName, []string{"src"}, []string{"merge", "side"})
	assertEntry(t, graph, "merge", []string{"src", repairBadName}, []string{"report"})

	// Before the repair, the invalid name blocks the export whether it sits in
	// the closure indirectly (report) or as a direct upstream (merge). The
	// failure returns no document and leaves the registry and every neighbor
	// list exactly as it was.
	before := snapshotExportGraph(graph)
	assertExportBlockedByInvalidName(t, graph, "report", repairBadName)
	assertExportBlockedByInvalidName(t, graph, "merge", repairBadName)
	assertGraphUnchanged(t, before, graph)

	// The encoding rule belongs to the export only: registry queries still
	// answer with the raw registered name.
	ups := mustUpstreams(t, graph, "report")
	if got, want := upstreamNames(ups), []string{"merge", repairBadName, "src"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("upstreams before repair = %q, want %q (raw invalid name still listed)", got, want)
	}
	assertGraphUnchanged(t, before, graph)

	// Repair the name. Rename accepts the raw old name and the node keeps its
	// exact slot in every neighbor's list; no edge is added, dropped or merged.
	mustRename(t, graph, repairBadName, "repaired")
	assertConsistent(t, graph)
	if _, ok := graph[repairBadName]; ok {
		t.Fatal("old invalid name still registered after rename")
	}
	if got, want := len(graph), 6; got != want {
		t.Fatalf("dataset count = %d, want %d (rename must not merge or add nodes)", got, want)
	}
	assertEntry(t, graph, "repaired", []string{"src"}, []string{"merge", "side"})
	assertEntry(t, graph, "src", nil, []string{"repaired", "merge"}) // first slot rewritten in place
	assertEntry(t, graph, "merge", []string{"src", "repaired"}, []string{"report"})
	assertEntry(t, graph, "side", []string{"repaired"}, nil)

	// The export of the same derived data now succeeds, even though an
	// unrelated invalid name remains registered elsewhere in the graph.
	exportBefore := snapshotExportGraph(graph)
	out := mustExport(t, graph, "report")
	doc := parseExport(t, out)

	wantNodes := []string{"merge", "repaired", "report", "src"}
	wantEdges := [][2]string{
		{"merge", "report"},
		{"repaired", "merge"},
		{"src", "merge"},
		{"src", "repaired"},
	}
	if len(doc.Nodes) != 4 || len(doc.Edges) != 4 {
		t.Errorf("node/edge counts = %d nodes, %d edges, want 4 and 4 (the original relationships)",
			len(doc.Nodes), len(doc.Edges))
	}
	if !reflect.DeepEqual(doc.Nodes, wantNodes) {
		t.Errorf("nodes = %q, want %q", doc.Nodes, wantNodes)
	}
	if !slices.IsSorted(doc.Nodes) {
		t.Errorf("nodes are not in Go string order: %q", doc.Nodes)
	}
	if got := exportEdgeNames(doc); !reflect.DeepEqual(got, wantEdges) {
		t.Errorf("edges = %q, want %q", got, wantEdges)
	}
	if !sort.SliceIsSorted(doc.Edges, func(i, j int) bool {
		if doc.Edges[i].From != doc.Edges[j].From {
			return doc.Edges[i].From < doc.Edges[j].From
		}
		return doc.Edges[i].To < doc.Edges[j].To
	}) {
		t.Errorf("edges are not ordered by from then to: %v", exportEdgeNames(doc))
	}
	assertEdgesFollowRegisteredParents(t, graph, doc)

	// The branch merge survives in full: src reaches merge directly, and the
	// longer src -> repaired -> merge route stays even though the shorter one
	// exists. The repaired dataset must not be lost as "already reachable".
	edgeSet := map[[2]string]bool{}
	for _, e := range wantEdges {
		edgeSet[e] = true
	}
	for _, want := range [][2]string{
		{"src", "merge"},      // short, direct route
		{"src", "repaired"},   // longer branch, edge into the repaired node
		{"repaired", "merge"}, // longer branch, edge out of it
	} {
		if !edgeSet[want] {
			t.Errorf("relationship %v missing from the repaired export %v", want, wantEdges)
		}
	}

	// The old name leaves the document entirely: no raw illegal byte and no
	// node decoded back to it. Unrelated nodes never enter report's closure.
	if strings.Contains(out, "\xff") {
		t.Errorf("illegal byte from the old name leaked into the export: %s", out)
	}
	for _, name := range doc.Nodes {
		if name == repairBadName {
			t.Errorf("old invalid name decoded back from the export: %q", name)
		}
	}
	for _, excluded := range []string{"side", "stranger"} {
		if strings.Contains(out, excluded) {
			t.Errorf("node outside report's derivation (%q) leaked into the export: %s", excluded, out)
		}
	}

	// The repaired export must be byte-identical to that of an always-valid
	// twin lineage holding the same relationships: the repair changed names
	// only, never the node or dependency set.
	twin := buildRegisteredGraph(t, [][]string{
		{"src"},
		{"repaired", "src"},
		{"merge", "src", "repaired"},
		{"report", "merge"},
		{"side", "repaired"},
	})
	if twinOut := mustExport(t, twin, "report"); twinOut != out {
		t.Errorf("repaired export differs from the always-valid twin:\nrepaired: %s\ntwin:     %s",
			out, twinOut)
	}

	// The export rewrites no stored list; the stranger node is still parked in
	// the graph untouched, and targeting its own lineage still fails there.
	assertGraphUnchanged(t, exportBefore, graph)
	assertExportBlockedByInvalidName(t, graph, repairStrangerName, repairStrangerName)
	assertGraphUnchanged(t, exportBefore, graph)
}

// A rename whose new name is already registered to another dataset fails under
// the existing name-in-use rule: it names the conflict, merges nothing and
// rewrites no reference, so the original target's export keeps failing with
// the original illegal bytes. Recovery with a genuinely free name still works.
func TestExportUpstreamLineageRenameToOccupiedNameFailsAtomically(t *testing.T) {
	graph := repairLineageScenario(t)
	assertConsistent(t, graph)
	before := snapshotExportGraph(graph)

	// Occupied by an ordinary dataset.
	err := Rename(graph, repairBadName, "merge")
	if err == nil || !strings.Contains(err.Error(), "already in use") ||
		!strings.Contains(err.Error(), "merge") {
		t.Fatalf("rename onto merge: want name-in-use error naming merge, got %v", err)
	}
	assertGraphUnchanged(t, before, graph)

	// Occupied by another invalid-UTF-8 dataset: name matching stays byte
	// exact, and raw names keep being accepted on the way into Rename.
	err = Rename(graph, repairBadName, repairStrangerName)
	if err == nil || !strings.Contains(err.Error(), "already in use") ||
		!strings.Contains(err.Error(), repairStrangerName) {
		t.Fatalf("rename onto the invalid-UTF-8 dataset: want name-in-use error, got %v", err)
	}
	assertGraphUnchanged(t, before, graph)

	// Nothing merged and no reference partially rewritten: both names still
	// name their own nodes and every stored list keeps its pre-request order.
	if got, want := len(graph), 6; got != want {
		t.Fatalf("dataset count = %d, want %d", got, want)
	}
	assertEntry(t, graph, repairBadName, []string{"src"}, []string{"merge", "side"})
	assertEntry(t, graph, "merge", []string{"src", repairBadName}, []string{"report"})
	assertEntry(t, graph, "src", nil, []string{repairBadName, "merge"})
	assertEntry(t, graph, "side", []string{repairBadName}, nil)
	assertEntry(t, graph, repairStrangerName, nil, nil)
	assertConsistent(t, graph)

	// The original target's export still reports the original invalid name and
	// returns no document; names, relationships and list order are unchanged.
	assertExportBlockedByInvalidName(t, graph, "report", repairBadName)
	if out, err := ExportUpstreamLineage(graph, "report"); err == nil || out != "" {
		t.Fatalf("export after rejected rename: want empty string and encoding error, got %q, %v", out, err)
	}
	assertGraphUnchanged(t, before, graph)

	// After the rejected attempts the repair still succeeds normally.
	mustRename(t, graph, repairBadName, "repaired")
	assertConsistent(t, graph)
	out := mustExport(t, graph, "report")
	doc := parseExport(t, out)
	if slices.Contains(doc.Nodes, repairBadName) {
		t.Errorf("old invalid name decoded from the recovered export: %q", doc.Nodes)
	}
	if !slices.Contains(doc.Nodes, "repaired") {
		t.Errorf("repaired node missing from %q", doc.Nodes)
	}
	for _, want := range [][2]string{{"src", "merge"}, {"src", "repaired"}, {"repaired", "merge"}} {
		if !slices.Contains(exportEdgeNames(doc), want) {
			t.Errorf("relationship %v missing after recovery from %v", want, exportEdgeNames(doc))
		}
	}
}

// Legal new names containing Chinese text or a genuine replacement character
// (U+FFFD) are preserved byte for byte after the repair. The real U+FFFD rune
// is itself valid UTF-8 and must never be mistaken for a bad byte.
func TestExportUpstreamLineageRepairedUnicodeNameRoundTrips(t *testing.T) {
	for _, newName := range []string{
		"修复后的数据集",
		"repaired�name",
	} {
		t.Run(newName, func(t *testing.T) {
			graph := repairLineageScenario(t)
			assertExportBlockedByInvalidName(t, graph, "report", repairBadName)

			mustRename(t, graph, repairBadName, newName)
			assertConsistent(t, graph)
			// The renamed node keeps its place in every neighbor's list.
			assertEntry(t, graph, newName, []string{"src"}, []string{"merge", "side"})
			assertEntry(t, graph, "src", nil, []string{newName, "merge"})
			assertEntry(t, graph, "merge", []string{"src", newName}, []string{"report"})
			assertEntry(t, graph, "side", []string{newName}, nil)

			before := snapshotExportGraph(graph)
			out := mustExport(t, graph, "report")
			doc := parseExport(t, out)

			wantNodes := []string{"merge", "report", "src", newName}
			sort.Strings(wantNodes)
			if !reflect.DeepEqual(doc.Nodes, wantNodes) {
				t.Errorf("nodes = %q, want %q", doc.Nodes, wantNodes)
			}
			if !slices.IsSorted(doc.Nodes) {
				t.Errorf("nodes are not in Go string order: %q", doc.Nodes)
			}

			wantEdges := [][2]string{
				{"merge", "report"},
				{newName, "merge"},
				{"src", "merge"},
				{"src", newName},
			}
			sort.Slice(wantEdges, func(i, j int) bool {
				if wantEdges[i][0] != wantEdges[j][0] {
					return wantEdges[i][0] < wantEdges[j][0]
				}
				return wantEdges[i][1] < wantEdges[j][1]
			})
			if got := exportEdgeNames(doc); !reflect.DeepEqual(got, wantEdges) {
				t.Errorf("edges = %q, want %q", got, wantEdges)
			}
			assertEdgesFollowRegisteredParents(t, graph, doc)

			// A JSON reader recovers the registered name exactly, rune for rune.
			if !slices.Contains(doc.Nodes, newName) {
				t.Errorf("new name %q did not round-trip; nodes are %q", newName, doc.Nodes)
			}
			if strings.ContainsRune(strings.Join(doc.Nodes, ""), '�') &&
				!strings.ContainsRune(newName, '�') {
				t.Errorf("a valid name was mangled into a replacement rune: %q", doc.Nodes)
			}
			// The genuine U+FFFD rune is emitted as itself rather than rejected;
			// no lone illegal byte is ever present in the document.
			if strings.ContainsRune(newName, '�') && !strings.Contains(out, "�") {
				t.Errorf("real U+FFFD rune missing from export text: %s", out)
			}
			if strings.Contains(out, "\xff") {
				t.Errorf("lone illegal byte 0xff leaked into the export: %s", out)
			}
			if strings.Contains(out, "side") || strings.Contains(out, "stranger") {
				t.Errorf("node outside report's derivation leaked into the export: %s", out)
			}
			assertGraphUnchanged(t, before, graph)
		})
	}
}

// Repairing the one invalid name the target depends on is sufficient: another
// invalid name elsewhere in the graph (here with its own downstream) must not
// be cleaned up first, and keeps blocking only exports of its own lineage.
func TestExportUpstreamLineageRepairIgnoresInvalidNameOutsideClosure(t *testing.T) {
	otherBad := "detached\xfe"
	graph := buildRegisteredGraph(t, [][]string{
		{"src"},
		{repairBadName, "src"},
		{"merge", "src", repairBadName},
		{"report", "merge"},
		{otherBad},
		{"otherchild", otherBad}, // invalid name feeds a subtree unrelated to report
	})
	assertConsistent(t, graph)

	// report is blocked solely by the bad name in its own closure.
	assertExportBlockedByInvalidName(t, graph, "report", repairBadName)

	mustRename(t, graph, repairBadName, "repaired")
	assertConsistent(t, graph)
	before := snapshotExportGraph(graph)

	// report's full upstream lineage exports without the unrelated bad name
	// being cleaned up anywhere in the graph.
	out := mustExport(t, graph, "report")
	doc := parseExport(t, out)
	wantNodes := []string{"merge", "repaired", "report", "src"}
	if !reflect.DeepEqual(doc.Nodes, wantNodes) {
		t.Errorf("nodes = %q, want %q", doc.Nodes, wantNodes)
	}
	wantEdges := [][2]string{
		{"merge", "report"},
		{"repaired", "merge"},
		{"src", "merge"},
		{"src", "repaired"},
	}
	if got := exportEdgeNames(doc); !reflect.DeepEqual(got, wantEdges) {
		t.Errorf("edges = %q, want %q", got, wantEdges)
	}
	if strings.Contains(out, "detached") || strings.Contains(out, "otherchild") {
		t.Errorf("unrelated subtree leaked into report's export: %s", out)
	}

	// Encoding stays enforced per selected lineage: exports that actually
	// include the other invalid name still fail with its bytes quoted.
	assertExportBlockedByInvalidName(t, graph, "otherchild", otherBad)
	assertExportBlockedByInvalidName(t, graph, otherBad, otherBad)
	assertGraphUnchanged(t, before, graph)
}
