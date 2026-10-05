package chainledger

import (
	"encoding/json"
	"reflect"
	"sort"
	"strings"
	"testing"
)

// exportDocument mirrors the exported JSON shape for assertions.
type exportDocument struct {
	Nodes []string `json:"nodes"`
	Edges []struct {
		From string `json:"from"`
		To   string `json:"to"`
	} `json:"edges"`
}

func mustExport(t *testing.T, graph map[string]*Lineage, target string) string {
	t.Helper()
	out, err := ExportUpstreamLineage(graph, target)
	if err != nil {
		t.Fatalf("ExportUpstreamLineage(%q): %v", target, err)
	}
	return out
}

func parseExport(t *testing.T, text string) exportDocument {
	t.Helper()
	var doc exportDocument
	if err := json.Unmarshal([]byte(text), &doc); err != nil {
		t.Fatalf("exported text is not valid JSON: %v\ntext: %s", err, text)
	}
	return doc
}

func exportEdgeNames(doc exportDocument) [][2]string {
	edges := make([][2]string, len(doc.Edges))
	for i, e := range doc.Edges {
		edges[i] = [2]string{e.From, e.To}
	}
	return edges
}

// snapshotExportGraph deep-copies the graph so a read-only export can be
// checked against the exact prior state, lists included. Nil and empty lists
// are kept distinct, matching how Register stores them.
func snapshotExportGraph(graph map[string]*Lineage) map[string]*Lineage {
	copyList := func(in []string) []string {
		if in == nil {
			return nil
		}
		out := make([]string, len(in))
		copy(out, in)
		return out
	}
	copied := make(map[string]*Lineage, len(graph))
	for name, entry := range graph {
		copied[name] = &Lineage{
			Dataset:  entry.Dataset,
			Parents:  copyList(entry.Parents),
			Children: copyList(entry.Children),
		}
	}
	return copied
}

func assertGraphUnchanged(t *testing.T, before, after map[string]*Lineage) {
	t.Helper()
	if !reflect.DeepEqual(before, after) {
		t.Errorf("export mutated the graph:\nbefore: %v\nafter:  %v", before, after)
	}
}

// The worked example from the spec: raw derives a and b, b derives mid, and
// report depends directly on raw, a and mid; report derives view and isolated
// stands alone. Exporting report keeps all five nodes and all six direct
// dependencies among them — including the longer raw -> a -> report and
// raw -> b -> mid -> report branches even though raw -> report exists —
// while view and isolated stay out.
func TestExportUpstreamLineageFullBranches(t *testing.T) {
	graph := buildRegisteredGraph(t, [][]string{
		{"raw"},
		{"b", "raw"},
		{"a", "raw"},
		{"mid", "b"},
		{"report", "raw", "a", "mid"},
		{"view", "report"},
		{"isolated"},
	})
	before := snapshotExportGraph(graph)

	out := mustExport(t, graph, "report")
	doc := parseExport(t, out)

	wantNodes := []string{"a", "b", "mid", "raw", "report"}
	if !reflect.DeepEqual(doc.Nodes, wantNodes) {
		t.Errorf("nodes = %v, want %v", doc.Nodes, wantNodes)
	}
	wantEdges := [][2]string{
		{"a", "report"},
		{"b", "mid"},
		{"mid", "report"},
		{"raw", "a"},
		{"raw", "b"},
		{"raw", "report"},
	}
	if got := exportEdgeNames(doc); !reflect.DeepEqual(got, wantEdges) {
		t.Errorf("edges = %v, want %v", got, wantEdges)
	}
	for _, excluded := range []string{"view", "isolated"} {
		for _, node := range doc.Nodes {
			if node == excluded {
				t.Errorf("nodes must not contain %q: %v", excluded, doc.Nodes)
			}
		}
	}
	assertGraphUnchanged(t, before, graph)
}

// The exact JSON text is pinned: top-level keys, edge direction written
// upstream -> derived, and both orderings fully determined by the names.
func TestExportUpstreamLineageExactJSON(t *testing.T) {
	graph := buildRegisteredGraph(t, [][]string{
		{"raw"},
		{"b", "raw"},
		{"a", "raw"},
		{"mid", "b"},
		{"report", "raw", "a", "mid"},
		{"view", "report"},
		{"isolated"},
	})

	out := mustExport(t, graph, "report")
	want := `{"nodes":["a","b","mid","raw","report"],` +
		`"edges":[{"from":"a","to":"report"},{"from":"b","to":"mid"},` +
		`{"from":"mid","to":"report"},{"from":"raw","to":"a"},` +
		`{"from":"raw","to":"b"},{"from":"raw","to":"report"}]}`
	if out != want {
		t.Errorf("export =\n%s\nwant:\n%s", out, want)
	}
}

