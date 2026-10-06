package chainledger

import (
	"encoding/json"
	"fmt"
	"reflect"
	"sort"
	"strings"
	"testing"
)

// lineageDoc mirrors the import/export JSON shape for test construction.
type lineageDoc struct {
	Nodes []string            `json:"nodes"`
	Edges []lineageExportEdge `json:"edges"`
}

func marshalLineageDoc(nodes []string, edges ...[2]string) string {
	doc := lineageDoc{Nodes: nodes}
	for _, e := range edges {
		doc.Edges = append(doc.Edges, lineageExportEdge{From: e[0], To: e[1]})
	}
	data, err := json.Marshal(doc)
	if err != nil {
		panic(err)
	}
	return string(data)
}

func mustImport(t *testing.T, text string) map[string]*Lineage {
	t.Helper()
	graph, err := ImportLineage(text)
	if err != nil {
		t.Fatalf("ImportLineage(%s): %v", text, err)
	}
	if graph == nil {
		t.Fatalf("successful import returned a nil graph")
	}
	return graph
}

func assertImportFails(t *testing.T, text string, wantParts ...string) {
	t.Helper()
	graph, err := ImportLineage(text)
	if err == nil {
		t.Fatalf("ImportLineage(%s) succeeded with %v, want error", text, graph)
	}
	if graph != nil {
		t.Fatalf("failed import returned a partial graph %v, want nil", graph)
	}
	for _, part := range wantParts {
		if !strings.Contains(err.Error(), part) {
			t.Errorf("error %q must contain %q", err.Error(), part)
		}
	}
}

// Every listed node exists, including a fully independent one; every edge is
// present in both directions, with direct lists in Go string order regardless
// of document order.
func TestImportLineageNodesAndBidirectionalEdges(t *testing.T) {
	// Nodes deliberately out of order; edges deliberately out of order and not
	// name-sorted at either endpoint.
	text := `{"nodes":["report","isolated","raw","b","a","mid"],` +
		`"edges":[{"from":"mid","to":"report"},{"from":"raw","to":"a"},` +
		`{"from":"b","to":"mid"},{"from":"raw","to":"b"},` +
		`{"from":"a","to":"report"},{"from":"raw","to":"report"}]}`

	graph := mustImport(t, text)
	assertConsistent(t, graph)

	if got, want := len(graph), 6; got != want {
		t.Fatalf("graph has %d nodes %v, want %d", got, graph, want)
	}
	for _, name := range []string{"raw", "a", "b", "mid", "report", "isolated"} {
		if _, ok := graph[name]; !ok {
			t.Errorf("listed node %q missing from imported graph", name)
		}
	}
	// The independent node exists with empty direct lists, sorted (nil-order
	// independent), and participates in Roots.
	assertEntry(t, graph, "isolated", nil, nil)

	assertEntry(t, graph, "raw", nil, []string{"a", "b", "report"})
	assertEntry(t, graph, "a", []string{"raw"}, []string{"report"})
	assertEntry(t, graph, "b", []string{"raw"}, []string{"mid"})
	assertEntry(t, graph, "mid", []string{"b"}, []string{"report"})
	assertEntry(t, graph, "report", []string{"a", "mid", "raw"}, nil)

	if got, want := Roots(graph), []string{"isolated", "raw"}; !reflect.DeepEqual(got, want) {
		t.Errorf("Roots = %v, want %v", got, want)
	}
}

