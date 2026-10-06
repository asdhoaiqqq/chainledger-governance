// Command import-lineage-example demonstrates the chainledger lineage
// library's lineage import: export one dataset's lineage as JSON, hand that
// text to ImportLineage to obtain a fresh independent graph, and keep using
// the new graph with the existing registration and query operations.
//
// This is a library feature, not a command-line query: the lineage text is
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
	// The source graph is an ordinary in-memory map; Register fills it in.
	graph := map[string]*chainledger.Lineage{}
	register := func(name string, parents ...string) {
		if err := chainledger.Register(graph, chainledger.Dataset{Name: name}, parents); err != nil {
			fmt.Fprintf(os.Stderr, "register %s: %v\n", name, err)
			os.Exit(1)
		}
	}

	// pre -> source; source derives a, b and report directly; a and the
	// b -> mid branch both feed report; report derives view. report also
	// depends on two independent sources that a source-scoped export omits.
	register("pre")
	register("source", "pre")
	register("a", "source")
	register("b", "source")
	register("mid", "b")
	register("extra")
	register("lone")
	register("report", "source", "a", "mid", "extra", "lone")
	register("view", "report")

	// A source-scoped export keeps only the source -> report routes.
	text, err := chainledger.ExportSourceTargetLineage(graph, "source", "report")
	if err != nil {
		fmt.Fprintf(os.Stderr, "export: %v\n", err)
		os.Exit(1)
	}
	fmt.Println("exported document:")
	fmt.Println(text)

	// Import turns the text into a brand-new graph. The caller's graph is not
	// touched, and the scope is exactly the document: pre, extra, lone and
	// view are not restored.
	imported, err := chainledger.ImportLineage(text)
	if err != nil {
		fmt.Fprintf(os.Stderr, "import: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("\ndatasets after import: %d (the export omitted pre, extra, lone and view)\n", len(imported))

	// The imported graph answers queries exactly like a registered one: the
	// direct source -> report edge wins on distance, and the longer branches
	// through a and b -> mid survive at their own distances.
	impacts, err := chainledger.Impacts(imported, "source")
	if err != nil {
		fmt.Fprintf(os.Stderr, "impacts: %v\n", err)
		os.Exit(1)
	}
	fmt.Println("\nImpacts(source) on the imported graph:")
	for _, im := range impacts {
		fmt.Printf("  %-7s distance=%d path=%v\n", im.Dataset, im.Distance, im.Path)
	}

	// Re-exporting the same source and target from the imported graph yields
	// byte-identical text.
	again, err := chainledger.ExportSourceTargetLineage(imported, "source", "report")
	if err != nil {
		fmt.Fprintf(os.Stderr, "re-export: %v\n", err)
		os.Exit(1)
	}
	if again == text {
		fmt.Println("\nre-export is byte-identical to the imported document")
	} else {
		fmt.Printf("\nre-export differs:\n%s\n%s\n", text, again)
	}

	// The new graph is independent and fully usable: register more datasets,
	// and query datasets the scoped document omitted as unregistered.
	if err := chainledger.Register(imported, chainledger.Dataset{Name: "newly"}, []string{"mid"}); err != nil {
		fmt.Fprintf(os.Stderr, "register into imported graph: %v\n", err)
		os.Exit(1)
	}
	fmt.Println("\nregistered newly into the imported graph; querying omitted datasets:")
	if _, err := chainledger.Upstreams(imported, "view"); err != nil {
		fmt.Printf("  Upstreams(view) error: %v\n", err)
	}
	// The caller's original graph is unchanged and still has every dataset.
	fmt.Printf("  original graph still holds %d datasets, imported graph holds %d\n", len(graph), len(imported))

	// Two empty arrays give an empty graph that is ready for registration.
	empty, err := chainledger.ImportLineage(`{"nodes":[],"edges":[]}`)
	if err != nil {
		fmt.Fprintf(os.Stderr, "empty import: %v\n", err)
		os.Exit(1)
	}
	if err := chainledger.Register(empty, chainledger.Dataset{Name: "fresh"}, nil); err != nil {
		fmt.Fprintf(os.Stderr, "register into empty import: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("\nempty document imported as a ready-to-register graph holding %q\n", "fresh")

	// Document problems fail the whole import with nil results, never a
	// partial graph: empty text, an edge endpoint not listed in nodes, and a
	// dependency cycle.
	importFail := func(label, doc string) {
		g, err := chainledger.ImportLineage(doc)
		if err == nil {
			fmt.Fprintf(os.Stderr, "%s unexpectedly succeeded: %v\n", label, g)
			os.Exit(1)
		}
		fmt.Printf("%s refused: %v\n", label, err)
	}
	fmt.Println()
	importFail("empty text", "")
	importFail("missing endpoint", `{"nodes":["a","b"],"edges":[{"from":"a","to":"ghost"}]}`)
	importFail("self dependency", `{"nodes":["a"],"edges":[{"from":"a","to":"a"}]}`)
	importFail("three-node cycle",
		`{"nodes":["a","b","c"],"edges":[`+
			`{"from":"a","to":"b"},{"from":"b","to":"c"},{"from":"c","to":"a"}]}`)
}
