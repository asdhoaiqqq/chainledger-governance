package chainledger

import (
	"errors"
	"reflect"
	"strings"
	"testing"
)

// This file pins down content independence for lineage snapshots. After
// BuildSnapshot returns, a caller can simultaneously hold:
//
//   - the original in-memory graph, which it keeps editing (repointing parents,
//     renaming entries, deleting nodes);
//   - several snapshots built from that same graph at the same moment.
//
// Every returned snapshot must own the datasets and direct upstreams it froze
// at creation: later edits to the graph, edits to one returned snapshot, or
// even two records that originally listed the same upstreams must never move
// another snapshot's content identifier or serialized bytes. Building a
// snapshot stays read-only, and a previously legal snapshot stays exportable
// after the graph is later corrupted, while rebuilding from the corrupted
// graph fails by the existing rules.

// snapshotRecordIndex returns the index of name in a snapshot's graph, or -1.
func snapshotRecordIndex(snap *SnapshotFile, name string) int {
	for i := range snap.Graph.Datasets {
		if snap.Graph.Datasets[i].Name == name {
			return i
		}
	}
	return -1
}

// mustSnapshotRecord returns a value copy of name's record from snap, failing
// the test when the frozen graph does not contain the dataset.
func mustSnapshotRecord(t *testing.T, snap *SnapshotFile, name string) GraphDataset {
	t.Helper()
	if i := snapshotRecordIndex(snap, name); i >= 0 {
		return snap.Graph.Datasets[i]
	}
	t.Fatalf("snapshot %s has no dataset record %q", snap.ContentID, name)
	return GraphDataset{}
}

// mustMarshalSnapshot renders a snapshot the way a caller saving it would.
func mustMarshalSnapshot(t *testing.T, snap *SnapshotFile) []byte {
	t.Helper()
	data, err := MarshalSnapshot(snap)
	if err != nil {
		t.Fatalf("MarshalSnapshot: %v", err)
	}
	return data
}

// snapshotFrozenGraph builds the headline graph: two roots A and B, an
// intermediate M depending on both roots, and a downstream D depending on M.
func snapshotFrozenGraph(t *testing.T) map[string]*Lineage {
	t.Helper()
	return buildGraph(t, P("A"), P("B"), P("M", "A", "B"), P("D", "M"))
}

// TestSnapshotFreezesAcrossLaterGraphEdits is the headline independence
// guarantee: after a snapshot is returned, the caller edits the original graph
// directly — replacing a name inside a stored upstream list and deleting a
// node — yet the already returned snapshot keeps the content identifier and
// the complete, byte-identical export it had at creation, with every original
// node and relation. Re-reading those bytes by the existing rules yields a
// legal snapshot whose content identifier matches its graph.
func TestSnapshotFreezesAcrossLaterGraphEdits(t *testing.T) {
	graph := snapshotFrozenGraph(t)

	snap, err := BuildSnapshot(graph)
	if err != nil {
		t.Fatalf("BuildSnapshot: %v", err)
	}
	wantID := snap.ContentID
	wantBytes := mustMarshalSnapshot(t, snap)

	// Edit the original graph directly, exactly as a holder of the map would:
	// overwrite an upstream name in the stored backing array, append into that
	// same array, and then remove one of the roots.
	graph["M"].Parents[0] = "HACK"
	graph["M"].Parents = append(graph["M"].Parents, "GHOST")
	delete(graph, "A")

	if got := snap.ContentID; got != wantID {
		t.Errorf("snapshot content id drifted:\n got %s\nwant %s", got, wantID)
	}
	if got := mustMarshalSnapshot(t, snap); !reflect.DeepEqual(got, wantBytes) {
		t.Fatalf("snapshot export bytes drifted after a graph edit:\n got %s\nwant %s", got, wantBytes)
	}

	// The frozen graph still contains every original node and relation.
	wantRecords := map[string][]string{
		"A": {},
		"B": {},
		"M": {"A", "B"},
		"D": {"M"},
	}
	if got := len(snap.Graph.Datasets); got != len(wantRecords) {
		t.Fatalf("snapshot has %d records, want %d (a node was lost): %+v", got, len(wantRecords), snap.Graph.Datasets)
	}
	for name, upstreams := range wantRecords {
		if got := mustSnapshotRecord(t, snap, name).Upstreams; !reflect.DeepEqual(got, upstreams) {
			t.Errorf("snapshot record %q upstreams = %v, want frozen %v", name, got, upstreams)
		}
	}

	// The unchanged bytes still read as a legal snapshot under the existing
	// reader, whose validation includes content id matching the graph.
	parsed, err := ParseSnapshot(wantBytes)
	if err != nil {
		t.Fatalf("frozen snapshot no longer parses after the graph was edited: %v", err)
	}
	if !reflect.DeepEqual(parsed, snap) {
		t.Errorf("re-read snapshot differs from the returned snapshot:\n got %#v\nwant %#v", parsed, snap)
	}
}