// A direct relation and longer branches through intermediate nodes are all
// kept: queries use the shortest distance, but nodes that only sit on a longer
// branch are still in the graph and reachable.
func TestImportLineageKeepsDirectAndLongBranchesForQueries(t *testing.T) {
	text := marshalLineageDoc(
		[]string{"source", "a", "b", "mid", "report"},
		[2]string{"source", "report"}, // direct edge
		[2]string{"source", "a"}, [2]string{"a", "report"},
		[2]string{"source", "b"}, [2]string{"b", "mid"}, [2]string{"mid", "report"},
	)
	graph := mustImport(t, text)
	assertConsistent(t, graph)

	impacts := mustImpacts(t, graph, "source")
	assertImpactOnce(t, impacts, "report", 1, []string{"source", "report"})
	assertImpactOnce(t, impacts, "a", 1, []string{"source", "a"})
	assertImpactOnce(t, impacts, "b", 1, []string{"source", "b"})
	// mid sits only on the longer branch; it is still reachable at its own
	// shortest distance.
	assertImpactOnce(t, impacts, "mid", 2, []string{"source", "b", "mid"})

	upstreams := mustUpstreams(t, graph, "report")
	// The longer source -> b -> mid -> report branch survives even though
	// source reaches report directly: b and mid stay among the upstreams.
	assertUpstreamOnce(t, upstreams, "mid", 1, []string{"mid", "report"})
	assertUpstreamOnce(t, upstreams, "b", 2, []string{"b", "mid", "report"})
	assertUpstreamOnce(t, upstreams, "source", 1, []string{"source", "report"})
}

// The imported graph is a fully usable graph: registering more datasets,
// rename, unregister, cutoffs and common-upstream queries all work.
func TestImportLineageGraphSupportsAllOperations(t *testing.T) {
	text := marshalLineageDoc(
		[]string{"raw", "a", "b", "left", "right"},
		[2]string{"raw", "a"}, [2]string{"raw", "b"},
		[2]string{"a", "left"}, [2]string{"b", "left"},
		[2]string{"a", "right"}, [2]string{"b", "right"},
	)
	graph := mustImport(t, text)

	common, err := CommonUpstreams(graph, "left", "right")
	if err != nil {
		t.Fatalf("CommonUpstreams: %v", err)
	}
	if got := upstreamCommonNames(common); !reflect.DeepEqual(got, []string{"a", "b"}) {
		t.Errorf("common upstreams = %v, want [a b]", got)
	}

	cut, err := ImpactsWithCutoffs(graph, "raw", []string{"a"})
	if err != nil {
		t.Fatalf("ImpactsWithCutoffs: %v", err)
	}
	if got := impactNames(cut); !reflect.DeepEqual(got, []string{"a", "b", "left", "right"}) {
		t.Errorf("cutoff impacts = %v, want [a b left right]", got)
	}

	if err := Rename(graph, "a", "aa"); err != nil {
		t.Fatalf("Rename: %v", err)
	}
	assertEntry(t, graph, "aa", []string{"raw"}, []string{"left", "right"})
	if _, ok := graph["a"]; ok {
		t.Fatal("old name still present after rename on imported graph")
	}

	if err := Unregister(graph, "right"); err != nil {
		t.Fatalf("Unregister leaf: %v", err)
	}
	if err := Register(graph, Dataset{Name: "fresh"}, []string{"raw"}); err != nil {
		t.Fatalf("Register into imported graph: %v", err)
	}
	assertConsistent(t, graph)
}

// Two empty arrays import as an empty graph that is ready for registrations.
func TestImportLineageEmptyArraysYieldUsableEmptyGraph(t *testing.T) {
	graph := mustImport(t, `{"nodes":[],"edges":[]}`)
	if len(graph) != 0 {
		t.Fatalf("want empty graph, got %v", graph)
	}
	mustRegister(t, graph, "raw")
	mustRegister(t, graph, "detail", "raw")
	assertEntry(t, graph, "detail", []string{"raw"}, nil)
	assertConsistent(t, graph)

	// Whitespace around and inside the document is fine.
	graph = mustImport(t, "  {\n \"nodes\" : [ ] , \"edges\": [ ]\n}\n")
	if len(graph) != 0 {
		t.Fatalf("want empty graph from spaced document, got %v", graph)
	}
}

