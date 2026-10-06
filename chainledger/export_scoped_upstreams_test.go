package chainledger

import (
	"reflect"
	"strings"
	"testing"
)

func mustExportScoped(t *testing.T, graph map[string]*Lineage, source, target string) string {
	t.Helper()
	out, err := ExportScopedUpstreamLineage(graph, source, target)
	if err != nil {
		t.Fatalf("ExportScopedUpstreamLineage(%q, %q): %v", source, target, err)
	}
	return out
}

// The worked example from the spec: source derives report directly, also
// reaches report through a, and reaches it through b and mid. a additionally
// depends on the independent source extra. source also derives orphan which
// cannot reach report, has an ancestor of its own (pre), and report derives
// down. Scoping to source and report keeps all three source -> report routes
// complete with their five nodes and six edges, and nothing else: extra and
// extra -> a, orphan, pre and down all stay out.
func TestExportScopedUpstreamLineageAllRoutes(t *testing.T) {
	graph := buildRegisteredGraph(t, [][]string{
		{"pre"},
		{"extra"},
		{"source", "pre"},
		{"b", "source"},
		{"a", "source", "extra"},
		{"mid", "b"},
		{"report", "source", "a", "mid"},
		{"orphan", "source"},
		{"down", "report"},
	})
	before := snapshotExportGraph(graph)

	out := mustExportScoped(t, graph, "source", "report")
	doc := parseExport(t, out)

	wantNodes := []string{"a", "b", "mid", "report", "source"}
	if !reflect.DeepEqual(doc.Nodes, wantNodes) {
		t.Errorf("nodes = %v, want %v", doc.Nodes, wantNodes)
	}
	wantEdges := [][2]string{
		{"a", "report"},
		{"b", "mid"},
		{"mid", "report"},
		{"source", "a"},
		{"source", "b"},
		{"source", "report"},
	}
	if got := exportEdgeNames(doc); !reflect.DeepEqual(got, wantEdges) {
		t.Errorf("edges = %v, want %v", got, wantEdges)
	}
	for _, excluded := range []string{"pre", "extra", "orphan", "down"} {
		for _, node := range doc.Nodes {
			if node == excluded {
				t.Errorf("nodes must not contain %q: %v", excluded, doc.Nodes)
			}
		}
	}
	for _, e := range doc.Edges {
		if e.From == "extra" || e.To == "extra" {
			t.Errorf("the independent source's edge must not appear: %+v", e)
		}
	}
	assertGraphUnchanged(t, before, graph)
}

// The exact JSON text is pinned, including the longer routes surviving the
// direct source -> report dependency.
func TestExportScopedUpstreamLineageExactJSON(t *testing.T) {
	graph := buildRegisteredGraph(t, [][]string{
		{"source"},
		{"b", "source"},
		{"a", "source"},
		{"mid", "b"},
		{"report", "source", "a", "mid"},
	})

	out := mustExportScoped(t, graph, "source", "report")
	want := `{"nodes":["a","b","mid","report","source"],` +
		`"edges":[{"from":"a","to":"report"},{"from":"b","to":"mid"},` +
		`{"from":"mid","to":"report"},{"from":"source","to":"a"},` +
		`{"from":"source","to":"b"},{"from":"source","to":"report"}]}`
	if out != want {
		t.Errorf("export =\n%s\nwant:\n%s", out, want)
	}
}

// A diamond shared by all routes contributes each node and edge exactly once.
func TestExportScopedUpstreamLineageDiamondDedupes(t *testing.T) {
	graph := buildRegisteredGraph(t, [][]string{
		{"source"},
		{"a", "source"},
		{"b", "source"},
		{"merge", "a", "b"},
		{"report", "merge"},
	})

	out := mustExportScoped(t, graph, "source", "report")
	doc := parseExport(t, out)
	wantNodes := []string{"a", "b", "merge", "report", "source"}
	if !reflect.DeepEqual(doc.Nodes, wantNodes) {
		t.Errorf("nodes = %v, want %v", doc.Nodes, wantNodes)
	}
	wantEdges := [][2]string{
		{"a", "merge"},
		{"b", "merge"},
		{"merge", "report"},
		{"source", "a"},
		{"source", "b"},
	}
	if got := exportEdgeNames(doc); !reflect.DeepEqual(got, wantEdges) {
		t.Errorf("edges = %v, want %v", got, wantEdges)
	}
}

