// Command export-source-target-example demonstrates the chainledger lineage
// library's source-scoped upstream-lineage export: build an in-memory lineage
// graph, register datasets and their upstreams, then export only the routes by
// which one registered source participates in one registered target's
// derivation, as a JSON document for other programs to read.
//
// This is a library feature, not a command-line query: the source and target
// datasets are fixed in code. Run it from the repository root with:
//
//	go run ./examples/export-source-target
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
		if err := chainledger.Register(graph, chainledger.Dataset{Name: name},
			parents); err != nil {
			fmt.Fprintf(os.Stderr, "register %s: %v\n", name, err)
			os.Exit(1)
		}
	}

	// pre is an ancestor of source; extra is an independent source feeding a;
	// sidetrack is a dead-end downstream of source; lone is report's
	// independent upstream; view is report's downstream; isolated stands
	// alone:
	//
	// pre ──> source ──┬──> a (also depends on extra) ──┐
	//                 ├──> b ──> mid ──────────────────┤
	//                 ├──> sidetrack                    ├──> report ──> view
	//                 └──> report (direct)              │
	//                                       extra ─────┘
	//                                       lone ──────┘
	//
	// report's upstream list and the registration sequence are deliberately
	// out of name order; neither decides the export's ordering.
	register("pre")
	register("extra")
	register("lone")
	register("source", "pre")
	register("a", "source", "extra")
	register("b", "source")
	register("mid", "b")
	register("sidetrack", "source")
	register("report", "source", "a", "mid", "lone")
	register("view", "report")
	register("isolated")

	export := func(source, target string) {
		text, err := chainledger.ExportSourceTargetLineage(graph, source, target)
		if err != nil {
			fmt.Printf("ExportSourceTargetLineage(%q, %q) error: %v\n", source, target, err)
			return
		}
		fmt.Printf("ExportSourceTargetLineage(%q, %q) ->\n%s\n", source, target, text)
	}

	// All three routes source -> report (direct, via a, via b -> mid) stay
	// complete, while everything taking no part in those routes stays out:
	// pre/extra/sidetrack/lone/view/isolated and their incident edges.
	export("source", "report")

	// A sub-route of the same derivation: just source -> b -> mid.
	export("source", "mid")

	// Source and target identical: the node alone, empty edges.
	export("source", "source")

	// Both registered, but no source -> target route: two empty arrays, not an
	// error and not isolated endpoints. extra feeds a, but that runs the wrong
	// way for asking how source derives extra; isolated has no connection to
	// source at all. (sidetrack is reachable from source but cannot reach
	// report, which is why it is absent from the scoped report export above.)
	export("source", "extra")
	export("source", "isolated")

	// A reachable source downstream simply exports that route: source derives
	// sidetrack directly, so both endpoints and their edge appear.
	export("source", "sidetrack")

	// Validation: source is checked first; an empty/unregistered name yields
	// no document at all.
	export("", "report")
	export("ghost", "report")
	export("source", "")
	export("source", "ghost")
}
