package chainledger

import (
	"bytes"
	"errors"
	"reflect"
	"strings"
	"testing"
)

func mustParseGraph(t *testing.T, data string) *GraphData {
	t.Helper()
	g, err := ParseGraph([]byte(data))
	if err != nil {
		t.Fatalf("ParseGraph(%s): %v", data, err)
	}
	return g
}

func previewJSON(t *testing.T, graph, plan string) (*BatchReport, error) {
	t.Helper()
	g, gErr := ParseGraph([]byte(graph))
	if gErr != nil {
		t.Fatalf("ParseGraph: %v", gErr)
	}
	p, pErr := ParsePlan([]byte(plan), g)
	if pErr != nil {
		return nil, pErr
	}
	return Preview(g, p)
}

// The headline scenario: B depends on A, and one batch both makes A depend on
// B and turns B into a root. Judged one record at a time this is impossible;
// judged against the fully-replaced graph it must succeed.
func TestBatchMutualRepointSucceeds(t *testing.T) {
	report, err := previewJSON(t,
		`{"nodes":[{"name":"A","upstreams":[]},{"name":"B","upstreams":["A"]}]}`,
		`{"adjustments":[{"name":"A","upstreams":["B"]},{"name":"B","upstreams":[]}]}`,
	)
	if err != nil {
		t.Fatalf("batch rejected: %v", err)
	}
	if got := report.NewDatasets; !reflect.DeepEqual(got, []string{}) {
		t.Errorf("NewDatasets = %v, want []", got)
	}
	if got := report.ChangedDatasets; !reflect.DeepEqual(got, []string{"A", "B"}) {
		t.Errorf("ChangedDatasets = %v, want [A B]", got)
	}
	wantRemoved := []Relationship{{Upstream: "A", Downstream: "B"}}
	if got := report.RemovedRelationships; !reflect.DeepEqual(got, wantRemoved) {
		t.Errorf("RemovedRelationships = %v, want %v", got, wantRemoved)
	}
	wantAdded := []Relationship{{Upstream: "B", Downstream: "A"}}
	if got := report.AddedRelationships; !reflect.DeepEqual(got, wantAdded) {
		t.Errorf("AddedRelationships = %v, want %v", got, wantAdded)
	}
	wantFinal := GraphJSON{Nodes: []GraphNode{
		{Name: "A", Upstreams: []string{"B"}},
		{Name: "B", Upstreams: []string{}},
	}}
	if got := report.FinalGraph; !reflect.DeepEqual(got, wantFinal) {
		t.Errorf("FinalGraph = %#v, want %#v", got, wantFinal)
	}
}

// New datasets in the same batch may reference each other, in any order.
func TestBatchNewDatasetsReferenceEachOther(t *testing.T) {
	plans := []string{
		`{"adjustments":[{"name":"X","upstreams":[]},{"name":"Y","upstreams":["X"]},{"name":"Z","upstreams":["X","Y"]}]}`,
		`{"adjustments":[{"name":"Z","upstreams":["Y","X"]},{"name":"Y","upstreams":["X"]},{"name":"X","upstreams":[]}]}`,
	}
	var first []byte
	for i, plan := range plans {
		g := mustParseGraph(t, `{"nodes":[]}`)
		p, err := ParsePlan([]byte(plan), g)
		if err != nil {
			t.Fatalf("plan %d rejected: %v", i+1, err)
		}
		report, err := Preview(g, p)
		if err != nil {
			t.Fatalf("preview %d rejected: %v", i+1, err)
		}
		if got := report.NewDatasets; !reflect.DeepEqual(got, []string{"X", "Y", "Z"}) {
			t.Errorf("NewDatasets = %v, want [X Y Z]", got)
		}
		if got := report.ChangedDatasets; !reflect.DeepEqual(got, []string{}) {
			t.Errorf("ChangedDatasets = %v, want []", got)
		}
		encoded, err := MarshalReport(report)
		if err != nil {
			t.Fatalf("marshal: %v", err)
		}
		if i == 0 {
			first = encoded
		} else if !bytes.Equal(encoded, first) {
			t.Fatalf("record/upstream order changed the report bytes:\n%s\nvs\n%s", first, encoded)
		}
	}
}

