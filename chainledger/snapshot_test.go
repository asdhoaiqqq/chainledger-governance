package chainledger

import (
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
)

// graphFromJSON parses a graph document, failing the test on error.
func graphFromJSON(t *testing.T, data string) map[string]*Lineage {
	t.Helper()
	graph, err := UnmarshalGraphFile([]byte(data))
	if err != nil {
		t.Fatalf("UnmarshalGraphFile(%q): %v", data, err)
	}
	return graph
}

func snapshotOf(t *testing.T, graphJSON string) *SnapshotFile {
	t.Helper()
	snap, err := BuildSnapshot(graphFromJSON(t, graphJSON))
	if err != nil {
		t.Fatalf("BuildSnapshot: %v", err)
	}
	return snap
}

// TestSnapshotDeterministicBytes is the core content-addressing guarantee:
// record order, upstream order, duplicate upstreams, and JSON whitespace must
// not influence either the content identifier or the serialized snapshot
// bytes, as long as the graph semantics are equal.
func TestSnapshotDeterministicBytes(t *testing.T) {
	inputs := []string{
		`{"datasets":[{"name":"A","upstreams":[]},{"name":"B","upstreams":["A","A"]},{"name":"C","upstreams":["B","A"]}]}`,
		`{ "datasets": [ {"upstreams": ["A","B","A"],"name":"C"}, {"upstreams":[],"name":"A"}, {"name":"B","upstreams":["A"]} ] }`,
		"{\"datasets\":[\n  {\"name\":\"C\",\"upstreams\":[\"B\",\"A\"]},\n  {\"name\":\"A\",\"upstreams\":[]},\n  {\"name\":\"B\",\"upstreams\":[\"A\"]}\n]}\n",
	}
	var wantID string
	var wantBytes []byte
	for i, in := range inputs {
		snap := snapshotOf(t, in)
		got, err := MarshalSnapshot(snap)
		if err != nil {
			t.Fatalf("MarshalSnapshot input %d: %v", i, err)
		}
		if i == 0 {
			wantID = snap.ContentID
			wantBytes = got
			continue
		}
		if snap.ContentID != wantID {
			t.Errorf("input %d content id = %s, want %s", i, snap.ContentID, wantID)
		}
		if string(got) != string(wantBytes) {
			t.Errorf("input %d snapshot bytes differ:\n got %s\nwant %s", i, got, wantBytes)
		}
	}
}

func TestSnapshotDifferentSemanticsDifferentID(t *testing.T) {
	a := snapshotOf(t, `{"datasets":[{"name":"A","upstreams":[]}]}`)
	b := snapshotOf(t, `{"datasets":[{"name":"B","upstreams":[]}]}`)
	c := snapshotOf(t, `{"datasets":[]}`)
	ids := map[string]bool{a.ContentID: true, b.ContentID: true, c.ContentID: true}
	if len(ids) != 3 {
		t.Errorf("distinct graphs share content ids: %s %s %s", a.ContentID, b.ContentID, c.ContentID)
	}
	if !strings.HasPrefix(a.ContentID, contentIDPrefix) {
		t.Errorf("content id %q missing prefix %q", a.ContentID, contentIDPrefix)
	}
}

func TestSnapshotEmptyGraph(t *testing.T) {
	snap := snapshotOf(t, `{"datasets":[]}`)
	if snap.FormatVersion != SnapshotFormatVersion {
		t.Errorf("formatVersion = %d, want %d", snap.FormatVersion, SnapshotFormatVersion)
	}
	if len(snap.Graph.Datasets) != 0 {
		t.Errorf("empty graph produced datasets %v", snap.Graph.Datasets)
	}
	data, err := MarshalSnapshot(snap)
	if err != nil {
		t.Fatalf("MarshalSnapshot: %v", err)
	}
	parsed, err := ParseSnapshot(data)
	if err != nil {
		t.Fatalf("empty snapshot failed to parse: %v", err)
	}
	if parsed.ContentID != snap.ContentID {
		t.Errorf("round-trip content id = %s, want %s", parsed.ContentID, snap.ContentID)
	}
	if !strings.Contains(string(data), `"datasets": []`) {
		t.Errorf("empty graph must encode datasets as [], got %s", data)
	}
}

