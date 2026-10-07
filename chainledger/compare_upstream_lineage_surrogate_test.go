package chainledger

import (
	"reflect"
	"strings"
	"testing"
)

// These tests guard name identity at the comparison entry point against
// unpaired \u surrogate escapes. encoding/json rewrites a lone surrogate
// escape to U+FFFD, which would collapse two different bad names into one
// "�" and could attach a bad edge endpoint to a real dataset named "�".
// CompareUpstreamLineage runs every document through the full ImportLineage
// validation before comparing, so any unpaired surrogate anywhere in either
// document — even in nodes or dependencies that play no part in the target's
// derivation — fails the whole comparison with a zero diff.

// surrogateCompareFails runs one comparison that must be rejected wholesale:
// a zero UpstreamLineageDiff with both lists nil, and an error that states
// which document was rejected (before-change or after-change), where the
// problem was found (node name / edge "from" / edge "to") and quotes the
// offending escape in its original \uXXXX spelling.
func surrogateCompareFails(t *testing.T, before, after, which, location, escape string) {
	t.Helper()
	diff, err := CompareUpstreamLineage(before, after, "t")
	if err == nil {
		t.Fatalf("CompareUpstreamLineage succeeded with %+v, want unpaired-surrogate error", diff)
	}
	if diff.Added != nil || diff.Removed != nil {
		t.Errorf("failed comparison must return the zero diff with nil lists, got %+v", diff)
	}
	msg := err.Error()
	for _, want := range []string{which, location, escape} {
		if !strings.Contains(msg, want) {
			t.Errorf("error %q must contain %q", msg, want)
		}
	}
	if strings.ContainsRune(msg, '�') {
		t.Errorf("error %q must not render the bad escape as a replacement character", msg)
	}
}

// The good counterpart document: s -> t, so target t has one dependency.
const surrogateGoodDoc = `{"nodes":["s","t"],"edges":[{"from":"s","to":"t"}]}`

// Every shape of unpaired surrogate — a lone high, a lone low, and a
// high-low pair broken by another character — is rejected wherever it
// appears: in a node name or in an edge's "from" or "to" endpoint, in either
// document. The bad names sit in nodes and dependencies that play no part in
// the target's derivation, proving validation covers the whole document, not
// just the compared scope.
func TestCompareUpstreamLineageRejectsUnpairedSurrogates(t *testing.T) {
	shapes := []struct {
		name   string
		token  string // raw JSON string token holding the bad name
		escape string // the exact escape the error must quote
	}{
		{"lone high", `"\ud800"`, `\ud800`},
		{"lone low", `"\udc00"`, `\udc00`},
		{"high and low separated", `"\ud83d-\ude00"`, `\ud83d`},
	}
	locations := []struct {
		name     string
		doc      func(token string) string
		location string
	}{
		{"node name", func(token string) string {
			return `{"nodes":["s","t",` + token + `],"edges":[{"from":"s","to":"t"}]}`
		}, "node name"},
		{"edge from", func(token string) string {
			return `{"nodes":["s","t","x"],"edges":[{"from":"s","to":"t"},` +
				`{"from":` + token + `,"to":"x"}]}`
		}, `"from"`},
		{"edge to", func(token string) string {
			return `{"nodes":["s","t","x"],"edges":[{"from":"s","to":"t"},` +
				`{"from":"x","to":` + token + `}]}`
		}, `"to"`},
	}
	for _, shape := range shapes {
		for _, loc := range locations {
			bad := loc.doc(shape.token)
			t.Run(shape.name+" in "+loc.name+" of before", func(t *testing.T) {
				surrogateCompareFails(t, bad, surrogateGoodDoc,
					"before-change", loc.location, shape.escape)
			})
			t.Run(shape.name+" in "+loc.name+" of after", func(t *testing.T) {
				surrogateCompareFails(t, surrogateGoodDoc, bad,
					"after-change", loc.location, shape.escape)
			})
		}
	}
}