// TestSnapshotTwoSnapshotsFromOneGraphAreIndependent covers the snapshot-to-
// snapshot direction: two snapshots of the same graph start identical, but
// editing one returned snapshot's upstream or dataset records must not move
// the other snapshot or the original graph. The program is not expected to
// repair the snapshot the caller itself corrupted; only the untouched objects
// must stay put.
func TestSnapshotTwoSnapshotsFromOneGraphAreIndependent(t *testing.T) {
	graph := snapshotFrozenGraph(t)

	first, err := BuildSnapshot(graph)
	if err != nil {
		t.Fatalf("first BuildSnapshot: %v", err)
	}
	other, err := BuildSnapshot(graph)
	if err != nil {
		t.Fatalf("second BuildSnapshot: %v", err)
	}
	if first.ContentID != other.ContentID {
		t.Fatalf("initial content ids differ: %s vs %s", first.ContentID, other.ContentID)
	}
	otherBytes := mustMarshalSnapshot(t, other)
	if got := mustMarshalSnapshot(t, first); !reflect.DeepEqual(got, otherBytes) {
		t.Fatalf("initial snapshot bytes differ:\n got %s\nwant %s", got, otherBytes)
	}

	// Corrupt the first snapshot the way a caller reshaping its own copy would:
	// rewrite an upstream in place, append into that record's backing array, and
	// rename a dataset record.
	iM := snapshotRecordIndex(first, "M")
	if iM < 0 {
		t.Fatalf("first snapshot missing M")
	}
	first.Graph.Datasets[iM].Upstreams[0] = "X"
	first.Graph.Datasets[iM].Upstreams = append(first.Graph.Datasets[iM].Upstreams, "Y")
	first.Graph.Datasets[snapshotRecordIndex(first, "D")].Name = "DD"

	if got := mustMarshalSnapshot(t, other); !reflect.DeepEqual(got, otherBytes) {
		t.Errorf("the second snapshot changed after editing the first:\n got %s\nwant %s", got, otherBytes)
	}
	if got := mustSnapshotRecord(t, other, "M").Upstreams; !reflect.DeepEqual(got, []string{"A", "B"}) {
		t.Errorf("second snapshot M upstreams = %v, want untouched [A B]", got)
	}
	if got := mustSnapshotRecord(t, other, "D").Name; got != "D" {
		t.Errorf("second snapshot record D was renamed to %q", got)
	}

	// The original graph is not reachable through the edited snapshot either.
	if got := graph["M"].Parents; !reflect.DeepEqual(got, []string{"A", "B"}) {
		t.Errorf("graph M.Parents = %v, want untouched [A B]", got)
	}
	if got := graph["D"].Dataset; got != "D" {
		t.Errorf("graph D node was renamed to %q", got)
	}
	if _, ok := graph["A"]; !ok {
		t.Errorf("graph lost node A after a snapshot record was edited")
	}
}

