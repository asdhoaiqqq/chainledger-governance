package chainledger

import (
	"bytes"
	"errors"
	"reflect"
	"strings"
	"testing"
)

// This file pins the content independence of lineage snapshots returned by
// BuildSnapshot. A caller that builds a snapshot from the in-memory graph keeps
// holding the original map and may hold several snapshots of the same graph at
// once. Every returned snapshot must freeze the datasets and direct upstreams
// as they were when BuildSnapshot returned:
//
//   - editing the original graph afterwards (replacing an upstream name in
//     place, appending to a stored list, removing a node) must not move an
//     already returned snapshot's content identifier or its full export bytes,
//     which must still parse under the existing reader rules with an id
//     matching the embedded graph;
//   - editing one returned snapshot's records (an upstream record or a dataset
//     record) must not reach another snapshot of the same graph, nor the
//     graph — including two dataset records that started with identical
//     upstream lists, which must own separate backing arrays;
//   - BuildSnapshot itself stays read-only: the graph keeps its submitted
//     upstream order and duplicates, while the snapshot sorts and deduplicates
//     by the existing rules, roots export "upstreams": [], and an empty graph
//     exports "datasets": [];
//   - a snapshot taken before the graph gained a missing upstream still
//     exports, while building again from the now-corrupt graph fails by the
//     existing rules, returns no usable snapshot, and changes nothing.
//
// The deliberately corrupted copy a caller edits for display is out of scope:
// nothing repairs it or recomputes its content identifier.

// mustMarshalSnapshot renders a snapshot the way every caller exports it,
// failing the test on an impossible-on-valid-input error.
func mustMarshalSnapshot(t *testing.T, snap *SnapshotFile) []byte {
	t.Helper()
	data, err := MarshalSnapshot(snap)
	if err != nil {
		t.Fatalf("MarshalSnapshot unexpected error: %v", err)
	}
	return data
}

// snapshotDatasetIndex returns the index of name's record in snap, or -1.
func snapshotDatasetIndex(snap *SnapshotFile, name string) int {
	for i := range snap.Graph.Datasets {
		if snap.Graph.Datasets[i].Name == name {
			return i
		}
	}
	return -1
}

// mustSnapshotDataset returns a value copy of name's record in snap.
func mustSnapshotDataset(t *testing.T, snap *SnapshotFile, name string) GraphDataset {
	t.Helper()
	if i := snapshotDatasetIndex(snap, name); i >= 0 {
		return snap.Graph.Datasets[i]
	}
	t.Fatalf("snapshot has no dataset record %q", name)
	return GraphDataset{}
}

// independentDiamondGraph builds the headline shape: two roots A and R, an
// intermediate M that depends on BOTH roots, and a downstream D depending on
// M. Register leaves every stored Parents list already sorted and
// duplicate-free, which is the tempting shortcut case for an implementation
// that could hand the graph's own slice to the snapshot.
func independentDiamondGraph(t *testing.T) map[string]*Lineage {
	t.Helper()
	return buildGraph(t, P("A"), P("R"), P("M", "A", "R"), P("D", "M"))
}

