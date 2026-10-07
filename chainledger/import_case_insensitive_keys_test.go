package chainledger

import (
	"strings"
	"testing"
)

// Only a member whose decoded JSON key is exactly "from" or "to" decides an
// edge endpoint. Case variants ("From", "FROM", "fRoM", ...) are ordinary
// unknown fields: their values never participate in the lineage, regardless
// of where the variant is placed relative to the official field. Before the
// fix encoding/json matched struct tags case-insensitively with last-match
// wins, so reordering the same edge could change its source or turn a valid
// import into a failure.

// The exact report from the bug: from:"raw", FROM:"other" and to:"report"
// always build raw -> report, whether FROM precedes or follows from.
func TestImportLineageCaseVariantFromDoesNotDecideEndpoint(t *testing.T) {
	for _, text := range []string{
		`{"nodes":["raw","other","report"],"edges":[{"from":"raw","FROM":"other","to":"report"}]}`,
		`{"nodes":["raw","other","report"],"edges":[{"FROM":"other","from":"raw","to":"report"}]}`,
		`{"nodes":["raw","other","report"],"edges":[{"fRoM":"other","from":"raw","to":"report"}]}`,
	} {
		graph := mustImport(t, text)
		assertConsistent(t, graph)

		if got, want := len(graph), 3; got != want {
			t.Fatalf("graph has %d nodes %v, want %d", got, graph, want)
		}
		assertEntry(t, graph, "raw", nil, []string{"report"})
		assertEntry(t, graph, "report", []string{"raw"}, nil)
		// other stays fully independent: no edge in either direction.
		assertEntry(t, graph, "other", nil, nil)

		// Downstream of raw contains report; downstream of other does not.
		assertImpactOnce(t, mustImpacts(t, graph, "raw"), "report", 1,
			[]string{"raw", "report"})
		if hits := mustImpacts(t, graph, "other"); len(hits) != 0 {
			t.Errorf("Impacts(other) = %v, want no downstreams", hits)
		}

		// Re-exporting report keeps exactly the officially declared edge; the
		// variant value is gone with the unknown field.
		if got, want := mustExport(t, graph, "report"),
			`{"nodes":["raw","report"],"edges":[{"from":"raw","to":"report"}]}`; got != want {
			t.Errorf("export = %s, want %s", got, want)
		}
	}
}

// Case variants of "to" behave the same way: they can never redirect which
// dataset is derived.
func TestImportLineageCaseVariantToDoesNotDecideEndpoint(t *testing.T) {
	for _, text := range []string{
		`{"nodes":["raw","other","report"],"edges":[{"from":"raw","to":"report","TO":"other"}]}`,
		`{"nodes":["raw","other","report"],"edges":[{"TO":"other","to":"report","from":"raw"}]}`,
		`{"nodes":["raw","other","report"],"edges":[{"from":"raw","To":"other","to":"report"}]}`,
	} {
		graph := mustImport(t, text)
		assertConsistent(t, graph)
		assertEntry(t, graph, "raw", nil, []string{"report"})
		assertEntry(t, graph, "report", []string{"raw"}, nil)
		assertEntry(t, graph, "other", nil, nil)
		if hits := mustImpacts(t, graph, "raw"); len(hits) != 1 || hits[0].Dataset != "report" {
			t.Errorf("Impacts(raw) = %v, want only report", hits)
		}
		if got, want := mustExport(t, graph, "report"),
			`{"nodes":["raw","report"],"edges":[{"from":"raw","to":"report"}]}`; got != want {
			t.Errorf("export = %s, want %s", got, want)
		}
	}
}

// A case variant cannot stand in for a missing endpoint: only From/TO present
// means the lowercase field is absent and the import fails as missing.
func TestImportLineageCaseVariantAloneIsMissingEndpoint(t *testing.T) {
	assertImportFails(t,
		`{"nodes":["a","b"],"edges":[{"From":"a","to":"b"}]}`,
		`missing its upstream endpoint ("from")`)
	assertImportFails(t,
		`{"nodes":["a","b"],"edges":[{"from":"a","TO":"b"}]}`,
		`missing its derived endpoint ("to")`)
	// Both endpoints case-only is a missing-from error reported first.
	assertImportFails(t,
		`{"nodes":["a","b"],"edges":[{"From":"a","To":"b"}]}`,
		`missing its upstream endpoint ("from")`)
}

