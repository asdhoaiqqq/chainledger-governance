package chainledger

import (
	"reflect"
	"strings"
	"testing"
)

// Regression coverage for endpoint recognition inside edge objects. The
// endpoints are identified by their exact decoded JSON key name: only a key
// that decodes to precisely "from" or "to" supplies an endpoint. Case variants
// ("From", "FROM", "To", "TO", ...) are unknown fields: their values are
// neither validated nor used, can never reroute or create a dependency, and a
// document that supplies an endpoint only through such a variant must fail as
// missing that endpoint. These tests pin that behavior from the graph itself —
// direct parent/child lists, existing queries, Roots and re-exported
// documents — never from a mere absence of errors.

// assertRawReportOnly is the one relation all "variant ignored" documents must
// produce: raw derives report and nothing else.
func assertRawReportOnly(t *testing.T, graph map[string]*Lineage, independent ...string) {
	t.Helper()
	assertConsistent(t, graph)
	assertEntry(t, graph, "raw", nil, []string{"report"})
	assertEntry(t, graph, "report", []string{"raw"}, nil)

	// Query level: raw reaches exactly report; an Upstreams answer names raw
	// once. A variant that pointed at another listed node must not have
	// broadened either result.
	impacts := mustImpacts(t, graph, "raw")
	if got := impactNames(impacts); !reflect.DeepEqual(got, []string{"report"}) {
		t.Errorf("Impacts(raw) = %v, want [report]", got)
	}
	assertImpactOnce(t, impacts, "report", 1, []string{"raw", "report"})
	upstreams := mustUpstreams(t, graph, "report")
	if got := upstreamNames(upstreams); !reflect.DeepEqual(got, []string{"raw"}) {
		t.Errorf("Upstreams(report) = %v, want [raw]", got)
	}
	assertUpstreamOnce(t, upstreams, "raw", 1, []string{"raw", "report"})

	for _, name := range independent {
		assertEntry(t, graph, name, nil, nil)
		if hits := mustImpacts(t, graph, name); len(hits) != 0 {
			t.Errorf("independent node %q must have no downstream, got %v", name, impactNames(hits))
		}
	}

	// Re-export level: the document carries the single real edge and none of
	// the nodes only touched by a variant value.
	want := `{"nodes":["raw","report"],"edges":[{"from":"raw","to":"report"}]}`
	if got := mustExport(t, graph, "report"); got != want {
		t.Errorf("export of report = %s, want %s", got, want)
	}
}

// A document that gives both the correct lowercase endpoint and a case variant
// imports exactly the lowercase relation. "From" writing another listed node
// must not add a second upstream/downstream, "To" writing another node must
// not reroute the dependency, and the variant's position in the object changes
// nothing. other stays an independent node; an unregistered variant value
// invents no node either.
func TestImportLineageCaseVariantsNeverSupplyEndpoints(t *testing.T) {
	cases := []struct {
		name string
		edge string
	}{
		{"From before from", `{"From":"other","from":"raw","to":"report"}`},
		{"From after from", `{"from":"raw","to":"report","From":"other"}`},
		{"FROM in the middle", `{"from":"raw","FROM":"other","to":"report"}`},
		{"To before to", `{"from":"raw","To":"other","to":"report"}`},
		{"TO after to", `{"from":"raw","to":"report","TO":"other"}`},
		{"both variants first", `{"From":"other","To":"other","from":"raw","to":"report"}`},
		{"both variants last", `{"from":"raw","to":"report","FROM":"other","tO":"other"}`},
		{"mixed case letters", `{"fRoM":"other","fRom":"other","from":"raw","to":"report"}`},
		{"unregistered variant values", `{"from":"raw","to":"report","From":"ghost","To":"phantom"}`},
	}

	var firstExport string
	var firstGraph map[string]*Lineage
	for i, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			text := `{"nodes":["raw","other","report"],"edges":[` + tc.edge + `]}`
			graph := mustImport(t, text)

			if got, want := len(graph), 3; got != want {
				t.Fatalf("graph = %v, want exactly the three listed nodes", graph)
			}
			assertRawReportOnly(t, graph, "other")

			// other is independent even though a variant named it; Roots lists
			// it alongside raw.
			if got, want := Roots(graph), []string{"other", "raw"}; !reflect.DeepEqual(got, want) {
				t.Errorf("Roots = %v, want %v", got, want)
			}
			if got := mustExport(t, graph, "other"); got != `{"nodes":["other"],"edges":[]}` {
				t.Errorf("independent other must export alone, got %s", got)
			}

			// Values mentioned only by variants never become datasets.
			for _, leaked := range []string{"ghost", "phantom"} {
				if _, ok := graph[leaked]; ok {
					t.Errorf("variant value %q leaked into the graph: %v", leaked, graph)
				}
				if _, err := Impacts(graph, leaked); err == nil ||
					!strings.Contains(err.Error(), leaked) {
					t.Errorf("query for variant-only name %q must fail as unregistered, got %v", leaked, err)
				}
			}

			// Member order must be invisible: every ordering builds one graph
			// and one canonical export.
			out := mustExport(t, graph, "report")
			if i == 0 {
				firstExport, firstGraph = out, snapshotExportGraph(graph)
			} else if out != firstExport {
				t.Errorf("export %s differs from first ordering %s", out, firstExport)
			} else if !reflect.DeepEqual(snapshotExportGraph(graph), firstGraph) {
				t.Errorf("graph differs from first ordering: %v vs %v", graph, firstGraph)
			}
		})
	}
}

