package chainledger

import (
	"reflect"
	"slices"
	"strings"
	"testing"
)

// exportUnregisterReuseScenario builds the lineage for the
// remove-then-reuse-the-same-name regression:
//
//	shared ──> a ──> b ──┐
//	shared ──> x ───────┼──> c ──┐
//	shared ─────────────┼────────┼──> target ──> view
//	a ──────────────────┘        │
//	dropped ──> d ───────────────┘
//	dropped ──> other (unrelated branch that survives every phase)
//	lonely (registered, never connected)
//
// target is a leaf with FOUR direct upstreams — shared, a, c and d — even
// though shared also reaches it along longer routes (through b -> c and
// through x -> c). dropped reaches target only through d. view is target's
// sole direct downstream and is removed first in the tests so that target
// itself becomes removable.
func exportUnregisterReuseScenario(t *testing.T) map[string]*Lineage {
	t.Helper()
	return buildRegisteredGraph(t, [][]string{
		{"shared"},
		{"dropped"},
		{"a", "shared"},
		{"x", "shared"},
		{"b", "a"},
		{"c", "b", "x"},
		{"d", "dropped"},
		{"target", "shared", "a", "c", "d"},
		{"other", "dropped"},
		{"view", "target"},
		{"lonely"},
	})
}

// Pinned JSON texts, all fully determined by the names.
const (
	// target's document before removal: every ancestor and every direct
	// dependency, the direct shared -> target edge and both longer merged
	// routes included.
	exportUnregisterBefore = `{"nodes":["a","b","c","d","dropped","shared","target","x"],` +
		`"edges":[{"from":"a","to":"b"},{"from":"a","to":"target"},` +
		`{"from":"b","to":"c"},{"from":"c","to":"target"},` +
		`{"from":"d","to":"target"},{"from":"dropped","to":"d"},` +
		`{"from":"shared","to":"a"},{"from":"shared","to":"target"},` +
		`{"from":"shared","to":"x"},{"from":"x","to":"c"}]}`

	// view simply adds its own node and the target -> view edge on top of
	// target's whole document.
	exportUnregisterViewBefore = `{"nodes":["a","b","c","d","dropped","shared","target","view","x"],` +
		`"edges":[{"from":"a","to":"b"},{"from":"a","to":"target"},` +
		`{"from":"b","to":"c"},{"from":"c","to":"target"},` +
		`{"from":"d","to":"target"},{"from":"dropped","to":"d"},` +
		`{"from":"shared","to":"a"},{"from":"shared","to":"target"},` +
		`{"from":"shared","to":"x"},{"from":"target","to":"view"},` +
		`{"from":"x","to":"c"}]}`

	// other depends only on dropped; its relationships never change in any
	// phase, so its document must stay byte-identical throughout.
	exportUnregisterOtherDoc = `{"nodes":["dropped","other"],` +
		`"edges":[{"from":"dropped","to":"other"}]}`

	// After target is registered again under the same name with parents
	// shared, c and x: dropped and d leave the document, shared and x stay
	// direct parents and shared also feeds the two merged branches through c,
	// so every surviving direct dependency appears — the long routes are not
	// collapsed into the direct edges.
	exportUnregisterReused = `{"nodes":["a","b","c","shared","target","x"],` +
		`"edges":[{"from":"a","to":"b"},{"from":"b","to":"c"},` +
		`{"from":"c","to":"target"},{"from":"shared","to":"a"},` +
		`{"from":"shared","to":"target"},{"from":"shared","to":"x"},` +
		`{"from":"x","to":"c"},{"from":"x","to":"target"}]}`

	// The same name finally registered with no upstreams: a successful
	// one-node document with an empty edges array — this is what a registered
	// root looks like, as opposed to the not-found failure during the gap.
	exportUnregisterSoloTarget = `{"nodes":["target"],"edges":[]}`
)

