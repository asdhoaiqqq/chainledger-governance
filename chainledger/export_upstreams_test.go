package chainledger

import (
	"encoding/json"
	"reflect"
	"testing"
)

// decodeExport parses an export document, expecting edges to decode to nil-free
// slices so callers can compare against []string literals directly.
func decodeExport(t *testing.T, text string) (map[string]bool, []edge) {
	t.Helper()
	var doc struct {
		Nodes []string `json:"nodes"`
		Edges []edge   `json:"edges"`
	}
	if err := json.Unmarshal([]byte(text), &doc); err != nil {
		t.Fatalf("export is not valid JSON %q: %v", text, err)
	}
	nodes := map[string]bool{}
	for _, name := range doc.Nodes {
		if nodes[name] {
			t.Fatalf("node %q listed more than once", name)
		}
		nodes[name] = true
	}
	seen := map[edge]bool{}
	for _, e := range doc.Edges {
		if seen[e] {
			t.Fatalf("edge %v listed more than once", e)
		}
		seen[e] = true
	}
	return nodes, doc.Edges
}

type edge struct {
	From string `json:"from"`
	To   string `json:"to"`
}

// The spec scenario: raw derives a and b, b derives mid, and report directly
// depends on raw, a and mid; report also derives view, and isolated is an
// independent dataset. The report export keeps all five upstream-lineage
// nodes and all six direct dependencies between them — including the longer
// branches through a and through b/mid despite the direct raw->report edge.
func TestExportUpstreamsKeepsEveryBranch(t *testing.T) {
	graph := map[string]*Lineage{}
	mustRegister(t, graph, "raw")
	mustRegister(t, graph, "a", "raw")
	mustRegister(t, graph, "b", "raw")
	mustRegister(t, graph, "mid", "b")
	mustRegister(t, graph, "report", "raw", "a", "mid")
	mustRegister(t, graph, "view", "report")
	mustRegister(t, graph, "isolated")

	text, err := ExportUpstreams(graph, "report")
	if err != nil {
		t.Fatalf("ExportUpstreams: %v", err)
	}

	nodes, edges := decodeExport(t, text)
	wantNodes := map[string]bool{
		"raw": true, "a": true, "b": true, "mid": true, "report": true,
	}
	if !reflect.DeepEqual(nodes, wantNodes) {
		t.Errorf("nodes = %v, want %v", nodes, wantNodes)
	}
	wantEdges := []edge{
		{From: "a", To: "report"},
		{From: "b", To: "mid"},
		{From: "mid", To: "report"},
		{From: "raw", To: "a"},
		{From: "raw", To: "b"},
		{From: "raw", To: "report"},
	}
	if !reflect.DeepEqual(edges, wantEdges) {
		t.Errorf("edges = %v, want %v", edges, wantEdges)
	}
}

// A target with no upstreams exports just itself and an explicit empty JSON
// edge array — never null.
func TestExportUpstreamsNoUpstreams(t *testing.T) {
	graph := map[string]*Lineage{}
	mustRegister(t, graph, "lonely")
	mustRegister(t, graph, "other")

	text, err := ExportUpstreams(graph, "lonely")
	if err != nil {
		t.Fatalf("ExportUpstreams: %v", err)
	}
	if got, want := text, `{"nodes":["lonely"],"edges":[]}`; got != want {
		t.Errorf("export = %s, want %s", got, want)
	}
}

// Failures return no output at all, matching the other queries' error text.
func TestExportUpstreamsErrors(t *testing.T) {
	graph := map[string]*Lineage{}
	mustRegister(t, graph, "raw")

	cases := []struct {
		name      string
		graph     map[string]*Lineage
		target    string
		wantError string
	}{
		{"empty target", graph, "", "dataset name is required"},
		{"unknown target", graph, "ghost", "dataset not found: ghost"},
		{"empty graph", map[string]*Lineage{}, "raw", "dataset not found: raw"},
		{"nil graph", nil, "raw", "dataset not found: raw"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			text, err := ExportUpstreams(tc.graph, tc.target)
			if err == nil || err.Error() != tc.wantError {
				t.Fatalf("error = %v, want %q", err, tc.wantError)
			}
			if text != "" {
				t.Errorf("failed export returned partial text %q", text)
			}
		})
	}
}

