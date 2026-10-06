package chainledger

import (
	"reflect"
	"slices"
	"strconv"
	"strings"
	"testing"
)

// A source whose registered name carries a real lone 0xff byte — an actual
// invalid byte, not the four-character text "\xff". Registration accepts it;
// only exports of a closure containing it reject.
const reregisterUTF8BadSource = "badraw\xff"

const (
	reregisterUTF8BranchOne = "via-one"
	reregisterUTF8BranchTwo = "via-two"
	reregisterUTF8Clean     = "clean"
	reregisterUTF8CleanRoot = "cleanroot"
	reregisterUTF8Merge     = "merge"
	reregisterUTF8Target    = "target"
)

// exportReregisterUTF8Scenario builds the multi-branch lineage:
//
//	badraw\xff ---> via-one --\
//	  |                        >-- merge -- target
//	  \--------> via-two -----/        ^
//	cleanroot ---> clean -------------/
//
// The invalid-named source takes part in merge's derivation through TWO
// different intermediate datasets, while merge also depends directly on a
// wholly legal source (itself derived from cleanroot). target depends on
// merge. Every registration — the 0xff byte included — must succeed.
func exportReregisterUTF8Scenario(t *testing.T) map[string]*Lineage {
	t.Helper()
	return buildRegisteredGraph(t, [][]string{
		{reregisterUTF8BadSource},
		{reregisterUTF8BranchOne, reregisterUTF8BadSource},
		{reregisterUTF8BranchTwo, reregisterUTF8BadSource},
		{reregisterUTF8CleanRoot},
		{reregisterUTF8Clean, reregisterUTF8CleanRoot},
		{reregisterUTF8Merge, reregisterUTF8BranchOne, reregisterUTF8BranchTwo, reregisterUTF8Clean},
		{reregisterUTF8Target, reregisterUTF8Merge},
	})
}

// requireInvalidNameExportFailure exports target and requires the documented
// failure shape: empty string (never partial JSON), the invalid-encoding
// error, and the offending name shown byte for byte in Go-quoted escaped form
// (so the raw 0xff byte never appears literally in the message).
func requireInvalidNameExportFailure(t *testing.T, graph map[string]*Lineage, target, bad string) string {
	t.Helper()
	out, err := ExportUpstreamLineage(graph, target)
	if err == nil {
		t.Fatalf("ExportUpstreamLineage(%q) with invalid name %q in its current lineage succeeded: %s",
			target, bad, out)
	}
	if out != "" {
		t.Errorf("ExportUpstreamLineage(%q) returned partial content %q, want empty string", target, out)
	}
	if !strings.Contains(err.Error(), "not valid UTF-8") {
		t.Errorf("ExportUpstreamLineage(%q) error %q must report invalid name encoding", target, err)
	}
	quoted := strconv.Quote(bad)
	if !strings.Contains(err.Error(), quoted) {
		t.Errorf("ExportUpstreamLineage(%q) error %q must preserve the bad byte in escaped form %s",
			target, err, quoted)
	}
	if strings.Contains(err.Error(), "\xff") {
		t.Errorf("ExportUpstreamLineage(%q) error %q must escape 0xff, not emit the raw byte", target, err)
	}
	return err.Error()
}

