package chainledger

import (
	"reflect"
	"strings"
	"testing"
)

// registerCutoffSpecGraph builds the scenario from the spec: source derives a
// and b; a derives report directly while b reaches report through mid; report
// derives view.
//
//	source ──> a ────────┐
//	  │                  ├──> report ──> view
//	  └──> b ──> mid ────┘
func registerCutoffSpecGraph(t *testing.T) map[string]*Lineage {
	t.Helper()
	graph := map[string]*Lineage{}
	mustRegister(t, graph, "source")
	mustRegister(t, graph, "a", "source")
	mustRegister(t, graph, "b", "source")
	mustRegister(t, graph, "report", "a")
	mustRegister(t, graph, "mid", "b")
	mustRegister(t, graph, "report", "a", "mid")
	mustRegister(t, graph, "view", "report")
	return graph
}

func assertImpacts(t *testing.T, got []Impact, want []Impact) {
	t.Helper()
	if got == nil {
		t.Fatal("ImpactsWithCutoffs returned a nil slice on success")
	}
	if len(got) != len(want) {
		t.Fatalf("got %d impact(s) %v, want %d %v", len(got), got, len(want), want)
	}
	for i := range want {
		if got[i].Dataset != want[i].Dataset ||
			got[i].Distance != want[i].Distance ||
			!reflect.DeepEqual(got[i].Path, want[i].Path) {
			t.Errorf("impact[%d] = %+v, want %+v", i, got[i], want[i])
		}
	}
}

// The worked example from the spec: with a as the cutoff, a itself is still
// reported at distance 1, but report is reached through b and mid at distance
// 3 and view at distance 4, with paths along that surviving route.
func TestImpactsWithCutoffsSpecExample(t *testing.T) {
	graph := registerCutoffSpecGraph(t)

	got, err := ImpactsWithCutoffs(graph, "source", []string{"a"})
	if err != nil {
		t.Fatalf("ImpactsWithCutoffs: %v", err)
	}
	assertImpacts(t, got, []Impact{
		{Dataset: "a", Distance: 1, Path: []string{"source", "a"}},
		{Dataset: "b", Distance: 1, Path: []string{"source", "b"}},
		{Dataset: "mid", Distance: 2, Path: []string{"source", "b", "mid"}},
		{Dataset: "report", Distance: 3, Path: []string{"source", "b", "mid", "report"}},
		{Dataset: "view", Distance: 4, Path: []string{"source", "b", "mid", "report", "view"}},
	})
}

// With both branches cut off, a and b are still reported but mid, report and
// view no longer appear.
func TestImpactsWithCutoffsBothBranchesCut(t *testing.T) {
	graph := registerCutoffSpecGraph(t)

	got, err := ImpactsWithCutoffs(graph, "source", []string{"a", "b"})
	if err != nil {
		t.Fatalf("ImpactsWithCutoffs: %v", err)
	}
	assertImpacts(t, got, []Impact{
		{Dataset: "a", Distance: 1, Path: []string{"source", "a"}},
		{Dataset: "b", Distance: 1, Path: []string{"source", "b"}},
	})
}

// An empty cutoff list (nil or zero-length) is exactly the plain Impacts
// query: report keeps its shorter direct route through a.
func TestImpactsWithCutoffsEmptyListMatchesImpacts(t *testing.T) {
	graph := registerCutoffSpecGraph(t)

	want, err := Impacts(graph, "source")
	if err != nil {
		t.Fatalf("Impacts: %v", err)
	}
	for _, cutoffs := range [][]string{nil, {}} {
		got, err := ImpactsWithCutoffs(graph, "source", cutoffs)
		if err != nil {
			t.Fatalf("ImpactsWithCutoffs(%v): %v", cutoffs, err)
		}
		assertImpacts(t, got, want)
	}
	if want[3].Dataset != "report" || want[3].Distance != 2 ||
		!reflect.DeepEqual(want[3].Path, []string{"source", "a", "report"}) {
		t.Errorf("sanity: full-query report impact = %+v, want distance 2 via a", want[3])
	}
}

