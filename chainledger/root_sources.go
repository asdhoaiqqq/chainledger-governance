// This file holds the root-source rules behind the two read-only reports a
// frozen snapshot supports:
//
//   - source tracing (TraceSources) lists every root source with one
//     representative SHORTEST PATH per root (traceRootSources), and
//   - snapshot comparison (CompareSnapshots) decides, for each dataset common
//     to two versions, whether the reachable root SET changed (rootSources).
//
// Both ask the same reachability question — "which datasets without upstreams
// are reachable from this one by following direct upstream edges" — but the
// comparison never displays a path: it only compares two root-name sets. The
// two walkers below are therefore deliberately separate. A names-only
// reachability walk is all the comparison needs; it neither selects a
// representative route among several branches nor builds or copies a path for
// any root, so a long derivation chain (or many downstreams sharing one
// upstream segment) never causes trace-shaped slices to be allocated only to
// be thrown away. Tracing still walks with full path state, because its
// report does carry paths. Walking the same normalized adjacency under the
// same root rule keeps the two reports in agreement: a root is reachable for
// the comparison exactly when a trace against the same snapshot lists it.
package chainledger

import "sort"

// rootSources finds, for one dataset within an already validated adjacency,
// the names of every root source reachable by following direct upstream
// edges. It is the comparison-only half of this file: it computes just the
// root-name set, and never builds, chooses, or copies a path, because the
// compare report never carries one.
//
// A dataset that itself has no upstream is a root and its own source, a root
// reached through several branches is reported exactly once, intermediates
// that still have upstreams never qualify, and roots in disconnected
// components are never reached. The result is sorted by name byte order, is
// never nil, and is a fresh slice owned by the caller.
func rootSources(dataset string, adj adjacency) []string {
	// Plain reachability walk along direct upstream edges. Only node identity
	// matters: the first visit marks a node for the rest of the walk, so no
	// per-route state (and no path slices) is ever kept.
	visited := map[string]bool{dataset: true}
	frontier := []string{dataset}
	roots := make([]string, 0)
	for len(frontier) > 0 {
		node := frontier[len(frontier)-1]
		frontier = frontier[:len(frontier)-1]
		parents := adj[node]
		if len(parents) == 0 {
			roots = append(roots, node)
			continue
		}
		for _, parent := range parents {
			if !visited[parent] {
				visited[parent] = true
				frontier = append(frontier, parent)
			}
		}
	}
	sort.Strings(roots)
	return roots
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
