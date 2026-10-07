package chainledger

import (
	"reflect"
	"strings"
	"testing"
)

// These tests pin down how an edge object's endpoint fields are recognized:
// only keys that decode to exactly "from" and "to" set the upstream and
// derived endpoints. Case variants ("From", "FROM", "To", "TO", ...) are
// unknown fields like any other — ignored, their values never validated or
// used — so they can neither add, move nor invent a dependency, and an edge
// that carries only a case variant is rejected as missing that endpoint.
// Recognition happens on the decoded key name, so a JSON \u escape spelling
// of "from"/"to" still names the endpoint, while an escaped backslash turns
// the same text into an ordinary unknown field.

// A document that carries both the correct lowercase endpoints and case
// variants imports only the lowercase relation. The variant may sit before or
// after the real fields; either way report depends on raw alone, other stays
// an independent node, and nothing the variant names enters the lineage. The
// result is checked through the direct lists, the queries and the re-exported
// document — not merely through the absence of an error.
func TestImportLineageCaseVariantFieldsAreUnknown(t *testing.T) {
	documents := []struct {
		name string
		text string
	}{
		{
			"From before from",
			`{"nodes":["raw","other","report"],"edges":[` +
				`{"From":"other","from":"raw","to":"report"}]}`,
		},
		{
			"From after from",
			`{"nodes":["raw","other","report"],"edges":[` +
				`{"from":"raw","to":"report","From":"other"}]}`,
		},
		{
			"FROM between the real fields",
			`{"nodes":["raw","other","report"],"edges":[` +
				`{"from":"raw","FROM":"other","to":"report"}]}`,
		},
		{
			"To variant does not retarget the derived endpoint",
			`{"nodes":["raw","other","report"],"edges":[` +
				`{"from":"raw","To":"other","to":"report"}]}`,
		},
		{
			"TO variant after the real fields",
			`{"nodes":["raw","other","report"],"edges":[` +
				`{"from":"raw","to":"report","TO":"other"}]}`,
		},
		{
			"variants on both endpoints at once",
			`{"nodes":["raw","other","report"],"edges":[` +
				`{"FROM":"other","from":"raw","TO":"other","to":"report"}]}`,
		},
	}

	for _, tc := range documents {
		t.Run(tc.name, func(t *testing.T) {
			graph := mustImport(t, tc.text)
			assertConsistent(t, graph)

			// Exactly the three declared nodes exist; the variant did not
			// invent a node or attach other to anything.
			if got, want := len(graph), 3; got != want {
				t.Fatalf("graph has %d nodes %v, want %d", got, graph, want)
			}
			assertEntry(t, graph, "raw", nil, []string{"report"})
			assertEntry(t, graph, "report", []string{"raw"}, nil)
			assertEntry(t, graph, "other", nil, nil)

			// Queries see only the lowercase relation, from both directions.
			upstreams := mustUpstreams(t, graph, "report")
			if got, want := upstreamNames(upstreams), []string{"raw"}; !reflect.DeepEqual(got, want) {
				t.Errorf("upstreams of report = %v, want %v", got, want)
			}
			assertUpstreamOnce(t, upstreams, "raw", 1, []string{"raw", "report"})
			impacts := mustImpacts(t, graph, "raw")
			assertImpactOnce(t, impacts, "report", 1, []string{"raw", "report"})
			if got := mustImpacts(t, graph, "other"); len(got) != 0 {
				t.Errorf("other must stay independent, impacts = %v", impactNames(got))
			}
			if got, want := Roots(graph), []string{"other", "raw"}; !reflect.DeepEqual(got, want) {
				t.Errorf("Roots = %v, want %v", got, want)
			}

			// The re-exported document holds exactly the lowercase relation.
			out := mustExport(t, graph, "report")
			if want := `{"nodes":["raw","report"],"edges":[{"from":"raw","to":"report"}]}`; out != want {
				t.Errorf("export = %s, want %s", out, want)
			}
		})
	}
}

