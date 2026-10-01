package chainledger

import (
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
)

// snapshotOf builds a snapshot from name -> parents pairs, asserting success.
func snapshotOf(t *testing.T, pairs ...struct {
	name    string
	parents []string
}) Snapshot {
	t.Helper()
	graph := map[string]*Lineage{}
	for _, p := range pairs {
		if err := Register(graph, Dataset{Name: p.name}, p.parents); err != nil {
			t.Fatalf("Register(%q, %v) unexpected error: %v", p.name, p.parents, err)
		}
	}
	snap, err := BuildSnapshot(graph)
	if err != nil {
		t.Fatalf("BuildSnapshot unexpected error: %v", err)
	}
	return snap
}

// TestSnapshotContentIDDeterminism verifies that semantically equivalent graphs
// produce the same content identifier and the same snapshot bytes regardless of
// record order, upstream order, duplicate upstreams, or JSON whitespace.
func TestSnapshotContentIDDeterminism(t *testing.T) {
	graph := buildGraph(t, P("A"), P("B", "A"), P("C", "B", "A"))

	snap1, err := BuildSnapshot(graph)
	if err != nil {
		t.Fatalf("BuildSnapshot unexpected error: %v", err)
	}
	data1, err := MarshalSnapshot(snap1)
	if err != nil {
		t.Fatalf("MarshalSnapshot unexpected error: %v", err)
	}

	// Re-parse with shuffled record order, shuffled upstream order, duplicates,
	// and different whitespace.
	variants := []string{
		`{"datasets":[{"name":"C","upstreams":["A","B","A"]},{"name":"B","upstreams":["A"]},{"name":"A","upstreams":[]}]}`,
		`{"datasets":[{"name":"A","upstreams":[]},{"name":"B","upstreams":["A","A"]},{"name":"C","upstreams":["B","A","B"]}]}`,
		`{ "datasets" : [ { "name" : "C" , "upstreams" : [ "B" , "A" ] } , { "name" : "B" , "upstreams" : [ "A" ] } , { "name" : "A" , "upstreams" : [ ] } ] }`,
	}
	for i, variant := range variants {
		parsed, err := UnmarshalGraphFile([]byte(variant))
		if err != nil {
			t.Fatalf("variant %d: UnmarshalGraphFile unexpected error: %v", i, err)
		}
		snap2, err := BuildSnapshot(parsed)
		if err != nil {
			t.Fatalf("variant %d: BuildSnapshot unexpected error: %v", i, err)
		}
		if snap2.ContentID != snap1.ContentID {
			t.Errorf("variant %d: contentId = %q, want %q (semantically equivalent graphs must match)", i, snap2.ContentID, snap1.ContentID)
		}
		data2, err := MarshalSnapshot(snap2)
		if err != nil {
			t.Fatalf("variant %d: MarshalSnapshot unexpected error: %v", i, err)
		}
		if string(data2) != string(data1) {
			t.Errorf("variant %d: snapshot bytes differ:\n got %s\nwant %s", i, data2, data1)
		}
	}

	// Different semantics must produce a different identifier.
	other := buildGraph(t, P("A"), P("B", "A"))
	snapOther, err := BuildSnapshot(other)
	if err != nil {
		t.Fatalf("BuildSnapshot other unexpected error: %v", err)
	}
	if snapOther.ContentID == snap1.ContentID {
		t.Errorf("different graphs share contentId %q", snap1.ContentID)
	}
}

// TestSnapshotRoundTrip verifies that a snapshot marshaled to JSON and parsed
// back is equivalent, and that the graph is canonical on disk.
func TestSnapshotRoundTrip(t *testing.T) {
	snap := snapshotOf(t, P("A"), P("B", "A"), P("C", "B"))
	data, err := MarshalSnapshot(snap)
	if err != nil {
		t.Fatalf("MarshalSnapshot unexpected error: %v", err)
	}
	parsed, err := UnmarshalSnapshot(data)
	if err != nil {
		t.Fatalf("UnmarshalSnapshot unexpected error: %v", err)
	}
	if !reflect.DeepEqual(parsed, snap) {
		t.Fatalf("round-trip mismatch:\n got %#v\nwant %#v", parsed, snap)
	}
	// The on-disk graph must be canonical: datasets sorted by name, upstreams
	// sorted and deduplicated.
	var onDisk struct {
		Graph GraphFile `json:"graph"`
	}
	if err := json.Unmarshal(data, &onDisk); err != nil {
		t.Fatalf("unmarshal on-disk snapshot: %v", err)
	}
	wantGraph := GraphFile{Datasets: []GraphDataset{
		{Name: "A", Upstreams: []string{}},
		{Name: "B", Upstreams: []string{"A"}},
		{Name: "C", Upstreams: []string{"B"}},
	}}
	if !reflect.DeepEqual(onDisk.Graph, wantGraph) {
		t.Errorf("on-disk graph = %#v, want %#v", onDisk.Graph, wantGraph)
	}
}