func TestBatchRejectsFinalCyclesAndSelfDependency(t *testing.T) {
	cases := []struct {
		name  string
		graph string
		plan  string
	}{
		{
			name:  "mutual cycle A and B",
			graph: `{"nodes":[{"name":"A","upstreams":[]},{"name":"B","upstreams":["A"]}]}`,
			plan:  `{"adjustments":[{"name":"A","upstreams":["B"]}]}`,
		},
		{
			name:  "self dependency",
			graph: `{"nodes":[{"name":"A","upstreams":[]}]}`,
			plan:  `{"adjustments":[{"name":"A","upstreams":["A"]}]}`,
		},
		{
			name:  "indirect cycle through new datasets",
			graph: `{"nodes":[{"name":"A","upstreams":[]}]}`,
			plan:  `{"adjustments":[{"name":"B","upstreams":["C"]},{"name":"C","upstreams":["A","B"]}]}`,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := previewJSON(t, tc.graph, tc.plan)
			if !errors.Is(err, ErrMalformedPlan) {
				t.Fatalf("err = %v, want ErrMalformedPlan", err)
			}
			if !strings.Contains(err.Error(), "cycle") {
				t.Fatalf("error %q does not explain the cycle", err.Error())
			}
		})
	}
}

func TestBatchPlanValidationRejectsWholeBatch(t *testing.T) {
	graph := `{"nodes":[{"name":"A","upstreams":[]}]}`
	cases := []struct {
		name     string
		plan     string
		wantName string
		wantWhy  string
	}{
		{
			name:     "duplicate dataset declaration",
			plan:     `{"adjustments":[{"name":"B","upstreams":[]},{"name":"B","upstreams":["A"]}]}`,
			wantName: "B",
			wantWhy:  "more than once",
		},
		{
			name:    "empty dataset name",
			plan:    `{"adjustments":[{"name":"","upstreams":[]}]}`,
			wantWhy: "empty dataset name",
		},
		{
			name:     "empty upstream name",
			plan:     `{"adjustments":[{"name":"B","upstreams":[""]}]}`,
			wantName: "B",
			wantWhy:  "empty upstream",
		},
		{
			name:     "upstream missing from graph and plan",
			plan:     `{"adjustments":[{"name":"B","upstreams":["ghost"]}]}`,
			wantName: "ghost",
			wantWhy:  "does not exist",
		},
		{
			name:     "new dataset references another dataset never declared",
			plan:     `{"adjustments":[{"name":"B","upstreams":["C"]},{"name":"D","upstreams":[]}]}`,
			wantName: "C",
			wantWhy:  "does not exist",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := previewJSON(t, graph, tc.plan)
			if !errors.Is(err, ErrMalformedPlan) {
				t.Fatalf("err = %v, want ErrMalformedPlan", err)
			}
			if tc.wantName != "" && !strings.Contains(err.Error(), tc.wantName) {
				t.Errorf("error %q does not name %q", err.Error(), tc.wantName)
			}
			if tc.wantWhy != "" && !strings.Contains(err.Error(), tc.wantWhy) {
				t.Errorf("error %q does not explain %q", err.Error(), tc.wantWhy)
			}
		})
	}
}

func TestParseGraphRejectsBrokenInputGraph(t *testing.T) {
	cases := []struct {
		name string
		data string
		want error
	}{
		{"empty name", `{"nodes":[{"name":"","upstreams":[]}]}`, ErrMalformedGraph},
		{"duplicate dataset", `{"nodes":[{"name":"A","upstreams":[]},{"name":"A","upstreams":[]}]}`, ErrMalformedGraph},
		{"missing upstream", `{"nodes":[{"name":"A","upstreams":["B"]}]}`, ErrMalformedGraph},
		{"empty upstream name", `{"nodes":[{"name":"A","upstreams":[""]}]}`, ErrMalformedGraph},
		{"cycle", `{"nodes":[{"name":"A","upstreams":["B"]},{"name":"B","upstreams":["A"]}]}`, ErrMalformedGraph},
		{"invalid json", `{"nodes":[}`, ErrMalformedGraph},
		{"trailing content", `{"nodes":[]} 42`, ErrMalformedGraph},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := ParseGraph([]byte(tc.data))
			if !errors.Is(err, tc.want) {
				t.Fatalf("err = %v, want %v", err, tc.want)
			}
		})
	}
}

