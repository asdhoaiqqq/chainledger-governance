package chainledger

import (
	"reflect"
	"strings"
	"testing"
)

// These tests guard name identity at the comparison entry point from the
// other side: an unpaired \u surrogate escape anywhere in either document —
// node name or edge endpoint, inside or outside the target's derivation —
// fails the whole comparison with a zero diff, never a partial result and
// never a silent attachment to a real "�" dataset. Legal spellings compare
// cleanly; those cases live in compare_upstream_lineage_encoding_test.go.

// surrogateCompareFails runs one comparison that must be rejected wholesale:
// the diff is the zero value with both lists nil, and the error states which
// document was rejected (before-change or after-change), where the bad escape
// was found (node name / edge "from" / edge "to") and quotes the escape in
// its original \uXXXX spelling rather than a shared replacement glyph.
func surrogateCompareFails(t *testing.T, before, after, target, side, location, escape string) {
	t.Helper()
	diff, err := CompareUpstreamLineage(before, after, target)
	if err == nil {
		t.Fatalf("CompareUpstreamLineage succeeded with %+v, want unpaired-surrogate rejection", diff)
	}
	if diff.Added != nil || diff.Removed != nil {
		t.Fatalf("rejected comparison returned partial lists %+v, want zero diff", diff)
	}
	if !reflect.DeepEqual(diff, UpstreamLineageDiff{}) {
		t.Fatalf("rejected comparison returned %+v, want zero UpstreamLineageDiff", diff)
	}
	msg := err.Error()
	for _, want := range []string{side, location, escape} {
		if !strings.Contains(msg, want) {
			t.Errorf("error %q must contain %q", msg, want)
		}
	}
	if strings.ContainsRune(msg, '�') {
		t.Errorf("error %q must not render the bad escape as a replacement character", msg)
	}
}

// Every shape of unpaired surrogate — lone high, lone low, and a high-low
// pair broken by other characters — is rejected whether it appears in a node
// name or in an edge's "from" or "to" endpoint, and whether the document is
// the before-change or the after-change one.
func TestCompareUpstreamLineageRejectsUnpairedSurrogates(t *testing.T) {
	good := `{"nodes":["s","t"],"edges":[{"from":"s","to":"t"}]}`
	cases := []struct {
		name     string
		doc      string
		location string
		escape   string
	}{
		{"lone high in node name",
			`{"nodes":["s","t","\ud800"],"edges":[{"from":"s","to":"t"}]}`,
			"node name", `\ud800`},
		{"lone low in node name",
			`{"nodes":["s","t","x\udc00"],"edges":[{"from":"s","to":"t"}]}`,
			"node name", `\udc00`},
		{"separated pair in node name",
			`{"nodes":["s","t","\ud83d-\ude00"],"edges":[{"from":"s","to":"t"}]}`,
			"node name", `\ud83d`},
		{"lone high in from endpoint of target edge",
			`{"nodes":["s","t"],"edges":[{"from":"\ud800","to":"t"}]}`,
			`"from"`, `\ud800`},
		{"lone low in to endpoint of target edge",
			`{"nodes":["s","t"],"edges":[{"from":"s","to":"\udc00"}]}`,
			`"to"`, `\udc00`},
		{"separated pair in to endpoint",
			`{"nodes":["s","t"],"edges":[{"from":"s","to":"\ud83d x\ude00"}]}`,
			`"to"`, `\ud83d`},
		{"lone high in from endpoint of unrelated edge",
			`{"nodes":["s","t","x","y"],"edges":[{"from":"s","to":"t"},{"from":"\ud801","to":"x"}]}`,
			`"from"`, `\ud801`},
	}
	for _, tc := range cases {
		t.Run(tc.name+" in before document", func(t *testing.T) {
			surrogateCompareFails(t, tc.doc, good, "t", "before-change", tc.location, tc.escape)
		})
		t.Run(tc.name+" in after document", func(t *testing.T) {
			surrogateCompareFails(t, good, tc.doc, "t", "after-change", tc.location, tc.escape)
		})
	}
}

// A document may register a genuine U+FFFD dataset, but a bad endpoint must
// never be rewritten to the same glyph and attached to it: the comparison
// fails instead of silently deriving t from "�".
func TestCompareUpstreamLineageBadEndpointNeverMatchesRealReplacementNode(t *testing.T) {
	good := `{"nodes":["s","t"],"edges":[{"from":"s","to":"t"}]}`

	// The from endpoint "\ud800" would decode to U+FFFD and match the real
	// "�" node; the to endpoint variant does the same from the other side.
	badFrom := `{"nodes":["s","t","�"],"edges":[{"from":"s","to":"t"},` +
		`{"from":"\ud800","to":"�"}]}`
	badTo := `{"nodes":["s","t","�"],"edges":[{"from":"s","to":"t"},` +
		`{"from":"s","to":"\udc00"}]}`

	surrogateCompareFails(t, badFrom, good, "t", "before-change", `"from"`, `\ud800`)
	surrogateCompareFails(t, good, badFrom, "t", "after-change", `"from"`, `\ud800`)
	surrogateCompareFails(t, badTo, good, "t", "before-change", `"to"`, `\udc00`)
	surrogateCompareFails(t, good, badTo, "t", "after-change", `"to"`, `\udc00`)
}

// Full-document validation reaches names that play no part in the target's
// derivation: an unpaired escape on an independent node, or on an edge of an
// independent chain, fails the comparison just the same.
func TestCompareUpstreamLineageValidatesNamesBeyondTargetScope(t *testing.T) {
	good := `{"nodes":["s","t"],"edges":[{"from":"s","to":"t"}]}`

	// The bad name sits on an independent node with no dependency at all.
	badNode := `{"nodes":["s","t","lone","\ud801"],"edges":[{"from":"s","to":"t"}]}`
	// The bad endpoint sits on an edge of the independent x chain.
	badEdge := `{"nodes":["s","t","x"],"edges":[{"from":"s","to":"t"},` +
		`{"from":"x","to":"\ude00"}]}`

	surrogateCompareFails(t, badNode, good, "t", "before-change", "node name", `\ud801`)
	surrogateCompareFails(t, good, badNode, "t", "after-change", "node name", `\ud801`)
	surrogateCompareFails(t, badEdge, good, "t", "before-change", `"to"`, `\ude00`)
	surrogateCompareFails(t, good, badEdge, "t", "after-change", `"to"`, `\ude00`)
}

// When both documents carry bad names, the before-change document is
// validated first and its rejection — its document side, its escape — is the
// one reported.
func TestCompareUpstreamLineageReportsBeforeDocumentSurrogateFirst(t *testing.T) {
	before := `{"nodes":["s","t","\ud800"],"edges":[{"from":"s","to":"t"}]}`
	after := `{"nodes":["s","t"],"edges":[{"from":"s","to":"\udc00"}]}`

	diff, err := CompareUpstreamLineage(before, after, "t")
	if err == nil {
		t.Fatal("expected rejection when both documents carry unpaired surrogates")
	}
	if diff.Added != nil || diff.Removed != nil {
		t.Fatalf("rejected comparison returned partial lists %+v, want zero diff", diff)
	}
	msg := err.Error()
	if !strings.Contains(msg, "before-change") || !strings.Contains(msg, `\ud800`) {
		t.Fatalf("error %q must report the before-change document's \\ud800", msg)
	}
	if strings.Contains(msg, "after-change") || strings.Contains(msg, `\udc00`) {
		t.Fatalf("error %q must not report the after-change document first", msg)
	}
}
