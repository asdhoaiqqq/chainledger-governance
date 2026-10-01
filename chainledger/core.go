// Package chainledger implements the on-chain data governance core.
package chainledger

import (
	"errors"
	"fmt"
	"sort"
)

// Sentinel errors so callers can recognize rejection reasons with errors.Is.
var (
	// ErrInvalidArgument is returned for malformed requests (empty names,
	// nil lineage nodes referenced as upstreams).
	ErrInvalidArgument = errors.New("invalid argument")
	// ErrNotFound is returned when an upstream dataset is not registered.
	ErrNotFound = errors.New("not found")
	// ErrCycle is returned when applying the registration would form a cycle.
	ErrCycle = errors.New("cycle detected")
	// ErrNotInitialized is returned when the graph map itself is nil.
	ErrNotInitialized = errors.New("lineage graph is not initialized")
)

// Dataset is one ingestible on-chain data source with a declared schema version.
type Dataset struct {
	Name          string
	SchemaVersion int
	Rows          int
	Upstreams     []string
}

// Lineage records where a dataset came from, so every derived table can be traced.
// Parents and Children are always sorted by name and free of duplicates.
type Lineage struct {
	Dataset  string
	Parents  []string
	Children []string
}

// Register links a dataset into the lineage graph.
//
// Registering an existing dataset name REPLACES its direct upstream (parent)
// relationships with the given parents:
//   - downstream (child) relationships of the dataset are preserved;
//   - parents dropped from the list no longer list the dataset as a child;
//   - newly added parents list the dataset exactly once;
//   - a nil or empty parents slice removes every direct upstream, turning the
//     dataset into a root while keeping its downstream.
//
// Duplicate parent names count as one relationship, and the stored lineage is
// independent of the caller's slice: mutating parents after Register returns
// does not affect the graph.
//
// Registration is atomic. If any parent is missing or the resulting graph
// would contain a cycle (including a dataset depending on itself, directly or
// through other nodes), no node and no parent/child record is changed. Cycle
// validation uses the relationships as they will be AFTER the replacement, so
// an edge removed by this registration cannot make a legal change fail.
func Register(graph map[string]*Lineage, dataset Dataset, parents []string) error {
	if graph == nil {
		return fmt.Errorf("%w: create the graph map with make before registering datasets", ErrNotInitialized)
	}
	if dataset.Name == "" {
		return fmt.Errorf("%w: dataset name is required", ErrInvalidArgument)
	}

	// Normalize on a copy: dedupe (duplicates are one relationship), sort,
	// and detach from the caller's backing array.
	wanted := uniqueSorted(parents)
	for _, parent := range wanted {
		entry, ok := graph[parent]
		if !ok {
			return fmt.Errorf("%w: upstream dataset %q is not registered", ErrNotFound, parent)
		}
		if entry == nil {
			return fmt.Errorf("%w: upstream dataset %q points to a nil lineage node", ErrInvalidArgument, parent)
		}
	}

	// Snapshot the current parents so validation can reason about the graph
	// after the replacement, before anything is mutated.
	var oldParents []string
	if current := graph[dataset.Name]; current != nil {
		oldParents = append(oldParents, current.Parents...)
	}

	// Adding an edge dataset -> parent creates a cycle exactly when parent
	// can already reach the dataset by following parent edges in the
	// post-replacement graph.
	for _, parent := range wanted {
		if reachesAfterReplacement(graph, dataset.Name, wanted, parent, dataset.Name) {
			return fmt.Errorf("%w: dataset %q cannot depend on %q, it would reach itself", ErrCycle, dataset.Name, parent)
		}
	}

	// ---- All checks passed; commit below this line. ----

	current, existed := graph[dataset.Name]
	if !existed || current == nil {
		current = &Lineage{Dataset: dataset.Name}
		graph[dataset.Name] = current
	}

	newSet := toSet(wanted)
	for _, oldParent := range oldParents {
		if newSet[oldParent] {
			continue // relationship is kept
		}
		if entry := graph[oldParent]; entry != nil {
			entry.Children = removeName(entry.Children, dataset.Name)
		}
	}

	current.Parents = wanted
	for _, parent := range wanted {
		entry := graph[parent]
		entry.Children = addName(entry.Children, dataset.Name)
	}
	// Downstream is preserved; normalize defensively in case the map was
	// hand-edited before this call.
	current.Children = uniqueSorted(current.Children)

	return nil
}

// reachesAfterReplacement reports whether start can reach target by following
// parent edges. While walking, the parents of overrideNode are treated as
// overrideParents, which makes the traversal reflect the graph AS IT WILL BE
// after the pending registration rather than the graph being replaced.
func reachesAfterReplacement(graph map[string]*Lineage, overrideNode string, overrideParents []string, start, target string) bool {
	seen := map[string]bool{}
	var walk func(node string) bool
	walk = func(node string) bool {
		if node == target {
			return true
		}
		if seen[node] {
			return false
		}
		seen[node] = true

		var nextParents []string
		if node == overrideNode {
			nextParents = overrideParents
		} else if entry, ok := graph[node]; ok && entry != nil {
			nextParents = entry.Parents
		}
		for _, parent := range nextParents {
			if walk(parent) {
				return true
			}
		}
		return false
	}
	return walk(start)
}

// Roots lists datasets with no parents, sorted by name.
func Roots(graph map[string]*Lineage) []string {
	var roots []string
	for name, entry := range graph {
		if entry != nil && len(entry.Parents) == 0 {
			roots = append(roots, name)
		}
	}
	sort.Strings(roots)
	return roots
}

// uniqueSorted returns a sorted, duplicate-free copy of names. The result is
// nil when no name remains, so empty lineage stays empty.
func uniqueSorted(names []string) []string {
	if len(names) == 0 {
		return nil
	}
	seen := make(map[string]bool, len(names))
	out := make([]string, 0, len(names))
	for _, name := range names {
		if !seen[name] {
			seen[name] = true
			out = append(out, name)
		}
	}
	sort.Strings(out)
	return out
}

func toSet(names []string) map[string]bool {
	set := make(map[string]bool, len(names))
	for _, name := range names {
		set[name] = true
	}
	return set
}

// addName inserts name into the already-sorted list, skipping duplicates.
func addName(list []string, name string) []string {
	index := sort.SearchStrings(list, name)
	if index < len(list) && list[index] == name {
		return list
	}
	list = append(list, "")
	copy(list[index+1:], list[index:])
	list[index] = name
	return list
}

// removeName drops name from the already-sorted list if it is present.
func removeName(list []string, name string) []string {
	index := sort.SearchStrings(list, name)
	if index >= len(list) || list[index] != name {
		return list
	}
	return append(list[:index], list[index+1:]...)
}
