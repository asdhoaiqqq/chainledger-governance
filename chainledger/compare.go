package chainledger

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

	// The comparison scope in each document is the target's ancestor closure
	// and the direct dependencies inside it — the same node set and the same
	// edge rule the lineage exports use. Both edge lists come back deduplicated
	// and ordered by From and then To, so the diff lists built from them are
	// already in result order.
	beforeEdges := directDependenciesWithin(beforeGraph, ancestorClosure(beforeGraph, target))
	afterEdges := directDependenciesWithin(afterGraph, ancestorClosure(afterGraph, target))

	beforeSet := make(map[lineageExportEdge]bool, len(beforeEdges))
	for _, edge := range beforeEdges {
		beforeSet[edge] = true
	}
	afterSet := make(map[lineageExportEdge]bool, len(afterEdges))
	for _, edge := range afterEdges {
		afterSet[edge] = true
	}

	diff := UpstreamLineageDiff{Added: []Dependency{}, Removed: []Dependency{}}
	for _, edge := range afterEdges {
		if !beforeSet[edge] {
			diff.Added = append(diff.Added, Dependency{From: edge.From, To: edge.To})
		}
	}
	for _, edge := range beforeEdges {
		if !afterSet[edge] {
			diff.Removed = append(diff.Removed, Dependency{From: edge.From, To: edge.To})
		}
	}
	return diff, nil
}
