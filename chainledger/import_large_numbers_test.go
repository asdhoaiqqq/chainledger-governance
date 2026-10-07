package chainledger

import (
	"fmt"
	"reflect"
	"strings"
	"testing"
)

// Statistics carried in extra fields are annotations, not lineage. A JSON number
// in an extra field is accepted purely on its JSON syntax: whether it is
// representable as a float64 is irrelevant, because extra values are never
// decoded into statistics or stored as dataset metadata. This regression guard
// pins that contract against a stricter float-based decoder: numbers that
// overflow (1e400, -1e400), underflow (1e-400) or refuse to round-trip through
// a float64 (a hundred-plus-digit integer) must all import wherever extra
// fields are allowed — at the top level, directly on a dependency object, and
// nested to any depth inside annotation objects and arrays.
func TestImportLineageAcceptsLargeNonFloatableNumbersInExtraFields(t *testing.T) {
	hugeInteger := strings.Repeat("9", 120) // far beyond int64 and float64 precision
	literals := []string{
		"1e400",     // positive overflow in exponential notation
		"-1e400",    // negative overflow in exponential notation
		"1e-400",    // positive underflow (rounds to zero in float64)
		"-1e-400",   // negative underflow
		"1E400",     // capital-E spelling is the same JSON syntax
		"1.5e300",   // finite but at the edge of the float64 range
		hugeInteger, // 120-digit integer, no exponent
		"-" + hugeInteger,
		strings.Repeat("1234567890", 11), // 110 digits alternating digits
	}

	// Every position is an "extra" location: the literal must not be read as a
	// node name or an edge endpoint, and the declared chain is all that lands in
	// the graph.
	positions := []struct {
		name string
		doc  func(lit string) string
	}{
		{
			"top-level extra field",
			func(lit string) string {
				return `{"nodes":["raw","mid","report"],"edges":[` +
					`{"from":"raw","to":"mid"},{"from":"mid","to":"report"}],` +
					`"stat":` + lit + `}`
			},
		},
		{
			"extra field first in a dependency object",
			func(lit string) string {
				return `{"nodes":["raw","mid","report"],"edges":[` +
					`{"stat":` + lit + `,"from":"raw","to":"mid"},` +
					`{"from":"mid","to":"report"}]}`
			},
		},
		{
			"extra field last in a dependency object",
			func(lit string) string {
				return `{"nodes":["raw","mid","report"],"edges":[` +
					`{"from":"raw","to":"mid"},{"from":"mid","to":"report","stat":` + lit + `}]}`
			},
		},
		{
			"nested annotation object",
			func(lit string) string {
				return `{"nodes":["raw","mid","report"],"edges":[` +
					`{"from":"raw","to":"mid"},{"from":"mid","to":"report"}],` +
					`"note":{"stats":{"rows":` + lit + `}}}`
			},
		},
		{
			"nested annotation array",
			func(lit string) string {
				return `{"nodes":["raw","mid","report"],"edges":[` +
					`{"from":"raw","to":"mid"},{"from":"mid","to":"report"}],` +
					`"note":{"stats":[` + lit + `]}}`
			},
		},
		{
			"array of nested objects",
			func(lit string) string {
				return `{"nodes":["raw","mid","report"],"edges":[` +
					`{"from":"raw","to":"mid"},{"from":"mid","to":"report"}],` +
					`"tags":[{"rows":` + lit + `}]}`
			},
		},
		{
			"deeply interleaved objects and arrays",
			func(lit string) string {
				return `{"nodes":["raw","mid","report"],"edges":[` +
					`{"from":"raw","to":"mid"},{"from":"mid","to":"report"}],` +
					`"note":{"outer":[{"inner":[[` + lit + `],{"deep":` + lit + `}]}]}}`
			},
		},
		{
			"bare numeric array elements",
			func(lit string) string {
				return `{"nodes":["raw","mid","report"],"edges":[` +
					`{"from":"raw","to":"mid"},{"from":"mid","to":"report"}],` +
					`"stats":[0,` + lit + `,-0]}`
			},
		},
	}

	for _, lit := range literals {
		for _, pos := range positions {
			name := lit
			if len(name) > 12 {
				name = name[:5] + "...(" + fmt.Sprintf("%d", len(lit)) + "-digit)"
			}
			t.Run(name+"/"+pos.name, func(t *testing.T) {
				graph := mustImport(t, pos.doc(lit))
				assertConsistent(t, graph)
				if got, want := len(graph), 3; got != want {
					t.Fatalf("graph has %d nodes %v, want exactly the declared chain of %d", got, graph, want)
				}
				assertEntry(t, graph, "raw", nil, []string{"mid"})
				assertEntry(t, graph, "mid", []string{"raw"}, []string{"report"})
				assertEntry(t, graph, "report", []string{"mid"}, nil)

				// The literal is annotation data: no dataset is named after it,
				// and the export of the chain carries no trace of the number.
				if _, ok := graph[lit]; ok {
					t.Fatalf("the statistic literal %q became a dataset node", lit)
				}
				out := mustExport(t, graph, "report")
				if want := `{"nodes":["mid","raw","report"],"edges":[{"from":"mid","to":"report"},` +
					`{"from":"raw","to":"mid"}]}`; out != want {
					t.Errorf("export = %s, want %s", out, want)
				}
				if strings.Contains(out, "e400") || strings.Contains(out, "e-400") ||
					strings.Contains(out, "stats") || strings.Contains(out, "note") {
					t.Errorf("export leaked annotation data: %s", out)
				}
			})
		}
	}
}

