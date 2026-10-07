package chainledger

import (
	"reflect"
	"testing"
)

// These tests guard dataset-name identity across the upstream-lineage
// comparison: a name spelled with legal JSON \u escapes is the same name as
// the one written literally, so re-spelling a document must never surface as
// a false added or removed dependency, and reported names always come back
// decoded. The comparison scope stays the target's complete upstream
// derivation, exactly as CompareUpstreamLineage defines it.

// assertCompareNoChange requires a successful comparison whose Added and
// Removed lists are both non-nil and empty.
func assertCompareNoChange(t *testing.T, diff UpstreamLineageDiff, err error) {
	t.Helper()
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

// Two documents describe the exact same nodes and dependency; one writes the
// Chinese and emoji names literally, the other uses the equivalent \u escapes
// (the emoji as a matched high-low surrogate pair). The comparison must see
// identical names and report no change. Within each document the node list
// and the edge endpoints also use different legal spellings of the same
// names, and they must still connect — the target name itself carries these
// characters.
func TestCompareUpstreamLineageEquivalentEscapeSpellingsNoChange(t *testing.T) {
	// 中文源 -> 报表😀 with every name written literally.
	literal := `{"nodes":["中文源","报表😀"],` +
		`"edges":[{"from":"中文源","to":"报表😀"}]}`
	// The same document with every name escape-spelled: 中文源, 报表😀.
	escaped := `{"nodes":["\u4e2d\u6587\u6e90","\u62a5\u8868\ud83d\ude00"],` +
		`"edges":[{"from":"\u4e2d\u6587\u6e90","to":"\u62a5\u8868\ud83d\ude00"}]}`

	diff, err := CompareUpstreamLineage(literal, escaped, "报表😀")
	assertCompareNoChange(t, diff, err)

	// Node written one way and edge endpoint the other inside a single
	// document: the different legal spellings of one name still connect, so
	// the mixed document equals the literal one in both directions.
	mixed := `{"nodes":["中文源","报表😀"],` +
		`"edges":[{"from":"\u4e2d\u6587\u6e90","to":"\u62a5\u8868\ud83d\ude00"}]}`
	diff, err = CompareUpstreamLineage(literal, mixed, "报表😀")
	assertCompareNoChange(t, diff, err)
	diff, err = CompareUpstreamLineage(mixed, escaped, "报表😀")
	assertCompareNoChange(t, diff, err)
}

// A real U+FFFD replacement character written literally and the � escape
// are the same dataset name; swapping the spelling between the two documents
// is not a change.
func TestCompareUpstreamLineageReplacementCharacterSpellingsNoChange(t *testing.T) {
	literal := `{"nodes":["�","t"],"edges":[{"from":"�","to":"t"}]}`
	escaped := `{"nodes":["\ufffd","t"],"edges":[{"from":"\ufffd","to":"t"}]}`

	diff, err := CompareUpstreamLineage(literal, escaped, "t")
	assertCompareNoChange(t, diff, err)
}

// Both sources are roots with no upstreams of their own and every other
// relationship is untouched; only the target's one direct dependency moves
// from 源甲 to 源乙. Exactly that relationship is reported as one removal and
// one addition, with the names decoded (never the raw \uXXXX spelling) and
// the direction From the source To the derived dataset.
func TestCompareUpstreamLineageSourceSwapReportsDecodedNames(t *testing.T) {
	before := `{"nodes":["源甲","源乙","keep","报表😀"],` +
		`"edges":[{"from":"keep","to":"报表😀"},{"from":"\u6e90\u7532","to":"\u62a5\u8868\ud83d\ude00"}]}`
	after := `{"nodes":["源甲","源乙","keep","报表😀"],` +
		`"edges":[{"from":"keep","to":"报表😀"},{"from":"\u6e90\u4e59","to":"报表😀"}]}`

	diff, err := CompareUpstreamLineage(before, after, "报表😀")
	if err != nil {
		t.Fatalf("CompareUpstreamLineage: %v", err)
	}
	if got, want := depPairs(diff.Added), [][2]string{{"源乙", "报表😀"}}; !reflect.DeepEqual(got, want) {
		t.Errorf("Added = %v, want %v", got, want)
	}
	if got, want := depPairs(diff.Removed), [][2]string{{"源甲", "报表😀"}}; !reflect.DeepEqual(got, want) {
		t.Errorf("Removed = %v, want %v", got, want)
	}
}