// TestSnapshotSharedUpstreamRecordsAreIndependent focuses on the alias a
// normalizer is tempted to create: when two datasets originally have the same
// direct-upstream list, each frozen record owns its own copy. Editing one
// record's list must not drag the other record along, either within the edited
// snapshot, in a second snapshot of the same graph, or in the original graph.
func TestSnapshotSharedUpstreamRecordsAreIndependent(t *testing.T) {
	// Roots A, E; both M and N depend on the same two roots; D depends on M.
	graph := buildGraph(t, P("A"), P("E"), P("M", "A", "E"), P("N", "A", "E"), P("D", "M"))

	first, err := BuildSnapshot(graph)
	if err != nil {
		t.Fatalf("first BuildSnapshot: %v", err)
	}
	other, err := BuildSnapshot(graph)
	if err != nil {
		t.Fatalf("second BuildSnapshot: %v", err)
	}
	otherBytes := mustMarshalSnapshot(t, other)

	iM := snapshotRecordIndex(first, "M")
	iN := snapshotRecordIndex(first, "N")
	if iM < 0 || iN < 0 {
		t.Fatalf("snapshot missing records: M=%d N=%d", iM, iN)
	}
	if got := first.Graph.Datasets[iM].Upstreams; !reflect.DeepEqual(got, []string{"A", "E"}) {
		t.Fatalf("M upstreams = %v, want [A E]", got)
	}
	if got := first.Graph.Datasets[iN].Upstreams; !reflect.DeepEqual(got, []string{"A", "E"}) {
		t.Fatalf("N upstreams = %v, want [A E]", got)
	}

	// Edit only M's frozen list, in place and via append.
	first.Graph.Datasets[iM].Upstreams[0] = "X"
	first.Graph.Datasets[iM].Upstreams = append(first.Graph.Datasets[iM].Upstreams, "Y")

	if got := first.Graph.Datasets[iN].Upstreams; !reflect.DeepEqual(got, []string{"A", "E"}) {
		t.Errorf("N record followed M's edit: %v, want untouched [A E]", got)
	}
	if got := mustSnapshotRecord(t, first, "D").Upstreams; !reflect.DeepEqual(got, []string{"M"}) {
		t.Errorf("D record changed: %v, want untouched [M]", got)
	}
	if got := mustMarshalSnapshot(t, other); !reflect.DeepEqual(got, otherBytes) {
		t.Errorf("the second snapshot changed:\n got %s\nwant %s", got, otherBytes)
	}
	if got := mustSnapshotRecord(t, other, "N").Upstreams; !reflect.DeepEqual(got, []string{"A", "E"}) {
		t.Errorf("second snapshot N upstreams = %v, want untouched [A E]", got)
	}
	if got := graph["N"].Parents; !reflect.DeepEqual(got, []string{"A", "E"}) {
		t.Errorf("graph N.Parents = %v, want untouched [A E]", got)
	}
	if got := graph["M"].Parents; !reflect.DeepEqual(got, []string{"A", "E"}) {
		t.Errorf("graph M.Parents = %v, want untouched [A E]", got)
	}
}

// TestSnapshotBuildIsReadOnlyNormalizesOnlyInSnapshot pins the read-only
// contract: creating a snapshot never rewrites the caller's graph, so the
// original upstream order and duplicate entries survive verbatim even though
// the snapshot itself sorts and deduplicates them. Roots export their
// upstreams as [] and an empty graph exports its datasets as [].
func TestSnapshotBuildIsReadOnlyNormalizesOnlyInSnapshot(t *testing.T) {
	// A hand-built graph deliberately stores M's parents shuffled and
	// duplicated, without taking them through Register's normalization.
	graph := map[string]*Lineage{
		"A": {Dataset: "A"},
		"E": {Dataset: "E"},
		"M": {Dataset: "M", Parents: []string{"E", "A", "E", "A"}},
	}
	snap, err := BuildSnapshot(graph)
	if err != nil {
		t.Fatalf("BuildSnapshot: %v", err)
	}

	if got := graph["M"].Parents; !reflect.DeepEqual(got, []string{"E", "A", "E", "A"}) {
		t.Errorf("BuildSnapshot rewrote the graph's stored parents: %v, want verbatim [E A E A]", got)
	}
	if got := mustSnapshotRecord(t, snap, "M").Upstreams; !reflect.DeepEqual(got, []string{"A", "E"}) {
		t.Errorf("snapshot M upstreams = %v, want sorted and deduped [A E]", got)
	}
	rootRecord := mustSnapshotRecord(t, snap, "A")
	if rootRecord.Upstreams == nil || len(rootRecord.Upstreams) != 0 {
		t.Errorf("root A upstreams = %v, want non-nil empty slice", rootRecord.Upstreams)
	}
	if text := string(mustMarshalSnapshot(t, snap)); !strings.Contains(text, `"upstreams": []`) {
		t.Errorf("snapshot does not export a root's upstreams as []:\n%s", text)
	}

	empty, err := BuildSnapshot(map[string]*Lineage{})
	if err != nil {
		t.Fatalf("BuildSnapshot on empty graph: %v", err)
	}
	if len(empty.Graph.Datasets) != 0 {
		t.Errorf("empty graph snapshot has datasets %v", empty.Graph.Datasets)
	}
	if text := string(mustMarshalSnapshot(t, empty)); !strings.Contains(text, `"datasets": []`) {
		t.Errorf("empty graph snapshot does not export datasets as []:\n%s", text)
	}
	if _, err := ParseSnapshot(mustMarshalSnapshot(t, empty)); err != nil {
		t.Errorf("empty graph snapshot failed to parse: %v", err)
	}
}