// Round trip with the full upstream export: importing the exported text and
// exporting the same target again yields byte-identical text.
func TestImportLineageRoundTripsFullUpstreamExport(t *testing.T) {
	original := buildRegisteredGraph(t, [][]string{
		{"raw"},
		{"b", "raw"},
		{"a", "raw"},
		{"mid", "b"},
		{"report", "raw", "a", "mid"},
		{"view", "report"},
		{"isolated"},
	})
	text := mustExport(t, original, "report")

	imported := mustImport(t, text)
	assertConsistent(t, imported)

	again, err := ExportUpstreamLineage(imported, "report")
	if err != nil {
		t.Fatalf("re-export after import: %v", err)
	}
	if again != text {
		t.Errorf("re-export differs:\nimport: %s\nagain:  %s", text, again)
	}

	// The import scope is exactly the document: view and isolated were not in
	// the export and are not restored.
	for _, missing := range []string{"view", "isolated"} {
		if _, ok := imported[missing]; ok {
			t.Errorf("import restored %q which the export omitted", missing)
		}
		if _, err := Impacts(imported, missing); err == nil ||
			!strings.Contains(err.Error(), missing) {
			t.Errorf("query for omitted %q must fail as unregistered, got %v", missing, err)
		}
	}
}

// Round trip with the source-scoped export rebuilds only that part: no other
// source, ancestor or downstream is completed, and the same scoped export is
// byte-identical.
func TestImportLineageRoundTripsSourceTargetExportWithoutCompleting(t *testing.T) {
	original := buildRegisteredGraph(t, [][]string{
		{"pre"},
		{"source", "pre"},
		{"a", "source"},
		{"b", "source"},
		{"sidetrack", "source"},
		{"mid", "b"},
		{"extra"},
		{"lone"},
		{"report", "source", "a", "mid", "extra", "lone"},
		{"view", "report"},
		{"isolated"},
	})
	text, err := ExportSourceTargetLineage(original, "source", "report")
	if err != nil {
		t.Fatalf("ExportSourceTargetLineage: %v", err)
	}
	imported := mustImport(t, text)
	assertConsistent(t, imported)

	again, err := ExportSourceTargetLineage(imported, "source", "report")
	if err != nil {
		t.Fatalf("scoped re-export after import: %v", err)
	}
	if again != text {
		t.Errorf("scoped re-export differs:\nimport: %s\nagain:  %s", text, again)
	}

	// Exactly the document's five nodes exist; excluded sources, ancestors,
	// side branches and downstreams are not invented.
	wantNodes := map[string]bool{"source": true, "a": true, "b": true, "mid": true, "report": true}
	if len(imported) != len(wantNodes) {
		t.Fatalf("imported graph has %v, want only %v", imported, wantNodes)
	}
	for name := range imported {
		if !wantNodes[name] {
			t.Errorf("import restored out-of-scope node %q", name)
		}
	}
	// a's independent source extra was excluded and must not be reattached.
	assertEntry(t, imported, "a", []string{"source"}, []string{"report"})
}

// Reordering nodes and edges, or repeating a node or dependency, imports the
// same graph and re-exports byte-identical canonical text.
func TestImportLineageOrderIndependentAndDeduplicated(t *testing.T) {
	shuffled := `{"edges":[{"from":"raw","to":"b"},{"from":"raw","to":"a"},` +
		`{"from":"raw","to":"a"}],"nodes":["b","raw","a","raw","b"]}`

	graph := mustImport(t, shuffled)
	assertConsistent(t, graph)
	assertEntry(t, graph, "raw", nil, []string{"a", "b"})
	assertEntry(t, graph, "a", []string{"raw"}, nil)
	assertEntry(t, graph, "b", []string{"raw"}, nil)
	// The full upstream export of a covers exactly raw -> a, canonically
	// rendered this way; dedupe must keep one edge and one raw node.
	out := mustExport(t, graph, "a")
	if want := `{"nodes":["a","raw"],"edges":[{"from":"raw","to":"a"}]}`; out != want {
		t.Errorf("export = %s, want %s", out, want)
	}
}