// When the named source is a root of the target's whole ancestry, the scoped
// document is byte-identical to the full upstream export of the target.
func TestExportScopedUpstreamLineageMatchesFullFromRootSource(t *testing.T) {
	graph := buildRegisteredGraph(t, [][]string{
		{"raw"},
		{"a", "raw"},
		{"b", "raw"},
		{"mid", "b"},
		{"report", "raw", "a", "mid"},
		{"view", "report"},
	})

	full := mustExport(t, graph, "report")
	scoped := mustExportScoped(t, graph, "raw", "report")
	if full != scoped {
		t.Errorf("scoped from the root source =\n%s\nwant full export:\n%s", scoped, full)
	}
}

// Source and target being the same registered dataset outputs just that node
// and an empty (non-null) edges array.
func TestExportScopedUpstreamLineageSameEndpoint(t *testing.T) {
	graph := buildRegisteredGraph(t, [][]string{
		{"raw"},
		{"mid", "raw"},
		{"x", "mid"},
	})

	out := mustExportScoped(t, graph, "mid", "mid")
	if want := `{"nodes":["mid"],"edges":[]}`; out != want {
		t.Errorf("export = %s, want %s", out, want)
	}
	doc := parseExport(t, out)
	if doc.Edges == nil {
		t.Errorf("edges must decode as an empty array, got nil from %s", out)
	}
}

// Two registered datasets with no derivation route from source to target
// succeed with two empty arrays — no isolated endpoint survives. A reverse
// dependency does not count as reachability.
func TestExportScopedUpstreamLineageNoRoute(t *testing.T) {
	graph := buildRegisteredGraph(t, [][]string{
		{"root"},
		{"child", "root"}, // child ->(parent) root, so root cannot reach child
		{"lonely"},
	})
	before := snapshotExportGraph(graph)

	cases := []struct {
		name   string
		source string
		target string
	}{
		{"reverse direction is not a route", "child", "root"},
		{"unrelated datasets", "root", "lonely"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			out := mustExportScoped(t, graph, tc.source, tc.target)
			if want := `{"nodes":[],"edges":[]}`; out != want {
				t.Errorf("export = %s, want %s", out, want)
			}

			doc := parseExport(t, out)
			if doc.Nodes == nil {
				t.Errorf("nodes must decode as an empty array, got nil from %s", out)
			}
			if doc.Edges == nil {
				t.Errorf("edges must decode as an empty array, got nil from %s", out)
			}
		})
	}
	assertGraphUnchanged(t, before, graph)
}

// An intermediate dataset on a source -> target route is excluded entirely
// when it is only reachable from source along a branch that does not lead to
// target, even though it sits adjacent to a selected node.
func TestExportScopedUpstreamLineageDropsSideBranch(t *testing.T) {
	graph := buildRegisteredGraph(t, [][]string{
		{"source"},
		{"mid", "source"},
		{"sidestep", "mid"},
		{"report", "mid"},
	})

	out := mustExportScoped(t, graph, "source", "report")
	doc := parseExport(t, out)
	wantNodes := []string{"mid", "report", "source"}
	if !reflect.DeepEqual(doc.Nodes, wantNodes) {
		t.Errorf("nodes = %v, want %v", doc.Nodes, wantNodes)
	}
	for _, e := range doc.Edges {
		if e.From == "sidestep" || e.To == "sidestep" {
			t.Errorf("side branch edge must not appear: %+v", e)
		}
	}
}

// The same lineage built in different registration orders and with different
// upstream list orders exports byte-identical text.
func TestExportScopedUpstreamLineageOrderIndependent(t *testing.T) {
	build := func(extraFirst bool, reversed bool) map[string]*Lineage {
		regs := [][]string{
			{"extra"},
			{"source"},
			{"a", "source", "extra"},
			{"b", "source"},
			{"mid", "b"},
		}
		if reversed {
			regs = append(regs, []string{"report", "mid", "a", "source"})
		} else {
			regs = append(regs, []string{"report", "source", "a", "mid"})
		}
		if !extraFirst {
			regs[0], regs[1] = regs[1], regs[0]
		}
		return buildRegisteredGraph(t, regs)
	}

	first := build(true, false)
	second := build(false, true)
	if got, want := mustExportScoped(t, first, "source", "report"),
		mustExportScoped(t, second, "source", "report"); got != want {
		t.Errorf("registration/list order changed the export:\nfirst:  %s\nsecond: %s", got, want)
	}
}

