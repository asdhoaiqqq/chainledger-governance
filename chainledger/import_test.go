package chainledger

import (
	"encoding/json"
	"reflect"
	"sort"
	"strings"
	"testing"
)

// mustImportLineage imports a document or fails the test.
func mustImportLineage(t *testing.T, text string) map[string]*Lineage {
	t.Helper()
	graph, err := ImportLineage(text)
	if err != nil {
		t.Fatalf("ImportLineage(%s): %v", text, err)
	}
	return graph
}

// lineageDoc builds a document from nodes and edges using the export shape, so
// test names keep their exact bytes (including escapes) through JSON.
func lineageDoc(t *testing.T, nodes []string, edges [][2]string) string {
	t.Helper()
	doc := exportDocument{Nodes: nodes}
	for _, e := range edges {
		doc.Edges = append(doc.Edges, struct {
			From string `json:"from"`
			To   string `json:"to"`
		}{From: e[0], To: e[1]})
	}
	data, err := json.Marshal(doc)
	if err != nil {
		t.Fatalf("marshal document: %v", err)
	}
	return string(data)
}

// adjacency is one imported node's direct up- and downstream lists.
type adjacency struct {
	parents  []string
	children []string
}

func graphAdjacency(graph map[string]*Lineage) map[string]adjacency {
	got := make(map[string]adjacency, len(graph))
	for name, entry := range graph {
		got[name] = adjacency{
			parents:  append([]string(nil), entry.Parents...),
			children: append([]string(nil), entry.Children...),
		}
	}
	return got
}

// Importing a document with isolated nodes, a chain and a merge materializes
// every listed node, mirrors each direct dependency in both directions, and
// stores both adjacency lists in Go string order regardless of document order.
func TestImportLineageBuildsBidirectionalGraph(t *testing.T) {
	// Deliberately unsorted: nodes shuffled and edges presented child-first.
	text := lineageDoc(t,
		[]string{"report", "solo", "raw", "mid", "b", "a"},
		[][2]string{
			{"a", "report"},
			{"raw", "a"},
			{"mid", "report"},
			{"b", "mid"},
			{"raw", "b"},
		})

	graph := mustImportLineage(t, text)

	if got, want := len(graph), 6; got != want {
		t.Fatalf("imported %d nodes, want %d: %v", got, want, graph)
	}
	want := map[string]adjacency{
		"raw":    {children: []string{"a", "b"}},
		"a":      {parents: []string{"raw"}, children: []string{"report"}},
		"b":      {parents: []string{"raw"}, children: []string{"mid"}},
		"mid":    {parents: []string{"b"}, children: []string{"report"}},
		"report": {parents: []string{"a", "mid"}},
		"solo":   {},
	}
	if got := graphAdjacency(graph); !reflect.DeepEqual(got, want) {
		t.Errorf("adjacency = %v, want %v", got, want)
	}
	for name, entry := range graph {
		if entry.Dataset != name {
			t.Errorf("entry %q has Dataset %q", name, entry.Dataset)
		}
	}
}

// The imported graph answers the existing queries directly: distances,
// tie-broken paths and ordering all come from the shared query rules.
func TestImportLineageGraphIsQueryable(t *testing.T) {
	text := lineageDoc(t,
		[]string{"source", "a", "b", "z", "c", "report", "view", "solo"},
		[][2]string{
			{"source", "a"},
			{"source", "b"},
			{"a", "z"},
			{"b", "c"},
			{"z", "report"},
			{"c", "report"},
			{"report", "view"},
		})
	graph := mustImportLineage(t, text)

	impacts, err := Impacts(graph, "source")
	if err != nil {
		t.Fatalf("Impacts on imported graph: %v", err)
	}
	wantImpacts := []Impact{
		{Dataset: "a", Distance: 1, Path: []string{"source", "a"}},
		{Dataset: "b", Distance: 1, Path: []string{"source", "b"}},
		{Dataset: "c", Distance: 2, Path: []string{"source", "b", "c"}},
		{Dataset: "z", Distance: 2, Path: []string{"source", "a", "z"}},
		{Dataset: "report", Distance: 3, Path: []string{"source", "a", "z", "report"}},
		{Dataset: "view", Distance: 4, Path: []string{"source", "a", "z", "report", "view"}},
	}
	if !reflect.DeepEqual(impacts, wantImpacts) {
		t.Errorf("Impacts = %v, want %v", impacts, wantImpacts)
	}

	upstreams, err := Upstreams(graph, "report")
	if err != nil {
		t.Fatalf("Upstreams: %v", err)
	}
	if got := upstreams[0]; got.Dataset != "c" || got.Distance != 1 ||
		!reflect.DeepEqual(got.Path, []string{"c", "report"}) {
		t.Errorf("nearest upstream = %+v, want c at distance 1", got)
	}

	common, err := CommonUpstreams(graph, "z", "c")
	if err != nil {
		t.Fatalf("CommonUpstreams: %v", err)
	}
	if len(common) != 1 || common[0].Dataset != "source" {
		t.Errorf("common upstreams = %+v, want just source", common)
	}

	// An isolated listed node is queryable as a registered dataset with no
	// relations in either direction.
	if got, err := Impacts(graph, "solo"); err != nil || len(got) != 0 {
		t.Errorf("Impacts(solo) = %v, %v; want empty success", got, err)
	}
	if got, err := Upstreams(graph, "solo"); err != nil || len(got) != 0 {
		t.Errorf("Upstreams(solo) = %v, %v; want empty success", got, err)
	}
}

