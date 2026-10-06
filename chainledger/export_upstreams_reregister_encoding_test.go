package chainledger

import (
	"bytes"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"testing"
)

// reregisterBadSource is a registered source whose name carries a REAL lone
// 0xff byte — invalid UTF-8 — not the four printable characters backslash, x,
// f, f. Such a name still registers; only an export of a closure that contains
// it must reject the document.
const reregisterBadSource = "source\xff"

// exportReregisterEncodingScenario builds the multi-branch lineage for the
// replacement regression:
//
//	source\xff ──> alpha ──┐
//	source\xff ──> beta  ──┼──> merge ──> target
//	clean ────────────────┘
//
// The invalid source participates in merge's derivation through TWO different
// intermediate datasets (alpha and beta), and merge also depends directly on
// the legal source clean. target depends solely on merge. The bad source's
// name is the only invalid name in target's closure.
func exportReregisterEncodingScenario(t *testing.T) map[string]*Lineage {
	t.Helper()
	// Self-check the fixture: 0xff must be an actual byte of the name rather
	// than literal "\xff" text, otherwise this regression would test nothing.
	if !bytes.Contains([]byte(reregisterBadSource), []byte{0xff}) ||
		strings.Contains(reregisterBadSource, `\xff`) {
		t.Fatalf("fixture constant must hold a real 0xff byte, got %q", reregisterBadSource)
	}
	return buildRegisteredGraph(t, [][]string{
		{reregisterBadSource},
		{"clean"},
		{"alpha", reregisterBadSource}, // first branch through which the bad source derives merge
		{"beta", reregisterBadSource},  // second, independent branch
		{"merge", "alpha", "beta", "clean"},
		{"target", "merge"},
	})
}

// reregisterEncodingError is the one encoding failure every blocked phase must
// report: the offending name in Go-quoted form, with the bad byte kept as its
// own \xNN escape.
func reregisterEncodingError(t *testing.T, graph map[string]*Lineage, target string) string {
	t.Helper()
	out, err := ExportUpstreamLineage(graph, target)
	if err == nil {
		t.Fatalf("ExportUpstreamLineage(%q) with invalid source %q in its lineage succeeded: %s",
			target, reregisterBadSource, out)
	}
	if out != "" {
		t.Errorf("ExportUpstreamLineage(%q) returned partial content %q, want empty string", target, out)
	}
	if !strings.Contains(err.Error(), "not valid UTF-8") {
		t.Errorf("ExportUpstreamLineage(%q) error %q must be the encoding error", target, err)
	}
	quoted := strconv.Quote(reregisterBadSource) // "source\xff" — the escape, not the byte
	if !strings.Contains(err.Error(), quoted) {
		t.Errorf("ExportUpstreamLineage(%q) error %q must preserve the bad byte in Go-quoted form %s",
			target, err, quoted)
	}
	if !strings.Contains(err.Error(), `\xff`) {
		t.Errorf("ExportUpstreamLineage(%q) error %q must show the 0xff byte as \\xff", target, err)
	}
	return err.Error()
}

