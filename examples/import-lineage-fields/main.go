// Command import-lineage-fields-example demonstrates what a lineage JSON
// document contributes to the imported graph when it carries more than nodes
// and edges: statistics, notes and case-variant endpoint spellings. Only the
// declared nodes and the lowercase "from"/"to" endpoints become lineage;
// everything else is validated purely as JSON syntax and then discarded.
//
// This is a library feature, not a command-line query: the lineage text is
// fixed in code. Run it from the repository root with:
//
//	go run ./examples/import-lineage-fields
package main

import (
	"fmt"
	"os"
	"strings"

	"github.com/asdhoaiqqq/chainledger-governance/chainledger"
)

func main() {
	// One valid document. nodes declares exactly two datasets: raw derives
	// report. The edge names its endpoints with the lowercase "from"/"to"
	// keys, and additionally carries:
	//   - a capitalized "From" pointing at "ghost", a dataset nodes never
	//     registers (a case variant is an unknown field, not an endpoint);
	//   - nested notes, including dataset-looking names and from/to-looking
	//     objects, which stay annotation data at every depth;
	//   - statistics written as 1e400 / 1e-400: legal JSON numbers far beyond
	//     the float64 range, accepted on their syntax alone.
	// The top level carries its own nested notes and statistics.
	doc := `{
  "nodes": ["raw", "report"],
  "edges": [
    {
      "from": "raw",
      "to": "report",
      "From": "ghost",
      "note": {
        "author": "ingest-job",
        "trace": ["ghost", {"from": "ghost", "to": "phantom"}]
      },
      "stats": {"rows": 1e400, "ratio": 1e-400}
    }
  ],
  "note": {"batch": "2026-10-07", "extra": [1, 2, {"from": "ghost", "to": "phantom"}]},
  "stats": {"totalRows": 1e400}
}`

	graph, err := chainledger.ImportLineage(doc)
	if err != nil {
		fmt.Fprintf(os.Stderr, "import: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("import ok; datasets in graph: %d\n", len(graph))
	fmt.Printf("  report parents=%v\n", graph["report"].Parents)
	fmt.Printf("  raw    children=%v\n", graph["raw"].Children)

	// The source query sees raw alone: distance 1, path [raw report].
	upstreams, err := chainledger.Upstreams(graph, "report")
	if err != nil {
		fmt.Fprintf(os.Stderr, "upstreams: %v\n", err)
		os.Exit(1)
	}
	fmt.Println("\nUpstreams(report):")
	for _, up := range upstreams {
		fmt.Printf("  %-5s distance=%d path=%v\n", up.Dataset, up.Distance, up.Path)
	}

	// The capitalized "From" named ghost, and every name seen only inside
	// notes, never became a node.
	if _, err := chainledger.Impacts(graph, "ghost"); err != nil {
		fmt.Printf("\nImpacts(ghost) error: %v\n", err)
	}

	// Re-exporting proves the extension fields are not stored or emitted: only
	// the declared node pair and its one direct dependency come back.
	out, err := chainledger.ExportUpstreamLineage(graph, "report")
	if err != nil {
		fmt.Fprintf(os.Stderr, "export: %v\n", err)
		os.Exit(1)
	}
	fmt.Println("\nExportUpstreamLineage(report):")
	fmt.Println(out)

	// Two documents derived by editing the same text must be rejected.
	attempt := func(label, mutated string) {
		fmt.Printf("\n%s\n", label)
		g, err := chainledger.ImportLineage(mutated)
		fmt.Printf("  graph is nil: %v\n", g == nil)
		if err != nil {
			fmt.Printf("  refused: %v\n", err)
			return
		}
		fmt.Fprintf(os.Stderr, "  unexpectedly imported %v\n", g)
		os.Exit(1)
	}

	// Variant 1: drop the lowercase "from" but keep the capitalized "From".
	// A case variant cannot stand in for the missing endpoint, so the edge is
	// rejected as missing its upstream endpoint; ghost is never connected.
	missingFrom := strings.Replace(doc, `      "from": "raw",`+"\n", "", 1)
	attempt(`variant 1: lowercase "from" removed, capitalized "From" stays`, missingFrom)

	// Variant 2: repeat a key inside the nested top-level note object. The
	// whole document still has to be valid JSON with no duplicated key, so the
	// import fails even though the offending field is purely annotation data.
	dupNested := strings.Replace(doc,
		`"note": {"batch": "2026-10-07",`,
		`"note": {"batch": "2026-10-07", "batch": "again",`, 1)
	attempt("variant 2: a key repeated inside the nested note object", dupNested)
}
