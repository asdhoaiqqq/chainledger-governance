// Command chainledger is the 链上数据治理与血缘工作台 entry point.
package main

import (
	"fmt"
	"os"
	"path/filepath"

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
	case "lineage":
		runLineage(os.Args[2:])
	case "help", "-h", "--help":
		usage()
	default:
		fmt.Fprintf(os.Stderr, "unknown command %q\n", command)
		usage()
		os.Exit(2)
	}
}

func usage() {
	fmt.Println(`usage: chainledger <command> [arguments]

commands:
  demo                                              run the built-in demo
  version                                           print the version
  lineage preview <graph.json> <plan.json>          preview a batch of lineage adjustments
  lineage apply <graph.json> <plan.json>            apply a batch and write the graph back
  help                                              show this help

lineage files are JSON:
  graph: {"nodes": [{"name": "B", "upstreams": ["A"]}]}
  plan:  {"adjustments": [{"name": "A", "upstreams": ["B"]}]}

preview prints the impact report and leaves the graph file untouched.
apply validates the same batch, writes the final graph back to the graph
file atomically, then prints the same report. Names are case-sensitive and
keep their spacing; an empty upstreams list makes a dataset a root.`)
}

// runLineage dispatches the lineage preview/apply subcommands.
func runLineage(args []string) {
	if len(args) == 1 && (args[0] == "-h" || args[0] == "--help" || args[0] == "help") {
		fmt.Println(`usage: chainledger lineage (preview|apply) <graph.json> <plan.json>`)
		return
	}
	if len(args) != 3 || (args[0] != "preview" && args[0] != "apply") {
		fmt.Fprintln(os.Stderr, `usage: chainledger lineage (preview|apply) <graph.json> <plan.json>`)
		os.Exit(2)
	}
	mode, graphPath, planPath := args[0], args[1], args[2]
	if err := lineageBatch(mode, graphPath, planPath); err != nil {
		fmt.Fprintf(os.Stderr, "lineage %s failed: %v\n", mode, err)
		os.Exit(1)
	}
}

// lineageBatch reads the two JSON files, validates and computes the batch.
// Preview never writes; apply persists the final graph before the report is
// emitted, so a write failure leaves the original file and stdout intact.
func lineageBatch(mode, graphPath, planPath string) error {
	graphBytes, err := os.ReadFile(graphPath)
	if err != nil {
		return fmt.Errorf("cannot read graph file %q: %w", graphPath, err)
	}
	planBytes, err := os.ReadFile(planPath)
	if err != nil {
		return fmt.Errorf("cannot read plan file %q: %w", planPath, err)
	}

	current, err := chainledger.ParseGraph(graphBytes)
	if err != nil {
		return err
	}
	plan, err := chainledger.ParsePlan(planBytes, current)
	if err != nil {
		return err
	}

	final, report, err := chainledger.Apply(current, plan)
	if err != nil {
		return err
	}

	if mode == "apply" {
		out, err := chainledger.MarshalGraph(final)
		if err != nil {
			return fmt.Errorf("cannot encode final graph: %w", err)
		}
		if err := writeFileAtomic(graphPath, out); err != nil {
			return fmt.Errorf("cannot write graph file %q: %w", graphPath, err)
		}
	}

	reportBytes, err := chainledger.MarshalReport(report)
	if err != nil {
		return fmt.Errorf("cannot encode report: %w", err)
	}
	os.Stdout.Write(reportBytes)
	return nil
}

// writeFileAtomic replaces path with data via a temp file and rename, so a
// failed write leaves the original graph file complete. The original file's
// permissions are preserved.
func writeFileAtomic(path string, data []byte) (err error) {
	mode := os.FileMode(0o644)
	if info, statErr := os.Stat(path); statErr == nil {
		mode = info.Mode()
	}
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, filepath.Base(path)+".tmp-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer func() {
		if err != nil {
			os.Remove(tmpName)
		}
	}()

	if _, err = tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err = tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err = tmp.Close(); err != nil {
		return err
	}
	if err = os.Chmod(tmpName, mode); err != nil {
		return err
	}
	if err = os.Rename(tmpName, path); err != nil {
		return err
	}
	return nil
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
}