func TestSnapshotRoundTrip(t *testing.T) {
	snap := snapshotOf(t, `{"datasets":[{"name":"A","upstreams":[]},{"name":"B","upstreams":["A"]},{"name":"C","upstreams":["A","B"]}]}`)
	data, err := MarshalSnapshot(snap)
	if err != nil {
		t.Fatalf("MarshalSnapshot: %v", err)
	}
	parsed, err := ParseSnapshot(data)
	if err != nil {
		t.Fatalf("ParseSnapshot: %v", err)
	}
	if !reflect.DeepEqual(parsed, snap) {
		t.Fatalf("round-trip mismatch:\n got %#v\nwant %#v", parsed, snap)
	}
}

func TestBuildSnapshotRejectsInvalidGraph(t *testing.T) {
	graph := map[string]*Lineage{"A": {Dataset: "A", Parents: []string{"ghost"}}}
	if _, err := BuildSnapshot(graph); !errors.Is(err, ErrNotFound) {
		t.Fatalf("err = %v, want ErrNotFound", err)
	}
	var nilGraph map[string]*Lineage
	if _, err := BuildSnapshot(nilGraph); !errors.Is(err, ErrNotInitialized) {
		t.Fatalf("nil graph err = %v, want ErrNotInitialized", err)
	}
}

func TestParseSnapshotRejections(t *testing.T) {
	good := snapshotOf(t, `{"datasets":[{"name":"A","upstreams":[]},{"name":"B","upstreams":["A"]}]}`)
	goodBytes, err := MarshalSnapshot(good)
	if err != nil {
		t.Fatalf("MarshalSnapshot: %v", err)
	}

	cases := map[string]string{
		"invalid json":           `{not json`,
		"missing all fields":     `{}`,
		"missing contentId":      `{"formatVersion":1,"graph":{"datasets":[]}}`,
		"missing graph":          `{"formatVersion":1,"contentId":"sha256:abc"}`,
		"missing formatVersion":  `{"contentId":"sha256:abc","graph":{"datasets":[]}}`,
		"null formatVersion":     `{"formatVersion":null,"contentId":"sha256:abc","graph":{"datasets":[]}}`,
		"null contentId":         `{"formatVersion":1,"contentId":null,"graph":{"datasets":[]}}`,
		"null graph":             `{"formatVersion":1,"contentId":"sha256:abc","graph":null}`,
		"unsupported version":    `{"formatVersion":2,"contentId":"sha256:abc","graph":{"datasets":[]}}`,
		"version wrong type":     `{"formatVersion":"1","contentId":"sha256:abc","graph":{"datasets":[]}}`,
		"empty contentId":        `{"formatVersion":1,"contentId":"","graph":{"datasets":[]}}`,
		"wrong content id":       strings.Replace(string(goodBytes), good.ContentID, "sha256:0000000000000000000000000000000000000000000000000000000000000000", 1),
		"graph empty name":       `{"formatVersion":1,"contentId":"x","graph":{"datasets":[{"name":"","upstreams":[]}]}}`,
		"graph duplicate node":   `{"formatVersion":1,"contentId":"x","graph":{"datasets":[{"name":"A","upstreams":[]},{"name":"A","upstreams":[]}]}}`,
		"graph missing upstream": `{"formatVersion":1,"contentId":"x","graph":{"datasets":[{"name":"A","upstreams":["ghost"]}]}}`,
		"graph cycle":            `{"formatVersion":1,"contentId":"x","graph":{"datasets":[{"name":"A","upstreams":["B"]},{"name":"B","upstreams":["A"]}]}}`,
	}
	for name, data := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := ParseSnapshot([]byte(data))
			if err == nil {
				t.Fatalf("ParseSnapshot accepted invalid snapshot for %s", name)
			}
		})
	}

	// A snapshot whose STRUCTURE is fine but whose stored content id does not
	// match its graph bytes must be rejected as tampered.
	tampered := *good
	tampered.ContentID = good.ContentID + "deadbeef"
	tb, err := MarshalSnapshot(&tampered)
	if err != nil {
		t.Fatalf("marshal tampered: %v", err)
	}
	if _, err := ParseSnapshot(tb); !errors.Is(err, ErrInvalidArgument) {
		t.Fatalf("tampered id err = %v, want ErrInvalidArgument", err)
	}
}

