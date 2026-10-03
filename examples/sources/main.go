// Command sources-example demonstrates the chainledger lineage library's
// upstream-provenance query: build an in-memory lineage graph, register
// datasets and their upstreams, then ask where every direct and indirect
// source of one registered dataset is.
//
// This is a library feature, not a command-line query: the queried dataset is
// fixed in code. Run it from the repository root with:
//
//	go run ./examples/sources
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

	// Two branches that merge into one derived dataset "report", report's own
	// downstream "view", and an unrelated dataset "isolated":
	//
	// source ──> a ──> z ──┐
	//   │                  ├──> report ──> view
	//   └──> b ──> c ──────┘
	//
	// The b-side branch is registered first and c is declared ahead of z in
	// report's upstream list, both on purpose: provenance ordering must depend
	// on distances and names, never on this order.
	register("source")
	register("b", "source")
	register("a", "source")
	register("c", "b")
	register("z", "a")
	register("report", "c", "z")
	register("view", "report")
	register("isolated")

	trace := func(dataset string) {
		sources, err := chainledger.Sources(graph, dataset)
		if err != nil {
			fmt.Printf("Sources(%q) error: %v\n", dataset, err)
			return
		}
		fmt.Printf("Sources(%q) -> %d upstream dataset(s):\n", dataset, len(sources))
		for _, s := range sources {
			fmt.Printf("  %-8s distance=%d path=%v\n", s.Name, s.Distance, s.Path)
		}
	}

	// report is fed by z and c directly; tracing further finds a, b and,
	// distance 3, the common root source — explained through a -> z because the
	// two length-3 routes are compared from the source and a < b.
	trace("report")

	// A failed query and a successful query with no upstream are different
	// outcomes: empty name, unknown name, and a registered root dataset.
	if _, err := chainledger.Sources(graph, ""); err != nil {
		fmt.Printf("Sources(%q) error: %v\n", "", err)
	}
	if _, err := chainledger.Sources(graph, "ghost"); err != nil {
		fmt.Printf("Sources(%q) error: %v\n", "ghost", err)
	}
	trace("source")

	// Re-registering an existing dataset REPLACES its direct upstreams rather
	// than appending to them: report now also depends on source directly.
	// source's distance collapses to 1, and the previously longer explanation
	// is replaced by the short edge for future queries.
	register("report", "source", "c", "z")
	trace("report")
}