// TestSnapshotFreezesContentWhenGraphEditedAfterward is the headline
// independence guarantee: after the caller edits the original graph in place
// (replacing a name inside an existing upstream list, appending a ghost
// upstream, and removing a node), the old snapshot still holds every original
// node and edge, with the same content identifier and byte-identical export,
// and those bytes still read back as a legal snapshot whose id matches its
// graph.
func TestSnapshotFreezesContentWhenGraphEditedAfterward(t *testing.T) {
	graph := independentDiamondGraph(t)
	snap, err := BuildSnapshot(graph)
	if err != nil {
		t.Fatalf("BuildSnapshot unexpected error: %v", err)
	}
	wantID := snap.ContentID
	wantBytes := mustMarshalSnapshot(t, snap)

	// Edit the original graph as a caller holding it might: overwrite a parent
	// name inside the existing backing array, append into it, and delete a
	// node the frozen snapshot must still contain. The graph is now corrupt
	// (M references missing upstreams), which is exactly why the frozen copy
	// must not read through to it.
	graph["M"].Parents[0] = "HACK"
	graph["M"].Parents = append(graph["M"].Parents, "ghost")
	delete(graph, "R")

	// Sanity: the edits really landed on the graph.
	if _, present := graph["R"]; present {
		t.Fatalf("test setup: node R still present in the original graph")
	}
	if got := graph["M"].Parents; reflect.DeepEqual(got, []string{"A", "R"}) {
		t.Fatalf("test setup: M.Parents was not edited: %v", got)
	}

	if got := snap.ContentID; got != wantID {
		t.Errorf("snapshot content id moved with the graph: %s, want %s", got, wantID)
	}
	if got := mustMarshalSnapshot(t, snap); !bytes.Equal(got, wantBytes) {
		t.Errorf("snapshot export moved with the graph:\n got %s\nwant %s", got, wantBytes)
	}

	// The frozen export still obeys every existing reader rule, including the
	// check that the content id matches the embedded graph.
	parsed, err := ParseSnapshot(wantBytes)
	if err != nil {
		t.Fatalf("frozen snapshot no longer parses after the graph was edited: %v", err)
	}
	if parsed.ContentID != wantID {
		t.Errorf("parsed content id = %s, want %s", parsed.ContentID, wantID)
	}
	if !reflect.DeepEqual(parsed, snap) {
		t.Errorf("parsed snapshot differs from the returned snapshot:\n got %#v\nwant %#v", parsed, snap)
	}

	// All four original nodes and every original edge survive.
	if got := len(snap.Graph.Datasets); got != 4 {
		t.Errorf("frozen snapshot has %d datasets, want 4: %+v", got, snap.Graph.Datasets)
	}
	if got := mustSnapshotDataset(t, snap, "R").Upstreams; !reflect.DeepEqual(got, []string{}) {
		t.Errorf("removed root R not frozen with empty upstreams: %v", got)
	}
	if got := mustSnapshotDataset(t, snap, "M").Upstreams; !reflect.DeepEqual(got, []string{"A", "R"}) {
		t.Errorf("M.upstreams = %v, want frozen [A R]", got)
	}
	if got := mustSnapshotDataset(t, snap, "D").Upstreams; !reflect.DeepEqual(got, []string{"M"}) {
		t.Errorf("D.upstreams = %v, want frozen [M]", got)
	}
	if got := mustSnapshotDataset(t, snap, "A").Upstreams; !reflect.DeepEqual(got, []string{}) {
		t.Errorf("A.upstreams = %v, want frozen empty list", got)
	}
	// The frozen graph's lineage still resolves D to both original roots.
	if got := rootSourceNames("D", adjacencyFromValidFile(snap.Graph)); !reflect.DeepEqual(got, []string{"A", "R"}) {
		t.Errorf("frozen D roots = %v, want [A R]", got)
	}
}

// TestSnapshotBuildIsReadOnly pins the read-only build: shuffled and repeated
// upstreams in the submitted graph stay verbatim there, while the snapshot
// normalizes by the existing sorting and de-duplication rules, roots carry a
// non-nil empty upstream list, and an empty graph exports "datasets": [].
func TestSnapshotBuildIsReadOnly(t *testing.T) {
	// Built by hand on purpose: Register would already normalize Parents, but
	// BuildSnapshot must tolerate and preserve any stored spelling.
	graph := map[string]*Lineage{
		"A": {Dataset: "A"},
		"R": {Dataset: "R"},
		"M": {Dataset: "M", Parents: []string{"R", "A", "R"}},
		"D": {Dataset: "D", Parents: []string{"M", "M"}},
	}
	snap, err := BuildSnapshot(graph)
	if err != nil {
		t.Fatalf("BuildSnapshot unexpected error: %v", err)
	}

	// The original graph keeps its submitted order, duplicates, and untouched
	// derived fields (BuildSnapshot never maintains Children).
	if got := graph["M"].Parents; !reflect.DeepEqual(got, []string{"R", "A", "R"}) {
		t.Errorf("BuildSnapshot reordered/deduped M.Parents: %v, want verbatim [R A R]", got)
	}
	if got := graph["D"].Parents; !reflect.DeepEqual(got, []string{"M", "M"}) {
		t.Errorf("BuildSnapshot deduped D.Parents: %v, want verbatim [M M]", got)
	}
	if got := graph["A"].Children; got != nil {
		t.Errorf("BuildSnapshot wrote derived Children on a root: %v", got)
	}

	// The snapshot side is normalized instead: records sorted by name and
	// upstreams sorted and duplicate-free.
	wantNames := []string{"A", "D", "M", "R"}
	gotNames := make([]string, 0, len(snap.Graph.Datasets))
	for _, ds := range snap.Graph.Datasets {
		gotNames = append(gotNames, ds.Name)
	}
	if !reflect.DeepEqual(gotNames, wantNames) {
		t.Errorf("snapshot dataset order = %v, want %v", gotNames, wantNames)
	}
	if got := mustSnapshotDataset(t, snap, "M").Upstreams; !reflect.DeepEqual(got, []string{"A", "R"}) {
		t.Errorf("snapshot M.upstreams = %v, want sorted/deduped [A R]", got)
	}
	if got := mustSnapshotDataset(t, snap, "D").Upstreams; !reflect.DeepEqual(got, []string{"M"}) {
		t.Errorf("snapshot D.upstreams = %v, want deduped [M]", got)
	}
	for _, name := range []string{"A", "R"} {
		got := mustSnapshotDataset(t, snap, name).Upstreams
		if got == nil || len(got) != 0 {
			t.Errorf("root %s upstreams = %v, want non-nil empty list", name, got)
		}
	}
	if data := mustMarshalSnapshot(t, snap); !strings.Contains(string(data), `"upstreams": []`) {
		t.Errorf("root upstreams not exported as an empty array:\n%s", data)
	}

	// An empty graph is legal and exports datasets as an empty array.
	empty, err := BuildSnapshot(map[string]*Lineage{})
	if err != nil {
		t.Fatalf("BuildSnapshot(empty) unexpected error: %v", err)
	}
	if len(empty.Graph.Datasets) != 0 {
		t.Errorf("empty graph produced datasets %v", empty.Graph.Datasets)
	}
	if data := mustMarshalSnapshot(t, empty); !strings.Contains(string(data), `"datasets": []`) {
		t.Errorf("empty graph datasets not exported as []:\n%s", data)
	}
}

