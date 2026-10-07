// This file adds snapshot-based source tracing: given one validated snapshot
// and a dataset name, it reports every root source reachable from that
// dataset through direct upstream edges, together with one shortest path per
// root. The answer depends only on the snapshot's graph semantics, so a later
// change to the live graph never alters what a frozen version reports.
package chainledger

import (
	"fmt"
	"sort"
)

// SourceTrace links one reachable root source to the path that reaches it.
//
// Path starts at the queried dataset and ends at Root, listing every dataset
// name along the way, both endpoints included. When the queried dataset is
// itself a root, Path is the single-element array holding its own name.
type SourceTrace struct {
	Root string   `json:"root"`
	Path []string `json:"path"`
}

// TraceReport is the deterministic, read-only answer of a source trace
// against one snapshot.
//
// ContentID is the snapshot's own content identifier, Dataset is the queried
// name exactly as given, and Sources lists every reachable root once, sorted
// by root name byte order.
type TraceReport struct {
	ContentID string        `json:"contentId"`
	Dataset   string        `json:"dataset"`
	Sources   []SourceTrace `json:"sources"`
}

// TraceSources traces the root sources of dataset within the graph frozen in
// snap. The snapshot is assumed to have passed ParseSnapshot; it is only read,
// never modified.
//
// Every reachable root is reported exactly once with one shortest path (fewest
// direct relations). Among equally short paths the one chosen is the smallest
// when the names are compared element by element from the start of the path in
// UTF-8 byte order. Roots sharing intermediate datasets keep their own paths.
//
// An empty dataset name or a name absent from the snapshot is an error.
func TraceSources(snap *SnapshotFile, dataset string) (*TraceReport, error) {
	if dataset == "" {
		return nil, fmt.Errorf("%w: dataset name to trace must not be empty", ErrInvalidArgument)
	}
	adj := adjacencyFromValidFile(snap.Graph)
	if _, ok := adj[dataset]; !ok {
		return nil, fmt.Errorf("%w: dataset %q is not in the snapshot", ErrNotFound, dataset)
	}

	return &TraceReport{
		ContentID: snap.ContentID,
		Dataset:   dataset,
		Sources:   rootSourceTraces(dataset, adj),
	}, nil
}

// rootSourceTraces is the single root-source lookup shared by source tracing
// and snapshot comparison. It walks the direct upstream edges reachable from
// node in the already normalized adjacency and returns every reachable root —
// a dataset with no upstream of its own — exactly once, even when several
// branches converge on it. A node without upstreams is its own root and is
// returned with the single-element path [node]; a still-derived intermediate
// and any disconnected root never enter the result.
//
// Each root keeps one shortest path (fewest direct relations), complete with
// the query node, every intermediate dataset, and the root. Among equally long
// routes the choice is made at the first differing name from the query end in
// UTF-8 byte order, never by the last branch name. The result is sorted by root
// name byte order and is never nil. Names are kept verbatim (case and
// surrounding spaces included); because adj is already normalized, record
// order, upstream order, and repeated upstreams cannot change the output.
func rootSourceTraces(node string, adj adjacency) []SourceTrace {
	// Breadth-first walk from the queried dataset along direct upstream
	// edges. best[node] holds the shortest, tie-broken path from the query to
	// node; because every edge has the same weight, the first level at which
	// a node is reached is its shortest distance, and keeping only the
	// smallest candidate path per node preserves the global minimum.
	best := map[string][]string{node: {node}}
	frontier := []string{node}
	for len(frontier) > 0 {
		candidates := make(map[string][]string)
		for _, current := range frontier {
			for _, parent := range adj[current] {
				if _, seen := best[parent]; seen {
					continue
				}
				path := append(append([]string{}, best[current]...), parent)
				if existing, ok := candidates[parent]; !ok || pathLess(path, existing) {
					candidates[parent] = path
				}
			}
		}
		next := make([]string, 0, len(candidates))
		for reached, path := range candidates {
			best[reached] = path
			next = append(next, reached)
		}
		frontier = next
	}

	sources := make([]SourceTrace, 0)
	for reached, path := range best {
		if len(adj[reached]) == 0 {
			sources = append(sources, SourceTrace{Root: reached, Path: path})
		}
	}
	sort.Slice(sources, func(i, j int) bool { return sources[i].Root < sources[j].Root })
	return sources
}

// rootSourceNames returns just the sorted root names of the shared
// rootSourceTraces lookup, so the comparison's old/new root sets are by
// construction the exact roots a trace reports in the same snapshot. It is
// never nil.
func rootSourceNames(node string, adj adjacency) []string {
	traces := rootSourceTraces(node, adj)
	roots := make([]string, len(traces))
	for i, src := range traces {
		roots[i] = src.Root
	}
	return roots
}

// pathLess compares two equal-purpose paths element by element from the start
// using plain string (UTF-8 byte) order, deciding on the first differing
// name. A strict prefix counts as smaller, matching lexicographic order.
func pathLess(a, b []string) bool {
	for i := 0; i < len(a) && i < len(b); i++ {
		if a[i] != b[i] {
			return a[i] < b[i]
		}
	}
	return len(a) < len(b)
}
