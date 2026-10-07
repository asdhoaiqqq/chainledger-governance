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
// The relationship change is determined solely by the submitted dataset name
// and its new upstreams. A hand-constructed graph may have two independent
// Lineage records whose Children (or Parents) slices share one backing array,
// overlap in a sub-slice window, or carry spare capacity another record's
// list occupies. Such a graph is legal as long as names correspond and no
// reference or cycle is missing, so Register never edits those name lists in
// place: a parent list that gains or loses the dataset receives a freshly
// allocated, independent slice (see withName and withoutName). Removing one
// upstream can therefore never shift or duplicate names another record's
// list shares, and adding one can never overwrite a name living in spare
// capacity. Records of datasets not involved in the change keep their
// previous contents.
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

	// Snapshot the relationships this commit changes on independent slices
	// BEFORE mutating anything. The commit decides from these copies which
	// edges are kept, added, and removed, and reassigns the dataset's own
	// lists to them; stored lists of other records (which may share backing
	// storage) are never resliced in place.
	current, existed := graph[dataset.Name]
	var oldParents, oldChildren []string
	if current != nil {
		oldParents = append(oldParents, current.Parents...)
		// Normalize the preserved downstream on the copy as well, so a
		// hand-edited unsorted or duplicated list is repaired without an
		// in-place write.
		oldChildren = uniqueSorted(current.Children)
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
	//
	// No name list is edited in place. An in-place removal
	// (append(list[:i], list[i+1:]...)) shifts the shared backing array and
	// corrupts every other record whose list shares that storage — the removed
	// name lands in a neighbor's window and a kept name can duplicate or
	// vanish. In-place insertion is equally unsafe when the list has spare
	// capacity another record's list occupies. withName and withoutName
	// allocate independent slices instead.

	if !existed || current == nil {
		current = &Lineage{Dataset: dataset.Name}
		graph[dataset.Name] = current
	}
	current.Parents = wanted
	current.Children = oldChildren

	newSet := toSet(wanted)
	oldSet := toSet(oldParents)

	// Dropped upstreams lose the dataset as a child. Every name list is
	// rewritten on a fresh slice (see withoutName): an in-place removal would
	// shift the shared backing array and corrupt another record's list that
	// happens to share that storage.
	for _, oldParent := range oldParents {
		if newSet[oldParent] {
			continue // relationship is kept; that record's list is not touched
		}
		if entry := graph[oldParent]; entry != nil {
			entry.Children = withoutName(entry.Children, dataset.Name)
		}
	}

	// Only newly added upstreams gain the dataset as a child, each listing it
	// exactly once. withName allocates a fresh slice, so spare capacity in the
	// stored list can never overwrite names another record's list occupies in
	// the same backing array. Kept upstreams already list the dataset and are
	// deliberately left alone, so an unrelated record sharing their list sees
	// no movement at all.
	for _, parent := range wanted {
		if oldSet[parent] {
			continue
		}
		entry := graph[parent]
		entry.Children = withName(entry.Children, dataset.Name)
	}

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

// withName returns a NEW sorted, duplicate-free slice that is list with name
// added. It never writes through list's backing array: that array may be
// shared with another record's name list (or may have spare capacity another
// record's window occupies), so an in-place insertion could overwrite names
// the other record still owns. The input list itself may be unsorted or hold
// duplicate names in a hand-constructed graph, so the result is rebuilt by
// copy and normalized with uniqueSorted rather than by binary-search
// insertion: name ends up present exactly once, at its byte-order position,
// and every other name is preserved exactly once as well.
func withName(list []string, name string) []string {
	combined := make([]string, 0, len(list)+1)
	combined = append(combined, list...)
	combined = append(combined, name)
	return uniqueSorted(combined)
}

// withoutName returns a NEW sorted, duplicate-free slice equal to list with
// EVERY occurrence of name omitted. Unlike an in-place
// append(list[:i], list[i+1:]...), it never shifts the original backing
// array, so removing a name from one record can not move or duplicate the
// names another record's list shares in the same storage. The input list may
// be unsorted or hold the name (or other names) more than once, so removal is
// a full filter rather than a binary search — a search that assumes sorted
// input can miss the name or drop only one of several copies. A list without
// the name — including one that becomes empty — is still returned as an
// independent slice (nil when empty, matching the package's empty-lineage
// convention).
func withoutName(list []string, name string) []string {
	remaining := make([]string, 0, len(list))
	for _, existing := range list {
		if existing != name {
			remaining = append(remaining, existing)
		}
	}
	return uniqueSorted(remaining)
}
