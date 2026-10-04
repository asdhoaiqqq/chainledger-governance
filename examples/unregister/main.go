// Command unregister-example demonstrates removing a dataset registration
// from the lineage graph with chainledger.Unregister: only a dataset without
// direct downstreams can be removed, its direct upstreams lose the reverse
// reference while keeping their other children and list order, and a rejected
// removal leaves the graph exactly as it was.
//
// This is a library feature, not a command-line subcommand: the dataset names
// are fixed in code. Run it from the repository root with:
//
//	go run ./examples/unregister
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

	// Parents are the dataset's direct upstreams and children its direct
	// downstreams (reverse edges maintained by Register and Unregister).
	edges := func(names ...string) {
		for _, name := range names {
			if e, ok := graph[name]; ok {
				fmt.Printf("  %-7s parents=%v children=%v\n", name, e.Parents, e.Children)
			} else {
				fmt.Printf("  %-7s <unregistered>\n", name)
			}
		}
	}

	unregister := func(name string) {
		if err := chainledger.Unregister(graph, name); err != nil {
			fmt.Printf("Unregister(%s) refused: %v\n", name, err)
			return
		}
		fmt.Printf("Unregister(%s) ok\n", name)
	}

	impacts := func(origin string) {
		found, err := chainledger.Impacts(graph, origin)
		if err != nil {
			fmt.Printf("Impacts(%q) error: %v\n", origin, err)
			return
		}
		names := make([]string, len(found))
		for i, im := range found {
			names[i] = im.Dataset
		}
		fmt.Printf("Impacts(%q) -> %v\n", origin, names)
	}

	upstreams := func(target string) {
		found, err := chainledger.Upstreams(graph, target)
		if err != nil {
			fmt.Printf("Upstreams(%q) error: %v\n", target, err)
			return
		}
		names := make([]string, len(found))
		for i, up := range found {
			names[i] = up.Dataset
		}
		fmt.Printf("Upstreams(%q) -> %v\n", target, names)
	}

	// raw derives detail; detail derives report and view. report also depends
	// on the independent source extra, which has its own other downstream.
	register("raw")
	register("detail", "raw")
	register("report", "detail")
	register("view", "detail")
	register("extra")
	register("otherchild", "extra")
	register("report", "detail", "extra")

	// detail still has direct downstreams (report and view): the removal is
	// refused and the graph is left exactly as it was.
	unregister("detail")
	fmt.Println("after the refused request:")
	edges("raw", "detail", "report", "view", "extra", "otherchild")

	// report is a leaf: it can be removed even though it has two upstreams.
	unregister("report")
	fmt.Println("after removing report:")
	edges("raw", "detail", "report", "view", "extra", "otherchild")

	// raw still reaches detail and view, but no longer report; view's
	// provenance still traces to detail and raw.
	impacts("raw")
	upstreams("view")

	// The removed name is now treated as unregistered.
	unregister("report")
	impacts("report")

	// A standalone dataset with neither upstreams nor downstreams can be
	// removed directly; an empty or unknown name is an error.
	unregister("otherchild")
	unregister("")
	unregister("ghost")

	// After view is removed, detail becomes a leaf and can be removed too.
	unregister("view")
	unregister("detail")
	fmt.Println("final graph:")
	edges("raw", "detail", "report", "view", "extra", "otherchild")
	impacts("raw")
}