// An invalid official declaration is rejected with the original error even
// when a perfectly legal case variant sits next to it — an unknown field can
// never rescue an empty, mistyped or unregistered official endpoint.
func TestImportLineageInvalidOfficialEndpointNotRescuedByVariant(t *testing.T) {
	t.Run("empty from despite valid variant", func(t *testing.T) {
		assertImportFails(t,
			`{"nodes":["raw","report"],"edges":[{"from":"","FROM":"raw","to":"report"}]}`,
			`missing its upstream endpoint ("from")`)
	})
	t.Run("empty to despite valid variant", func(t *testing.T) {
		assertImportFails(t,
			`{"nodes":["raw","report"],"edges":[{"from":"raw","TO":"report","to":""}]}`,
			`missing its derived endpoint ("to")`)
	})
	t.Run("from has wrong type despite valid string variant", func(t *testing.T) {
		assertImportFails(t,
			`{"nodes":["raw","report"],"edges":[{"from":1,"FROM":"raw","to":"report"}]}`,
			`endpoint "from" must be a JSON string`)
	})
	t.Run("to has wrong type despite valid string variant", func(t *testing.T) {
		assertImportFails(t,
			`{"nodes":["raw","report"],"edges":[{"from":"raw","to":[],"To":"report"}]}`,
			`endpoint "to" must be a JSON string`)
	})
	t.Run("from null despite valid string variant", func(t *testing.T) {
		assertImportFails(t,
			`{"nodes":["raw","report"],"edges":[{"from":null,"FROM":"raw","to":"report"}]}`,
			`missing its upstream endpoint ("from")`)
	})
	t.Run("official endpoint names an unlisted node despite valid variant", func(t *testing.T) {
		assertImportFails(t,
			`{"nodes":["raw","report"],"edges":[{"from":"ghost","FROM":"raw","to":"report"}]}`,
			"endpoint not listed in nodes", "ghost")
		assertImportFails(t,
			`{"nodes":["raw","report"],"edges":[{"from":"raw","to":"ghost","To":"report"}]}`,
			"endpoint not listed in nodes", "ghost")
	})
}