// The full regression: replacing merge's whole direct-upstream list changes
// whether target currently depends on the invalid-named source, and that
// judgment is made over EVERY source the target still depends on — never over
// historical participation or any single explanation path. Dropping one of the
// two branches that carry the bad name keeps the export failing; only dropping
// the last branch lets the export succeed, and then it contains exactly the
// surviving derivation. Exited sources and intermediates stay registered; the
// fix must not rely on deletion or renaming.
func TestExportUpstreamLineageReregisterDiamondInvalidName(t *testing.T) {
	graph := exportReregisterUTF8Scenario(t)
	assertConsistent(t, graph)

	// The bad source reaches merge through both intermediates; the legal source
	// is the third direct upstream.
	assertEntry(t, graph, reregisterUTF8BadSource, nil,
		[]string{reregisterUTF8BranchOne, reregisterUTF8BranchTwo})
	assertEntry(t, graph, reregisterUTF8BranchOne, []string{reregisterUTF8BadSource},
		[]string{reregisterUTF8Merge})
	assertEntry(t, graph, reregisterUTF8BranchTwo, []string{reregisterUTF8BadSource},
		[]string{reregisterUTF8Merge})
	assertEntry(t, graph, reregisterUTF8Merge,
		[]string{reregisterUTF8BranchOne, reregisterUTF8BranchTwo, reregisterUTF8Clean},
		[]string{reregisterUTF8Target})
	assertEntry(t, graph, reregisterUTF8Clean, []string{reregisterUTF8CleanRoot},
		[]string{reregisterUTF8Merge})
	assertEntry(t, graph, reregisterUTF8Target, []string{reregisterUTF8Merge}, nil)

	// Both routes mean the bad source is genuinely a current dependency; the
	// export must fail with no document, however many legal nodes also appear.
	before := snapshotExportGraph(graph)
	firstErr := requireInvalidNameExportFailure(t, graph, reregisterUTF8Target, reregisterUTF8BadSource)
	assertGraphUnchanged(t, before, graph)
	if got := upstreamNames(mustUpstreams(t, graph, reregisterUTF8Target)); !slices.Contains(got, reregisterUTF8BadSource) {
		t.Fatalf("bad source must be a current upstream of target, got %q", got)
	}
	assertGraphUnchanged(t, before, graph)

	// Replace merge's direct upstreams, severing only the via-two route while
	// the via-one route and the legal source survive. A same-name Register is a
	// whole-list replacement: merge's own downstream target is untouched.
	mustRegister(t, graph, reregisterUTF8Merge, reregisterUTF8BranchOne, reregisterUTF8Clean)
	assertConsistent(t, graph)
	assertEntry(t, graph, reregisterUTF8Merge,
		[]string{reregisterUTF8BranchOne, reregisterUTF8Clean}, []string{reregisterUTF8Target})
	assertEntry(t, graph, reregisterUTF8Target, []string{reregisterUTF8Merge}, nil)
	// via-two left merge's derivation but kept its own registration and its
	// parent edge to the bad source; it merely lost the reverse edge from
	// merge. The bad source's own children list is untouched: both branches
	// still derive from it.
	assertEntry(t, graph, reregisterUTF8BranchTwo, []string{reregisterUTF8BadSource}, nil)
	assertEntry(t, graph, reregisterUTF8BadSource, nil,
		[]string{reregisterUTF8BranchOne, reregisterUTF8BranchTwo})

	// One bad route is gone, but the target STILL indirectly depends on the
	// invalid name through via-one. Losing one dependency edge must not turn the
	// export into a success or change the reported problem.
	phaseTwo := snapshotExportGraph(graph)
	secondErr := requireInvalidNameExportFailure(t, graph, reregisterUTF8Target, reregisterUTF8BadSource)
	if secondErr != firstErr {
		t.Errorf("encoding error changed after one branch was cut:\nbefore: %q\nafter:  %q",
			firstErr, secondErr)
	}
	if got := upstreamNames(mustUpstreams(t, graph, reregisterUTF8Target)); !slices.Contains(got, reregisterUTF8BadSource) {
		t.Fatalf("bad source must remain a current upstream through the surviving branch, got %q", got)
	}
	assertGraphUnchanged(t, phaseTwo, graph)

	// Replace merge's direct upstreams once more: the last bad route is cut and
	// only the legal source remains.
	mustRegister(t, graph, reregisterUTF8Merge, reregisterUTF8Clean)
	assertConsistent(t, graph)
	assertEntry(t, graph, reregisterUTF8Merge, []string{reregisterUTF8Clean}, []string{reregisterUTF8Target})
	assertEntry(t, graph, reregisterUTF8BranchOne, []string{reregisterUTF8BadSource}, nil)
	assertEntry(t, graph, reregisterUTF8BranchTwo, []string{reregisterUTF8BadSource}, nil)
	// The bad source still feeds both exited intermediates; neither they nor it
	// were deleted, renamed or detached.
	assertEntry(t, graph, reregisterUTF8BadSource, nil,
		[]string{reregisterUTF8BranchOne, reregisterUTF8BranchTwo})
	assertEntry(t, graph, reregisterUTF8Clean, []string{reregisterUTF8CleanRoot}, []string{reregisterUTF8Merge})

	// Nothing was deleted or renamed to earn the success: the bad source and
	// both intermediates keep their registrations and mutual edges outside
	// target's current closure.
	if got := len(graph); got != 7 {
		t.Fatalf("dataset count = %d, want 7; exited nodes must stay registered", got)
	}
	for _, name := range []string{reregisterUTF8BadSource, reregisterUTF8BranchOne, reregisterUTF8BranchTwo} {
		if _, ok := graph[name]; !ok {
			t.Errorf("exited dataset %q must stay registered after leaving target's lineage", name)
		}
	}

	// The export now succeeds and carries target, merge, the legal source and
	// its root with exactly their surviving direct dependencies.
	phaseThree := snapshotExportGraph(graph)
	out := mustExport(t, graph, reregisterUTF8Target)
	wantJSON := `{"nodes":["clean","cleanroot","merge","target"],` +
		`"edges":[{"from":"clean","to":"merge"},{"from":"cleanroot","to":"clean"},` +
		`{"from":"merge","to":"target"}]}`
	if out != wantJSON {
		t.Errorf("export after the last bad route was cut =\n%s\nwant:\n%s", out, wantJSON)
	}
	doc := parseExport(t, out)
	wantNodes := []string{
		reregisterUTF8Clean, reregisterUTF8CleanRoot, reregisterUTF8Merge, reregisterUTF8Target,
	}
	if !reflect.DeepEqual(doc.Nodes, wantNodes) {
		t.Errorf("nodes = %q, want %q", doc.Nodes, wantNodes)
	}
	wantEdges := [][2]string{
		{reregisterUTF8Clean, reregisterUTF8Merge},
		{reregisterUTF8CleanRoot, reregisterUTF8Clean},
		{reregisterUTF8Merge, reregisterUTF8Target},
	}
	if got := exportEdgeNames(doc); !reflect.DeepEqual(got, wantEdges) {
		t.Errorf("edges = %q, want %q", got, wantEdges)
	}

	// The bad source and both now-historical branches leave the document
	// entirely: no raw illegal byte, no node decoding back to them.
	if strings.Contains(out, "\xff") {
		t.Errorf("illegal 0xff byte leaked into the successful export: %s", out)
	}
	for _, name := range doc.Nodes {
		if name == reregisterUTF8BadSource {
			t.Errorf("exited invalid source decoded back from the export: %q", doc.Nodes)
		}
	}
	for _, excluded := range []string{reregisterUTF8BranchOne, reregisterUTF8BranchTwo} {
		if slices.Contains(doc.Nodes, excluded) {
			t.Errorf("historical intermediate %q must not appear in the current export: %q",
				excluded, doc.Nodes)
		}
	}

	// History is not evidence: the same current closure reached purely by
	// registrations (no bad source ever involved) exports byte-identical text.
	twin := buildRegisteredGraph(t, [][]string{
		{reregisterUTF8CleanRoot},
		{reregisterUTF8Clean, reregisterUTF8CleanRoot},
		{reregisterUTF8Merge, reregisterUTF8Clean},
		{reregisterUTF8Target, reregisterUTF8Merge},
	})
	if twinOut := mustExport(t, twin, reregisterUTF8Target); twinOut != out {
		t.Errorf("current-closure export depends on registration history:\nrepaired: %s\ntwin:     %s",
			out, twinOut)
	}

	// A successful export changes no registration, relationship or list order;
	// the exited subtree is still exactly as registered.
	assertGraphUnchanged(t, phaseThree, graph)
	assertEntry(t, graph, reregisterUTF8BadSource, nil,
		[]string{reregisterUTF8BranchOne, reregisterUTF8BranchTwo})
	assertEntry(t, graph, reregisterUTF8BranchOne, []string{reregisterUTF8BadSource}, nil)
	assertEntry(t, graph, reregisterUTF8BranchTwo, []string{reregisterUTF8BadSource}, nil)

	// And its own export still fails there: leaving target's closure never
	// repaired the name, and the registry query over the exited subtree still
	// reaches the bad source.
	requireInvalidNameExportFailure(t, graph, reregisterUTF8BranchOne, reregisterUTF8BadSource)
	assertGraphUnchanged(t, phaseThree, graph)
}