// A broken input graph is rejected even when the plan would happen to mask it.
func TestBatchDoesNotMaskGraphProblems(t *testing.T) {
	// Graph has A missing upstream X. The plan registering X cannot rescue it:
	// the graph is rejected before the plan is ever considered.
	_, err := ParseGraph([]byte(`{"nodes":[{"name":"A","upstreams":["X"]}]}`))
	if !errors.Is(err, ErrMalformedGraph) {
		t.Fatalf("ParseGraph err = %v, want ErrMalformedGraph", err)
	}

	// Graph already cyclic; even an empty plan cannot apply: ParseGraph
	// rejects before any plan is considered.
	if _, err := ParseGraph([]byte(`{"nodes":[{"name":"A","upstreams":["B"]},{"name":"B","upstreams":["A"]}]}`)); !errors.Is(err, ErrMalformedGraph) {
		t.Fatalf("cyclic graph err = %v, want ErrMalformedGraph", err)
	}
}

// The empty graph accepts a batch of new datasets.
func TestBatchEmptyGraphRegistersDatasets(t *testing.T) {
	report, err := previewJSON(t,
		`{"nodes":[]}`,
		`{"adjustments":[{"name":"A","upstreams":[]},{"name":"B","upstreams":["A"]}]}`,
	)
	if err != nil {
		t.Fatalf("batch rejected: %v", err)
	}
	if got := report.NewDatasets; !reflect.DeepEqual(got, []string{"A", "B"}) {
		t.Errorf("NewDatasets = %v, want [A B]", got)
	}
	if got := report.AffectedDatasets; !reflect.DeepEqual(got, []string{}) {
		t.Errorf("AffectedDatasets = %v, want []", got)
	}
}

// A<-B<-C<-D; turn B and C into roots in one batch. Only D, which is neither
// new nor directly changed, is affected — once — and the change of B reaches
// D through the pre-change edge B->C even though that edge is removed.
func TestBatchAffectedDownstreams(t *testing.T) {
	report, err := previewJSON(t,
		`{"nodes":[{"name":"A","upstreams":[]},{"name":"B","upstreams":["A"]},{"name":"C","upstreams":["B"]},{"name":"D","upstreams":["C"]}]}`,
		`{"adjustments":[{"name":"B","upstreams":[]},{"name":"C","upstreams":[]}]}`,
	)
	if err != nil {
		t.Fatalf("batch rejected: %v", err)
	}
	if got := report.ChangedDatasets; !reflect.DeepEqual(got, []string{"B", "C"}) {
		t.Errorf("ChangedDatasets = %v, want [B C]", got)
	}
	if got := report.AffectedDatasets; !reflect.DeepEqual(got, []string{"D"}) {
		t.Fatalf("AffectedDatasets = %v, want [D]", got)
	}
	wantRemoved := []Relationship{
		{Upstream: "A", Downstream: "B"},
		{Upstream: "B", Downstream: "C"},
	}
	if got := report.RemovedRelationships; !reflect.DeepEqual(got, wantRemoved) {
		t.Errorf("RemovedRelationships = %v, want %v", got, wantRemoved)
	}
}

// A new dataset inserted into an existing chain A<-C<-D pulls C with it and
// impacts D; A itself (upstream of the new node) is not a downstream.
func TestBatchAffectedThroughNewDataset(t *testing.T) {
	report, err := previewJSON(t,
		`{"nodes":[{"name":"A","upstreams":[]},{"name":"C","upstreams":["A"]},{"name":"D","upstreams":["C"]}]}`,
		`{"adjustments":[{"name":"X","upstreams":["A"]},{"name":"C","upstreams":["X"]}]}`,
	)
	if err != nil {
		t.Fatalf("batch rejected: %v", err)
	}
	if got := report.NewDatasets; !reflect.DeepEqual(got, []string{"X"}) {
		t.Errorf("NewDatasets = %v, want [X]", got)
	}
	if got := report.ChangedDatasets; !reflect.DeepEqual(got, []string{"C"}) {
		t.Errorf("ChangedDatasets = %v, want [C]", got)
	}
	if got := report.AffectedDatasets; !reflect.DeepEqual(got, []string{"D"}) {
		t.Fatalf("AffectedDatasets = %v, want [D]", got)
	}
}

