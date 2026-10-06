// Command import-lineage-example demonstrates the chainledger lineage
// library's lineage JSON import: take the JSON text produced by the lineage
// exporters and turn it back into an independent in-memory lineage graph that
// supports the same registrations and queries as any freshly built one.
//
// This is a library feature, not a command-line query: the JSON documents are
// fixed in code. Run it from the repository root with:
//
//	go run ./examples/import-lineage
package main

import (
	"fmt"
	"os"

	"github.com/asdhoaiqqq/chainledger-governance/chainledger"
)

func main() {
	// Build a lineage in one graph, then hand its exported JSON text to a
	// different part of the program as if it had arrived from another program.
	origin := map[string]*chainledger.Lineage{}
	register := func(graph map[string]*chainledger.Lineage, name string, parents ...string) {
		if err := chainledger.Register(graph, chainledger.Dataset{Name: name}, parents); err != nil {
			fmt.Fprintf(os.Stderr, "register %s: %v\n", name, err)
			os.Exit(1)
		}
	}

	register(origin, "pre")
	register(origin, "extra")
	register(origin, "source", "pre")
	register(origin, "a", "source", "extra")
	register(origin, "b", "source")
	register(origin, "mid", "b")
	register(origin, "report", "source", "a", "mid")
	register(origin, "view", "report")
	register(origin, "isolated")

	full, err := chainledger.ExportUpstreamLineage(origin, "report")
	if err != nil {
		fmt.Fprintf(os.Stderr, "export: %v\n", err)
		os.Exit(1)
	}
	scoped, err := chainledger.ExportSourceTargetLineage(origin, "source", "report")
	if err != nil {
		fmt.Fprintf(os.Stderr, "scoped export: %v\n", err)
		os.Exit(1)
	}

	// Importing the document yields a brand-new, independent graph. It carries
	// exactly the document's nodes and direct dependencies — every branch the
	// export kept, no more and no fewer.
	imported, err := chainledger.ImportLineage(full)
	if err != nil {
		fmt.Fprintf(os.Stderr, "import: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("ImportLineage(full export of report) -> %d datasets\n", len(imported))

	// The imported graph answers the existing queries directly. Explanation
	// paths are still chosen by shortest distance and the name-by-name tie
	// break over the imported relationships.
	fmt.Println("queries on the imported full-lineage graph:")
	for _, target := range []string{"report", "a"} {
		ups, err := chainledger.Upstreams(imported, target)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Upstreams(%s): %v\n", target, err)
			os.Exit(1)
		}
		fmt.Printf("  Upstreams(%q) -> %d dataset(s)\n", target, len(ups))
		for _, up := range ups {
			fmt.Printf("    %-7s distance=%d path=%v\n", up.Dataset, up.Distance, up.Path)
		}
	}

	// Import -> export reproduces the document byte for byte.
	again, err := chainledger.ExportUpstreamLineage(imported, "report")
	if err != nil {
		fmt.Fprintf(os.Stderr, "re-export: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("export(import(export)) byte-identical: %v\n", again == full)

	// A source-scoped export that kept only part of the lineage imports as
	// exactly that part: excluded sources and downstreams are not rebuilt.
	scopedGraph, err := chainledger.ImportLineage(scoped)
	if err != nil {
		fmt.Fprintf(os.Stderr, "scoped import: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("ImportLineage(source-scoped export) -> %d datasets; "+
		"pre/extra/view/isolated present: %v\n",
		len(scopedGraph),
		scopedGraph["pre"] != nil || scopedGraph["extra"] != nil ||
			scopedGraph["view"] != nil || scopedGraph["isolated"] != nil)
	scopedAgain, err := chainledger.ExportSourceTargetLineage(scopedGraph, "source", "report")
	if err != nil {
		fmt.Fprintf(os.Stderr, "scoped re-export: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("scoped export(import(export)) byte-identical: %v\n", scopedAgain == scoped)

	// New registrations work on an imported graph just as on a hand-built one;
	// the original graph stays untouched.
	register(imported, "fresh")
	register(imported, "fresh-detail", "fresh")
	hit, err := chainledger.Impacts(imported, "fresh")
	if err != nil {
		fmt.Fprintf(os.Stderr, "Impacts after register: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("after registering into the imported graph, Impacts(%q) -> %v\n",
		"fresh", names(hit))
	if origin["fresh"] != nil {
		fmt.Fprintln(os.Stderr, "import leaked into the caller's graph")
		os.Exit(1)
	}

	// Two empty arrays are a successful empty graph ready for registrations.
	empty, err := chainledger.ImportLineage(`{"nodes":[],"edges":[]}`)
	if err != nil {
		fmt.Fprintf(os.Stderr, "empty import: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("ImportLineage(two empty arrays) -> %d datasets, err=nil\n", len(empty))

	// Malformed documents fail with an error describing the document and no
	// partial graph.
	for _, text := range []string{
		"",
		`{"nodes":["a","b"],"edges":[{"from":"a","to":"ghost"}]}`,
		`{"nodes":["a","b"],"edges":[{"from":"a","to":"b"},{"from":"b","to":"a"}]}`,
	} {
		graph, err := chainledger.ImportLineage(text)
		if err == nil {
			fmt.Fprintf(os.Stderr, "bad document imported unexpectedly: %v\n", graph)
			os.Exit(1)
		}
		fmt.Printf("ImportLineage(%q) refused: %v\n", text, err)
	}
}

func names(hits []chainledger.Impact) []string {
	out := make([]string, len(hits))
	for i, hit := range hits {
		out[i] = hit.Dataset
	}
	return out
}
