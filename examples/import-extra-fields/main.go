// Command import-extra-fields-example demonstrates how the chainledger
// lineage library's ImportLineage treats fields beyond the declared document
// shape: unknown top-level fields and unknown edge fields (statistics, notes,
// case variants of the endpoint keys) never become nodes or dependencies and
// are not stored, while document-level JSON validity — including duplicated
// keys inside ignored fields — is still enforced.
//
// This is a library feature, not a command-line query: the lineage text is
// fixed in code. Run it from the repository root with:
//
//	go run ./examples/import-extra-fields
package main

import (
	"fmt"
	"os"
	"strings"

	"github.com/asdhoaiqqq/chainledger-governance/chainledger"
)

func main() {
	// One correct document: nodes lists exactly raw and report, and the edge
	// carries the lowercase endpoints from -> raw, to -> report. Everything
	// else is extra information riding along on the same document:
	//   - "From": "ghost" is a case variant of the endpoint key. Only keys
	//     that decode to precisely "from" and "to" set the endpoints, so this
	//     is an unknown field: ghost is never looked up and never becomes a
	//     node, and the variant neither overwrites nor replaces the real
	//     lowercase endpoint.
	//   - "note" and "stats" on the edge and "meta" at the top level hold
	//     nested objects, arrays and numbers. No name inside them (ghost
	//     again) is read as a dataset or a dependency, and 1e400 is a legal
	//     JSON number literal even though it overflows float64.
	doc := `{
  "nodes": ["raw", "report"],
  "edges": [
    {
      "from": "raw",
      "to": "report",
      "From": "ghost",
      "note": {"owner": "quality", "see": ["ghost"]},
      "stats": {"rows": 1e400}
    }
  ],
  "meta": {"title": "nightly export", "datasets": ["ghost"], "totals": {"rows": 1e400}}
}`

	graph, err := chainledger.ImportLineage(doc)
	if err != nil {
		fmt.Fprintf(os.Stderr, "import: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("import ok: %d dataset(s)\n", len(graph))
	if _, ok := graph["ghost"]; ok {
		fmt.Fprintln(os.Stderr, "ghost unexpectedly became a node")
		os.Exit(1)
	}
	fmt.Println(`ghost is not a node: the unknown "From" field was ignored`)

	// The lineage is exactly the declared edge: report's only upstream is raw,
	// at distance 1 along the path [raw report].
	upstreams, err := chainledger.Upstreams(graph, "report")
	if err != nil {
		fmt.Fprintf(os.Stderr, "upstreams: %v\n", err)
		os.Exit(1)
	}
	fmt.Println("\nUpstreams(report) on the imported graph:")
	for _, up := range upstreams {
		fmt.Printf("  %-7s distance=%d path=%v\n", up.Dataset, up.Distance, up.Path)
	}

	// Re-exporting shows what the graph actually holds: the declared nodes and
	// the direct dependency, nothing else. Extension fields are never stored,
	// so they cannot reappear in the export.
	exported, err := chainledger.ExportUpstreamLineage(graph, "report")
	if err != nil {
		fmt.Fprintf(os.Stderr, "export: %v\n", err)
		os.Exit(1)
	}
	fmt.Println("\nre-exported lineage (extension fields are gone):")
	fmt.Println(exported)

	// The same key written with a legal Unicode escape still decodes to
	// "from", so this variant of the document imports identically. (Writing
	// both spellings in one object would be a duplicated key and fail.)
	escaped := strings.Replace(doc, `"from": "raw"`, `"\u0066rom": "raw"`, 1)
	if _, err := chainledger.ImportLineage(escaped); err != nil {
		fmt.Fprintf(os.Stderr, "escaped-key import unexpectedly failed: %v\n", err)
		os.Exit(1)
	}
	fmt.Println(`"\u0066rom" decodes to "from": import ok`)

	// Removing the lowercase "from" and keeping only the "From" variant leaves
	// the edge without an upstream endpoint: the variant cannot stand in for
	// the missing endpoint, and the whole import fails with a nil graph.
	missing := strings.Replace(doc, `"from": "raw",`, "", 1)
	g, err := chainledger.ImportLineage(missing)
	fmt.Printf("\nedge with only \"From\" refused: graph==nil is %v, err=%v\n", g == nil, err)

	// Ignored fields are still part of the JSON document: a duplicated key
	// inside a nested unknown object fails the whole import, again nil graph.
	dup := strings.Replace(doc, `"owner": "quality"`, `"owner": "quality", "owner": "ops"`, 1)
	g, err = chainledger.ImportLineage(dup)
	fmt.Printf("duplicated key inside \"note\" refused: graph==nil is %v, err=%v\n", g == nil, err)
}
