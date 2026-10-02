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

// Impact names one dataset directly or indirectly downstream of an impact
// query's origin. Distance is the number of lineage edges on the shortest path
// from the origin to this dataset (a direct downstream has distance 1), and
// Path is that shortest path written from the origin to the dataset, one name
// per hop.
type Impact struct {
	Dataset  string
	Distance int
	Path     []string
}

// Impacts returns every dataset directly or indirectly downstream of origin,
// i.e. every dataset reachable from origin by following child edges. The origin
// itself is never listed, and datasets with no such connection never appear.
//
// Distance is the shortest edge count; each dataset is returned at most once
// even where several branches merge into it. When several shortest paths exist,
// the lexicographically smallest full path (name by name, Go string order) is
// reported. Results are ordered by distance and then by dataset name, never by
// registration order or the stored parent/child list order.
//
// The graph is read only: a successful or failed query changes no node, edge or
// ordering. The returned Path slices are independent copies, so mutating them
// cannot affect the graph. An empty origin is rejected as a missing name; an
// origin absent from the graph (including an empty or nil graph) is rejected
// with an error naming it and no partial results.
func Impacts(graph map[string]*Lineage, origin string) ([]Impact, error) {
	if origin == "" {
		return nil, errInvalid("dataset name is required")
	}
	if _, ok := graph[origin]; !ok {
		return nil, errInvalid("dataset not found: " + origin)
	}

	// BFS one distance level at a time. A node commits its distance and best
	// (lexicographically smallest) shortest path only once the whole level is
	// known, so candidates reaching the same child through different parents in
	// the same level can be compared before either wins. Register rejects
	// cycles, so a committed node is never reached again at an equal level.
	distance := map[string]int{origin: 0}
	best := map[string][]string{origin: {origin}}
	frontier := []string{origin}
	for d := 0; len(frontier) > 0; d++ {
		nextDistance := d + 1
		candidates := map[string][]string{}
		var next []string
		for _, node := range frontier {
			for _, child := range graph[node].Children {
				if _, seen := distance[child]; seen {
					continue // reached on an earlier, strictly shorter level
				}
				// Fresh backing array per candidate: paths must not alias each
				// other or any slice stored in the graph.
				candidate := make([]string, len(best[node])+1)
				copy(candidate, best[node])
				candidate[len(candidate)-1] = child
				if current, ok := candidates[child]; !ok || lessPath(candidate, current) {
					if !ok {
						next = append(next, child)
					}
					candidates[child] = candidate
				}
			}
		}
		for _, child := range next {
			distance[child] = nextDistance
			best[child] = candidates[child]
		}
		frontier = next
	}

	impacts := make([]Impact, 0, len(distance)-1)
	for name, d := range distance {
		if name == origin {
			continue
		}
		impacts = append(impacts, Impact{
			Dataset:  name,
			Distance: d,
			Path:     append([]string(nil), best[name]...),
		})
	}
	sort.Slice(impacts, func(i, j int) bool {
		if impacts[i].Distance != impacts[j].Distance {
			return impacts[i].Distance < impacts[j].Distance
		}
		return impacts[i].Dataset < impacts[j].Dataset
	})
	return impacts, nil
}

// lessPath reports whether path a sorts before path b as a name sequence:
// names are compared with Go string order from the start, and an equal prefix
// makes the shorter path smaller.
func lessPath(a, b []string) bool {
	for i := 0; i < len(a) && i < len(b); i++ {
		if a[i] != b[i] {
			return a[i] < b[i]
		}
	}
	return len(a) < len(b)
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