// Full replacement regression around same-name Register calls on the merge
// dataset while one of its ancestor sources carries an invalid-UTF-8 name.
//
// The export verdict must be derived solely from the sources target CURRENTLY
// depends on — every surviving branch counts, and neither a branch removed from
// the direct-upstream list nor the history of having participated can change
// the answer. The bad name blocks while either route survives; only when the
// last bad route is severed does export succeed. Exited sources and
// intermediates stay registered throughout — no delete or rename is used to
// repair.
func TestExportUpstreamLineageReregisterReplaceUpstreamsEncoding(t *testing.T) {
	graph := exportReregisterEncodingScenario(t)
	assertConsistent(t, graph)

	// The invalid name registered normally and answers normally from the
	// registry-side queries, raw byte and all; encoding is a rule of the
	// export alone.
	if _, ok := graph[reregisterBadSource]; !ok {
		t.Fatal("source with the 0xff byte must be registered")
	}
	if got, want := upstreamNames(mustUpstreams(t, graph, "target")),
		[]string{"merge", "alpha", "beta", "clean", reregisterBadSource}; !reflect.DeepEqual(got, want) {
		t.Fatalf("upstreams before replacement = %q, want %q", got, want)
	}

	// Phase A: both bad branches live. Exporting target (indirect dependency)
	// and merge (direct dependency through alpha/beta) fails wholesale: empty
	// string, no partial JSON, the bad byte shown escaped.
	phaseA := snapshotExportGraph(graph)
	errA := reregisterEncodingError(t, graph, "target")
	reregisterEncodingError(t, graph, "merge")
	assertGraphUnchanged(t, phaseA, graph)
	assertEntry(t, graph, "merge", []string{"alpha", "beta", "clean"}, []string{"target"})
	assertEntry(t, graph, reregisterBadSource, nil, []string{"alpha", "beta"})

	// Phase B: replace merge's direct upstreams wholesale, severing only the
	// route through alpha. The beta route and the legal clean source remain.
	mustRegister(t, graph, "merge", "beta", "clean")
	assertConsistent(t, graph)
	assertEntry(t, graph, "merge", []string{"beta", "clean"}, []string{"target"})
	// alpha detached from merge but still registered and still derived from
	// the bad source; the reverse edge into merge is gone from alpha, while
	// beta kept exactly its slot.
	assertEntry(t, graph, "alpha", []string{reregisterBadSource}, nil)
	assertEntry(t, graph, "beta", []string{reregisterBadSource}, []string{"merge"})
	assertEntry(t, graph, "clean", nil, []string{"merge"})
	assertEntry(t, graph, reregisterBadSource, nil, []string{"alpha", "beta"})

	// target still reaches the bad name indirectly along merge -> beta. One
	// fewer dependency edge must not repair the export; the failure is the
	// very same encoding error, and nothing about the registry moves.
	if got, want := upstreamNames(mustUpstreams(t, graph, "target")),
		[]string{"merge", "beta", "clean", reregisterBadSource}; !reflect.DeepEqual(got, want) {
		t.Fatalf("upstreams after severing one branch = %q, want %q", got, want)
	}
	phaseB := snapshotExportGraph(graph)
	errB := reregisterEncodingError(t, graph, "target")
	if errB != errA {
		t.Errorf("severing one bad route changed the encoding error:\nbefore: %q\nafter:  %q", errA, errB)
	}
	assertGraphUnchanged(t, phaseB, graph)

	// A replacement that names an unregistered upstream is rejected naming
	// that upstream, and validation happens before any edge is touched: merge
	// keeps its phase-B parents and children in order, and every neighbor list
	// is untouched.
	err := Register(graph, Dataset{Name: "merge"}, []string{"clean", "ghost"})
	if err == nil || !strings.Contains(err.Error(), "unknown parent") ||
		!strings.Contains(err.Error(), "ghost") {
		t.Fatalf("want unknown-parent error naming ghost, got %v", err)
	}
	assertGraphUnchanged(t, phaseB, graph)
	assertEntry(t, graph, "merge", []string{"beta", "clean"}, []string{"target"})
	assertEntry(t, graph, "beta", []string{reregisterBadSource}, []string{"merge"})
	assertEntry(t, graph, "alpha", []string{reregisterBadSource}, nil)
	assertConsistent(t, graph)

	// With the rejected replacement leaving the surviving beta route in place,
	// the export still reports the original encoding problem, not success and
	// not the registration error.
	errC := reregisterEncodingError(t, graph, "target")
	if errC != errA {
		t.Errorf("export after a rejected replacement reported a different error:\nwant: %q\ngot:  %q",
			errA, errC)
	}
	assertGraphUnchanged(t, phaseB, graph)

	// Phase C: replace merge's direct upstreams with the legal source alone.
	// The LAST route through the invalid source is severed; beta drops its
	// reverse edge to merge.
	mustRegister(t, graph, "merge", "clean")
	assertConsistent(t, graph)
	assertEntry(t, graph, "merge", []string{"clean"}, []string{"target"})
	assertEntry(t, graph, "clean", nil, []string{"merge"})
	assertEntry(t, graph, "beta", []string{reregisterBadSource}, nil)
	// The bad source and both intermediates remain registered, still linked to
	// one another outside target's lineage. Repair came from replacing the
	// direct-upstream list, never from deleting or renaming anything.
	assertEntry(t, graph, "alpha", []string{reregisterBadSource}, nil)
	assertEntry(t, graph, reregisterBadSource, nil, []string{"alpha", "beta"})
	if len(graph) != 6 {
		t.Errorf("dataset count = %d, want 6 (exited lineage nodes stay registered)", len(graph))
	}

	// target now depends only on merge and clean: the historical participation
	// of source\xff through either branch is no longer a basis for the verdict.
	if got, want := upstreamNames(mustUpstreams(t, graph, "target")),
		[]string{"merge", "clean"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("upstreams after severing the last branch = %q, want %q", got, want)
	}
	phaseC := snapshotExportGraph(graph)
	out := mustExport(t, graph, "target")
	wantText := `{"nodes":["clean","merge","target"],` +
		`"edges":[{"from":"clean","to":"merge"},{"from":"merge","to":"target"}]}`
	if out != wantText {
		t.Errorf("export after severing the last bad route =\n%s\nwant:\n%s", out, wantText)
	}
	doc := parseExport(t, out)
	if !reflect.DeepEqual(doc.Nodes, []string{"clean", "merge", "target"}) {
		t.Errorf("nodes = %q, want only target, merge and the surviving legal source", doc.Nodes)
	}
	if got := exportEdgeNames(doc); !reflect.DeepEqual(got, [][2]string{
		{"clean", "merge"},
		{"merge", "target"},
	}) {
		t.Errorf("edges = %q, want only the surviving direct dependencies", got)
	}
	if bytes.Contains([]byte(out), []byte{0xff}) {
		t.Errorf("illegal byte leaked into the successful export: %s", out)
	}
	for _, excluded := range []string{"alpha", "beta", reregisterBadSource} {
		if slices.Contains(doc.Nodes, excluded) {
			t.Errorf("node %q that left target's lineage must not be exported: %q", excluded, doc.Nodes)
		}
	}
	// Same lineage relationships, had they always been clean, produce
	// byte-identical output: the removed routes leave no trace beyond their
	// surviving registrations elsewhere in the graph.
	twin := buildRegisteredGraph(t, [][]string{
		{"clean"},
		{"merge", "clean"},
		{"target", "merge"},
	})
	if twinOut := mustExport(t, twin, "target"); twinOut != out {
		t.Errorf("repaired export differs from an always-clean twin:\nrepaired: %s\ntwin:     %s",
			out, twinOut)
	}
	// A successful export is read-only too: every registration and list order
	// stays exactly as after the replacement.
	assertGraphUnchanged(t, phaseC, graph)
}