// A direct relation and the longer branches through intermediates survive
// together, matching how the exporters emit full branch sets.
func TestImportLineageKeepsDirectAndLongBranches(t *testing.T) {
	text := lineageDoc(t,
		[]string{"raw", "a", "b", "mid", "report"},
		[][2]string{
			{"raw", "report"},
			{"raw", "a"},
			{"a", "report"},
			{"raw", "b"},
			{"b", "mid"},
			{"mid", "report"},
		})
	graph := mustImportLineage(t, text)

	entry := graph["report"]
	wantParents := []string{"a", "mid", "raw"}
	if !reflect.DeepEqual(entry.Parents, wantParents) {
		t.Errorf("report parents = %v, want %v", entry.Parents, wantParents)
	}
	upstreams, err := Upstreams(graph, "report")
	if err != nil {
		t.Fatalf("Upstreams: %v", err)
	}
	// raw, a and mid are all DIRECT upstreams of report; the longer branch via
	// mid still keeps b reachable at distance 2.
	byName := map[string]Upstream{}
	for _, up := range upstreams {
		byName[up.Dataset] = up
	}
	for name, wantDist := range map[string]int{"raw": 1, "a": 1, "mid": 1, "b": 2} {
		if got, ok := byName[name]; !ok || got.Distance != wantDist {
			t.Errorf("upstream %s = %+v, ok=%v, want distance %d", name, got, ok, wantDist)
		}
	}
}

// Export -> import -> export of a full upstream export reproduces byte-identical
// text; a source-scoped export does the same with the same endpoints.
func TestImportLineageExportRoundTripByteIdentical(t *testing.T) {
	graph := buildRegisteredGraph(t, [][]string{
		{"pre"},
		{"extra"},
		{"lone"},
		{"source", "pre"},
		{"a", "source", "extra"},
		{"b", "source"},
		{"mid", "b"},
		{"sidetrack", "source"},
		{"report", "source", "a", "mid", "lone"},
		{"view", "report"},
		{"isolated"},
	})

	full := mustExport(t, graph, "report")
	importedFull := mustImportLineage(t, full)
	if again, err := ExportUpstreamLineage(importedFull, "report"); err != nil || again != full {
		t.Errorf("full export round trip:\nfirst:  %s\nsecond: %v %v", full, again, err)
	}

	scoped, err := ExportSourceTargetLineage(graph, "source", "report")
	if err != nil {
		t.Fatalf("ExportSourceTargetLineage: %v", err)
	}
	importedScoped := mustImportLineage(t, scoped)
	if again, err := ExportSourceTargetLineage(importedScoped, "source", "report"); err != nil || again != scoped {
		t.Errorf("scoped export round trip:\nfirst:  %s\nsecond: %v %v", scoped, again, err)
	}

	// The scoped import carries exactly the partial lineage: excluded sources
	// and downstreams are not reconstructed.
	for _, absent := range []string{"pre", "extra", "lone", "sidetrack", "view", "isolated"} {
		if _, ok := importedScoped[absent]; ok {
			t.Errorf("scoped import must not reconstruct %q", absent)
		}
	}
	if got, want := len(importedScoped), 5; got != want {
		t.Errorf("scoped import has %d nodes, want %d", got, want)
	}
	// The full upstream export of target from the scoped graph equals the
	// scoped document itself: only source's routes exist there.
	fromScoped, err := ExportUpstreamLineage(importedScoped, "report")
	if err != nil {
		t.Fatalf("ExportUpstreamLineage on scoped import: %v", err)
	}
	if fromScoped != scoped {
		t.Errorf("scoped graph full export =\n%s\nwant\n%s", fromScoped, scoped)
	}
}