// Reordering upstreams or listing duplicates is not a change. Empty plans and
// applying the same plan twice report empty changes and impacts.
func TestBatchNoOpChanges(t *testing.T) {
	graph := `{"nodes":[{"name":"A","upstreams":[]},{"name":"B","upstreams":[]},{"name":"C","upstreams":["A","B"]}]}`

	t.Run("reordered and duplicated upstreams", func(t *testing.T) {
		report, err := previewJSON(t, graph,
			`{"adjustments":[{"name":"C","upstreams":["B","A","B"]}]}`)
		if err != nil {
			t.Fatalf("batch rejected: %v", err)
		}
		if len(report.ChangedDatasets) != 0 || len(report.AddedRelationships) != 0 || len(report.RemovedRelationships) != 0 {
			t.Fatalf("order/dup-only edit counted as change: %+v", report)
		}
		if len(report.AffectedDatasets) != 0 {
			t.Fatalf("AffectedDatasets = %v, want []", report.AffectedDatasets)
		}
	})

	t.Run("empty plan", func(t *testing.T) {
		report, err := previewJSON(t, graph, `{"adjustments":[]}`)
		if err != nil {
			t.Fatalf("batch rejected: %v", err)
		}
		assertEmptyReportLists(t, report)
	})

	t.Run("same plan applied twice", func(t *testing.T) {
		g := mustParseGraph(t, graph)
		p, err := ParsePlan([]byte(`{"adjustments":[{"name":"B","upstreams":["A"]}]}`), g)
		if err != nil {
			t.Fatalf("ParsePlan: %v", err)
		}
		first, report, err := Apply(g, p)
		if err != nil {
			t.Fatalf("first apply: %v", err)
		}
		if !reflect.DeepEqual(report.ChangedDatasets, []string{"B"}) {
			t.Fatalf("first ChangedDatasets = %v, want [B]", report.ChangedDatasets)
		}
		// Apply again starting from the resulting graph.
		p2, err := ParsePlan([]byte(`{"adjustments":[{"name":"B","upstreams":["A"]}]}`), first)
		if err != nil {
			t.Fatalf("ParsePlan second: %v", err)
		}
		_, report2, err := Apply(first, p2)
		if err != nil {
			t.Fatalf("second apply: %v", err)
		}
		assertEmptyReportLists(t, report2)
	})
}

func assertEmptyReportLists(t *testing.T, report *BatchReport) {
	t.Helper()
	if len(report.NewDatasets) != 0 {
		t.Errorf("NewDatasets = %v, want empty", report.NewDatasets)
	}
	if len(report.ChangedDatasets) != 0 {
		t.Errorf("ChangedDatasets = %v, want empty", report.ChangedDatasets)
	}
	if len(report.AddedRelationships) != 0 {
		t.Errorf("AddedRelationships = %v, want empty", report.AddedRelationships)
	}
	if len(report.RemovedRelationships) != 0 {
		t.Errorf("RemovedRelationships = %v, want empty", report.RemovedRelationships)
	}
	if len(report.AffectedDatasets) != 0 {
		t.Errorf("AffectedDatasets = %v, want empty", report.AffectedDatasets)
	}
}

// Names keep case and internal/outer spacing, and sorting is byte order.
func TestBatchPreservesNamesAndByteOrder(t *testing.T) {
	report, err := previewJSON(t,
		`{"nodes":[{"name":" Raw-Blocks ","upstreams":[]},{"name":"RAW-BLOCKS","upstreams":[" Raw-Blocks "]}]}`,
		`{"adjustments":[{"name":"beta","upstreams":["RAW-BLOCKS"]},{"name":"Alpha","upstreams":[" Raw-Blocks "]}]}`,
	)
	if err != nil {
		t.Fatalf("batch rejected: %v", err)
	}
	// Byte order: ' ' (32) < 'R' (82) < uppercase letters... names sorted:
	// " Raw-Blocks ", "Alpha", "RAW-BLOCKS", "beta".
	wantNames := []string{" Raw-Blocks ", "Alpha", "RAW-BLOCKS", "beta"}
	var gotNames []string
	for _, node := range report.FinalGraph.Nodes {
		gotNames = append(gotNames, node.Name)
	}
	if !reflect.DeepEqual(gotNames, wantNames) {
		t.Fatalf("final node order = %v, want %v", gotNames, wantNames)
	}
	if got := report.FinalGraph.Nodes[2].Upstreams; !reflect.DeepEqual(got, []string{" Raw-Blocks "}) {
		t.Fatalf("name casing/spacing altered: %v", got)
	}
}

