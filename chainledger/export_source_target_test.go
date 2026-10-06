package chainledger

import (
	"reflect"
	"sort"
	"strings"
	"testing"
)

func mustExportScoped(t *testing.T, graph map[string]*Lineage, source, target string) string {
	t.Helper()
	out, err := ExportSourceTargetLineage(graph, source, target)
	if err != nil {
		t.Fatalf("ExportSourceTargetLineage(%q, %q): %v", source, target, err)
	}
	return out
}

// scopedFixture builds the spec's worked graph:
//
//	pre ──> source ──┬──> a ──────────┐
//	                │   ^            ├──> report ──> view
//	                │   └── extra    │
//	                ├──> b ──> mid ──┘
//	                ├──> sidetrack   (source downstream that never reaches report)
//	                └──> report (direct)
//
// report additionally depends on the independent root lone, so its full
// upstream closure contains ancestry that source takes no part in.
func scopedFixture(t *testing.T) map[string]*Lineage {
	return buildRegisteredGraph(t, [][]string{
		{"pre"},
		{"source", "pre"},
		{"extra"},
		{"a", "source", "extra"},
		{"b", "source"},
		{"mid", "b"},
		{"sidetrack", "source"},
		{"lone"},
		{"report", "source", "a", "mid", "lone"},
		{"view", "report"},
	})
}

var scopedWantNodes = []string{"a", "b", "mid", "report", "source"}

var scopedWantEdges = [][2]string{
	{"a", "report"},
	{"b", "mid"},
	{"mid", "report"},
	{"source", "a"},
	{"source", "b"},
	{"source", "report"},
}

// The worked example from the spec: source derives report directly, through a
// and through b -> mid. All three routes stay complete in the scoped document;
// a's independent source extra (and extra -> a), source's ancestor pre,
// source's dead-end downstream sidetrack, report's independent upstream lone
// (and lone -> report) and report's downstream view all stay out.
func TestExportSourceTargetLineageScopedRoutes(t *testing.T) {
	graph := scopedFixture(t)
	before := snapshotExportGraph(graph)

	out := mustExportScoped(t, graph, "source", "report")
	doc := parseExport(t, out)

	if !reflect.DeepEqual(doc.Nodes, scopedWantNodes) {
		t.Errorf("nodes = %v, want %v", doc.Nodes, scopedWantNodes)
	}
	if got := exportEdgeNames(doc); !reflect.DeepEqual(got, scopedWantEdges) {
		t.Errorf("edges = %v, want %v", got, scopedWantEdges)
	}
	for _, excluded := range []string{"pre", "extra", "sidetrack", "lone", "view"} {
		for _, node := range doc.Nodes {
			if node == excluded {
				t.Errorf("nodes must not contain %q: %v", excluded, doc.Nodes)
			}
		}
	}
	assertGraphUnchanged(t, before, graph)
}

// The exact JSON text is pinned: same shape and ordering rules as the full
// export, restricted to the scoped node set.
func TestExportSourceTargetLineageExactJSON(t *testing.T) {
	graph := scopedFixture(t)

	out := mustExportScoped(t, graph, "source", "report")
	want := `{"nodes":["a","b","mid","report","source"],` +
		`"edges":[{"from":"a","to":"report"},{"from":"b","to":"mid"},` +
		`{"from":"mid","to":"report"},{"from":"source","to":"a"},` +
		`{"from":"source","to":"b"},{"from":"source","to":"report"}]}`
	if out != want {
		t.Errorf("export =\n%s\nwant:\n%s", out, want)
	}
}

