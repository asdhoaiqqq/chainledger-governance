// Command register-example demonstrates the chainledger lineage library's
// registration semantics: a same-name Register replaces the whole direct
// upstream list (it does not append), a rejected request leaves the existing
// lineage completely untouched, and a dataset's own downstreams survive an
// upstream swap.
//
// This is a library feature, not a command-line query. Run it from the
// repository root with:
//
//	go run ./examples/register
package main

import (
	"fmt"
	"os"

	"github.com/asdhoaiqqq/chainledger-governance/chainledger"
)

func main() {
	// The lineage graph is an ordinary in-memory map; Register fills it in.
	graph := map[string]*chainledger.Lineage{}

	// Build a raw -> detail -> summary lineage chain plus an independent
	// source "other":
	//
	// raw ──> detail ──> summary
	//
	// other
	//
	// These registrations are expected to succeed, so a refusal here is a
	// bug in the example and aborts the program.
	mustRegister := func(name string, parents ...string) {
		if err := chainledger.Register(graph, chainledger.Dataset{Name: name}, parents); err != nil {
			fmt.Fprintf(os.Stderr, "register %s: %v\n", name, err)
			os.Exit(1)
		}
		fmt.Printf("registered %s parents=%v\n", name, parents)
	}
	mustRegister("raw")
	mustRegister("detail", "raw")
	mustRegister("summary", "detail")
	mustRegister("other")

	report := func(origin string) {
		impacts, err := chainledger.Impacts(graph, origin)
		if err != nil {
			fmt.Printf("Impacts(%q) error: %v\n", origin, err)
			return
		}
		fmt.Printf("Impacts(%q) -> %d downstream dataset(s):\n", origin, len(impacts))
		for _, im := range impacts {
			fmt.Printf("  %-8s distance=%d path=%v\n", im.Dataset, im.Distance, im.Path)
		}
	}

	// Attempt to replace detail's upstreams with other AND summary. other is
	// registered and legal on its own, but summary is detail's own
	// downstream, so the new edge detail -> summary closes a cycle and the
	// WHOLE request is rejected: the graph keeps exactly the lineage it had
	// before the call.
	fmt.Println("\n-- re-register detail with upstreams [other summary] --")
	if err := chainledger.Register(graph, chainledger.Dataset{Name: "detail"}, []string{"other", "summary"}); err != nil {
		fmt.Printf("register detail refused: %v\n", err)
	}
	fmt.Println("after the refusal, nothing changed:")
	report("raw")   // detail and summary still depend on raw
	report("other") // other gained no downstream from the rejected request

	// Corrected request: detail depends on other only. A same-name Register
	// REPLACES the whole direct upstream list rather than appending to it,
	// so raw is dropped; detail's own downstream summary is kept.
	fmt.Println("\n-- re-register detail with upstreams [other] --")
	mustRegister("detail", "other")
	report("raw")   // raw no longer reaches anything
	report("other") // other now reaches detail and, through it, summary
}