// The full mixed scenario: source reaches report both directly and through the
// longer source -> b -> mid -> report branch, while a sits on a second
// two-edge branch and isolated is declared independently. Extra fields carry
// large statistics at the top level, on the dependency objects themselves, and
// deep inside annotation objects and arrays, alongside other dataset names and
// from/to-looking objects. Changing the statistic values or moving the fields
// around must change nothing: graph, direct upstream/downstream relations, the
// shortest-path query choice (the direct edge, distance one), the longer
// branch's survival in the full upstream closure, both exports, and their
// ordering. The explicitly listed independent dataset stays; annotation names
// never become nodes or relations; no statistic is stored as metadata.
func TestImportLineageExtraStatisticsLeaveLineageQueriesAndExportsIdentical(t *testing.T) {
	huge1 := strings.Repeat("9", 120)
	huge2 := strings.Repeat("7", 131)

	baseline := `{"nodes":["source","a","b","mid","report","isolated"],"edges":[` +
		`{"from":"source","to":"report"},` +
		`{"from":"source","to":"a"},{"from":"a","to":"report"},` +
		`{"from":"source","to":"b"},{"from":"b","to":"mid"},{"from":"mid","to":"report"}]}`

	// Every variant declares exactly the same lineage as baseline; they differ
	// only in where the annotations sit and which statistic values they carry.
	variants := []struct {
		name string
		text string
	}{
		{
			"stats object before nodes",
			`{"stats":{"rows":1e400,"negative":-1e400,"tiny":1e-400,"huge":` + huge1 + `},` +
				`"nodes":["source","a","b","mid","report","isolated"],"edges":[` +
				`{"from":"source","to":"report"},` +
				`{"from":"source","to":"a"},{"from":"a","to":"report"},` +
				`{"from":"source","to":"b"},{"from":"b","to":"mid"},{"from":"mid","to":"report"}]}`,
		},
		{
			"statistics carried directly by dependency objects",
			`{"nodes":["source","a","b","mid","report","isolated"],"edges":[` +
				`{"weight":1e400,"from":"source","to":"report"},` +
				`{"from":"source","to":"a"},{"from":"a","to":"report","confidence":1e-400},` +
				`{"from":"source","to":"b","meta":{"count":` + huge1 + `}},` +
				`{"from":"b","to":"mid"},{"from":"mid","to":"report","delta":-1e400}]}`,
		},
		{
			"deep annotations after edges with decoy dataset names and relations",
			`{"nodes":["source","a","b","mid","report","isolated"],"edges":[` +
				`{"from":"source","to":"report"},` +
				`{"from":"source","to":"a"},{"from":"a","to":"report"},` +
				`{"from":"source","to":"b"},{"from":"b","to":"mid"},{"from":"mid","to":"report"}],` +
				`"notes":{"summary":"statistics live here","datasets":["ghost","phantom"],` +
				`"relation":{"from":"ghost","to":"phantom"},` +
				`"history":[{"from":"phantom","to":"ghost","rows":[1e-400,` + huge1 + `,-1e400]}],` +
				`"deep":{"layers":[{"values":[[1e400],{"score":-1e-400}]}]}}}`,
		},
		{
			"reordered fields with changed statistic values",
			`{"edges":[{"from":"source","to":"a"},{"from":"a","to":"report"},` +
				`{"from":"source","to":"b"},{"from":"b","to":"mid"},{"from":"mid","to":"report"},` +
				`{"from":"source","to":"report"}],` +
				`"notes":{"n":1e401,"m":-1e401,"arr":[[1e-401,` + huge2 + `]]},` +
				`"nodes":["isolated","report","mid","b","a","source"]}`,
		},
		{
			"bare numeric members at the top level",
			`{"nodes":["source","a","b","mid","report","isolated"],"edges":[` +
				`{"from":"source","to":"report"},` +
				`{"from":"source","to":"a"},{"from":"a","to":"report"},` +
				`{"from":"source","to":"b"},{"from":"b","to":"mid"},{"from":"mid","to":"report"}],` +
				`"total":1e400,"ratio":1e-400,"count":` + huge2 + `}`,
		},
	}

	wantGraph := mustImport(t, baseline)
	wantSnapshot := snapshot(wantGraph)

	// The full upstream export keeps every branch and the existing node/edge
	// ordering, with no statistics anywhere.
	wantExport := `{"nodes":["a","b","mid","report","source"],"edges":[` +
		`{"from":"a","to":"report"},{"from":"b","to":"mid"},` +
		`{"from":"mid","to":"report"},{"from":"source","to":"a"},` +
		`{"from":"source","to":"b"},{"from":"source","to":"report"}]}`
	if got := mustExport(t, wantGraph, "report"); got != wantExport {
		t.Fatalf("baseline export = %s, want %s", got, wantExport)
	}
	if got := mustExportScoped(t, wantGraph, "source", "report"); got != wantExport {
		t.Fatalf("baseline scoped export = %s, want %s", got, wantExport)
	}

	for _, variant := range variants {
		t.Run(variant.name, func(t *testing.T) {
			graph := mustImport(t, variant.text)
			assertConsistent(t, graph)

			// Same lineage result regardless of statistic values or placement:
			// snapshot equality also proves no statistic survives as dataset
			// metadata on any entry.
			if got := snapshot(graph); !reflect.DeepEqual(got, wantSnapshot) {
				t.Fatalf("annotated graph %v, want the baseline graph %v", got, wantSnapshot)
			}
			if got, want := len(graph), 6; got != want {
				t.Fatalf("graph has %d nodes, want %d declared datasets", got, want)
			}

			// Direct relations survive unchanged in both directions, including
			// the direct source -> report edge and the longer branch edges.
			assertEntry(t, graph, "source", nil, []string{"a", "b", "report"})
			assertEntry(t, graph, "a", []string{"source"}, []string{"report"})
			assertEntry(t, graph, "b", []string{"source"}, []string{"mid"})
			assertEntry(t, graph, "mid", []string{"b"}, []string{"report"})
			assertEntry(t, graph, "report", []string{"a", "mid", "source"}, nil)
			assertEntry(t, graph, "isolated", nil, nil)

			// The independent dataset explicitly listed in nodes is retained.
			if got, want := Roots(graph), []string{"isolated", "source"}; !reflect.DeepEqual(got, want) {
				t.Errorf("Roots = %v, want %v", got, want)
			}

			// Names and from/to-looking relations seen only inside annotations
			// are neither nodes nor edges.
			for _, leaked := range []string{"ghost", "phantom", "stats", "notes", "relation"} {
				if _, ok := graph[leaked]; ok {
					t.Errorf("annotation content %q leaked into the graph", leaked)
				}
				if _, err := Impacts(graph, leaked); err == nil ||
					!strings.Contains(err.Error(), leaked) {
					t.Errorf("query for annotation name %q must fail as unregistered, got %v", leaked, err)
				}
			}

			// Downstream impact keeps choosing the direct route at distance one.
			impacts := mustImpacts(t, graph, "source")
			assertImpactOnce(t, impacts, "report", 1, []string{"source", "report"})
			assertImpactOnce(t, impacts, "a", 1, []string{"source", "a"})
			assertImpactOnce(t, impacts, "b", 1, []string{"source", "b"})
			assertImpactOnce(t, impacts, "mid", 2, []string{"source", "b", "mid"})

			// The full upstream closure still carries the longer branch through
			// b and mid even though source reaches report directly.
			upstreams := mustUpstreams(t, graph, "report")
			assertUpstreamOnce(t, upstreams, "source", 1, []string{"source", "report"})
			assertUpstreamOnce(t, upstreams, "a", 1, []string{"a", "report"})
			assertUpstreamOnce(t, upstreams, "mid", 1, []string{"mid", "report"})
			assertUpstreamOnce(t, upstreams, "b", 2, []string{"b", "mid", "report"})

			// Both exports are byte-identical to the annotation-free document:
			// same sorting, same branches, no statistics.
			if got := mustExport(t, graph, "report"); got != wantExport {
				t.Errorf("full export = %s, want %s", got, wantExport)
			}
			if got := mustExportScoped(t, graph, "source", "report"); got != wantExport {
				t.Errorf("scoped export = %s, want %s", got, wantExport)
			}
		})
	}
}

