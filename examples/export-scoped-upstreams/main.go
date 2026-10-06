// Command export-scoped-upstreams-example demonstrates the chainledger lineage
// library's source-scoped upstream-lineage export: given a registered source
// and a registered target, it exports the JSON document describing only how
// that particular source participates in the target's derivation.
//
// This is a library feature, not a command-line query: the source and target
// datasets are fixed in code. Run it from the repository root with:
//
//	go run ./examples/export-scoped-upstreams
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

	// source reaches report three ways — directly, through a, and through b
	// then mid — while a also takes the independent input extra, source feeds
	// an unreachable orphan, source has its own ancestor pre, and report
	// derives down:
	//
	// pre ──> source ──┬──> a (also <- extra) ──┐
	//                  │                         ├──> report ──> down
	//                  ├──> b ──> mid ──────────┤
	//                  ├──> orphan              │
	//                  └────────────────────────┘ (direct)
	//
	// Registration itself follows dependency order (parents must already be
	// registered); the declared upstream lists are deliberately not written in
	// name order — neither detail decides the export's ordering.
	register("pre")
	register("extra")
	register("source", "pre")
	register("a", "extra", "source")
	register("b", "source")
	register("mid", "b")
	register("report", "mid", "a", "source")
	register("down", "report")
	register("orphan", "source")

	scoped := func(source, target string) {
		text, err := chainledger.ExportScopedUpstreamLineage(graph, source, target)
		if err != nil {
			fmt.Printf("ExportScopedUpstreamLineage(%q, %q) error: %v\n", source, target, err)
		}
		fmt.Printf("ExportScopedUpstreamLineage(%q, %q) ->\n%s\n", source, target, text)
	}

	// All three source -> report routes survive, including the longer ones
	// despite the direct edge; extra/extra->a, orphan, pre and down stay out.
	scoped("source", "report")

	// An intermediate source keeps everything on source -> a -> report
	// routes: the direct source -> report edge and the b/mid branch are not
	// reachable from a, so they leave the document.
	scoped("a", "report")

	// Same registered dataset as both endpoints: one node, empty edges.
	scoped("b", "b")

	// Two registered datasets with no route between them succeed with two
	// empty arrays, a different outcome from an error: orphan is a leaf
	// downstream of source and cannot reach report.
	scoped("orphan", "report")

	// Errors: an empty or unregistered name, checked source first.
	scoped("", "report")
	scoped("ghost", "report")
	scoped("source", "")
}
