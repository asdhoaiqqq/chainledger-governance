package chainledger

import (
	"strings"
	"testing"
)

// A repeated object key must fail the whole import no matter where the object
// sits: the rule covers not only the document root and edge objects (covered
// in import_test.go) but also objects nested inside unknown fields, including
// objects that are array elements. Unknown fields never register lineage, but
// they cannot bypass document validation.
func TestImportLineageRejectsDuplicateKeysInNestedObjects(t *testing.T) {
	cases := []struct {
		name string
		text string
		key  string
	}{
		{
			name: "unknown object",
			text: `{"nodes":["a","b"],"edges":[{"from":"a","to":"b"}],` +
				`"notes":{"owner":"x","owner":"y"}}`,
			key: "owner",
		},
		{
			name: "object nested two levels in unknown field",
			text: `{"nodes":[],"edges":[],` +
				`"meta":{"review":{"by":"x","by":"y"}}}`,
			key: "by",
		},
		{
			name: "object element of an unknown-field array",
			text: `{"nodes":[],"edges":[],` +
				`"annotations":[{"id":"n1","id":"n2"}]}`,
			key: "id",
		},
		{
			name: "duplicate in the second object element of an array",
			text: `{"nodes":[],"edges":[],` +
				`"annotations":[{"id":"n1"},{"id":"n2","id":"n3"}]}`,
			key: "id",
		},
		{
			name: "object nested in an array inside an edge object",
			text: `{"nodes":["a","b"],"edges":[` +
				`{"from":"a","to":"b","tags":[{"v":1,"v":2}]}]}`,
			key: "v",
		},
		{
			name: "unknown object nested in an edge object",
			text: `{"nodes":["a","b"],"edges":[` +
				`{"from":"a","to":"b","note":{"k":1,"k":2}}]}`,
			key: "k",
		},
		{
			name: "deeply interleaved object-array nesting",
			text: `{"nodes":[],"edges":[],` +
				`"a":[{"b":[{"c":{"d":1,"d":2}}]}]}`,
			key: "d",
		},
		{
			name: "duplicate listed before the lineage fields",
			text: `{"meta":{"a":1,"a":2},"nodes":["a"],"edges":[]}`,
			key:  "a",
		},
		{
			name: "identical values are still a duplicate",
			text: `{"nodes":["a"],"edges":[],"meta":{"a":1,"a":1}}`,
			key:  "a",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// Every document carries otherwise-valid nodes and edges (when it
			// carries any), yet the failure must return no partial graph.
			assertImportFails(t, tc.text, "duplicated", tc.key)
		})
	}
}

// Object/array tracking must stay correct regardless of each member's value
// type: the second occurrence of a key is caught after a string, number,
// boolean, null or an empty container value, and when the repeated value
// itself has any of those types.
func TestImportLineageDuplicateKeyTrackingAcrossValueTypes(t *testing.T) {
	values := []string{`"s"`, "1", "true", "false", "null", "{}", "[]"}
	for _, first := range values {
		for _, second := range values {
			text := `{"nodes":[],"edges":[],` +
				`"meta":{"a":` + first + `,"b":1,"a":` + second + `}}`
			t.Run(first+" then "+second, func(t *testing.T) {
				assertImportFails(t, text, "duplicated", "a")
			})
		}
	}

	// Later members follow the same rule after an empty container member.
	assertImportFails(t,
		`{"nodes":[],"edges":[],"meta":{"empty":{},"tail":1,"tail":2}}`,
		"duplicated", "tail")
	assertImportFails(t,
		`{"nodes":[],"edges":[],"meta":{"empty":[],"tail":1,"tail":2}}`,
		"duplicated", "tail")
}

// Keys are compared by their decoded JSON text. A key spelled with a \uXXXX
// escape is the same key as the literal spelling; an escaped backslash is a
// literal backslash, so lookalike raw spellings must not be merged.
func TestImportLineageDuplicateKeysComparedByDecodedText(t *testing.T) {
	t.Run("unicode escape matches literal key at root", func(t *testing.T) {
		// n decodes to "n", so the escaped key is "nodes".
		assertImportFails(t,
			`{"nodes":[],"nodes":[],"edges":[]}`,
			"duplicated", "nodes")
	})
	t.Run("unicode escape matches literal key in nested object", func(t *testing.T) {
		assertImportFails(t,
			`{"nodes":[],"edges":[],"meta":{"notes":1,"notes":2}}`,
			"duplicated", "notes")
		assertImportFails(t,
			`{"nodes":[],"edges":[],"meta":{"a":1,"a":2}}`,
			"duplicated", "a")
		// The escape may be on the second occurrence instead.
		assertImportFails(t,
			`{"nodes":[],"edges":[],"meta":{"a":1,"a":2}}`,
			"duplicated", "a")
		// A \uXXXX escape and the rune written literally are the same key.
		assertImportFails(t,
			`{"nodes":[],"edges":[],"meta":{"é":1,"é":2}}`,
			"duplicated", "é")
	})
	t.Run("escaped backslash lookalike is a different key", func(t *testing.T) {
		// "\\u006eodes" decodes to a literal backslash followed by u006eodes;
		// it is not the "nodes" key and must import as an unknown field.
		graph := mustImport(t,
			`{"nodes":[],"edges":[],"\\u006eodes":1,"meta":{"a":1,"\\a":2}}`)
		if len(graph) != 0 {
			t.Fatalf("lookalike keys imported lineage nodes: %v", graph)
		}
	})
}

