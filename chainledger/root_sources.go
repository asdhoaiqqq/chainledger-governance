// This file holds the root-source SET rule the snapshot comparison needs
// (CompareSnapshots): for one dataset, the names of every dataset without
// upstreams reachable by following direct upstream edges.
//
// The comparison asks only "does this set differ between the two versions" —
// it never prints, selects, or copies a representative path — so this finder
// performs a plain reachability walk and allocates no per-node path slices,
// no shortest-path tie-breaking, and none of the path copying that gets
// repeated along long derivation chains and shared upstream relations. The
// full source-tracing rule (one shortest, tie-broken path per root) lives in
// trace.go as traceRootSources and is the only place paths are built; the two
// share the same root-reachability semantics but are no longer coupled
// through one path-producing finder.
package chainledger

import "sort"

// rootSources finds, for one dataset within an already validated adjacency,
// every root source reachable by following direct upstream edges: a dataset
// that itself has no upstream is a root and its own source, a root reached
// through several branches is reported exactly once, intermediates that
// still have upstreams never qualify, and roots in disconnected components
// are never reached.
//
// Only the root NAMES are returned. They are sorted in UTF-8 byte order and
// the slice is never nil, matching what a TraceSources query against the same
// snapshot lists as its root names. The representative shortest paths that
// trace attaches to those roots are deliberately neither chosen nor copied
// here: the comparison cannot observe them.
func rootSources(dataset string, adj adjacency) []string {
	// Breadth-first reachability walk from the queried dataset along direct
	// upstream edges. Only reachability matters, so each node is marked the
	// first time any branch reaches it; no path state travels with the
	// frontier, which keeps shared upstream relations walked once regardless
	// of how many downstream branches pass through them.
	visited := map[string]bool{dataset: true}
	frontier := []string{dataset}
	roots := make([]string, 0)
	for len(frontier) > 0 {
		current := frontier
		frontier = nil
		for _, node := range current {
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
	}
	sort.Strings(roots)
	return roots
}