// TestSnapshotEmptyGraph verifies that an empty graph can be snapshotted and
// compared, and encodes datasets as [].
func TestSnapshotEmptyGraph(t *testing.T) {
	graph := map[string]*Lineage{}
	snap, err := BuildSnapshot(graph)
	if err != nil {
		t.Fatalf("BuildSnapshot empty unexpected error: %v", err)
	}
	data, err := MarshalSnapshot(snap)
	if err != nil {
		t.Fatalf("MarshalSnapshot empty unexpected error: %v", err)
	}
	if !strings.Contains(string(data), `"datasets": []`) {
		t.Errorf("empty snapshot JSON = %s, want datasets: []", data)
	}
	parsed, err := UnmarshalSnapshot(data)
	if err != nil {
		t.Fatalf("UnmarshalSnapshot empty unexpected error: %v", err)
	}
	report, err := CompareSnapshots(parsed, parsed)
	if err != nil {
		t.Fatalf("CompareSnapshots self unexpected error: %v", err)
	}
	if len(report.AddedDatasets) != 0 || len(report.DeletedDatasets) != 0 ||
		len(report.ChangedDatasets) != 0 || len(report.AddedRelations) != 0 ||
		len(report.RemovedRelations) != 0 || len(report.RootSourceChanges) != 0 {
		t.Errorf("self-compare of empty snapshot produced non-empty report: %+v", report)
	}
}

// TestSnapshotDoesNotMutateGraph verifies that building a snapshot leaves the
// source graph untouched.
func TestSnapshotDoesNotMutateGraph(t *testing.T) {
	graph := buildGraph(t, P("A"), P("B", "A"))
	before := dump(graph)
	if _, err := BuildSnapshot(graph); err != nil {
		t.Fatalf("BuildSnapshot unexpected error: %v", err)
	}
	if got := dump(graph); got != before {
		t.Fatalf("BuildSnapshot mutated graph:\n got %s\nwant %s", got, before)
	}
}

// TestSnapshotRejectsInvalidFiles verifies that UnmarshalSnapshot rejects
// invalid JSON, missing required fields, unsupported versions, mismatched
// identifiers, and structurally invalid graphs.
func TestSnapshotRejectsInvalidFiles(t *testing.T) {
	valid := snapshotOf(t, P("A"), P("B", "A"))
	validData, err := MarshalSnapshot(valid)
	if err != nil {
		t.Fatalf("MarshalSnapshot unexpected error: %v", err)
	}

	cases := map[string]string{
		"invalid json":           `{not json`,
		"missing formatVersion":  `{"contentId":"x","graph":{"datasets":[]}}`,
		"missing contentId":      `{"formatVersion":1,"graph":{"datasets":[]}}`,
		"missing graph":          `{"formatVersion":1,"contentId":"x"}`,
		"unsupported version":    `{"formatVersion":2,"contentId":"x","graph":{"datasets":[]}}`,
		"contentId mismatch":     `{"formatVersion":1,"contentId":"deadbeef","graph":{"datasets":[]}}`,
		"graph empty name":       `{"formatVersion":1,"contentId":"x","graph":{"datasets":[{"name":"","upstreams":[]}]}}`,
		"graph duplicate node":   `{"formatVersion":1,"contentId":"x","graph":{"datasets":[{"name":"A","upstreams":[]},{"name":"A","upstreams":[]}]}}`,
		"graph missing upstream": `{"formatVersion":1,"contentId":"x","graph":{"datasets":[{"name":"A","upstreams":["ghost"]}]}}`,
		"graph cycle":            `{"formatVersion":1,"contentId":"x","graph":{"datasets":[{"name":"A","upstreams":["B"]},{"name":"B","upstreams":["A"]}]}}`,
	}
	for name, data := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := UnmarshalSnapshot([]byte(data)); err == nil {
				t.Fatalf("expected error for %s, got nil", name)
			}
		})
	}

	// A valid snapshot must parse.
	if _, err := UnmarshalSnapshot(validData); err != nil {
		t.Fatalf("valid snapshot rejected: %v", err)
	}
}

