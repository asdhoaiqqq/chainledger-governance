// Command export-upstreams-example demonstrates the chainledger lineage
// library's complete upstream export: build an in-memory lineage graph, then
// ask for one registered dataset's target-plus-all-upstreams subgraph as JSON
// text that other programs can consume.
//
// Unlike Upstreams, which keeps one shortest explanation path per source, the
// export preserves every branch the target's derivation actually relies on.
//
// This is a library feature, not a command-line query: the target datasets are
// fixed in code. Run it from the repository root with:
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

	// The export scenario:
	//
	//  raw ──> a ─────────────┐
	//   │                     ├──> report ──> view
	//   ├──> b ──> mid ───────┤
	//   └─────────────────────┘
	//
	// report directly depends on raw, a and mid, so raw -> report exists as a
	// direct edge (the bottom branch) while raw also feeds report through a and
	// through b -> mid. view is report's downstream; isolated is unrelated to
	// the derivation.
	register("raw")
	register("a", "raw")
	register("b", "raw")
	register("mid", "b")
	register("report", "raw", "a", "mid")
	register("view", "report")
	register("isolated")

	export := func(target string) {
		text, err := chainledger.ExportUpstreams(graph, target)
		if err != nil {
			fmt.Printf("ExportUpstreams(%q) error: %v\n", target, err)
			return
		}
		fmt.Printf("ExportUpstreams(%q) ->\n%s\n", target, text)
	}

	// Five nodes (raw, a, b, mid, report) and all six direct dependencies
	// between them; the longer branches survive even though raw -> report is
	// already direct. view and isolated never appear.
	export("report")

	// A registered dataset with no upstreams exports just itself and an empty
	// JSON edge array.
	export("isolated")

	// A failed export and a successful one are different outcomes: empty name
	// and unknown name are errors naming the problem.
	export("")
	export("ghost")
}
