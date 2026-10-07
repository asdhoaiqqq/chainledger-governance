package chainledger

import (
	"reflect"
	"strings"
	"testing"
)

// These tests guard name identity when a lineage document spells code points
// with \u escapes. encoding/json accepts a lone surrogate escape and rewrites
// it to U+FFFD, which used to collapse two different names into one "�" and
// let a bad edge endpoint attach to a real dataset named "�". Every unpaired
// surrogate escape must now fail the whole import; matched pairs, the real
// replacement character and escaped backslashes stay ordinary, exact names.

// surrogateImportFails runs one import that must be rejected wholesale: nil
// graph, an error that states the unpaired-surrogate rule, the location
// (node name / edge "from" / edge "to") and the escape in its original
// \uXXXX spelling rather than a shared replacement glyph.
func surrogateImportFails(t *testing.T, text, location, escape string) {
	t.Helper()
	graph, err := ImportLineage(text)
	if err == nil {
		t.Fatalf("ImportLineage(%s) succeeded with %v, want unpaired-surrogate error", text, graph)
	}
	if graph != nil {
		t.Fatalf("failed import returned a partial graph %v, want nil", graph)
	}
	msg := err.Error()
	for _, want := range []string{"unpaired Unicode surrogate escape", location, escape} {
		if !strings.Contains(msg, want) {
			t.Errorf("error %q must contain %q", msg, want)
		}
	}
	if strings.ContainsRune(msg, '�') {
		t.Errorf("error %q must not render the bad escape as a replacement character", msg)
	}
}

// The exact failure from the bug report: two distinct lone-high escapes are
// different names and must not both decode to one "�".
func TestImportLineageRejectsTwoDifferentLoneHighNodeNames(t *testing.T) {
	text := `{"nodes":["\ud800","\ud801"],"edges":[]}`
	surrogateImportFails(t, text, "node name", `\ud800`)

	// Each bad spelling is preserved on its own: the two errors quote the
	// escape that appeared instead of converging on the same glyph.
	for _, doc := range []string{
		`{"nodes":["\ud800"],"edges":[]}`,
		`{"nodes":["\ud801"],"edges":[]}`,
	} {
		_, err := ImportLineage(doc)
		if err == nil {
			t.Fatalf("ImportLineage(%s) succeeded, want error", doc)
		}
		if !strings.Contains(err.Error(), doc[strings.Index(doc, `\u`):strings.Index(doc, `\u`)+6]) {
			t.Errorf("error %q must preserve the raw escape from %s", err.Error(), doc)
		}
	}
	_, err1 := ImportLineage(`{"nodes":["\ud800"],"edges":[]}`)
	_, err2 := ImportLineage(`{"nodes":["\ud801"],"edges":[]}`)
	if err1.Error() == err2.Error() {
		t.Errorf("distinct bad names produced the same error %q", err1.Error())
	}
}

// Every shape of unpaired surrogate in a node name is rejected, even with
// valid nodes sitting beside it (no partial graph comes back).
func TestImportLineageRejectsUnpairedNodeSurrogates(t *testing.T) {
	cases := []struct {
		name   string
		token  string // raw JSON node element, including its quotes
		escape string // the exact escape the error must quote
	}{
		{"lone high", `"\ud800"`, `\ud800`},
		{"lone high at emoji code point", `"\ud83d"`, `\ud83d`},
		{"highest high", `"\udbff"`, `\udbff`},
		{"lone low", `"\udc00"`, `\udc00`},
		{"lone low after text", `"ab\ude00"`, `\ude00`},
		{"uppercase hex lone high", `"\uD800"`, `\uD800`},
		{"reversed low then high", `"\udc00\ud83d"`, `\udc00`},
		{"high high low", `"\ud83d\ud83d\ude00"`, `\ud83d`},
		{"high then letters then low", `"\ud83d x\ude00"`, `\ud83d`},
		{"high then other escape then low", `"\ud83d\n\ude00"`, `\ud83d`},
		{"high then valid non-surrogate escape", `"\ud83d\u0041"`, `\ud83d`},
		{"valid pair followed by lone high", `"\ud83d\ude00\ud800"`, `\ud800`},
		{"valid pair followed by lone low", `"a\ud83d\ude00b\udc00c"`, `\udc00`},
		{"lone high at end of string", `"\ud83d\ude00x\udbff"`, `\udbff`},
		{"bad escape in middle of name", `"pre\ud800post"`, `\ud800`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// A valid node alongside the bad one must not rescue the import.
			text := `{"nodes":["perfectly-fine",` + tc.token + `],"edges":[]}`
			surrogateImportFails(t, text, "node name", tc.escape)
		})
	}
}