// The duplicate-key scope is one object. Parent and child objects, or distinct
// objects inside an array, may reuse the same key name, and a string value
// that happens to equal a key name is not a key occurrence.
func TestImportLineageDuplicateKeyScopeIsSingleObject(t *testing.T) {
	legal := []struct {
		name string
		text string
	}{
		{
			name: "parent and child reuse a key name",
			text: `{"notes":{"notes":1},"nodes":[],"edges":[]}`,
		},
		{
			name: "sibling objects in an array reuse key names",
			text: `{"nodes":[],"edges":[],` +
				`"tags":[{"k":1},{"k":2},{"k":3}]}`,
		},
		{
			name: "sibling objects nested at different depths",
			text: `{"nodes":[],"edges":[],` +
				`"k":[{"k":{"k":1}},{"k":[{"k":2}]}]}`,
		},
		{
			name: "string value equal to surrounding key names",
			text: `{"nodes":["k"],"edges":[],"k":{"k":"k"}}`,
		},
	}
	for _, tc := range legal {
		t.Run(tc.name, func(t *testing.T) {
			graph := mustImport(t, tc.text)
			assertConsistent(t, graph)
		})
	}

	// The string value "k" registered one real node even though objects named
	// "k" surround it; only object members participate in key tracking.
	graph := mustImport(t, `{"nodes":["k"],"edges":[],"k":{"k":"k"}}`)
	if _, ok := graph["k"]; !ok {
		t.Fatalf(`node named "k" missing, got %v`, graph)
	}

	// Reusing a name across objects is legal, but repeating it inside just one
	// of those objects still fails.
	assertImportFails(t,
		`{"nodes":[],"edges":[],`+
			`"tags":[{"k":1},{"k":2,"k":3}]}`,
		"duplicated", "k")
}

// Unknown fields without duplicate keys are tolerated and their contents never
// become lineage: annotation names and relations stay out of the graph, while
// the documented nodes and edges import normally and still answer queries and
// re-export.
func TestImportLineageUnknownAnnotationsDoNotRegisterLineage(t *testing.T) {
	text := `{"nodes":["a","b"],"edges":[{"from":"a","to":"b"}],` +
		`"annotations":{` +
		`"summary":"hand-written note",` +
		`"nodes":["ghost","phantom"],` +
		`"edges":[{"from":"ghost","to":"phantom"}],` +
		`"tags":[{"id":"ghost"},{"id":"phantom"}]}}`
	graph := mustImport(t, text)
	assertConsistent(t, graph)

	if len(graph) != 2 {
		t.Fatalf("want exactly nodes a and b, got %v", graph)
	}
	for _, name := range []string{"ghost", "phantom"} {
		if _, ok := graph[name]; ok {
			t.Errorf("annotation name %q became a lineage node", name)
		}
		if _, err := Impacts(graph, name); err == nil ||
			!strings.Contains(err.Error(), name) {
			t.Errorf("annotation name %q must be unregistered, got %v", name, err)
		}
	}
	assertEntry(t, graph, "a", nil, []string{"b"})
	assertEntry(t, graph, "b", []string{"a"}, nil)

	impacts := mustImpacts(t, graph, "a")
	assertImpactOnce(t, impacts, "b", 1, []string{"a", "b"})

	out := mustExport(t, graph, "b")
	if want := `{"nodes":["a","b"],"edges":[{"from":"a","to":"b"}]}`; out != want {
		t.Errorf("export = %s, want %s", out, want)
	}
}

// Array-level de-duplication is unrelated to duplicate object keys: a node or
// a direct dependency listed twice in its array imports successfully with one
// copy, even when annotation objects with reused key names are present.
func TestImportLineageArrayDeduplicationCoexistsWithNestedKeyScope(t *testing.T) {
	text := `{"nodes":["a","b","a"],"edges":[` +
		`{"from":"a","to":"b"},{"from":"a","to":"b"}],` +
		`"annotations":[{"id":"x"},{"id":"y"}]}`
	graph := mustImport(t, text)
	assertConsistent(t, graph)
	if len(graph) != 2 {
		t.Fatalf("want two de-duplicated nodes, got %v", graph)
	}
	assertEntry(t, graph, "a", nil, []string{"b"})
	assertEntry(t, graph, "b", []string{"a"}, nil)

	out := mustExport(t, graph, "b")
	if want := `{"nodes":["a","b"],"edges":[{"from":"a","to":"b"}]}`; out != want {
		t.Errorf("export = %s, want one de-duplicated edge %s", out, want)
	}
}
