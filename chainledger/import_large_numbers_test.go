package chainledger

import (
	"reflect"
	"strings"
	"testing"
)

// These tests pin down how the import treats numbers in additional fields:
// unknown top-level fields, unknown fields of an edge object, and nested
// objects and arrays inside such annotations may carry statistical values of
// any magnitude. Those values never participate in the lineage, so whether a
// number is representable as a float64 must not decide whether the document
// imports — 1e400, a hundred-digit integer and 1e-400 are all fine wherever
// JSON syntax allows a number. The document is still validated as a whole:
// a malformed number or a duplicated object key in an annotation fails the
// entire import with a nil graph, exactly like any other document problem.

// bigIntLiteral is a syntactically valid integer far beyond int64, uint64 and
// the exact-integer range of float64: 120 digits.
const bigIntLiteral = "1" + "2345678901234567890123456789012345678901234567890" +
	"123456789012345678901234567890123456789012345678901234567890123456789"

// Legal big numbers in every additional position import successfully, and the
// resulting graph holds exactly the declared nodes and dependencies.
func TestImportLineageAcceptsLargeNumbersInExtraFields(t *testing.T) {
	cases := []struct {
		name string
		text string
	}{
		{
			"positive exponent overflow at top level",
			`{"nodes":["raw","report"],"edges":[{"from":"raw","to":"report"}],` +
				`"stats":{"rows":1e400}}`,
		},
		{
			"negative exponent overflow at top level",
			`{"nodes":["raw","report"],"edges":[{"from":"raw","to":"report"}],` +
				`"stats":{"rows":-1e400}}`,
		},
		{
			"hundred-digit integer at top level",
			`{"nodes":["raw","report"],"edges":[{"from":"raw","to":"report"}],` +
				`"stats":{"rows":` + bigIntLiteral + `}}`,
		},
		{
			"negative hundred-digit integer at top level",
			`{"nodes":["raw","report"],"edges":[{"from":"raw","to":"report"}],` +
				`"stats":{"rows":-` + bigIntLiteral + `}}`,
		},
		{
			"tiny negative exponent at top level",
			`{"nodes":["raw","report"],"edges":[{"from":"raw","to":"report"}],` +
				`"stats":{"epsilon":1e-400}}`,
		},
		{
			"big numbers inside an edge object's unknown field",
			`{"nodes":["raw","report"],"edges":[{"from":"raw","to":"report",` +
				`"stats":{"rows":1e400,"epsilon":1e-400}}]}`,
		},
		{
			"big numbers nested in objects and arrays of an annotation",
			`{"nodes":["raw","report"],"edges":[{"from":"raw","to":"report"}],` +
				`"stats":{"perShard":[{"shard":0,"rows":1e400},` +
				`{"shard":1,"rows":[-1e400,` + bigIntLiteral + `,1e-400]}],` +
				`"total":` + bigIntLiteral + `}}`,
		},
		{
			"big numbers in several additional fields at once",
			`{"stats":{"rows":1e400},"nodes":["raw","report"],` +
				`"edges":[{"from":"raw","to":"report","weight":-1e400}],` +
				`"histogram":[1e-400,` + bigIntLiteral + `]}`,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			graph := mustImport(t, tc.text)
			assertConsistent(t, graph)

			if got, want := len(graph), 2; got != want {
				t.Fatalf("graph has %d nodes %v, want %d", got, graph, want)
			}
			assertEntry(t, graph, "raw", nil, []string{"report"})
			assertEntry(t, graph, "report", []string{"raw"}, nil)

			// The statistics are not dataset metadata: the re-exported
			// document holds exactly the declared lineage, no numbers.
			out := mustExport(t, graph, "report")
			if want := `{"nodes":["raw","report"],"edges":[{"from":"raw","to":"report"}]}`; out != want {
				t.Errorf("export = %s, want %s", out, want)
			}
		})
	}
}