// Names match by exact registered value: a name differing only in case is a
// different, unregistered dataset.
func TestExportScopedUpstreamLineageCaseSensitive(t *testing.T) {
	graph := buildRegisteredGraph(t, [][]string{
		{"source"},
		{"report", "source"},
	})

	out, err := ExportScopedUpstreamLineage(graph, "Source", "report")
	if err == nil {
		t.Fatalf("scoped export with wrong-case source succeeded with %q", out)
	}
	if !strings.Contains(err.Error(), "Source") {
		t.Errorf("error %q must name the queried source", err)
	}

	out, err = ExportScopedUpstreamLineage(graph, "source", "Report")
	if err == nil {
		t.Fatalf("scoped export with wrong-case target succeeded with %q", out)
	}
	if !strings.Contains(err.Error(), "Report") {
		t.Errorf("error %q must name the queried target", err)
	}
}

// source is validated before target: an empty or unregistered source is
// reported even when the target is equally bad, and an empty target is only
// reached once the source checks pass. Failures return an empty string.
func TestExportScopedUpstreamLineageValidationOrder(t *testing.T) {
	graph := buildRegisteredGraph(t, [][]string{
		{"source"},
		{"report", "source"},
	})
	before := snapshotExportGraph(graph)

	cases := []struct {
		name    string
		graph   map[string]*Lineage
		source  string
		target  string
		wantErr string
	}{
		{"empty source first", graph, "", "", "dataset name is required"},
		{"empty source, unregistered target", graph, "", "ghost", "dataset name is required"},
		{"unregistered source first", graph, "ghost", "ghost2", "dataset not found: ghost"},
		{"unregistered source, valid target", graph, "ghost", "report", "dataset not found: ghost"},
		{"valid source, empty target", graph, "source", "", "dataset name is required"},
		{"valid source, unregistered target", graph, "source", "ghost", "dataset not found: ghost"},
		{"nil graph", nil, "ghost", "report", "dataset not found: ghost"},
		{"empty graph", map[string]*Lineage{}, "", "report", "dataset name is required"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			out, err := ExportScopedUpstreamLineage(tc.graph, tc.source, tc.target)
			if err == nil {
				t.Fatalf("ExportScopedUpstreamLineage(%q, %q) succeeded with %q, want error %q",
					tc.source, tc.target, out, tc.wantErr)
			}
			if err.Error() != tc.wantErr {
				t.Errorf("error = %q, want %q", err, tc.wantErr)
			}
			if out != "" {
				t.Errorf("failed export returned partial content %q, want empty string", out)
			}
		})
	}
	assertGraphUnchanged(t, before, graph)
}

// An invalid UTF-8 name on a selected node fails the whole export and quotes
// the offending bytes; failures return no document.
func TestExportScopedUpstreamLineageRejectsInvalidUTF8OnRoute(t *testing.T) {
	bad := "mid\xff"
	graph := buildRegisteredGraph(t, [][]string{
		{"source"},
		{bad, "source"},
		{"report", bad},
	})
	before := snapshotExportGraph(graph)

	out, err := ExportScopedUpstreamLineage(graph, "source", "report")
	if err == nil {
		t.Fatalf("scoped export with an invalid selected node succeeded with %q", out)
	}
	if out != "" {
		t.Errorf("failed export returned partial content %q, want empty string", out)
	}
	if !strings.Contains(err.Error(), `"mid\xff"`) {
		t.Errorf("error %q must quote the original bytes of the selected node", err)
	}
	assertGraphUnchanged(t, before, graph)
}

