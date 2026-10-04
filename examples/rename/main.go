// Command rename-example demonstrates renaming a registered dataset with
// chainledger.Rename: the node keeps its exact place in the lineage graph
// while its direct upstreams' and downstreams' lists are rewritten in place,
// Impacts/Upstreams recompute under the new name, and invalid requests are
// rejected with the graph untouched.
//
// This is a library feature, not a command-line subcommand: the dataset names
// are fixed in code. Run it from the repository root with:
//
//	go run ./examples/rename
package main

import (
	"fmt"

	"github.com/asdhoaiqqq/chainledger-governance/chainledger"
)

func main() {
	// The lineage graph is an ordinary in-memory map; Register fills it in.
	graph := map[string]*chainledger.Lineage{}

	register := func(name string, parents ...string) {
		if err := chainledger.Register(graph, chainledger.Dataset{Name: name}, parents); err != nil {
			fmt.Printf("Register(%s, %v) refused: %v\n", name, parents, err)
			return
		}
		fmt.Printf("Register(%s, %v) ok\n", name, parents)
	}

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

	upstreams := func(target string) {
		found, err := chainledger.Upstreams(graph, target)
		if err != nil {
			fmt.Printf("Upstreams(%q) error: %v\n", target, err)
			return
		}
		fmt.Printf("Upstreams(%q) -> %d upstream dataset(s)\n", target, len(found))
		for _, up := range found {
			fmt.Printf("  %-8s distance=%d path=%v\n", up.Dataset, up.Distance, up.Path)
		}
	}

	// source derives a and b; report depends on both.
	register("source")
	register("a", "source")
	register("b", "source")
	register("report", "a", "b")

	fmt.Println("before the rename:")
	edges("source", "a", "b", "report")
	impacts("source")

	// Rename a to z. The node stays in place: report's direct-upstream list
	// shows z where a sat, source's child list likewise, and nothing is added
	// or removed.
	if err := chainledger.Rename(graph, "a", "z"); err != nil {
		fmt.Printf("Rename(a, z) refused: %v\n", err)
		return
	}
	fmt.Println("Rename(a, z) ok")

	fmt.Println("after the rename:")
	edges("source", "z", "b", "report")
	impacts("source")
	upstreams("report")

	// The old name now reads as unregistered; the new name owns the old
	// node's reach.
	if _, err := chainledger.Impacts(graph, "a"); err != nil {
		fmt.Printf("Impacts(%q) after rename: %v\n", "a", err)
	}

	// Invalid requests name the problem and leave the graph untouched.
	fmt.Printf("Rename(ghost, z): %v\n", chainledger.Rename(graph, "ghost", "z"))
	fmt.Printf("Rename(z, b):     %v\n", chainledger.Rename(graph, "z", "b"))
	fmt.Printf("Rename(z, z):     %v\n", chainledger.Rename(graph, "z", "z"))
	fmt.Printf("datasets=%d\n", len(graph))
}
