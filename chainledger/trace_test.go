package chainledger

import (
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
)

func TestTraceChain(t *testing.T) {
	// A is root; B derives from A; C derives from B. Tracing C walks C->B->A.
	snap := snapshotOf(t, `{"datasets":[{"name":"A","upstreams":[]},{"name":"B","upstreams":["A"]},{"name":"C","upstreams":["B"]}]}`)
	report, err := TraceSnapshot(snap, "C")
	if err != nil {
		t.Fatalf("TraceSnapshot: %v", err)
	}
	want := []TraceSource{{Root: "A", Path: []string{"C", "B", "A"}}}
	if !reflect.DeepEqual(report.Sources, want) {
		t.Errorf("sources = %v, want %v", report.Sources, want)
	}
	if report.ContentID != snap.ContentID {
		t.Errorf("contentId = %q, want %q", report.ContentID, snap.ContentID)
	}
	if report.Dataset != "C" {
		t.Errorf("dataset = %q, want %q", report.Dataset, "C")
	}
}

func TestTraceDirectPathBeatsIndirect(t *testing.T) {
	// T depends on R directly AND via B. The direct path [T,R] must win over
	// [T,B,R].
	snap := snapshotOf(t, `{"datasets":[{"name":"R","upstreams":[]},{"name":"B","upstreams":["R"]},{"name":"T","upstreams":["R","B"]}]}`)
	report, err := TraceSnapshot(snap, "T")
	if err != nil {
		t.Fatalf("TraceSnapshot: %v", err)
	}
	want := []TraceSource{{Root: "R", Path: []string{"T", "R"}}}
	if !reflect.DeepEqual(report.Sources, want) {
		t.Errorf("sources = %v, want %v (direct path must win)", report.Sources, want)
	}
}

func TestTraceEqualLengthTieBreakByName(t *testing.T) {
	// T reaches the same root R via A or B, equal length. The lexicographically
	// smaller path [T,A,R] must win.
	snap := snapshotOf(t, `{"datasets":[{"name":"R","upstreams":[]},{"name":"A","upstreams":["R"]},{"name":"B","upstreams":["R"]},{"name":"T","upstreams":["A","B"]}]}`)
	report, err := TraceSnapshot(snap, "T")
	if err != nil {
		t.Fatalf("TraceSnapshot: %v", err)
	}
	want := []TraceSource{{Root: "R", Path: []string{"T", "A", "R"}}}
	if !reflect.DeepEqual(report.Sources, want) {
		t.Errorf("sources = %v, want %v (tie-break by name)", report.Sources, want)
	}
}

func TestTraceEqualLengthTieBreakDeeper(t *testing.T) {
	// Tie-break must compare from the dataset toward the root, not just the
	// first hop: T reaches R via X->A or X->B. Both share [T,X,...]; the
	// differing name is A vs B, so [T,X,A,R] wins.
	snap := snapshotOf(t, `{"datasets":[
		{"name":"R","upstreams":[]},
		{"name":"A","upstreams":["R"]},
		{"name":"B","upstreams":["R"]},
		{"name":"X","upstreams":["A","B"]},
		{"name":"T","upstreams":["X"]}
	]}`)
	report, err := TraceSnapshot(snap, "T")
	if err != nil {
		t.Fatalf("TraceSnapshot: %v", err)
	}
	want := []TraceSource{{Root: "R", Path: []string{"T", "X", "A", "R"}}}
	if !reflect.DeepEqual(report.Sources, want) {
		t.Errorf("sources = %v, want %v", report.Sources, want)
	}
}

func TestTraceMultipleRootsSorted(t *testing.T) {
	// C reaches two roots A and B; sources are ordered by root name.
	snap := snapshotOf(t, `{"datasets":[{"name":"A","upstreams":[]},{"name":"B","upstreams":[]},{"name":"C","upstreams":["A","B"]}]}`)
	report, err := TraceSnapshot(snap, "C")
	if err != nil {
		t.Fatalf("TraceSnapshot: %v", err)
	}
	want := []TraceSource{
		{Root: "A", Path: []string{"C", "A"}},
		{Root: "B", Path: []string{"C", "B"}},
	}
	if !reflect.DeepEqual(report.Sources, want) {
		t.Errorf("sources = %v, want %v", report.Sources, want)
	}
}

func TestTraceSharedIntermediatesKeepOwnPaths(t *testing.T) {
	// Two roots share intermediate datasets but each keeps its own path.
	// T -> M -> A (root), T -> M -> B (root).
	snap := snapshotOf(t, `{"datasets":[
		{"name":"A","upstreams":[]},
		{"name":"B","upstreams":[]},
		{"name":"M","upstreams":["A","B"]},
		{"name":"T","upstreams":["M"]}
	]}`)
	report, err := TraceSnapshot(snap, "T")
	if err != nil {
		t.Fatalf("TraceSnapshot: %v", err)
	}
	want := []TraceSource{
		{Root: "A", Path: []string{"T", "M", "A"}},
		{Root: "B", Path: []string{"T", "M", "B"}},
	}
	if !reflect.DeepEqual(report.Sources, want) {
		t.Errorf("sources = %v, want %v", report.Sources, want)
	}
}

