package chainledger

import (
	"bytes"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"testing"
)

// scopedEncodingBadMid is a registered intermediate dataset whose name carries
// a REAL lone 0xff byte — invalid UTF-8 — not the four printable characters
// backslash, x, f, f. Such a name registers and stays queryable; only an export
// whose selected node set contains it must reject the document.
const scopedEncodingBadMid = "joined\xff"

// scopedEncodingReregisterScenario builds the regression lineage for judging a
// source-scoped export strictly over the source -> target routes that CURRENTLY
// exist after same-name Register replacements:
//
//	source ──> cleanway ───────────────────────> target   (legal route)
//	source ──> joined\xff ────────────────────> target   (direct bad route)
//	source ──> fork ──> side ──> joined\xff ──> target   (indirect bad route)
//	other ───────────────────────────────────> target   (independent source)
//
// The bad intermediate sits on TWO different routes from source and is a direct
// upstream of target the whole time. target also keeps the independent source
// other, and source reaches target through cleanway on a route that never
// touches the bad intermediate. Re-registering joined\xff under the same name
// replaces only its direct-upstream list; the node itself and its
// joined\xff -> target edge are never removed.
func scopedEncodingReregisterScenario(t *testing.T) map[string]*Lineage {
	t.Helper()
	// Self-check the fixture: 0xff must be an actual name byte rather than
	// literal "\xff" text, otherwise this regression would test nothing.
	if !bytes.Contains([]byte(scopedEncodingBadMid), []byte{0xff}) ||
		strings.Contains(scopedEncodingBadMid, `\xff`) {
		t.Fatalf("fixture constant must hold a real 0xff byte, got %q", scopedEncodingBadMid)
	}
	return buildRegisteredGraph(t, [][]string{
		{"source"},
		{"other"},
		{"cleanway", "source"},
		{"fork", "source"},
		{"side", "fork"},
		{scopedEncodingBadMid, "source", "side"},
		{"target", "cleanway", scopedEncodingBadMid, "other"},
	})
}

// scopedEncodingRepairedText is the scoped source -> target document once the
// bad intermediate no longer depends on source: exactly the surviving legal
// route, with nodes in Go string order and edges written upstream -> derived,
// ordered by from then to.
const scopedEncodingRepairedText = `{"nodes":["cleanway","source","target"],` +
	`"edges":[{"from":"cleanway","to":"target"},{"from":"source","to":"cleanway"}]}`

// assertEncodingExportError checks one failed lineage export against the single
// encoding contract: an empty string (never partial JSON, never a replacement
// glyph), and an error that states the name cannot be exported losslessly and
// quotes the offending bytes in Go form, so the real 0xff survives as its own
// \xff escape. It returns the error text for cross-phase equality checks.
func assertEncodingExportError(t *testing.T, phase, out string, err error) string {
	t.Helper()
	if err == nil {
		t.Fatalf("%s: export succeeded with %q, want an invalid-UTF-8 error", phase, out)
	}
	if out != "" {
		t.Errorf("%s: failed export returned partial content %q, want empty string", phase, out)
	}
	msg := err.Error()
	if !strings.Contains(msg, "not valid UTF-8") || !strings.Contains(msg, "cannot be exported losslessly") {
		t.Errorf("%s: error %q must state the name cannot be exported losslessly", phase, msg)
	}
	quoted := strconv.Quote(scopedEncodingBadMid) // "joined\xff" — the escape, not the byte
	if !strings.Contains(msg, quoted) {
		t.Errorf("%s: error %q must quote the offending name as %s", phase, msg, quoted)
	}
	if !strings.Contains(msg, `\xff`) {
		t.Errorf("%s: error %q must render byte 0xff as its own \\xff escape", phase, msg)
	}
	if bytes.Contains([]byte(msg), []byte{0xff}) {
		t.Errorf("%s: error %q must not carry the raw 0xff byte", phase, msg)
	}
	if strings.ContainsRune(msg, '�') {
		t.Errorf("%s: error %q must not use a replacement character", phase, msg)
	}
	return msg
}

