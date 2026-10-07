package chainledger

import (
	"reflect"
	"strings"
	"testing"
)

// diffDoc renders a lineage document in the export shape for test input.
func diffDoc(nodes []string, edges ...[2]string) string {
	var b strings.Builder
	b.WriteString(`{"nodes":[`)
	for i, n := range nodes {
		if i > 0 {
			b.WriteByte(',')
		}
		writeJSONString(&b, n)
	}
	b.WriteString(`],"edges":[`)
	for i, e := range edges {
		if i > 0 {
			b.WriteByte(',')
		}
		b.WriteString(`{"from":`)
		writeJSONString(&b, e[0])
		b.WriteString(`,"to":`)
		writeJSONString(&b, e[1])
		b.WriteString(`}`)
	}
	b.WriteString(`]}`)
	return b.String()
}

// writeJSONString writes s as a JSON string, escaping the characters JSON
// requires. Test names stay within printable ASCII plus the occasional
// escaped control character.
func writeJSONString(b *strings.Builder, s string) {
	b.WriteByte('"')
	for _, r := range s {
		switch r {
		case '"':
			b.WriteString(`\"`)
		case '\\':
			b.WriteString(`\\`)
		case '\n':
			b.WriteString(`\n`)
		default:
			b.WriteRune(r)
		}
	}
	b.WriteByte('"')
}

func assertDiff(t *testing.T, diff UpstreamLineageDiff, wantAdded, wantRemoved []LineageDiffEdge) {
	t.Helper()
	if diff.Added == nil {
		t.Errorf("Added is nil; want a non-nil list")
	}
	if diff.Removed == nil {
		t.Errorf("Removed is nil; want a non-nil list")
	}
	if !reflect.DeepEqual(diff.Added, wantAdded) {
		t.Errorf("Added = %v, want %v", diff.Added, wantAdded)
	}
	if !reflect.DeepEqual(diff.Removed, wantRemoved) {
		t.Errorf("Removed = %v, want %v", diff.Removed, wantRemoved)
	}
}

// A direct dependency added between the two snapshots is reported once, in
// the export's from/to direction, and an untouched dependency is not listed.
func TestDiffUpstreamLineageBasicAddRemove(t *testing.T) {
	before := diffDoc(
		[]string{"raw", "detail", "report", "extra"},
		[2]string{"raw", "detail"}, [2]string{"detail", "report"},
		[2]string{"extra", "report"},
	)
	after := diffDoc(
		[]string{"raw", "detail", "report"},
		[2]string{"raw", "detail"}, [2]string{"detail", "report"},
	)

	diff, err := DiffUpstreamLineage(before, after, "report")
	if err != nil {
		t.Fatalf("DiffUpstreamLineage: %v", err)
	}
	assertDiff(t, diff,
		[]LineageDiffEdge{},
		[]LineageDiffEdge{{From: "extra", To: "report"}},
	)

	// The other direction of the same change reports the addition.
	diff, err = DiffUpstreamLineage(after, before, "report")
	if err != nil {
		t.Fatalf("DiffUpstreamLineage: %v", err)
	}
	assertDiff(t, diff,
		[]LineageDiffEdge{{From: "extra", To: "report"}},
		[]LineageDiffEdge{},
	)
}

// Identical documents compare as a successful no-change result with two
// non-nil empty groups.
func TestDiffUpstreamLineageNoChange(t *testing.T) {
	doc := diffDoc(
		[]string{"raw", "mid", "report"},
		[2]string{"raw", "mid"}, [2]string{"mid", "report"},
	)
	diff, err := DiffUpstreamLineage(doc, doc, "report")
	if err != nil {
		t.Fatalf("DiffUpstreamLineage: %v", err)
	}
	assertDiff(t, diff, []LineageDiffEdge{}, []LineageDiffEdge{})
}