// TestParseSnapshotRejectsDuplicateFields: a known field declared twice within
// the same object makes the whole snapshot ambiguous and must be rejected,
// even when the duplicates carry identical values, the first is null, or the
// surviving value would pass every version and content check.
func TestParseSnapshotRejectsDuplicateFields(t *testing.T) {
	graph := `{"datasets":[{"name":"A","upstreams":[]},{"name":"B","upstreams":["A"]}]}`
	id := snapshotOf(t, graph).ContentID
	doc := func(body string) string {
		return `{"formatVersion":1,"contentId":"` + id + `","graph":` + body + `}`
	}
	// A complete, otherwise valid document; mutations below re-declare one
	// known field inside it.
	valid := doc(graph)

	cases := map[string]struct {
		data  string
		field string
	}{
		"version 2 then 1": {
			`{"formatVersion":2,"formatVersion":1,"contentId":"` + id + `","graph":` + graph + `}`,
			"formatVersion",
		},
		"identical version twice": {
			`{"formatVersion":1,"formatVersion":1,"contentId":"` + id + `","graph":` + graph + `}`,
			"formatVersion",
		},
		"contentId twice": {
			`{"formatVersion":1,"contentId":"` + id + `","contentId":"` + id + `","graph":` + graph + `}`,
			"contentId",
		},
		"null contentId then valid": {
			`{"formatVersion":1,"contentId":null,"contentId":"` + id + `","graph":` + graph + `}`,
			"contentId",
		},
		"empty graph then real graph": {
			`{"formatVersion":1,"contentId":"` + id + `","graph":{"datasets":[]},"graph":` + graph + `}`,
			"graph",
		},
		"null graph then real graph": {
			`{"formatVersion":1,"contentId":"` + id + `","graph":null,"graph":` + graph + `}`,
			"graph",
		},
		"datasets twice": {
			doc(`{"datasets":[],"datasets":[{"name":"A","upstreams":[]},{"name":"B","upstreams":["A"]}]}`),
			"datasets",
		},
		"name twice same value": {
			doc(`{"datasets":[{"name":"A","name":"A","upstreams":[]},{"name":"B","upstreams":["A"]}]}`),
			"name",
		},
		"upstreams twice": {
			doc(`{"datasets":[{"name":"A","upstreams":[]},{"name":"B","upstreams":["A"],"upstreams":["A"]}]}`),
			"upstreams",
		},
		"null upstreams then valid": {
			doc(`{"datasets":[{"name":"A","upstreams":[]},{"name":"B","upstreams":null,"upstreams":["A"]}]}`),
			"upstreams",
		},
		"escaped name key": {
			doc(`{"datasets":[{"na\u006de":"A","name":"A","upstreams":[]},{"name":"B","upstreams":["A"]}]}`),
			"name",
		},
		"case variant name": {
			doc(`{"datasets":[{"Name":"A","name":"A","upstreams":[]},{"name":"B","upstreams":["A"]}]}`),
			"name",
		},
		"case variant formatVersion": {
			`{"FormatVersion":1,"formatVersion":1,"contentId":"` + id + `","graph":` + graph + `}`,
			"formatVersion",
		},
		"case variant datasets": {
			doc(`{"Datasets":[],"datasets":[{"name":"A","upstreams":[]},{"name":"B","upstreams":["A"]}]}`),
			"datasets",
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := ParseSnapshot([]byte(tc.data))
			if !errors.Is(err, ErrInvalidArgument) {
				t.Fatalf("err = %v, want ErrInvalidArgument", err)
			}
			if !strings.Contains(err.Error(), `"`+tc.field+`"`) {
				t.Errorf("err = %q, want it to name field %q", err, tc.field)
			}
		})
	}

	// The duplicate lives in the second dataset record; the error must locate
	// it by datasets array index.
	indexed := doc(`{"datasets":[{"name":"A","upstreams":[]},{"name":"B","upstreams":["A"],"upstreams":["A"]}]}`)
	_, err := ParseSnapshot([]byte(indexed))
	if err == nil || !strings.Contains(err.Error(), "index 1") {
		t.Errorf("err = %v, want the datasets index of the offending record", err)
	}

	// Sanity: the unmutated document parses and keeps its content id.
	parsed, err := ParseSnapshot([]byte(valid))
	if err != nil {
		t.Fatalf("valid snapshot rejected: %v", err)
	}
	if parsed.ContentID != id {
		t.Errorf("content id = %s, want %s", parsed.ContentID, id)
	}
}