// Names and dependency-looking objects inside an annotation do not become
// nodes or relations, even when the annotation also carries big numbers: the
// graph holds exactly the declared lineage and nothing else.
func TestImportLineageLargeNumberAnnotationsStayOutOfLineage(t *testing.T) {
	text := `{"nodes":["raw","mid","report"],"edges":[` +
		`{"from":"raw","to":"mid"},{"from":"mid","to":"report"}],` +
		`"stats":{"rows":1e400,"datasets":["ghost","phantom"],` +
		`"relation":{"from":"ghost","to":"phantom"},` +
		`"history":[{"from":"phantom","to":"ghost","rows":` + bigIntLiteral + `}]}}`

	graph := mustImport(t, text)
	assertConsistent(t, graph)

	if got, want := len(graph), 3; got != want {
		t.Fatalf("graph has %d nodes %v, want %d", got, graph, want)
	}
	assertEntry(t, graph, "raw", nil, []string{"mid"})
	assertEntry(t, graph, "mid", []string{"raw"}, []string{"report"})
	assertEntry(t, graph, "report", []string{"mid"}, nil)

	// Content mentioned only in the annotation is not lineage.
	for _, leaked := range []string{"ghost", "phantom", "stats", "relation"} {
		if _, ok := graph[leaked]; ok {
			t.Errorf("annotation content %q leaked into the graph", leaked)
		}
		if _, err := Impacts(graph, leaked); err == nil ||
			!strings.Contains(err.Error(), leaked) {
			t.Errorf("query for annotation name %q must fail as unregistered, got %v", leaked, err)
		}
	}

	assertImpactOnce(t, mustImpacts(t, graph, "raw"), "report", 2,
		[]string{"raw", "mid", "report"})
	assertUpstreamOnce(t, mustUpstreams(t, graph, "report"), "raw", 2,
		[]string{"raw", "mid", "report"})
}

// Changing the annotation's numbers and rearranging the additional fields
// yields the identical lineage: same direct lists, same queries and
// byte-identical exports.
func TestImportLineageLargeNumberValuesAndPositionsDoNotChangeLineage(t *testing.T) {
	plain := `{"nodes":["raw","mid","report","isolated"],"edges":[` +
		`{"from":"raw","to":"mid"},{"from":"mid","to":"report"},` +
		`{"from":"raw","to":"report"}]}`
	annotated := `{"stats":{"rows":1e400},"nodes":["raw","mid","report","isolated"],` +
		`"edges":[{"from":"raw","to":"mid","weight":` + bigIntLiteral + `},` +
		`{"from":"mid","to":"report"},{"from":"raw","to":"report"}],` +
		`"histogram":[1e-400,-1e400]}`
	rearranged := `{"histogram":[-1e400,1e-400],` +
		`"edges":[{"from":"mid","to":"report"},{"from":"raw","to":"report"},` +
		`{"from":"raw","to":"mid","weight":-` + bigIntLiteral + `}],` +
		`"nodes":["isolated","report","mid","raw"],"stats":{"rows":-1e400}}`

	graphPlain := mustImport(t, plain)
	graphAnnotated := mustImport(t, annotated)
	graphRearranged := mustImport(t, rearranged)

	for _, graph := range []map[string]*Lineage{graphAnnotated, graphRearranged} {
		assertConsistent(t, graph)
		if got, want := len(graph), 4; got != want {
			t.Fatalf("graph has %d nodes %v, want %d", got, graph, want)
		}
		assertEntry(t, graph, "raw", nil, []string{"mid", "report"})
		assertEntry(t, graph, "mid", []string{"raw"}, []string{"report"})
		assertEntry(t, graph, "report", []string{"mid", "raw"}, nil)
		assertEntry(t, graph, "isolated", nil, nil)

		// Queries and exports match the annotation-free document exactly.
		if got, want := mustImpacts(t, graph, "raw"), mustImpacts(t, graphPlain, "raw"); !reflect.DeepEqual(got, want) {
			t.Errorf("impacts = %v, want %v", got, want)
		}
		if got, want := mustUpstreams(t, graph, "report"), mustUpstreams(t, graphPlain, "report"); !reflect.DeepEqual(got, want) {
			t.Errorf("upstreams = %v, want %v", got, want)
		}
		if got, want := mustExport(t, graph, "report"), mustExport(t, graphPlain, "report"); got != want {
			t.Errorf("export = %s, want %s", got, want)
		}
	}
}

