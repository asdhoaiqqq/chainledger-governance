// Package chainledger implements the on-chain data governance core.
package chainledger

import (
	"slices"
	"sort"
)

// Dataset is one ingestible on-chain data source with a declared schema version.
type Dataset struct {
	Name          string
	SchemaVersion int
	Rows          int
	Upstreams     []string
}

// Lineage records where a dataset came from, so every derived table can be traced.
type Lineage struct {
	Dataset  string
	Parents  []string
	Children []string
}

// Register links a dataset into the lineage graph and rejects cycles.
//
// Re-registering an existing dataset replaces its direct upstreams with parents
// (de-duplicated, first occurrence wins, list order preserved) while keeping its
// existing direct downstreams. Reverse edges stay consistent: upstreams removed
// from the list drop the dataset from their children, newly added upstreams gain
// it at the end of their children, and retained upstreams keep exactly one entry
// in its original position.
//
// The whole request is validated before the graph is touched, so a rejected
// registration leaves every node, edge and ordering untouched. The parents slice
// is copied: mutating it after Register returns does not affect the graph.
func Register(graph map[string]*Lineage, dataset Dataset, parents []string) error {
	if dataset.Name == "" {
		return errInvalid("dataset name is required")
	}

	// Validate up front, walking the request in input order so the first problem
	// encountered is the one reported. No graph mutation happens in this loop.
	upstreams := make([]string, 0, len(parents))
	known := map[string]bool{}
	for _, parent := range parents {
		if parent == dataset.Name {
			return errInvalid("dataset " + dataset.Name + " cannot be its own parent")
		}
		if _, ok := graph[parent]; !ok {
			return errInvalid("unknown parent " + parent)
		}
		// A new edge dataset -> parent closes a cycle exactly when parent can
		// already reach dataset through existing parent edges. This covers
		// transitive paths of any depth, not only direct mutual references.
		if reaches(graph, parent, dataset.Name) {
			return errInvalid("cycle through " + parent + " for dataset " + dataset.Name)
		}
		if !known[parent] {
			known[parent] = true
			upstreams = append(upstreams, parent)
		}
	}

	entry, existed := graph[dataset.Name]
	if !existed {
		entry = &Lineage{Dataset: dataset.Name}
	}

	// Remove reverse edges from upstreams the dataset no longer declares.
	for _, old := range entry.Parents {
		if known[old] {
			continue
		}
		if p, ok := graph[old]; ok {
			p.Children = removeChild(p.Children, dataset.Name)
		}
	}

	// Mirror every declared upstream exactly once. An existing edge keeps its
	// position; a missing edge is appended after the current children.
	for _, parent := range upstreams {
		graph[parent].Children = addChildOnce(graph[parent].Children, dataset.Name)
	}

	entry.Parents = upstreams
	if !existed {
		graph[dataset.Name] = entry
	}
	return nil
}

// reaches reports whether target is reachable from node by following parent
// edges, i.e. whether target is among node's direct or transitive upstreams.
func reaches(graph map[string]*Lineage, from, target string) bool {
	seen := map[string]bool{}
	var walk func(string) bool
	walk = func(node string) bool {
		if node == target {
			return true
		}
		if seen[node] {
			return false
		}
		seen[node] = true
		entry, ok := graph[node]
		if !ok {
			return false
		}
		for _, parent := range entry.Parents {
			if walk(parent) {
				return true
			}
		}
		return false
	}
	return walk(from)
}

// removeChild drops every occurrence of child from children, preserving the
// relative order of the remaining entries.
func removeChild(children []string, child string) []string {
	out := children[:0]
	for _, c := range children {
		if c != child {
			out = append(out, c)
		}
	}
	return out
}

// addChildOnce ensures child appears exactly once in children. An existing entry
// keeps its position (duplicate later entries are dropped); otherwise child is
// appended at the end.
func addChildOnce(children []string, child string) []string {
	out := children[:0]
	present := false
	for _, c := range children {
		if c == child {
			if present {
				continue
			}
			present = true
		}
		out = append(out, c)
	}
	if !present {
		out = append(out, child)
	}
	return out
}

// Roots lists datasets with no parents, in stable order.
func Roots(graph map[string]*Lineage) []string {
	var roots []string
	for name, entry := range graph {
		if len(entry.Parents) == 0 {
			roots = append(roots, name)
		}
	}
	sort.Strings(roots)
	return roots
}

// Impact is one dataset downstream of the queried start dataset: it is
// reachable from start by following child edges. Distance is the minimum
// number of lineage edges from start (a direct downstream has distance 1)
// and Path is one shortest explanation route, beginning at start and ending
// at Name, listing datasets in upstream-to-downstream order.
type Impact struct {
	Name     string
	Distance int
	Path     []string
}

// ImpactOf reports every dataset directly or indirectly affected by start, i.e.
// every dataset reachable from start along child edges. The start dataset
// itself and datasets with no such connection are not included. When a dataset
// is reached through several branches it appears exactly once with the
// shortest distance; if several routes tie on distance, the route whose name
// sequence is lexicographically smallest (compared element by element with Go
// string ordering) is chosen. The result is ordered by distance ascending,
// ties broken by dataset name, independent of registration or list order.
//
// The graph is only read, never modified: the returned slice and every Path
// are freshly allocated, so mutating them cannot affect the graph.
func ImpactOf(graph map[string]*Lineage, start string) ([]Impact, error) {
	if start == "" {
		return nil, errInvalid("dataset name is required")
	}
	if _, ok := graph[start]; !ok {
		return nil, errInvalid("unknown dataset " + start)
	}

	// BFS in distance order. Because every edge goes one level deeper, by the
	// time a node is dequeued all equally-deep candidates have already been
	// considered, so its best route is final and can be propagated.
	distance := map[string]int{start: 0}
	bestPath := map[string][]string{start: {start}}
	queue := []string{start}
	for len(queue) > 0 {
		node := queue[0]
		queue = queue[1:]
		base := bestPath[node]
		for _, child := range graph[node].Children {
			candidate := make([]string, 0, len(base)+1)
			candidate = append(candidate, base...)
			candidate = append(candidate, child)

			d := distance[node] + 1
			current, seen := distance[child]
			if !seen {
				distance[child] = d
				bestPath[child] = candidate
				queue = append(queue, child)
				continue
			}
			if d < current || (d == current && slices.Compare(candidate, bestPath[child]) < 0) {
				distance[child] = d
				bestPath[child] = candidate
			}
		}
	}

	results := make([]Impact, 0, len(distance)-1)
	for name, d := range distance {
		if name == start {
			continue
		}
		route := make([]string, len(bestPath[name]))
		copy(route, bestPath[name])
		results = append(results, Impact{Name: name, Distance: d, Path: route})
	}
	sort.Slice(results, func(i, j int) bool {
		if results[i].Distance != results[j].Distance {
			return results[i].Distance < results[j].Distance
		}
		return results[i].Name < results[j].Name
	})
	return results, nil
}

type errInvalid string

func (e errInvalid) Error() string { return string(e) }
