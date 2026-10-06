package chainledger

import (
	"bytes"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"testing"
)

// scopedReregisterBadMid is a registered intermediate dataset whose name
// carries a REAL lone 0xff byte — invalid UTF-8 — not the four printable
// characters backslash, x, f, f. Such a name registers and participates in
// lineage normally; only an export whose selected node set contains it must
// reject the document.
const scopedReregisterBadMid = "mid\xff"

// scopedBadMidEncodingGraph builds the primary regression lineage:
//
//	source ──> legal ────────┐
//	source ──> clean ────────┼──> target ──> view
//	source ──> mid\xff ──────┘   (mid\xff additionally depends on indie)
//	indie ───> mid\xff
//
// target is derived along THREE routes from source: two with perfectly legal
// names (through legal and through clean) and one through mid\xff, whose name
// cannot be exported losslessly. mid\xff sits on an actual source -> target
// route for as long as it depends (directly) on source. The same mid\xff keeps
// a second, independent parent indie, which lets a same-name re-registration
// detach mid\xff from source WITHOUT unregistering it or removing its edge into
// target: mid\xff then stays a registered upstream of target on target's
// "other sources" branch.
func scopedBadMidEncodingGraph(t *testing.T) map[string]*Lineage {
	t.Helper()
	// Self-check the fixture: 0xff must be an actual byte of the name rather
	// than literal "\xff" text, otherwise this regression would test nothing.
	if !bytes.Contains([]byte(scopedReregisterBadMid), []byte{0xff}) ||
		strings.Contains(scopedReregisterBadMid, `\xff`) {
		t.Fatalf("fixture constant must hold a real 0xff byte, got %q", scopedReregisterBadMid)
	}
	return buildRegisteredGraph(t, [][]string{
		{"source"},
		{"legal", "source"},
		{"clean", "source"},
		{"indie"},
		{scopedReregisterBadMid, "source", "indie"},
		{"target", "legal", "clean", scopedReregisterBadMid},
		{"view", "target"},
	})
}

// scopedTwoBadRoutesEncodingGraph is the two-route variant: mid\xff reaches
// source through TWO different legal-named intermediates a and b, so a single
// same-name re-registration can sever one route while the other keeps mid\xff
// on a source -> target route.
//
//	source ──> a ──────────┐
//	source ──> b ──────────┼──> mid\xff ──> target
//	indie ────────────────┘
//	source ──> legal ──────┘   (target's clean parallel route)
func scopedTwoBadRoutesEncodingGraph(t *testing.T) map[string]*Lineage {
	t.Helper()
	return buildRegisteredGraph(t, [][]string{
		{"source"},
		{"a", "source"},
		{"b", "source"},
		{"legal", "source"},
		{"indie"},
		{scopedReregisterBadMid, "a", "b", "indie"},
		{"target", "legal", scopedReregisterBadMid},
	})
}

// The exact JSON texts of the successful scoped exports once mid\xff no longer
// lies on a source -> target route. Both orderings are fully determined by the
// names: nodes in Go string order, edges upstream -> derived ordered by from
// then to.
const (
	// One-route graph after Register(mid\xff, [indie]): both clean parallel
	// routes survive; mid\xff and every edge touching it are gone.
	scopedEncodingRecoveredOneRouteGraph = `{"nodes":["clean","legal","source","target"],` +
		`"edges":[{"from":"clean","to":"target"},{"from":"legal","to":"target"},` +
		`{"from":"source","to":"clean"},{"from":"source","to":"legal"}]}`
	// Two-route graph after both source routes into mid\xff are severed: only
	// the legal route remains.
	scopedEncodingRecoveredTwoRouteGraph = `{"nodes":["legal","source","target"],` +
		`"edges":[{"from":"legal","to":"target"},{"from":"source","to":"legal"}]}`
)