// Names are identified by their exact decoded value: case, spaces, Chinese,
// quotes, backslashes and newlines all survive verbatim.
func TestImportLineagePreservesExactNames(t *testing.T) {
	quote := `say "hi"`
	slash := `a\b`
	newline := "line1\nline2"
	chinese := "中文 数据集"
	spaced := "  keep spaces  "
	text := marshalLineageDoc(
		[]string{quote, slash, newline, chinese, spaced, "A", "a"},
		[2]string{quote, slash},
		[2]string{slash, newline},
		[2]string{chinese, spaced},
		[2]string{"a", "A"},
	)
	graph := mustImport(t, text)
	assertConsistent(t, graph)

	for _, name := range []string{quote, slash, newline, chinese, spaced, "A", "a"} {
		if _, ok := graph[name]; !ok {
			t.Errorf("name %q not found by exact value", name)
		}
	}
	assertEntry(t, graph, slash, []string{quote}, []string{newline})
	assertEntry(t, graph, newline, []string{slash}, nil)
	assertEntry(t, graph, spaced, []string{chinese}, nil)
	// Case-sensitive: "A" derives from "a"; they are distinct datasets.
	assertEntry(t, graph, "A", []string{"a"}, nil)
	assertEntry(t, graph, "a", nil, []string{"A"})

	// A re-export decodes back to the very same string values.
	out := mustExport(t, graph, newline)
	doc := parseExport(t, out)
	wantNodes := []string{quote, slash, newline}
	sort.Strings(wantNodes)
	if !reflect.DeepEqual(doc.Nodes, wantNodes) {
		t.Errorf("nodes = %q, want %q", doc.Nodes, wantNodes)
	}
}

// Import never touches the caller's graph (it does not even receive one).
func TestImportLineageLeavesCallerGraphUntouched(t *testing.T) {
	caller := buildRegisteredGraph(t, [][]string{
		{"raw"},
		{"keep", "raw"},
	})
	before := snapshotExportGraph(caller)

	mustImport(t, marshalLineageDoc(
		[]string{"x", "y", "z"},
		[2]string{"x", "y"}, [2]string{"y", "z"},
	))
	assertImportFails(t, marshalLineageDoc([]string{"a", "b"}, [2]string{"a", "b"}, [2]string{"b", "a"}), "cycle")
	assertImportFails(t, marshalLineageDoc([]string{"a"}, [2]string{"a", "ghost"}), "ghost")

	assertGraphUnchanged(t, before, caller)
	if _, ok := caller["x"]; ok {
		t.Fatal("import leaked a node into the caller's graph")
	}
}

// Unknown fields beyond nodes and edges are ignored: only the documented
// shape is required, and tolerance here keeps hand-authored documents working.
func TestImportLineageIgnoresUnknownFields(t *testing.T) {
	text := `{"version":1,"nodes":["a"],"edges":[],"nested":{"keep":true}}`
	graph := mustImport(t, text)
	if len(graph) != 1 || graph["a"] == nil {
		t.Fatalf("want exactly node a, got %v", graph)
	}
}

