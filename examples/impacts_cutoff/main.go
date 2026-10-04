// Command impacts-cutoff-example demonstrates the chainledger lineage
// library's downstream-impact query with a propagation cutoff list: build an
// in-memory lineage graph, register datasets and their upstreams, then ask
// which derived datasets a change to one dataset would reach when
// propagation stops at chosen datasets.
//
// This is a library feature, not a command-line query: the origin datasets
// and cutoff lists are fixed in code. Run it from the repository root with:
//
//	go run ./examples/impacts_cutoff
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

	// source derives a and b; a derives report directly while b reaches
	// report through mid; report derives view:
	//
	// source ──> a ────────┐
	//   │                  ├──> report ──> view
	//   └──> b ──> mid ────┘
	register("source")
	register("a", "source")
	register("b", "source")
	register("mid", "b")
	register("report", "a", "mid")
	register("view", "report")

	report := func(origin string, cutoffs ...string) {
		impacts, err := chainledger.ImpactsWithCutoffs(graph, origin, cutoffs)
		if err != nil {
			fmt.Printf("ImpactsWithCutoffs(%q, %v) error: %v\n", origin, cutoffs, err)
			return
		}
		fmt.Printf("ImpactsWithCutoffs(%q, %v) -> %d downstream dataset(s):\n", origin, cutoffs, len(impacts))
		for _, im := range impacts {
			fmt.Printf("  %-8s distance=%d path=%v\n", im.Dataset, im.Distance, im.Path)
		}
	}

	// The full query first: report is reached through a at distance 2.
	report("source")

	// With a as the cutoff, a itself is still reported at distance 1, but
	// nothing propagates beyond it: report is now reached through b and mid
	// at distance 3, and view at distance 4, with paths along that route.
	report("source", "a")

	// Cutting both branches keeps a and b in the result but removes
	// everything behind them.
	report("source", "a", "b")

	// The origin itself may be listed as a cutoff: the query succeeds and
	// returns an empty (non-nil) list.
	report("source", "source")

	// Cutoff-list problems fail the whole query with nil results: an empty
	// cutoff name, and a cutoff name that is not registered (the error names
	// it). Origin errors behave exactly as in Impacts.
	if _, err := chainledger.ImpactsWithCutoffs(graph, "source", []string{""}); err != nil {
		fmt.Printf("ImpactsWithCutoffs(%q, %v) error: %v\n", "source", []string{""}, err)
	}
	if _, err := chainledger.ImpactsWithCutoffs(graph, "source", []string{"ghost"}); err != nil {
		fmt.Printf("ImpactsWithCutoffs(%q, %v) error: %v\n", "source", []string{"ghost"}, err)
	}
	if _, err := chainledger.ImpactsWithCutoffs(graph, "ghost", []string{"a"}); err != nil {
		fmt.Printf("ImpactsWithCutoffs(%q, %v) error: %v\n", "ghost", []string{"a"}, err)
	}
}
