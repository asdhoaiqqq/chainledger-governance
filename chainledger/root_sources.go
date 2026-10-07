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
// be thrown away. Tracing must additionally carry one representative path per
// root, but it keeps only constant state per reached dataset while walking — a
// single predecessor and a route rank — and builds each reported root's path
// once, at the end, instead of copying a growing full-path prefix into every
// intermediate. Walking the same normalized adjacency under the same root rule
// keeps the two reports in agreement: a root is reachable for the comparison
// exactly when a trace against the same snapshot lists it.
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

// traceRouteState is all the per-dataset state the trace walk keeps while it
// runs: pred is the direct upstream on the chosen shortest, tie-broken route
// from the queried dataset (empty only for the query itself, since validated
// names are never empty), and rank is the lexicographic rank of that route
// among the routes finalized in the same BFS level.
type traceRouteState struct {
	pred string
	rank int
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
//
// The walk stores only constant state per reached dataset — one predecessor
// and one route rank — never a copy of the growing path, so the storage the
// tracing stage adds is linear in the reached datasets, the direct relations
// scanned, and the names of the paths it actually reports: a single long chain
// no longer makes every intermediate hold the full prefix shared with the one
// reported path. A reported root's full path is reconstructed once at the end
// by following predecessors, which is also what gives roots past a merge point
// independent slices.
func traceRootSources(dataset string, adj adjacency) []SourceTrace {
	// Breadth-first walk from the queried dataset along direct upstream edges.
	// Every edge has the same weight, so the level at which a dataset is first
	// reached is its shortest distance; state holds its chosen route and the
	// level-local rank of that route.
	state := map[string]traceRouteState{dataset: {rank: 0}}
	frontier := []string{dataset}
	for len(frontier) > 0 {
		// candidate[parent] is the best route by which a frontier node reaches
		// a not-yet-finalized parent at the next level. All such routes append
		// the same parent name, so they differ only in the predecessor's route,
		// which the predecessor's rank compares in O(1): the smallest rank is
		// exactly the lexicographically smallest full path, with no path copy.
		candidate := make(map[string]traceRouteState)
		for _, node := range frontier {
			nodeRank := state[node].rank
			for _, parent := range adj[node] {
				if _, seen := state[parent]; seen {
					continue
				}
				if best, ok := candidate[parent]; !ok || nodeRank < best.rank {
					candidate[parent] = traceRouteState{pred: node, rank: nodeRank}
				}
			}
		}
		if len(candidate) == 0 {
			break
		}

		// Finalize the next level and assign its route ranks. A route is
		// (predecessor route, own name): sorting the level by (predecessor
		// rank, name) therefore reproduces element-by-element UTF-8 byte order
		// of the complete equal-length paths without ever holding them.
		next := make([]string, 0, len(candidate))
		for node := range candidate {
			next = append(next, node)
		}
		sort.Slice(next, func(i, j int) bool {
			ri, rj := candidate[next[i]].rank, candidate[next[j]].rank
			if ri != rj {
				return ri < rj
			}
			return next[i] < next[j]
		})
		for rank, node := range next {
			state[node] = traceRouteState{pred: candidate[node].pred, rank: rank}
		}
		frontier = next
	}

	// Reachable datasets without upstreams are roots, including the query when
	// it is one. Report them sorted by root name byte order; each path is a
	// fresh slice walked over the predecessors and reversed, so shared prefixes
	// are copied per root and returned paths never alias one another.
	roots := make([]string, 0)
	for node := range state {
		if len(adj[node]) == 0 {
			roots = append(roots, node)
		}
	}
	sort.Strings(roots)

	sources := make([]SourceTrace, 0, len(roots))
	for _, root := range roots {
		path := []string{root}
		for node := state[root].pred; node != ""; node = state[node].pred {
			path = append(path, node)
		}
		for left, right := 0, len(path)-1; left < right; left, right = left+1, right-1 {
			path[left], path[right] = path[right], path[left]
		}
		sources = append(sources, SourceTrace{Root: root, Path: path})
	}
	return sources
}
