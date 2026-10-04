package main

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/asdhoaiqqq/chainledger-governance/chainledger"
)

// End-to-end (file in, file out) regression coverage for the shared-intermediate
// batch-delete scenario, exercising the public preview/apply commands exactly
// as the README documents:
//
//	R -> M -> B -> H -> T
//	     \-> C -/   (H converges on B and C)
//	S (root)       U -> V (isolated component)
//
// The successful batch deletes M and B, repoints C at R, and repoints H at C
// and S. These tests pin that preview is strictly read-only, that apply writes
// exactly the final graph the report describes, that the refusal case (H left
// dangling at deleted B) rejects the whole batch for BOTH commands without
// letting C's legal repoint take effect, and that record/upstream ordering and
// repeated upstreams do not change a byte of the success output.

const convergenceGraphFile = `{"datasets":[
	{"name":"R","upstreams":[]},
	{"name":"S","upstreams":[]},
	{"name":"M","upstreams":["R"]},
	{"name":"B","upstreams":["M"]},
	{"name":"C","upstreams":["M"]},
	{"name":"H","upstreams":["B","C"]},
	{"name":"T","upstreams":["H"]},
	{"name":"U","upstreams":[]},
	{"name":"V","upstreams":["U"]}
]}`

const convergencePlanFile = `{
	"changes":[
		{"name":"C","upstreams":["R"]},
		{"name":"H","upstreams":["C","S"]}
	],
	"removals":["M","B"]
}`

// convergenceDanglingPlanFile forgets H's adjustment: after M and B are
// deleted, H still references B directly, so the whole batch must be rejected.
const convergenceDanglingPlanFile = `{
	"changes":[
		{"name":"C","upstreams":["R"]}
	],
	"removals":["M","B"]
}`

// convergenceShuffledPlanFile carries the same semantics as the canonical
// plan with records, removal entries, and upstream names reordered, H's
// upstreams repeated, and extra JSON whitespace.
const convergenceShuffledPlanFile = `{"removals" : ["B", "M"], "changes":[
	{"upstreams":["S","C","S","C"],"name":"H"},
	{"name":"C","upstreams":["R","R"]}
]}`

// convergenceShuffledGraphFile lists the same graph with shuffled records,
// shuffled and repeated upstreams, and different whitespace.
const convergenceShuffledGraphFile = `{"datasets":[
	{"upstreams":["U"],"name":"V"},
	{"upstreams":["H"],"name":"T"},
	{"upstreams":[],"name":"U"},
	{"upstreams":["C","B","B"],"name":"H"},
	{"name":"C","upstreams":["M"]},
	{"upstreams":["M"],"name":"B"},
	{"name":"S","upstreams":[]},
	{"name":"M","upstreams":["R"]},
	{"name":"R","upstreams":[]}
]}`

// parseBatchReport decodes a preview/apply stdout report.
func parseBatchReport(t *testing.T, stdout string) *chainledger.BatchReport {
	t.Helper()
	var report chainledger.BatchReport
	if err := json.Unmarshal([]byte(stdout), &report); err != nil {
		t.Fatalf("report stdout is not valid JSON: %v\n%s", err, stdout)
	}
	return &report
}

// assertConvergenceSuccessReport pins the complete classification of the
// successful batch.
func assertConvergenceSuccessReport(t *testing.T, report *chainledger.BatchReport) {
	t.Helper()
	if got, want := report.RemovedDatasets, []string{"B", "M"}; !reflect.DeepEqual(got, want) {
		t.Errorf("removedDatasets = %v, want %v", got, want)
	}
	if got, want := report.ChangedDatasets, []string{"C", "H"}; !reflect.DeepEqual(got, want) {
		t.Errorf("changedDatasets = %v, want %v", got, want)
	}
	if len(report.NewDatasets) != 0 {
		t.Errorf("newDatasets = %v, want []", report.NewDatasets)
	}
	// T is affected exactly once through the converging branches; H is a
	// direct change and must not be listed here.
	if got, want := report.AffectedDownstreams, []string{"T"}; !reflect.DeepEqual(got, want) {
		t.Errorf("affectedDownstreams = %v, want %v", got, want)
	}
	if got, want := report.RemovedRelations, []chainledger.Relation{
		{Upstream: "B", Downstream: "H"},
		{Upstream: "M", Downstream: "B"},
		{Upstream: "M", Downstream: "C"},
		{Upstream: "R", Downstream: "M"},
	}; !reflect.DeepEqual(got, want) {
		t.Errorf("removedRelations = %v, want %v", got, want)
	}
	if got, want := report.AddedRelations, []chainledger.Relation{
		{Upstream: "R", Downstream: "C"},
		{Upstream: "S", Downstream: "H"},
	}; !reflect.DeepEqual(got, want) {
		t.Errorf("addedRelations = %v, want %v", got, want)
	}
	wantFinal := chainledger.GraphFile{Datasets: []chainledger.GraphDataset{
		{Name: "C", Upstreams: []string{"R"}},
		{Name: "H", Upstreams: []string{"C", "S"}},
		{Name: "R", Upstreams: []string{}},
		{Name: "S", Upstreams: []string{}},
		{Name: "T", Upstreams: []string{"H"}},
		{Name: "U", Upstreams: []string{}},
		{Name: "V", Upstreams: []string{"U"}},
	}}
	if !reflect.DeepEqual(report.FinalGraph, wantFinal) {
		t.Errorf("finalGraph = %+v, want %+v", report.FinalGraph, wantFinal)
	}
}