// TestSnapshotOldSnapshotSurvivesLaterCorruptGraphAndRebuildFails covers the
// failure boundary: a legal snapshot created before the graph gained a missing
// upstream still exports and parses afterwards, while building a fresh
// snapshot from the now-corrupt graph fails by the existing rule, returns no
// snapshot, and changes neither the graph nor the previously saved snapshot.
func TestSnapshotOldSnapshotSurvivesLaterCorruptGraphAndRebuildFails(t *testing.T) {
	graph := buildGraph(t, P("A"), P("B"), P("M", "A", "B"))

	saved, err := BuildSnapshot(graph)
	if err != nil {
		t.Fatalf("initial BuildSnapshot: %v", err)
	}
	savedBytes := mustMarshalSnapshot(t, saved)

	// The caller corrupts the graph directly: M now points at an upstream that
	// is not registered.
	graph["M"].Parents = []string{"GHOST"}
	corruptDump := dump(graph)

	rebuilt, err := BuildSnapshot(graph)
	if err == nil {
		t.Fatalf("BuildSnapshot on a graph with a missing upstream returned %v, want error", rebuilt)
	}
	if !errors.Is(err, ErrNotFound) {
		t.Errorf("err = %v, want ErrNotFound", err)
	}
	if rebuilt != nil {
		t.Errorf("failed BuildSnapshot returned a usable snapshot: %#v", rebuilt)
	}
	if got := dump(graph); got != corruptDump {
		t.Errorf("failed BuildSnapshot mutated the graph:\n got %s\nwant %s", got, corruptDump)
	}

	// The previously saved snapshot still exports byte-identically and reads
	// back as a legal snapshot with the same content identifier.
	if got := mustMarshalSnapshot(t, saved); !reflect.DeepEqual(got, savedBytes) {
		t.Errorf("saved snapshot bytes changed after the corrupted rebuild:\n got %s\nwant %s", got, savedBytes)
	}
	parsed, err := ParseSnapshot(savedBytes)
	if err != nil {
		t.Fatalf("previously saved snapshot no longer parses: %v", err)
	}
	if parsed.ContentID != saved.ContentID {
		t.Errorf("saved content id = %s, want %s", parsed.ContentID, saved.ContentID)
	}
	if got := mustSnapshotRecord(t, saved, "M").Upstreams; !reflect.DeepEqual(got, []string{"A", "B"}) {
		t.Errorf("saved snapshot M upstreams = %v, want frozen [A B]", got)
	}
}

// TestSnapshotNamesPreservedVerbatim pins name fidelity: dataset names are
// frozen exactly as registered, so letter casing and surrounding whitespace
// keep distinguishing datasets in both the records and the serialized bytes.
func TestSnapshotNamesPreservedVerbatim(t *testing.T) {
	graph := buildGraph(t,
		P("A"),
		P("a", "A"),
		P(" a ", "A"),
	)
	snap, err := BuildSnapshot(graph)
	if err != nil {
		t.Fatalf("BuildSnapshot: %v", err)
	}
	for _, name := range []string{"A", "a", " a "} {
		if snapshotRecordIndex(snap, name) < 0 {
			t.Errorf("snapshot lost or rewrote dataset name %q", name)
		}
	}
	if got := mustSnapshotRecord(t, snap, "a").Upstreams; !reflect.DeepEqual(got, []string{"A"}) {
		t.Errorf("record \"a\" upstreams = %v, want [A]", got)
	}
	if got := mustSnapshotRecord(t, snap, " a ").Upstreams; !reflect.DeepEqual(got, []string{"A"}) {
		t.Errorf("record \" a \" upstreams = %v, want [A]", got)
	}
	if text := string(mustMarshalSnapshot(t, snap)); !strings.Contains(text, `"name": " a "`) {
		t.Errorf("snapshot export does not preserve the spaced name verbatim:\n%s", text)
	}

	// The three name spellings address different content, even though they
	// would collide under case/space-insensitive matching.
	ids := map[string]bool{
		snapshotOf(t, `{"datasets":[{"name":"A","upstreams":[]}]}`).ContentID:   true,
		snapshotOf(t, `{"datasets":[{"name":"a","upstreams":[]}]}`).ContentID:   true,
		snapshotOf(t, `{"datasets":[{"name":" A ","upstreams":[]}]}`).ContentID: true,
	}
	if len(ids) != 3 {
		t.Errorf("cased/spaced names share content ids: %v", ids)
	}
}
