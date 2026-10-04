// Command rename-example demonstrates chainledger.Rename: register two
// equal-length branches that merge into one derived dataset, rename the
// intermediate dataset sitting inside one branch, and watch the dataset keep
// its dependency position while query names, same-distance ordering and the
// tie-broken equal-length explanation route follow the new name.
//
// The graph starts empty and every dataset is registered by this program; no
// external service is involved. This is a library feature, not a command-line
// subcommand: the dataset names are fixed in code. Run it from the repository
// root with:
//
//	go run ./examples/rename
package main

import (
	"fmt"
	"os"

	"github.com/asdhoaiqqq/chainledger-governance/chainledger"
)

func main() {
	// The lineage graph starts as an empty in-memory map; Register fills it in.
	graph := map[string]*chainledger.Lineage{}

	// Every name and upstream below is deliberate, so a rejected registration
	// is a bug in the example and aborts the program.
	register := func(name string, parents ...string) {
		if err := chainledger.Register(graph, chainledger.Dataset{Name: name}, parents); err != nil {
			fmt.Fprintf(os.Stderr, "register %s: %v\n", name, err)
			os.Exit(1)
		}
	}

	// Two equal-length branches merge into one derived dataset "report";
	// report has its own downstream "view", and "isolated" is unrelated:
	//
	// source ──> a ──> z ──┐
	//   │                  ├──> report ──> view
	//   └──> b ──> c ──────┘
	//
	// The b-side branch is registered first and c is declared ahead of z in
	// report's upstream list, both on purpose: neither fact decides which
	// equal-length route explains the merge (see the notes in README).
	register("source")
	register("b", "source")
	register("a", "source")
	register("c", "b")
	register("z", "a")
	register("report", "c", "z")
	register("view", "report")
	register("isolated")

	// edges prints each node's direct upstream/downstream lists straight from
	// the graph map, including names that are currently not registered, so the
	// in-place old-name/new-name swap is directly visible.
	edges := func(names ...string) {
		for _, name := range names {
			if e, ok := graph[name]; ok {
				fmt.Printf("  %-8s parents=%v children=%v\n", name, e.Parents, e.Children)
			} else {
				fmt.Printf("  %-8s <unregistered>\n", name)
			}
		}
	}

	printImpacts := func(found []chainledger.Impact) {
		for _, im := range found {
			fmt.Printf("  %-8s distance=%d path=%v\n", im.Dataset, im.Distance, im.Path)
		}
	}
	printUpstreams := func(found []chainledger.Upstream) {
		for _, up := range found {
			fmt.Printf("  %-8s distance=%d path=%v\n", up.Dataset, up.Distance, up.Path)
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
	rename := func(oldName, newName string) {
		if err := chainledger.Rename(graph, oldName, newName); err != nil {
			fmt.Printf("Rename(%s, %s) refused: %v\n", oldName, newName, err)
			return
		}
		fmt.Printf("Rename(%s, %s) ok\n", oldName, newName)
	}

	// Listing both the old and the future new name makes the adjacency output
	// show one of them as <unregistered> at each stage.
	allNames := []string{"source", "a", "m", "b", "c", "z", "report", "view", "isolated"}

	// Query both directions BEFORE the rename and keep the returned slices.
	// Every Path is an independent copy, so later renames and queries do not
	// overwrite these results.
	fmt.Println("== before the rename ==")
	edges(allNames...)
	fmt.Printf("datasets in graph: %d\n", len(graph))
	impactsBefore := impacts("source")
	upstreamsBefore := upstreams("report")

	// c is already registered to the node on the other branch: the new name is
	// taken by another dataset, so the rename is refused and the error names
	// the conflicting name.
	rename("a", "c")
	fmt.Println("== after the refused rename ==")
	edges(allNames...)
	fmt.Printf("datasets in graph: %d\n", len(graph))
	impacts("source")
	upstreams("report")

	// Legal rename: m sorts past b in Go string order, which makes the other
	// branch win the equal-length whole-path comparison after the rename.
	rename("a", "m")
	fmt.Println("== after the rename ==")
	edges(allNames...)
	fmt.Printf("datasets in graph: %d\n", len(graph))

	// Slices obtained before the rename still hold the original names and
	// paths; only a fresh query against the current graph reflects the rename.
	fmt.Println("== results obtained before the rename, printed again now ==")
	fmt.Println(`Impacts("source") snapshot from before the rename:`)
	printImpacts(impactsBefore)
	fmt.Println(`Upstreams("report") snapshot from before the rename:`)
	printUpstreams(upstreamsBefore)

	fmt.Println("== results re-queried after the rename ==")
	impacts("source")
	upstreams("report")

	// The old name is not an alias: after a successful rename it is simply
	// unregistered, and both query directions report it that way.
	impacts("a")
	upstreams("a")
}