// The direct edge and the longer routes are all retained together. A scoped
// export built with only the source-side routes must be byte-identical to the
// fixture's result even though the fixture also has the direct edge and extra
// ancestry; and limiting to a sub-path keeps just that path.
func TestExportSourceTargetLineageKeepsLongRoutesAndSubPaths(t *testing.T) {
	graph := buildRegisteredGraph(t, [][]string{
		{"source"},
		{"a", "source"},
		{"b", "source"},
		{"mid", "b"},
		{"report", "source", "a", "mid"},
	})

	doc := parseExport(t, mustExportScoped(t, graph, "source", "report"))
	if !reflect.DeepEqual(doc.Nodes, scopedWantNodes) {
		t.Errorf("nodes = %v, want %v", doc.Nodes, scopedWantNodes)
	}
	if got := exportEdgeNames(doc); !reflect.DeepEqual(got, scopedWantEdges) {
		t.Errorf("edges = %v, want %v", got, scopedWantEdges)
	}

	// Scope to an intermediate target: just the source -> b -> mid route.
	sub := parseExport(t, mustExportScoped(t, graph, "source", "mid"))
	if !reflect.DeepEqual(sub.Nodes, []string{"b", "mid", "source"}) {
		t.Errorf("sub-path nodes = %v", sub.Nodes)
	}
	if got := exportEdgeNames(sub); !reflect.DeepEqual(got, [][2]string{
		{"b", "mid"},
		{"source", "b"},
	}) {
		t.Errorf("sub-path edges = %v", got)
	}

	// Scope to a sub-path starting at an intermediate source.
	aOnly := parseExport(t, mustExportScoped(t, graph, "a", "report"))
	if !reflect.DeepEqual(aOnly.Nodes, []string{"a", "report"}) {
		t.Errorf("a-scope nodes = %v", aOnly.Nodes)
	}
	if got := exportEdgeNames(aOnly); !reflect.DeepEqual(got, [][2]string{{"a", "report"}}) {
		t.Errorf("a-scope edges = %v", got)
	}
}

// Nodes and edges shared by several routes (diamond merges) appear once each.
func TestExportSourceTargetLineageMergesOnce(t *testing.T) {
	graph := buildRegisteredGraph(t, [][]string{
		{"source"},
		{"a", "source"},
		{"b", "source"},
		{"merge", "a", "b"},
		{"report", "merge", "a"},
	})

	doc := parseExport(t, mustExportScoped(t, graph, "source", "report"))
	wantNodes := []string{"a", "b", "merge", "report", "source"}
	if !reflect.DeepEqual(doc.Nodes, wantNodes) {
		t.Errorf("nodes = %v, want %v", doc.Nodes, wantNodes)
	}
	wantEdges := [][2]string{
		{"a", "merge"},
		{"a", "report"},
		{"b", "merge"},
		{"merge", "report"},
		{"source", "a"},
		{"source", "b"},
	}
	if got := exportEdgeNames(doc); !reflect.DeepEqual(got, wantEdges) {
		t.Errorf("edges = %v, want %v", got, wantEdges)
	}
}

// Source and target being the same registered dataset exports that dataset
// alone with an empty (non-null) edges array.
func TestExportSourceTargetLineageSameSourceAndTarget(t *testing.T) {
	graph := buildRegisteredGraph(t, [][]string{
		{"pre"},
		{"source", "pre"},
		{"a", "source"},
	})

	out := mustExportScoped(t, graph, "source", "source")
	if want := `{"nodes":["source"],"edges":[]}`; out != want {
		t.Errorf("export = %s, want %s", out, want)
	}
	doc := parseExport(t, out)
	if doc.Edges == nil {
		t.Errorf("edges must decode as an empty array, got nil from %s", out)
	}
}

// Two registered datasets with no source -> target route succeed with two
// empty arrays: disconnected pairs, a reverse dependency, and a route that
// stops one hop short all keep no isolated endpoint.
func TestExportSourceTargetLineageUnreachable(t *testing.T) {
	graph := buildRegisteredGraph(t, [][]string{
		{"source"},
		{"other"},
		{"child", "source"},
		{"report", "other"}, // report derives the other root: source cannot reach it
		{"downstream", "report"},
	})

	cases := []struct {
		name   string
		source string
		target string
	}{
		{"disconnected", "source", "other"},
		{"reverse dependency", "report", "other"},
		{"route stops short", "source", "downstream"},
		{"siblings", "child", "report"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			out := mustExportScoped(t, graph, tc.source, tc.target)
			if want := `{"nodes":[],"edges":[]}`; out != want {
				t.Errorf("export = %s, want %s", out, want)
			}
			doc := parseExport(t, out)
			if len(doc.Nodes) != 0 || doc.Nodes == nil {
				t.Errorf("nodes must be a non-nil empty array, got %#v", doc.Nodes)
			}
			if len(doc.Edges) != 0 || doc.Edges == nil {
				t.Errorf("edges must be a non-nil empty array, got %#v", doc.Edges)
			}
		})
	}
}