// A registered target with no upstreams exports successfully: it is the only
// node and the edges array is a JSON empty array, not null.
func TestExportUpstreamLineageNoUpstreams(t *testing.T) {
	graph := buildRegisteredGraph(t, [][]string{
		{"root"},
		{"other"},
	})

	out := mustExport(t, graph, "root")
	if want := `{"nodes":["root"],"edges":[]}`; out != want {
		t.Errorf("export = %s, want %s", out, want)
	}
	doc := parseExport(t, out)
	if doc.Edges == nil {
		t.Errorf("edges must decode as an empty array, got nil from %s", out)
	}
}

// The same lineage built in different registration orders and with different
// upstream list orders exports byte-identical text.
func TestExportUpstreamLineageOrderIndependent(t *testing.T) {
	first := buildRegisteredGraph(t, [][]string{
		{"raw"},
		{"b", "raw"},
		{"a", "raw"},
		{"mid", "b"},
		{"report", "raw", "a", "mid"},
		{"view", "report"},
		{"isolated"},
	})
	second := buildRegisteredGraph(t, [][]string{
		{"isolated"},
		{"raw"},
		{"a", "raw"},
		{"b", "raw"},
		{"mid", "b"},
		{"report", "mid", "raw", "a"},
		{"view", "report"},
	})

	if got, want := mustExport(t, first, "report"), mustExport(t, second, "report"); got != want {
		t.Errorf("registration order changed the export:\nfirst:  %s\nsecond: %s", got, want)
	}
}

// Names are written verbatim with correct JSON escaping: quotes, backslashes
// and newlines survive a round trip back to the original bytes.
func TestExportUpstreamLineageEscapesNames(t *testing.T) {
	quote := `say "hi"`
	slash := `a\b`
	newline := "line1\nline2"
	graph := buildRegisteredGraph(t, [][]string{
		{quote},
		{slash, quote},
		{newline, slash},
	})

	out := mustExport(t, graph, newline)
	doc := parseExport(t, out)
	wantNodes := []string{quote, slash, newline}
	sort.Strings(wantNodes) // the export's own ordering rule
	if !reflect.DeepEqual(doc.Nodes, wantNodes) {
		t.Errorf("nodes = %q, want %q", doc.Nodes, wantNodes)
	}
	wantEdges := [][2]string{{slash, newline}, {quote, slash}} // ordered by from, then to
	if got := exportEdgeNames(doc); !reflect.DeepEqual(got, wantEdges) {
		t.Errorf("edges = %q, want %q", got, wantEdges)
	}
}

// Names match by exact registered value: a name differing only in case is a
// different, unregistered dataset.
func TestExportUpstreamLineageCaseSensitive(t *testing.T) {
	graph := buildRegisteredGraph(t, [][]string{{"report"}})

	out, err := ExportUpstreamLineage(graph, "Report")
	if err == nil {
		t.Fatalf("ExportUpstreamLineage(%q) succeeded with %q, want a not-found error", "Report", out)
	}
	if !strings.Contains(err.Error(), "Report") {
		t.Errorf("error %q must name the queried dataset", err)
	}
}

// Failures: an empty target is a missing-name error; an unregistered target
// is an error naming it, also against empty and nil graphs. No failure returns
// partial export content.
func TestExportUpstreamLineageFailures(t *testing.T) {
	graph := buildRegisteredGraph(t, [][]string{
		{"raw"},
		{"report", "raw"},
	})
	before := snapshotExportGraph(graph)

	cases := []struct {
		name    string
		graph   map[string]*Lineage
		target  string
		wantErr string
	}{
		{"empty target", graph, "", "dataset name is required"},
		{"unregistered target", graph, "ghost", "dataset not found: ghost"},
		{"empty graph", map[string]*Lineage{}, "ghost", "dataset not found: ghost"},
		{"nil graph", nil, "ghost", "dataset not found: ghost"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			out, err := ExportUpstreamLineage(tc.graph, tc.target)
			if err == nil {
				t.Fatalf("ExportUpstreamLineage(%q) succeeded with %q, want error %q",
					tc.target, out, tc.wantErr)
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

// A failed export leaves the graph exactly as it was, and a successful one
// does too — checked over nodes, both edge directions and list orders.
func TestExportUpstreamLineageReadOnly(t *testing.T) {
	graph := buildRegisteredGraph(t, [][]string{
		{"raw"},
		{"a", "raw"},
		{"b", "raw"},
		{"mid", "b"},
		{"report", "raw", "a", "mid"},
		{"view", "report"},
	})
	before := snapshotExportGraph(graph)

	mustExport(t, graph, "report")
	mustExport(t, graph, "view")
	if _, err := ExportUpstreamLineage(graph, "ghost"); err == nil {
		t.Fatal("expected a not-found error")
	}
	assertGraphUnchanged(t, before, graph)
}