// A target with no upstreams in both documents is also a successful
// no-change comparison.
func TestDiffUpstreamLineageNoChangeRootTarget(t *testing.T) {
	doc := diffDoc([]string{"lone"})
	diff, err := DiffUpstreamLineage(doc, doc, "lone")
	if err != nil {
		t.Fatalf("DiffUpstreamLineage: %v", err)
	}
	assertDiff(t, diff, []LineageDiffEdge{}, []LineageDiffEdge{})
}

// report depends on raw directly AND through the raw -> mid -> report route.
// Dropping the mid -> report edge of the longer route must show up even
// though the direct raw -> report dependency still reaches report; and
// because mid then stops participating in report's derivation, raw -> mid is
// reported as removed too.
func TestDiffUpstreamLineageLongerRouteEdgeRemoval(t *testing.T) {
	before := diffDoc(
		[]string{"raw", "mid", "report"},
		[2]string{"raw", "report"}, [2]string{"raw", "mid"}, [2]string{"mid", "report"},
	)
	after := diffDoc(
		[]string{"raw", "mid", "report"},
		[2]string{"raw", "report"}, [2]string{"raw", "mid"},
	)

	diff, err := DiffUpstreamLineage(before, after, "report")
	if err != nil {
		t.Fatalf("DiffUpstreamLineage: %v", err)
	}
	assertDiff(t, diff,
		[]LineageDiffEdge{},
		[]LineageDiffEdge{{From: "mid", To: "report"}, {From: "raw", To: "mid"}},
	)
}

// Removing one edge can detach a whole upstream segment: cutting deep ->
// report removes mid, deep and their dependencies from report's derivation,
// and every one of those dependencies is listed as removed — not just the cut
// edge.
func TestDiffUpstreamLineageDetachedSegmentFullyRemoved(t *testing.T) {
	before := diffDoc(
		[]string{"a", "mid", "deep", "report"},
		[2]string{"a", "mid"}, [2]string{"mid", "deep"}, [2]string{"deep", "report"},
		[2]string{"a", "report"},
	)
	after := diffDoc(
		[]string{"a", "mid", "deep", "report"},
		[2]string{"a", "mid"}, [2]string{"mid", "deep"},
		[2]string{"a", "report"},
	)

	diff, err := DiffUpstreamLineage(before, after, "report")
	if err != nil {
		t.Fatalf("DiffUpstreamLineage: %v", err)
	}
	assertDiff(t, diff,
		[]LineageDiffEdge{},
		[]LineageDiffEdge{
			{From: "a", To: "mid"},
			{From: "deep", To: "report"},
			{From: "mid", To: "deep"},
		},
	)
}

// report's direct upstream mid is unchanged, but mid's own source switches
// from old to new: the diff shows the ancestor's changed dependencies.
func TestDiffUpstreamLineageAncestorSwitchesSource(t *testing.T) {
	before := diffDoc(
		[]string{"old", "mid", "report"},
		[2]string{"old", "mid"}, [2]string{"mid", "report"},
	)
	after := diffDoc(
		[]string{"new", "mid", "report"},
		[2]string{"new", "mid"}, [2]string{"mid", "report"},
	)

	diff, err := DiffUpstreamLineage(before, after, "report")
	if err != nil {
		t.Fatalf("DiffUpstreamLineage: %v", err)
	}
	assertDiff(t, diff,
		[]LineageDiffEdge{{From: "new", To: "mid"}},
		[]LineageDiffEdge{{From: "old", To: "mid"}},
	)
}

// Node and edge order inside the documents, and duplicated node or edge
// listings, never create differences.
func TestDiffUpstreamLineageOrderAndDuplicatesAreInvisible(t *testing.T) {
	ordered := diffDoc(
		[]string{"a", "b", "report"},
		[2]string{"a", "report"}, [2]string{"b", "report"},
	)
	// Same lineage with reversed listings and repeated nodes and edges.
	shuffled := `{"nodes":["report","b","a","b","report"],` +
		`"edges":[{"from":"b","to":"report"},{"from":"a","to":"report"},` +
		`{"from":"b","to":"report"},{"from":"a","to":"report"}]}`

	diff, err := DiffUpstreamLineage(ordered, shuffled, "report")
	if err != nil {
		t.Fatalf("DiffUpstreamLineage: %v", err)
	}
	assertDiff(t, diff, []LineageDiffEdge{}, []LineageDiffEdge{})
}