// TestTwoSnapshotsOfSameGraphAreIndependent verifies that two snapshots
// returned separately from one graph start out identical (same content id and
// export bytes), but editing an upstream record or a dataset record in one
// copy leaves the other copy and the original graph untouched.
func TestTwoSnapshotsOfSameGraphAreIndependent(t *testing.T) {
	graph := independentDiamondGraph(t)
	first, err := BuildSnapshot(graph)
	if err != nil {
		t.Fatalf("first BuildSnapshot unexpected error: %v", err)
	}
	second, err := BuildSnapshot(graph)
	if err != nil {
		t.Fatalf("second BuildSnapshot unexpected error: %v", err)
	}
	// An independently built reference describing the same frozen graph.
	reference, err := BuildSnapshot(independentDiamondGraph(t))
	if err != nil {
		t.Fatalf("reference BuildSnapshot unexpected error: %v", err)
	}

	if first.ContentID != second.ContentID {
		t.Fatalf("initial content ids differ: %s vs %s", first.ContentID, second.ContentID)
	}
	if got, want := mustMarshalSnapshot(t, first), mustMarshalSnapshot(t, second); !bytes.Equal(got, want) {
		t.Fatalf("initial export bytes differ:\n%s\n%s", got, want)
	}

	graphDumpBefore := dump(graph)

	// Corrupt the first returned snapshot for display: rewrite an upstream in
	// place, append into the same backing array, and change a dataset record's
	// name.
	iM := snapshotDatasetIndex(first, "M")
	if iM < 0 {
		t.Fatalf("first snapshot missing M")
	}
	first.Graph.Datasets[iM].Upstreams[0] = "HACK"
	first.Graph.Datasets[iM].Upstreams = append(first.Graph.Datasets[iM].Upstreams, "ghost")
	iD := snapshotDatasetIndex(first, "D")
	if iD < 0 {
		t.Fatalf("first snapshot missing D")
	}
	first.Graph.Datasets[iD].Name = "DISPLAY"

	// Sanity: the local edit really happened on the first copy.
	if got := first.Graph.Datasets[iM].Upstreams; reflect.DeepEqual(got, []string{"A", "R"}) {
		t.Fatalf("test setup: editing the first snapshot had no local effect: %v", got)
	}

	// The second snapshot is byte-for-byte the original frozen graph.
	if got := second.ContentID; got != reference.ContentID {
		t.Errorf("second snapshot content id = %s, want %s", got, reference.ContentID)
	}
	if got, want := mustMarshalSnapshot(t, second), mustMarshalSnapshot(t, reference); !bytes.Equal(got, want) {
		t.Errorf("second snapshot export changed:\n got %s\nwant %s", got, want)
	}
	if !reflect.DeepEqual(second.Graph, reference.Graph) {
		t.Errorf("second snapshot graph = %#v, want %#v", second.Graph, reference.Graph)
	}
	if got := mustSnapshotDataset(t, second, "M").Upstreams; !reflect.DeepEqual(got, []string{"A", "R"}) {
		t.Errorf("second snapshot M.upstreams = %v, want untouched [A R]", got)
	}
	if got := mustSnapshotDataset(t, second, "D"); got.Name != "D" || !reflect.DeepEqual(got.Upstreams, []string{"M"}) {
		t.Errorf("second snapshot D record changed: %#v", got)
	}

	// The original graph is untouched as well.
	if got := dump(graph); got != graphDumpBefore {
		t.Errorf("editing a snapshot mutated the original graph:\n got %s\nwant %s", got, graphDumpBefore)
	}
	if got := graph["M"].Parents; !reflect.DeepEqual(got, []string{"A", "R"}) {
		t.Errorf("graph M.Parents = %v, want untouched [A R]", got)
	}
	if got := graph["D"].Parents; !reflect.DeepEqual(got, []string{"M"}) {
		t.Errorf("graph D.Parents = %v, want untouched [M]", got)
	}
}

