package chainledger

import (
	"strings"
	"testing"
)

// These tests pin the order CompareUpstreamLineage applies its two failure
// modes: each document is validated as a whole lineage first (the before
// document, then the after one), and only then is the target's registration
// checked in the same order. A document problem and a missing target are
// different failures with different messages — neither may be reported in
// place of the other — and either one fails the comparison as a whole with
// the zero UpstreamLineageDiff, never partial added/removed lists.

// assertCompareRejected requires a wholesale rejection: a non-nil error whose
// message contains every wanted fragment, and the zero UpstreamLineageDiff
// with both lists nil — a failed comparison never yields partial lists.
func assertCompareRejected(t *testing.T, diff UpstreamLineageDiff, err error, wants ...string) {
	t.Helper()
	if err == nil {
		t.Fatalf("CompareUpstreamLineage succeeded with %+v, want rejection mentioning %q", diff, wants)
	}
	if diff.Added != nil || diff.Removed != nil {
		t.Errorf("failed comparison must return the zero diff with nil lists, got %+v", diff)
	}
	msg := err.Error()
	for _, want := range wants {
		if !strings.Contains(msg, want) {
			t.Errorf("error %q must contain %q", msg, want)
		}
	}
}

// Document validation covers the whole lineage, not just the target's
// reachable upstream, and runs before the target-registration check. Here one
// document is valid but never registers the target while the other registers
// it yet carries a dependency cycle among datasets the target never touches:
// the cycle must be reported — not the other document's missing target.
func TestCompareUpstreamLineageValidatesWholeDocumentBeforeTargetLookup(t *testing.T) {
	// Valid, registers other datasets, but not the target t.
	withoutTarget := `{"nodes":["s"],"edges":[]}`
	// Registers t as a root; the a/b cycle is unrelated to t's derivation.
	cyclic := `{"nodes":["t","a","b"],"edges":[{"from":"a","to":"b"},{"from":"b","to":"a"}]}`

	diff, err := CompareUpstreamLineage(withoutTarget, cyclic, "t")
	assertCompareRejected(t, diff, err, "after-change", "cycle", "a -> b -> a")
	if strings.Contains(err.Error(), "not found") {
		t.Errorf("error %q must report the after-change cycle, not the missing target", err)
	}

	// Swapped: the cyclic document is the before one, and its cycle is
	// reported even though the after document lacks the target.
	diff, err = CompareUpstreamLineage(cyclic, withoutTarget, "t")
	assertCompareRejected(t, diff, err, "before-change", "cycle", "a -> b -> a")
	if strings.Contains(err.Error(), "not found") {
		t.Errorf("error %q must report the before-change cycle, not the missing target", err)
	}
}

// An invalid document and a valid document that simply does not register the
// target are distinct failures: the first is a document rejection carrying
// the document's own validation problem, the second names the target and the
// document it is missing from. Neither message may stand in for the other.
func TestCompareUpstreamLineageDistinguishesInvalidDocumentFromMissingTarget(t *testing.T) {
	withTarget := `{"nodes":["s","t"],"edges":[{"from":"s","to":"t"}]}`
	// Valid, with other registered datasets and their dependency, but no t.
	withoutTarget := `{"nodes":["s","u"],"edges":[{"from":"s","to":"u"}]}`
	invalid := `{"nodes":["s","t"],"edges":null}`

	diff, invalidErr := CompareUpstreamLineage(invalid, withTarget, "t")
	assertCompareRejected(t, diff, invalidErr, "before-change", "rejected", "null")
	if strings.Contains(invalidErr.Error(), "not found") {
		t.Errorf("invalid-document error %q must not read as a missing target", invalidErr)
	}

	diff, missingErr := CompareUpstreamLineage(withoutTarget, withTarget, "t")
	assertCompareRejected(t, diff, missingErr, "before-change", "not found", "t")
	if strings.Contains(missingErr.Error(), "rejected") {
		t.Errorf("missing-target error %q must not read as a document rejection", missingErr)
	}
}

// A missing, null, or wrongly typed nodes/edges field makes the document
// invalid — it is never a valid document that merely lacks the target. The
// empty arrays {"nodes":[],"edges":[]} are the valid empty lineage.
func TestCompareUpstreamLineageMalformedArraysAreDocumentRejections(t *testing.T) {
	withTarget := `{"nodes":["t"],"edges":[]}`
	malformed := map[string]string{
		"null edges":      `{"nodes":["t"],"edges":null}`,
		"null nodes":      `{"nodes":null,"edges":[]}`,
		"missing edges":   `{"nodes":["t"]}`,
		"missing nodes":   `{"edges":[]}`,
		"nodes not array": `{"nodes":"t","edges":[]}`,
		"edges not array": `{"nodes":["t"],"edges":{}}`,
	}
	for name, bad := range malformed {
		t.Run(name+" in before", func(t *testing.T) {
			diff, err := CompareUpstreamLineage(bad, withTarget, "t")
			assertCompareRejected(t, diff, err, "before-change", "rejected")
			if strings.Contains(err.Error(), "not found") {
				t.Errorf("error %q must reject the document, not report a missing target", err)
			}
		})
		t.Run(name+" in after", func(t *testing.T) {
			diff, err := CompareUpstreamLineage(withTarget, bad, "t")
			assertCompareRejected(t, diff, err, "after-change", "rejected")
			if strings.Contains(err.Error(), "not found") {
				t.Errorf("error %q must reject the document, not report a missing target", err)
			}
		})
	}
}

