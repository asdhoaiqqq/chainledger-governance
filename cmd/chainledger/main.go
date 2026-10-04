// Command chainledger is the 链上数据治理与血缘工作台 entry point.
package main

import (
	"fmt"
	"os"

	"github.com/asdhoaiqqq/chainledger-governance/chainledger"
)

func main() {
	command := "demo"
	if len(os.Args) > 1 {
		command = os.Args[1]
	}
	switch command {
	case "demo":
		runDemo()
	case "version":
		fmt.Println("chainledger 0.1.0")
	case "help", "-h", "--help":
		usage()
	default:
		fmt.Fprintf(os.Stderr, "unknown command %q\n", command)
		usage()
		os.Exit(2)
	}
}

func usage() {
	fmt.Println("usage: chainledger [demo|version|help]")
}

func runDemo() {
	graph := map[string]*chainledger.Lineage{}
	register := func(dataset chainledger.Dataset, parents []string) {
		if err := chainledger.Register(graph, dataset, parents); err != nil {
			fmt.Printf("dataset=%s refused: %v\n", dataset.Name, err)
			return
		}
		fmt.Printf("dataset=%s registered parents=%v\n", dataset.Name, parents)
	}
	register(chainledger.Dataset{Name: "raw-blocks", SchemaVersion: 3}, nil)
	register(chainledger.Dataset{Name: "transfers", SchemaVersion: 2}, []string{"raw-blocks"})
	register(chainledger.Dataset{Name: "wallet-daily", SchemaVersion: 1}, []string{"transfers"})
	register(chainledger.Dataset{Name: "raw-blocks", SchemaVersion: 4}, []string{"wallet-daily"})
	fmt.Println("roots:", chainledger.Roots(graph))
	fmt.Printf("datasets=%d\n", len(graph))

	// Rename demo: blocks derive both txns and events, and the account-daily
	// aggregate depends on the two of them together.
	fmt.Println("-- rename demo --")
	register(chainledger.Dataset{Name: "blocks"}, nil)
	register(chainledger.Dataset{Name: "txns"}, []string{"blocks"})
	register(chainledger.Dataset{Name: "events"}, []string{"blocks"})
	register(chainledger.Dataset{Name: "account-daily"}, []string{"txns", "events"})
	showImpacts(graph, "blocks")
	showUpstreams(graph, "account-daily")

	fmt.Println(`rename "events" -> "logs":`)
	if err := chainledger.Rename(graph, "events", "logs"); err != nil {
		fmt.Printf("rename refused: %v\n", err)
	} else {
		fmt.Println("rename ok")
	}
	fmt.Printf("account-daily parents=%v children=%v\n",
		graph["account-daily"].Parents, graph["account-daily"].Children)
	fmt.Printf("blocks children=%v\n", graph["blocks"].Children)
	showImpacts(graph, "blocks")
	showUpstreams(graph, "account-daily")

	// Invalid renames are reported with the offending name and change nothing.
	fmt.Printf("rename ghost -> mirage: %v\n", chainledger.Rename(graph, "ghost", "mirage"))
	fmt.Printf("rename txns -> blocks: %v\n", chainledger.Rename(graph, "txns", "blocks"))
	fmt.Printf("rename logs -> logs: %v\n", chainledger.Rename(graph, "logs", "logs"))
	fmt.Printf("datasets=%d\n", len(graph))
}

func showImpacts(graph map[string]*chainledger.Lineage, origin string) {
	impacts, err := chainledger.Impacts(graph, origin)
	if err != nil {
		fmt.Printf("Impacts(%q) error: %v\n", origin, err)
		return
	}
	fmt.Printf("Impacts(%q) -> %d downstream dataset(s)\n", origin, len(impacts))
	for _, im := range impacts {
		fmt.Printf("  %-15s distance=%d path=%v\n", im.Dataset, im.Distance, im.Path)
	}
}

func showUpstreams(graph map[string]*chainledger.Lineage, target string) {
	upstreams, err := chainledger.Upstreams(graph, target)
	if err != nil {
		fmt.Printf("Upstreams(%q) error: %v\n", target, err)
		return
	}
	fmt.Printf("Upstreams(%q) -> %d upstream dataset(s)\n", target, len(upstreams))
	for _, up := range upstreams {
		fmt.Printf("  %-15s distance=%d path=%v\n", up.Dataset, up.Distance, up.Path)
	}
}