// Invalid names on unselected nodes never block the scoped export: an
// ancestor of source, an independent side input, and a downstream branch that
// cannot reach target may all hold invalid bytes while the route document
// exports cleanly.
func TestExportScopedUpstreamLineageIgnoresInvalidUTF8OutsideSelection(t *testing.T) {
	ancestorBad := "pre\xff"
	sideBad := "extra\xfe"
	branchBad := "orphan\xfd"
	graph := buildRegisteredGraph(t, [][]string{
		{ancestorBad},
		{sideBad},
		{"source", ancestorBad},
		{"a", "source", sideBad},
		{"report", "a"},
		{branchBad, "source"},
	})
	before := snapshotExportGraph(graph)

	out := mustExportScoped(t, graph, "source", "report")
	doc := parseExport(t, out)
	wantNodes := []string{"a", "report", "source"}
	if !reflect.DeepEqual(doc.Nodes, wantNodes) {
		t.Errorf("nodes = %q, want %q", doc.Nodes, wantNodes)
	}
	wantEdges := [][2]string{{"a", "report"}, {"source", "a"}}
	if got := exportEdgeNames(doc); !reflect.DeepEqual(got, wantEdges) {
		t.Errorf("edges = %q, want %q", got, wantEdges)
	}
	assertGraphUnchanged(t, before, graph)
}

// When no route exists, invalid names on neither endpoint's surroundings can
// block the successful empty document.
func TestExportScopedUpstreamLineageEmptyResultSkipsEncoding(t *testing.T) {
	graph := buildRegisteredGraph(t, [][]string{
		{"source"},
		{"bad\xff", "source"}, // reachable from source, cannot reach target
		{"target"},
	})

	out := mustExportScoped(t, graph, "source", "target")
	if want := `{"nodes":[],"edges":[]}`; out != want {
		t.Errorf("export = %s, want %s", out, want)
	}
}

// An invalid UTF-8 name on an endpoint itself fails: the endpoint is selected
// whenever the two registered names coincide (the only route of length zero).
func TestExportScopedUpstreamLineageRejectsInvalidUTF8SameEndpoint(t *testing.T) {
	bad := "x\xff"
	graph := buildRegisteredGraph(t, [][]string{{bad}})

	out, err := ExportScopedUpstreamLineage(graph, bad, bad)
	if err == nil {
		t.Fatalf("scoped export with an invalid endpoint succeeded with %q", out)
	}
	if out != "" {
		t.Errorf("failed export returned partial content %q, want empty string", out)
	}
	if !strings.Contains(err.Error(), `\xff`) {
		t.Errorf("error %q must show the bad byte", err)
	}
}

// Endpoint existence checks keep priority over encoding validation.
func TestExportScopedUpstreamLineageErrorsPrecedence(t *testing.T) {
	graph := buildRegisteredGraph(t, [][]string{{"bad\xff"}})
	for _, tc := range []struct {
		source  string
		target  string
		wantErr string
	}{
		{"", "bad\xff", "dataset name is required"},
		{"ghost", "bad\xff", "dataset not found: ghost"},
		{"bad\xff", "", "dataset name is required"},
		{"bad\xff", "ghost", "dataset not found: ghost"},
	} {
		out, err := ExportScopedUpstreamLineage(graph, tc.source, tc.target)
		if err == nil {
			t.Fatalf("ExportScopedUpstreamLineage(%q, %q) succeeded with %q", tc.source, tc.target, out)
		}
		if err.Error() != tc.wantErr {
			t.Errorf("(%q, %q): error = %q, want %q", tc.source, tc.target, err, tc.wantErr)
		}
		if out != "" {
			t.Errorf("failed export returned partial content %q", out)
		}
	}
}

// Successful and failed scoped exports, and the existing full export, leave
// the graph exactly as it was: nodes, both edge directions and list orders.
func TestExportScopedUpstreamLineageReadOnly(t *testing.T) {
	graph := buildRegisteredGraph(t, [][]string{
		{"extra"},
		{"source"},
		{"a", "source", "extra"},
		{"b", "source"},
		{"mid", "b"},
		{"report", "source", "a", "mid"},
		{"orphan", "source"},
	})
	before := snapshotExportGraph(graph)

	mustExportScoped(t, graph, "source", "report")
	mustExportScoped(t, graph, "source", "source")
	mustExportScoped(t, graph, "a", "source") // no route: empty document
	mustExport(t, graph, "report")
	if _, err := ExportScopedUpstreamLineage(graph, "ghost", "report"); err == nil {
		t.Fatal("expected a not-found error for the source")
	}
	if _, err := ExportScopedUpstreamLineage(graph, "source", "ghost"); err == nil {
		t.Fatal("expected a not-found error for the target")
	}
	assertGraphUnchanged(t, before, graph)
}
