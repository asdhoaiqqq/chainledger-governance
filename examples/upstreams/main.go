// Command upstreams-example demonstrates the chainledger lineage library's
// upstream-provenance query: build an in-memory lineage graph, register
// datasets and their upstreams, then ask where one derived dataset came from.
//
// This is a library feature, not a command-line query: the target datasets
// are fixed in code. Run it from the repository root with:
//
//	go run ./examples/upstreams
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
	// report's upstream list, both on purpose (see the explanation in README).
	register("source")
	register("b", "source")
	register("a", "source")
	register("c", "b")
	register("z", "a")
	register("report", "c", "z")
	register("view", "report")
	register("isolated")

	provenance := func(target string) {
		upstreams, err := chainledger.Upstreams(graph, target)
		if err != nil {
			fmt.Printf("Upstreams(%q) error: %v\n", target, err)
			return
		}
		fmt.Printf("Upstreams(%q) -> %d upstream dataset(s):\n", target, len(upstreams))
		for _, up := range upstreams {
			fmt.Printf("  %-8s distance=%d path=%v\n", up.Dataset, up.Distance, up.Path)
		}
	}

	provenance("report")

	// A failed query and a successful query with no upstream are different
	// outcomes: empty name, unknown name, and a registered root dataset.
	if _, err := chainledger.Upstreams(graph, ""); err != nil {
		fmt.Printf("Upstreams(%q) error: %v\n", "", err)
	}
	if _, err := chainledger.Upstreams(graph, "ghost"); err != nil {
		fmt.Printf("Upstreams(%q) error: %v\n", "ghost", err)
	}
	provenance("isolated")

	// Re-registering an existing dataset REPLACES its direct upstreams rather
	// than appending to them: report now also depends on source directly. The
	// shared source shortens to distance 1 with path [source report], and the
	// longer route through a -> z no longer explains it.
	register("report", "source", "c", "z")
	provenance("report")
}