// Full lifecycle regression: a leaf dataset with several upstreams is
// unregistered, the freed name briefly denotes nothing, and then a different
// dataset with different dependencies is registered under the same name.
// Every ExportUpstreamLineage call must describe the CURRENT registration:
// the document must fail while the name is gone (never a target-only or stale
// document), and after reuse it must mix in none of the old sources or edges
// even though some old sources — dropped included — stay registered for other
// datasets, while the shared source and both merged branches it still feeds
// must remain complete.
func TestExportUpstreamLineageUnregisterThenSameNameReregister(t *testing.T) {
	graph := exportUnregisterReuseScenario(t)
	assertConsistent(t, graph)

	// Phase 0: the original registration exports its complete ancestry, and
	// every export — including a failing one — is read-only.
	before := snapshotExportGraph(graph)
	if out := mustExport(t, graph, "target"); out != exportUnregisterBefore {
		t.Fatalf("target export before removal =\n%s\nwant:\n%s", out, exportUnregisterBefore)
	}
	if out := mustExport(t, graph, "view"); out != exportUnregisterViewBefore {
		t.Fatalf("view export before removal =\n%s\nwant:\n%s", out, exportUnregisterViewBefore)
	}
	if out := mustExport(t, graph, "other"); out != exportUnregisterOtherDoc {
		t.Fatalf("other export before removal =\n%s\nwant:\n%s", out, exportUnregisterOtherDoc)
	}
	if out, err := ExportUpstreamLineage(graph, "ghost"); err == nil ||
		!strings.Contains(err.Error(), "ghost") || out != "" {
		t.Fatalf("ghost export: want not-found error naming ghost and empty text, got %q, %v", out, err)
	}
	assertGraphUnchanged(t, before, graph)

	// view is target's only direct downstream; removing it first makes target
	// a leaf. target still declares four upstreams, which must not block
	// removal: only direct DOWNSTREAMS gate Unregister.
	mustUnregister(t, graph, "view")
	assertEntry(t, graph, "target", []string{"shared", "a", "c", "d"}, nil)
	if err := Unregister(graph, "target"); err != nil {
		t.Fatalf("Unregister(target) on a multi-parent leaf: %v", err)
	}
	assertConsistent(t, graph)

	// The registration is gone and no empty node remains; view left with it.
	if _, ok := graph["target"]; ok {
		t.Fatal("target registration still present after Unregister")
	}
	if _, ok := graph["view"]; ok {
		t.Fatal("view registration still present after Unregister")
	}
	if got, want := len(graph), 9; got != want {
		t.Fatalf("dataset count = %d, want %d; graph=%v", got, want, graph)
	}

	// Every former upstream dropped exactly the target reverse edge, keeping
	// its other children and their positions; the old sources and their other
	// derivations stay registered.
	assertEntry(t, graph, "shared", nil, []string{"a", "x"})
	assertEntry(t, graph, "a", []string{"shared"}, []string{"b"})
	assertEntry(t, graph, "x", []string{"shared"}, []string{"c"})
	assertEntry(t, graph, "b", []string{"a"}, []string{"c"})
	assertEntry(t, graph, "c", []string{"b", "x"}, nil)
	assertEntry(t, graph, "d", []string{"dropped"}, nil)
	assertEntry(t, graph, "dropped", nil, []string{"d", "other"})
	assertEntry(t, graph, "other", []string{"dropped"}, nil)
	assertEntry(t, graph, "lonely", nil, nil)

	// Phase 1: the name is not registered. Exporting it must fail naming the
	// name and return the empty string — never a successful document holding
	// just the target, and never the pre-removal content.
	gap := snapshotExportGraph(graph)
	out, err := ExportUpstreamLineage(graph, "target")
	if err == nil {
		t.Fatalf("export during the gap succeeded with %q, want a not-found error", out)
	}
	if !strings.Contains(err.Error(), "target") {
		t.Errorf("error %q must name the unregistered target", err)
	}
	if out != "" {
		t.Errorf("export during the gap returned %q, want empty string", out)
	}
	if strings.Contains(out, "dropped") {
		t.Errorf("gap export must not return pre-removal content: %q", out)
	}

	// Targets whose relationships did not change export exactly what they did
	// before the removal: other (dropped -> other) and c (both surviving
	// shared branches).
	if got := mustExport(t, graph, "other"); got != exportUnregisterOtherDoc {
		t.Errorf("other export during the gap =\n%s\nwant:\n%s", got, exportUnregisterOtherDoc)
	}
	cGapDoc := `{"nodes":["a","b","c","shared","x"],` +
		`"edges":[{"from":"a","to":"b"},{"from":"b","to":"c"},` +
		`{"from":"shared","to":"a"},{"from":"shared","to":"x"},` +
		`{"from":"x","to":"c"}]}`
	if got := mustExport(t, graph, "c"); got != cGapDoc {
		t.Errorf("c export during the gap =\n%s\nwant:\n%s", got, cGapDoc)
	}
	// The removed target's old sources are still there on their own: dropped
	// is a root and d still derives from it.
	if got := mustExport(t, graph, "dropped"); got != `{"nodes":["dropped"],"edges":[]}` {
		t.Errorf("dropped export during the gap = %s, want the solo-root document", got)
	}
	if got := mustExport(t, graph, "d"); got != `{"nodes":["d","dropped"],"edges":[{"from":"dropped","to":"d"}]}` {
		t.Errorf("d export during the gap = %s, want dropped -> d unchanged", got)
	}
	// Successful and failed exports alike change no registration, edge or
	// list order during the gap.
	assertGraphUnchanged(t, gap, graph)

	// Phase 2: register a DIFFERENT dataset under the freed name. It keeps
	// shared and x as direct parents (shared also through both branches via c)
	// and c, and drops d and dropped entirely.
	mustRegister(t, graph, "target", "shared", "c", "x")
	assertConsistent(t, graph)
	assertEntry(t, graph, "target", []string{"shared", "c", "x"}, nil)
	// target is appended at the end of each kept upstream's child list; c and
	// x had lost the old reverse edge and gain a fresh one now.
	assertEntry(t, graph, "shared", nil, []string{"a", "x", "target"})
	assertEntry(t, graph, "c", []string{"b", "x"}, []string{"target"})
	assertEntry(t, graph, "x", []string{"shared"}, []string{"c", "target"})
	// The exited branch is untouched: dropped still serves d and other, and d
	// keeps no edge toward the new target.
	assertEntry(t, graph, "dropped", nil, []string{"d", "other"})
	assertEntry(t, graph, "d", []string{"dropped"}, nil)
	assertEntry(t, graph, "other", []string{"dropped"}, nil)

	// The document comes solely from the new registration: no stale dropped/d
	// content can follow the reused name in.
	reused := snapshotExportGraph(graph)
	out = mustExport(t, graph, "target")
	if out != exportUnregisterReused {
		t.Fatalf("target export after same-name re-registration =\n%s\nwant:\n%s", out, exportUnregisterReused)
	}
	doc := parseExport(t, out)

	wantNodes := []string{"a", "b", "c", "shared", "target", "x"}
	if !reflect.DeepEqual(doc.Nodes, wantNodes) {
		t.Errorf("nodes = %v, want %v", doc.Nodes, wantNodes)
	}
	wantEdges := [][2]string{
		{"a", "b"},
		{"b", "c"},
		{"c", "target"},
		{"shared", "a"},
		{"shared", "target"},
		{"shared", "x"},
		{"x", "c"},
		{"x", "target"},
	}
	gotEdges := exportEdgeNames(doc)
	if !reflect.DeepEqual(gotEdges, wantEdges) {
		t.Errorf("edges = %v, want %v", gotEdges, wantEdges)
	}

	// The target appears exactly once, and no node or edge is duplicated.
	if count := countString(doc.Nodes, "target"); count != 1 {
		t.Errorf("target appears %d times in nodes %v, want exactly once", count, doc.Nodes)
	}
	if !allUnique(doc.Nodes) {
		t.Errorf("nodes repeat: %v", doc.Nodes)
	}
	if !allUniqueEdges(gotEdges) {
		t.Errorf("edges repeat: %v", gotEdges)
	}

	// Every surviving branch and direct dependency is present, not only the
	// shortest explanation: the direct shared -> target and x -> target edges,
	// shared -> a -> b -> c -> target and shared -> x -> c -> target.
	for _, want := range wantEdges {
		if !slices.Contains(gotEdges, want) {
			t.Errorf("reused document is missing direct dependency %v in %v", want, gotEdges)
		}
	}
	// Sources that only served the OLD relation stay out even though they
	// remain registered elsewhere, and their old edges stay out too.
	for _, excluded := range []string{"dropped", "d", "view", "lonely"} {
		if slices.Contains(doc.Nodes, excluded) {
			t.Errorf("old/unrelated node %q must not enter the new target's document: %v",
				excluded, doc.Nodes)
		}
	}
	for _, e := range gotEdges {
		if e[0] == "dropped" || e[1] == "d" || e == [2]string{"a", "target"} {
			t.Errorf("old dependency %v survived into the reused-name document", e)
		}
	}

	// other's document is still byte-identical to before the whole sequence,
	// proving dropped stayed registered and connected outside target.
	if got := mustExport(t, graph, "other"); got != exportUnregisterOtherDoc {
		t.Errorf("other export after reuse =\n%s\nwant:\n%s", got, exportUnregisterOtherDoc)
	}
	assertGraphUnchanged(t, reused, graph)

	// A rejected replacement must apply partially in no way: naming an
	// unregistered upstream fails naming it, leaves the new registration's
	// parents and every neighbor list untouched, and the next export still
	// describes the accepted registration.
	err = Register(graph, Dataset{Name: "target"}, []string{"shared", "ghost"})
	if err == nil || !strings.Contains(err.Error(), "unknown parent") ||
		!strings.Contains(err.Error(), "ghost") {
		t.Fatalf("want unknown-parent error naming ghost, got %v", err)
	}
	assertGraphUnchanged(t, reused, graph)
	assertEntry(t, graph, "target", []string{"shared", "c", "x"}, nil)
	assertEntry(t, graph, "shared", nil, []string{"a", "x", "target"})
	assertConsistent(t, graph)
	if got := mustExport(t, graph, "target"); got != exportUnregisterReused {
		t.Fatalf("target export after rejected replacement =\n%s\nwant:\n%s", got, exportUnregisterReused)
	}

	// Phase 3: re-registering the same name with NO upstreams is a successful
	// root: target alone, edges [], clearly distinct from the gap's failure.
	mustRegister(t, graph, "target")
	assertConsistent(t, graph)
	assertEntry(t, graph, "target", nil, nil)
	assertEntry(t, graph, "shared", nil, []string{"a", "x"})
	assertEntry(t, graph, "c", []string{"b", "x"}, nil)
	assertEntry(t, graph, "x", []string{"shared"}, []string{"c"})

	solo := snapshotExportGraph(graph)
	out = mustExport(t, graph, "target")
	if out != exportUnregisterSoloTarget {
		t.Fatalf("target export with no upstreams = %s, want %s", out, exportUnregisterSoloTarget)
	}
	soloDoc := parseExport(t, out)
	if !reflect.DeepEqual(soloDoc.Nodes, []string{"target"}) {
		t.Errorf("nodes = %v, want [target]", soloDoc.Nodes)
	}
	if soloDoc.Edges == nil || len(soloDoc.Edges) != 0 {
		t.Errorf("edges must decode as an empty array, got %v", soloDoc.Edges)
	}
	// The still-registered old sources and other targets remain unaffected.
	if got := mustExport(t, graph, "other"); got != exportUnregisterOtherDoc {
		t.Errorf("other export after target became a root =\n%s\nwant:\n%s",
			got, exportUnregisterOtherDoc)
	}
	// Repeating every kind of export changes nothing in the registry.
	mustExport(t, graph, "target")
	mustExport(t, graph, "shared")
	if _, err := ExportUpstreamLineage(graph, "view"); err == nil ||
		!strings.Contains(err.Error(), "view") {
		t.Fatalf("removed view must still be a not-found error, got %v", err)
	}
	assertGraphUnchanged(t, solo, graph)
}