// A dataset behind a cutoff stays in scope while any route avoiding every
// cutoff remains, and its distance and path come from that route alone.
func TestImpactsWithCutoffsBehindCutoffViaOtherRoute(t *testing.T) {
	graph := map[string]*Lineage{}
	mustRegister(t, graph, "source")
	mustRegister(t, graph, "gate", "source")
	mustRegister(t, graph, "leaf", "gate")
	// leaf is also reachable directly from source, bypassing gate entirely.
	mustRegister(t, graph, "leaf", "gate", "source")

	got, err := ImpactsWithCutoffs(graph, "source", []string{"gate"})
	if err != nil {
		t.Fatalf("ImpactsWithCutoffs: %v", err)
	}
	assertImpacts(t, got, []Impact{
		{Dataset: "gate", Distance: 1, Path: []string{"source", "gate"}},
		{Dataset: "leaf", Distance: 1, Path: []string{"source", "leaf"}},
	})
}

// Duplicate cutoff names act once, and a registered cutoff the origin cannot
// reach changes nothing.
func TestImpactsWithCutoffsDuplicatesAndUnreachable(t *testing.T) {
	graph := registerCutoffSpecGraph(t)
	mustRegister(t, graph, "isolated")

	full, err := Impacts(graph, "source")
	if err != nil {
		t.Fatalf("Impacts: %v", err)
	}
	got, err := ImpactsWithCutoffs(graph, "source", []string{"isolated", "isolated"})
	if err != nil {
		t.Fatalf("ImpactsWithCutoffs: %v", err)
	}
	assertImpacts(t, got, full)

	// A duplicate of an effective cutoff does not double its effect either.
	got, err = ImpactsWithCutoffs(graph, "source", []string{"a", "a"})
	if err != nil {
		t.Fatalf("ImpactsWithCutoffs: %v", err)
	}
	once, err := ImpactsWithCutoffs(graph, "source", []string{"a"})
	if err != nil {
		t.Fatalf("ImpactsWithCutoffs: %v", err)
	}
	assertImpacts(t, got, once)
}

// Listing the origin itself as a cutoff is legal: the query succeeds with a
// non-nil empty list and the origin is not listed as impacted.
func TestImpactsWithCutoffsOriginIsCutoff(t *testing.T) {
	graph := registerCutoffSpecGraph(t)

	got, err := ImpactsWithCutoffs(graph, "source", []string{"source"})
	if err != nil {
		t.Fatalf("ImpactsWithCutoffs: %v", err)
	}
	if got == nil {
		t.Fatal("ImpactsWithCutoffs returned a nil slice on success")
	}
	if len(got) != 0 {
		t.Fatalf("got %v, want empty list", got)
	}
}

// Equal-length surviving routes still pick the lexicographically smallest
// full path compared from the origin, and a truncated route never lends its
// length or names to a surviving hit.
func TestImpactsWithCutoffsTieBreakAmongSurvivingRoutes(t *testing.T) {
	graph := map[string]*Lineage{}
	mustRegister(t, graph, "source")
	mustRegister(t, graph, "a", "source")
	mustRegister(t, graph, "b", "source")
	mustRegister(t, graph, "za", "a")
	mustRegister(t, graph, "zb", "b")
	mustRegister(t, graph, "join", "za", "zb")

	// Without cutoffs the a-side route wins (a < b at hop 1).
	full, err := Impacts(graph, "source")
	if err != nil {
		t.Fatalf("Impacts: %v", err)
	}
	join := full[len(full)-1]
	if join.Dataset != "join" || !reflect.DeepEqual(join.Path, []string{"source", "a", "za", "join"}) {
		t.Fatalf("sanity: full-query join path = %v", join.Path)
	}

	// Cutting a removes that route entirely: join is still reached, but only
	// through the b side, and the path shows it.
	got, err := ImpactsWithCutoffs(graph, "source", []string{"a"})
	if err != nil {
		t.Fatalf("ImpactsWithCutoffs: %v", err)
	}
	assertImpacts(t, got, []Impact{
		{Dataset: "a", Distance: 1, Path: []string{"source", "a"}},
		{Dataset: "b", Distance: 1, Path: []string{"source", "b"}},
		{Dataset: "zb", Distance: 2, Path: []string{"source", "b", "zb"}},
		{Dataset: "join", Distance: 3, Path: []string{"source", "b", "zb", "join"}},
	})
}

