package chainledger

import (
	"strings"
	"testing"
)

// A duplicated object key anywhere in the document fails the whole import —
// not only at the top level or inside edges, but also inside unknown fields
// that carry extra annotations. Unknown fields never register lineage, yet
// their nested objects (including objects inside arrays) are still validated,
// so an annotated document with a repeated key cannot slip through. Every
// failing document below declares otherwise-legal nodes and edges; the import
// must still return a nil graph and an error naming the duplicated key.
func TestImportLineageRejectsDuplicateKeysInNestedDocuments(t *testing.T) {
	cases := []struct {
		name string
		text string
		want []string
	}{
		{
			"unknown field object",
			`{"nodes":["raw","report"],"edges":[{"from":"raw","to":"report"}],` +
				`"note":{"owner":"ann","owner":"bob"}}`,
			[]string{"duplicated", "owner"},
		},
		{
			"identical values still duplicate",
			`{"nodes":["raw"],"edges":[],"note":{"owner":"ann","owner":"ann"}}`,
			[]string{"duplicated", "owner"},
		},
		{
			"object inside array inside unknown field",
			`{"nodes":["raw"],"edges":[],"tags":[{"key":"a","key":"b"}]}`,
			[]string{"duplicated", "key"},
		},
		{
			"second array element object",
			`{"nodes":["raw"],"edges":[],"tags":[{"key":"a"},{"key":"b","key":"c"}]}`,
			[]string{"duplicated", "key"},
		},
		{
			"deeply interleaved objects and arrays",
			`{"nodes":["raw"],"edges":[],"meta":{"outer":[{"inner":[{"deep":1,"deep":2}]}]}}`,
			[]string{"duplicated", "deep"},
		},
		{
			"nested object inside an edge object",
			`{"nodes":["raw","report"],"edges":[{"from":"raw","to":"report",` +
				`"meta":{"m":1,"m":2}}]}`,
			[]string{"duplicated", "m"},
		},
		{
			"later member repeats an earlier key",
			`{"nodes":["raw"],"edges":[],"note":{"x":1,"y":2,"z":3,"x":4}}`,
			[]string{"duplicated", "x"},
		},
		{
			"duplicate among every value type",
			`{"nodes":["raw"],"edges":[],"note":{"s":"text","n":1.5,"b":true,` +
				`"z":null,"o":{},"a":[],"s":"again"}}`,
			[]string{"duplicated", "s"},
		},
		{
			"empty containers as member values",
			`{"nodes":["raw"],"edges":[],"note":{"box":{},"box":[]}}`,
			[]string{"duplicated", "box"},
		},
		{
			"unicode escape spelling of the same key",
			`{"nodes":["raw"],"edges":[],"note":{"kind":1,"\u006bind":2}}`,
			[]string{"duplicated", "kind"},
		},
		{
			"escaped spelling of a reserved key at top level",
			`{"nodes":["raw"],"edges":[],"\u006eodes":[]}`,
			[]string{"duplicated", "nodes"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assertImportFails(t, tc.text, tc.want...)
		})
	}
}

// The duplicate rule is scoped to each single object: the same key name in a
// parent object and a nested child object, or in two different objects of one
// array, is legal. A string value that happens to equal a key name is not a
// key at all. Keys are compared by their decoded text, so a key containing an
// escaped backslash decodes to a different name and must not be merged with
// the plain-looking key it resembles. All of these documents import fine.
func TestImportLineageDuplicateKeyScopeIsPerObject(t *testing.T) {
	cases := []struct {
		name string
		text string
	}{
		{
			"same key in parent and child objects",
			`{"name":"root","nodes":["raw"],"edges":[],"child":{"name":"leaf"}}`,
		},
		{
			"same key across sibling objects in one array",
			`{"nodes":["raw"],"edges":[],"items":[{"id":1},{"id":2},{"id":3}]}`,
		},
		{
			"same key in object and in nested array element",
			`{"nodes":["raw"],"edges":[],"meta":{"id":1,"list":[{"id":2}]}}`,
		},
		{
			"string value equal to a key name",
			`{"note":"nodes","nodes":["raw"],"edges":[],"other":{"field":"note"}}`,
		},
		{
			"string value equal to a key of the same object",
			`{"nodes":["raw"],"edges":[],"note":{"kind":"kind"}}`,
		},
		{
			"key with escaped backslash is a different key",
			`{"nodes":["raw"],"edges":[],"note":{"kind":1,"\\kind":2}}`,
		},
		{
			"escaped backslash before u is literal text, not an escape",
			`{"nodes":["raw"],"edges":[],"note":{"kind":1,"\\u006bind":2}}`,
		},
		{
			"reserved key name prefixed by a literal backslash",
			`{"nodes":["raw"],"\\nodes":["not","lineage"],"edges":[]}`,
		},
		{
			"all value types as distinct members",
			`{"nodes":["raw"],"edges":[],"meta":{"s":"text","n":-1.5,"b":false,` +
				`"z":null,"o":{},"a":[],"deep":{"x":[{},[null,true]]}}}`,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			graph := mustImport(t, tc.text)
			assertConsistent(t, graph)
			if len(graph) != 1 || graph["raw"] == nil {
				t.Errorf("want exactly the declared node raw, got %v", graph)
			}
		})
	}
}

