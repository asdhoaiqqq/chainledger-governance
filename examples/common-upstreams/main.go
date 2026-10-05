// Command common-upstreams-example demonstrates the chainledger lineage
// library's common-provenance query: given two registered datasets, list the
// nearest datasets upstream of both, with each source's shortest distance and
// explanation path to the two targets separately.
//
// This is a library feature, not a command-line query: the target datasets
// are fixed in code. Run it from the repository root with:
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

	// raw derives a and b, and both a and b feed left and right:
	//
	// raw ──> a ──┬──> left
	//   │         │
	//   └──> b ──┴──> right
	//
	// The two targets declare their upstreams in opposite orders on purpose:
	// the result depends only on the final relationships.
	register("raw")
	register("b", "raw")
	register("a", "raw")
	register("left", "b", "a")
	register("right", "a", "b")

	// An independent lineage with no connection to left or right.
	register("other")
	register("oright", "other")

	// A second merge scenario: r2 reaches mixL at distance 2 through either y1
	// or y2 (y2 declared first), and mixR at distance 1 through a direct edge.
	register("r2")
	register("y2", "r2")
	register("y1", "r2")
	register("mixL", "y2", "y1")
	register("mixR", "r2")

	common := func(first, second string) {
		found, err := chainledger.CommonUpstreams(graph, first, second)
		if err != nil {
			fmt.Printf("CommonUpstreams(%q, %q) error: %v\n", first, second, err)
			return
		}
		fmt.Printf("CommonUpstreams(%q, %q) -> %d common source(s):\n", first, second, len(found))
		for _, c := range found {
			fmt.Printf("  %-4s to-first  distance=%d path=%v\n", c.Dataset, c.DistanceToFirst, c.PathToFirst)
			fmt.Printf("  %-4s to-second distance=%d path=%v\n", "", c.DistanceToSecond, c.PathToSecond)
		}
	}

	// Headline case: a and b are the nearest shared sources; raw is excluded
	// even though both targets ultimately derive from it.
	common("left", "right")

	// Adding direct raw -> left / raw -> right edges makes raw shorter to
	// reach, but it still cannot appear: a and b are common sources downstream
	// of raw, so the nearest-only rule removes raw regardless of distance.
	register("left", "raw", "b", "a")
	register("right", "raw", "a", "b")
	common("left", "right")

	// The same dataset queried against itself: the sole answer is the target
	// at distance 0 on both sides.
	common("left", "left")

	// One target is itself upstream of the other: it is the sole answer, with
	// distance 0 on its own side.
	common("a", "left")

	// No shared ancestry at all: the query succeeds with an empty list.
	common("left", "oright")

	// Asymmetric distances and the full-path tie break: r2 is the only shared
	// source; its path to mixL compares the whole sequence and takes the
	// lexicographically smaller y1 route despite y2 being listed first.
	common("mixL", "mixR")

	// Validation problems report the offending target in argument order.
	if _, err := chainledger.CommonUpstreams(graph, "", "right"); err != nil {
		fmt.Printf("CommonUpstreams(%q, %q) error: %v\n", "", "right", err)
	}
	if _, err := chainledger.CommonUpstreams(graph, "left", "ghost"); err != nil {
		fmt.Printf("CommonUpstreams(%q, %q) error: %v\n", "left", "ghost", err)
	}
}
