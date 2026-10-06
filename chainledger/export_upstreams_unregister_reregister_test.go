package chainledger

import (
	"reflect"
	"strings"
	"testing"
)

// exportUnregisterReregisterScenario builds the regression lineage: raw
// derives a and b, b derives c; target depends directly on raw, a, c and old;
// old depends on oldroot and also derives other; alt stands alone as a
// replacement source registered up front; view depends on c. target is a leaf
// with several direct upstreams and merging branches (raw reaches it directly,
// through a, and through b -> c), so it can be unregistered and later
// re-registered under the same name.
func exportUnregisterReregisterScenario(t *testing.T) map[string]*Lineage {
	t.Helper()
	return buildRegisteredGraph(t, [][]string{
		{"raw"},
		{"a", "raw"},
		{"b", "raw"},
		{"c", "b"},
		{"oldroot"},
		{"old", "oldroot"},
		{"other", "old"},
		{"alt"},
		{"target", "raw", "a", "c", "old"},
		{"view", "c"},
	})
}

// The exact JSON texts around the remove-then-reregister sequence. All
// orderings are fully determined by the names.
const (
	// target's document while it still declares raw, a, c and old.
	exportUnregisterBefore = `{"nodes":["a","b","c","old","oldroot","raw","target"],` +
		`"edges":[{"from":"a","to":"target"},{"from":"b","to":"c"},` +
		`{"from":"c","to":"target"},{"from":"old","to":"target"},` +
		`{"from":"oldroot","to":"old"},{"from":"raw","to":"a"},` +
		`{"from":"raw","to":"b"},{"from":"raw","to":"target"}]}`
	// view's document never involves target and must survive the whole
	// sequence byte for byte.
	exportUnregisterView = `{"nodes":["b","c","raw","view"],` +
		`"edges":[{"from":"b","to":"c"},{"from":"c","to":"view"},` +
		`{"from":"raw","to":"b"}]}`
	// other's document keeps old and oldroot in use regardless of what
	// happens to target.
	exportUnregisterOther = `{"nodes":["old","oldroot","other"],` +
		`"edges":[{"from":"old","to":"other"},{"from":"oldroot","to":"old"}]}`
	// target's document after the same-name re-registration with raw, a, c
	// and alt: old and oldroot are gone, alt is in, and raw keeps BOTH its
	// direct edge to target and the longer raw -> a -> target and
	// raw -> b -> c -> target routes.
	exportUnregisterAfter = `{"nodes":["a","alt","b","c","raw","target"],` +
		`"edges":[{"from":"a","to":"target"},{"from":"alt","to":"target"},` +
		`{"from":"b","to":"c"},{"from":"c","to":"target"},` +
		`{"from":"raw","to":"a"},{"from":"raw","to":"b"},` +
		`{"from":"raw","to":"target"}]}`
)

