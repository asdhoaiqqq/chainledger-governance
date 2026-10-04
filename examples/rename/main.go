// Command rename-example demonstrates renaming a dataset with
// chainledger.Rename inside a lineage graph where two equal-length branches
// merge into one derived dataset: the rename keeps the dataset's exact
// dependency position (neighbors swap the old name for the new one in
// place), a re-issued query sorts same-distance results by the new name and
// may explain the merge point through the other branch, and a rejected
// rename (new name already taken) leaves the graph and every query result
// exactly as they were.
//
// This is a library feature, not a command-line subcommand: the dataset
// names are fixed in code. Run it from the repository root with:
//
//	go run ./examples/rename
package main

import (
	"fmt"
	"os"

	"github.com/asdhoaiqqq/chainledger-governance/chainledger"
)

func main() {
	// The lineage graph is an ordinary in-memory map; Register fills it in.
	graph := map[string]*chainledger.Lineage{}

	// Every registration below is deliberate, so a rejected registration is a
	// bug in the example and aborts the program.
	register := func(name string, parents ...string) {
		if err := chainledger.Register(graph, chainledger.Dataset{Name: name}, parents); err != nil {
			fmt.Fprintf(os.Stderr, "register %s: %v\n", name, err)
			os.Exit(1)
		}
	}

	// Two equal-length branches merge into one derived dataset "report",
	// which has its own downstream "view":
	//
	// source ──> a ──> z ──┐
	//   │                  ├──> report ──> view
	//   └──> b ──> c ──────┘
	//
	// The b-side branch is registered first on purpose: a sits in the second
	// slot of source's downstream list, so the rename below can show the new
	// name taking over exactly that slot instead of being appended.
	register("source")
	register("b", "source")
	register("a", "source")
	register("c", "b")
	register("z", "a")
	register("report", "c", "z")
	register("view", "report")

	// The graph is an ordinary map, so the stored adjacency is directly
	// readable: Parents are the dataset's direct upstreams and Children its
	// direct downstreams (reverse edges maintained by Register, renamed in
	// place by Rename).
	edges := func(names ...string) {
		for _, name := range names {
			if e, ok := graph[name]; ok {
				fmt.Printf("  %-7s parents=%v children=%v\n", name, e.Parents, e.Children)
			} else {
				fmt.Printf("  %-7s <unregistered>\n", name)
			}
		}
	}

	impacts := func(origin string) []chainledger.Impact {
		found, err := chainledger.Impacts(graph, origin)
		if err != nil {
			fmt.Printf("Impacts(%q) error: %v\n", origin, err)
			return nil
		}
		fmt.Printf("Impacts(%q) -> %d downstream dataset(s):\n", origin, len(found))
		printImpacts(found)
		return found
	}

	upstreams := func(target string) []chainledger.Upstream {
		found, err := chainledger.Upstreams(graph, target)
		if err != nil {
			fmt.Printf("Upstreams(%q) error: %v\n", target, err)
			return nil
		}
		fmt.Printf("Upstreams(%q) -> %d upstream dataset(s):\n", target, len(found))
		printUpstreams(found)
		return found
	}

	fmt.Printf("registered %d datasets:\n", len(graph))
	edges("source", "a", "b", "c", "z", "report", "view")

	// Query both directions BEFORE any rename and keep the results. Impacts
	// walks downstream from the source; Upstreams walks upstream from the
	// merge point. In both, every Path is written along the actual derivation
	// direction: from the origin or source toward the derived dataset.
	impactsBefore := impacts("source")
	upstreamsBefore := upstreams("report")

	// First attempt: rename a to c. c is already registered to the b-side
	// branch node, so the rename is refused with an error naming the
	// conflicting name, and the graph is left exactly as it was.
	if err := chainledger.Rename(graph, "a", "c"); err != nil {
		fmt.Printf("Rename(a, c) refused: %v\n", err)
	}
	fmt.Println("after the refused rename, relations and queries are unchanged:")
	edges("source", "a", "b", "c", "z", "report", "view")
	impacts("source")

	// Legal rename: a becomes m. The node keeps its own upstream and
	// downstream lists untouched; every neighbor swaps the old name for the
	// new one in its original list position. No edge is added, removed or
	// reordered, and the dataset count stays the same.
	if err := chainledger.Rename(graph, "a", "m"); err != nil {
		fmt.Fprintf(os.Stderr, "rename a -> m: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("Rename(a, m) ok, still %d datasets:\n", len(graph))
	edges("source", "a", "m", "b", "c", "z", "report", "view")

	// Re-issued queries reflect the new name: the reachable set and every
	// shortest distance are unchanged, but same-distance records sort by the
	// current name, and the merge point's explanation path now runs through
	// the OTHER branch. The tie between [source m z report] and
	// [source b c report] is decided by comparing the whole path name by name
	// from the start: the first differing hop is m vs b, and b < m, so the
	// b/c branch wins. Comparing only the merge point's direct upstreams
	// (c < z, true before and after) could never produce this flip.
	impacts("source")
	upstreams("report")

	// The results fetched before the rename kept their original content:
	// they still say a, and only queries issued now reflect the new name.
	fmt.Println("the results fetched before the rename are unchanged:")
	printImpacts(impactsBefore)
	printUpstreams(upstreamsBefore)

	// The old name is not kept as an alias: it is simply unregistered now.
	impacts("a")
	upstreams("a")
}

func printImpacts(impacts []chainledger.Impact) {
	for _, im := range impacts {
		fmt.Printf("  %-8s distance=%d path=%v\n", im.Dataset, im.Distance, im.Path)
	}
}

func printUpstreams(upstreams []chainledger.Upstream) {
	for _, up := range upstreams {
		fmt.Printf("  %-8s distance=%d path=%v\n", up.Dataset, up.Distance, up.Path)
	}
}