// Relationships sort by upstream first, then downstream.
func TestBatchRelationshipsSortedUpstreamThenDownstream(t *testing.T) {
	report, err := previewJSON(t,
		`{"nodes":[{"name":"A","upstreams":[]},{"name":"B","upstreams":[]},{"name":"C","upstreams":[]}]}`,
		`{"adjustments":[{"name":"X","upstreams":["C","A","B"]}]}`,
	)
	if err != nil {
		t.Fatalf("batch rejected: %v", err)
	}
	want := []Relationship{
		{Upstream: "A", Downstream: "X"},
		{Upstream: "B", Downstream: "X"},
		{Upstream: "C", Downstream: "X"},
	}
	if got := report.AddedRelationships; !reflect.DeepEqual(got, want) {
		t.Fatalf("AddedRelationships = %v, want %v", got, want)
	}
}

// Same graph and plan with different record/upstream orders produce identical
// report bytes and identical written-back graph bytes.
func TestBatchDeterministicBytes(t *testing.T) {
	graphs := []string{
		`{"nodes":[{"name":"B","upstreams":["A"]},{"name":"A","upstreams":[]}]}`,
		`{"nodes":[{"name":"A","upstreams":[]},{"name":"B","upstreams":["A"]}]}`,
	}
	plans := []string{
		`{"adjustments":[{"name":"C","upstreams":["B","A"]},{"name":"A","upstreams":["B"]},{"name":"B","upstreams":[]}]}`,
		`{"adjustments":[{"name":"B","upstreams":[]},{"name":"A","upstreams":["B"]},{"name":"C","upstreams":["A","B"]}]}`,
	}
	var reference []byte
	for gi, gs := range graphs {
		for pi, ps := range plans {
			g := mustParseGraph(t, gs)
			p, err := ParsePlan([]byte(ps), g)
			if err != nil {
				t.Fatalf("g%d p%d ParsePlan: %v", gi, pi, err)
			}
			final, report, err := Apply(g, p)
			if err != nil {
				t.Fatalf("g%d p%d apply: %v", gi, pi, err)
			}
			graphBytes, err := MarshalGraph(final)
			if err != nil {
				t.Fatalf("marshal graph: %v", err)
			}
			reportBytes, err := MarshalReport(report)
			if err != nil {
				t.Fatalf("marshal report: %v", err)
			}
			if !bytes.Contains(reportBytes, []byte(`[]`)) {
				t.Error("empty lists must render as []")
			}
			combined := append(append([]byte{}, graphBytes...), reportBytes...)
			if reference == nil {
				reference = combined
			} else if !bytes.Equal(combined, reference) {
				t.Fatalf("g%d p%d bytes differ:\n%s", gi, pi, combined)
			}
		}
	}
}

// Datasets absent from the plan keep their upstreams untouched.
func TestBatchUntouchedDatasetsPreserved(t *testing.T) {
	report, err := previewJSON(t,
		`{"nodes":[{"name":"A","upstreams":[]},{"name":"B","upstreams":["A"]},{"name":"C","upstreams":["B"]}]}`,
		`{"adjustments":[{"name":"A","upstreams":["C"]}]}`,
	)
	if err == nil {
		t.Fatalf("expected cycle rejection, got report: %+v", report)
	}
	// Same change but legal: repoint A at a fresh root D; B and C untouched.
	report, err = previewJSON(t,
		`{"nodes":[{"name":"A","upstreams":[]},{"name":"B","upstreams":["A"]},{"name":"C","upstreams":["B"]}]}`,
		`{"adjustments":[{"name":"D","upstreams":[]},{"name":"A","upstreams":["D"]}]}`,
	)
	if err != nil {
		t.Fatalf("batch rejected: %v", err)
	}
	wantFinal := GraphJSON{Nodes: []GraphNode{
		{Name: "A", Upstreams: []string{"D"}},
		{Name: "B", Upstreams: []string{"A"}},
		{Name: "C", Upstreams: []string{"B"}},
		{Name: "D", Upstreams: []string{}},
	}}
	if !reflect.DeepEqual(report.FinalGraph, wantFinal) {
		t.Fatalf("FinalGraph = %#v, want %#v", report.FinalGraph, wantFinal)
	}
	if got := report.AffectedDatasets; !reflect.DeepEqual(got, []string{"B", "C"}) {
		t.Fatalf("AffectedDatasets = %v, want [B C]", got)
	}
}