// The same lineage built in different registration orders and with reordered
// upstream lists exports byte-identical text.
func TestExportSourceTargetLineageOrderIndependent(t *testing.T) {
	first := buildRegisteredGraph(t, [][]string{
		{"pre"},
		{"source", "pre"},
		{"extra"},
		{"a", "source", "extra"},
		{"b", "source"},
		{"mid", "b"},
		{"sidetrack", "source"},
		{"lone"},
		{"report", "source", "a", "mid", "lone"},
		{"view", "report"},
	})
	second := buildRegisteredGraph(t, [][]string{
		{"lone"},
		{"extra"},
		{"pre"},
		{"source", "pre"},
		{"a", "extra", "source"},
		{"sidetrack", "source"},
		{"b", "source"},
		{"mid", "b"},
		{"report", "lone", "mid", "a", "source"},
		{"view", "report"},
	})

	if got, want := mustExportScoped(t, first, "source", "report"),
		mustExportScoped(t, second, "source", "report"); got != want {
		t.Errorf("registration order changed the export:\nfirst:  %s\nsecond: %s", got, want)
	}
}

// With no route, even the two endpoints are unselected, so invalid UTF-8 in
// their names must not block the successful two-empty-arrays result: only
// actually selected names are encoding-checked.
func TestExportSourceTargetLineageUnreachableSkipsInvalidEndpointNames(t *testing.T) {
	graph := buildRegisteredGraph(t, [][]string{
		{"bad\xffsource"},
		{"bad\xfetarget"},
	})

	out, err := ExportSourceTargetLineage(graph, "bad\xffsource", "bad\xfetarget")
	if err != nil {
		t.Fatalf("unreachable scoped export failed on unselected names: %v", err)
	}
	if want := `{"nodes":[],"edges":[]}`; out != want {
		t.Errorf("export = %s, want %s", out, want)
	}
}

// Names match by exact registered value: a case difference is unregistered.
func TestExportSourceTargetLineageCaseSensitive(t *testing.T) {
	graph := buildRegisteredGraph(t, [][]string{
		{"source"},
		{"report", "source"},
	})

	out, err := ExportSourceTargetLineage(graph, "Source", "report")
	if err == nil {
		t.Fatalf("case-different source succeeded with %q", out)
	}
	if !strings.Contains(err.Error(), "Source") {
		t.Errorf("error %q must name the queried source", err)
	}

	out, err = ExportSourceTargetLineage(graph, "source", "Report")
	if err == nil {
		t.Fatalf("case-different target succeeded with %q", out)
	}
	if !strings.Contains(err.Error(), "Report") {
		t.Errorf("error %q must name the queried target", err)
	}
}

// Source is validated before target: its empty or missing-name error wins
// regardless of target's state; target is checked only afterwards. Failures
// return an empty string and never partial content.
func TestExportSourceTargetLineageValidationOrder(t *testing.T) {
	graph := buildRegisteredGraph(t, [][]string{
		{"source"},
		{"report", "source"},
	})
	before := snapshotExportGraph(graph)

	cases := []struct {
		name    string
		graph   map[string]*Lineage
		source  string
		target  string
		wantErr string
	}{
		{"empty source first", graph, "", "", "dataset name is required"},
		{"empty source over bad target", graph, "", "ghost", "dataset name is required"},
		{"missing source first", graph, "ghost", "", "dataset not found: ghost"},
		{"missing source over missing target", graph, "ghost", "phantom", "dataset not found: ghost"},
		{"source valid then empty target", graph, "source", "", "dataset name is required"},
		{"source valid then missing target", graph, "source", "ghost", "dataset not found: ghost"},
		{"empty graph source", map[string]*Lineage{}, "source", "report", "dataset not found: source"},
		{"nil graph source", nil, "source", "report", "dataset not found: source"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			out, err := ExportSourceTargetLineage(tc.graph, tc.source, tc.target)
			if err == nil {
				t.Fatalf("(%q, %q) succeeded with %q, want error %q",
					tc.source, tc.target, out, tc.wantErr)
			}
			if err.Error() != tc.wantErr {
				t.Errorf("error = %q, want %q", err, tc.wantErr)
			}
			if out != "" {
				t.Errorf("failed export returned partial content %q, want empty string", out)
			}
		})
	}
	assertGraphUnchanged(t, before, graph)
}