// TestMarshalSnapshotRejectsMismatchedID verifies that MarshalSnapshot refuses
// to write a snapshot whose content identifier does not match its graph.
func TestMarshalSnapshotRejectsMismatchedID(t *testing.T) {
	snap := snapshotOf(t, P("A"))
	snap.ContentID = "deadbeef"
	if _, err := MarshalSnapshot(snap); err == nil {
		t.Fatal("MarshalSnapshot accepted a mismatched contentId")
	}
}

// TestCompareBasic verifies the added/deleted/changed/relation lists.
func TestCompareBasic(t *testing.T) {
	old := snapshotOf(t, P("A"), P("B", "A"), P("C", "B"))
	// New: A is deleted; B becomes a root (edge A-B removed); C still depends
	// on B; D is new and depends on B.
	new := snapshotOf(t, P("B"), P("C", "B"), P("D", "B"))

	report, err := CompareSnapshots(old, new)
	if err != nil {
		t.Fatalf("CompareSnapshots unexpected error: %v", err)
	}
	if want := []string{"D"}; !reflect.DeepEqual(report.AddedDatasets, want) {
		t.Errorf("AddedDatasets = %v, want %v", report.AddedDatasets, want)
	}
	if want := []string{"A"}; !reflect.DeepEqual(report.DeletedDatasets, want) {
		t.Errorf("DeletedDatasets = %v, want %v", report.DeletedDatasets, want)
	}
	if want := []string{"B"}; !reflect.DeepEqual(report.ChangedDatasets, want) {
		t.Errorf("ChangedDatasets = %v, want %v", report.ChangedDatasets, want)
	}
	wantAdded := []Relation{
		{Upstream: "B", Downstream: "D"},
	}
	if !reflect.DeepEqual(report.AddedRelations, wantAdded) {
		t.Errorf("AddedRelations = %v, want %v", report.AddedRelations, wantAdded)
	}
	wantRemoved := []Relation{
		{Upstream: "A", Downstream: "B"}, // edge removed with deleted node A
	}
	if !reflect.DeepEqual(report.RemovedRelations, wantRemoved) {
		t.Errorf("RemovedRelations = %v, want %v", report.RemovedRelations, wantRemoved)
	}
}

// TestCompareRootSources is the headline scenario: A is the root of B, C
// depends on B. Repoint B at another root X. Both B and C must report their
// source changing from A to X. Adjusting the path while reaching the same roots
// must not be reported.
func TestCompareRootSources(t *testing.T) {
	old := snapshotOf(t, P("A"), P("B", "A"), P("C", "B"))
	// B now depends on X (a different root); C still depends on B.
	new := snapshotOf(t, P("X"), P("B", "X"), P("C", "B"))

	report, err := CompareSnapshots(old, new)
	if err != nil {
		t.Fatalf("CompareSnapshots unexpected error: %v", err)
	}
	wantChanges := []RootSourceChange{
		{Dataset: "B", OldRoots: []string{"A"}, NewRoots: []string{"X"}},
		{Dataset: "C", OldRoots: []string{"A"}, NewRoots: []string{"X"}},
	}
	if !reflect.DeepEqual(report.RootSourceChanges, wantChanges) {
		t.Errorf("RootSourceChanges = %v, want %v", report.RootSourceChanges, wantChanges)
	}
	// B's direct upstreams changed too; C's did not.
	if want := []string{"B"}; !reflect.DeepEqual(report.ChangedDatasets, want) {
		t.Errorf("ChangedDatasets = %v, want %v", report.ChangedDatasets, want)
	}
}