// When both documents are valid but the target is registered in neither —
// here one holds other datasets with their dependency and the other is the
// empty lineage — the before document's missing target is the one reported.
// Each single-sided case names the target's full name and its own document.
func TestCompareUpstreamLineageMissingTargetNamesDocumentAndFullTarget(t *testing.T) {
	const target = "报表 目标/v2"
	withTarget := `{"nodes":["src","aux","报表 目标/v2"],` +
		`"edges":[{"from":"src","to":"报表 目标/v2"},{"from":"aux","to":"src"}]}`
	// Valid documents without the target: other registered datasets with
	// their dependency, and the empty lineage.
	othersOnly := `{"nodes":["src","aux"],"edges":[{"from":"aux","to":"src"}]}`
	emptyLineage := `{"nodes":[],"edges":[]}`

	diff, err := CompareUpstreamLineage(othersOnly, withTarget, target)
	assertCompareRejected(t, diff, err, target, "before-change", "not found")
	if strings.Contains(err.Error(), "after-change") {
		t.Errorf("error %q must identify only the before-change document", err)
	}

	diff, err = CompareUpstreamLineage(withTarget, emptyLineage, target)
	assertCompareRejected(t, diff, err, target, "after-change", "not found")
	if strings.Contains(err.Error(), "before-change") {
		t.Errorf("error %q must identify only the after-change document", err)
	}

	diff, err = CompareUpstreamLineage(emptyLineage, othersOnly, target)
	assertCompareRejected(t, diff, err, target, "before-change", "not found")
	if strings.Contains(err.Error(), "after-change") {
		t.Errorf("error %q must report the before document's missing target first", err)
	}
}

// Whichever side lacks the target, the comparison fails as a whole: the zero
// diff comes back even though the document that does register the target
// would on its own yield dependencies for the other list.
func TestCompareUpstreamLineageMissingTargetYieldsNoPartialDiff(t *testing.T) {
	withTarget := `{"nodes":["s","m","t"],"edges":[{"from":"s","to":"m"},{"from":"m","to":"t"}]}`
	withoutTarget := `{"nodes":["s","m"],"edges":[{"from":"s","to":"m"}]}`

	diff, err := CompareUpstreamLineage(withTarget, withoutTarget, "t")
	assertCompareRejected(t, diff, err, "after-change", "not found")

	diff, err = CompareUpstreamLineage(withoutTarget, withTarget, "t")
	assertCompareRejected(t, diff, err, "before-change", "not found")
}

// When both documents have document-level problems of different kinds, the
// before document is validated first and its own problem is the one reported.
func TestCompareUpstreamLineageBothDocumentsInvalidReportsBeforeProblem(t *testing.T) {
	beforeBad := `{"nodes":["t"],"edges":null}`
	afterBad := `{"nodes":["t","a"],"edges":[{"from":"a","to":"a"}]}`

	diff, err := CompareUpstreamLineage(beforeBad, afterBad, "t")
	assertCompareRejected(t, diff, err, "before-change", "null")
	msg := err.Error()
	if strings.Contains(msg, "after-change") || strings.Contains(msg, "cycle") {
		t.Errorf("error %q must be the before document's rejection, not the after document's", msg)
	}
}

// An empty target name is rejected before anything else: even with valid
// documents that do register datasets, the missing-name error comes first.
func TestCompareUpstreamLineageEmptyTargetCheckedBeforeDocuments(t *testing.T) {
	valid := `{"nodes":["t"],"edges":[]}`
	for _, docs := range [][2]string{
		{valid, valid},
		{"not json", valid},
		{valid, "not json"},
	} {
		diff, err := CompareUpstreamLineage(docs[0], docs[1], "")
		if err == nil || err.Error() != "target dataset name is required" {
			t.Fatalf("CompareUpstreamLineage(%q, %q, \"\") error = %v, want target dataset name is required",
				docs[0], docs[1], err)
		}
		if diff.Added != nil || diff.Removed != nil {
			t.Errorf("failed comparison must return the zero diff with nil lists, got %+v", diff)
		}
	}
}

// A target registered in both valid documents but with no upstream anywhere
// compares successfully with two non-nil empty lists — including when one
// document is the empty-arrays lineage holding just the target, and when the
// target's downstream or an independent lineage changes legally between the
// documents. None of that may turn the success into a missing-target failure.
func TestCompareUpstreamLineageRootTargetSucceedsDespiteUnrelatedChanges(t *testing.T) {
	// t is a bare node in both documents; the after document changes the
	// independent x/y/z lineage and gives t a downstream.
	before := `{"nodes":["t","x","y"],"edges":[{"from":"x","to":"y"}]}`
	after := `{"nodes":["t","x","y","z","down"],"edges":[{"from":"y","to":"z"},{"from":"t","to":"down"}]}`

	diff, err := CompareUpstreamLineage(before, after, "t")
	assertCompareNoChange(t, diff, err)

	// The empty-arrays document holding just the target is valid too.
	justTarget := `{"nodes":["t"],"edges":[]}`
	diff, err = CompareUpstreamLineage(justTarget, justTarget, "t")
	assertCompareNoChange(t, diff, err)
}
