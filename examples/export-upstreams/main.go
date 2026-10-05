// Command export-upstreams-example demonstrates the chainledger lineage
// library's full upstream-lineage export: build an in-memory lineage graph,
// register datasets and their upstreams, then export one dataset's complete
// derivation as a JSON document for other programs to read.
//
// This is a library feature, not a command-line query: the target datasets
// are fixed in code. Run it from the repository root with:
//
//	go run ./examples/export-upstreams
package main

import (
	"fmt"
	"os"

	"github.com/asdhoaiqqq/chainledger-governance/chainledger"
)

func main() {
	// The lineage graph is an ordinary in-memory map; Register fills it in.
	graph := map[string]*chainledger.Lineage{}

	// Every name and upstream below is deliberate, so a rejected registration
	// is a bug in the example and aborts the program.
	register := func(name string, parents ...string) {
		if err := chainledger.Register(graph, chainledger.Dataset{Name: name}, parents); err != nil {
			fmt.Fprintf(os.Stderr, "register %s: %v\n", name, err)
			os.Exit(1)
		}
	}

	// raw derives a and b, b derives mid, and report depends directly on raw,
	// a and mid; report derives view, and isolated stands alone:
	//
	// raw ──┬──> a ────────┐
	//       │              ├──> report ──> view
	//       └──> b ──> mid ┘
	//       └────────────────^ (report also depends on raw directly)
	//
	// The b-side branch is registered before a and report's upstream list is
	// not written in name order, both on purpose: neither decides the export's
	// ordering.
	register("raw")
	register("b", "raw")
	register("a", "raw")
	register("mid", "b")
	register("report", "raw", "a", "mid")
	register("view", "report")
	register("isolated")

	export := func(target string) {
		text, err := chainledger.ExportUpstreamLineage(graph, target)
		if err != nil {
			fmt.Printf("ExportUpstreamLineage(%q) error: %v\n", target, err)
			return
		}
		fmt.Printf("ExportUpstreamLineage(%q) ->\n%s\n", target, text)
	}

	// The export keeps every branch report depends on: even though raw reaches
	// report directly, the longer routes through a and through b -> mid stay
	// in the document. view (a downstream) and isolated (unrelated) never
	// appear.
	export("report")

	// A registered target with no upstreams exports successfully: it is the
	// only node and the edges array is empty.
	export("isolated")

	// Failed exports are different from empty ones: an empty name and an
	// unregistered name are errors and produce no document at all.
	export("")
	export("ghost")
}