// TestCompareRootSourcesPathOnlyChange verifies that repointing a dataset to a
// longer path that reaches the same roots is not a source change.
func TestCompareRootSourcesPathOnlyChange(t *testing.T) {
	old := snapshotOf(t, P("A"), P("B", "A"), P("C", "B"))
	// C now depends on A directly instead of B; the reachable root is still A.
	new := snapshotOf(t, P("A"), P("B", "A"), P("C", "A"))

	report, err := CompareSnapshots(old, new)
	if err != nil {
		t.Fatalf("CompareSnapshots unexpected error: %v", err)
	}
	if len(report.RootSourceChanges) != 0 {
		t.Errorf("path-only change reported as source change: %+v", report.RootSourceChanges)
	}
	if want := []string{"C"}; !reflect.DeepEqual(report.ChangedDatasets, want) {
		t.Errorf("ChangedDatasets = %v, want %v", report.ChangedDatasets, want)
	}
}

// TestCompareRootSourcesMultiplePaths verifies that multiple paths to the same
// root count once, and that a dataset reaching two roots reports both.
func TestCompareRootSourcesMultiplePaths(t *testing.T) {
	// A, B roots; C depends on A and B; D depends on C.
	old := snapshotOf(t, P("A"), P("B"), P("C", "A", "B"), P("D", "C"))
	// C drops B; now only reaches A.
	new := snapshotOf(t, P("A"), P("B"), P("C", "A"), P("D", "C"))

	report, err := CompareSnapshots(old, new)
	if err != nil {
		t.Fatalf("CompareSnapshots unexpected error: %v", err)
	}
	wantChanges := []RootSourceChange{
		{Dataset: "C", OldRoots: []string{"A", "B"}, NewRoots: []string{"A"}},
		{Dataset: "D", OldRoots: []string{"A", "B"}, NewRoots: []string{"A"}},
	}
	if !reflect.DeepEqual(report.RootSourceChanges, wantChanges) {
		t.Errorf("RootSourceChanges = %v, want %v", report.RootSourceChanges, wantChanges)
	}
}

// TestCompareSelfAndSameContent verifies that comparing a snapshot with itself
// yields all-empty lists, and that two snapshots of the same content compare
// identically.
func TestCompareSelfAndSameContent(t *testing.T) {
	snap := snapshotOf(t, P("A"), P("B", "A"), P("C", "B"))
	report, err := CompareSnapshots(snap, snap)
	if err != nil {
		t.Fatalf("CompareSnapshots self unexpected error: %v", err)
	}
	if len(report.AddedDatasets) != 0 || len(report.DeletedDatasets) != 0 ||
		len(report.ChangedDatasets) != 0 || len(report.AddedRelations) != 0 ||
		len(report.RemovedRelations) != 0 || len(report.RootSourceChanges) != 0 {
		t.Errorf("self-compare produced non-empty report: %+v", report)
	}

	// Same content, rebuilt from a differently-ordered graph (shuffled records
	// and upstreams with duplicates).
	otherGraph, err := UnmarshalGraphFile([]byte(`{"datasets":[{"name":"C","upstreams":["B","B"]},{"name":"A","upstreams":[]},{"name":"B","upstreams":["A"]}]}`))
	if err != nil {
		t.Fatalf("UnmarshalGraphFile shuffled unexpected error: %v", err)
	}
	other, err := BuildSnapshot(otherGraph)
	if err != nil {
		t.Fatalf("BuildSnapshot other unexpected error: %v", err)
	}
	report2, err := CompareSnapshots(snap, other)
	if err != nil {
		t.Fatalf("CompareSnapshots same-content unexpected error: %v", err)
	}
	if len(report2.AddedDatasets) != 0 || len(report2.DeletedDatasets) != 0 ||
		len(report2.ChangedDatasets) != 0 || len(report2.AddedRelations) != 0 ||
		len(report2.RemovedRelations) != 0 || len(report2.RootSourceChanges) != 0 {
		t.Errorf("same-content compare produced non-empty report: %+v", report2)
	}
}

