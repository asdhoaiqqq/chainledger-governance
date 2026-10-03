// Command impacts-example demonstrates the chainledger lineage library's
// downstream-impact query: build an in-memory lineage graph, register
// datasets and their upstreams, then ask which derived datasets a change to
// one dataset would reach.
//
// This is a library feature, not a command-line query: the origin datasets
// are fixed in code. Run it from the repository root with:
//
//	go run ./examples/impacts
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

	report := func(origin string) {
		impacts, err := chainledger.Impacts(graph, origin)
		if err != nil {
			fmt.Printf("Impacts(%q) error: %v\n", origin, err)
			return
		}
		fmt.Printf("Impacts(%q) -> %d downstream dataset(s):\n", origin, len(impacts))
		for _, im := range impacts {
			fmt.Printf("  %-8s distance=%d path=%v\n", im.Dataset, im.Distance, im.Path)
		}
	}

	report("source")

	// A failed query and a successful query with no downstream are different
	// outcomes: empty name, unknown name, and a registered leaf dataset.
	if _, err := chainledger.Impacts(graph, ""); err != nil {
		fmt.Printf("Impacts(%q) error: %v\n", "", err)
	}
	if _, err := chainledger.Impacts(graph, "ghost"); err != nil {
		fmt.Printf("Impacts(%q) error: %v\n", "ghost", err)
	}
	report("isolated")

	// Re-registering an existing dataset REPLACES its direct upstreams rather
	// than appending to them: report now depends on c only. Its own downstream
	// view is kept, so report stays in source's scope through the b -> c route
	// even though the previously chosen a -> z route no longer reaches it.
	register("report", "c")
	report("source")
}
