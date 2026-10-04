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

// Rename changes the registered name of the dataset oldName to newName while
// keeping the dataset's exact position in the lineage graph: the node keeps
// its own direct upstream and downstream lists untouched, and every neighbor
// that referenced oldName now references newName in the same list position.
// No edge is added, removed, merged or reordered, and the total number of
// datasets in the graph is unchanged. Roots, leaves and intermediate datasets
// can all be renamed.
//
// The request is validated before the graph is touched, so a rejected rename
// leaves every node, edge and list order untouched. Names match by exact
// registered value (case-sensitive). An empty old or new name is rejected as
// a missing name; an oldName absent from the graph is rejected with an error
// naming it; a newName already registered to a different dataset is rejected
// with an error naming it. Renaming a dataset to its own current name is a
// successful no-op.
func Rename(graph map[string]*Lineage, oldName, newName string) error {
	if oldName == "" {
		return errInvalid("dataset name is required")
	}
	if newName == "" {
		return errInvalid("new dataset name is required")
	}
	entry, ok := graph[oldName]
	if !ok {
		return errInvalid("dataset not found: " + oldName)
	}
	if oldName == newName {
		return nil
	}
	if _, taken := graph[newName]; taken {
		return errInvalid("dataset name already in use: " + newName)
	}

	delete(graph, oldName)
	entry.Dataset = newName
	graph[newName] = entry

	// Rewire the reverse references in place: the renamed node keeps its slot
	// in every neighbor's list, so each neighbor's ordering is preserved.
	for _, parent := range entry.Parents {
		children := graph[parent].Children
		for i, child := range children {
			if child == oldName {
				children[i] = newName
			}
		}
	}
	for _, child := range entry.Children {
		parents := graph[child].Parents
		for i, parent := range parents {
			if parent == oldName {
				parents[i] = newName
			}
		}
	}
	return nil
}