// Two empty arrays are a successful empty graph that accepts registrations;
// harmless whitespace around the document is accepted.
func TestImportLineageEmptyArrays(t *testing.T) {
	for _, text := range []string{
		`{"nodes":[],"edges":[]}`,
		"{\n  \"nodes\": [],\n  \"edges\": []\n}\n",
		` { "nodes" : [ ] , "edges" : [ ] } `,
	} {
		graph, err := ImportLineage(text)
		if err != nil {
			t.Fatalf("ImportLineage(%q): %v", text, err)
		}
		if graph == nil {
			t.Fatalf("empty import returned nil map")
		}
		if len(graph) != 0 {
			t.Fatalf("empty import has %d nodes", len(graph))
		}
		if err := Register(graph, Dataset{Name: "raw"}, nil); err != nil {
			t.Fatalf("Register into imported empty graph: %v", err)
		}
		if err := Register(graph, Dataset{Name: "detail"}, []string{"raw"}); err != nil {
			t.Fatalf("Register detail: %v", err)
		}
		if got, err := Impacts(graph, "raw"); err != nil || len(got) != 1 || got[0].Dataset != "detail" {
			t.Errorf("post-import query = %v, %v", got, err)
		}
	}
}

// Node and edge arrangement in the document cannot change the imported graph;
// duplicate names and duplicate direct dependencies collapse to one.
func TestImportLineageOrderAndDeduplication(t *testing.T) {
	first := lineageDoc(t,
		[]string{"raw", "a", "report", "solo"},
		[][2]string{{"raw", "a"}, {"a", "report"}})
	second := lineageDoc(t,
		[]string{"report", "raw", "solo", "raw", "a", "report"},
		[][2]string{
			{"a", "report"},
			{"a", "report"},
			{"raw", "a"},
			{"raw", "a"},
		})

	g1 := mustImportLineage(t, first)
	g2 := mustImportLineage(t, second)
	if !reflect.DeepEqual(graphAdjacency(g1), graphAdjacency(g2)) {
		t.Errorf("document order/dedup changed the graph:\n%v\n%v",
			graphAdjacency(g1), graphAdjacency(g2))
	}
	if got, want := len(g2), 4; got != want {
		t.Errorf("deduped graph has %d nodes, want %d", got, want)
	}
}

// Names are the exact JSON-decoded values: case, surrounding spaces, Chinese,
// quotes, backslashes and newlines are all preserved verbatim.
func TestImportLineageExactNames(t *testing.T) {
	quote := `say "hi"`
	slash := `a\b`
	newline := "line1\nline2"
	spaced := "  边缘  "
	upper := "Report"
	lower := "report"
	text := lineageDoc(t,
		[]string{quote, slash, newline, spaced, upper, lower},
		[][2]string{
			{quote, slash},
			{slash, newline},
			{spaced, newline},
			{upper, lower},
		})
	graph := mustImportLineage(t, text)

	for _, name := range []string{quote, slash, newline, spaced, upper, lower} {
		if _, ok := graph[name]; !ok {
			t.Errorf("imported graph missing exact name %q", name)
		}
	}
	// A name differing only in case is neither of the listed datasets.
	if _, ok := graph["REPORT"]; ok {
		t.Errorf("case must be preserved exactly: REPORT must not resolve")
	}
	// ' ' (0x20) sorts before 'a' in Go string order, so the spaced Chinese
	// name leads newline's sorted direct-upstream list.
	if got := graph[newline].Parents; !reflect.DeepEqual(got, []string{spaced, slash}) {
		t.Errorf("newline node parents = %q, want verbatim upstream names in Go string order", got)
	}
	// Re-exporting newline's upstream closure recovers the bytes through JSON
	// escaping; the unrelated Report/report pair is not part of that closure.
	out := mustExport(t, graph, newline)
	var doc exportDocument
	if err := json.Unmarshal([]byte(out), &doc); err != nil {
		t.Fatalf("re-export is not JSON: %v", err)
	}
	sort.Strings(doc.Nodes)
	wantNodes := []string{slash, newline, quote, spaced}
	sort.Strings(wantNodes)
	if !reflect.DeepEqual(doc.Nodes, wantNodes) {
		t.Errorf("round-tripped nodes = %q, want %q", doc.Nodes, wantNodes)
	}
}