// expectScopedEncodingFailure runs one source-scoped export and checks it
// against the shared encoding contract.
func expectScopedEncodingFailure(t *testing.T, phase string, graph map[string]*Lineage, source, target string) string {
	t.Helper()
	out, err := ExportSourceTargetLineage(graph, source, target)
	return assertEncodingExportError(t, phase, out, err)
}

// expectFullEncodingFailure runs one full upstream export and checks it
// against the shared encoding contract.
func expectFullEncodingFailure(t *testing.T, phase string, graph map[string]*Lineage, target string) string {
	t.Helper()
	out, err := ExportUpstreamLineage(graph, target)
	return assertEncodingExportError(t, phase, out, err)
}

// upstreamPathTo returns the committed explanation path from upstream to target
// when upstream is among target's reported upstreams, failing the test if not.
func upstreamPathTo(t *testing.T, graph map[string]*Lineage, upstream, target string) []string {
	t.Helper()
	for _, hit := range mustUpstreams(t, graph, target) {
		if hit.Dataset == upstream {
			return hit.Path
		}
	}
	t.Fatalf("%q is not among the upstreams of %q", upstream, target)
	return nil
}

// Full replacement regression: while the bad intermediate depends on the
// specified source through ANY route, ExportSourceTargetLineage(source, target)
// must fail wholesale even though source reaches target over an entirely legal
// parallel route and even though target keeps another, independent source. The
// verdict follows the CURRENT source -> target routes rather than the whole
// target ancestry or the replaced node's mere registration: after the last
// source -> joined\xff route is severed by a same-name Register replacement,
// the scoped export succeeds with exactly the surviving legal route, while the
// intermediate stays registered, keeps feeding target, and the full upstream
// export still fails on its name.
func TestExportSourceTargetLineageReregisterRepairsEncodingByReplacingRoute(t *testing.T) {
	graph := scopedEncodingReregisterScenario(t)
	assertConsistent(t, graph)

	// The invalid name registered normally and answers normally from the
	// registry-side queries, raw byte and all; encoding is a rule of the
	// exports alone.
	if _, ok := graph[scopedEncodingBadMid]; !ok {
		t.Fatal("intermediate with the 0xff byte must be registered")
	}

	// Phase A: both source -> joined\xff routes live. Registry-side evidence of
	// the layout this regression protects:
	wantUpstreams := []string{"cleanway", scopedEncodingBadMid, "other", "side", "source", "fork"}
	if got := upstreamNames(mustUpstreams(t, graph, "target")); !reflect.DeepEqual(got, wantUpstreams) {
		t.Fatalf("upstreams of target = %q, want %q", got, wantUpstreams)
	}
	// Source already reaches target over the fully legal cleanway route (its
	// shortest tie-broken path), so a failure below cannot be blamed on the
	// legal route being absent.
	if got, want := upstreamPathTo(t, graph, "source", "target"),
		[]string{"source", "cleanway", "target"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("source -> target path = %q, want the legal route %q", got, want)
	}

	// The scoped export fails because joined\xff lies on a source -> target
	// route: empty string, no partial JSON, bad byte shown escaped. The legal
	// parallel route must not make the export ignore that branch.
	phaseA := snapshotExportGraph(graph)
	scopedErr := expectScopedEncodingFailure(t, "phase A scoped", graph, "source", "target")
	// The full upstream export fails for the very same single reason; the two
	// entry points share one encoding rule and one offending name.
	fullErr := expectFullEncodingFailure(t, "phase A full", graph, "target")
	if scopedErr != fullErr {
		t.Errorf("scoped and full export errors differ:\nscoped: %q\nfull:   %q", scopedErr, fullErr)
	}
	assertGraphUnchanged(t, phaseA, graph)
	assertEntry(t, graph, "source", nil, []string{"cleanway", "fork", scopedEncodingBadMid})
	assertEntry(t, graph, "cleanway", []string{"source"}, []string{"target"})
	assertEntry(t, graph, "fork", []string{"source"}, []string{"side"})
	assertEntry(t, graph, "side", []string{"fork"}, []string{scopedEncodingBadMid})
	assertEntry(t, graph, scopedEncodingBadMid, []string{"source", "side"}, []string{"target"})
	assertEntry(t, graph, "other", nil, []string{"target"})
	assertEntry(t, graph, "target", []string{"cleanway", scopedEncodingBadMid, "other"}, nil)

	// Phase B: replace joined\xff's direct-upstream list with side alone. Only
	// the direct source -> joined\xff edge is severed; the indirect
	// source -> fork -> side -> joined\xff route and the legal cleanway route
	// both remain.
	mustRegister(t, graph, scopedEncodingBadMid, "side")
	assertConsistent(t, graph)
	assertEntry(t, graph, scopedEncodingBadMid, []string{"side"}, []string{"target"})
	assertEntry(t, graph, "source", nil, []string{"cleanway", "fork"})
	assertEntry(t, graph, "side", []string{"fork"}, []string{scopedEncodingBadMid})
	if !slices.Contains(upstreamNames(mustUpstreams(t, graph, scopedEncodingBadMid)), "source") {
		t.Fatal("joined\xff must still depend on source through fork -> side after one route is severed")
	}
	// target's own upstream list never moved: the replaced node kept its slot.
	assertEntry(t, graph, "target", []string{"cleanway", scopedEncodingBadMid, "other"}, nil)

	// One fewer route must not repair the scoped export: the bad intermediate
	// is still both a descendant of source and an ancestor of target, so it
	// stays selected. The error is byte-identical to phase A, and neither
	// export changes the graph.
	phaseB := snapshotExportGraph(graph)
	errB := expectScopedEncodingFailure(t, "phase B scoped", graph, "source", "target")
	if errB != scopedErr {
		t.Errorf("severing one bad route changed the scoped encoding error:\nbefore: %q\nafter:  %q",
			scopedErr, errB)
	}
	expectFullEncodingFailure(t, "phase B full", graph, "target")
	assertGraphUnchanged(t, phaseB, graph)

	// A replacement naming an unregistered upstream is rejected before any edge
	// moves, so the surviving indirect route and the scoped verdict are
	// untouched.
	err := Register(graph, Dataset{Name: scopedEncodingBadMid}, []string{"side", "ghost"})
	if err == nil || !strings.Contains(err.Error(), "unknown parent") ||
		!strings.Contains(err.Error(), "ghost") {
		t.Fatalf("want unknown-parent error naming ghost, got %v", err)
	}
	assertGraphUnchanged(t, phaseB, graph)
	assertEntry(t, graph, scopedEncodingBadMid, []string{"side"}, []string{"target"})
	assertEntry(t, graph, "side", []string{"fork"}, []string{scopedEncodingBadMid})
	assertConsistent(t, graph)
	if errRejected := expectScopedEncodingFailure(t, "after rejected replacement", graph, "source", "target"); errRejected != scopedErr {
		t.Errorf("scoped export after a rejected replacement reported a different error:\nwant: %q\ngot:  %q",
			scopedErr, errRejected)
	}
	assertGraphUnchanged(t, phaseB, graph)

	// Phase C: the last source -> joined\xff route is severed by re-registering
	// the intermediate under its same name with the independent source other.
	// Nothing is unregistered or renamed: joined\xff keeps its edge into
	// target, and target keeps every direct upstream in its stored order.
	mustRegister(t, graph, scopedEncodingBadMid, "other")
	assertConsistent(t, graph)
	if got, want := len(graph), 7; got != want {
		t.Fatalf("dataset count = %d, want %d (the replaced node stays registered)", got, want)
	}
	assertEntry(t, graph, scopedEncodingBadMid, []string{"other"}, []string{"target"})
	assertEntry(t, graph, "other", nil, []string{"target", scopedEncodingBadMid})
	assertEntry(t, graph, "side", []string{"fork"}, nil)
	assertEntry(t, graph, "fork", []string{"source"}, []string{"side"})
	assertEntry(t, graph, "source", nil, []string{"cleanway", "fork"})
	assertEntry(t, graph, "cleanway", []string{"source"}, []string{"target"})
	assertEntry(t, graph, "target", []string{"cleanway", scopedEncodingBadMid, "other"}, nil)
	if entry := graph[scopedEncodingBadMid]; entry.Dataset != scopedEncodingBadMid {
		t.Fatalf("the bad name was rewritten: stored Dataset = %q", entry.Dataset)
	}
	if slices.Contains(upstreamNames(mustUpstreams(t, graph, scopedEncodingBadMid)), "source") {
		t.Fatal("joined\xff must no longer depend on source through any route")
	}
	if !slices.Contains(upstreamNames(mustUpstreams(t, graph, "target")), scopedEncodingBadMid) {
		t.Fatal("joined\xff must remain an upstream of target")
	}

	// The scoped export recovers and carries ONLY the surviving legal route:
	// joined\xff, the edges touching it, other and other -> target, and the
	// detached fork/side branch are all absent.
	phaseC := snapshotExportGraph(graph)
	out := mustExportScoped(t, graph, "source", "target")
	if out != scopedEncodingRepairedText {
		t.Errorf("repaired scoped export =\n%s\nwant:\n%s", out, scopedEncodingRepairedText)
	}
	doc := parseExport(t, out)
	if !reflect.DeepEqual(doc.Nodes, []string{"cleanway", "source", "target"}) {
		t.Errorf("nodes = %q, want only the surviving legal route", doc.Nodes)
	}
	if got := exportEdgeNames(doc); !reflect.DeepEqual(got, [][2]string{
		{"cleanway", "target"},
		{"source", "cleanway"},
	}) {
		t.Errorf("edges = %q, want only the legal route's direct dependencies", got)
	}
	if bytes.Contains([]byte(out), []byte{0xff}) {
		t.Errorf("illegal byte leaked into the successful export: %s", out)
	}
	for _, excluded := range []string{scopedEncodingBadMid, "other", "side", "fork"} {
		if slices.Contains(doc.Nodes, excluded) {
			t.Errorf("node %q outside the surviving route must not be exported: %q", excluded, doc.Nodes)
		}
		for _, e := range doc.Edges {
			if e.From == excluded || e.To == excluded {
				t.Errorf("edge touching %q must leave the scoped document: %+v", excluded, e)
			}
		}
	}
	// Every decoded name is byte-identical to the still-registered value.
	for _, want := range []string{"cleanway", "source", "target"} {
		if !slices.Contains(doc.Nodes, want) {
			t.Errorf("registered name %q did not survive the JSON round trip: %q", want, doc.Nodes)
		}
	}
	// Same surviving relationships, had the bad branch never existed, produce
	// byte-identical scoped output: the exited routes leave no trace beyond
	// their surviving registrations elsewhere in the graph.
	twin := buildRegisteredGraph(t, [][]string{
		{"source"},
		{"cleanway", "source"},
		{"target", "cleanway"},
	})
	if twinOut := mustExportScoped(t, twin, "source", "target"); twinOut != out {
		t.Errorf("repaired export differs from an always-clean twin:\nrepaired: %s\ntwin:     %s",
			out, twinOut)
	}

	// The intermediate is still registered and still feeds target, so the FULL
	// upstream export must keep failing on its name: success of the scoped
	// export is not a verdict about target's whole ancestry.
	expectFullEncodingFailure(t, "phase C full", graph, "target")

	// The node exited only THIS source's scope: other -> target now runs through
	// joined\xff (other also reaches target directly), so that scoped export
	// still has the bad intermediate on a selected route and fails.
	expectScopedEncodingFailure(t, "other scoped", graph, "other", "target")

	// Both source and joined\xff remain registered, but no route connects them
	// anymore: the export succeeds with two empty arrays rather than failing on
	// either the bad name or a not-found check — mere registration cannot keep a
	// node in scope.
	if disconnected := mustExportScoped(t, graph, "source", scopedEncodingBadMid); disconnected != `{"nodes":[],"edges":[]}` {
		t.Errorf("source -> joined\xff export = %s, want two empty arrays", disconnected)
	}

	// Every export, successful or failed, left nodes, both edge directions and
	// stored list orders exactly as after the phase-C replacement.
	assertGraphUnchanged(t, phaseC, graph)
}

