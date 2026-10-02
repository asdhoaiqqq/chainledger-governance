package main

import (
	"strings"
	"testing"

	"github.com/asdhoaiqqq/chainledger-governance/chainledger"
)

// TestCLIRemovalPreviewKeepsGraphFile verifies preview reports removals and
// never touches the graph file.
func TestCLIRemovalPreviewKeepsGraphFile(t *testing.T) {
	graphPath := writeFile(t, "graph.json", `{"datasets":[{"name":"A","upstreams":[]},{"name":"B","upstreams":["A"]},{"name":"C","upstreams":["B"]}]}`)
	planPath := writeFile(t, "plan.json", `{"changes":[{"name":"B","upstreams":["D"]},{"name":"D","upstreams":[]}],"removals":["A"]}`)

	original := readFile(t, graphPath)
	stdout, stderr, exit := captureStdout(t, func() int {
		return run([]string{"preview", graphPath, planPath})
	})
	if exit != 0 {
		t.Fatalf("preview exit = %d, stderr = %s", exit, stderr)
	}
	for _, want := range []string{`"removedDatasets": [`, `"A"`, `"affectedDownstreams"`, `"C"`} {
		if !strings.Contains(stdout, want) {
			t.Errorf("preview stdout %q missing %s", stdout, want)
		}
	}
	if got := readFile(t, graphPath); got != original {
		t.Errorf("graph file changed after preview:\n got %s\nwant %s", got, original)
	}
}

// TestCLIRemovalApplyWritesGraphBackAndIsIdempotent verifies apply removes the
// dataset from the written graph and a second apply of the same plan succeeds
// with an empty actual-removal list.
func TestCLIRemovalApplyWritesGraphBackAndIsIdempotent(t *testing.T) {
	graphPath := writeFile(t, "graph.json", `{"datasets":[{"name":"A","upstreams":[]},{"name":"B","upstreams":["A"]},{"name":"C","upstreams":["B"]}]}`)
	planPath := writeFile(t, "plan.json", `{"changes":[{"name":"B","upstreams":["D"]},{"name":"D","upstreams":[]}],"removals":["A"]}`)

	stdout, stderr, exit := captureStdout(t, func() int {
		return run([]string{"apply", graphPath, planPath})
	})
	if exit != 0 {
		t.Fatalf("apply exit = %d, stderr = %s", exit, stderr)
	}
	if !strings.Contains(stdout, `"removedDatasets"`) {
		t.Errorf("apply stdout missing removedDatasets: %s", stdout)
	}

	written := readFile(t, graphPath)
	graph, err := chainledger.UnmarshalGraphFile([]byte(written))
	if err != nil {
		t.Fatalf("written graph is invalid: %v", err)
	}
	if _, ok := graph["A"]; ok {
		t.Errorf("A still present in written graph: %s", written)
	}
	if got := graph["B"].Parents; len(got) != 1 || got[0] != "D" {
		t.Errorf("B.Parents = %v, want [D]", got)
	}
	if got := graph["C"].Parents; len(got) != 1 || got[0] != "B" {
		t.Errorf("C.Parents = %v, want [B] (unadjusted downstream retained)", got)
	}

	// Re-apply: A no longer exists, D/B already settled, so every change and
	// impact list is empty and the graph bytes stay identical.
	stdout2, stderr2, exit2 := captureStdout(t, func() int {
		return run([]string{"apply", graphPath, planPath})
	})
	if exit2 != 0 {
		t.Fatalf("second apply exit = %d, stderr = %s", exit2, stderr2)
	}
	for _, want := range []string{`"newDatasets": []`, `"changedDatasets": []`, `"removedDatasets": []`, `"addedRelations": []`, `"removedRelations": []`, `"affectedDownstreams": []`} {
		if !strings.Contains(stdout2, want) {
			t.Errorf("second apply stdout %q missing empty list %s", stdout2, want)
		}
	}
	if got := readFile(t, graphPath); got != written {
		t.Errorf("idempotent re-apply changed graph bytes")
	}
}

// TestCLIRemovalRejectionPreservesGraphFile verifies that deleting an upstream
// without adjusting the retained downstream fails, keeps the original file
// byte-for-byte, and prints no success report.
func TestCLIRemovalRejectionPreservesGraphFile(t *testing.T) {
	graphPath := writeFile(t, "graph.json", `{"datasets":[{"name":"A","upstreams":[]},{"name":"B","upstreams":["A"]}]}`)
	planPath := writeFile(t, "plan.json", `{"removals":["A"]}`)

	original := readFile(t, graphPath)
	stdout, stderr, exit := captureStdout(t, func() int {
		return run([]string{"apply", graphPath, planPath})
	})
	if exit == 0 {
		t.Fatalf("apply expected non-zero exit for dangling reference, got 0")
	}
	if !strings.Contains(stderr, "A") || !strings.Contains(stderr, "B") {
		t.Errorf("stderr = %q, must name referrer B and referenced A", stderr)
	}
	if stdout != "" {
		t.Errorf("stdout = %q, want empty on rejection", stdout)
	}
	if got := readFile(t, graphPath); got != original {
		t.Errorf("graph file changed after rejected apply:\n got %s\nwant %s", got, original)
	}
}

// TestCLIRemovalMalformedPlan verifies the CLI rejects duplicate/overlapping
// removal declarations with a non-zero exit and an untouched graph.
func TestCLIRemovalMalformedPlan(t *testing.T) {
	graphPath := writeFile(t, "graph.json", `{"datasets":[{"name":"A","upstreams":[]}]}`)
	planPath := writeFile(t, "plan.json", `{"changes":[{"name":"A","upstreams":[]}],"removals":["A"]}`)

	_, stderr, exit := captureStdout(t, func() int {
		return run([]string{"apply", graphPath, planPath})
	})
	if exit == 0 {
		t.Fatalf("expected non-zero exit for changes/removals overlap, got 0")
	}
	if !strings.Contains(stderr, "both changes and removals") {
		t.Errorf("stderr = %q, want overlap error", stderr)
	}
}

// TestCLIRemoveAllDatasets verifies a removals-only plan can empty the graph
// file into a legal {"datasets": []}.
func TestCLIRemoveAllDatasets(t *testing.T) {
	graphPath := writeFile(t, "graph.json", `{"datasets":[{"name":"A","upstreams":[]},{"name":"B","upstreams":["A"]}]}`)
	planPath := writeFile(t, "plan.json", `{"removals":["B","A"]}`)

	stdout, stderr, exit := captureStdout(t, func() int {
		return run([]string{"apply", graphPath, planPath})
	})
	if exit != 0 {
		t.Fatalf("apply exit = %d, stderr = %s", exit, stderr)
	}
	if !strings.Contains(stdout, `"removedDatasets": [
    "A",
    "B"
  ]`) {
		t.Errorf("stdout missing sorted removedDatasets: %s", stdout)
	}
	written := readFile(t, graphPath)
	graph, err := chainledger.UnmarshalGraphFile([]byte(written))
	if err != nil {
		t.Fatalf("written graph is invalid: %v", err)
	}
	if len(graph) != 0 {
		t.Errorf("graph still has %d datasets, want 0", len(graph))
	}
}