// Unregister removes a dataset registration from the lineage graph.
//
// Only a leaf dataset can be removed: a dataset with any direct downstream
// (a dataset still derived from it) is kept, regardless of how many upstreams
// it itself has. A removable dataset may still have direct upstreams; every
// one of them drops the dataset from its downstream list, in that list's
// original position, and the upstreams themselves and their other
// downstreams are untouched. A dataset with neither upstreams nor
// downstreams can also be removed. On success the name is gone from the
// graph: Impacts and Upstreams treat it as unregistered, and no empty node
// remains behind.
//
// The request is fully validated before the graph is touched, so a rejected
// removal leaves every node, edge and list order exactly as it was — no
// upstream can end up detached while the dataset stays. An empty name is
// rejected as a missing name; a non-empty name absent from the graph (also
// against an empty or nil graph) is rejected with an error naming it; a
// dataset that still has direct downstreams is rejected with an error that
// names it and states the remaining downstream. Names match by exact
// registered value (case-sensitive).
func Unregister(graph map[string]*Lineage, name string) error {
	if name == "" {
		return errInvalid("dataset name is required")
	}
	entry, ok := graph[name]
	if !ok {
		return errInvalid("dataset not found: " + name)
	}
	if len(entry.Children) > 0 {
		return errInvalid("dataset " + name + " cannot be unregistered while it still has direct downstreams")
	}

	// The dataset is a leaf, so its only edges point at its direct upstreams.
	// Drop the matching reverse edge from each one, preserving the remaining
	// order, then remove the node itself. The checks above guarantee nothing
	// points back at it, so the graph stays fully consistent.
	for _, parent := range entry.Parents {
		if p, ok := graph[parent]; ok {
			p.Children = removeChild(p.Children, name)
		}
	}
	delete(graph, name)
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
// Distance, the tie-broken explanation path, result ordering, read-only graph
// access and error handling all follow the single shared lineage-query rule
// set implemented by traceLineage; only the direction is specific to Impacts:
// child edges outward, with paths written from the origin. An empty origin is
// rejected as a missing name; an origin absent from the graph (including an
// empty or nil graph) is rejected with an error naming it and no partial
// results.
func Impacts(graph map[string]*Lineage, origin string) ([]Impact, error) {
	hits, err := traceLineage(graph, origin, dirDownstream, nil)
	if err != nil {
		return nil, err
	}
	impacts := make([]Impact, len(hits))
	for i, hit := range hits {
		impacts[i] = Impact{Dataset: hit.name, Distance: hit.distance, Path: hit.path}
	}
	return impacts, nil
}

// ImpactsWithCutoffs is the downstream-impact query of Impacts with a
// propagation cutoff list: impact still spreads from origin along child
// edges, but it does not spread beyond any dataset named in cutoffs. A
// cutoff reachable from origin is itself reported — with its distance and
// explanation path computed over the routes that remain — its downstreams
// are just not reached through it. A dataset sitting behind a cutoff still
// appears whenever some route from the origin reaches it without passing
// through any cutoff, and its distance and path come from those surviving
// routes only, never from a truncated one.
//
// Distance, the tie-broken explanation path (fewest edges first, then the
// lexicographically smallest full path compared name by name from the
// origin in Go string order), one record per dataset, result ordering by
// distance then name, read-only graph access and independently copied paths
// all follow the single shared lineage-query rule set implemented by
// traceLineage, exactly as in Impacts. An empty or nil cutoff list makes
// ImpactsWithCutoffs identical to Impacts.
//
// Cutoff names match by exact registered value (case-sensitive) and
// duplicates in the list act once. A registered cutoff that origin cannot
// reach changes nothing. Listing origin itself as a cutoff is legal: the
// query succeeds and returns a non-nil empty list, and origin is still not
// listed as impacted. The whole request is validated before any result is
// computed: an empty origin is rejected as a missing name and an
// unregistered origin with an error naming it, both as in Impacts; an empty
// cutoff name is rejected as a missing name and an unregistered cutoff with
// an error naming it. Any rejection fails the whole query with nil results.
func ImpactsWithCutoffs(graph map[string]*Lineage, origin string, cutoffs []string) ([]Impact, error) {
	hits, err := traceLineage(graph, origin, dirDownstream, cutoffs)
	if err != nil {
		return nil, err
	}
	impacts := make([]Impact, len(hits))
	for i, hit := range hits {
		impacts[i] = Impact{Dataset: hit.name, Distance: hit.distance, Path: hit.path}
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
// Distance, the tie-broken explanation path, result ordering, read-only graph
// access and error handling all follow the single shared lineage-query rule
// set implemented by traceLineage; only the direction is specific to
// Upstreams: parent edges inward, with paths written from each source toward
// the target. An empty target is rejected as a missing name; a target absent
// from the graph (including an empty or nil graph) is rejected with an error
// naming it and no partial results.
func Upstreams(graph map[string]*Lineage, target string) ([]Upstream, error) {
	hits, err := traceLineage(graph, target, dirUpstream, nil)
	if err != nil {
		return nil, err
	}
	upstreams := make([]Upstream, len(hits))
	for i, hit := range hits {
		upstreams[i] = Upstream{Dataset: hit.name, Distance: hit.distance, Path: hit.path}
	}
	return upstreams, nil
}

// queryDirection carries the only way the two lineage queries differ: which
// edges to walk and from which end an explanation path is written.
type queryDirection int

const (
	// dirDownstream follows child edges away from the query's origin and grows
	// explanation paths at the far end (origin -> ... -> dataset).
	dirDownstream queryDirection = iota
	// dirUpstream follows parent edges away from the query's target and grows
	// explanation paths at the near end (source -> ... -> target).
	dirUpstream
)

// neighbors lists the datasets one edge beyond node in the query direction.
func (d queryDirection) neighbors(node *Lineage) []string {
	if d == dirDownstream {
		return node.Children
	}
	return node.Parents
}

// extendPath grows a committed shortest path by one hop, placing the new hop
// where the explanation direction requires: a downstream hop is appended
// (origin -> node -> hop), an upstream hop is prepended (hop -> node ->
// target). The result always gets a fresh backing array, so candidates never
// alias each other, committed paths, or slices stored in the graph.
func (d queryDirection) extendPath(path []string, hop string) []string {
	extended := make([]string, len(path)+1)
	if d == dirDownstream {
		copy(extended, path)
		extended[len(extended)-1] = hop
		return extended
	}
	extended[0] = hop
	copy(extended[1:], path)
	return extended
}

// lineageHit is one direction-neutral query result: a reached dataset besides
// the query's own node, its shortest edge distance, and the lexicographically
// smallest shortest explanation path held in an independent slice.
type lineageHit struct {
	name     string
	distance int
	path     []string
}

// traceLineage runs the one query rule set shared by Impacts,
// ImpactsWithCutoffs and Upstreams and returns every dataset reachable from
// start besides start itself.
//
// The traversal is a BFS that commits a whole distance level at once, so a node
// reached through several branches within the same level keeps exactly one
// candidate before the level commits. The rules shared by both directions are:
//
//   - Distance is the minimum number of lineage edges; a node committed on an
//     earlier level can never be re-explained at a longer one (Register
//     rejects cycles, so equal-length rediscoveries only happen inside one
//     level's candidate map).
//   - Each dataset appears at most once, however many branches merge into it.
//   - Among equal-length shortest paths, the lexicographically smallest full
//     path wins: names are compared one by one from the path's beginning in Go
//     string order. Downstream paths begin at the query origin and upstream
//     paths at the source, so candidates for one upstream share their first
//     hop and compare directly against the committed tails.
//   - Hits are ordered by distance ascending and then by dataset name, never
//     by registration order or the stored parent/child list order.
//
// cutoffs is the propagation cutoff list (nil or empty for the plain full
// query, which is what Impacts and Upstreams pass). Every cutoff name must be
// non-empty and registered, checked in list order after the start checks, and
// any problem fails the whole query with nil results; duplicates act once. A
// committed cutoff node is reported like any other hit but is never expanded,
// so nothing beyond it is reached through it — while routes that avoid every
// cutoff still reach, and solely determine the distance and path of, whatever
// lies behind one. Because distance and path are committed only from routes
// the traversal actually walks, a truncated route can never lend its length
// or its names to a surviving hit. Listing start itself as a cutoff simply
// stops the traversal at level 0, yielding a non-nil empty result.
//
// Explanation paths are written in the query direction (see extendPath). The
// graph is read only and every returned path is an independent copy. An empty
// start is rejected as a missing name; an unregistered start (also against an
// empty or nil graph) is rejected with an error naming it, with nil results;
// a registered start that reaches nothing returns a non-nil empty slice.
func traceLineage(graph map[string]*Lineage, start string, direction queryDirection, cutoffs []string) ([]lineageHit, error) {
	if start == "" {
		return nil, errInvalid("dataset name is required")
	}
	if _, ok := graph[start]; !ok {
		return nil, errInvalid("dataset not found: " + start)
	}

	// Validate the cutoff list before any traversal, in input order so the
	// first problem encountered is the one reported. The graph is read only,
	// so a rejected list leaves nothing to undo.
	blocked := map[string]bool{}
	for _, name := range cutoffs {
		if name == "" {
			return nil, errInvalid("cutoff dataset name is required")
		}
		if _, ok := graph[name]; !ok {
			return nil, errInvalid("cutoff dataset not found: " + name)
		}
		blocked[name] = true
	}

	distance := map[string]int{start: 0}
	best := map[string][]string{start: {start}}
	frontier := []string{start}
	for level := 0; len(frontier) > 0; level++ {
		nextDistance := level + 1
		candidates := map[string][]string{}
		var next []string
		for _, node := range frontier {
			if blocked[node] {
				continue // a cutoff is reported but never propagates further
			}
			for _, hop := range direction.neighbors(graph[node]) {
				if _, seen := distance[hop]; seen {
					continue // reached on an earlier, strictly shorter level
				}
				candidate := direction.extendPath(best[node], hop)
				if current, ok := candidates[hop]; !ok || lessPath(candidate, current) {
					if !ok {
						next = append(next, hop)
					}
					candidates[hop] = candidate
				}
			}
		}
		for _, hop := range next {
			distance[hop] = nextDistance
			best[hop] = candidates[hop]
		}
		frontier = next
	}

	hits := make([]lineageHit, 0, len(distance)-1)
	for name, d := range distance {
		if name == start {
			continue
		}
		hits = append(hits, lineageHit{
			name:     name,
			distance: d,
			path:     append([]string(nil), best[name]...),
		})
	}
	sort.Slice(hits, func(i, j int) bool {
		if hits[i].distance != hits[j].distance {
			return hits[i].distance < hits[j].distance
		}
		return hits[i].name < hits[j].name
	})
	return hits, nil
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