// With a direct source -> target edge and a longer branch through an
// intermediate dataset in one document, additional big numbers change no
// relation: the downstream impact query still picks the direct path at
// distance one, the full upstream export still keeps the intermediate dataset
// and the longer branch's direct dependencies, and the exported text is
// identical to the annotation-free document's, sorted as usual and free of
// the statistics. An explicitly listed independent dataset is kept.
func TestImportLineageLargeNumbersKeepDirectAndBranchRelations(t *testing.T) {
	plain := `{"nodes":["source","mid","report","isolated"],"edges":[` +
		`{"from":"source","to":"report"},{"from":"source","to":"mid"},` +
		`{"from":"mid","to":"report"}]}`
	annotated := `{"nodes":["source","mid","report","isolated"],"edges":[` +
		`{"from":"source","to":"report"},{"from":"source","to":"mid"},` +
		`{"from":"mid","to":"report"}],` +
		`"stats":{"rows":1e400,"epsilon":1e-400,"total":` + bigIntLiteral + `}}`

	graph := mustImport(t, annotated)
	graphPlain := mustImport(t, plain)
	assertConsistent(t, graph)

	// The independent dataset listed in nodes survives alongside the chain.
	assertEntry(t, graph, "isolated", nil, nil)
	if got, want := Roots(graph), []string{"isolated", "source"}; !reflect.DeepEqual(got, want) {
		t.Errorf("Roots = %v, want %v", got, want)
	}

	// The impact query picks the direct path: report at distance one.
	impacts := mustImpacts(t, graph, "source")
	assertImpactOnce(t, impacts, "report", 1, []string{"source", "report"})
	assertImpactOnce(t, impacts, "mid", 1, []string{"source", "mid"})

	// The upstream query and the full export keep the intermediate dataset
	// and the longer branch's direct dependencies, not only the direct edge.
	upstreams := mustUpstreams(t, graph, "report")
	assertUpstreamOnce(t, upstreams, "source", 1, []string{"source", "report"})
	assertUpstreamOnce(t, upstreams, "mid", 1, []string{"mid", "report"})

	out := mustExport(t, graph, "report")
	if want := mustExport(t, graphPlain, "report"); out != want {
		t.Errorf("export = %s, want the annotation-free export %s", out, want)
	}
	doc := parseExport(t, out)
	if got, want := doc.Nodes, []string{"mid", "report", "source"}; !reflect.DeepEqual(got, want) {
		t.Errorf("exported nodes = %v, want %v", got, want)
	}
	if strings.Contains(out, "1e400") || strings.Contains(out, "stats") {
		t.Errorf("export leaks annotation content: %s", out)
	}
}