// Origin errors keep the existing Impacts behavior; cutoff-list problems fail
// the whole query with nil results, and an unregistered cutoff is named.
func TestImpactsWithCutoffsErrors(t *testing.T) {
	graph := registerCutoffSpecGraph(t)

	cases := []struct {
		name    string
		origin  string
		cutoffs []string
		wantErr string
	}{
		{"empty origin", "", []string{"a"}, "dataset name is required"},
		{"unknown origin", "ghost", []string{"a"}, "dataset not found: ghost"},
		{"empty cutoff name", "source", []string{"a", ""}, "cutoff dataset name is required"},
		{"unknown cutoff", "source", []string{"ghost"}, "cutoff dataset not found: ghost"},
		{"unknown cutoff among valid ones", "source", []string{"a", "ghost"}, "cutoff dataset not found: ghost"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := ImpactsWithCutoffs(graph, tc.origin, tc.cutoffs)
			if err == nil {
				t.Fatalf("ImpactsWithCutoffs(%q, %v) succeeded with %v, want error %q",
					tc.origin, tc.cutoffs, got, tc.wantErr)
			}
			if err.Error() != tc.wantErr {
				t.Errorf("error = %q, want %q", err.Error(), tc.wantErr)
			}
			if got != nil {
				t.Errorf("result = %v, want nil on failure", got)
			}
		})
	}

	// Cutoff names match by exact registered value, case-sensitive.
	if _, err := ImpactsWithCutoffs(graph, "source", []string{"A"}); err == nil ||
		!strings.Contains(err.Error(), "A") {
		t.Errorf("case-mismatched cutoff: err = %v, want one naming A", err)
	}
}

// The query is read-only and every returned path is an independent copy:
// mutating a result affects neither the graph nor later queries.
func TestImpactsWithCutoffsReadOnlyAndIndependentPaths(t *testing.T) {
	graph := registerCutoffSpecGraph(t)
	before := snapshot(graph)

	first, err := ImpactsWithCutoffs(graph, "source", []string{"a"})
	if err != nil {
		t.Fatalf("ImpactsWithCutoffs: %v", err)
	}
	for i := range first {
		for j := range first[i].Path {
			first[i].Path[j] = "corrupted"
		}
	}
	first[0].Dataset = "corrupted"

	assertConsistent(t, graph)
	if !reflect.DeepEqual(snapshot(graph), before) {
		t.Errorf("graph changed across the query:\nbefore=%v\nafter=%v", before, snapshot(graph))
	}

	second, err := ImpactsWithCutoffs(graph, "source", []string{"a"})
	if err != nil {
		t.Fatalf("ImpactsWithCutoffs: %v", err)
	}
	assertImpacts(t, second, []Impact{
		{Dataset: "a", Distance: 1, Path: []string{"source", "a"}},
		{Dataset: "b", Distance: 1, Path: []string{"source", "b"}},
		{Dataset: "mid", Distance: 2, Path: []string{"source", "b", "mid"}},
		{Dataset: "report", Distance: 3, Path: []string{"source", "b", "mid", "report"}},
		{Dataset: "view", Distance: 4, Path: []string{"source", "b", "mid", "report", "view"}},
	})
}
