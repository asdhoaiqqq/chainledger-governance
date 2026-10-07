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
// The root lookup is traceRootSources, the one finder that builds a
// representative path per reachable root. Snapshot comparison answers the
// same reachability question for its root-set diff via the path-free
// rootSources finder (root_sources.go); the two share root-reachability
// semantics without the comparison selecting or copying paths its report
// cannot show.
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
		Sources:   traceRootSources(dataset, adj),
	}, nil
}

// traceRootSources finds, for one dataset within an already validated
// adjacency, every root source reachable by following direct upstream edges:
// a dataset that itself has no upstream is a root and its own source, a root
// reached through several branches is reported exactly once, intermediates
// that still have upstreams never qualify, and roots in disconnected
// components are never reached.
//
// Every root carries one shortest path (fewest direct relations), beginning at
// the queried dataset, listing every intermediate, and ending at the root.
// Among equally short paths the choice is made element by element from the
// START of the path in UTF-8 byte order, at the first differing name — never
// by the last branch name. Roots reached through a shared intermediate each
// keep their own complete path. The returned entries are sorted by root name
// byte order, and every path is a fresh slice owned by the caller, so editing
// a returned report can never reach the adjacency or another report.
func traceRootSources(dataset string, adj adjacency) []SourceTrace {
	// Breadth-first walk from the queried dataset along direct upstream
	// edges. best[node] holds the shortest, tie-broken path from the query to
	// node; because every edge has the same weight, the first level at which
	// a node is reached is its shortest distance, and keeping only the
	// smallest candidate path per node preserves the global minimum.
	best := map[string][]string{dataset: {dataset}}
	frontier := []string{dataset}
	for len(frontier) > 0 {
		candidates := make(map[string][]string)
		for _, node := range frontier {
			for _, parent := range adj[node] {
				if _, seen := best[parent]; seen {
					continue
				}
				path := append(append([]string{}, best[node]...), parent)
				if current, ok := candidates[parent]; !ok || pathLess(path, current) {
					candidates[parent] = path
				}
			}
		}
		next := make([]string, 0, len(candidates))
		for node, path := range candidates {
			best[node] = path
			next = append(next, node)
		}
		frontier = next
	}

	sources := make([]SourceTrace, 0)
	for node, path := range best {
		if len(adj[node]) == 0 {
			sources = append(sources, SourceTrace{Root: node, Path: path})
		}
	}
	sort.Slice(sources, func(i, j int) bool { return sources[i].Root < sources[j].Root })
	return sources
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