// Independent datasets, the target's downstreams, and relationships that do
// not participate in the target's derivation never affect the result.
func TestDiffUpstreamLineageIgnoresUnrelatedLineage(t *testing.T) {
	before := diffDoc(
		[]string{"raw", "report", "view", "isolated", "other"},
		[2]string{"raw", "report"}, [2]string{"report", "view"},
		[2]string{"other", "isolated"},
	)
	// The after document rewires everything outside report's upstream scope.
	after := diffDoc(
		[]string{"raw", "report", "view", "isolated", "other", "fresh"},
		[2]string{"raw", "report"}, [2]string{"view", "report"},
		[2]string{"other", "fresh"}, [2]string{"fresh", "isolated"},
	)

	diff, err := DiffUpstreamLineage(before, after, "report")
	if err != nil {
		t.Fatalf("DiffUpstreamLineage: %v", err)
	}
	assertDiff(t, diff,
		[]LineageDiffEdge{{From: "view", To: "report"}},
		[]LineageDiffEdge{},
	)
}

// Names are compared by exact decoded value: case variants and padding are
// different datasets, so renaming a source only by case shows as remove+add.
func TestDiffUpstreamLineageNamesAreExact(t *testing.T) {
	before := diffDoc(
		[]string{"Raw", "report"},
		[2]string{"Raw", "report"},
	)
	after := diffDoc(
		[]string{"raw", " report", "report"},
		[2]string{"raw", "report"},
	)

	diff, err := DiffUpstreamLineage(before, after, "report")
	if err != nil {
		t.Fatalf("DiffUpstreamLineage: %v", err)
	}
	assertDiff(t, diff,
		[]LineageDiffEdge{{From: "raw", To: "report"}},
		[]LineageDiffEdge{{From: "Raw", To: "report"}},
	)
}

// Each group is ordered by From and then To in Go string order, independent
// of the document's own listing order.
func TestDiffUpstreamLineageResultOrdering(t *testing.T) {
	before := diffDoc([]string{"report"})
	after := diffDoc(
		[]string{"b", "a", "report", "a2"},
		[2]string{"b", "report"}, [2]string{"a", "report"},
		[2]string{"a", "a2"}, [2]string{"a2", "report"},
	)

	diff, err := DiffUpstreamLineage(before, after, "report")
	if err != nil {
		t.Fatalf("DiffUpstreamLineage: %v", err)
	}
	assertDiff(t, diff,
		[]LineageDiffEdge{
			{From: "a", To: "a2"},
			{From: "a", To: "report"},
			{From: "a2", To: "report"},
			{From: "b", To: "report"},
		},
		[]LineageDiffEdge{},
	)
}

// An empty target fails as a missing name before any document is read.
func TestDiffUpstreamLineageEmptyTarget(t *testing.T) {
	doc := diffDoc([]string{"a"})
	if _, err := DiffUpstreamLineage(doc, doc, ""); err == nil ||
		err.Error() != "dataset name is required" {
		t.Fatalf("empty target error = %v, want %q", err, "dataset name is required")
	}
}

// A target missing from either document fails the whole comparison, and the
// error names the target and says which document lacks it.
func TestDiffUpstreamLineageTargetNotRegistered(t *testing.T) {
	withTarget := diffDoc([]string{"raw", "report"}, [2]string{"raw", "report"})
	withoutTarget := diffDoc([]string{"raw", "other"}, [2]string{"raw", "other"})

	if _, err := DiffUpstreamLineage(withoutTarget, withTarget, "report"); err == nil ||
		err.Error() != "target dataset not found in the before document: report" {
		t.Errorf("before-missing error = %v", err)
	}
	if _, err := DiffUpstreamLineage(withTarget, withoutTarget, "report"); err == nil ||
		err.Error() != "target dataset not found in the after document: report" {
		t.Errorf("after-missing error = %v", err)
	}
	// Missing from both: the before document is reported first.
	if _, err := DiffUpstreamLineage(withoutTarget, withoutTarget, "report"); err == nil ||
		err.Error() != "target dataset not found in the before document: report" {
		t.Errorf("both-missing error = %v", err)
	}
}