// TestParseSnapshotDuplicateTolerances: repetitions that do NOT create
// ambiguity stay legal — unknown fields may repeat anywhere, each dataset
// record declares its own name, and duplicate names inside one upstreams
// array are still deduplicated (that is not a repeated field).
func TestParseSnapshotDuplicateTolerances(t *testing.T) {
	graph := `{"datasets":[{"name":"A","upstreams":[]},{"name":"B","upstreams":["A"]}]}`
	id := snapshotOf(t, graph).ContentID
	doc := func(body string) string {
		return `{"formatVersion":1,"contentId":"` + id + `","graph":` + body + `}`
	}

	cases := map[string]string{
		"unknown top-level field repeats": `{"note":1,"note":2,"formatVersion":1,"contentId":"` + id + `","graph":` + graph + `}`,
		"unknown graph field repeats":     doc(`{"meta":"x","meta":"y","datasets":[{"name":"A","upstreams":[]},{"name":"B","upstreams":["A"]}]}`),
		"unknown record field repeats":    doc(`{"datasets":[{"name":"A","note":"x","note":"y","upstreams":[]},{"name":"B","upstreams":["A"]}]}`),
		"duplicate names inside upstreams array": doc(
			`{"datasets":[{"name":"A","upstreams":[]},{"name":"B","upstreams":["A","A"]}]}`),
	}
	for name, data := range cases {
		t.Run(name, func(t *testing.T) {
			parsed, err := ParseSnapshot([]byte(data))
			if err != nil {
				t.Fatalf("ParseSnapshot rejected a legal document: %v", err)
			}
			if parsed.ContentID != id {
				t.Errorf("content id = %s, want %s", parsed.ContentID, id)
			}
		})
	}
}

// TestParseSnapshotCaseVariantSpellings: a field written once in any letter
// casing the reader already supports keeps working; only declaring it twice
// is an error.
func TestParseSnapshotCaseVariantSpellings(t *testing.T) {
	id := snapshotOf(t, `{"datasets":[{"name":"A","upstreams":[]},{"name":"B","upstreams":["A"]}]}`).ContentID
	data := `{"FORMATVERSION":1,"ContentID":"` + id + `","GRAPH":{"Datasets":` +
		`[{"Name":"A","Upstreams":[]},{"NAME":"B","UPSTREAMS":["A"]}]}}`
	parsed, err := ParseSnapshot([]byte(data))
	if err != nil {
		t.Fatalf("case-variant spellings rejected: %v", err)
	}
	if parsed.ContentID != id {
		t.Errorf("content id = %s, want %s", parsed.ContentID, id)
	}
}

