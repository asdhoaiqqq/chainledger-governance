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
// be thrown away. Tracing does carry paths in its report, but its walk stores
// none either: one predecessor pointer per reached dataset builds a single
// shared tree, and only the reported roots expand their route into a name
// slice (see traceRootSources). Walking the same normalized adjacency under the
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
//
// The search never stores a full path at an intermediate node. A long single
// chain used to make storage grow quadratically: each of the O(depth)
// intermediate datasets kept its own complete path copy, even though only the
// root's path is ever reported. Instead the walk keeps ONE compact tree of the
// visit: one parent pointer per reached dataset and, per dataset reached with
// siblings in the same round, one list of its competing predecessor names.
// Both are the node's own choice state, never a path copy, so the tree's size
// is linear in the reached datasets and direct relations, and reconstruction
// copies a name only into a path the report actually carries.
func traceRootSources(dataset string, adj adjacency) []SourceTrace {
	// A level-by-level shortest walk from the queried dataset along direct
	// upstream edges. The level at which a node is first reached is its
	// shortest distance, because every edge has the same weight; only
	// predecessors reached on that level can sit on a shortest path.
	//
	// parent[node] is the chosen predecessor on the shortest, tie-broken path
	// from the query to node; the query itself carries the empty string. A node
	// first reached in the current round collects every predecessor that reaches
	// it on that shortest level in ties[node]; the round end promotes the winner.
	//
	// rank[node] is that node's position when every node reached on its level
	// is sorted by its full query-to-node path, from the query end in UTF-8
	// byte order. Two equally long routes therefore compare in constant time:
	// the one through the lower-ranked predecessor is smaller at the first
	// differing name, with no route expanded into a name slice.
	parent := map[string]string{dataset: ""}
	rank := map[string]int{dataset: 0}
	ties := map[string][]string{}
	frontier := []string{dataset}
	for len(frontier) > 0 {
		for _, node := range frontier {
			for _, pred := range adj[node] {
				if _, seen := parent[pred]; seen {
					continue
				}
				ties[pred] = append(ties[pred], node)
			}
		}
		next := make([]string, 0, len(ties))
		for pred, predecessors := range ties {
			best := predecessors[0]
			for _, candidate := range predecessors[1:] {
				if rank[candidate] < rank[best] {
					best = candidate
				}
			}
			parent[pred] = best
			next = append(next, pred)
		}
		// Rank this level: each route is the predecessor's route followed by
		// the node's own name, and all routes at one level have equal length,
		// so lexicographic order from the query end is (predecessor rank, own
		// name). Equal predecessor rank would mean the same predecessor, which
		// two distinct nodes of one level cannot share as a tie.
		sort.Slice(next, func(i, j int) bool {
			ri, rj := rank[parent[next[i]]], rank[parent[next[j]]]
			if ri != rj {
				return ri < rj
			}
			return next[i] < next[j]
		})
		for i, node := range next {
			rank[node] = i
		}
		ties = map[string][]string{}
		frontier = next
	}

	// Every reached node without upstreams is a root. Reconstruct each path
	// once by following parent pointers from the root back to the query, then
	// reversing into a fresh slice. Reachable nodes, predecessor comparisons
	// (ties lists), and names copied into reported paths are all linear, so no
	// storage keeps a full prefix for an intermediate dataset.
	sources := make([]SourceTrace, 0)
	for node := range parent {
		if len(adj[node]) != 0 {
			continue
		}
		path := make([]string, 0, 1)
		for cur := node; cur != ""; cur = parent[cur] {
			path = append(path, cur)
		}
		for i, j := 0, len(path)-1; i < j; i, j = i+1, j-1 {
			path[i], path[j] = path[j], path[i]
		}
		sources = append(sources, SourceTrace{Root: node, Path: path})
	}
	sort.Slice(sources, func(i, j int) bool { return sources[i].Root < sources[j].Root })
	return sources
}