// TestCompareDirection verifies that compare is old-to-new: swapping the
// arguments swaps added/deleted and the root change direction.
func TestCompareDirection(t *testing.T) {
	old := snapshotOf(t, P("A"), P("B", "A"))
	new := snapshotOf(t, P("X"), P("B", "X"))

	forward, err := CompareSnapshots(old, new)
	if err != nil {
		t.Fatalf("forward compare unexpected error: %v", err)
	}
	backward, err := CompareSnapshots(new, old)
	if err != nil {
		t.Fatalf("backward compare unexpected error: %v", err)
	}
	if !reflect.DeepEqual(forward.AddedDatasets, backward.DeletedDatasets) {
		t.Errorf("direction asymmetry: forward.Added=%v backward.Deleted=%v", forward.AddedDatasets, backward.DeletedDatasets)
	}
	if !reflect.DeepEqual(forward.DeletedDatasets, backward.AddedDatasets) {
		t.Errorf("direction asymmetry: forward.Deleted=%v backward.Added=%v", forward.DeletedDatasets, backward.AddedDatasets)
	}
	// Root change direction must flip.
	if len(forward.RootSourceChanges) != 1 || len(backward.RootSourceChanges) != 1 {
		t.Fatalf("expected one root change each, got forward=%d backward=%d", len(forward.RootSourceChanges), len(backward.RootSourceChanges))
	}
	f := forward.RootSourceChanges[0]
	b := backward.RootSourceChanges[0]
	if f.Dataset != b.Dataset || !reflect.DeepEqual(f.OldRoots, b.NewRoots) || !reflect.DeepEqual(f.NewRoots, b.OldRoots) {
		t.Errorf("root change direction did not flip: forward=%+v backward=%+v", f, b)
	}
}

// TestCompareDeterminism verifies that semantically identical comparisons
// produce byte-identical reports.
func TestCompareDeterminism(t *testing.T) {
	old := snapshotOf(t, P("A"), P("B", "A"), P("C", "B"))
	new := snapshotOf(t, P("X"), P("B", "X"), P("C", "B"))

	r1, err := CompareSnapshots(old, new)
	if err != nil {
		t.Fatalf("CompareSnapshots r1 unexpected error: %v", err)
	}
	r2, err := CompareSnapshots(old, new)
	if err != nil {
		t.Fatalf("CompareSnapshots r2 unexpected error: %v", err)
	}
	b1, err := json.Marshal(r1)
	if err != nil {
		t.Fatalf("marshal r1: %v", err)
	}
	b2, err := json.Marshal(r2)
	if err != nil {
		t.Fatalf("marshal r2: %v", err)
	}
	if string(b1) != string(b2) {
		t.Fatalf("compare report bytes differ:\n--- r1 ---\n%s\n--- r2 ---\n%s", b1, b2)
	}
}

// TestSnapshotPreservesNames verifies that name casing and spaces are preserved.
func TestSnapshotPreservesNames(t *testing.T) {
	graph := map[string]*Lineage{}
	mustRegister(t, graph, " Raw-Blocks ")
	mustRegister(t, graph, "RAW-BLOCKS", " Raw-Blocks ")
	snap, err := BuildSnapshot(graph)
	if err != nil {
		t.Fatalf("BuildSnapshot unexpected error: %v", err)
	}
	data, err := MarshalSnapshot(snap)
	if err != nil {
		t.Fatalf("MarshalSnapshot unexpected error: %v", err)
	}
	parsed, err := UnmarshalSnapshot(data)
	if err != nil {
		t.Fatalf("UnmarshalSnapshot unexpected error: %v", err)
	}
	adj, err := graphFileAdjacency(parsed.Graph)
	if err != nil {
		t.Fatalf("graphFileAdjacency unexpected error: %v", err)
	}
	if got := adj["RAW-BLOCKS"]; !reflect.DeepEqual(got, []string{" Raw-Blocks "}) {
		t.Fatalf("name was trimmed or case-folded: %v", got)
	}
}

// TestSnapshotErrorsAreSentinel verifies that snapshot rejections use the
// package sentinel errors so callers can recognize them.
func TestSnapshotErrorsAreSentinel(t *testing.T) {
	cases := map[string]string{
		"missing field":    `{"formatVersion":1,"contentId":"x"}`,
		"bad version":      `{"formatVersion":2,"contentId":"x","graph":{"datasets":[]}}`,
		"mismatched id":    `{"formatVersion":1,"contentId":"deadbeef","graph":{"datasets":[]}}`,
		"empty name":       `{"formatVersion":1,"contentId":"x","graph":{"datasets":[{"name":"","upstreams":[]}]}}`,
		"missing upstream": `{"formatVersion":1,"contentId":"x","graph":{"datasets":[{"name":"A","upstreams":["ghost"]}]}}`,
	}
	for name, data := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := UnmarshalSnapshot([]byte(data))
			if !errors.Is(err, ErrInvalidArgument) && !errors.Is(err, ErrNotFound) {
				t.Fatalf("err = %v, want ErrInvalidArgument or ErrNotFound", err)
			}
		})
	}
}
