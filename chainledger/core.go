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

// Register links a dataset into the lineage graph, replacing its direct upstreams
// and rejecting cycles.
//
// Registering a name that already exists replaces that dataset's direct upstreams
// with the explicitly given parents while preserving its existing direct
// downstreams; the downstreams' parent links back to it stay valid. The parent
// list is deduplicated by first occurrence, and the stored upstream order follows
// the input list. The parents slice is copied, so later mutation by the caller
// cannot change the registered result.
//
// Registration is atomic: if any validation fails (empty dataset or upstream
// name, self-reference, unknown upstream, or a cycle), the graph is left
// completely unchanged and no new node is created. Problems are reported in
// input order, and error messages name the offending dataset/upstream.
//
// A cycle is judged against the actual data flow: adding dataset -> parent
// creates a cycle exactly when parent can reach dataset by following parent
// edges. dataset is treated as a terminal target because its own outgoing edges
// are the ones being replaced.
func Register(graph map[string]*Lineage, dataset Dataset, parents []string) error {
	if dataset.Name == "" {
		return errInvalid("dataset name is required")
	}

	// Validate and deduplicate parents in a single input-order pass.
	resolved := make([]string, 0, len(parents))
	seen := make(map[string]bool, len(parents))
	for _, parent := range parents {
		if parent == "" {
			return errInvalid("upstream name is required")
		}
		if parent == dataset.Name {
			return errInvalid("dataset cannot be its own upstream: " + parent)
		}
		if _, ok := graph[parent]; !ok {
			return errInvalid("unknown upstream: " + parent)
		}
		if seen[parent] {
			continue
		}
		seen[parent] = true
		resolved = append(resolved, parent)

		// Adding dataset -> parent creates a cycle iff parent can reach
		// dataset by following parent edges. dataset is a terminal target:
		// its own outgoing edges are being replaced and must not take part.
		if reaches(graph, parent, dataset.Name) {
			return errInvalid("cycle detected through upstream: " + parent)
		}
	}

	// All checks passed; apply the replacement atomically.
	node, exists := graph[dataset.Name]
	var oldParents []string
	if exists {
		oldParents = node.Parents
	}

	// Drop the reverse link on upstreams that are no longer direct parents.
	for _, old := range oldParents {
		if !seen[old] {
			if entry := graph[old]; entry != nil {
				entry.Children = removeName(entry.Children, dataset.Name)
			}
		}
	}
	// Add the reverse link on new upstreams exactly once; upstreams that
	// already record this dataset are left untouched so order stays stable.
	for _, parent := range resolved {
		entry := graph[parent]
		if entry == nil {
			continue
		}
		if !containsName(entry.Children, dataset.Name) {
			entry.Children = append(entry.Children, dataset.Name)
		}
	}

	if !exists {
		graph[dataset.Name] = &Lineage{
			Dataset:  dataset.Name,
			Parents:  resolved,
			Children: nil,
		}
	} else {
		node.Parents = resolved
		// node.Children is intentionally preserved: existing downstreams
		// keep their relative order, and later registrations append.
	}
	return nil
}

// reaches reports whether target can be reached from start by following parent
// edges. target is matched on entry and never expanded, so it acts as a terminal
// node.
func reaches(graph map[string]*Lineage, start, target string) bool {
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
	return walk(start)
}

func containsName(list []string, name string) bool {
	for _, v := range list {
		if v == name {
			return true
		}
	}
	return false
}

func removeName(list []string, name string) []string {
	out := make([]string, 0, len(list))
	for _, v := range list {
		if v != name {
			out = append(out, v)
		}
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

type errInvalid string

func (e errInvalid) Error() string { return string(e) }