// scopedEncodingFailure runs the source-scoped export and asserts the exact
// failure contract: an error explaining that the name cannot be exported
// losslessly, the offending name in Go-quoted form keeping the illegal byte as
// its own \xNN escape (never a U+FFFD replacement glyph), and an empty string
// — never a partial JSON document. It returns the error text so tests can pin
// that the same verdict is reported across replacement phases.
func scopedEncodingFailure(t *testing.T, graph map[string]*Lineage, source, target string) string {
	t.Helper()
	out, err := ExportSourceTargetLineage(graph, source, target)
	if err == nil {
		t.Fatalf("ExportSourceTargetLineage(%q, %q) with invalid node %q on the route succeeded: %s",
			source, target, scopedReregisterBadMid, out)
	}
	if out != "" {
		t.Errorf("failed scoped export returned partial content %q, want empty string", out)
	}
	if strings.Contains(out, "{") || strings.Contains(out, "nodes") {
		t.Errorf("failed scoped export leaked partial JSON %q", out)
	}
	if bytes.Contains([]byte(out), []byte{0xff}) {
		t.Errorf("failed scoped output must not carry the illegal byte: %q", out)
	}
	if !strings.Contains(err.Error(), "not valid UTF-8") {
		t.Errorf("error %q must explain the name cannot be exported losslessly", err)
	}
	quoted := strconv.Quote(scopedReregisterBadMid) // "mid\xff" — the escape, not the byte
	if !strings.Contains(err.Error(), quoted) {
		t.Errorf("error %q must preserve the bad byte in Go-quoted form %s", err, quoted)
	}
	if !strings.Contains(err.Error(), `\xff`) {
		t.Errorf("error %q must show the 0xff byte as \\xff", err)
	}
	if strings.ContainsRune(err.Error(), '�') {
		t.Errorf("error %q must not rewrite the bad byte into a U+FFFD replacement rune", err)
	}
	return err.Error()
}

// assertFullExportBlockedByMid verifies the full (unscoped) upstream export of
// target still fails on mid\xff: once mid\xff stays registered as a target
// upstream, the full closure keeps judging it even after the source-scoped
// export recovered.
func assertFullExportBlockedByMid(t *testing.T, graph map[string]*Lineage, target string) {
	t.Helper()
	out, err := ExportUpstreamLineage(graph, target)
	if err == nil {
		t.Fatalf("full upstream export of %q must still fail on %q, got %s",
			target, scopedReregisterBadMid, out)
	}
	if out != "" {
		t.Errorf("failed full export returned partial content %q, want empty string", out)
	}
	if !strings.Contains(err.Error(), "not valid UTF-8") ||
		!strings.Contains(err.Error(), strconv.Quote(scopedReregisterBadMid)) {
		t.Errorf("full export error %q must be the mid\\xff encoding error", err)
	}
}

// While mid\xff directly derives from source, it lies on an actual
// source -> target route even though two perfectly legal parallel routes
// (source -> legal -> target and source -> clean -> target) also exist. The
// scoped export must fail wholesale: the clean routes cannot make the encoder
// ignore the invalid branch, the error keeps the illegal byte quoted, and no
// replacement character or partial JSON comes back.
func TestExportSourceTargetLineageBadUTF8RouteFailsDespiteCleanRoutes(t *testing.T) {
	graph := scopedBadMidEncodingGraph(t)
	assertConsistent(t, graph)

	// Both sides of the route condition really hold: source reaches mid\xff and
	// mid\xff is target's direct upstream; the two legal routes exist alongside.
	if !slices.Contains(impactNames(mustImpacts(t, graph, "source")), scopedReregisterBadMid) {
		t.Fatal("source must reach the invalid intermediate along the bad route")
	}
	if !slices.Contains(impactNames(mustImpacts(t, graph, "source")), "target") {
		t.Fatal("source must still reach target through the legal parallel routes")
	}
	if !slices.Contains(upstreamNames(mustUpstreams(t, graph, "target")), scopedReregisterBadMid) {
		t.Fatal("the invalid intermediate must be a registered upstream of target")
	}
	assertEntry(t, graph, scopedReregisterBadMid,
		[]string{"source", "indie"}, []string{"target"})

	// The export fails despite the legal routes, and repeating it gives the
	// same verdict while changing nothing in the graph.
	before := snapshotExportGraph(graph)
	first := scopedEncodingFailure(t, graph, "source", "target")
	again := scopedEncodingFailure(t, graph, "source", "target")
	if again != first {
		t.Errorf("repeated failure reported a different error:\nfirst:  %q\nsecond: %q",
			first, again)
	}
	assertGraphUnchanged(t, before, graph)

	// The full upstream export of target fails for the same reason: judging the
	// scoped set strictly is not an excuse to silently drop the node elsewhere.
	assertFullExportBlockedByMid(t, graph, "target")
	assertGraphUnchanged(t, before, graph)

	// The failed export neither removed the registration nor rewrote the name:
	// the raw byte is still a live graph key with both edge directions.
	if _, ok := graph[scopedReregisterBadMid]; !ok {
		t.Fatal("the invalid intermediate must stay registered after the failure")
	}
	assertEntry(t, graph, scopedReregisterBadMid,
		[]string{"source", "indie"}, []string{"target"})
	assertEntry(t, graph, "indie", nil, []string{scopedReregisterBadMid})
	assertEntry(t, graph, "target",
		[]string{"legal", "clean", scopedReregisterBadMid}, []string{"view"})
}