// TestSnapshotSharedUpstreamRecordsAreIndependent verifies that when two
// datasets are frozen with the same direct-upstream list, each snapshot record
// owns that list: editing one record (in place and via append) must not drag
// the other record — in either the snapshot or the original graph.
func TestSnapshotSharedUpstreamRecordsAreIndependent(t *testing.T) {
	// Two roots; both M and N derive from both; a downstream keeps M used.
	graph := buildGraph(t, P("A"), P("R"), P("M", "A", "R"), P("N", "R", "A"), P("D", "M"))
	snap, err := BuildSnapshot(graph)
	if err != nil {
		t.Fatalf("BuildSnapshot unexpected error: %v", err)
	}
	if got := mustSnapshotDataset(t, snap, "M").Upstreams; !reflect.DeepEqual(got, []string{"A", "R"}) {
		t.Fatalf("test setup: M.upstreams = %v, want [A R]", got)
	}
	if got := mustSnapshotDataset(t, snap, "N").Upstreams; !reflect.DeepEqual(got, []string{"A", "R"}) {
		t.Fatalf("test setup: N.upstreams = %v, want [A R]", got)
	}

	// Edit only M's frozen record, both by overwriting in place and by
	// appending into its backing array.
	iM := snapshotDatasetIndex(snap, "M")
	snap.Graph.Datasets[iM].Upstreams[0] = "X"
	snap.Graph.Datasets[iM].Upstreams = append(snap.Graph.Datasets[iM].Upstreams, "Y")

	if got := mustSnapshotDataset(t, snap, "N").Upstreams; !reflect.DeepEqual(got, []string{"A", "R"}) {
		t.Errorf("N.upstreams = %v, want untouched [A R] after editing M's record", got)
	}
	if got := mustSnapshotDataset(t, snap, "D").Upstreams; !reflect.DeepEqual(got, []string{"M"}) {
		t.Errorf("D.upstreams = %v, want untouched [M]", got)
	}
	if got := graph["N"].Parents; !reflect.DeepEqual(got, []string{"A", "R"}) {
		t.Errorf("graph N.Parents = %v, want untouched [A R]", got)
	}
	if got := graph["M"].Parents; !reflect.DeepEqual(got, []string{"A", "R"}) {
		t.Errorf("graph M.Parents = %v, want untouched [A R]", got)
	}
}