// Regression for exporting after a dataset was UNREGISTERED and then
// re-registered under the same name with a different dependency mix. The
// export must come entirely from the current registration: sources that only
// took part in the removed registration (old, and transitively oldroot) must
// not leak into the new document just because the name is the same, even
// though they stay registered for other datasets. Sources still participating
// (raw, a, c) are kept with every surviving branch — raw reaches target
// directly AND through a AND through b -> c, so all three routes and every
// direct dependency on them appear, not just the shortest explanation path.
func TestExportUpstreamLineageAfterUnregisterAndReregister(t *testing.T) {
	graph := exportUnregisterReregisterScenario(t)
	assertConsistent(t, graph)

	// Baseline documents before anything is removed.
	if out := mustExport(t, graph, "target"); out != exportUnregisterBefore {
		t.Errorf("export before removal =\n%s\nwant:\n%s", out, exportUnregisterBefore)
	}
	if out := mustExport(t, graph, "view"); out != exportUnregisterView {
		t.Errorf("view export before removal =\n%s\nwant:\n%s", out, exportUnregisterView)
	}
	if out := mustExport(t, graph, "other"); out != exportUnregisterOther {
		t.Errorf("other export before removal =\n%s\nwant:\n%s", out, exportUnregisterOther)
	}

	// target has no direct downstream, so removal succeeds even though it
	// declares several direct upstreams. Each upstream drops exactly the
	// reverse edge to target; the rest of every list keeps its order.
	mustUnregister(t, graph, "target")
	if _, ok := graph["target"]; ok {
		t.Fatal("target registration still present after Unregister")
	}
	assertEntry(t, graph, "raw", nil, []string{"a", "b"})
	assertEntry(t, graph, "a", []string{"raw"}, nil)
	assertEntry(t, graph, "c", []string{"b"}, []string{"view"})
	assertEntry(t, graph, "old", []string{"oldroot"}, []string{"other"})
	assertEntry(t, graph, "oldroot", nil, []string{"old"})
	assertEntry(t, graph, "other", []string{"old"}, nil)
	assertConsistent(t, graph)

	// While the name is unregistered, exporting it must fail with an error
	// naming it and an empty string — never a successful document holding
	// only the target, and never the pre-removal content.
	before := snapshotExportGraph(graph)
	out, err := ExportUpstreamLineage(graph, "target")
	if err == nil {
		t.Fatalf("export of unregistered target succeeded with %q, want a not-found error", out)
	}
	if out != "" {
		t.Errorf("failed export returned partial content %q, want empty string", out)
	}
	if !strings.Contains(err.Error(), "target") {
		t.Errorf("error %q must name the removed dataset", err)
	}
	if out == exportUnregisterBefore {
		t.Errorf("export after removal returned the pre-removal document %q", out)
	}
	assertGraphUnchanged(t, before, graph)

	// The removal leaves every other target's document byte-identical: the
	// former upstreams and their other derived datasets stay registered with
	// unchanged dependencies, nodes and ordering.
	if out := mustExport(t, graph, "view"); out != exportUnregisterView {
		t.Errorf("view export after removal =\n%s\nwant:\n%s", out, exportUnregisterView)
	}
	if out := mustExport(t, graph, "other"); out != exportUnregisterOther {
		t.Errorf("other export after removal =\n%s\nwant:\n%s", out, exportUnregisterOther)
	}

	// Re-register under the same name, keeping raw, a and c from the old
	// dependency list, dropping old, and adding the previously unrelated alt.
	mustRegister(t, graph, "target", "raw", "a", "c", "alt")
	assertEntry(t, graph, "target", []string{"raw", "a", "c", "alt"}, nil)
	assertEntry(t, graph, "alt", nil, []string{"target"})
	assertEntry(t, graph, "raw", nil, []string{"a", "b", "target"})
	assertEntry(t, graph, "a", []string{"raw"}, []string{"target"})
	assertEntry(t, graph, "c", []string{"b"}, []string{"view", "target"})
	// old keeps its registration and its own downstream other; it only lost
	// the reverse edge to the removed target and gains nothing back.
	assertEntry(t, graph, "old", []string{"oldroot"}, []string{"other"})
	assertConsistent(t, graph)

	before = snapshotExportGraph(graph)
	out = mustExport(t, graph, "target")
	if out != exportUnregisterAfter {
		t.Errorf("export after re-register =\n%s\nwant:\n%s", out, exportUnregisterAfter)
	}
	doc := parseExport(t, out)

	// Every surviving branch participates: raw reaches target directly AND
	// through a AND through b -> c, so raw, a, b, c, alt and target itself
	// are each listed exactly once, in name order.
	wantNodes := []string{"a", "alt", "b", "c", "raw", "target"}
	if !reflect.DeepEqual(doc.Nodes, wantNodes) {
		t.Errorf("nodes = %v, want %v", doc.Nodes, wantNodes)
	}
	targetCount := 0
	for _, node := range doc.Nodes {
		if node == "target" {
			targetCount++
		}
	}
	if targetCount != 1 {
		t.Errorf("target appears %d times in nodes %v, want exactly once", targetCount, doc.Nodes)
	}
	wantEdges := [][2]string{
		{"a", "target"},
		{"alt", "target"},
		{"b", "c"},
		{"c", "target"},
		{"raw", "a"},
		{"raw", "b"},
		{"raw", "target"},
	}
	gotEdges := exportEdgeNames(doc)
	if !reflect.DeepEqual(gotEdges, wantEdges) {
		t.Errorf("edges = %v, want %v", gotEdges, wantEdges)
	}
	seenEdges := map[[2]string]bool{}
	for _, e := range gotEdges {
		if seenEdges[e] {
			t.Errorf("edge %v appears more than once in %v", e, gotEdges)
		}
		seenEdges[e] = true
	}

	// Sources that only belonged to the REMOVED registration stay out, even
	// though old and oldroot are still registered and still derive other.
	// view and other never take part in target's derivation at all.
	for _, excluded := range []string{"old", "oldroot", "view", "other"} {
		for _, node := range doc.Nodes {
			if node == excluded {
				t.Errorf("nodes must not contain %q after re-register: %v", excluded, doc.Nodes)
			}
		}
		for _, e := range gotEdges {
			if e[0] == excluded || e[1] == excluded {
				t.Errorf("edges must not involve %q after re-register: %v", excluded, gotEdges)
			}
		}
	}

	// The successful export is read-only over the post-replacement state.
	assertGraphUnchanged(t, before, graph)

	// Unrelated targets are still exported exactly as before the removal.
	if out := mustExport(t, graph, "view"); out != exportUnregisterView {
		t.Errorf("view export after re-register =\n%s\nwant:\n%s", out, exportUnregisterView)
	}
	if out := mustExport(t, graph, "other"); out != exportUnregisterOther {
		t.Errorf("other export after re-register =\n%s\nwant:\n%s", out, exportUnregisterOther)
	}
}

