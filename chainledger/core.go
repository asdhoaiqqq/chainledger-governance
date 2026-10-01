// Package chainledger implements the on-chain data governance core.
package chainledger

import "sort"

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
// Registering a dataset that already exists replaces its direct upstream
// relationships while keeping its downstreams: parents that are no longer
// listed stop referencing it, and newly listed parents reference it exactly
// once. The replacement is validated against the post-replacement graph
// before anything is written, so a rejected call leaves every node and every
// parent/child record untouched.
func Register(graph map[string]*Lineage, dataset Dataset, parents []string) error {
	if graph == nil {
		return errInvalid("graph is not initialized")
	}
	if dataset.Name == "" {
		return errInvalid("dataset name is required")
	}

	// Normalize the requested parents up front so a rejected call cannot
	// leave partial relationships or a newly inserted node behind.
	seen := make(map[string]bool, len(parents))
	nextParents := make([]string, 0, len(parents))
	for _, parent := range parents {
		if parent == "" {
			return errInvalid("parent name is required")
		}
		if _, ok := graph[parent]; !ok {
			return errInvalid("unknown parent " + parent)
		}
		if seen[parent] {
			continue
		}
		seen[parent] = true
		nextParents = append(nextParents, parent)
	}
	sort.Strings(nextParents)

	// Cycle check against the post-replacement graph. The replacement adds
	// edges dataset.Name -> parent for every requested parent; a cycle exists
	// iff dataset.Name can reach one of those parents. Any such path ends
	// with an edge into dataset.Name from one of its children, which the
	// replacement does not affect, so the check can run on the current graph
	// without mutating it first.
	for _, parent := range nextParents {
		if parent == dataset.Name {
			return errInvalid("cycle through " + parent)
		}
		if reaches(graph, parent, dataset.Name) {
			return errInvalid("cycle through " + parent)
		}
	}

	var oldParents, oldChildren []string
	if existing := graph[dataset.Name]; existing != nil {
		oldParents = append(oldParents, existing.Parents...)
		oldChildren = append(oldChildren, existing.Children...)
	}

	// Detach parents that are no longer direct upstreams of this dataset.
	for _, parent := range oldParents {
		if seen[parent] {
			continue
		}
		entry := graph[parent]
		entry.Children = removeName(entry.Children, dataset.Name)
	}

	// Attach newly added parents, each referencing this dataset exactly once.
	for _, parent := range nextParents {
		if wasParent(oldParents, parent) {
			continue
		}
		entry := graph[parent]
		entry.Children = append(entry.Children, dataset.Name)
		sort.Strings(entry.Children)
	}

	graph[dataset.Name] = &Lineage{
		Dataset:  dataset.Name,
		Parents:  nextParents,
		Children: oldChildren,
	}
	return nil
}

func wasParent(parents []string, name string) bool {
	for _, parent := range parents {
		if parent == name {
			return true
		}
	}
	return false
}

func removeName(names []string, name string) []string {
	out := make([]string, 0, len(names))
	for _, n := range names {
		if n != name {
			out = append(out, n)
		}
	}
	return out
}

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

type errInvalid string

func (e errInvalid) Error() string { return string(e) }