// The reused-name document depends only on the final registration, never on
// the removed one's history: the same final graph built directly, in a
// different registration order, with flipped upstream-list orders, exports
// byte-identical text.
func TestExportUpstreamLineageSameNameReusedOrderIndependent(t *testing.T) {
	history := exportUnregisterReuseScenario(t)
	mustUnregister(t, history, "view")
	mustUnregister(t, history, "target")
	mustRegister(t, history, "target", "shared", "c", "x")

	fresh := buildRegisteredGraph(t, [][]string{
		{"lonely"},
		{"dropped"},
		{"other", "dropped"}, // dropped's child list in the opposite order
		{"d", "dropped"},
		{"shared"},
		{"x", "shared"},
		{"a", "shared"},
		{"b", "a"},
		{"c", "x", "b"}, // direct-upstream list flipped
		{"target", "x", "c", "shared"},
	})

	if got, want := mustExport(t, history, "target"), exportUnregisterReused; got != want {
		t.Fatalf("reused-name export =\n%s\nwant:\n%s", got, want)
	}
	if got := mustExport(t, fresh, "target"); got != exportUnregisterReused {
		t.Fatalf("fresh-build export =\n%s\nwant:\n%s", got, exportUnregisterReused)
	}
	if got, want := mustExport(t, history, "target"), mustExport(t, fresh, "target"); got != want {
		t.Errorf("registration history changed the export:\nhistory: %s\nfresh:   %s", got, want)
	}
	// dropped's document is identical too despite its reversed child list.
	if got, want := mustExport(t, history, "other"), mustExport(t, fresh, "other"); got != want {
		t.Errorf("other export differs:\nhistory: %s\nfresh:   %s", got, want)
	}
}

