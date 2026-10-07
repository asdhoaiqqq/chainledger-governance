// This file is the single business rule shared by the two read-only reports a
// frozen snapshot supports:
//
//   - source tracing (TraceSources) lists every root source with one
//     representative path per root, and
//   - snapshot comparison (CompareSnapshots) decides, for each dataset common
//     to two versions, whether that reachable root set changed.
//
// Both ask the same question — "which datasets without upstreams are reachable
// from this one by following direct upstream edges" — so they walk the same
// normalized adjacency through the one finder below. The comparison can never
// explain a root differently from a trace run against the same snapshot: its
// old/new root names are a strict projection of what traceRootSources returns.
package chainledger

import "sort"

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

// rootSources is the names-only projection of the shared finder: the sorted
// root names a snapshot comparison records as a dataset's old or new sources.
// It is deliberately computed through traceRootSources so that the
// comparison's roots are exactly the names a TraceSources query reports
// against the same snapshot, in the same byte order. A node without upstreams
// is itself a root and its own source; a root reached by more than one path
// counts once. The result is sorted by name and never nil.
func rootSources(node string, adj adjacency) []string {
	traces := traceRootSources(node, adj)
	roots := make([]string, 0, len(traces))
	for _, src := range traces {
		roots = append(roots, src.Root)
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
