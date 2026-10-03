// Command register-example demonstrates dataset registration and lineage
// maintenance in the chainledger library: what a same-name Register call
// replaces, what a rejected (cycle-forming) request leaves behind, and how
// Impacts answers before and after the re-wire.
//
// This is a library feature, not a command-line subcommand: the dataset names
// are fixed in code. Run it from the repository root with:
//
//	go run ./examples/register
package main

import (
	"fmt"

	"github.com/asdhoaiqqq/chainledger-governance/chainledger"
)

func main() {
	// The lineage graph is an ordinary in-memory map; Register fills it in.
	graph := map[string]*chainledger.Lineage{}

	register := func(name string, parents ...string) {
		err := chainledger.Register(graph, chainledger.Dataset{Name: name}, parents)
		if err != nil {
			fmt.Printf("Register(%s, %v) refused: %v\n", name, parents, err)
			return
		}
		fmt.Printf("Register(%s, %v) ok\n", name, parents)
	}

	// The graph is an ordinary map, so the stored result of each registration
	// is directly readable: Parents are the dataset's direct upstreams and
	// Children its direct downstreams (reverse edges maintained by Register).
	edges := func(names ...string) {
		for _, name := range names {
			e := graph[name]
			fmt.Printf("  %-7s parents=%v children=%v\n", name, e.Parents, e.Children)
		}
	}

	impacts := func(origin string) {
		found, err := chainledger.Impacts(graph, origin)
		if err != nil {
			fmt.Printf("Impacts(%q) error: %v\n", origin, err)
			return
		}
		fmt.Printf("Impacts(%q) -> %d downstream dataset(s)\n", origin, len(found))
		for _, im := range found {
			fmt.Printf("  %-8s distance=%d path=%v\n", im.Dataset, im.Distance, im.Path)
		}
	}

	// The starting lineage is raw -> detail -> summary; other is an
	// independent source unrelated to that chain.
	register("raw")
	register("detail", "raw")
	register("summary", "detail")
	register("other")

	// Try to replace detail's direct upstreams with other and summary. other is
	// already registered, but summary is detail's own downstream: the edge
	// detail -> summary would close the cycle detail -> summary -> detail.
	register("detail", "other", "summary")

	// The rejected request is atomic: detail still depends on raw, summary still
	// depends on detail, and other did not gain detail as a downstream.
	fmt.Println("after the rejected request:")
	edges("raw", "detail", "summary", "other")
	impacts("raw")
	impacts("other")

	// Fixed request: other becomes detail's only direct upstream. A same-name
	// Register REPLACES the whole direct-upstream list (it does not append raw),
	// while detail's existing downstream summary is retained.
	register("detail", "other")

	fmt.Println("after the fixed request:")
	edges("raw", "detail", "summary", "other")
	// raw no longer reaches detail or summary ...
	impacts("raw")
	// ... while other now affects both, and detail keeps its old downstream.
	impacts("other")
	impacts("detail")
}