// Severing EITHER one of the two bad branches alone leaves the other route and
// therefore leaves the encoding failure in place; branch identity, replacement
// list order and the legal source's presence cannot make one-route removal
// sufficient. Both routes must be gone before target exports.
func TestExportUpstreamLineageReregisterOneBranchRemovalNeverSuffices(t *testing.T) {
	cases := []struct {
		name        string
		keepParents []string // merge's replacement direct upstreams, one bad branch kept
	}{
		{"keep beta route", []string{"beta", "clean"}},
		{"keep alpha route", []string{"clean", "alpha"}}, // legal source listed first this time
		{"keep alpha only", []string{"alpha"}},           // legal source dropped as well
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			graph := exportReregisterEncodingScenario(t)
			errBefore := reregisterEncodingError(t, graph, "target")

			mustRegister(t, graph, "merge", tc.keepParents...)
			assertConsistent(t, graph)
			// The bad source still feeds a kept intermediate, which still feeds
			// merge: target's current closure contains the invalid name.
			if !slices.Contains(upstreamNames(mustUpstreams(t, graph, "target")), reregisterBadSource) {
				t.Fatalf("bad source must still be an upstream of target with parents %v", tc.keepParents)
			}
			errAfter := reregisterEncodingError(t, graph, "target")
			if errAfter != errBefore {
				t.Errorf("encoding error changed after partial branch removal:\nbefore: %q\nafter:  %q",
					errBefore, errAfter)
			}

			// Removing the final kept route repairs the export without touching
			// any registration; clean alone has a valid name.
			mustRegister(t, graph, "merge", "clean")
			if slices.Contains(upstreamNames(mustUpstreams(t, graph, "target")), reregisterBadSource) {
				t.Fatal("bad source must stop being an upstream once merge keeps only clean")
			}
			repaired := snapshotExportGraph(graph)
			out := mustExport(t, graph, "target")
			if want := `{"nodes":["clean","merge","target"],` +
				`"edges":[{"from":"clean","to":"merge"},{"from":"merge","to":"target"}]}`; out != want {
				t.Errorf("export =\n%s\nwant:\n%s", out, want)
			}
			if bytes.Contains([]byte(out), []byte{0xff}) {
				t.Errorf("illegal byte leaked into the export: %s", out)
			}
			// The intermediates and bad source detached by the replacements are
			// still registered, still mutually linked.
			assertEntry(t, graph, reregisterBadSource, nil, []string{"alpha", "beta"})
			assertEntry(t, graph, "alpha", []string{reregisterBadSource}, nil)
			assertEntry(t, graph, "beta", []string{reregisterBadSource}, nil)
			// The successful export was read-only over the repaired registry.
			assertGraphUnchanged(t, repaired, graph)
		})
	}
}