// Severing just ONE of the two routes by which joined\xff depends on source is
// never sufficient, whichever route is dropped first: the intermediate stays a
// descendant of source and the scoped encoding failure stands. Only after the
// last route disappears — in either severing order — does it leave the scoped
// document; both orders converge on the same repaired graph and byte-identical
// output while the full export still fails.
func TestExportSourceTargetLineageReregisterEncodingOneRouteRemovalNeverSuffices(t *testing.T) {
	cases := []struct {
		name        string
		keepParents []string // joined\xff's replacement parents, keeping exactly one source route
	}{
		{"keep indirect route", []string{"side"}},
		{"keep direct route", []string{"source"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			graph := scopedEncodingReregisterScenario(t)
			assertConsistent(t, graph)
			errBefore := expectScopedEncodingFailure(t, "before replacement", graph, "source", "target")

			mustRegister(t, graph, scopedEncodingBadMid, tc.keepParents...)
			assertConsistent(t, graph)
			if !slices.Contains(upstreamNames(mustUpstreams(t, graph, scopedEncodingBadMid)), "source") {
				t.Fatalf("joined\xff must still trace to source with parents %v", tc.keepParents)
			}
			before := snapshotExportGraph(graph)
			errAfter := expectScopedEncodingFailure(t, "one route removed", graph, "source", "target")
			if errAfter != errBefore {
				t.Errorf("encoding error changed after partial route removal:\nbefore: %q\nafter:  %q",
					errBefore, errAfter)
			}
			// The legal parallel route cannot rescue the export either.
			expectFullEncodingFailure(t, "full export one route removed", graph, "target")
			assertGraphUnchanged(t, before, graph)

			// Sever the last source route by attaching the intermediate to the
			// independent source.
			mustRegister(t, graph, scopedEncodingBadMid, "other")
			assertConsistent(t, graph)
			if slices.Contains(upstreamNames(mustUpstreams(t, graph, scopedEncodingBadMid)), "source") {
				t.Fatal("joined\xff must stop depending on source once its last route is severed")
			}
			repaired := snapshotExportGraph(graph)
			if out := mustExportScoped(t, graph, "source", "target"); out != scopedEncodingRepairedText {
				t.Errorf("repaired scoped export =\n%s\nwant:\n%s", out, scopedEncodingRepairedText)
			}
			doc := parseExport(t, mustExportScoped(t, graph, "source", "target"))
			if slices.Contains(doc.Nodes, scopedEncodingBadMid) {
				t.Errorf("joined\xff must leave the scoped document, got nodes %q", doc.Nodes)
			}
			// The detached branch and the independent source stay registered in
			// exactly the converged final shape, independent of severing order.
			assertEntry(t, graph, "source", nil, []string{"cleanway", "fork"})
			assertEntry(t, graph, "fork", []string{"source"}, []string{"side"})
			assertEntry(t, graph, "side", []string{"fork"}, nil)
			assertEntry(t, graph, scopedEncodingBadMid, []string{"other"}, []string{"target"})
			assertEntry(t, graph, "other", nil, []string{"target", scopedEncodingBadMid})
			assertEntry(t, graph, "target", []string{"cleanway", scopedEncodingBadMid, "other"}, nil)
			// The retained bad upstream keeps blocking the full export.
			expectFullEncodingFailure(t, "full export repaired", graph, "target")
			assertGraphUnchanged(t, repaired, graph)
		})
	}
}
