package chainledger

import (
	"strings"
	"testing"
)

// A grammatical number that overflows float64 lives in an unknown field, so it
// is attached content, not lineage: 1e400 (and a 400-digit integer, large or
// small, positive or negative, decimal or exponent spelling) must neither fail
// the import nor be turned into a dataset name or a dependency. The declared
// raw -> mid -> report lineage imports intact, and queries and re-exports
// behave exactly as if the annotation were absent.
func TestImportLineageIgnoresOverflowNumbersInUnknownFields(t *testing.T) {
	hugeInt := strings.Repeat("9", 400)
	numbers := []string{
		"1e400",
		"-1e400",
		hugeInt,
		"-" + hugeInt,
		"0." + strings.Repeat("0", 400) + "1",
		"1.797693134862315907729305190789e308", // just past max float64
		"123456789012345678901234567890.25",
		"-0.000" + strings.Repeat("0", 380) + "5e20",
	}

	for _, num := range numbers {
		t.Run(num[:min(12, len(num))], func(t *testing.T) {
			// Top level, inside an edge object, nested in an object and inside
			// an array; also before and after the endpoint members.
			text := `{"nodes":["raw","mid","report"],"note":` + num + `,"edges":[` +
				`{"note":` + num + `,"from":"raw","to":"mid"},` +
				`{"from":"mid","to":"report","note":` + num + `,"meta":{` +
				`"n":` + num + `,"list":[` + num + `,{"x":` + num + `}]}}]}`

			graph := mustImport(t, text)
			assertConsistent(t, graph)
			assertImportedChain(t, graph)

			// The number never became a dataset name: exactly three nodes.
			if got := len(graph); got != 3 {
				t.Fatalf("graph has %d nodes %v, want exactly raw/mid/report", got, graph)
			}

			// A re-export is byte-identical to one made from the annotation-free
			// document: attached numbers leave no trace in the lineage.
			clean := `{"nodes":["raw","mid","report"],"edges":[` +
				`{"from":"raw","to":"mid"},{"from":"mid","to":"report"}]}`
			cleanGraph := mustImport(t, clean)
			if got, want := mustExport(t, graph, "report"),
				mustExport(t, cleanGraph, "report"); got != want {
				t.Errorf("export with annotation = %s\nwant %s", got, want)
			}
		})
	}
}

// A case variant "From" is an unknown key even alongside a correct lowercase
// "from"; its overflowing numeric value stays ignored and must neither
// overwrite the declared endpoint nor trigger a numeric range error.
func TestImportLineageCaseVariantEndpointWithOverflowNumberIsIgnored(t *testing.T) {
	text := `{"nodes":["raw","mid","report"],"edges":[` +
		`{"From":1e400,"from":"raw","to":"mid"},` +
		`{"from":"mid","TO":` + "1" + strings.Repeat("0", 400) + `,"to":"report"}]}`

	graph := mustImport(t, text)
	assertConsistent(t, graph)
	assertImportedChain(t, graph)
}

// Two empty arrays together with large-numbered annotations import as an empty
// graph that is still ready for registrations.
func TestImportLineageEmptyArraysWithOverflowAnnotations(t *testing.T) {
	text := `{"note":1e400,"nodes":[],"edges":[],"meta":[{"n":` +
		strings.Repeat("7", 400) + `}]}`
	graph := mustImport(t, text)
	if len(graph) != 0 {
		t.Fatalf("want empty graph, got %v", graph)
	}
	mustRegister(t, graph, "raw")
	mustRegister(t, graph, "report", "raw")
	assertEntry(t, graph, "report", []string{"raw"}, nil)
	assertConsistent(t, graph)
}

// Unknown-field numbers still have to be grammatical JSON: 01, 1e and NaN are
// malformed values and fail the whole import with a nil graph wherever they
// appear, just as they would at the top level.
func TestImportLineageMalformedNumberSpellingStillRejected(t *testing.T) {
	cases := []struct {
		name string
		text string
	}{
		{"leading zero at top level", `{"nodes":["raw"],"edges":[],"note":01}`},
		{"dangling exponent at top level", `{"nodes":["raw"],"edges":[],"note":1e}`},
		{"nan literal at top level", `{"nodes":["raw"],"edges":[],"note":NaN}`},
		{"leading zero nested in edge",
			`{"nodes":["raw","r"],"edges":[{"from":"raw","to":"r","note":01}]}`},
		{"dangling exponent nested in object",
			`{"nodes":["raw"],"edges":[],"note":{"n":1e}}`},
		{"nan inside array", `{"nodes":["raw"],"edges":[],"note":[NaN]}`},
		{"negative leading zero", `{"nodes":["raw"],"edges":[],"note":-01}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assertImportFails(t, tc.text, "complete JSON object")
		})
	}
}

// A legal large number must not mask a later duplicate-key problem: the
// duplicate is reported with its key name, including when the two key
// spellings only collide after Unicode-escape decoding. Every failure returns
// a nil graph.
func TestImportLineageLargeNumberDoesNotMaskDuplicateKey(t *testing.T) {
	cases := []struct {
		name string
		text string
		key  string
	}{
		{
			"large number before duplicate key",
			`{"nodes":["raw"],"edges":[],"note":{"big":1e400,"owner":"ann","owner":"bob"}}`,
			"owner",
		},
		{
			"400-digit number before duplicate key",
			`{"nodes":["raw"],"edges":[],"note":{"big":` + strings.Repeat("8", 400) +
				`,"kind":1,"kind":2}}`,
			"kind",
		},
		{
			"overflow number between duplicate keys in edge",
			`{"nodes":["raw","r"],"edges":[{"from":"raw","to":"r","m":1e400,"m":2}]}`,
			"m",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assertImportFails(t, tc.text, "duplicated", tc.key)
		})
	}
}

// Nodes elements and edge endpoints keep their strict string typing: a legal
// large number written in one of those positions is a type error, never a
// number-to-string coercion.
func TestImportLineageLargeNumbersInLineagePositionsRejected(t *testing.T) {
	cases := []struct {
		name string
		text string
		want []string
	}{
		{"overflow number node", `{"nodes":[1e400],"edges":[]}`,
			[]string{"nodes", "string"}},
		{"large integer node", `{"nodes":[` + strings.Repeat("9", 400) + `],"edges":[]}`,
			[]string{"nodes", "string"}},
		{"overflow number in from",
			`{"nodes":["r"],"edges":[{"from":1e400,"to":"r"}]}`,
			[]string{`"from"`, "string"}},
		{"large integer in to",
			`{"nodes":["raw"],"edges":[{"from":"raw","to":` + strings.Repeat("5", 400) + `}]}`,
			[]string{`"to"`, "string"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assertImportFails(t, tc.text, tc.want...)
		})
	}
}

// assertImportedChain checks the three-node raw -> mid -> report lineage, in
// both directions, with the distance-two explanation paths through mid.
func assertImportedChain(t *testing.T, graph map[string]*Lineage) {
	t.Helper()
	assertEntry(t, graph, "raw", nil, []string{"mid"})
	assertEntry(t, graph, "mid", []string{"raw"}, []string{"report"})
	assertEntry(t, graph, "report", []string{"mid"}, nil)

	assertImpactOnce(t, mustImpacts(t, graph, "raw"), "report", 2,
		[]string{"raw", "mid", "report"})
	assertUpstreamOnce(t, mustUpstreams(t, graph, "report"), "raw", 2,
		[]string{"raw", "mid", "report"})
}