func TestTraceQueryIsRoot(t *testing.T) {
	// The queried dataset itself is a root: report only itself, single-element
	// path.
	snap := snapshotOf(t, `{"datasets":[{"name":"A","upstreams":[]},{"name":"B","upstreams":["A"]}]}`)
	report, err := TraceSnapshot(snap, "A")
	if err != nil {
		t.Fatalf("TraceSnapshot: %v", err)
	}
	want := []TraceSource{{Root: "A", Path: []string{"A"}}}
	if !reflect.DeepEqual(report.Sources, want) {
		t.Errorf("sources = %v, want %v", report.Sources, want)
	}
}

func TestTraceNameCasingAndSpacingPreserved(t *testing.T) {
	// Names are case-sensitive and preserve surrounding spaces.
	snap := snapshotOf(t, `{"datasets":[{"name":" Raw ","upstreams":[]},{"name":"RAW","upstreams":[" Raw "]}]}`)
	report, err := TraceSnapshot(snap, "RAW")
	if err != nil {
		t.Fatalf("TraceSnapshot: %v", err)
	}
	want := []TraceSource{{Root: " Raw ", Path: []string{"RAW", " Raw "}}}
	if !reflect.DeepEqual(report.Sources, want) {
		t.Errorf("sources = %v, want %v", report.Sources, want)
	}
}

func TestTraceUnconnectedDatasetsExcluded(t *testing.T) {
	// Datasets not reachable upstream from the query never appear.
	snap := snapshotOf(t, `{"datasets":[
		{"name":"A","upstreams":[]},
		{"name":"B","upstreams":["A"]},
		{"name":"X","upstreams":[]},
		{"name":"Y","upstreams":["X"]}
	]}`)
	report, err := TraceSnapshot(snap, "B")
	if err != nil {
		t.Fatalf("TraceSnapshot: %v", err)
	}
	want := []TraceSource{{Root: "A", Path: []string{"B", "A"}}}
	if !reflect.DeepEqual(report.Sources, want) {
		t.Errorf("sources = %v, want %v (X/Y must not appear)", report.Sources, want)
	}
}

func TestTraceEmptyNameRejected(t *testing.T) {
	snap := snapshotOf(t, `{"datasets":[{"name":"A","upstreams":[]}]}`)
	_, err := TraceSnapshot(snap, "")
	if !errors.Is(err, ErrInvalidArgument) {
		t.Fatalf("err = %v, want ErrInvalidArgument", err)
	}
}

func TestTraceMissingDatasetRejected(t *testing.T) {
	snap := snapshotOf(t, `{"datasets":[{"name":"A","upstreams":[]}]}`)
	_, err := TraceSnapshot(snap, "ghost")
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("err = %v, want ErrNotFound", err)
	}
}

func TestTraceEmptyGraphRejectsQuery(t *testing.T) {
	snap := snapshotOf(t, `{"datasets":[]}`)
	_, err := TraceSnapshot(snap, "A")
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("err = %v, want ErrNotFound for empty graph", err)
	}
}

func TestTraceDeterministicBytes(t *testing.T) {
	// Record order, upstream order, duplicate upstreams, and JSON whitespace
	// must not change the trace report bytes for the same query.
	inputs := []string{
		`{"datasets":[{"name":"A","upstreams":[]},{"name":"B","upstreams":["A"]},{"name":"C","upstreams":["B","A"]}]}`,
		`{ "datasets": [ {"upstreams":["A","B","A"],"name":"C"}, {"upstreams":[],"name":"A"}, {"name":"B","upstreams":["A"]} ] }`,
		"{\"datasets\":[\n  {\"name\":\"C\",\"upstreams\":[\"A\",\"B\"]},\n  {\"name\":\"A\",\"upstreams\":[]},\n  {\"name\":\"B\",\"upstreams\":[\"A\"]}\n]}\n",
	}
	var want []byte
	for i, in := range inputs {
		snap := snapshotOf(t, in)
		report, err := TraceSnapshot(snap, "C")
		if err != nil {
			t.Fatalf("TraceSnapshot input %d: %v", i, err)
		}
		got, err := json.Marshal(report)
		if err != nil {
			t.Fatalf("marshal report input %d: %v", i, err)
		}
		if i == 0 {
			want = got
			continue
		}
		if string(got) != string(want) {
			t.Errorf("input %d trace bytes differ:\n got %s\nwant %s", i, got, want)
		}
	}
}

func TestTraceSourcesAlwaysArray(t *testing.T) {
	// Even a root query encodes sources as [{"root":...,"path":[...]}], never
	// null.
	snap := snapshotOf(t, `{"datasets":[{"name":"A","upstreams":[]}]}`)
	report, err := TraceSnapshot(snap, "A")
	if err != nil {
		t.Fatalf("TraceSnapshot: %v", err)
	}
	data, err := json.Marshal(report)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if strings.Contains(string(data), "null") {
		t.Errorf("trace report encoded null: %s", data)
	}
}