// TestOldSnapshotExportsAfterGraphCorruptsRebuildFails verifies the boundary
// around a graph that becomes invalid after a snapshot was taken: the existing
// legal snapshot still exports and parses unchanged, while building again from
// the corrupt graph fails by the existing rules, returns no usable snapshot,
// and mutates neither the graph nor the previously saved snapshot.
func TestOldSnapshotExportsAfterGraphCorruptsRebuildFails(t *testing.T) {
	cases := map[string]struct {
		corrupt  func(map[string]*Lineage)
		wantErr  error
		checkErr func(error) bool
	}{
		"missing upstream": {
			corrupt: func(g map[string]*Lineage) { g["B"].Parents = []string{"ghost"} },
			wantErr: ErrNotFound,
		},
		"cycle introduced": {
			corrupt: func(g map[string]*Lineage) { g["A"].Parents = []string{"B"} },
			wantErr: ErrCycle,
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			graph := buildGraph(t, P("A"), P("B", "A"), P("C", "B"))
			old, err := BuildSnapshot(graph)
			if err != nil {
				t.Fatalf("initial BuildSnapshot unexpected error: %v", err)
			}
			wantID := old.ContentID
			wantBytes := mustMarshalSnapshot(t, old)

			tc.corrupt(graph)
			graphDumpAfterCorruption := dump(graph)

			bad, err := BuildSnapshot(graph)
			if !errors.Is(err, tc.wantErr) {
				t.Fatalf("BuildSnapshot on corrupt graph err = %v, want %v", err, tc.wantErr)
			}
			if bad != nil {
				t.Fatalf("failed build returned a usable snapshot: %#v", bad)
			}

			// The failed build changed nothing: the corrupt graph is as the
			// caller left it and the old snapshot is byte-for-byte intact.
			if got := dump(graph); got != graphDumpAfterCorruption {
				t.Errorf("failed build mutated the graph:\n got %s\nwant %s", got, graphDumpAfterCorruption)
			}
			if got := old.ContentID; got != wantID {
				t.Errorf("old snapshot content id = %s, want %s", got, wantID)
			}
			if got := mustMarshalSnapshot(t, old); !bytes.Equal(got, wantBytes) {
				t.Errorf("old snapshot export changed:\n got %s\nwant %s", got, wantBytes)
			}
			parsed, err := ParseSnapshot(wantBytes)
			if err != nil {
				t.Fatalf("old snapshot no longer parses: %v", err)
			}
			if parsed.ContentID != wantID {
				t.Errorf("parsed old snapshot id = %s, want %s", parsed.ContentID, wantID)
			}
		})
	}
}

// TestSnapshotPreservesNameCaseAndWhitespace verifies that the frozen records
// keep dataset and upstream names verbatim: casing and surrounding spaces
// still distinguish datasets, and the distinction survives the full export
// and re-read.
func TestSnapshotPreservesNameCaseAndWhitespace(t *testing.T) {
	// "A", "a", and " A " are three different datasets; M derives from the
	// two space/case-variant roots.
	graph := buildGraph(t, P("A"), P("a"), P(" A "), P("M", " A ", "a"))
	snap, err := BuildSnapshot(graph)
	if err != nil {
		t.Fatalf("BuildSnapshot unexpected error: %v", err)
	}

	// Byte order sorts " A " (0x20) before "A" (0x41) before "M" before "a"
	// (0x61); the point here is not the exact order but that every spelling
	// survives as its own record.
	names := map[string]bool{}
	for _, ds := range snap.Graph.Datasets {
		names[ds.Name] = true
	}
	for _, want := range []string{"A", "a", " A ", "M"} {
		if !names[want] {
			t.Errorf("snapshot lost or rewrote dataset name %q; records: %v", want, names)
		}
	}
	if got := mustSnapshotDataset(t, snap, "M").Upstreams; !reflect.DeepEqual(got, []string{" A ", "a"}) {
		t.Errorf("M.upstreams = %v, want verbatim [\" A \" \"a\"]", got)
	}

	data := mustMarshalSnapshot(t, snap)
	// The space-wrapped name must appear verbatim in the export, never trimmed.
	if !bytes.Contains(data, []byte(`" A "`)) {
		t.Errorf("export does not preserve the space-wrapped name:\n%s", data)
	}
	parsed, err := ParseSnapshot(data)
	if err != nil {
		t.Fatalf("name-variant snapshot failed to parse: %v", err)
	}
	if parsed.ContentID != snap.ContentID {
		t.Errorf("parsed content id = %s, want %s", parsed.ContentID, snap.ContentID)
	}
	if !reflect.DeepEqual(parsed.Graph, snap.Graph) {
		t.Errorf("parsed graph differs: %#v\nwant %#v", parsed.Graph, snap.Graph)
	}

	// Editing the original graph afterwards still cannot rename a frozen node.
	graph["M"].Parents[0] = "trimmed"
	if got := mustSnapshotDataset(t, snap, "M").Upstreams; !reflect.DeepEqual(got, []string{" A ", "a"}) {
		t.Errorf("frozen M.upstreams followed a graph edit: %v", got)
	}
}