// An invalid UTF-8 name on a selected route fails the whole scoped export
// under the full export's encoding rule, naming the offending bytes and
// returning no document.
func TestExportSourceTargetLineageRejectsInvalidUTF8OnRoute(t *testing.T) {
	graph := buildRegisteredGraph(t, [][]string{
		{"source"},
		{"bad\xffmid", "source"},
		{"report", "bad\xffmid"},
	})
	before := snapshotExportGraph(graph)

	out, err := ExportSourceTargetLineage(graph, "source", "report")
	if err == nil {
		t.Fatalf("scoped export with invalid intermediate node succeeded with %q", out)
	}
	if out != "" {
		t.Errorf("failed export returned partial content %q", out)
	}
	if !strings.Contains(err.Error(), `"bad\xffmid"`) {
		t.Errorf("error %q must quote the original bytes", err)
	}
	assertGraphUnchanged(t, before, graph)
}

// The endpoints are selected too: an invalid source or target name fails even
// though there is nothing else on the route.
func TestExportSourceTargetLineageRejectsInvalidUTF8Endpoints(t *testing.T) {
	badSource := "src\xff"
	graph := buildRegisteredGraph(t, [][]string{
		{badSource},
		{"report", badSource},
	})
	if out, err := ExportSourceTargetLineage(graph, badSource, "report"); err == nil {
		t.Fatalf("invalid source exported %q", out)
	} else if !strings.Contains(err.Error(), `"src\xff"`) {
		t.Errorf("error %q must name the invalid source bytes", err)
	}

	badTarget := "rep\xff"
	graph2 := buildRegisteredGraph(t, [][]string{
		{"source"},
		{badTarget, "source"},
	})
	if out, err := ExportSourceTargetLineage(graph2, "source", badTarget); err == nil {
		t.Fatalf("invalid target exported %q", out)
	} else if !strings.Contains(err.Error(), `"rep\xff"`) {
		t.Errorf("error %q must name the invalid target bytes", err)
	}

	// Source and target identical: that node is selected (the single-node
	// document), so its invalid name still fails the export.
	if out, err := ExportSourceTargetLineage(graph2, badTarget, badTarget); err == nil {
		t.Fatalf("same-name invalid endpoint exported %q", out)
	} else if !strings.Contains(err.Error(), `"rep\xff"`) {
		t.Errorf("error %q must name the invalid endpoint bytes", err)
	}
}

// Invalid names anywhere outside the selected routes — source's ancestor,
// target's independent upstream, a dead-end source downstream, target's
// downstream, a standalone dataset — do not block the scoped export.
func TestExportSourceTargetLineageIgnoresInvalidNamesOutsideRoutes(t *testing.T) {
	graph := buildRegisteredGraph(t, [][]string{
		{"pre\xff"},
		{"source", "pre\xff"},
		{"extra\xfe"},
		{"a", "source", "extra\xfe"},
		{"b", "source"},
		{"mid", "b"},
		{"sidetrack\xfd", "source"},
		{"lone\xfc"},
		{"report", "source", "a", "mid", "lone\xfc"},
		{"view\xfb", "report"},
		{"isolated\xfa"},
	})

	out := mustExportScoped(t, graph, "source", "report")
	doc := parseExport(t, out)
	if !reflect.DeepEqual(doc.Nodes, scopedWantNodes) {
		t.Errorf("nodes = %v, want %v", doc.Nodes, scopedWantNodes)
	}
	if got := exportEdgeNames(doc); !reflect.DeepEqual(got, scopedWantEdges) {
		t.Errorf("edges = %v, want %v", got, scopedWantEdges)
	}

	// The same graph's full upstream export of report must still fail on
	// report's invalid independent upstream, proving the scoped export judges
	// only its own selected set.
	if full, err := ExportUpstreamLineage(graph, "report"); err == nil {
		t.Errorf("full export unexpectedly succeeded with %q, want an encoding error", full)
	}
}