// A same-name re-registration replaces mid\xff's direct-upstream list and severs
// its dependency on source, while mid\xff keeps its own registration and its
// edge into target (fed by indie). The source-scoped export must then recover:
// only nodes and direct dependencies on the surviving source -> target routes
// appear, mid\xff and its edges are gone, names read back exactly as
// registered; but the full upstream export of target still fails because
// mid\xff remains its upstream through the other source branch.
func TestExportSourceTargetLineageSeveringBadRouteRecoversButFullExportStaysBlocked(t *testing.T) {
	graph := scopedBadMidEncodingGraph(t)
	assertConsistent(t, graph)

	// The pre-replacement scoped export is blocked by the bad route.
	phaseA := snapshotExportGraph(graph)
	scopedEncodingFailure(t, graph, "source", "target")
	assertGraphUnchanged(t, phaseA, graph)

	// Replace mid\xff's whole direct-upstream list: source is dropped, indie
	// kept. mid\xff's own existing downstream target is retained.
	mustRegister(t, graph, scopedReregisterBadMid, "indie")
	assertConsistent(t, graph)
	assertEntry(t, graph, scopedReregisterBadMid, []string{"indie"}, []string{"target"})
	assertEntry(t, graph, "source", nil, []string{"legal", "clean"})
	assertEntry(t, graph, "indie", nil, []string{scopedReregisterBadMid})
	assertEntry(t, graph, "target",
		[]string{"legal", "clean", scopedReregisterBadMid}, []string{"view"})
	assertEntry(t, graph, "legal", []string{"source"}, []string{"target"})
	assertEntry(t, graph, "clean", []string{"source"}, []string{"target"})
	assertEntry(t, graph, "view", []string{"target"}, nil)

	// The replaced node is still registered and STILL an upstream of target —
	// success must not be explained by the node having vanished — but source no
	// longer reaches it, so it has left the scoped route set.
	if len(graph) != 7 {
		t.Errorf("dataset count = %d, want 7 (the replaced node stays registered)", len(graph))
	}
	if _, ok := graph[scopedReregisterBadMid]; !ok {
		t.Fatal("the invalid intermediate must stay registered")
	}
	targetUpstreams := upstreamNames(mustUpstreams(t, graph, "target"))
	if !slices.Contains(targetUpstreams, scopedReregisterBadMid) {
		t.Errorf("mid\\xff must still be an upstream of target: %q", targetUpstreams)
	}
	if !slices.Contains(targetUpstreams, "indie") {
		t.Errorf("indie must now be an indirect upstream of target: %q", targetUpstreams)
	}
	sourceImpact := impactNames(mustImpacts(t, graph, "source"))
	if slices.Contains(sourceImpact, scopedReregisterBadMid) {
		t.Errorf("source must no longer reach mid\\xff: %q", sourceImpact)
	}
	if !slices.Contains(sourceImpact, "target") {
		t.Fatal("source must still reach target through the legal routes")
	}

	// The scoped export recovers with exactly the surviving routes.
	recovered := snapshotExportGraph(graph)
	out := mustExportScoped(t, graph, "source", "target")
	if out != scopedEncodingRecoveredOneRouteGraph {
		t.Errorf("recovered scoped export =\n%s\nwant:\n%s",
			out, scopedEncodingRecoveredOneRouteGraph)
	}
	doc := parseExport(t, out)
	wantNodes := []string{"clean", "legal", "source", "target"}
	if !reflect.DeepEqual(doc.Nodes, wantNodes) {
		t.Errorf("nodes = %v, want %v", doc.Nodes, wantNodes)
	}
	wantEdges := [][2]string{
		{"clean", "target"},
		{"legal", "target"},
		{"source", "clean"},
		{"source", "legal"},
	}
	if got := exportEdgeNames(doc); !reflect.DeepEqual(got, wantEdges) {
		t.Errorf("edges = %v, want %v", got, wantEdges)
	}

	// mid\xff, its other source indie and target's downstream view appear as
	// neither nodes nor edge endpoints; no illegal byte leaks into the document.
	for _, excluded := range []string{scopedReregisterBadMid, "indie", "view"} {
		for _, node := range doc.Nodes {
			if node == excluded {
				t.Errorf("nodes must not contain %q: %v", excluded, doc.Nodes)
			}
		}
		for _, e := range doc.Edges {
			if e.From == excluded || e.To == excluded {
				t.Errorf("edge %v touching %q must stay out of the scoped export", e, excluded)
			}
		}
	}
	if bytes.Contains([]byte(out), []byte{0xff}) {
		t.Errorf("illegal byte leaked into the successful export: %s", out)
	}
	// Names survive the JSON round trip verbatim: every read-back name is an
	// exact registered graph key, and edges run upstream -> derived.
	for _, node := range doc.Nodes {
		if _, ok := graph[node]; !ok {
			t.Errorf("exported node %q does not match a registered name exactly", node)
		}
	}
	for _, e := range doc.Edges {
		if !slices.Contains(graph[e.To].Parents, e.From) {
			t.Errorf("edge %q -> %q is not a stored upstream -> derived dependency", e.From, e.To)
		}
	}

	// The successful scoped export is read-only and deterministic.
	assertGraphUnchanged(t, recovered, graph)
	if again := mustExportScoped(t, graph, "source", "target"); again != out {
		t.Errorf("repeated recovered export differs:\nfirst:  %s\nsecond: %s", out, again)
	}

	// The full upstream export of target still fails on mid\xff: its
	// registration and its dependency on target were preserved, and that export
	// judges the whole closure.
	blocked := snapshotExportGraph(graph)
	assertFullExportBlockedByMid(t, graph, "target")
	assertGraphUnchanged(t, blocked, graph)

	// Existing registration is neither deleted nor restricted: re-attaching
	// mid\xff to source makes the scoped export fail again, and severing it once
	// more restores the exact successful document. The verdict tracks the
	// CURRENT source-to-target routes, not registration history.
	mustRegister(t, graph, scopedReregisterBadMid, "source", "indie")
	assertConsistent(t, graph)
	assertEntry(t, graph, scopedReregisterBadMid,
		[]string{"source", "indie"}, []string{"target"})
	assertEntry(t, graph, "source", nil, []string{"legal", "clean", scopedReregisterBadMid})
	reattached := snapshotExportGraph(graph)
	scopedEncodingFailure(t, graph, "source", "target")
	assertGraphUnchanged(t, reattached, graph)

	mustRegister(t, graph, scopedReregisterBadMid, "indie")
	assertConsistent(t, graph)
	if got := mustExportScoped(t, graph, "source", "target"); got != out {
		t.Errorf("re-severed export =\n%s\nwant:\n%s", got, out)
	}
	assertEntry(t, graph, scopedReregisterBadMid, []string{"indie"}, []string{"target"})
}