// Ignoring additional fields never relaxes the JSON syntax checks: a number
// that is not legal JSON — an unfinished exponent, a leading zero, a bare
// sign or dot — fails the whole import wherever it appears, with a nil graph
// and an error describing the document problem. Legal big numbers and
// malformed numbers must stay distinguishable.
func TestImportLineageRejectsMalformedNumbersInExtraFields(t *testing.T) {
	cases := []struct {
		name string
		text string
	}{
		{
			"unfinished exponent at top level",
			`{"nodes":["raw","report"],"edges":[{"from":"raw","to":"report"}],` +
				`"stats":{"rows":1e}}`,
		},
		{
			"exponent sign without digits at top level",
			`{"nodes":["raw","report"],"edges":[{"from":"raw","to":"report"}],` +
				`"stats":{"rows":1e+}}`,
		},
		{
			"leading zero at top level",
			`{"nodes":["raw","report"],"edges":[{"from":"raw","to":"report"}],` +
				`"stats":{"rows":01}}`,
		},
		{
			"leading zero on a negative number",
			`{"nodes":["raw","report"],"edges":[{"from":"raw","to":"report"}],` +
				`"stats":{"rows":-01}}`,
		},
		{
			"trailing dot",
			`{"nodes":["raw","report"],"edges":[{"from":"raw","to":"report"}],` +
				`"stats":{"rows":1.}}`,
		},
		{
			"missing integer part",
			`{"nodes":["raw","report"],"edges":[{"from":"raw","to":"report"}],` +
				`"stats":{"rows":.5}}`,
		},
		{
			"leading plus sign",
			`{"nodes":["raw","report"],"edges":[{"from":"raw","to":"report"}],` +
				`"stats":{"rows":+1}}`,
		},
		{
			"unfinished exponent inside an edge object's unknown field",
			`{"nodes":["raw","report"],"edges":[{"from":"raw","to":"report","weight":1e}]}`,
		},
		{
			"leading zero nested in an annotation array",
			`{"nodes":["raw","report"],"edges":[{"from":"raw","to":"report"}],` +
				`"stats":{"perShard":[{"rows":01}]}}`,
		},
		{
			"unfinished exponent deep in nested containers",
			`{"nodes":["raw","report"],"edges":[{"from":"raw","to":"report"}],` +
				`"stats":{"outer":[{"inner":[1e]}]}}`,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assertImportFails(t, tc.text, "JSON object")
		})
	}

	// Valid nodes and edges earlier in the document do not produce a partial
	// graph: the caller gets nil, never a graph with some nodes registered.
	graph, err := ImportLineage(`{"nodes":["raw","mid","report"],"edges":[` +
		`{"from":"raw","to":"mid"},{"from":"mid","to":"report"}],` +
		`"stats":{"rows":1e}}`)
	if err == nil || !strings.Contains(err.Error(), "JSON object") {
		t.Fatalf("want document format error, got graph=%v err=%v", graph, err)
	}
	if graph != nil {
		t.Fatalf("want nil graph after malformed number, got %v", graph)
	}
}

// A duplicated object key inside an annotation fails the whole import even
// when both values are legal big numbers: the second value never replaces the
// first, the graph is nil and the error names the duplicated key.
func TestImportLineageRejectsDuplicateKeysWithLargeNumberValues(t *testing.T) {
	cases := []struct {
		name string
		text string
		want []string
	}{
		{
			"both values are exponent-overflowing numbers",
			`{"nodes":["raw","report"],"edges":[{"from":"raw","to":"report"}],` +
				`"stats":{"rows":1e400,"rows":-1e400}}`,
			[]string{"duplicated", "rows"},
		},
		{
			"both values are hundred-digit integers",
			`{"nodes":["raw","report"],"edges":[{"from":"raw","to":"report"}],` +
				`"stats":{"rows":` + bigIntLiteral + `,"rows":` + bigIntLiteral + `}}`,
			[]string{"duplicated", "rows"},
		},
		{
			"duplicate inside an edge object's unknown field",
			`{"nodes":["raw","report"],"edges":[{"from":"raw","to":"report",` +
				`"stats":{"weight":1e400,"weight":1e-400}}]}`,
			[]string{"duplicated", "weight"},
		},
		{
			"duplicate inside an object nested in an annotation array",
			`{"nodes":["raw","report"],"edges":[{"from":"raw","to":"report"}],` +
				`"stats":{"perShard":[{"rows":1e400,"rows":` + bigIntLiteral + `}]}}`,
			[]string{"duplicated", "rows"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assertImportFails(t, tc.text, tc.want...)
		})
	}

	// No partial graph: the declared lineage before the bad annotation is
	// not handed back half-registered.
	graph, err := ImportLineage(`{"nodes":["raw","mid","report"],"edges":[` +
		`{"from":"raw","to":"mid"},{"from":"mid","to":"report"}],` +
		`"stats":{"rows":1e400,"rows":1e400}}`)
	if err == nil || !strings.Contains(err.Error(), "duplicated") {
		t.Fatalf("want duplicate-key error, got graph=%v err=%v", graph, err)
	}
	if graph != nil {
		t.Fatalf("want nil graph after duplicate key, got %v", graph)
	}
}