// When several selected names are invalid, the string-smallest one is
// reported identically under reversed registration and upstream-list orders.
func TestExportSourceTargetLineageFirstInvalidNameOrderIndependent(t *testing.T) {
	fe := "x\xfe"
	ff := "x\xff"
	first := buildRegisteredGraph(t, [][]string{
		{"source"},
		{fe, "source"},
		{ff, "source"},
		{"report", fe, ff},
	})
	second := buildRegisteredGraph(t, [][]string{
		{"source"},
		{ff, "source"},
		{fe, "source"},
		{"report", ff, fe},
	})

	_, err1 := ExportSourceTargetLineage(first, "source", "report")
	_, err2 := ExportSourceTargetLineage(second, "source", "report")
	if err1 == nil || err2 == nil {
		t.Fatalf("exports with invalid route nodes must fail, got %v and %v", err1, err2)
	}
	if err1.Error() != err2.Error() {
		t.Errorf("error depends on registration/list order:\nfirst:  %q\nsecond: %q", err1, err2)
	}
	if !strings.Contains(err1.Error(), `"x\xfe"`) {
		t.Errorf("error %q must report the string-smallest invalid name x\\xfe", err1)
	}
}

// Missing-name and not-found checks keep priority over encoding validation,
// even while the graph holds invalid names.
func TestExportSourceTargetLineageErrorPrecedence(t *testing.T) {
	graph := buildRegisteredGraph(t, [][]string{
		{"bad\xff"},
		{"good", "bad\xff"},
	})
	for _, tc := range []struct {
		source  string
		target  string
		wantErr string
	}{
		{"", "good", "dataset name is required"},
		{"ghost", "good", "dataset not found: ghost"},
		{"bad\xff", "", "dataset name is required"},
		{"bad\xff", "ghost", "dataset not found: ghost"},
	} {
		out, err := ExportSourceTargetLineage(graph, tc.source, tc.target)
		if err == nil {
			t.Fatalf("(%q, %q) succeeded with %q", tc.source, tc.target, out)
		}
		if err.Error() != tc.wantErr {
			t.Errorf("(%q, %q): error = %q, want %q", tc.source, tc.target, err, tc.wantErr)
		}
		if out != "" {
			t.Errorf("failure returned partial content %q", out)
		}
	}
}

// Success and failure alike leave nodes, both edge directions and list order
// untouched.
func TestExportSourceTargetLineageReadOnly(t *testing.T) {
	graph := scopedFixture(t)
	before := snapshotExportGraph(graph)

	mustExportScoped(t, graph, "source", "report")
	mustExportScoped(t, graph, "source", "source")
	mustExportScoped(t, graph, "source", "sidetrack") // empty result
	if _, err := ExportSourceTargetLineage(graph, "ghost", "report"); err == nil {
		t.Fatal("expected a not-found error for the source")
	}
	if _, err := ExportSourceTargetLineage(graph, "source", "ghost"); err == nil {
		t.Fatal("expected a not-found error for the target")
	}
	assertGraphUnchanged(t, before, graph)
}

// A valid unusual name on the selected routes round-trips exactly.
func TestExportSourceTargetLineageRoundTripsUnusualValidNames(t *testing.T) {
	emoji := "source 🚀"
	quote := `report "x"`
	graph := buildRegisteredGraph(t, [][]string{
		{emoji},
		{"mid", emoji},
		{quote, "mid"},
	})

	doc := parseExport(t, mustExportScoped(t, graph, emoji, quote))
	wantNodes := []string{emoji, "mid", quote}
	sort.Strings(wantNodes) // the export's own ordering rule
	if !reflect.DeepEqual(doc.Nodes, wantNodes) {
		t.Errorf("nodes = %q, want %q", doc.Nodes, wantNodes)
	}
	if got := exportEdgeNames(doc); !reflect.DeepEqual(got, [][2]string{
		{"mid", quote},
		{emoji, "mid"},
	}) {
		t.Errorf("edges = %q", got)
	}
}
