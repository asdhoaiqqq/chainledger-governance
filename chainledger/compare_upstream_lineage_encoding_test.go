package chainledger

import (
	"reflect"
	"testing"
)

// These tests guard dataset-name identity across the comparison entry point:
// two documents that spell the same code points differently — literal text
// versus legal \uXXXX escapes — describe the same lineage and must compare
// with no false changes, while a real change comes back with the decoded
// names. Unpaired surrogate escapes are rejected wholesale instead; those
// cases live in compare_upstream_lineage_surrogate_test.go.

// assertNoLineageChange compares two documents that must describe the same
// upstream lineage for target: success with two non-nil empty lists.
func assertNoLineageChange(t *testing.T, before, after, target string) {
	t.Helper()
	diff, err := CompareUpstreamLineage(before, after, target)
	if err != nil {
		t.Fatalf("CompareUpstreamLineage: %v", err)
	}
	if diff.Added == nil || diff.Removed == nil {
		t.Fatalf("no-change result must hold non-nil empty lists, got %+v", diff)
	}
	if len(diff.Added) != 0 || len(diff.Removed) != 0 {
		t.Fatalf("expected no change, got added=%v removed=%v",
			depPairs(diff.Added), depPairs(diff.Removed))
	}
}

// The same nodes and dependencies written once with literal Chinese and emoji
// and once with the equivalent \uXXXX escapes are the same document: the
// comparison succeeds with no change in either direction, and the target name
// itself carries the special characters.
func TestCompareUpstreamLineageEquivalentEscapeSpellingsNoChange(t *testing.T) {
	literal := `{"nodes":["中文源","中间","报表😀"],"edges":[` +
		`{"from":"中文源","to":"中间"},{"from":"中间","to":"报表😀"}]}`
	escaped := `{"nodes":["\u4e2d\u6587\u6e90","\u4e2d\u95f4","\u62a5\u8868\ud83d\ude00"],"edges":[` +
		`{"from":"\u4e2d\u6587\u6e90","to":"\u4e2d\u95f4"},` +
		`{"from":"\u4e2d\u95f4","to":"\u62a5\u8868\ud83d\ude00"}]}`

	assertNoLineageChange(t, literal, escaped, "报表😀")
	assertNoLineageChange(t, escaped, literal, "报表😀")
}

// Within one document a node and the edge endpoints naming it may use
// different legal spellings: they decode to the same name and connect, so
// shuffling which spelling appears where between the two documents is not a
// change. Were the spellings not one identity, the endpoint would miss its
// node and the document would be rejected outright.
func TestCompareUpstreamLineageMixedSpellingsConnect(t *testing.T) {
	// Nodes literal, edge endpoints escaped.
	before := `{"nodes":["😀源","报表"],"edges":[` +
		`{"from":"\ud83d\ude00\u6e90","to":"\u62a5\u8868"}]}`
	// Nodes escaped, edge endpoints literal.
	after := `{"nodes":["\ud83d\ude00\u6e90","\u62a5\u8868"],"edges":[` +
		`{"from":"😀源","to":"报表"}]}`

	assertNoLineageChange(t, before, after, "报表")
}

// Both sources are roots with no upstream of their own and every other
// relationship is untouched; swapping the target's one direct dependency to
// the other source reports exactly that relationship — one removal, one
// addition — with the decoded names and the from-source-to-derived direction.
func TestCompareUpstreamLineageSourceSwapReturnsDecodedNames(t *testing.T) {
	before := `{"nodes":["\u4e2d\u6587\u6e90","\u5907\u7528\u6e90","keep","t"],"edges":[` +
		`{"from":"keep","to":"t"},{"from":"\u4e2d\u6587\u6e90","to":"t"}]}`
	after := `{"nodes":["\u4e2d\u6587\u6e90","\u5907\u7528\u6e90","keep","t"],"edges":[` +
		`{"from":"keep","to":"t"},{"from":"\u5907\u7528\u6e90","to":"t"}]}`

	diff, err := CompareUpstreamLineage(before, after, "t")
	if err != nil {
		t.Fatalf("CompareUpstreamLineage: %v", err)
	}
	if got, want := depPairs(diff.Added), [][2]string{{"备用源", "t"}}; !reflect.DeepEqual(got, want) {
		t.Errorf("Added = %v, want %v", got, want)
	}
	if got, want := depPairs(diff.Removed), [][2]string{{"中文源", "t"}}; !reflect.DeepEqual(got, want) {
		t.Errorf("Removed = %v, want %v", got, want)
	}
}