// Key equality follows the decoded JSON text: "nodes" and "nodes" written
// with a \u escape are the same key and must be rejected as duplicates, while
// a key whose backslash is itself escaped ("\\u006eodes", decoding to the
// literal text nodes) is a different key and stays legal.
func TestImportLineageKeyComparisonUsesDecodedText(t *testing.T) {
	t.Run("escaped and plain spellings of one key are duplicates", func(t *testing.T) {
		assertImportFails(t,
			`{"nodes":["raw"],"edges":[],"note":{"alpha":1,"\u0061lpha":2}}`,
			"duplicated", "alpha")
	})
	t.Run("the error names the decoded key", func(t *testing.T) {
		_, err := ImportLineage(`{"nodes":["raw"],"edges":[],"\u0065dges":[]}`)
		if err == nil || !strings.Contains(err.Error(), "duplicated") ||
			!strings.Contains(err.Error(), "edges") {
			t.Fatalf("want duplicate-key error naming edges, got %v", err)
		}
	})
	t.Run("escaped backslash keeps keys distinct", func(t *testing.T) {
		// The second key decodes to the literal text nodes (backslash
		// included); it only resembles an escape spelling of the first.
		graph := mustImport(t,
			`{"nodes":["raw"],"edges":[],"note":{"alpha":1,"\\u0061lpha":2}}`)
		assertConsistent(t, graph)
	})
}

// An unknown field without duplicate keys never blocks the import, and its
// content never becomes lineage: dataset names and from/to-looking relations
// inside an annotation are not registered as nodes or dependencies. The
// imported graph holds exactly the declared lineage and answers queries and
// exports as usual.
func TestImportLineageUnknownFieldContentStaysOutOfLineage(t *testing.T) {
	text := `{"nodes":["raw","mid","report"],"edges":[` +
		`{"from":"raw","to":"mid"},{"from":"mid","to":"report"}],` +
		`"readme":{"summary":"demo lineage","datasets":["ghost","phantom"],` +
		`"relation":{"from":"ghost","to":"phantom"},` +
		`"history":[{"from":"phantom","to":"ghost"}]}}`

	graph := mustImport(t, text)
	assertConsistent(t, graph)

	if got, want := len(graph), 3; got != want {
		t.Fatalf("graph has %d nodes %v, want %d", got, graph, want)
	}
	assertEntry(t, graph, "raw", nil, []string{"mid"})
	assertEntry(t, graph, "mid", []string{"raw"}, []string{"report"})
	assertEntry(t, graph, "report", []string{"mid"}, nil)

	// Names and relations mentioned only in the annotation are not lineage.
	for _, leaked := range []string{"ghost", "phantom", "readme", "relation"} {
		if _, ok := graph[leaked]; ok {
			t.Errorf("annotation content %q leaked into the graph", leaked)
		}
		if _, err := Impacts(graph, leaked); err == nil ||
			!strings.Contains(err.Error(), leaked) {
			t.Errorf("query for annotation name %q must fail as unregistered, got %v", leaked, err)
		}
	}

	// The declared upstream/downstream relations drive queries and exports.
	assertImpactOnce(t, mustImpacts(t, graph, "raw"), "report", 2,
		[]string{"raw", "mid", "report"})
	assertUpstreamOnce(t, mustUpstreams(t, graph, "report"), "raw", 2,
		[]string{"raw", "mid", "report"})
	if got, want := mustExport(t, graph, "report"),
		`{"nodes":["mid","raw","report"],"edges":[{"from":"mid","to":"report"},`+
			`{"from":"raw","to":"mid"}]}`; got != want {
		t.Errorf("export = %s, want %s", got, want)
	}
}

// Array-level de-duplication is a separate, kept behavior: listing the same
// dataset twice in nodes or the same direct dependency twice in edges is not
// a duplicated object key and imports successfully, keeping one copy of each.
func TestImportLineageArrayDuplicatesStillDeduplicated(t *testing.T) {
	text := `{"nodes":["raw","a","b","raw","a"],"edges":[` +
		`{"from":"raw","to":"a"},{"from":"raw","to":"b"},` +
		`{"from":"raw","to":"a"}],"note":{"seen":true}}`

	graph := mustImport(t, text)
	assertConsistent(t, graph)

	if got, want := len(graph), 3; got != want {
		t.Fatalf("graph has %d nodes %v, want %d", got, graph, want)
	}
	assertEntry(t, graph, "raw", nil, []string{"a", "b"})
	assertEntry(t, graph, "a", []string{"raw"}, nil)
	assertEntry(t, graph, "b", []string{"raw"}, nil)

	// One copy of each: the re-export lists every node and edge exactly once.
	if got, want := mustExport(t, graph, "a"),
		`{"nodes":["a","raw"],"edges":[{"from":"raw","to":"a"}]}`; got != want {
		t.Errorf("export = %s, want %s", got, want)
	}
}
