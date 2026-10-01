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
  snapshot <graph> <snapshot> save the current graph as a snapshot; if the
                              target already holds the same content it is kept,
                              otherwise it is left untouched and the save is
                              rejected
  compare <old> <new>         compare two snapshots in the old-to-new direction
                              and print the JSON report to stdout
  help                        show this help

The graph file is JSON:
  {"datasets": [{"name": "A", "upstreams": []}, ...]}

The plan file is JSON:
  {"changes": [{"name": "A", "upstreams": ["B"]}, ...]}

The snapshot file is JSON:
  {"formatVersion": 1, "contentId": "<sha256>", "graph": {"datasets": [...]}}

A batch registers new datasets or replaces every direct upstream of an
existing dataset with the listed upstreams. An empty upstream list makes the
dataset a root. Datasets not mentioned in the plan keep their upstreams.
New datasets may reference each other regardless of plan order; the final
graph must be acyclic and every upstream must resolve to a dataset in the
final graph. Preview and apply produce the same report for the same inputs.

A snapshot captures dataset names and their direct upstream relationships.
Its content identifier is a sha256 over the graph semantics, so equivalent
graphs (different record or upstream order, duplicate upstreams, or JSON
whitespace) produce the same identifier and the same snapshot bytes. Saving
does not modify the source graph; a write failure never leaves a partial
snapshot.

Compare lists added, deleted, and directly changed datasets, added and
removed direct relations (including relations that disappear with a deleted
node), and root-source changes for datasets present in both versions. A root
source is a dataset with no upstream reachable via upstream edges; a root's
source set contains itself. Adjusting a path without changing the reachable
root set is not reported as a source change.
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

// runSnapshot saves the current graph as a snapshot. The source graph file is
// only read, never modified. On success the content identifier is printed to
// stdout; the identifier is printed only after the snapshot is committed.
//
// Idempotency and conflict policy: if the target already contains a valid
// snapshot with the same content identifier, the save succeeds and the existing
// file bytes are kept. If the target exists with different content or is
// corrupt, the save is rejected and the file is preserved. The check-and-create
// is atomic (hard link), so two concurrent saves to the same target cannot
// clobber each other: the loser re-checks the winner's file.
func runSnapshot(args []string) int {
	if len(args) != 2 {
		fmt.Fprintf(os.Stderr, "usage: chainledger snapshot <graph.json> <snapshot.json>\n")
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
		fmt.Fprintf(os.Stderr, "error: cannot build snapshot from graph file %q: %v\n", graphPath, err)
		return 1
	}
	data, err := chainledger.MarshalSnapshot(snap)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: cannot serialize snapshot: %v\n", err)
		return 1
	}
	if err := commitSnapshot(snapshotPath, data, snap.ContentID); err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return 1
	}
	fmt.Println(snap.ContentID)
	return 0
}

// commitSnapshot writes the snapshot bytes to path atomically. If path already
// exists, it is left untouched when it contains a valid snapshot with wantID;
// otherwise an error is returned and the existing file is preserved. The
// check-and-create is atomic (hard link), so a write failure never leaves a
// partial snapshot and concurrent saves cannot clobber each other.
func commitSnapshot(path string, data []byte, wantID string) error {
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, ".chainledger-snapshot-*")
	if err != nil {
		return fmt.Errorf("cannot create temporary file in %q: %w", dir, err)
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
		return fmt.Errorf("cannot write snapshot: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return fmt.Errorf("cannot sync snapshot: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("cannot close snapshot: %w", err)
	}

	for {
		err := os.Link(tmpName, path)
		if err == nil {
			committed = true
			return nil
		}
		if !os.IsExist(err) {
			return fmt.Errorf("cannot write snapshot file %q: %w", path, err)
		}
		// The target exists. Keep its bytes if it holds the same content;
		// otherwise reject the overwrite.
		existing, readErr := os.ReadFile(path)
		if readErr != nil {
			return fmt.Errorf("snapshot file %q already exists but cannot be read: %w", path, readErr)
		}
		if chainledger.SnapshotHasContent(existing, wantID) {
			return nil
		}
		return fmt.Errorf("snapshot file %q already exists with different content or is corrupt", path)
	}
}

// runCompare reads two snapshot files, validates them, and prints the
// old-to-new JSON report to stdout. It is read-only: neither snapshot file is
// modified. Any failure is reported on stderr with a non-zero exit code and no
// success report is printed.
func runCompare(args []string) int {
	if len(args) != 2 {
		fmt.Fprintf(os.Stderr, "usage: chainledger compare <old.json> <new.json>\n")
		return 2
	}
	oldPath := args[0]
	newPath := args[1]

	oldData, err := os.ReadFile(oldPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: cannot read snapshot file %q: %v\n", oldPath, err)
		return 1
	}
	oldSnap, err := chainledger.UnmarshalSnapshot(oldData)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: invalid snapshot file %q: %v\n", oldPath, err)
		return 1
	}
	newData, err := os.ReadFile(newPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: cannot read snapshot file %q: %v\n", newPath, err)
		return 1
	}
	newSnap, err := chainledger.UnmarshalSnapshot(newData)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: invalid snapshot file %q: %v\n", newPath, err)
		return 1
	}
	report, err := chainledger.CompareSnapshots(oldSnap, newSnap)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
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