// Every malformed-document condition fails with a nil graph and a document
// error; no partial graph is ever returned.
func TestImportLineageDocumentFailures(t *testing.T) {
	cases := []struct {
		name string
		text string
		// want parts that must all appear in the error message.
		want []string
	}{
		{"empty text", "", []string{"empty"}},
		{"whitespace only", "   \n\t", []string{"empty"}},
		{"invalid UTF-8", "{\"nodes\":[\"a\xff\"],\"edges\":[]}", []string{"UTF-8"}},
		{"malformed JSON", "{oops", []string{"JSON"}},
		{"array instead of object", `[1,2]`, []string{"JSON"}},
		{"string instead of object", `"hello"`, []string{"JSON"}},
		{"number instead of object", `42`, []string{"JSON"}},
		{"null document", `null`, []string{"JSON"}},
		{"missing nodes", `{"edges":[]}`, []string{"nodes"}},
		{"missing edges", `{"nodes":[]}`, []string{"edges"}},
		{"nodes null", `{"nodes":null,"edges":[]}`, []string{"nodes"}},
		{"nodes is string", `{"nodes":"a","edges":[]}`, []string{"JSON"}},
		{"nodes is object", `{"nodes":{},"edges":[]}`, []string{"JSON"}},
		{"edges null", `{"nodes":[],"edges":null}`, []string{"edges"}},
		{"edges is string", `{"nodes":[],"edges":"x"}`, []string{"JSON"}},
		{"node is number", `{"nodes":[1],"edges":[]}`, []string{"nodes[0]", "string"}},
		{"node is null", `{"nodes":[null],"edges":[]}`, []string{"nodes[0]", "string"}},
		{"node is object", `{"nodes":[{}],"edges":[]}`, []string{"nodes[0]", "string"}},
		{"node is array", `{"nodes":[["a"]],"edges":[]}`, []string{"nodes[0]", "string"}},
		{"edge is number", `{"nodes":["a"],"edges":[1]}`, []string{"edges[0]"}},
		{"edge is null", `{"nodes":["a"],"edges":[null]}`, []string{"edges[0]"}},
		{"edge is string", `{"nodes":["a"],"edges":["a"]}`, []string{"edges[0]"}},
		{"edge missing from", `{"nodes":["a"],"edges":[{"to":"a"}]}`, []string{"edges[0].from"}},
		{"edge missing to", `{"nodes":["a"],"edges":[{"from":"a"}]}`, []string{"edges[0].to"}},
		{"from is number", `{"nodes":["a"],"edges":[{"from":1,"to":"a"}]}`, []string{"edges[0].from", "string"}},
		{"to is null", `{"nodes":["a"],"edges":[{"from":"a","to":null}]}`, []string{"edges[0].to", "string"}},
		{"empty node name", `{"nodes":[""],"edges":[]}`, []string{"nodes[0]", "empty"}},
		{"empty from", `{"nodes":["a"],"edges":[{"from":"","to":"a"}]}`, []string{"edges[0]", "empty"}},
		{"empty to", `{"nodes":["a"],"edges":[{"from":"a","to":""}]}`, []string{"edges[0]", "empty"}},
		{"missing from endpoint", `{"nodes":["b"],"edges":[{"from":"a","to":"b"}]}`, []string{"edges[0]", "a"}},
		{"missing to endpoint", `{"nodes":["a"],"edges":[{"from":"a","to":"b"}]}`, []string{"edges[0]", "b"}},
		{"trailing value", `{"nodes":[],"edges":[]} {}`, []string{"trailing"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			graph, err := ImportLineage(tc.text)
			if err == nil {
				t.Fatalf("ImportLineage(%q) succeeded with %v, want error", tc.text, graph)
			}
			if graph != nil {
				t.Errorf("failed import returned partial graph %v, want nil", graph)
			}
			for _, part := range tc.want {
				if !strings.Contains(err.Error(), part) {
					t.Errorf("error %q must mention %q", err.Error(), part)
				}
			}
		})
	}
}