// TestCLISharedIntermediatePreviewIsReadOnly: preview prints the full success
// report but the graph file stays byte-for-byte as submitted — nodes and
// bidirectional relations untouched.
func TestCLISharedIntermediatePreviewIsReadOnly(t *testing.T) {
	graphPath := writeFile(t, "graph.json", convergenceGraphFile)
	planPath := writeFile(t, "plan.json", convergencePlanFile)
	original := readFile(t, graphPath)

	stdout, stderr, exit := captureStdout(t, func() int {
		return run([]string{"preview", graphPath, planPath})
	})
	if exit != 0 {
		t.Fatalf("preview exit = %d, stderr = %s", exit, stderr)
	}
	assertConvergenceSuccessReport(t, parseBatchReport(t, stdout))

	if got := readFile(t, graphPath); got != original {
		t.Fatalf("preview modified the graph file:\n got %s\nwant %s", got, original)
	}
}

// TestCLISharedIntermediateApplyWritesReportedFinalGraph: apply commits the
// batch; the graph rewritten to disk parses to exactly the finalGraph carried
// in the report, and the preview and apply reports are identical.
func TestCLISharedIntermediateApplyWritesReportedFinalGraph(t *testing.T) {
	previewGraphPath := writeFile(t, "preview-graph.json", convergenceGraphFile)
	planPath := writeFile(t, "plan.json", convergencePlanFile)
	previewOut, stderr, exit := captureStdout(t, func() int {
		return run([]string{"preview", previewGraphPath, planPath})
	})
	if exit != 0 {
		t.Fatalf("preview exit = %d, stderr = %s", exit, stderr)
	}
	previewReport := parseBatchReport(t, previewOut)

	applyGraphPath := writeFile(t, "apply-graph.json", convergenceGraphFile)
	applyOut, stderr, exit := captureStdout(t, func() int {
		return run([]string{"apply", applyGraphPath, planPath})
	})
	if exit != 0 {
		t.Fatalf("apply exit = %d, stderr = %s", exit, stderr)
	}
	applyReport := parseBatchReport(t, applyOut)
	if !reflect.DeepEqual(previewReport, applyReport) {
		t.Fatalf("preview and apply reports differ:\n preview=%#v\n apply=%#v", previewReport, applyReport)
	}
	assertConvergenceSuccessReport(t, applyReport)

	// The written graph file is exactly the report's final graph (and contains
	// no trace of M or B).
	written, err := chainledger.UnmarshalGraphFile([]byte(readFile(t, applyGraphPath)))
	if err != nil {
		t.Fatalf("written graph file is invalid: %v", err)
	}
	if _, ok := written["M"]; ok {
		t.Errorf("M still present in written graph file")
	}
	if _, ok := written["B"]; ok {
		t.Errorf("B still present in written graph file")
	}
	writtenReport, err := chainledger.MarshalGraphFile(written)
	if err != nil {
		t.Fatalf("marshal written graph: %v", err)
	}
	finalBytes, err := json.MarshalIndent(applyReport.FinalGraph, "", "  ")
	if err != nil {
		t.Fatalf("marshal report final graph: %v", err)
	}
	if string(writtenReport) != string(finalBytes) {
		t.Fatalf("written graph differs from report finalGraph:\n written:\n%s\n report:\n%s", writtenReport, finalBytes)
	}
	// The isolated U -> V component is intact in the written file.
	if got := written["V"].Parents; !reflect.DeepEqual(got, []string{"U"}) {
		t.Errorf("V.Parents = %v, want [U]", got)
	}
}