// When mid\xff depends on source through TWO different routes, a replacement
// that severs just one of them leaves the encoding failure in place: branch
// identity and list order cannot make one-route removal sufficient. Only once
// the LAST source route into mid\xff disappears may the node leave the scoped
// export; its registration and its edge to target remain, and the full export
// keeps failing.
func TestExportSourceTargetLineageTwoBadRoutesMustBothBeSevered(t *testing.T) {
	graph := scopedTwoBadRoutesEncodingGraph(t)
	assertConsistent(t, graph)

	// Phase A: both bad routes live.
	phaseA := snapshotExportGraph(graph)
	errA := scopedEncodingFailure(t, graph, "source", "target")
	assertEntry(t, graph, scopedReregisterBadMid,
		[]string{"a", "b", "indie"}, []string{"target"})
	assertEntry(t, graph, "a", []string{"source"}, []string{scopedReregisterBadMid})
	assertEntry(t, graph, "b", []string{"source"}, []string{scopedReregisterBadMid})
	assertGraphUnchanged(t, phaseA, graph)

	// Phase B: sever only the route through a. The route through b keeps
	// mid\xff on a source -> target route, so the very same failure holds.
	mustRegister(t, graph, scopedReregisterBadMid, "b", "indie")
	assertConsistent(t, graph)
	assertEntry(t, graph, scopedReregisterBadMid,
		[]string{"b", "indie"}, []string{"target"})
	assertEntry(t, graph, "a", []string{"source"}, nil)
	assertEntry(t, graph, "b", []string{"source"}, []string{scopedReregisterBadMid})
	assertEntry(t, graph, "source", nil, []string{"a", "b", "legal"})
	if !slices.Contains(impactNames(mustImpacts(t, graph, "source")), scopedReregisterBadMid) {
		t.Fatal("source must still reach mid\xff through b")
	}
	phaseB := snapshotExportGraph(graph)
	if errB := scopedEncodingFailure(t, graph, "source", "target"); errB != errA {
		t.Errorf("severing one of two bad routes changed the error:\nbefore: %q\nafter:  %q",
			errA, errB)
	}
	assertGraphUnchanged(t, phaseB, graph)

	// Severing the symmetric route instead (keeping a) is equally insufficient:
	// the surviving route still carries the invalid node.
	other := scopedTwoBadRoutesEncodingGraph(t)
	mustRegister(t, other, scopedReregisterBadMid, "a", "indie")
	assertConsistent(t, other)
	assertEntry(t, other, "a", []string{"source"}, []string{scopedReregisterBadMid})
	assertEntry(t, other, "b", []string{"source"}, nil)
	scopedEncodingFailure(t, other, "source", "target")

	// Phase C: sever the LAST route into mid\xff (through b as well). It leaves
	// the scoped document while staying registered and feeding target.
	mustRegister(t, graph, scopedReregisterBadMid, "indie")
	assertConsistent(t, graph)
	assertEntry(t, graph, scopedReregisterBadMid, []string{"indie"}, []string{"target"})
	assertEntry(t, graph, "b", []string{"source"}, nil)
	assertEntry(t, graph, "a", []string{"source"}, nil)
	assertEntry(t, graph, "source", nil, []string{"a", "b", "legal"})
	if slices.Contains(impactNames(mustImpacts(t, graph, "source")), scopedReregisterBadMid) {
		t.Fatal("mid\\xff must leave source's downstream closure once both routes are severed")
	}
	if !slices.Contains(upstreamNames(mustUpstreams(t, graph, "target")), scopedReregisterBadMid) {
		t.Fatal("mid\\xff must remain an upstream of target")
	}

	phaseC := snapshotExportGraph(graph)
	out := mustExportScoped(t, graph, "source", "target")
	if out != scopedEncodingRecoveredTwoRouteGraph {
		t.Errorf("recovered scoped export =\n%s\nwant:\n%s",
			out, scopedEncodingRecoveredTwoRouteGraph)
	}
	doc := parseExport(t, out)
	if !reflect.DeepEqual(doc.Nodes, []string{"legal", "source", "target"}) {
		t.Errorf("nodes = %v, want only the surviving legal route", doc.Nodes)
	}
	if got := exportEdgeNames(doc); !reflect.DeepEqual(got, [][2]string{
		{"legal", "target"},
		{"source", "legal"},
	}) {
		t.Errorf("edges = %v, want only the surviving legal route", got)
	}
	for _, excluded := range []string{"a", "b", "indie", scopedReregisterBadMid} {
		if slices.Contains(doc.Nodes, excluded) {
			t.Errorf("node %q that left the routes must not be exported: %v",
				excluded, doc.Nodes)
		}
	}
	if bytes.Contains([]byte(out), []byte{0xff}) {
		t.Errorf("illegal byte leaked into the recovered export: %s", out)
	}
	// The node count is unchanged from the fixture: recovery deleted nothing.
	if len(graph) != 7 {
		t.Errorf("dataset count = %d, want 7 (no registration removed)", len(graph))
	}
	// The full upstream export of target still fails over mid\xff, and neither
	// the failed nor the successful scoped export moved anything in the graph.
	assertFullExportBlockedByMid(t, graph, "target")
	assertGraphUnchanged(t, phaseC, graph)
}