// While the invalid source still participates in the derivation, a replacement
// request that names an unregistered upstream must be rejected as a whole,
// naming that upstream, and leave the original direct dependencies and list
// orders untouched. The export afterwards reports the very same encoding
// failure — the rejected request cannot remove a bad route nor add one.
func TestExportUpstreamLineageFailedReregisterKeepsEncodingError(t *testing.T) {
	graph := exportReregisterUTF8Scenario(t)
	assertConsistent(t, graph)
	before := snapshotExportGraph(graph)
	originalErr := requireInvalidNameExportFailure(t, graph, reregisterUTF8Target, reregisterUTF8BadSource)

	// This request would sever the via-two route; the unknown name makes the
	// whole registration invalid before the graph is touched.
	err := Register(graph, Dataset{Name: reregisterUTF8Merge},
		[]string{reregisterUTF8BranchOne, reregisterUTF8Clean, "ghost"})
	if err == nil || !strings.Contains(err.Error(), "ghost") {
		t.Fatalf("want unknown-parent error naming ghost, got %v", err)
	}
	assertGraphUnchanged(t, before, graph)

	// Even a request that would cut BOTH bad routes is rejected wholesale for
	// the same reason; the legal sole-source future must not take effect.
	err = Register(graph, Dataset{Name: reregisterUTF8Merge},
		[]string{reregisterUTF8Clean, "ghost"})
	if err == nil || !strings.Contains(err.Error(), "ghost") {
		t.Fatalf("want unknown-parent error naming ghost, got %v", err)
	}
	assertGraphUnchanged(t, before, graph)

	// Every original relationship and ordering survives the rejected attempts.
	assertEntry(t, graph, reregisterUTF8Merge,
		[]string{reregisterUTF8BranchOne, reregisterUTF8BranchTwo, reregisterUTF8Clean},
		[]string{reregisterUTF8Target})
	assertEntry(t, graph, reregisterUTF8BadSource, nil,
		[]string{reregisterUTF8BranchOne, reregisterUTF8BranchTwo})
	assertEntry(t, graph, reregisterUTF8BranchOne, []string{reregisterUTF8BadSource},
		[]string{reregisterUTF8Merge})
	assertEntry(t, graph, reregisterUTF8BranchTwo, []string{reregisterUTF8BadSource},
		[]string{reregisterUTF8Merge})
	assertConsistent(t, graph)

	// The export still fails with the original encoding error and empty output —
	// not a success, a different error, or a partial document.
	afterErr := requireInvalidNameExportFailure(t, graph, reregisterUTF8Target, reregisterUTF8BadSource)
	if afterErr != originalErr {
		t.Errorf("encoding error changed after rejected re-registrations:\nbefore: %q\nafter:  %q",
			originalErr, afterErr)
	}
	assertGraphUnchanged(t, before, graph)
}