// A self dependency and cycles of any length fail the whole import with a
// cyclic error naming the datasets on the cycle.
func TestImportLineageCycles(t *testing.T) {
	cases := []struct {
		name      string
		nodes     []string
		edges     [][2]string
		wantNames []string
	}{
		{
			name:      "self dependency",
			nodes:     []string{"a"},
			edges:     [][2]string{{"a", "a"}},
			wantNames: []string{"a"},
		},
		{
			name:      "two node cycle",
			nodes:     []string{"a", "b"},
			edges:     [][2]string{{"a", "b"}, {"b", "a"}},
			wantNames: []string{"a", "b"},
		},
		{
			name:      "three node cycle",
			nodes:     []string{"a", "b", "c"},
			edges:     [][2]string{{"a", "b"}, {"b", "c"}, {"c", "a"}},
			wantNames: []string{"a", "b", "c"},
		},
		{
			name:  "long cycle past a diamond",
			nodes: []string{"s", "a", "b", "m", "t"},
			edges: [][2]string{
				{"s", "a"}, {"s", "b"}, {"a", "m"}, {"b", "m"},
				{"m", "t"}, {"t", "s"},
			},
			wantNames: []string{"s", "a", "m", "t"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			text := lineageDoc(t, tc.nodes, tc.edges)
			graph, err := ImportLineage(text)
			if err == nil {
				t.Fatalf("cyclic document imported: %v", graph)
			}
			if graph != nil {
				t.Errorf("cyclic import returned a graph: %v", graph)
			}
			if !strings.Contains(err.Error(), "cyclic") {
				t.Errorf("error %q must state the document is cyclic", err)
			}
			for _, name := range tc.wantNames {
				if !strings.Contains(err.Error(), name) {
					t.Errorf("cyclic error %q must name involved dataset %q", err, name)
				}
			}
		})
	}
}

// Unknown keys at the top level or on an edge are tolerated: the contract is
// the two arrays and the from/to endpoints, not the absence of anything else.
func TestImportLineageIgnoresUnknownKeys(t *testing.T) {
	text := `{"version":1,"nodes":["a","b"],"extra":[1,2],` +
		`"edges":[{"from":"a","to":"b","note":"direct"}]}`
	graph, err := ImportLineage(text)
	if err != nil {
		t.Fatalf("import with extra keys: %v", err)
	}
	if got, want := len(graph), 2; got != want {
		t.Fatalf("graph has %d nodes, want %d", got, want)
	}
	if got := graph["b"].Parents; !reflect.DeepEqual(got, []string{"a"}) {
		t.Errorf("b parents = %v, want [a]", got)
	}
}

// Import always creates a fresh independent graph: the caller has no graph to
// mutate, and two imports of the same document, plus later registrations, never
// share storage.
func TestImportLineageGraphIsIndependent(t *testing.T) {
	text := lineageDoc(t, []string{"raw", "a"}, [][2]string{{"raw", "a"}})

	first := mustImportLineage(t, text)
	second := mustImportLineage(t, text)

	if err := Register(first, Dataset{Name: "only-in-first"}, []string{"raw"}); err != nil {
		t.Fatalf("Register: %v", err)
	}
	if err := Rename(second, "a", "renamed"); err != nil {
		t.Fatalf("Rename on second graph: %v", err)
	}

	if _, ok := first["renamed"]; ok {
		t.Errorf("first graph leaked the rename from the second: %v", first)
	}
	if _, ok := second["only-in-first"]; ok {
		t.Errorf("second graph leaked the registration from the first: %v", second)
	}
	if _, ok := first["a"]; !ok {
		t.Errorf("rename on the second graph must not touch the first")
	}

	// Mutating returned adjacency slices cannot corrupt another import either:
	// re-import after mutating a list and compare.
	first["raw"].Children[0] = "tampered"
	fresh := mustImportLineage(t, text)
	if got := fresh["raw"].Children; !reflect.DeepEqual(got, []string{"a"}) {
		t.Errorf("later import shares storage with earlier graph: %v", got)
	}
}
