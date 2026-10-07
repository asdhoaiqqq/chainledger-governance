package chainledger

import "sort"

// Dependency is one direct lineage relationship in an upstream comparison
// result, written in the same direction the exports use: From is the upstream
// dataset and To is the dataset derived from it.
type Dependency struct {
	From string
	To   string
}

// UpstreamLineageDiff is the outcome of comparing one target's complete
// upstream lineage between two lineage documents. Added holds the direct
// dependencies that exist only in the after-change document; Removed holds
// those that exist only in the before-change document. A relationship present
// in both is listed in neither.
type UpstreamLineageDiff struct {
	Added   []Dependency
	Removed []Dependency
}

// CompareUpstreamLineage compares one target dataset's complete upstream
// lineage between two lineage JSON documents: beforeText is the lineage as it
// stood before a change and afterText the lineage afterwards, each in the same
// nodes/edges document shape ImportLineage and the exports accept. It returns
// which of the target's dependencies were added and which removed, not merely
// how its direct upstream list changed.
//
// The comparison scope is exactly the target's full upstream derivation in
// each document: the target itself, every direct or indirect upstream it
// actually depends on, and every direct dependency carried by those routes —
// the same node and edge set ExportUpstreamLineage writes. Every branch is
// retained: when a source reaches the target both directly and through
// intermediate datasets, deleting one edge on the longer route is reflected in
// the result even though the direct relation survives, and if that deletion
// makes an upstream segment stop participating in the target's derivation
// altogether, every dependency of that segment that belonged to the scope
// (the segment's own upstream edges included) is listed as removed. Swapping
// an ancestor's source while leaving the target's direct upstreams untouched
// likewise shows the dependencies that actually changed. Independent datasets
// with no part in the derivation, the target's downstreams, and relationships
// that never participate in deriving the target are outside the scope and
// cannot affect either list.
//
// Each result item keeps just the two endpoint names, From the upstream and To
// the derived dataset, matching the exports' "from"/"to" direction. Added
// means a direct dependency present only in the after document; Removed means
// one present only in the before document; relationships in both are omitted.
// Each relationship appears at most once per list. Both lists are ordered by
// From and then To in Go string order; when nothing changed the comparison
// still succeeds with two non-nil empty lists — "no change" is never an error.
//
// The result is independent of document arrangement: nodes and edges may
// appear in any order and nodes or edges may be listed repeatedly without
// producing a difference, since each document passes the full ImportLineage
// validation (de-duplication included) before it is compared. Names match by
// their exact decoded JSON value — case-sensitive, with spaces and every other
// character preserved verbatim.
//
// The comparison fails as a whole, returning a zero UpstreamLineageDiff and
// never partial lists, when:
//
//   - target is empty ("target dataset name is required");
//   - either document fails ImportLineage validation (malformed JSON, a
//     missing endpoint, a cycle, and every other document-level rule); the
//     error states which document was rejected — the before-change or the
//     after-change one — and carries that document's own validation message;
//   - target is not registered in one of the imported graphs; the error names
//     both the target and whether it was missing from the before-change or the
//     after-change document.
//
// Both documents are validated before any comparison is reported, with the
// before document checked first and then the after document; target presence
// is checked in the same order afterwards. Neither input text nor any graph is
// mutated: each document builds its own throwaway graph through ImportLineage.
// The existing registration, query, import and export behavior is unchanged.
func CompareUpstreamLineage(beforeText, afterText, target string) (UpstreamLineageDiff, error) {
	if target == "" {
		return UpstreamLineageDiff{}, errInvalid("target dataset name is required")
	}

	// Each document must pass the existing import validation exactly as a
	// standalone import would — cycles, missing endpoints and every other
	// document rule included — before either graph is compared. The before
	// document is validated first, so when both are bad its problem is the one
	// reported. A rejected comparison yields no partial lists.
	beforeGraph, err := ImportLineage(beforeText)
	if err != nil {
		return UpstreamLineageDiff{}, errInvalid("before-change lineage document rejected: " + err.Error())
	}
	afterGraph, err := ImportLineage(afterText)
	if err != nil {
		return UpstreamLineageDiff{}, errInvalid("after-change lineage document rejected: " + err.Error())
	}

	if _, ok := beforeGraph[target]; !ok {
		return UpstreamLineageDiff{}, errInvalid("target dataset " + target + " not found in the before-change lineage document")
	}
	if _, ok := afterGraph[target]; !ok {
		return UpstreamLineageDiff{}, errInvalid("target dataset " + target + " not found in the after-change lineage document")
	}

	beforeEdges := targetUpstreamEdges(beforeGraph, target)
	afterEdges := targetUpstreamEdges(afterGraph, target)

	diff := UpstreamLineageDiff{Added: []Dependency{}, Removed: []Dependency{}}
	for edge := range afterEdges {
		if !beforeEdges[edge] {
			diff.Added = append(diff.Added, Dependency{From: edge.From, To: edge.To})
		}
	}
	for edge := range beforeEdges {
		if !afterEdges[edge] {
			diff.Removed = append(diff.Removed, Dependency{From: edge.From, To: edge.To})
		}
	}
	sortDependencies(diff.Added)
	sortDependencies(diff.Removed)
	return diff, nil
}

// targetUpstreamEdges collects every direct dependency inside target's
// complete upstream derivation: the target's ancestor closure as nodes, and
// each from-upstream-to-derived edge whose two endpoints belong to that
// closure — the same node set and edge rule ExportUpstreamLineage uses.
// ImportLineage already guarantees acyclicity and endpoint consistency, but
// the endpoint membership check keeps the scope exactly the closure regardless
// of how the graph was built. The set de-duplicates, so a repeatedly listed
// edge counts once.
func targetUpstreamEdges(graph map[string]*Lineage, target string) map[lineageExportEdge]bool {
	included := ancestorClosure(graph, target)
	edges := make(map[lineageExportEdge]bool)
	for _, edge := range directDependencies(graph, included) {
		edges[edge] = true
	}
	return edges
}

// sortDependencies orders dependencies by From and then To, both in Go string
// order — the same two-level order the lineage exports use for edges.
func sortDependencies(dependencies []Dependency) {
	sort.Slice(dependencies, func(i, j int) bool {
		return edgeLess(dependencies[i].From, dependencies[i].To,
			dependencies[j].From, dependencies[j].To)
	})
}