// The legal boundary spellings keep their identity through the comparison: a
// real U+FFFD equals its \uFFFD escape, a matched \uD83D\uDE00 pair
// equals the literal emoji, and an escaped backslash before "uD800" is
// ordinary literal text — never a surrogate escape.
func TestCompareUpstreamLineageLegalBoundarySpellings(t *testing.T) {
	t.Run("real replacement character equals escaped", func(t *testing.T) {
		literal := `{"nodes":["�","t"],"edges":[{"from":"�","to":"t"}]}`
		escaped := `{"nodes":["\ufffd","t"],"edges":[{"from":"\ufffd","to":"t"}]}`
		assertNoLineageChange(t, literal, escaped, "t")
	})

	t.Run("paired escapes equal literal emoji", func(t *testing.T) {
		literal := `{"nodes":["😀","t"],"edges":[{"from":"😀","to":"t"}]}`
		escaped := `{"nodes":["\ud83d\ude00","t"],"edges":[{"from":"\ud83d\ude00","to":"t"}]}`
		assertNoLineageChange(t, literal, escaped, "t")
	})

	t.Run("escaped backslash stays literal text", func(t *testing.T) {
		// The JSON token "\\ud800" decodes to the six ordinary characters
		// \ud800: not a surrogate escape, so the document is valid and the
		// name is kept verbatim.
		doc := `{"nodes":["\\ud800","t"],"edges":[{"from":"\\ud800","to":"t"}]}`
		assertNoLineageChange(t, doc, doc, "t")

		// A change involving the literal-backslash name reports it verbatim,
		// backslash and all — it must not come back rewritten or paired off.
		swapped := `{"nodes":["\\ud800","other","t"],"edges":[{"from":"other","to":"t"}]}`
		diff, err := CompareUpstreamLineage(doc, swapped, "t")
		if err != nil {
			t.Fatalf("CompareUpstreamLineage: %v", err)
		}
		const literalName = `\ud800` // six characters: backslash, u, d, 8, 0, 0
		if got, want := depPairs(diff.Removed), [][2]string{{literalName, "t"}}; !reflect.DeepEqual(got, want) {
			t.Errorf("Removed = %v, want %v", got, want)
		}
		if got, want := depPairs(diff.Added), [][2]string{{"other", "t"}}; !reflect.DeepEqual(got, want) {
			t.Errorf("Added = %v, want %v", got, want)
		}
	})
}

// Strings inside annotation fields are not dataset names: a lone surrogate
// escape in a note value cannot fail the comparison on its own, and note
// content shaped like nodes or dependencies never enters the lineage.
func TestCompareUpstreamLineageNoteStringsAreNotNames(t *testing.T) {
	before := `{"nodes":["s","t"],` +
		`"edges":[{"from":"s","to":"t","note":"lone \ud800 in an edge note"}],` +
		`"notes":{"summary":"lone \udc00 here","datasets":["ghost","phantom"],` +
		`"links":[{"from":"ghost","to":"t"}]}}`
	after := `{"nodes":["s","t"],"edges":[{"from":"s","to":"t"}],` +
		`"notes":"\ud83d not a pair"}`

	// The lone escapes sit only in notes, so the comparison succeeds; the
	// ghost/phantom names and the ghost -> t "dependency" quoted inside the
	// notes must not leak into either list.
	assertNoLineageChange(t, before, after, "t")
}
