// Command common-upstreams-example demonstrates the chainledger lineage
// library's common-source query: build an in-memory lineage graph, register
// datasets and their upstreams, then ask which sources two derived datasets
// BOTH trace back to, and how far those shared points are from each side.
//
// This is a library feature, not a command-line query: the two target
// datasets are fixed in code. Run it from the repository root with:
//
//	go run ./examples/common-upstreams
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

	// raw derives a and b, and a and b both take part in deriving left and
	// right:
	//
	//         ┌──> a ──┬──> left
	//    raw ─┤        └──> right
	//         └──> b ──┬──> left
	//                  └──> right
	register("raw")
	register("a", "raw")
	register("b", "raw")
	register("left", "a", "b")
	register("right", "a", "b")

	common := func(first, second string) {
		found, err := chainledger.CommonUpstreams(graph, first, second)
		if err != nil {
			fmt.Printf("CommonUpstreams(%q, %q) error: %v\n", first, second, err)
			return
		}
		fmt.Printf("CommonUpstreams(%q, %q) -> %d shared source(s):\n", first, second, len(found))
		for _, c := range found {
			fmt.Printf("  %-5s to %s: distance=%d path=%v | to %s: distance=%d path=%v\n",
				c.Dataset,
				first, c.DistanceToFirst, c.PathToFirst,
				second, c.DistanceToSecond, c.PathToSecond)
		}
	}

	// a and b are the closest shared points. raw is also a common source, but
	// it has later common sources (a, b) downstream of it, so it is excluded.
	common("left", "right")

	// Querying a dataset with itself returns only that dataset, with zero
	// distance on both sides.
	common("left", "left")

	// When one target is upstream of the other, that target is the sole shared
	// source, at distance zero on its own side; its own ancestors stay hidden.
	common("raw", "left")

	// Two datasets on unrelated lineages share nothing: the query succeeds
	// with an empty list rather than failing.
	register("other")
	register("otherchild", "other")
	common("left", "otherchild")

	// Target order matters only for which side is which: swapping the query
	// swaps the two distance/path sides.
	common("right", "left")

	// The two sides are traced independently and need not be symmetric: let
	// right also reach raw directly. raw then has a distance-1 route to right,
	// yet it STAYS excluded, because a and b remain later common sources.
	// Distance never rescues a hidden source.
	register("right", "raw", "a", "b")
	common("left", "right")

	// Validation mirrors Upstreams/Impacts and happens in target input order.
	if _, err := chainledger.CommonUpstreams(graph, "", "right"); err != nil {
		fmt.Printf("CommonUpstreams(%q, %q) error: %v\n", "", "right", err)
	}
	if _, err := chainledger.CommonUpstreams(graph, "left", "ghost"); err != nil {
		fmt.Printf("CommonUpstreams(%q, %q) error: %v\n", "left", "ghost", err)
	}
	if _, err := chainledger.CommonUpstreams(nil, "left", "right"); err != nil {
		fmt.Printf("CommonUpstreams on nil graph error: %v\n", err)
	}
}