// Document-level problems: empty/invalid text, bad shape, wrong types.
func TestImportLineageDocumentErrors(t *testing.T) {
	cases := []struct {
		name string
		text string
		want []string
	}{
		{"empty", "", []string{"empty"}},
		{"whitespace only", "   \n\t", []string{"empty"}},
		{"not json", "{not json", []string{"JSON object"}},
		{"truncated", `{"nodes":[`, []string{"complete"}},
		{"top-level array", `[]`, []string{"JSON object"}},
		{"top-level string", `"nodes"`, []string{"JSON object"}},
		{"top-level number", `42`, []string{"JSON object"}},
		{"top-level bool", `true`, []string{"JSON object"}},
		{"top-level null", `null`, []string{"JSON object"}},
		{"two values", `{"nodes":[],"edges":[]}{}`, []string{"exactly one"}},
		{"duplicate key", `{"nodes":[],"nodes":[],"edges":[]}`, []string{"duplicated"}},
		{"missing nodes", `{"edges":[]}`, []string{"nodes"}},
		{"missing edges", `{"nodes":[]}`, []string{"edges"}},
		{"nodes null", `{"nodes":null,"edges":[]}`, []string{"nodes", "array"}},
		{"edges null", `{"nodes":[],"edges":null}`, []string{"edges", "array"}},
		{"nodes as object", `{"nodes":{},"edges":[]}`, []string{"nodes", "array"}},
		{"edges as object", `{"nodes":[],"edges":{}}`, []string{"edges", "array"}},
		{"nodes as string", `{"nodes":"a","edges":[]}`, []string{"nodes", "array"}},
		{"node element number", `{"nodes":[1],"edges":[]}`, []string{"nodes"}},
		{"node element object", `{"nodes":[{}],"edges":[]}`, []string{"nodes"}},
		{"node element bool", `{"nodes":[true],"edges":[]}`, []string{"nodes"}},
		{"node element null", `{"nodes":[null],"edges":[]}`, []string{"nodes"}},
		{"edge element string", `{"nodes":[],"edges":["a->b"]}`, []string{"edges"}},
		{"edge element number", `{"nodes":[],"edges":[1]}`, []string{"edges"}},
		{"edge element null", `{"nodes":[],"edges":[null]}`, []string{"edges"}},
		{"edge element array", `{"nodes":[],"edges":[[]]}`, []string{"edges"}},
		{"edge from number", `{"nodes":["b"],"edges":[{"from":1,"to":"b"}]}`, []string{"edges"}},
		{"edge to array", `{"nodes":["a"],"edges":[{"from":"a","to":[]}]}`, []string{"edges"}},
		{"edge from null", `{"nodes":["a"],"edges":[{"from":null,"to":"a"}]}`, []string{"from"}},
		{"edge duplicated key", `{"nodes":["a","b"],"edges":[{"from":"a","from":"b","to":"a"}]}`, []string{"duplicated"}},
		{"empty node name", `{"nodes":[""],"edges":[]}`, []string{"dataset name is required"}},
		{"edge missing from", `{"nodes":["a"],"edges":[{"to":"a"}]}`, []string{"from"}},
		{"edge missing to", `{"nodes":["a"],"edges":[{"from":"a"}]}`, []string{"to"}},
		{"invalid utf-8", "{\"nodes\":[\"a\xffb\"],\"edges\":[]}", []string{"UTF-8"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assertImportFails(t, tc.text, tc.want...)
		})
	}
}

// Both endpoints of every edge must be listed nodes.
func TestImportLineageEdgeEndpointErrors(t *testing.T) {
	assertImportFails(t,
		marshalLineageDoc([]string{"b"}, [2]string{"ghost", "b"}),
		"endpoint", "ghost")
	assertImportFails(t,
		marshalLineageDoc([]string{"a"}, [2]string{"a", "ghost"}),
		"endpoint", "ghost")
	assertImportFails(t,
		marshalLineageDoc([]string{"a", "b"}, [2]string{"a", "b"}, [2]string{"b", "missing"}),
		"endpoint", "missing")
	// A valid-looking document elsewhere does not rescue the bad edge; no
	// partial graph comes back, so nothing is registered.
	graph, err := ImportLineage(marshalLineageDoc(
		[]string{"a", "b", "c"},
		[2]string{"a", "b"}, [2]string{"b", "c"}, [2]string{"c", "phantom"},
	))
	if err == nil || !strings.Contains(err.Error(), "phantom") {
		t.Fatalf("want endpoint error naming phantom, got graph=%v err=%v", graph, err)
	}
	if graph != nil {
		t.Fatalf("want nil graph after endpoint error, got %v", graph)
	}
}

// Self dependencies and cycles of any length fail the whole import with a
// cycle error naming the involved datasets; an independent node alongside the
// cycle changes nothing.
func TestImportLineageCycleErrors(t *testing.T) {
	t.Run("self dependency", func(t *testing.T) {
		assertImportFails(t,
			marshalLineageDoc([]string{"a", "lonely"}, [2]string{"a", "a"}),
			"cycle", "a")
	})
	t.Run("two node cycle", func(t *testing.T) {
		assertImportFails(t,
			marshalLineageDoc([]string{"a", "b"}, [2]string{"a", "b"}, [2]string{"b", "a"}),
			"cycle", "a", "b")
	})
	t.Run("three node cycle", func(t *testing.T) {
		text := marshalLineageDoc(
			[]string{"a", "b", "c"},
			[2]string{"a", "b"}, [2]string{"b", "c"}, [2]string{"c", "a"},
		)
		assertImportFails(t, text, "cycle", "a", "b", "c")
	})
	t.Run("long cycle", func(t *testing.T) {
		var nodes []string
		var edges [][2]string
		for _, n := range []string{"n1", "n2", "n3", "n4", "n5"} {
			nodes = append(nodes, n)
		}
		edges = append(edges,
			[2]string{"n1", "n2"}, [2]string{"n2", "n3"},
			[2]string{"n3", "n4"}, [2]string{"n4", "n5"}, [2]string{"n5", "n1"},
		)
		assertImportFails(t, marshalLineageDoc(nodes, edges...),
			"cycle", "n1", "n2", "n3", "n4", "n5")
	})
	t.Run("diamond is not a cycle", func(t *testing.T) {
		graph := mustImport(t, marshalLineageDoc(
			[]string{"raw", "a", "b", "merge"},
			[2]string{"raw", "a"}, [2]string{"raw", "b"}, [2]string{"a", "merge"}, [2]string{"b", "merge"},
		))
		assertConsistent(t, graph)
	})
}

