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
	// All cycle checks in this one request share a single memoized walk over the
	// graph as it stands now: probe records whether each visited node can reach
	// dataset.Name through existing parent edges. Candidate upstreams that come
	// from the same source or merge repeatedly (diamonds) then traverse the
	// shared lineage once instead of re-walking it per candidate. The recorded
	// conclusion is a property of the current graph, so it is never reused
	// across registrations.
	probe := newAncestorProbe(graph, dataset.Name)
	for _, parent := range parents {
		if parent == dataset.Name {
			return errInvalid("dataset " + dataset.Name + " cannot be its own parent")
		}
		if _, ok := graph[parent]; !ok {
			return errInvalid("unknown parent " + parent)
		}
		// A new edge dataset -> parent closes a cycle exactly when parent can
		// already reach dataset through existing parent edges. This covers
		// transitive paths of any depth, not only direct mutual references;
		// shared ancestors and diamond merges read as the benign merges they are
		// unless a branch genuinely leads back to dataset.
		if probe.canReach(parent) {
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

// ancestorProbe answers "can node reach target by following parent edges?" for
// many starting nodes against one fixed, read-only snapshot of the graph, i.e.
// whether target is among each node's direct or transitive upstreams. Each node
// in the shared ancestry is resolved at most once, so candidate parents that
// descend from a common source or merge along the way reuse the same findings
// instead of each paying for a complete traversal.
type ancestorProbe struct {
	graph  map[string]*Lineage
	target string
	// resolved caches the conclusion for every fully explored node; visiting
	// marks nodes mid-resolution. Register never accepts cycles, but visiting
	// doubles as cycle protection regardless.
	resolved map[string]bool
	visiting map[string]bool
}

func newAncestorProbe(graph map[string]*Lineage, target string) *ancestorProbe {
	return &ancestorProbe{
		graph:    graph,
		target:   target,
		resolved: map[string]bool{},
		visiting: map[string]bool{},
	}
}

func (p *ancestorProbe) canReach(node string) bool {
	if node == p.target {
		return true
	}
	if hit, ok := p.resolved[node]; ok {
		return hit
	}
	if p.visiting[node] {
		return false
	}
	p.visiting[node] = true

	found := false
	if entry, ok := p.graph[node]; ok {
		for _, parent := range entry.Parents {
			if p.canReach(parent) {
				found = true
				break
			}
		}
	}
	delete(p.visiting, node)
	p.resolved[node] = found
	return found
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
	if err := requireDataset(graph, origin); err != nil {
		return nil, err
	}

	// Downstream walk: follow child edges away from the origin, so each newly
	// reached dataset is appended to the committed path of the node it derives
	// from.
	hits := walkLineage(graph, origin, func(entry *Lineage) []string { return entry.Children }, appendHop)
	impacts := make([]Impact, 0, len(hits))
	for _, hit := range hits {
		impacts = append(impacts, Impact{
			Dataset:  hit.dataset,
			Distance: hit.distance,
			Path:     hit.path,
		})
	}
	return impacts, nil
}

// Upstream names one dataset directly or indirectly upstream of a provenance
// query's target. Distance is the number of lineage edges on the shortest path
// from this upstream to the target (a direct upstream has distance 1), and
// Path is that shortest path written along the actual derivation direction,
// from this upstream to the queried dataset, one name per hop.
type Upstream struct {
	Dataset  string
	Distance int
	Path     []string
}

// Upstreams returns every dataset directly or indirectly upstream of target,
// i.e. every dataset reachable from target by following parent edges. The
// target itself is never listed, its downstreams never appear, and datasets
// with no such connection never appear.
//
// Distance is the shortest edge count; each upstream is returned at most once
// even where it feeds the target through several branches. When several
// shortest paths exist, the lexicographically smallest full path (name by
// name from the upstream onward, Go string order) is reported. Results are
// ordered by distance and then by upstream name, never by registration order
// or the stored parent/child list order.
//
// The graph is read only: a successful or failed query changes no node, edge
// or ordering. The returned Path slices are independent copies, so mutating
// them cannot affect the graph, other records, or later queries. An empty
// target is rejected as a missing name; a target absent from the graph
// (including an empty or nil graph) is rejected with an error naming it and
// no partial results.
func Upstreams(graph map[string]*Lineage, target string) ([]Upstream, error) {
	if err := requireDataset(graph, target); err != nil {
		return nil, err
	}

	// Upstream walk: follow parent edges away from the target. Paths are still
	// written in the derivation direction (from the upstream to the target), so
	// each newly reached upstream is prepended to the committed path of the
	// node it feeds.
	hits := walkLineage(graph, target, func(entry *Lineage) []string { return entry.Parents }, prependHop)
	upstreams := make([]Upstream, 0, len(hits))
	for _, hit := range hits {
		upstreams = append(upstreams, Upstream{
			Dataset:  hit.dataset,
			Distance: hit.distance,
			Path:     hit.path,
		})
	}
	return upstreams, nil
}

// requireDataset enforces the query preconditions shared by Impacts and
// Upstreams: an empty name is rejected as a missing name, and a name absent
// from the graph (including an empty or nil graph) is rejected with an error
// naming it. Nothing is read or written beyond the membership check.
func requireDataset(graph map[string]*Lineage, name string) error {
	if name == "" {
		return errInvalid("dataset name is required")
	}
	if _, ok := graph[name]; !ok {
		return errInvalid("dataset not found: " + name)
	}
	return nil
}

// lineageHit is one dataset reached by a lineage walk, before it is shaped
// into the direction-specific result type (Impact or Upstream).
type lineageHit struct {
	dataset  string
	distance int
	path     []string
}

// walkLineage performs the level-by-level breadth-first traversal shared by
// Impacts and Upstreams. edges selects which adjacency list to follow
// (children downstream, parents upstream); extend builds a candidate path by
// adding the newly reached name to the committed path of the node it was
// reached from (appended downstream, prepended upstream), so paths always read
// in the derivation direction regardless of the walk direction.
//
// A node commits its distance and best (lexicographically smallest) shortest
// path only once the whole level is known, so candidates reaching the same
// node through different frontier nodes in the same level can be compared
// before either wins. Register rejects cycles, so a committed node is never
// reached again at an equal level. The start node itself is never reported.
//
// Hits are ordered by distance and then by dataset name, never by
// registration order or the stored parent/child list order. The graph is read
// only, and every returned path is an independent copy that aliases neither
// the graph nor any other result.
func walkLineage(graph map[string]*Lineage, start string, edges func(*Lineage) []string, extend func(path []string, next string) []string) []lineageHit {
	distance := map[string]int{start: 0}
	best := map[string][]string{start: {start}}
	frontier := []string{start}
	for d := 0; len(frontier) > 0; d++ {
		nextDistance := d + 1
		candidates := map[string][]string{}
		var next []string
		for _, node := range frontier {
			for _, neighbor := range edges(graph[node]) {
				if _, seen := distance[neighbor]; seen {
					continue // reached on an earlier, strictly shorter level
				}
				candidate := extend(best[node], neighbor)
				if current, ok := candidates[neighbor]; !ok || lessPath(candidate, current) {
					if !ok {
						next = append(next, neighbor)
					}
					candidates[neighbor] = candidate
				}
			}
		}
		for _, neighbor := range next {
			distance[neighbor] = nextDistance
			best[neighbor] = candidates[neighbor]
		}
		frontier = next
	}

	hits := make([]lineageHit, 0, len(distance)-1)
	for name, d := range distance {
		if name == start {
			continue
		}
		hits = append(hits, lineageHit{
			dataset:  name,
			distance: d,
			path:     append([]string(nil), best[name]...),
		})
	}
	sort.Slice(hits, func(i, j int) bool {
		if hits[i].distance != hits[j].distance {
			return hits[i].distance < hits[j].distance
		}
		return hits[i].dataset < hits[j].dataset
	})
	return hits
}

// appendHop extends a path that grows away from the walk's start (downstream
// queries) with one more hop at the end. The result always has a fresh backing
// array: paths must not alias each other or any slice stored in the graph.
func appendHop(path []string, next string) []string {
	extended := make([]string, len(path)+1)
	copy(extended, path)
	extended[len(path)] = next
	return extended
}

// prependHop extends a path that grows toward the walk's start (upstream
// queries) with one more hop at the front, keeping the path written from the
// upstream toward the queried target. The result always has a fresh backing
// array: paths must not alias each other or any slice stored in the graph.
func prependHop(path []string, next string) []string {
	extended := make([]string, len(path)+1)
	extended[0] = next
	copy(extended[1:], path)
	return extended
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