// Output must not depend on registration order or on the order a target lists
// its direct upstreams: every build of the same lineage exports byte-identical
// JSON, with nodes and edges in Go string order.
func TestExportUpstreamsOrderIsStable(t *testing.T) {
	build := func() map[string]*Lineage {
		g := map[string]*Lineage{}
		mustRegister(t, g, "zeta")
		mustRegister(t, g, "alpha")
		mustRegister(t, g, "mid", "zeta", "alpha")
		mustRegister(t, g, "report", "mid", "zeta")
		return g
	}

	want := `{"nodes":["alpha","mid","report","zeta"],"edges":[{"from":"alpha","to":"mid"},{"from":"mid","to":"report"},{"from":"zeta","to":"mid"},{"from":"zeta","to":"report"}]}`
	var first string
	for i := 0; i < 5; i++ {
		text, err := ExportUpstreams(build(), "report")
		if err != nil {
			t.Fatalf("ExportUpstreams iteration %d: %v", i, err)
		}
		if i == 0 {
			first = text
			if text != want {
				t.Errorf("export = %s, want %s", text, want)
			}
			continue
		}
		if text != first {
			t.Fatalf("export differs across runs:\n%s\n%s", first, text)
		}
	}

	// Re-registering report with its direct upstreams in the opposite order
	// must leave the export unchanged.
	graph := build()
	mustRegister(t, graph, "report", "zeta", "mid")
	text, err := ExportUpstreams(graph, "report")
	if err != nil {
		t.Fatalf("ExportUpstreams after re-register: %v", err)
	}
	if text != want {
		t.Errorf("export = %s, want %s", text, want)
	}
}

// Exporting an intermediate dataset excludes everything above only the
// branches it does not use and everything downstream of it.
func TestExportUpstreamsExcludesUnrelatedSidesAndDownstreams(t *testing.T) {
	graph := map[string]*Lineage{}
	mustRegister(t, graph, "raw")
	mustRegister(t, graph, "unused-branch", "raw")
	mustRegister(t, graph, "used", "raw")
	mustRegister(t, graph, "mid", "used")
	mustRegister(t, graph, "report", "mid")
	mustRegister(t, graph, "view", "report")
	mustRegister(t, graph, "lonely")

	text, err := ExportUpstreams(graph, "mid")
	if err != nil {
		t.Fatalf("ExportUpstreams: %v", err)
	}
	nodes, edges := decodeExport(t, text)
	wantNodes := map[string]bool{"raw": true, "used": true, "mid": true}
	if !reflect.DeepEqual(nodes, wantNodes) {
		t.Errorf("nodes = %v, want %v", nodes, wantNodes)
	}
	wantEdges := []edge{
		{From: "raw", To: "used"},
		{From: "used", To: "mid"},
	}
	if !reflect.DeepEqual(edges, wantEdges) {
		t.Errorf("edges = %v, want %v", edges, wantEdges)
	}
}

// Names are matched exactly and exported verbatim: case matters, and quotes,
// backslashes and newlines survive a JSON round trip.
func TestExportUpstreamsPreservesNames(t *testing.T) {
	graph := map[string]*Lineage{}
	rawName := `quo"te\back` + "\nline"
	mustRegister(t, graph, rawName)
	mustRegister(t, graph, "Report", rawName)
	mustRegister(t, graph, "report", rawName)

	if _, err := ExportUpstreams(graph, "REPORT"); err == nil {
		t.Fatal("case-different target unexpectedly found")
	} else if err.Error() != "dataset not found: REPORT" {
		t.Fatalf("error = %v, want case-sensitive not-found", err)
	}

	text, err := ExportUpstreams(graph, "Report")
	if err != nil {
		t.Fatalf("ExportUpstreams: %v", err)
	}
	nodes, edges := decodeExport(t, text)
	if !nodes[rawName] || !nodes["Report"] {
		t.Errorf("nodes = %v, want exact names preserved", nodes)
	}
	if nodes["report"] {
		t.Errorf("case-different dataset %q must not appear", "report")
	}
	wantEdges := []edge{{From: rawName, To: "Report"}}
	if !reflect.DeepEqual(edges, wantEdges) {
		t.Errorf("edges = %v, want %v", edges, wantEdges)
	}
}

// The export is read-only: nodes, bidirectional relationships and stored list
// order are all byte-for-byte the same afterward.
func TestExportUpstreamsIsReadOnly(t *testing.T) {
	graph := map[string]*Lineage{}
	mustRegister(t, graph, "raw")
	mustRegister(t, graph, "b", "raw")
	mustRegister(t, graph, "a", "raw")
	mustRegister(t, graph, "report", "b", "a", "raw")
	mustRegister(t, graph, "view", "report")

	before := snapshot(graph)
	if _, err := ExportUpstreams(graph, "report"); err != nil {
		t.Fatalf("ExportUpstreams: %v", err)
	}
	if _, err := ExportUpstreams(graph, "raw"); err != nil {
		t.Fatalf("ExportUpstreams root: %v", err)
	}
	if got := snapshot(graph); !reflect.DeepEqual(got, before) {
		t.Fatalf("export changed the graph:\nbefore=%v\nafter =%v", before, got)
	}
}

// The JSON document always carries both top-level fields in the fixed order
// nodes then edges, even when either side is trivial.
func TestExportUpstreamsJSONShape(t *testing.T) {
	graph := map[string]*Lineage{}
	mustRegister(t, graph, "raw")
	text, err := ExportUpstreams(graph, "raw")
	if err != nil {
		t.Fatalf("ExportUpstreams: %v", err)
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal([]byte(text), &fields); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}
	if _, ok := fields["nodes"]; !ok {
		t.Errorf("missing nodes field in %s", text)
	}
	if _, ok := fields["edges"]; !ok {
		t.Errorf("missing edges field in %s", text)
	}
}