// TestCLISharedIntermediateDanglingReferenceRejectsBatch: when H is not
// repointed and still references deleted B, both preview and apply must fail,
// naming H and B, printing no success report, and leaving the graph file
// byte-for-byte untouched (so C's otherwise-legal repoint never lands).
func TestCLISharedIntermediateDanglingReferenceRejectsBatch(t *testing.T) {
	for _, command := range []string{"preview", "apply"} {
		t.Run(command, func(t *testing.T) {
			graphPath := writeFile(t, "graph.json", convergenceGraphFile)
			planPath := writeFile(t, "plan.json", convergenceDanglingPlanFile)
			original := readFile(t, graphPath)

			stdout, stderr, exit := captureStdout(t, func() int {
				return run([]string{command, graphPath, planPath})
			})
			if exit == 0 {
				t.Fatalf("%s exit = 0, want non-zero for dangling H -> B", command)
			}
			if stdout != "" {
				t.Errorf("%s stdout = %q, want empty on rejection", command, stdout)
			}
			if !strings.Contains(stderr, "H") || !strings.Contains(stderr, "B") {
				t.Errorf("%s stderr = %q, want error naming referrer H and deleted B", command, stderr)
			}
			if got := readFile(t, graphPath); got != original {
				t.Fatalf("%s modified the graph file on rejection:\n got %s\nwant %s", command, got, original)
			}
			// The preserved file still parses with C on M and H on B and C.
			graph, err := chainledger.UnmarshalGraphFile([]byte(original))
			if err != nil {
				t.Fatalf("preserved graph file is invalid: %v", err)
			}
			if got := graph["C"].Parents; !reflect.DeepEqual(got, []string{"M"}) {
				t.Errorf("C.Parents = %v, want [M] (legal repoint must not apply early)", got)
			}
			if got := graph["H"].Parents; !reflect.DeepEqual(got, []string{"B", "C"}) {
				t.Errorf("H.Parents = %v, want [B C]", got)
			}
		})
	}
}

// TestCLISharedIntermediateDeterministicBytes: semantically identical graph
// and plan inputs that differ only in record order, upstream order, repeated
// upstreams, and JSON whitespace must produce byte-identical preview reports
// and byte-identical applied graph files.
func TestCLISharedIntermediateDeterministicBytes(t *testing.T) {
	canonicalGraph := writeFile(t, "graph-canonical.json", convergenceGraphFile)
	shuffledGraph := writeFile(t, "graph-shuffled.json", convergenceShuffledGraphFile)
	canonicalPlan := writeFile(t, "plan-canonical.json", convergencePlanFile)
	shuffledPlan := writeFile(t, "plan-shuffled.json", convergenceShuffledPlanFile)

	runPreview := func(graphPath, planPath string) string {
		t.Helper()
		stdout, stderr, exit := captureStdout(t, func() int {
			return run([]string{"preview", graphPath, planPath})
		})
		if exit != 0 {
			t.Fatalf("preview exit = %d, stderr = %s", exit, stderr)
		}
		return stdout
	}
	canonicalReport := runPreview(canonicalGraph, canonicalPlan)
	assertConvergenceSuccessReport(t, parseBatchReport(t, canonicalReport))

	for name, paths := range map[string][2]string{
		"shuffled plan only":    {canonicalGraph, shuffledPlan},
		"shuffled graph only":   {shuffledGraph, canonicalPlan},
		"shuffled graph & plan": {shuffledGraph, shuffledPlan},
	} {
		t.Run(name, func(t *testing.T) {
			if got := runPreview(paths[0], paths[1]); got != canonicalReport {
				t.Fatalf("preview bytes differ for semantically equal inputs:\n--- got ---\n%s\n--- want ---\n%s", got, canonicalReport)
			}
		})
	}

	// Apply must also write byte-identical graph files.
	applyGraph := writeFile(t, "graph-apply.json", convergenceShuffledGraphFile)
	_, stderr, exit := captureStdout(t, func() int {
		return run([]string{"apply", applyGraph, shuffledPlan})
	})
	if exit != 0 {
		t.Fatalf("apply exit = %d, stderr = %s", exit, stderr)
	}
	// Apply the canonical inputs in a separate file and compare the on-disk
	// bytes directly.
	canonicalApplyGraph := writeFile(t, "graph-apply-canonical.json", convergenceGraphFile)
	_, stderr, exit = captureStdout(t, func() int {
		return run([]string{"apply", canonicalApplyGraph, canonicalPlan})
	})
	if exit != 0 {
		t.Fatalf("canonical apply exit = %d, stderr = %s", exit, stderr)
	}
	if got, want := readFile(t, applyGraph), readFile(t, canonicalApplyGraph); got != want {
		t.Fatalf("applied graph files differ:\n--- shuffled ---\n%s\n--- canonical ---\n%s", got, want)
	}
}