// Values carried by case-variant keys are not lineage endpoints, so they are
// never validated as names: an unregistered name, a number, a boolean, null,
// an array, an object (even one that looks like an edge or a whole document),
// and a string holding an unpaired surrogate escape are all ignored while the
// real lowercase endpoints are valid. Nothing of that content may enter the
// graph as a node or a dependency.
func TestImportLineageCaseVariantValuesAreNotValidated(t *testing.T) {
	cases := []struct {
		name   string
		member string // raw JSON member inserted into the edge object
	}{
		{"unregistered name", `"From":"ghost"`},
		{"integer", `"FROM":1`},
		{"fraction", `"To":1.5`},
		{"boolean", `"From":true`},
		{"null", `"To":null`},
		{"array", `"From":["ghost"]`},
		{"object holding an edge-looking relation", `"To":{"from":"ghost","to":"phantom"}`},
		{"object holding a nodes-looking list and edge",
			`"FROM":{"nodes":["ghost"],"edges":[{"from":"ghost","to":"phantom"}]}`},
		{"lone high surrogate string", `"From":"\ud800"`},
		{"lone low surrogate inside text", `"To":"x\ude00y"`},
		{"object holding a surrogate string",
			`"FROM":{"note":"\ud800","edges":[{"from":"ghost","to":"phantom"}]}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			text := `{"nodes":["raw","report"],"edges":[{"from":"raw",` + tc.member +
				`,"to":"report"}]}`
			graph := mustImport(t, text)
			if got, want := len(graph), 2; got != want {
				t.Fatalf("graph = %v, want exactly raw and report", graph)
			}
			assertRawReportOnly(t, graph)
			for _, leaked := range []string{"ghost", "phantom"} {
				if _, ok := graph[leaked]; ok {
					t.Errorf("unknown-field content %q leaked into the graph: %v", leaked, graph)
				}
			}
		})
	}

	// The same rule at the top level: an annotation string with an unpaired
	// surrogate and annotation content shaped like nodes/edges is ignored, and
	// the real document imports normally.
	t.Run("surrogate and edge-like content in top-level annotation", func(t *testing.T) {
		text := `{"nodes":["raw","report"],"edges":[{"from":"raw","to":"report"}],` +
			`"meta":{"label":"\ud800","nodes":["ghost"],` +
			`"edges":[{"from":"ghost","to":"phantom"}]}}`
		graph := mustImport(t, text)
		assertRawReportOnly(t, graph)
		for _, leaked := range []string{"ghost", "phantom", "meta", "label"} {
			if _, ok := graph[leaked]; ok {
				t.Errorf("annotation content %q leaked into the graph: %v", leaked, graph)
			}
		}
	})
}

// Endpoint keys are matched by their decoded JSON name: a key spelled with
// legal \u escapes that decodes to "from" or "to" still binds the endpoint,
// while an escape that decodes to a case variant, or an escaped backslash that
// yields literal text, does not.
func TestImportLineageEndpointKeysMatchDecodedText(t *testing.T) {
	t.Run("unicode escape spellings bind endpoints", func(t *testing.T) {
		cases := []struct {
			name string
			edge string
		}{
			{"both keys escaped", `{"\u0066rom":"raw","\u0074o":"report"}`},
			{"escaped to", `{"from":"raw","\u0074o":"report"}`},
			{"escaped from", `{"\u0066rom":"raw","to":"report"}`},
			{"escapes embedded inside the keys", `{"f\u0072om":"raw","t\u006f":"report"}`},
		}
		for _, tc := range cases {
			t.Run(tc.name, func(t *testing.T) {
				text := `{"nodes":["raw","report"],"edges":[` + tc.edge + `]}`
				graph := mustImport(t, text)
				assertRawReportOnly(t, graph)

				// The re-export renders plain lowercase keys, proving the
				// escaped key really was "from"/"to".
				out := mustExport(t, graph, "report")
				if !strings.Contains(out, `{"from":"raw","to":"report"}`) {
					t.Errorf("escaped key did not bind the endpoints; export = %s", out)
				}
				reimported := mustImport(t, out)
				assertRawReportOnly(t, reimported)
			})
		}
	})

	t.Run("escapes decoding to case variants stay unknown", func(t *testing.T) {
		// F is "F": the decoded key is "From", an unknown field.
		assertImportFails(t,
			`{"nodes":["raw","report"],"edges":[{"\u0046rom":"raw","to":"report"}]}`,
			`missing its upstream endpoint ("from")`)
		// R is "R": the key decodes to "fRom".
		assertImportFails(t,
			`{"nodes":["raw","report"],"edges":[{"f\u0052om":"raw","to":"report"}]}`,
			`missing its upstream endpoint ("from")`)
		// tO decodes to "tO", so "to" is absent.
		assertImportFails(t,
			`{"nodes":["raw","report"],"edges":[{"from":"raw","t\u004f":"report"}]}`,
			`missing its derived endpoint ("to")`)
	})

	t.Run("escaped backslash yields a literal unknown key", func(t *testing.T) {
		// "\\u0066rom" decodes to the literal text from, not "from".
		assertImportFails(t,
			`{"nodes":["raw","report"],"edges":[{"\\u0066rom":"raw","to":"report"}]}`,
			`missing its upstream endpoint ("from")`)
	})

	t.Run("plain and escaped spellings of one endpoint key are duplicates", func(t *testing.T) {
		// Both keys decode to "from": structural validation must reject the
		// object instead of letting one silently win.
		assertImportFails(t,
			`{"nodes":["raw","report"],"edges":[{"from":"raw","from":"x","to":"report"}]}`,
			"duplicated", "from")
		assertImportFails(t,
			`{"nodes":["raw","report"],"edges":[{"from":"raw","to":"raw","to":"report"}]}`,
			"duplicated", "to")
	})
}

// An endpoint supplied only by a case variant (or any key that does not decode
// to the lowercase name) is simply absent: the whole import fails, the graph
// is nil, and the error states which lowercase endpoint is missing. A legal
// edge appearing earlier in the document must not produce a partial graph.
func TestImportLineageVariantOnlyEndpointsFailWholeImport(t *testing.T) {
	cases := []struct {
		name string
		text string
		want []string
	}{
		{
			"From only",
			`{"nodes":["raw","report"],"edges":[{"From":"raw","to":"report"}]}`,
			[]string{`missing its upstream endpoint ("from")`},
		},
		{
			"FROM only",
			`{"nodes":["raw","report"],"edges":[{"FROM":"raw","to":"report"}]}`,
			[]string{`missing its upstream endpoint ("from")`},
		},
		{
			"To only",
			`{"nodes":["raw","report"],"edges":[{"from":"raw","To":"report"}]}`,
			[]string{`missing its derived endpoint ("to")`},
		},
		{
			"TO only",
			`{"nodes":["raw","report"],"edges":[{"from":"raw","TO":"report"}]}`,
			[]string{`missing its derived endpoint ("to")`},
		},
		{
			"both endpoints variants",
			`{"nodes":["raw","report"],"edges":[{"From":"raw","To":"report"}]}`,
			[]string{`missing its upstream endpoint ("from")`},
		},
		{
			"escaped uppercase From only",
			`{"nodes":["raw","report"],"edges":[{"\u0046rom":"raw","to":"report"}]}`,
			[]string{`missing its upstream endpoint ("from")`},
		},
		{
			"literal backslash key only",
			`{"nodes":["raw","report"],"edges":[{"\\u0066rom":"raw","to":"report"}]}`,
			[]string{`missing its upstream endpoint ("from")`},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assertImportFails(t, tc.text, tc.want...)
		})
	}

	// The variant value naming a listed node changes nothing: without the
	// lowercase key the endpoint is still missing.
	t.Run("listed node as variant value does not rescue the edge", func(t *testing.T) {
		text := `{"nodes":["raw","other","report"],` +
			`"edges":[{"from":"raw","to":"other"},{"From":"other","to":"report"}]}`
		assertImportFails(t, text, `missing its upstream endpoint ("from")`)
	})

	// A fully valid first edge must not come back as a partial result when a
	// later edge relies on case variants.
	t.Run("valid first edge does not survive the failure", func(t *testing.T) {
		text := `{"nodes":["raw","other","report"],` +
			`"edges":[{"from":"raw","to":"other"},{"from":"other","To":"report"}]}`
		graph, err := ImportLineage(text)
		if err == nil {
			t.Fatalf("import succeeded with graph %v, want missing-to error", graph)
		}
		if !strings.Contains(err.Error(), `missing its derived endpoint ("to")`) {
			t.Fatalf("error %q must name the missing lowercase to endpoint", err.Error())
		}
		if graph != nil {
			t.Fatalf("want nil graph after the second edge failed, got %v", graph)
		}
	})
}

// Ignoring unknown fields never means accepting a damaged document: structural
// validation, duplicate-key validation and validation of the real lowercase
// endpoints all keep running with case variants present, and exact-key
// recognition applies to the top-level fields too.
func TestImportLineageUnknownFieldsDoNotDisableValidation(t *testing.T) {
	cases := []struct {
		name string
		text string
		want []string
	}{
		{
			"top-level Nodes does not supply nodes",
			`{"Nodes":["a"],"edges":[]}`,
			[]string{`missing the "nodes" array`},
		},
		{
			"top-level Edges does not supply edges",
			`{"nodes":[],"Edges":[]}`,
			[]string{`missing the "edges" array`},
		},
		{
			"duplicated variant key inside an edge",
			`{"nodes":["a","b"],"edges":[{"from":"a","to":"b","From":"a","From":"b"}]}`,
			[]string{"duplicated", "From"},
		},
		{
			"duplicated real key after the arrays with an unknown field present",
			`{"nodes":[],"edges":[],"edges":[],"x":1}`,
			[]string{"duplicated", "edges"},
		},
		{
			"truncated unknown field value",
			`{"nodes":[],"edges":[],"x":`,
			[]string{"complete"},
		},
		{
			"invalid UTF-8 inside an unknown annotation string",
			"{\"nodes\":[\"a\"],\"edges\":[],\"x\":\"\xff\"}",
			[]string{"UTF-8"},
		},
		{
			"real to endpoint not listed despite variants",
			`{"nodes":["a"],"edges":[{"from":"a","From":"x","to":"ghost"}]}`,
			[]string{"endpoint", "ghost"},
		},
		{
			"unpaired surrogate in the real to endpoint",
			`{"nodes":["a"],"edges":[{"from":"a","To":"a","to":"\ud800"}]}`,
			[]string{"unpaired Unicode surrogate escape", `"to"`, `\ud800`},
		},
		{
			"non-string real from endpoint despite From variant",
			`{"nodes":["a"],"edges":[{"From":"a","from":1,"to":"a"}]}`,
			[]string{"edges", `"from"`},
		},
		{
			"cycle still detected through real endpoints",
			`{"nodes":["a","b"],"edges":[{"from":"a","to":"b"},{"From":"a","from":"b","to":"a"}]}`,
			[]string{"cycle", "a", "b"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assertImportFails(t, tc.text, tc.want...)
		})
	}
}