// Ignoring extra fields never means tolerating a malformed document. A number
// that is illegal JSON — an unfinished exponent, an exponent sign with no
// digits, a superfluous leading zero — fails the whole import at any extra
// position with a document-format error and a nil graph. A legal large number
// elsewhere in the same document does not rescue the illegal one, and a legal
// number alone never fails: legal and illegal literals must not be conflated.
func TestImportLineageRejectsMalformedJSONNumbersInExtraFields(t *testing.T) {
	const chain = `{"nodes":["raw","mid","report"],"edges":[` +
		`{"from":"raw","to":"mid"},{"from":"mid","to":"report"}]`
	cases := []struct {
		name string
		text string
	}{
		{"unfinished exponent at top level", chain + `,"bad":1e}`},
		{"capital-E unfinished exponent", chain + `,"bad":3E}`},
		{"exponent sign without digits", chain + `,"bad":1e+}`},
		{"negative exponent without digits", chain + `,"bad":2e-}`},
		{"leading zero at top level", chain + `,"bad":01}`},
		{"negative leading zero", chain + `,"bad":-01}`},
		{"two decimal points", chain + `,"bad":1.2.3}`},
		{"leading zero before a decimal point", chain + `,"bad":00.5}`},
		{"leading zero in a nested object", chain + `,"note":{"bad":01}}`},
		{"unfinished exponent in a nested object", chain + `,"note":{"bad":1e}}`},
		{"leading zero in a nested array", chain + `,"note":{"vals":[01]}}`},
		{"unfinished exponent inside an array of objects", chain + `,"tags":[{"x":1e}]}`},
		{"illegal number inside a dependency object",
			`{"nodes":["raw","mid","report"],"edges":[` +
				`{"from":"raw","to":"mid","x":01},{"from":"mid","to":"report"}]}`},
		{"illegal number deep inside arrays", chain + `,"note":{"outer":[[[{"x":1e}]]]}}`},
		{"legal big number does not rescue malformed sibling",
			chain + `,"stats":{"ok":1e400,"bad":01}}`},
		{"malformed top-level number beside a legal nested one",
			`{"nodes":["raw"],"edges":[],"ok":{"x":1e-400},"bad":-01}`},
		{
			"valid nodes and edges registered before the bad number",
			`{"nodes":["raw","a","b","mid","report","isolated"],"edges":[` +
				`{"from":"raw","to":"a"},{"from":"a","to":"mid"},{"from":"mid","to":"report"},` +
				`{"from":"raw","to":"report"}],"stats":{"rows":1e400},"bad":01}`,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// The graph must be nil: a format failure can never hand back a map
			// in which the valid nodes listed before the error were registered.
			assertImportFails(t, tc.text, "complete JSON object")
		})
	}

	// Explicit legal/illegal contrast with identical structure: only the
	// malformed spelling fails, and the failure returns nil rather than the
	// already-declared chain.
	legal := chain + `,"stats":{"rows":1e400}}`
	graph := mustImport(t, legal)
	if got, want := len(graph), 3; got != want {
		t.Fatalf("legal document imported %d nodes %v, want %d", got, graph, want)
	}
	returned, err := ImportLineage(chain + `,"stats":{"rows":01}}`)
	if err == nil {
		t.Fatalf("malformed number imported successfully: %v", returned)
	}
	if returned != nil {
		t.Fatalf("malformed number returned a partial graph %v, want nil", returned)
	}
	if !strings.Contains(err.Error(), "complete JSON object") {
		t.Fatalf("error %q must describe the document format problem", err)
	}
}

