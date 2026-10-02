package chainledger

import (
	"errors"
	"reflect"
	"strings"
	"testing"
)

// traceOf traces dataset in the snapshot built from graphJSON.
func traceOf(t *testing.T, graphJSON, dataset string) *TraceReport {
	t.Helper()
	report, err := TraceSources(snapshotOf(t, graphJSON), dataset)
	if err != nil {
		t.Fatalf("TraceSources(%q): %v", dataset, err)
	}
	return report
}

func TestTraceDirectRootBeatsIndirect(t *testing.T) {
	// T depends on R directly and also reaches R through B: the direct,
	// shorter path wins.
	report := traceOf(t, `{"datasets":[
		{"name":"T","upstreams":["R","B"]},
		{"name":"B","upstreams":["R"]},
		{"name":"R","upstreams":[]}
	]}`, "T")
	want := []SourceTrace{{Root: "R", Path: []string{"T", "R"}}}
	if !reflect.DeepEqual(report.Sources, want) {
		t.Errorf("Sources = %+v, want %+v", report.Sources, want)
	}
}

func TestTraceEqualLengthPrefersByteOrder(t *testing.T) {
	// T reaches R through A or through B with the same number of relations;
	// the path via the bytewise-smaller intermediate name wins.
	report := traceOf(t, `{"datasets":[
		{"name":"T","upstreams":["B","A"]},
		{"name":"A","upstreams":["R"]},
		{"name":"B","upstreams":["R"]},
		{"name":"R","upstreams":[]}
	]}`, "T")
	want := []SourceTrace{{Root: "R", Path: []string{"T", "A", "R"}}}
	if !reflect.DeepEqual(report.Sources, want) {
		t.Errorf("Sources = %+v, want %+v", report.Sources, want)
	}
}

func TestTraceMultipleRootsSortedAndSharedIntermediates(t *testing.T) {
	// Two roots share the intermediate M; each keeps its own path, and the
	// sources are sorted by root name byte order ("Z" before "a").
	report := traceOf(t, `{"datasets":[
		{"name":"T","upstreams":["M"]},
		{"name":"M","upstreams":["a","Z"]},
		{"name":"a","upstreams":[]},
		{"name":"Z","upstreams":[]}
	]}`, "T")
	want := []SourceTrace{
		{Root: "Z", Path: []string{"T", "M", "Z"}},
		{Root: "a", Path: []string{"T", "M", "a"}},
	}
	if !reflect.DeepEqual(report.Sources, want) {
		t.Errorf("Sources = %+v, want %+v", report.Sources, want)
	}
}

func TestTraceRootQueriesItself(t *testing.T) {
	report := traceOf(t, `{"datasets":[
		{"name":"R","upstreams":[]},
		{"name":"S","upstreams":["R"]}
	]}`, "R")
	want := []SourceTrace{{Root: "R", Path: []string{"R"}}}
	if !reflect.DeepEqual(report.Sources, want) {
		t.Errorf("Sources = %+v, want %+v", report.Sources, want)
	}
}

func TestTraceIgnoresDisconnectedAndNonRootIntermediates(t *testing.T) {
	// X is disconnected and must not appear; M has an upstream so it is not
	// a root even though it is on the path.
	report := traceOf(t, `{"datasets":[
		{"name":"T","upstreams":["M"]},
		{"name":"M","upstreams":["R"]},
		{"name":"R","upstreams":[]},
		{"name":"X","upstreams":[]}
	]}`, "T")
	want := []SourceTrace{{Root: "R", Path: []string{"T", "M", "R"}}}
	if !reflect.DeepEqual(report.Sources, want) {
		t.Errorf("Sources = %+v, want %+v", report.Sources, want)
	}
}

func TestTraceReportCarriesContentIDAndQuery(t *testing.T) {
	snap := snapshotOf(t, `{"datasets":[{"name":"T","upstreams":[]}]}`)
	report, err := TraceSources(snap, "T")
	if err != nil {
		t.Fatalf("TraceSources: %v", err)
	}
	if report.ContentID != snap.ContentID {
		t.Errorf("ContentID = %q, want snapshot id %q", report.ContentID, snap.ContentID)
	}
	if report.Dataset != "T" {
		t.Errorf("Dataset = %q, want %q", report.Dataset, "T")
	}
}

func TestTraceUnknownAndEmptyDatasetRejected(t *testing.T) {
	snap := snapshotOf(t, `{"datasets":[{"name":"T","upstreams":[]}]}`)
	if _, err := TraceSources(snap, ""); !errors.Is(err, ErrInvalidArgument) {
		t.Errorf("empty name error = %v, want ErrInvalidArgument", err)
	}
	if _, err := TraceSources(snap, "ghost"); !errors.Is(err, ErrNotFound) {
		t.Errorf("unknown name error = %v, want ErrNotFound", err)
	}
	if _, err := TraceSources(snap, "ghost"); err == nil || !strings.Contains(err.Error(), "ghost") {
		t.Errorf("unknown name error = %v, must name the dataset", err)
	}
}

// TestTraceDeterministicAcrossEquivalentSnapshots checks that record order,
// upstream order, duplicate upstreams, and JSON whitespace never change the
// trace report for the same query.
func TestTraceDeterministicAcrossEquivalentSnapshots(t *testing.T) {
	variants := []string{
		`{"datasets":[{"name":"T","upstreams":["A","B"]},{"name":"A","upstreams":["R"]},{"name":"B","upstreams":["R"]},{"name":"R","upstreams":[]}]}`,
		`{ "datasets": [ {"upstreams":[],"name":"R"}, {"name":"B","upstreams":["R","R"]}, {"name":"A","upstreams":["R"]}, {"name":"T","upstreams":["B","A","B"]} ] }`,
	}
	var want *TraceReport
	for i, in := range variants {
		report := traceOf(t, in, "T")
		if i == 0 {
			want = report
			continue
		}
		if !reflect.DeepEqual(report, want) {
			t.Errorf("variant %d report = %+v, want %+v", i, report, want)
		}
	}
}

// TestTraceSnapshotIsTheOnlySource pins the version semantics: the trace is
// computed from the snapshot alone, so mutating the live graph afterwards
// cannot change what the frozen version reports.
func TestTraceSnapshotIsTheOnlySource(t *testing.T) {
	graph := graphFromJSON(t, `{"datasets":[{"name":"T","upstreams":["R"]},{"name":"R","upstreams":[]}]}`)
	snap, err := BuildSnapshot(graph)
	if err != nil {
		t.Fatalf("BuildSnapshot: %v", err)
	}
	// Rewire the live graph after the snapshot was frozen.
	if err := Register(graph, Dataset{Name: "S"}, nil); err != nil {
		t.Fatalf("Register S: %v", err)
	}
	if err := Register(graph, Dataset{Name: "T"}, []string{"S"}); err != nil {
		t.Fatalf("rewire T: %v", err)
	}
	report, err := TraceSources(snap, "T")
	if err != nil {
		t.Fatalf("TraceSources: %v", err)
	}
	want := []SourceTrace{{Root: "R", Path: []string{"T", "R"}}}
	if !reflect.DeepEqual(report.Sources, want) {
		t.Errorf("Sources = %+v, want frozen %+v", report.Sources, want)
	}
}