// TestCompareHeadlineRootSourceChange is the scenario from the product spec:
// A is a root source of B, C depends on B; repointing B at another root X must
// report BOTH B and C changing source from A to X.
func TestCompareHeadlineRootSourceChange(t *testing.T) {
	oldSnap := snapshotOf(t, `{"datasets":[{"name":"A","upstreams":[]},{"name":"B","upstreams":["A"]},{"name":"C","upstreams":["B"]}]}`)
	newSnap := snapshotOf(t, `{"datasets":[{"name":"A","upstreams":[]},{"name":"X","upstreams":[]},{"name":"B","upstreams":["X"]},{"name":"C","upstreams":["B"]}]}`)

	report := CompareSnapshots(oldSnap, newSnap)
	if got, want := report.NewDatasets, []string{"X"}; !reflect.DeepEqual(got, want) {
		t.Errorf("NewDatasets = %v, want %v", got, want)
	}
	if len(report.RemovedDatasets) != 0 {
		t.Errorf("RemovedDatasets = %v, want empty", report.RemovedDatasets)
	}
	if got, want := report.ChangedDatasets, []string{"B"}; !reflect.DeepEqual(got, want) {
		t.Errorf("ChangedDatasets = %v, want %v", got, want)
	}
	wantAdded := []Relation{{Upstream: "X", Downstream: "B"}}
	wantRemoved := []Relation{{Upstream: "A", Downstream: "B"}}
	if !reflect.DeepEqual(report.AddedRelations, wantAdded) {
		t.Errorf("AddedRelations = %v, want %v", report.AddedRelations, wantAdded)
	}
	if !reflect.DeepEqual(report.RemovedRelations, wantRemoved) {
		t.Errorf("RemovedRelations = %v, want %v", report.RemovedRelations, wantRemoved)
	}
	wantRootChanges := []RootSourceChange{
		{Dataset: "B", OldRoots: []string{"A"}, NewRoots: []string{"X"}},
		{Dataset: "C", OldRoots: []string{"A"}, NewRoots: []string{"X"}},
	}
	if !reflect.DeepEqual(report.RootSourceChanges, wantRootChanges) {
		t.Errorf("RootSourceChanges = %v\nwant %v", report.RootSourceChanges, wantRootChanges)
	}
}

// TestCompareNodeDeletionCascadesRelations verifies that removing a node lists
// every edge incident to it among the removed relations.
func TestCompareNodeDeletionCascadesRelations(t *testing.T) {
	oldSnap := snapshotOf(t, `{"datasets":[{"name":"A","upstreams":[]},{"name":"B","upstreams":["A"]},{"name":"C","upstreams":["B"]}]}`)
	// B is deleted; C now derives directly from A.
	newSnap := snapshotOf(t, `{"datasets":[{"name":"A","upstreams":[]},{"name":"C","upstreams":["A"]}]}`)

	report := CompareSnapshots(oldSnap, newSnap)
	if got, want := report.RemovedDatasets, []string{"B"}; !reflect.DeepEqual(got, want) {
		t.Errorf("RemovedDatasets = %v, want %v", got, want)
	}
	if got, want := report.ChangedDatasets, []string{"C"}; !reflect.DeepEqual(got, want) {
		t.Errorf("ChangedDatasets = %v, want %v", got, want)
	}
	wantRemoved := []Relation{
		{Upstream: "A", Downstream: "B"},
		{Upstream: "B", Downstream: "C"},
	}
	if !reflect.DeepEqual(report.RemovedRelations, wantRemoved) {
		t.Errorf("RemovedRelations = %v, want %v", report.RemovedRelations, wantRemoved)
	}
	wantAdded := []Relation{{Upstream: "A", Downstream: "C"}}
	if !reflect.DeepEqual(report.AddedRelations, wantAdded) {
		t.Errorf("AddedRelations = %v, want %v", report.AddedRelations, wantAdded)
	}
	// A and C are common and both still root at A; B was removed and must not
	// appear in the root-source list.
	if len(report.RootSourceChanges) != 0 {
		t.Errorf("RootSourceChanges = %v, want empty", report.RootSourceChanges)
	}
}