// A same-name re-registration with NO upstreams exports successfully with the
// target as the only node and an empty (non-null) edges array — a successful
// degenerate document, not to be confused with the not-found failure that the
// same name produced while it was unregistered.
func TestExportUpstreamLineageReregisterWithoutUpstreams(t *testing.T) {
	graph := buildRegisteredGraph(t, [][]string{
		{"raw"},
		{"target", "raw"},
	})

	mustUnregister(t, graph, "target")
	out, err := ExportUpstreamLineage(graph, "target")
	if err == nil || out != "" {
		t.Fatalf("export while unregistered = %q, %v; want empty string and not-found error", out, err)
	}

	// The freed name registers again as a standalone dataset.
	mustRegister(t, graph, "target")
	assertEntry(t, graph, "target", nil, nil)
	assertEntry(t, graph, "raw", nil, nil)
	assertConsistent(t, graph)

	before := snapshotExportGraph(graph)
	out = mustExport(t, graph, "target")
	if want := `{"nodes":["target"],"edges":[]}`; out != want {
		t.Errorf("export of upstream-less re-registration = %s, want %s", out, want)
	}
	doc := parseExport(t, out)
	if doc.Edges == nil {
		t.Errorf("edges must decode as an empty array, got nil from %s", out)
	}
	assertGraphUnchanged(t, before, graph)
}

// A dataset that still has direct downstreams cannot be unregistered: the
// error names it and states that downstreams remain, and afterwards the
// exports of both the refused dataset and everything derived from it are
// byte-identical to the pre-request documents — no upstream relation is left
// half-detached.
func TestExportUpstreamLineageAfterRefusedUnregister(t *testing.T) {
	graph := exportUnregisterReregisterScenario(t)
	assertConsistent(t, graph)

	// c has two direct downstreams (target and view), so removing it must be
	// refused. Pin the documents that depend on it before the attempt.
	targetBefore := mustExport(t, graph, "target")
	viewBefore := mustExport(t, graph, "view")
	cBefore := mustExport(t, graph, "c")
	before := snapshotExportGraph(graph)

	err := Unregister(graph, "c")
	if err == nil {
		t.Fatal("Unregister(c) with downstreams remaining: expected refusal, got nil")
	}
	if !strings.Contains(err.Error(), "c") {
		t.Errorf("error %q must name the refused dataset c", err)
	}
	if !strings.Contains(err.Error(), "downstream") {
		t.Errorf("error %q must state that direct downstreams remain", err)
	}

	// The refusal is atomic: no node, edge or list order moved.
	assertGraphUnchanged(t, before, graph)
	assertEntry(t, graph, "c", []string{"b"}, []string{"target", "view"})
	assertConsistent(t, graph)

	// The refused dataset itself and both of its downstreams export exactly
	// as before the request — no partially disconnected upstream relation.
	if out := mustExport(t, graph, "c"); out != cBefore {
		t.Errorf("c export after refused removal =\n%s\nwant:\n%s", out, cBefore)
	}
	if out := mustExport(t, graph, "target"); out != targetBefore {
		t.Errorf("target export after refused removal =\n%s\nwant:\n%s", out, targetBefore)
	}
	if out := mustExport(t, graph, "view"); out != viewBefore {
		t.Errorf("view export after refused removal =\n%s\nwant:\n%s", out, viewBefore)
	}
	assertGraphUnchanged(t, before, graph)
}
