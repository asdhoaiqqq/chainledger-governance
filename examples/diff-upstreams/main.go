// Command diff-upstreams-example demonstrates the chainledger lineage
// library's upstream lineage comparison: two lineage JSON documents — the
// lineage as it stood before and after a change — are compared for one
// target dataset, and the direct dependencies added and removed in the
// target's complete upstream scope are printed.
//
// This is a library feature, not a command-line query: the documents are
// fixed in code. Run it from the repository root with:
//
//	go run ./examples/diff-upstreams
package main

import (
	"fmt"
	"os"

	"github.com/asdhoaiqqq/chainledger-governance/chainledger"
)

func main() {
	// Both documents describe the same pipeline at two points in time:
	// report derives from raw directly and through raw -> mid -> report;
	// view is report's downstream and isolated stands alone. In the after
	// document the mid -> report edge is gone, so mid no longer participates
	// in report's derivation at all — and raw -> mid leaves the compared
	// scope with it, even though the edge itself still exists in the
	// document. The unrelated rewire of isolated -> view and the new
	// isolated -> fresh branch never touch report's upstream scope.
	before := `{"nodes":["raw","mid","report","view","isolated"],` +
		`"edges":[{"from":"raw","to":"mid"},{"from":"mid","to":"report"},` +
		`{"from":"raw","to":"report"},{"from":"report","to":"view"}]}`
	after := `{"nodes":["raw","mid","report","view","isolated","fresh"],` +
		`"edges":[{"from":"raw","to":"mid"},{"from":"raw","to":"report"},` +
		`{"from":"isolated","to":"view"},{"from":"isolated","to":"fresh"}]}`

	diff, err := chainledger.DiffUpstreamLineage(before, after, "report")
	if err != nil {
		fmt.Fprintf(os.Stderr, "diff: %v\n", err)
		os.Exit(1)
	}
	fmt.Println("DiffUpstreamLineage(before, after, \"report\"):")
	fmt.Printf("  added:   %v\n", diff.Added)
	fmt.Println("  removed:")
	for _, edge := range diff.Removed {
		fmt.Printf("    %s -> %s\n", edge.From, edge.To)
	}

	// Comparing a document with itself is a successful no-change result:
	// both groups are empty (non-nil), not an error.
	same, err := chainledger.DiffUpstreamLineage(before, before, "report")
	if err != nil {
		fmt.Fprintf(os.Stderr, "diff identical documents: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("\nno-change comparison: added=%d removed=%d err=%v\n",
		len(same.Added), len(same.Removed), err)

	// Failures: an empty target, a target missing from one document, and a
	// document rejected by import validation — each error says which side
	// caused it, and no partial diff is returned.
	fail := func(label, beforeDoc, afterDoc, target string) {
		if _, err := chainledger.DiffUpstreamLineage(beforeDoc, afterDoc, target); err != nil {
			fmt.Printf("%s: %v\n", label, err)
		}
	}
	fmt.Println()
	fail("empty target", before, after, "")
	fail("target missing in the after document", before, `{"nodes":["raw"],"edges":[]}`, "report")
	fail("after document has a cycle", before,
		`{"nodes":["a","b"],"edges":[{"from":"a","to":"b"},{"from":"b","to":"a"}]}`, "report")
}