// When the official fields are valid, the values carried by case-variant
// members are never inspected as endpoints: numbers, null or nested
// annotations inside a variant cause neither a type error nor an
// unregistered-name error.
func TestImportLineageCaseVariantValuesAreNotValidatedAsEndpoints(t *testing.T) {
	cases := []struct {
		name string
		edge string
	}{
		{"variant from is a number", `{"from":"raw","FROM":123,"to":"report"}`},
		{"variant from is null", `{"from":"raw","FROM":null,"to":"report"}`},
		{"variant from is an object", `{"from":"raw","FROM":{"note":"x"},"to":"report"}`},
		{"variant from is an array", `{"from":"raw","FROM":[1,2,3],"to":"report"}`},
		{"variant to is a number", `{"from":"raw","TO":4.5,"to":"report"}`},
		{"variant to is null", `{"from":"raw","TO":null,"to":"report"}`},
		{"variant to is a nested annotation", `{"from":"raw","TO":{"who":4},"to":"report"}`},
		{"variant names an unlisted node", `{"from":"raw","FROM":"ghost","TO":"phantom","to":"report"}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			text := `{"nodes":["raw","report"],"edges":[` + tc.edge + `]}`
			graph := mustImport(t, text)
			assertConsistent(t, graph)
			assertEntry(t, graph, "raw", nil, []string{"report"})
			assertEntry(t, graph, "report", []string{"raw"}, nil)
		})
	}
}

// Endpoint identification follows the decoded key: a \uXXXX spelling that
// decodes to "from" or "to" is the official field, not an unknown one.
func TestImportLineageUnicodeEscapedFromAndToAreOfficialFields(t *testing.T) {
	// f == 'f', t == 't'.
	text := `{"nodes":["raw","report"],"edges":[{"from":"raw","to":"report"}]}`
	graph := mustImport(t, text)
	assertConsistent(t, graph)
	assertEntry(t, graph, "raw", nil, []string{"report"})
	assertEntry(t, graph, "report", []string{"raw"}, nil)

	// A bad value under an escaped official key is validated exactly like the
	// plain spelling; it is not swallowed as an unknown field.
	assertImportFails(t,
		`{"nodes":["report"],"edges":[{"from":1,"to":"report"}]}`,
		`endpoint "from" must be a JSON string`)
	assertImportFails(t,
		`{"nodes":["raw"],"edges":[{"from":"raw","to":null}]}`,
		`missing its derived endpoint ("to")`)
	assertImportFails(t,
		`{"nodes":["raw"],"edges":[{"from":"ghost","to":"raw"}]}`,
		"endpoint not listed in nodes", "ghost")
}

// An escaped spelling and the plain spelling of "from"/"to" in one object are
// the same decoded key and remain duplicate-key rejections. Keys that merely
// differ in case are distinct keys, never duplicates.
func TestImportLineageCaseAndEscapeDuplicateKeyRules(t *testing.T) {
	t.Run("escaped official key duplicates plain from", func(t *testing.T) {
		assertImportFails(t,
			`{"nodes":["raw","report"],"edges":[{"from":"raw","from":"raw","to":"report"}]}`,
			"duplicated", "from")
	})
	t.Run("escaped official key duplicates plain to", func(t *testing.T) {
		assertImportFails(t,
			`{"nodes":["raw","report"],"edges":[{"from":"raw","to":"report","to":"report"}]}`,
			"duplicated", "to")
	})
	t.Run("two escaped spellings duplicate each other", func(t *testing.T) {
		assertImportFails(t,
			`{"nodes":["raw","report"],"edges":[{"from":"raw","from":"x","to":"report"}]}`,
			"duplicated", "from")
	})
	t.Run("different case keys are not duplicates", func(t *testing.T) {
		graph := mustImport(t,
			`{"nodes":["raw","report"],"edges":[`+
				`{"from":"raw","From":"x","FROM":"y","fRoM":"z","to":"report","To":1,"TO":null}]}`)
		assertConsistent(t, graph)
		assertEntry(t, graph, "raw", nil, []string{"report"})
		assertEntry(t, graph, "report", []string{"raw"}, nil)
	})
	t.Run("duplicate key nested inside a case variant still rejected", func(t *testing.T) {
		assertImportFails(t,
			`{"nodes":["raw","report"],"edges":[{"from":"raw","to":"report",`+
				`"META":{"k":1,"k":2}}]}`,
			"duplicated", "k")
	})
}

// Exact, case-sensitive name matching is unaffected: only the value of the
// lowercase key is compared against nodes, and case-different dataset names
// remain distinct datasets.
func TestImportLineageCaseVariantFixKeepsExactNameMatching(t *testing.T) {
	// Nodes "Raw"/"raw" coexist; FROM points at the uppercase node but is an
	// unknown member, so the edge uses lowercase raw.
	text := `{"nodes":["raw","Raw","report"],"edges":[{"from":"raw","FROM":"Raw","to":"report"}]}`
	graph := mustImport(t, text)
	assertConsistent(t, graph)
	assertEntry(t, graph, "raw", nil, []string{"report"})
	assertEntry(t, graph, "Raw", nil, nil)
	assertEntry(t, graph, "report", []string{"raw"}, nil)

	// Standard export documents (lowercase keys only) still import unchanged.
	if round := mustExport(t, graph, "report"); !strings.Contains(round, `"from":"raw"`) {
		t.Fatalf("unexpected export: %s", round)
	}
	reimported := mustImport(t, mustExport(t, graph, "report"))
	assertEntry(t, reimported, "raw", nil, []string{"report"})
	assertEntry(t, reimported, "report", []string{"raw"}, nil)
}