// A bad endpoint must never be connected to a real dataset named "�": the
// lone surrogate escape would decode to the same replacement glyph, but the
// comparison must reject the document instead of attaching the edge.
func TestCompareUpstreamLineageBadEndpointNeverMatchesReplacementNode(t *testing.T) {
	// The real U+FFFD node is escape-spelled here; the bad "from" endpoint is
	// a lone high surrogate that would rewrite to the same glyph.
	badFrom := `{"nodes":["s","t","\ufffd","x"],"edges":[{"from":"s","to":"t"},` +
		`{"from":"\ud800","to":"\ufffd"}]}`
	surrogateCompareFails(t, badFrom, surrogateGoodDoc, "before-change", `"from"`, `\ud800`)
	surrogateCompareFails(t, surrogateGoodDoc, badFrom, "after-change", `"from"`, `\ud800`)

	// The real U+FFFD node written as a literal character, bad "to" endpoint.
	badTo := `{"nodes":["s","t","�","x"],"edges":[{"from":"s","to":"t"},` +
		`{"from":"x","to":"\udc00"}]}`
	surrogateCompareFails(t, badTo, surrogateGoodDoc, "before-change", `"to"`, `\udc00`)
	surrogateCompareFails(t, surrogateGoodDoc, badTo, "after-change", `"to"`, `\udc00`)
}

// When both documents hold bad names, the before-change document is validated
// first and its rejection — quoting its own escape — is the one reported.
func TestCompareUpstreamLineageBothDocumentsBadReportsBefore(t *testing.T) {
	before := `{"nodes":["s","t","\ud801"],"edges":[{"from":"s","to":"t"}]}`
	after := `{"nodes":["s","t","\ud802"],"edges":[{"from":"s","to":"t"}]}`

	diff, err := CompareUpstreamLineage(before, after, "t")
	if err == nil {
		t.Fatalf("CompareUpstreamLineage succeeded with %+v, want before-document rejection", diff)
	}
	if diff.Added != nil || diff.Removed != nil {
		t.Errorf("failed comparison must return the zero diff with nil lists, got %+v", diff)
	}
	msg := err.Error()
	if !strings.HasPrefix(msg, "before-change") {
		t.Errorf("error %q must be the before-change document rejection", msg)
	}
	if !strings.Contains(msg, `\ud801`) {
		t.Errorf("error %q must quote the before document's escape", msg)
	}
	if strings.Contains(msg, `\ud802`) {
		t.Errorf("error %q must not quote the after document's escape", msg)
	}
}

// An escaped backslash followed by letters is ordinary text: the JSON token
// "\\uD800" decodes to the six-character name \uD800 (backslash, u, D, 8, 0, 0),
// not to a surrogate, and must neither fail the comparison nor be rewritten
// in the reported difference.
func TestCompareUpstreamLineageEscapedBackslashIsLiteralName(t *testing.T) {
	const literal = `\uD800` // decoded value of the JSON token "\\uD800"
	before := `{"nodes":["\\uD800","t"],"edges":[{"from":"\\uD800","to":"t"}]}`
	after := `{"nodes":["\\uD800","t"],"edges":[]}`

	diff, err := CompareUpstreamLineage(before, after, "t")
	if err != nil {
		t.Fatalf("CompareUpstreamLineage: %v", err)
	}
	if len(diff.Added) != 0 {
		t.Errorf("Added = %v, want none", depPairs(diff.Added))
	}
	wantRemoved := [][2]string{{literal, "t"}}
	if got := depPairs(diff.Removed); !reflect.DeepEqual(got, wantRemoved) {
		t.Errorf("Removed = %v, want %v — the literal backslash name must be kept verbatim", got, wantRemoved)
	}
}

// Strings carried in note fields are not dataset names: unknown fields are
// ignored by the document format, so a lone surrogate escape inside a note
// value — at the top level or inside an edge object — must not fail the
// comparison on its own, and never creates a dependency.
func TestCompareUpstreamLineageNoteValuesAreNotNames(t *testing.T) {
	before := `{"nodes":["s","t"],"note":"lone \ud800 in a note",` +
		`"edges":[{"from":"s","to":"t","note":"\udc00"}]}`
	after := `{"nodes":["s","t"],"note":"broken pair \ud83d x \ude00",` +
		`"edges":[{"from":"s","to":"t"}],"notes":["\ud801","\ud802"]}`

	diff, err := CompareUpstreamLineage(before, after, "t")
	assertCompareNoChange(t, diff, err)
}