// A duplicated key stays a duplicated key even when both values are legal large
// numbers that a float64 could never hold. The document must be rejected with a
// nil graph and an error naming the repeated key; last-value-wins semantics are
// not accepted at any nesting depth, including on dependency objects.
func TestImportLineageRejectsDuplicateKeysWithLargeNumberValues(t *testing.T) {
	huge := strings.Repeat("9", 120)
	cases := []struct {
		name string
		text string
		key  string
	}{
		{
			"positive and negative overflow as duplicate values",
			`{"nodes":["raw","report"],"edges":[{"from":"raw","to":"report"}],` +
				`"stats":{"rows":1e400,"rows":-1e400}}`,
			"rows",
		},
		{
			"identical underflow values still duplicate",
			`{"nodes":["raw"],"edges":[],"stats":{"rows":1e-400,"rows":1e-400}}`,
			"rows",
		},
		{
			"two hundred-digit integers as duplicate values",
			`{"nodes":["raw"],"edges":[],"stats":{"rows":` + huge + `,"rows":` + huge + `}}`,
			"rows",
		},
		{
			"duplicate inside an array element object",
			`{"nodes":["raw"],"edges":[],"tags":[{"rows":` + huge + `,"rows":1e400}]}`,
			"rows",
		},
		{
			"duplicate nested deeply among arrays",
			`{"nodes":["raw"],"edges":[],"notes":{"outer":[{"inner":[{"rows":-1e400,"rows":-1e-400}]}]}}`,
			"rows",
		},
		{
			"duplicate key on a dependency object",
			`{"nodes":["raw","report"],"edges":[{"from":"raw","to":"report",` +
				`"weight":1e400,"weight":1e-400}]}`,
			"weight",
		},
		{
			"duplicate top-level members carrying large numbers",
			`{"nodes":["raw"],"edges":[],"count":1e400,"count":1e-400}`,
			"count",
		},
		{
			"later member repeats an earlier large-integer member",
			`{"nodes":["raw","mid","report"],"edges":[` +
				`{"from":"raw","to":"mid"},{"from":"mid","to":"report"}],` +
				`"total":` + huge + `,"other":1,"total":-1e400}`,
			"total",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assertImportFails(t, tc.text, "duplicated", tc.key)
		})
	}
}