// A case variant whose value names a dataset the document does not list is
// still just an unknown field: the unregistered name is not validated against
// the node set, no node is invented for it, and the real endpoints alone
// decide the lineage.
func TestImportLineageCaseVariantNamingUnlistedDatasetStaysOut(t *testing.T) {
	text := `{"nodes":["raw","report"],"edges":[` +
		`{"from":"raw","to":"report","From":"ghost","TO":"phantom"}]}`

	graph := mustImport(t, text)
	assertConsistent(t, graph)

	if got, want := len(graph), 2; got != want {
		t.Fatalf("graph has %d nodes %v, want %d", got, graph, want)
	}
	assertEntry(t, graph, "raw", nil, []string{"report"})
	assertEntry(t, graph, "report", []string{"raw"}, nil)
	for _, leaked := range []string{"ghost", "phantom"} {
		if _, ok := graph[leaked]; ok {
			t.Errorf("case-variant value %q leaked into the graph as a node", leaked)
		}
		if _, err := Impacts(graph, leaked); err == nil ||
			!strings.Contains(err.Error(), leaked) {
			t.Errorf("query for variant value %q must fail as unregistered, got %v", leaked, err)
		}
	}
}

// Unknown edge fields are ignored wholesale, whatever their values look like:
// an unregistered name, a number, null, a nested object (even one holding its
// own from/to members), an array, or a string containing an unpaired
// surrogate escape. None of these trigger the name-type, missing-endpoint or
// name-encoding checks, because those checks apply to endpoints only, and
// none of the node- or dependency-looking content enters the graph.
func TestImportLineageUnknownEdgeFieldValuesNeverValidated(t *testing.T) {
	text := `{"nodes":["raw","mid","report"],"edges":[` +
		`{"from":"raw","to":"mid",` +
		`"From":"ghost","TO":42,"note":null,"flag":true,` +
		`"meta":{"from":"phantom","to":"specter"},"list":["phantom","specter"],` +
		`"bad":"\ud800"},` +
		`{"from":"mid","to":"report","From":["array","of","names"]}]}`

	graph := mustImport(t, text)
	assertConsistent(t, graph)

	if got, want := len(graph), 3; got != want {
		t.Fatalf("graph has %d nodes %v, want %d", got, graph, want)
	}
	assertEntry(t, graph, "raw", nil, []string{"mid"})
	assertEntry(t, graph, "mid", []string{"raw"}, []string{"report"})
	assertEntry(t, graph, "report", []string{"mid"}, nil)

	// Content that merely looks like nodes and dependencies inside unknown
	// fields is not lineage.
	for _, leaked := range []string{"ghost", "phantom", "specter", "meta", "bad"} {
		if _, ok := graph[leaked]; ok {
			t.Errorf("unknown-field content %q leaked into the graph", leaked)
		}
	}

	// The declared relations drive queries and the re-exported document.
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

// Ignoring unknown fields never relaxes the document-level checks: malformed
// JSON and duplicated object keys are still rejected, including when the
// problem sits inside an unknown field of an edge object — a repeated case
// variant is a duplicated key like any other.
func TestImportLineageUnknownEdgeFieldsStillStructurallyValidated(t *testing.T) {
	cases := []struct {
		name string
		text string
		want []string
	}{
		{
			"malformed unknown field value",
			`{"nodes":["raw","report"],"edges":[{"from":"raw","to":"report","note":}]}`,
			[]string{"JSON object"},
		},
		{
			"truncated inside unknown field",
			`{"nodes":["raw","report"],"edges":[{"from":"raw","to":"report","meta":{"a":1}`,
			[]string{"complete"},
		},
		{
			"duplicate case-variant key on an edge",
			`{"nodes":["raw","report"],"edges":[{"from":"raw","to":"report","From":"a","From":"b"}]}`,
			[]string{"duplicated", "From"},
		},
		{
			"duplicate key inside unknown edge field object",
			`{"nodes":["raw","report"],"edges":[{"from":"raw","to":"report","meta":{"m":1,"m":2}}]}`,
			[]string{"duplicated", "m"},
		},
		{
			"escaped spelling duplicates the variant key",
			`{"nodes":["raw","report"],"edges":[{"from":"raw","to":"report","From":1,"\u0046rom":2}]}`,
			[]string{"duplicated", "From"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assertImportFails(t, tc.text, tc.want...)
		})
	}
}

// Field recognition works on the decoded key name: writing "from" and "to"
// with legal \u escapes still sets the endpoints and connects the very same
// nodes, while doubling the backslash makes the key an ordinary literal text
// that names no endpoint — the edge then fails as missing its upstream field.
func TestImportLineageEndpointKeysRecognizedByDecodedName(t *testing.T) {
	t.Run("escaped spellings of from and to connect the endpoints", func(t *testing.T) {
		// The keys are written as "\u0066rom" and "\u0074o": both decode to
		// exactly "from" and "to", so they set the endpoints like the plain
		// spelling.
		text := `{"nodes":["raw","report"],"edges":[` +
			`{"\u0066rom":"raw","\u0074o":"report"}]}`
		graph := mustImport(t, text)
		assertConsistent(t, graph)
		if got, want := len(graph), 2; got != want {
			t.Fatalf("graph has %d nodes %v, want %d", got, graph, want)
		}
		assertEntry(t, graph, "raw", nil, []string{"report"})
		assertEntry(t, graph, "report", []string{"raw"}, nil)
		assertUpstreamOnce(t, mustUpstreams(t, graph, "report"), "raw", 1,
			[]string{"raw", "report"})
	})

	t.Run("partially escaped key still decodes to the endpoint name", func(t *testing.T) {
		// "f\u0072om" and "t\u006f": only some letters are escaped, the
		// decoded key is still exactly the endpoint name.
		text := `{"nodes":["raw","report"],"edges":[` +
			`{"f\u0072om":"raw","t\u006f":"report"}]}`
		graph := mustImport(t, text)
		assertEntry(t, graph, "raw", nil, []string{"report"})
		assertEntry(t, graph, "report", []string{"raw"}, nil)
	})

	t.Run("escaped backslash makes the key an unknown field", func(t *testing.T) {
		// The key decodes to the literal text from (backslash included), not
		// to "from", so the edge has no upstream endpoint at all.
		text := `{"nodes":["raw","report"],"edges":[` +
			`{"\\u0066rom":"raw","to":"report"}]}`
		assertImportFails(t, text, "missing", `"from"`)
	})

	t.Run("escaped backslash on the derived endpoint", func(t *testing.T) {
		text := `{"nodes":["raw","report"],"edges":[` +
			`{"from":"raw","\\u0074o":"report"}]}`
		assertImportFails(t, text, "missing", `"to"`)
	})
}

// An edge whose endpoint is provided only through a case variant is missing
// that endpoint: the whole import fails with a nil graph and an error naming
// the missing field, and valid edges earlier in the same document do not
// produce a partial graph.
func TestImportLineageOnlyCaseVariantEndpointFailsWholeImport(t *testing.T) {
	cases := []struct {
		name string
		text string
		want []string
	}{
		{
			"From instead of from",
			`{"nodes":["raw","report"],"edges":[{"From":"raw","to":"report"}]}`,
			[]string{"missing", `"from"`},
		},
		{
			"FROM instead of from",
			`{"nodes":["raw","report"],"edges":[{"FROM":"raw","to":"report"}]}`,
			[]string{"missing", `"from"`},
		},
		{
			"To instead of to",
			`{"nodes":["raw","report"],"edges":[{"from":"raw","To":"report"}]}`,
			[]string{"missing", `"to"`},
		},
		{
			"TO instead of to",
			`{"nodes":["raw","report"],"edges":[{"from":"raw","TO":"report"}]}`,
			[]string{"missing", `"to"`},
		},
		{
			"both endpoints only as variants",
			`{"nodes":["raw","report"],"edges":[{"From":"raw","To":"report"}]}`,
			[]string{"missing", `"from"`},
		},
		{
			"valid edges before the variant-only edge give no partial graph",
			`{"nodes":["raw","mid","report"],"edges":[` +
				`{"from":"raw","to":"mid"},{"From":"mid","to":"report"}]}`,
			[]string{"missing", `"from"`},
		},
		{
			"valid edges before a to-variant-only edge",
			`{"nodes":["raw","mid","report"],"edges":[` +
				`{"from":"raw","to":"mid"},{"from":"mid","TO":"report"}]}`,
			[]string{"missing", `"to"`},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assertImportFails(t, tc.text, tc.want...)
		})
	}
}

// The error for a variant-only edge is the same missing-endpoint error a
// genuinely absent field produces, so a reader can tell which endpoint the
// document lacks.
func TestImportLineageVariantOnlyEdgeErrorMatchesMissingFieldError(t *testing.T) {
	absent := `{"nodes":["raw","report"],"edges":[{"to":"report"}]}`
	variant := `{"nodes":["raw","report"],"edges":[{"From":"raw","to":"report"}]}`

	_, errAbsent := ImportLineage(absent)
	_, errVariant := ImportLineage(variant)
	if errAbsent == nil || errVariant == nil {
		t.Fatal("want missing-endpoint errors for both documents")
	}
	if errAbsent.Error() != errVariant.Error() {
		t.Errorf("variant-only edge error %q differs from absent-field error %q",
			errVariant.Error(), errAbsent.Error())
	}
	if want := `lineage document edge is missing its upstream endpoint ("from")`; errVariant.Error() != want {
		t.Errorf("error = %q, want %q", errVariant.Error(), want)
	}
}