// While the target still has a direct downstream, its removal is refused with
// an error stating it is still depended on, and neither the target nor any
// downstream may afterwards export a document with even one upstream relation
// detached: every export stays byte-identical to before the request, whether
// the export call itself succeeds or fails.
func TestExportUpstreamLineageUnregisterBlockedByDownstreamLeavesExportsUntouched(t *testing.T) {
	graph := buildRegisteredGraph(t, [][]string{
		{"root"},
		{"parent2"},
		{"target", "root", "parent2"},
		{"consumer", "target"},
	})
	assertConsistent(t, graph)

	const (
		targetDoc = `{"nodes":["parent2","root","target"],` +
			`"edges":[{"from":"parent2","to":"target"},{"from":"root","to":"target"}]}`
		consumerDoc = `{"nodes":["consumer","parent2","root","target"],` +
			`"edges":[{"from":"parent2","to":"target"},{"from":"root","to":"target"},` +
			`{"from":"target","to":"consumer"}]}`
	)
	before := snapshotExportGraph(graph)
	if got := mustExport(t, graph, "target"); got != targetDoc {
		t.Fatalf("target export before blocked removal =\n%s\nwant:\n%s", got, targetDoc)
	}
	if got := mustExport(t, graph, "consumer"); got != consumerDoc {
		t.Fatalf("consumer export before blocked removal =\n%s\nwant:\n%s", got, consumerDoc)
	}

	err := Unregister(graph, "target")
	if err == nil {
		t.Fatal("Unregister(target) with direct downstream consumer: expected refusal, got nil")
	}
	if !strings.Contains(err.Error(), "target") {
		t.Errorf("error %q must name the dataset target", err)
	}
	if !strings.Contains(err.Error(), "downstream") {
		t.Errorf("error %q must state that target still has direct downstreams", err)
	}

	// No partial effect: node present, both directions of every edge and the
	// stored list orders exactly as before.
	assertGraphUnchanged(t, before, graph)
	assertEntry(t, graph, "target", []string{"root", "parent2"}, []string{"consumer"})
	assertEntry(t, graph, "consumer", []string{"target"}, nil)
	assertEntry(t, graph, "root", nil, []string{"target"})
	assertEntry(t, graph, "parent2", nil, []string{"target"})
	assertConsistent(t, graph)

	// The target and the downstream depending on it both export exactly what
	// they exported before the refused request — no half-detached document.
	if got := mustExport(t, graph, "target"); got != targetDoc {
		t.Errorf("target export after blocked removal =\n%s\nwant:\n%s", got, targetDoc)
	}
	if got := mustExport(t, graph, "consumer"); got != consumerDoc {
		t.Errorf("consumer export after blocked removal =\n%s\nwant:\n%s", got, consumerDoc)
	}
	// A failing export is read-only as well, and the graph still equals the
	// pre-request state after every successful and failed export.
	if out, err := ExportUpstreamLineage(graph, "ghost"); err == nil ||
		!strings.Contains(err.Error(), "ghost") || out != "" {
		t.Fatalf("ghost export: want naming error and empty text, got %q, %v", out, err)
	}
	assertGraphUnchanged(t, before, graph)
}

// countString reports how many entries equal value.
func countString(entries []string, value string) int {
	n := 0
	for _, e := range entries {
		if e == value {
			n++
		}
	}
	return n
}

// allUnique reports whether every entry is distinct.
func allUnique(entries []string) bool {
	seen := make(map[string]bool, len(entries))
	for _, e := range entries {
		if seen[e] {
			return false
		}
		seen[e] = true
	}
	return true
}

// allUniqueEdges reports whether every edge pair is distinct.
func allUniqueEdges(edges [][2]string) bool {
	seen := make(map[[2]string]bool, len(edges))
	for _, e := range edges {
		if seen[e] {
			return false
		}
		seen[e] = true
	}
	return true
}