// Unpaired surrogates in edge endpoints are rejected too, and the error says
// whether they came from "from" or "to".
func TestImportLineageRejectsUnpairedEdgeEndpointSurrogates(t *testing.T) {
	t.Run("bad from", func(t *testing.T) {
		text := `{"nodes":["a","b"],"edges":[{"from":"\ud800","to":"a"}]}`
		surrogateImportFails(t, text, `"from"`, `\ud800`)
	})
	t.Run("bad to", func(t *testing.T) {
		text := `{"nodes":["a","b"],"edges":[{"from":"a","to":"\udc00"}]}`
		surrogateImportFails(t, text, `"to"`, `\udc00`)
	})
	t.Run("reversed pair in from", func(t *testing.T) {
		text := `{"nodes":["a","b"],"edges":[{"from":"\udc00\ud83d","to":"a"}]}`
		surrogateImportFails(t, text, `"from"`, `\udc00`)
	})
	t.Run("separated pair in to", func(t *testing.T) {
		text := `{"nodes":["a","b"],"edges":[{"from":"a","to":"\ud83d-\ude00"}]}`
		surrogateImportFails(t, text, `"to"`, `\ud83d`)
	})
	t.Run("second edge bad despite good first edge", func(t *testing.T) {
		text := `{"nodes":["a","b","c"],` +
			`"edges":[{"from":"a","to":"b"},{"from":"b","to":"\ud801"}]}`
		surrogateImportFails(t, text, `"to"`, `\ud801`)
	})
}

// A bad endpoint must not silently attach to a real dataset named "�": before
// the fix, "\ud800" decoded to U+FFFD and the edge connected to that node.
func TestImportLineageBadEndpointDoesNotMatchRealReplacementNode(t *testing.T) {
	// Nodes include a genuine U+FFFD name (written with the \ufffd escape) and
	// the bad endpoint is a lone high that used to rewrite to the same glyph.
	text := `{"nodes":["a","�"],` +
		`"edges":[{"from":"a","to":"\ud800"}]}`
	surrogateImportFails(t, text, `"to"`, `\ud800`)

	// The same protection holds for a literal U+FFFD character in the document.
	literal := "{\"nodes\":[\"a\",\"�\"]," +
		"\"edges\":[{\"from\":\"\\udc00\",\"to\":\"a\"}]}"
	surrogateImportFails(t, literal, `"from"`, `\udc00`)
}

// Matched surrogate pairs are one character exactly like the literal
// character, wherever they appear: node names and edge endpoints.
func TestImportLineageAcceptsPairedSurrogatesAsExactNames(t *testing.T) {
	const emoji = "😀"

	// The node is written twice, once paired-escaped once literal: they are
	// the same name and de-duplicate to one node. The edge names its upstream
	// with the escaped pair and its derived node with the literal character;
	// the different spellings must still connect.
	text := `{"nodes":["\ud83d\ude00","😀","report"],` +
		`"edges":[{"from":"\ud83d\ude00","to":"report"},` +
		`{"from":"😀","to":"report"}]}`
	graph := mustImport(t, text)
	assertConsistent(t, graph)
	if len(graph) != 2 {
		t.Fatalf("escaped and literal emoji must be one node, got %v", graph)
	}
	if graph[emoji] == nil {
		t.Fatalf("graph must hold the emoji name, got %v", graph)
	}
	assertEntry(t, graph, emoji, nil, []string{"report"})
	assertEntry(t, graph, "report", []string{emoji}, nil)

	// The imported graph answers queries under the decoded name.
	impacts := mustImpacts(t, graph, emoji)
	assertImpactOnce(t, impacts, "report", 1, []string{emoji, "report"})
}