// A document that fails import validation fails the whole comparison; the
// error says which document was rejected and no partial diff is returned.
func TestDiffUpstreamLineageDocumentRejected(t *testing.T) {
	good := diffDoc([]string{"raw", "report"}, [2]string{"raw", "report"})
	cycle := `{"nodes":["a","b"],"edges":[{"from":"a","to":"b"},{"from":"b","to":"a"}]}`
	missingEndpoint := `{"nodes":["a","b"],"edges":[{"from":"a","to":"ghost"}]}`
	malformed := `{"nodes":[`

	cases := []struct {
		name         string
		before       string
		after        string
		wantPrefix   string
		wantContains string
	}{
		{"before cycle", cycle, good, "before document rejected: ", "lineage contains a cycle"},
		{"after cycle", good, cycle, "after document rejected: ", "lineage contains a cycle"},
		{"before missing endpoint", missingEndpoint, good, "before document rejected: ", "edge endpoint not listed in nodes: ghost"},
		{"after missing endpoint", good, missingEndpoint, "after document rejected: ", "edge endpoint not listed in nodes: ghost"},
		{"before malformed", malformed, good, "before document rejected: ", ""},
		{"after malformed", good, malformed, "after document rejected: ", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			diff, err := DiffUpstreamLineage(tc.before, tc.after, "report")
			if err == nil {
				t.Fatalf("expected an error, got diff %+v", diff)
			}
			if !strings.HasPrefix(err.Error(), tc.wantPrefix) {
				t.Errorf("error %q does not start with %q", err.Error(), tc.wantPrefix)
			}
			if tc.wantContains != "" && !strings.Contains(err.Error(), tc.wantContains) {
				t.Errorf("error %q does not contain %q", err.Error(), tc.wantContains)
			}
			if diff.Added != nil || diff.Removed != nil {
				t.Errorf("failed comparison returned a partial diff: %+v", diff)
			}
		})
	}
}

// The comparison works directly on export output: exporting a target's
// upstream lineage from two graphs and diffing the texts matches diffing
// hand-written documents of the same lineage.
func TestDiffUpstreamLineageRoundTripsExportDocuments(t *testing.T) {
	build := func(edges ...[2]string) map[string]*Lineage {
		graph := map[string]*Lineage{}
		names := map[string]bool{}
		for _, e := range edges {
			names[e[0]] = true
			names[e[1]] = true
		}
		for name := range names {
			if err := Register(graph, Dataset{Name: name}, nil); err != nil {
				t.Fatalf("register %s: %v", name, err)
			}
		}
		parents := map[string][]string{}
		for _, e := range edges {
			parents[e[1]] = append(parents[e[1]], e[0])
		}
		for name, ups := range parents {
			if err := Register(graph, Dataset{Name: name}, ups); err != nil {
				t.Fatalf("register %s: %v", name, err)
			}
		}
		return graph
	}

	beforeGraph := build(
		[2]string{"raw", "a"}, [2]string{"a", "report"}, [2]string{"raw", "report"},
	)
	afterGraph := build(
		[2]string{"raw", "a"}, [2]string{"raw", "report"},
	)

	beforeText, err := ExportUpstreamLineage(beforeGraph, "report")
	if err != nil {
		t.Fatalf("export before: %v", err)
	}
	afterText, err := ExportUpstreamLineage(afterGraph, "report")
	if err != nil {
		t.Fatalf("export after: %v", err)
	}

	diff, err := DiffUpstreamLineage(beforeText, afterText, "report")
	if err != nil {
		t.Fatalf("DiffUpstreamLineage: %v", err)
	}
	assertDiff(t, diff,
		[]LineageDiffEdge{},
		[]LineageDiffEdge{{From: "a", To: "report"}, {From: "raw", To: "a"}},
	)
}