// TestCompareSameRootsDifferentPath verifies that adjusting the path while
// still reaching the same root set is not reported as a source change.
func TestCompareSameRootsDifferentPath(t *testing.T) {
	oldSnap := snapshotOf(t, `{"datasets":[{"name":"A","upstreams":[]},{"name":"B","upstreams":["A"]},{"name":"C","upstreams":["B"]}]}`)
	newSnap := snapshotOf(t, `{"datasets":[{"name":"A","upstreams":[]},{"name":"B","upstreams":["A"]},{"name":"C","upstreams":["B","A"]}]}`)

	report := CompareSnapshots(oldSnap, newSnap)
	if got, want := report.ChangedDatasets, []string{"C"}; !reflect.DeepEqual(got, want) {
		t.Errorf("ChangedDatasets = %v, want %v", got, want)
	}
	if len(report.RootSourceChanges) != 0 {
		t.Errorf("same-root reroute reported as source change: %v", report.RootSourceChanges)
	}
}

// TestCompareNodeBecomingRoot: a node's own source includes itself, so when B
// stops depending on A and becomes a root, its source set changes A -> B.
func TestCompareNodeBecomingRoot(t *testing.T) {
	oldSnap := snapshotOf(t, `{"datasets":[{"name":"A","upstreams":[]},{"name":"B","upstreams":["A"]}]}`)
	newSnap := snapshotOf(t, `{"datasets":[{"name":"A","upstreams":[]},{"name":"B","upstreams":[]}]}`)
	report := CompareSnapshots(oldSnap, newSnap)
	want := []RootSourceChange{{Dataset: "B", OldRoots: []string{"A"}, NewRoots: []string{"B"}}}
	if !reflect.DeepEqual(report.RootSourceChanges, want) {
		t.Errorf("RootSourceChanges = %v, want %v", report.RootSourceChanges, want)
	}
}

func TestCompareRootsMultipathOnceAndSorted(t *testing.T) {
	// C reaches A and B (both roots) by two paths; duplicates count once and
	// the set is name-sorted.
	adj := adjacency{
		"A": nil,
		"B": nil,
		"C": {"A", "B"},
		"D": {"C", "A"},
	}
	got := rootSources("D", adj)
	want := []string{"A", "B"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("rootSources(D) = %v, want %v", got, want)
	}
	if got := rootSources("A", adj); !reflect.DeepEqual(got, []string{"A"}) {
		t.Errorf("rootSources(A) = %v, want [A] (root is its own source)", got)
	}
}

func TestCompareSelfAndEmpty(t *testing.T) {
	t.Run("identical snapshot all diffs empty", func(t *testing.T) {
		snap := snapshotOf(t, `{"datasets":[{"name":"A","upstreams":[]},{"name":"B","upstreams":["A"]}]}`)
		report := CompareSnapshots(snap, snap)
		assertEmptyReport(t, report)
	})

	t.Run("two empty snapshots", func(t *testing.T) {
		snap := snapshotOf(t, `{"datasets":[]}`)
		assertEmptyReport(t, CompareSnapshots(snap, snap))
	})

	t.Run("empty to populated lists only new nodes and edges", func(t *testing.T) {
		empty := snapshotOf(t, `{"datasets":[]}`)
		pop := snapshotOf(t, `{"datasets":[{"name":"A","upstreams":[]},{"name":"B","upstreams":["A"]}]}`)
		report := CompareSnapshots(empty, pop)
		if got, want := report.NewDatasets, []string{"A", "B"}; !reflect.DeepEqual(got, want) {
			t.Errorf("NewDatasets = %v, want %v", got, want)
		}
		if !reflect.DeepEqual(report.AddedRelations, []Relation{{Upstream: "A", Downstream: "B"}}) {
			t.Errorf("AddedRelations = %v", report.AddedRelations)
		}
		if len(report.RootSourceChanges) != 0 {
			t.Errorf("new datasets must not appear in root source changes: %v", report.RootSourceChanges)
		}
	})

	t.Run("populated to empty lists only removed nodes and edges", func(t *testing.T) {
		empty := snapshotOf(t, `{"datasets":[]}`)
		pop := snapshotOf(t, `{"datasets":[{"name":"A","upstreams":[]},{"name":"B","upstreams":["A"]}]}`)
		report := CompareSnapshots(pop, empty)
		if got, want := report.RemovedDatasets, []string{"A", "B"}; !reflect.DeepEqual(got, want) {
			t.Errorf("RemovedDatasets = %v, want %v", got, want)
		}
		if !reflect.DeepEqual(report.RemovedRelations, []Relation{{Upstream: "A", Downstream: "B"}}) {
			t.Errorf("RemovedRelations = %v", report.RemovedRelations)
		}
		if len(report.RootSourceChanges) != 0 {
			t.Errorf("removed datasets must not appear in root source changes: %v", report.RootSourceChanges)
		}
	})
}

