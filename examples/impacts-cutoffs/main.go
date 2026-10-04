// Command impacts-cutoffs-example demonstrates chainledger's downstream-impact
// query with a propagation cutoff list: build an in-memory lineage graph,
// register datasets and their upstreams, then ask how far a change to one
// dataset spreads when the change is not allowed to travel past named
// registered datasets.
//
// This is a library feature, not a command-line query: the origin and cutoff
// datasets are fixed in code. Run it from the repository root with:
//
//	go run ./examples/impacts-cutoffs
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

	// source derives a and b; a derives report directly, while b reaches report
	// through mid; report derives view; isolated stands alone:
	//
	// source ──> a ────────> report ──> view
	//   │                    ^
	//   └──> b ──> mid ──────┘
	register("source")
	register("a", "source")
	register("b", "source")
	register("mid", "b")
	register("report", "a", "mid")
	register("view", "report")
	register("isolated")

	report := func(origin string, cutoffs []string) {
		impacts, err := chainledger.ImpactsWithCutoffs(graph, origin, cutoffs)
		if err != nil {
			fmt.Printf("ImpactsWithCutoffs(%q, %v) error: %v\n", origin, cutoffs, err)
			return
		}
		fmt.Printf("ImpactsWithCutoffs(%q, %v) -> %d dataset(s):\n", origin, cutoffs, len(impacts))
		for _, im := range impacts {
			fmt.Printf("  %-8s distance=%d path=%v\n", im.Dataset, im.Distance, im.Path)
		}
	}

	// No cutoffs: the answer is identical to chainledger.Impacts.
	report("source", nil)

	// Cut off at a: a itself stays at distance 1, but propagation through it
	// stops. report is then reached around a via b -> mid at distance 3, and
	// view at 4, both explained by that bypass.
	report("source", []string{"a"})

	// Cut off both a and b: both stay in the result, but mid, report and view
	// are no longer reachable on any cutoff-free route.
	report("source", []string{"a", "b"})

	// A registered-but-unreachable cutoff changes nothing; repeating a name
	// acts just once.
	report("source", []string{"isolated", "isolated"})

	// Naming the origin itself as a cutoff succeeds with an empty list: the
	// origin is still never listed as affected.
	report("source", []string{"source"})

	// A leaf origin with no downstream answers an empty list too.
	report("isolated", nil)

	// Failure cases: an empty cutoff name and an unregistered cutoff name each
	// fail the whole query (nil results) and say why; an unknown origin keeps
	// the ordinary Impacts error behavior.
	if _, err := chainledger.ImpactsWithCutoffs(graph, "source", []string{""}); err != nil {
		fmt.Printf("empty cutoff name error: %v\n", err)
	}
	if _, err := chainledger.ImpactsWithCutoffs(graph, "source", []string{"ghost"}); err != nil {
		fmt.Printf("unregistered cutoff error: %v\n", err)
	}
	if _, err := chainledger.ImpactsWithCutoffs(graph, "ghost", nil); err != nil {
		fmt.Printf("unknown origin error: %v\n", err)
	}
}