// The reported cycle is written in derivation direction and is independent of
// the document's node/edge ordering.
func TestImportLineageCycleMessageIsDeterministic(t *testing.T) {
	forward := marshalLineageDoc(
		[]string{"a", "b", "c"},
		[2]string{"a", "b"}, [2]string{"b", "c"}, [2]string{"c", "a"},
	)
	reversed := `{"nodes":["c","a","b"],"edges":[` +
		`{"from":"c","to":"a"},{"from":"b","to":"c"},{"from":"a","to":"b"}]}`

	_, err1 := ImportLineage(forward)
	_, err2 := ImportLineage(reversed)
	if err1 == nil || err2 == nil {
		t.Fatal("want cycle errors")
	}
	if err1.Error() != err2.Error() {
		t.Errorf("cycle error depends on document order:\n%s\n%s", err1, err2)
	}
	if want := "lineage contains a cycle: a -> b -> c -> a"; err1.Error() != want {
		t.Errorf("cycle error = %q, want %q", err1.Error(), want)
	}
}

// A graph imported from a hand-written document answers queries identically to
// the same graph built with Register; the result includes the merge tie-break
// rule that compares full paths.
func TestImportLineageMatchesRegisteredGraphQueries(t *testing.T) {
	registered := buildRegisteredGraph(t, [][]string{
		{"source"},
		{"b", "source"},
		{"a", "source"},
		{"c", "b"},
		{"z", "a"},
		{"report", "c", "z"},
		{"view", "report"},
		{"isolated"},
	})
	text := marshalLineageDoc(
		[]string{"source", "a", "b", "c", "z", "report", "view", "isolated"},
		[2]string{"source", "a"}, [2]string{"source", "b"},
		[2]string{"b", "c"}, [2]string{"a", "z"},
		[2]string{"c", "report"}, [2]string{"z", "report"},
		[2]string{"report", "view"},
	)
	imported := mustImport(t, text)

	wantImpacts := mustImpacts(t, registered, "source")
	if got := mustImpacts(t, imported, "source"); !reflect.DeepEqual(got, wantImpacts) {
		t.Errorf("imported impacts = %v, want %v", got, wantImpacts)
	}
	wantUpstreams := mustUpstreams(t, registered, "report")
	if got := mustUpstreams(t, imported, "report"); !reflect.DeepEqual(got, wantUpstreams) {
		t.Errorf("imported upstreams = %v, want %v", got, wantUpstreams)
	}
}

// Sanity guard for the test helper itself: documents it constructs are valid
// JSON with the intended shape.
func TestMarshalLineageDocHelper(t *testing.T) {
	text := marshalLineageDoc([]string{"a"}, [2]string{"a", "b"})
	var doc lineageDoc
	if err := json.Unmarshal([]byte(text), &doc); err != nil {
		t.Fatal(err)
	}
	if fmt.Sprint(doc.Nodes) != "[a]" || len(doc.Edges) != 1 {
		t.Fatalf("helper produced %s", text)
	}
}

func upstreamCommonNames(found []CommonUpstream) []string {
	names := make([]string, len(found))
	for i, c := range found {
		names[i] = c.Dataset
	}
	return names
}