// A real U+FFFD character, whether written literally or as \ufffd, is a valid
// name; the two spellings are the same dataset.
func TestImportLineageAcceptsRealReplacementCharacter(t *testing.T) {
	const replacement = "�"
	text := `{"nodes":["\ufffd","�","a"],` +
		`"edges":[{"from":"\ufffd","to":"a"}]}`
	graph := mustImport(t, text)
	assertConsistent(t, graph)
	if len(graph) != 2 {
		t.Fatalf("literal and escaped U+FFFD must be one node, got %v", graph)
	}
	assertEntry(t, graph, replacement, nil, []string{"a"})
	assertEntry(t, graph, "a", []string{replacement}, nil)

	// Export renders the real replacement character and it reads back equal.
	out := mustExport(t, graph, "a")
	doc := parseExport(t, out)
	if want := []string{"a", replacement}; !reflect.DeepEqual(doc.Nodes, want) {
		t.Errorf("exported nodes = %q, want %q", doc.Nodes, want)
	}
	reimported := mustImport(t, out)
	if reimported[replacement] == nil || reimported["a"] == nil {
		t.Fatalf("re-import of replacement-character export lost names: %v", reimported)
	}
	if again := mustExport(t, reimported, "a"); again != out {
		t.Errorf("re-export differs:\n%s\n%s", out, again)
	}
}

// An escaped backslash followed by letters is ordinary text: the document
// token "\\ud800" is the six-character name \ud800 (backslash, u, d, 8, 0, 0),
// not a surrogate escape, and it survives import, queries and export intact.
func TestImportLineageEscapedBackslashBeforeUdIsLiteralName(t *testing.T) {
	const literal = `\ud800` // decoded value of the JSON token "\\ud800"
	text := `{"nodes":["\\ud800","a"],` +
		`"edges":[{"from":"\\ud800","to":"a"}]}`
	graph := mustImport(t, text)
	assertConsistent(t, graph)
	if graph[literal] == nil {
		t.Fatalf("graph must hold literal name %q, got %v", literal, graph)
	}
	assertEntry(t, graph, literal, nil, []string{"a"})
	assertEntry(t, graph, "a", []string{literal}, nil)

	// It is a normal name for queries, and exports with a doubled backslash so
	// a reader recovers exactly \ud800 rather than a surrogate escape.
	if hits := mustImpacts(t, graph, literal); len(hits) != 1 || hits[0].Dataset != "a" {
		t.Errorf("Impacts(%q) = %v, want [a]", literal, hits)
	}
	out := mustExport(t, graph, "a")
	if !strings.Contains(out, `"\\ud800"`) {
		t.Errorf("export %s must write the literal name as %q", out, `\\ud800`)
	}
	reimported := mustImport(t, out)
	if reimported[literal] == nil {
		t.Fatalf("re-import lost the literal backslash name, got %v", reimported)
	}
	if again := mustExport(t, reimported, "a"); again != out {
		t.Errorf("re-export differs:\n%s\n%s", out, again)
	}
}

// Surrogate validation never disturbs ordinary \u escapes: non-surrogate code
// points spelled with \u keep their decoded value, including \u0000 (which is
// a legal non-empty name) and escapes standing next to paired surrogates.
func TestImportLineageOrdinaryUEscapesStillDecode(t *testing.T) {
	text := `{"nodes":["\u4e2d\u6587","x\u0041","\u0000","\ud83d\ude00"],` +
		`"edges":[{"from":"\u4e2d\u6587","to":"x\u0041"},` +
		`{"from":"\ud83d\ude00","to":"x\u0041"}]}`
	graph := mustImport(t, text)
	for _, name := range []string{"中文", "xA", "😀"} {
		if graph[name] == nil {
			t.Errorf("missing decoded name %q in %v", name, graph)
		}
	}
	if graph["\u0000"] == nil {
		t.Error("U+0000 is a legal non-empty name")
	}
	assertEntry(t, graph, "中文", nil, []string{"xA"})
	assertEntry(t, graph, "xA", []string{"中文", "😀"}, nil)
}
