// Command chainledger is the 链上数据治理与血缘工作台 entry point.
package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/asdhoaiqqq/chainledger-governance/chainledger"
)

func main() {
	os.Exit(run(os.Args[1:]))
}

// run dispatches the command and returns the process exit code. Keeping the
// exit code out of main makes the CLI testable without spawning a subprocess.
func run(args []string) int {
	command := "demo"
	if len(args) > 0 {
		command = args[0]
	}
	switch command {
	case "demo":
		runDemo()
		return 0
	case "version":
		fmt.Println("chainledger 0.2.0")
		return 0
	case "preview":
		return runBatch(args[1:], false)
	case "apply":
		return runBatch(args[1:], true)
	case "help", "-h", "--help":
		usage()
		return 0
	default:
		fmt.Fprintf(os.Stderr, "unknown command %q\n", command)
		usage()
		return 2
	}
}

func usage() {
	fmt.Print(`usage: chainledger <command> [arguments]

commands:
  demo                        run the built-in lineage demo
  version                     print the version
  preview <graph> <plan>      validate the batch and print the report without
                              modifying the graph file
  apply <graph> <plan>        validate the batch, apply it to the graph file,
                              and print the report
  help                        show this help

The graph file is JSON:
  {"datasets": [{"name": "A", "upstreams": []}, ...]}

The plan file is JSON:
  {"changes": [{"name": "A", "upstreams": ["B"]}, ...]}

A batch registers new datasets or replaces every direct upstream of an
existing dataset with the listed upstreams. An empty upstream list makes the
dataset a root. Datasets not mentioned in the plan keep their upstreams.
New datasets may reference each other regardless of plan order; the final
graph must be acyclic and every upstream must resolve to a dataset in the
final graph. Preview and apply produce the same report for the same inputs.
`)
}

// runBatch implements preview (apply=false) and apply (apply=true). It reads
// the graph and plan JSON files, validates them, prints the report to stdout,
// and — only for apply — writes the final graph back to the graph file. Any
// failure is reported on stderr with a non-zero exit code; on failure the graph
// file is left untouched.
func runBatch(args []string, apply bool) int {
	if len(args) != 2 {
		fmt.Fprintf(os.Stderr, "usage: chainledger %s <graph.json> <plan.json>\n", verb(apply))
		return 2
	}
	graphPath := args[0]
	planPath := args[1]

	graphData, err := os.ReadFile(graphPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: cannot read graph file %q: %v\n", graphPath, err)
		return 1
	}
	graph, err := chainledger.UnmarshalGraphFile(graphData)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: invalid graph file %q: %v\n", graphPath, err)
		return 1
	}

	planData, err := os.ReadFile(planPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: cannot read plan file %q: %v\n", planPath, err)
		return 1
	}
	plan, err := chainledger.UnmarshalPlan(planData)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: invalid plan file %q: %v\n", planPath, err)
		return 1
	}

	var report *chainledger.BatchReport
	if apply {
		report, err = chainledger.ApplyBatch(graph, plan)
	} else {
		report, err = chainledger.PreviewBatch(graph, plan)
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return 1
	}

	if apply {
		out, err := chainledger.MarshalGraphFile(graph)
		if err != nil {
			fmt.Fprintf(os.Stderr, "error: cannot serialize final graph: %v\n", err)
			return 1
		}
		if err := atomicWrite(graphPath, out); err != nil {
			fmt.Fprintf(os.Stderr, "error: cannot write graph file %q: %v\n", graphPath, err)
			return 1
		}
	}

	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	if err := enc.Encode(report); err != nil {
		fmt.Fprintf(os.Stderr, "error: cannot write report: %v\n", err)
		return 1
	}
	return 0
}

func verb(apply bool) string {
	if apply {
		return "apply"
	}
	return "preview"
}

// atomicWrite writes data to a temporary file in the same directory as path
// and renames it into place, so a write failure never leaves a partially
// written graph file: the original is preserved.
func atomicWrite(path string, data []byte) error {
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, ".chainledger-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	committed := false
	defer func() {
		if !committed {
			os.Remove(tmpName)
		}
	}()
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmpName, path); err != nil {
		return err
	}
	committed = true
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
