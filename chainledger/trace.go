// This file adds read-only lineage tracing over a snapshot: given a dataset
// name frozen in a snapshot, trace reports every root source reachable by
// following direct upstream edges, together with the shortest path from the
// queried dataset to each root. The result is determined solely by the
// snapshot's graph semantics and is unaffected by later changes to the
// current graph.
package chainledger

import (
	"fmt"
	"sort"
)

// TraceSource is one root source reachable from the queried dataset within a
// snapshot. Path lists the dataset names from the queried dataset itself,
// following direct upstream edges, to Root, inclusive at both ends.
type TraceSource struct {
	Root string   `json:"root"`
	Path []string `json:"path"`
}

// TraceReport is the read-only result of tracing one dataset in a snapshot:
// the snapshot's content identifier, the queried dataset name verbatim, and
// every reachable root source with its shortest path.
type TraceReport struct {
	ContentID string        `json:"contentId"`
	Dataset   string        `json:"dataset"`
	Sources   []TraceSource `json:"sources"`
}

// TraceSnapshot returns the root sources reachable from dataset within the
// snapshot's graph. snap is assumed to have passed ParseSnapshot, so the whole
// graph is already validated; dataset must be a declared dataset name.
//
// Each reachable root is reported exactly once, via the path with the fewest
// direct relationships; ties are broken by comparing the path names in UTF-8
// byte order from the dataset toward the root. The queried dataset itself, if
// it has no upstreams, is its own single-element path. Sources are ordered by
// root name byte order. The returned report depends only on the snapshot's
// graph semantics.
func TraceSnapshot(snap *SnapshotFile, dataset string) (*TraceReport, error) {
	if dataset == "" {
		return nil, fmt.Errorf("%w: dataset name is required", ErrInvalidArgument)
	}
	adj := adjacencyFromValidFile(snap.Graph)
	if _, ok := adj[dataset]; !ok {
		return nil, fmt.Errorf("%w: dataset %q is not in the snapshot", ErrNotFound, dataset)
	}

	return &TraceReport{
		ContentID: snap.ContentID,
		Dataset:   dataset,
		Sources:   traceSources(dataset, adj),
	}, nil
}

// traceSources computes the shortest, lexicographically smallest path from
// start to every reachable root (a dataset with no upstreams), ordered by root
// name. The graph is acyclic, so BFS by increasing path length visits each
// node at its shortest distance; among equal-length candidates to the same
// node the lexicographically smallest path is kept.
func traceSources(start string, adj adjacency) []TraceSource {
	sources := make([]TraceSource, 0)

	if len(adj[start]) == 0 {
		return []TraceSource{{Root: start, Path: []string{start}}}
	}

	// best maps each reached node to its best path from start.
	best := map[string][]string{start: {start}}
	frontier := []string{start}
	for len(frontier) > 0 {
		next := make(map[string][]string)
		for _, node := range frontier {
			base := best[node]
			for _, parent := range adj[node] {
				if _, seen := best[parent]; seen {
					// A shorter (or equal-length) route already reaches this
					// node; the new candidate is strictly longer and cannot
					// win, so skip it.
					continue
				}
				cand := make([]string, 0, len(base)+1)
				cand = append(cand, base...)
				cand = append(cand, parent)
				if existing, ok := next[parent]; !ok || pathLess(cand, existing) {
					next[parent] = cand
				}
			}
		}
		frontier = frontier[:0]
		for node, path := range next {
			best[node] = path
			if len(adj[node]) == 0 {
				// node is a root: this is its first (shortest) reach.
				sources = append(sources, TraceSource{Root: node, Path: append([]string(nil), path...)})
			}
			frontier = append(frontier, node)
		}
		sort.Strings(frontier)
	}

	sort.Slice(sources, func(i, j int) bool { return sources[i].Root < sources[j].Root })
	return sources
}

// pathLess reports whether path a is preferred to path b: it has fewer names
// (fewer direct relationships), or — the same length — the first name that
// differs is smaller in UTF-8 byte order.
func pathLess(a, b []string) bool {
	if len(a) != len(b) {
		return len(a) < len(b)
	}
	for i := range a {
		if a[i] != b[i] {
			return a[i] < b[i]
		}
	}
	return false
}
