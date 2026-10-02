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
	case "snapshot":
		return runSnapshot(args[1:])
	case "compare":
		return runCompare(args[1:])
	case "trace":
		return runTrace(args[1:])
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
  snapshot <graph> <snapshot> save a read-only versioned snapshot of the
                              current graph; an existing snapshot of the same
                              content is left byte-for-byte untouched, a
                              different or corrupt target is refused
  compare <old-snapshot> <new-snapshot>
                              read-only comparison of two snapshots; prints a
                              JSON report of nodes, relations, and root-source
                              sets that differ from the old to the new version
  trace <snapshot> <dataset>  read-only source trace of one dataset within a
                              single snapshot; prints a JSON report of every
                              reachable root source with one shortest path
  help                        show this help

The graph file is JSON:
  {"datasets": [{"name": "A", "upstreams": []}, ...]}

The plan file is JSON:
  {"changes": [{"name": "A", "upstreams": ["B"]}, ...], "removals": ["C", ...]}

The snapshot file is JSON with formatVersion 1, a semantic contentId, and the
complete graph. Its bytes depend only on graph semantics, never on record
order, upstream order, duplicate upstreams, JSON whitespace, save time, or
file path. compare and trace never write anything.

A batch registers new datasets, replaces every direct upstream of an
existing dataset with the listed upstreams, and deletes the datasets named in
removals. An empty upstream list makes the dataset a root. Datasets not
mentioned in the plan keep their upstreams. New datasets may reference each
other regardless of plan order; the final graph must be acyclic and every
upstream must resolve to a dataset in the final graph. A retained dataset
that still references a deleted name rejects the batch. Preview and apply
produce the same report for the same inputs.
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

// runSnapshot implements `snapshot <graph.json> <snapshot.json>`. It reads and
// validates the current graph without modifying it, builds a deterministic,
// content-addressed snapshot, and saves it.
//
// Save rules, serialized against concurrent saves by an advisory lock on a
// sibling lock file:
//   - target absent: create it atomically (temp file + rename);
//   - target present, a valid snapshot of the same content: succeed and leave
//     the existing file's bytes untouched (idempotent);
//   - target present with different content, or corrupt/unreadable: refuse and
//     never overwrite it.
//
// The content identifier is printed only after the save has committed.
func runSnapshot(args []string) int {
	if len(args) != 2 {
		fmt.Fprintln(os.Stderr, "usage: chainledger snapshot <graph.json> <snapshot.json>")
		return 2
	}
	graphPath := args[0]
	snapshotPath := args[1]

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
	snap, err := chainledger.BuildSnapshot(graph)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: cannot build snapshot from %q: %v\n", graphPath, err)
		return 1
	}
	data, err := chainledger.MarshalSnapshot(snap)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: cannot serialize snapshot: %v\n", err)
		return 1
	}

	// Hold a process-wide advisory lock for the whole check-then-write so
	// concurrent saves cannot interleave between inspecting the target and
	// committing the replacement.
	lock, err := acquireLock(snapshotPath + ".lock")
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: cannot lock snapshot target %q: %v\n", snapshotPath, err)
		return 1
	}
	defer lock.release()

	existing, err := os.ReadFile(snapshotPath)
	switch {
	case err == nil:
		oldSnap, perr := chainledger.ParseSnapshot(existing)
		if perr != nil {
			fmt.Fprintf(os.Stderr, "error: refusing to overwrite %q: existing file is not a valid snapshot: %v\n", snapshotPath, perr)
			return 1
		}
		if oldSnap.ContentID != snap.ContentID {
			fmt.Fprintf(os.Stderr, "error: refusing to overwrite %q: it already holds a different snapshot (content %s); target content would be %s\n", snapshotPath, oldSnap.ContentID, snap.ContentID)
			return 1
		}
		// Same content: success without touching the file's bytes.
	case os.IsNotExist(err):
		if err := atomicWrite(snapshotPath, data); err != nil {
			fmt.Fprintf(os.Stderr, "error: cannot write snapshot file %q: %v\n", snapshotPath, err)
			return 1
		}
	default:
		fmt.Fprintf(os.Stderr, "error: cannot inspect snapshot target %q: %v\n", snapshotPath, err)
		return 1
	}

	// The save committed (or an identical snapshot was already present); only
	// now is the content identifier reported.
	fmt.Println(snap.ContentID)
	return 0
}

// runCompare implements `compare <old.json> <new.json>`: a strictly read-only
// comparison of two snapshots that prints the deterministic JSON report to
// stdout. It never writes to either file.
func runCompare(args []string) int {
	if len(args) != 2 {
		fmt.Fprintln(os.Stderr, "usage: chainledger compare <old-snapshot.json> <new-snapshot.json>")
		return 2
	}
	oldPath := args[0]
	newPath := args[1]

	oldData, err := os.ReadFile(oldPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: cannot read old snapshot %q: %v\n", oldPath, err)
		return 1
	}
	oldSnap, err := chainledger.ParseSnapshot(oldData)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: invalid old snapshot %q: %v\n", oldPath, err)
		return 1
	}
	newData, err := os.ReadFile(newPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: cannot read new snapshot %q: %v\n", newPath, err)
		return 1
	}
	newSnap, err := chainledger.ParseSnapshot(newData)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: invalid new snapshot %q: %v\n", newPath, err)
		return 1
	}

	report := chainledger.CompareSnapshots(oldSnap, newSnap)
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	if err := enc.Encode(report); err != nil {
		fmt.Fprintf(os.Stderr, "error: cannot write report: %v\n", err)
		return 1
	}
	return 0
}

// runTrace implements `trace <snapshot.json> <dataset>`: a strictly read-only
// source trace against one snapshot that prints the deterministic JSON report
// to stdout. The whole snapshot is validated before the query runs; any
// failure is reported on stderr with a non-zero exit code and empty stdout,
// and the snapshot file is never modified.
func runTrace(args []string) int {
	if len(args) != 2 {
		fmt.Fprintln(os.Stderr, "usage: chainledger trace <snapshot.json> <dataset>")
		return 2
	}
	snapshotPath := args[0]
	dataset := args[1]

	data, err := os.ReadFile(snapshotPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: cannot read snapshot %q: %v\n", snapshotPath, err)
		return 1
	}
	snap, err := chainledger.ParseSnapshot(data)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: invalid snapshot %q: %v\n", snapshotPath, err)
		return 1
	}

	report, err := chainledger.TraceSources(snap, dataset)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: cannot trace %q in snapshot %q: %v\n", dataset, snapshotPath, err)
		return 1
	}

	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	if err := enc.Encode(report); err != nil {
		fmt.Fprintf(os.Stderr, "error: cannot write report: %v\n", err)
		return 1
	}
	return 0
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
