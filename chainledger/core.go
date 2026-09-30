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
func Register(graph map[string]*Lineage, dataset Dataset, parents []string) error {
	if dataset.Name == "" {
		return errInvalid("dataset name is required")
	}
	for _, parent := range parents {
		entry, ok := graph[parent]
		if !ok {
			return errInvalid("unknown parent " + parent)
		}
		if reaches(graph, dataset.Name, parent) {
			return errInvalid("cycle through " + parent)
		}
		entry.Children = append(entry.Children, dataset.Name)
	}
	graph[dataset.Name] = &Lineage{Dataset: dataset.Name, Parents: append([]string(nil), parents...)}
	return nil
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