func TestCompareEmptyListsEncodeAsArrays(t *testing.T) {
	snap := snapshotOf(t, `{"datasets":[]}`)
	data, err := json.Marshal(CompareSnapshots(snap, snap))
	if err != nil {
		t.Fatalf("marshal report: %v", err)
	}
	jsonText := string(data)
	if strings.Contains(jsonText, "null") {
		t.Errorf("empty report encoded a null list: %s", jsonText)
	}
	for _, key := range []string{`"newDatasets":`, `"removedDatasets":`, `"changedDatasets":`, `"addedRelations":`, `"removedRelations":`, `"rootSourceChanges":`} {
		if !strings.Contains(jsonText, key+"[]") {
			t.Errorf("report key %s not encoded as an empty array:\n%s", key, jsonText)
		}
	}
}

func TestCompareDeterministicBytes(t *testing.T) {
	oldA := snapshotOf(t, `{"datasets":[{"name":"A","upstreams":[]},{"name":"B","upstreams":["A"]},{"name":"C","upstreams":["B"]}]}`)
	// Same graph semantics, fed in shuffled/duplicated form.
	oldB := snapshotOf(t, `{"datasets":[{"name":"C","upstreams":["B"]},{"name":"B","upstreams":["A","A"]},{"name":"A","upstreams":[]}]}`)
	newA := snapshotOf(t, `{"datasets":[{"name":"X","upstreams":[]},{"name":"B","upstreams":["X"]},{"name":"C","upstreams":["B"]}]}`)

	r1 := CompareSnapshots(oldA, newA)
	r2 := CompareSnapshots(oldB, newA)
	b1, _ := json.Marshal(r1)
	b2, _ := json.Marshal(r2)
	if string(b1) != string(b2) {
		t.Fatalf("compare bytes differ for semantically equal old snapshots:\n%s\n%s", b1, b2)
	}
}

func TestCompareNameCasingAndSpacingPreserved(t *testing.T) {
	oldSnap := snapshotOf(t, `{"datasets":[{"name":" Raw ","upstreams":[]},{"name":"RAW","upstreams":[" Raw "]}]}`)
	newSnap := snapshotOf(t, `{"datasets":[{"name":" Raw ","upstreams":[]},{"name":"RAW","upstreams":[]}]}`)
	report := CompareSnapshots(oldSnap, newSnap)
	want := []RootSourceChange{{Dataset: "RAW", OldRoots: []string{" Raw "}, NewRoots: []string{"RAW"}}}
	if !reflect.DeepEqual(report.RootSourceChanges, want) {
		t.Errorf("RootSourceChanges = %#v, want %#v", report.RootSourceChanges, want)
	}
}

func assertEmptyReport(t *testing.T, report *CompareReport) {
	t.Helper()
	if len(report.NewDatasets) != 0 || len(report.RemovedDatasets) != 0 || len(report.ChangedDatasets) != 0 ||
		len(report.AddedRelations) != 0 || len(report.RemovedRelations) != 0 || len(report.RootSourceChanges) != 0 {
		t.Fatalf("expected empty report, got %+v", report)
	}
	// Slices must be non-nil so JSON renders [].
	if report.NewDatasets == nil || report.RemovedDatasets == nil || report.ChangedDatasets == nil ||
		report.AddedRelations == nil || report.RemovedRelations == nil || report.RootSourceChanges == nil {
		t.Errorf("empty report contains nil slices: %+v", report)
	}
}
