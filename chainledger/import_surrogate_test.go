package chainledger

import (
	"strings"
	"testing"
)

// Unpaired surrogate escapes in node names fail the whole import: a lone high
// surrogate, a lone low surrogate, a reversed pair and a pair separated by
// other characters are all invalid, the graph is nil even when the document
// otherwise holds legal nodes and edges, and the error quotes the original
// escape spelling and says it came from a node name.
func TestImportLineageRejectsUnpairedSurrogatesInNodeNames(t *testing.T) {
	cases := []struct {
		name   string
		text   string
		escape string
	}{
		{"lone high surrogate", `{"nodes":["a","\ud800"],"edges":[]}`, `\ud800`},
		{"lone low surrogate", `{"nodes":["\udc00","a"],"edges":[]}`, `\udc00`},
		{"high surrogate at end of name", `{"nodes":["x\udbff"],"edges":[]}`, `\udbff`},
		{"reversed pair", `{"nodes":["\udc00\ud800"],"edges":[]}`, `\udc00`},
		{"pair separated by a character", `{"nodes":["\ud800x\udc00"],"edges":[]}`, `\ud800`},
		{"high surrogate followed by non-surrogate escape", `{"nodes":["\ud800A"],"edges":[]}`, `\ud800`},
		{"uppercase hex spelling preserved", `{"nodes":["\uD801"],"edges":[]}`, `\uD801`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			graph, err := ImportLineage(tc.text)
			if err == nil {
				t.Fatalf("ImportLineage(%s) succeeded with %v, want error", tc.text, graph)
			}
			if graph != nil {
				t.Fatalf("failed import returned a partial graph %v, want nil", graph)
			}
			for _, part := range []string{"unpaired Unicode surrogate escape", tc.escape, "node"} {
				if !strings.Contains(err.Error(), part) {
					t.Errorf("error %q must contain %q", err.Error(), part)
				}
			}
		})
	}
}

// Two names that differ only in their unpaired surrogate escapes must not
// collapse into one "�" name: each is rejected with its own original spelling.
func TestImportLineageDistinctBadEscapesKeepTheirSpelling(t *testing.T) {
	_, errA := ImportLineage(`{"nodes":["\ud800","\ud801"],"edges":[]}`)
	_, errB := ImportLineage(`{"nodes":["\ud801","\ud800"],"edges":[]}`)
	if errA == nil || errB == nil {
		t.Fatal("want unpaired surrogate errors")
	}
	if !strings.Contains(errA.Error(), `\ud800`) || !strings.Contains(errB.Error(), `\ud801`) {
		t.Errorf("errors must quote the first bad escape as written, got %q and %q", errA, errB)
	}
}

// Edge endpoints follow the same rule, and the error says whether the bad
// name was the edge's "from" or its "to".
func TestImportLineageRejectsUnpairedSurrogatesInEdgeEndpoints(t *testing.T) {
	assertImportFails(t,
		`{"nodes":["a","b"],"edges":[{"from":"\ud800","to":"b"}]}`,
		"unpaired Unicode surrogate escape", `\ud800`, `"from"`)
	assertImportFails(t,
		`{"nodes":["a","b"],"edges":[{"from":"a","to":"x\udc00y"}]}`,
		"unpaired Unicode surrogate escape", `\udc00`, `"to"`)
	// A bad endpoint fails the whole import even when every other node and
	// edge in the document is legal.
	graph, err := ImportLineage(
		`{"nodes":["a","b","c"],"edges":[{"from":"a","to":"b"},{"from":"\ud800","to":"c"}]}`)
	if err == nil || graph != nil {
		t.Fatalf("want nil graph and error, got graph=%v err=%v", graph, err)
	}
}

// Correctly paired surrogate escapes decode to the character they encode: a
// node written with the pair and an edge endpoint written with the literal
// character (and vice versa) are the same name and connect.
func TestImportLineagePairedSurrogatesMatchLiteralCharacter(t *testing.T) {
	emoji := "\U0001F600"
	graph := mustImport(t,
		`{"nodes":["\ud83d\ude00","b"],"edges":[{"from":"😀","to":"b"}]}`)
	assertConsistent(t, graph)
	assertEntry(t, graph, emoji, nil, []string{"b"})
	assertEntry(t, graph, "b", []string{emoji}, nil)

	// The reverse spelling split connects too, and the graph re-exports.
	graph = mustImport(t,
		`{"nodes":["😀","b"],"edges":[{"from":"\ud83d\ude00","to":"b"}]}`)
	assertEntry(t, graph, emoji, nil, []string{"b"})
	out := mustExport(t, graph, "b")
	again := mustImport(t, out)
	assertEntry(t, again, emoji, nil, []string{"b"})
}

// A real U+FFFD character and the � escape are ordinary valid name
// content; they decode to the same name and are distinct from other names.
func TestImportLineageReplacementCharacterIsAValidName(t *testing.T) {
	graph := mustImport(t,
		`{"nodes":["\ufffd","�","a"],"edges":[{"from":"�","to":"a"}]}`)
	assertConsistent(t, graph)
	assertEntry(t, graph, "�", nil, []string{"a"})
	if got, want := len(graph), 2; got != want {
		t.Fatalf("graph has %d nodes, want %d: %v", got, want, graph)
	}
}

// An escaped backslash in front of escape-looking text is just a backslash
// followed by letters: the name is kept verbatim, never read as a surrogate
// escape, and survives an export round trip.
func TestImportLineageEscapedBackslashBeforeUDigitsIsLiteralText(t *testing.T) {
	name := `\ud800` // backslash followed by letters, no surrogate involved
	graph := mustImport(t, `{"nodes":["\\ud800","a"],"edges":[{"from":"\\ud800","to":"a"}]}`)
	assertConsistent(t, graph)
	assertEntry(t, graph, name, nil, []string{"a"})

	out := mustExport(t, graph, "a")
	again := mustImport(t, out)
	if _, ok := again[name]; !ok {
		t.Fatalf("name %q lost after export round trip, graph %v", name, again)
	}
	assertEntry(t, again, name, nil, []string{"a"})
}

// A graph holding names written with paired escapes, U+FFFD and escaped
// backslashes still answers queries and exports names identical to the
// imported values.
func TestImportLineageLegalEscapedNamesQueryAndExport(t *testing.T) {
	emoji := "\U0001F600"
	text := `{"nodes":["\ud83d\ude00","\ufffd","\\ud800","report"],` +
		`"edges":[{"from":"\ud83d\ude00","to":"report"},{"from":"\ufffd","to":"report"},` +
		`{"from":"\\ud800","to":"report"}]}`
	graph := mustImport(t, text)
	assertConsistent(t, graph)

	upstreams := mustUpstreams(t, graph, "report")
	if got, want := len(upstreams), 3; got != want {
		t.Fatalf("upstreams of report = %v, want %d entries", upstreams, want)
	}

	out := mustExport(t, graph, "report")
	again := mustImport(t, out)
	for _, name := range []string{emoji, "�", `\ud800`, "report"} {
		if _, ok := again[name]; !ok {
			t.Errorf("name %q missing after export round trip", name)
		}
	}
	assertEntry(t, again, "report", []string{`\ud800`, "�", emoji}, nil)
}
