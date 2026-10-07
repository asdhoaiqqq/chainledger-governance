package chainledger

import "sort"

// LineageDiffEdge is one direct dependency that differs between two lineage
// snapshots, written in the same direction the exports use: From is the
// upstream (source) dataset and To is the dataset derived from it.
type LineageDiffEdge struct {
	From string
	To   string
}

// UpstreamLineageDiff is the result of comparing one target's complete
// upstream lineage between a before and an after document: Added holds the
// direct dependencies that exist only in the after snapshot, Removed the ones
// that exist only in the before snapshot. Dependencies present in both are
// not listed. Each group is ordered by From and then To in Go string order,
// and both groups are non-nil even when empty.
type UpstreamLineageDiff struct {
	Added   []LineageDiffEdge
	Removed []LineageDiffEdge
}

// DiffUpstreamLineage compares the complete upstream lineage of target
// between two lineage documents and reports which direct dependencies were
// added and which were removed. beforeText and afterText are each a lineage
// JSON document in exactly the shape ImportLineage accepts (the shape both
// exports produce); each document is the whole lineage as it stood at that
// time, and the comparison is scoped to the part of it target actually
// derives from.
//
// The compared scope is target's full upstream closure in each document —
// target itself plus every dataset reachable from it by following parent
// edges — and every direct dependency among those datasets. This preserves
// all real branches, never just target's direct upstreams or one shortest
// explanation path: when target depends on a source both directly and through
// intermediate datasets, removing one edge of the longer route shows up, and
// when a removal makes a whole upstream segment stop participating in
// target's derivation, every dependency of that segment is listed as removed.
// An ancestor switching its source likewise appears even when target's direct
// upstreams are unchanged. Independent datasets, target's downstreams, and
// relationships that play no part in target's derivation never affect the
// result.
//
// Every direct dependency present in both scopes is left out. Each remaining
// dependency appears exactly once, keeping its From (source) and To (derived)
// names; Added holds the dependencies only the after document has, Removed
// the ones only the before document has, and each group is ordered by From
// then To in Go string order. When nothing changed the comparison succeeds
// with both groups empty (non-nil) — "no change" is not a failure.
//
// The documents are compared by the exact decoded name values: matching is
// case-sensitive and spaces and other content are preserved verbatim. Node
// and edge order inside a document and duplicated node or edge listings never
// create differences, because each document is imported through the same
// de-duplicating ImportLineage rules first.
//
// Both documents must pass the existing import validation — malformed JSON,
// missing edge endpoints, dependency cycles and every other document problem
// fail the whole comparison with an error saying which document (before or
// after) was rejected, and no partial diff is returned. An empty target is
// rejected as a missing name; a target not registered in either document
// fails the whole comparison with an error naming it and saying whether the
// before or the after document lacks it. The comparison is read-only: it
// builds its own graphs from the documents and touches no caller state.
func DiffUpstreamLineage(beforeText, afterText, target string) (UpstreamLineageDiff, error) {
	if target == "" {
		return UpstreamLineageDiff{}, errInvalid("dataset name is required")
	}

	before, err := ImportLineage(beforeText)
	if err != nil {
		return UpstreamLineageDiff{}, errInvalid("before document rejected: " + err.Error())
	}
	after, err := ImportLineage(afterText)
	if err != nil {
		return UpstreamLineageDiff{}, errInvalid("after document rejected: " + err.Error())
	}

	if _, ok := before[target]; !ok {
		return UpstreamLineageDiff{}, errInvalid("target dataset not found in the before document: " + target)
	}
	if _, ok := after[target]; !ok {
		return UpstreamLineageDiff{}, errInvalid("target dataset not found in the after document: " + target)
	}

	beforeEdges := closureEdgeSet(before, ancestorClosure(before, target))
	afterEdges := closureEdgeSet(after, ancestorClosure(after, target))

	diff := UpstreamLineageDiff{
		Added:   make([]LineageDiffEdge, 0),
		Removed: make([]LineageDiffEdge, 0),
	}
	for edge := range afterEdges {
		if !beforeEdges[edge] {
			diff.Added = append(diff.Added, LineageDiffEdge{From: edge.From, To: edge.To})
		}
	}
	for edge := range beforeEdges {
		if !afterEdges[edge] {
			diff.Removed = append(diff.Removed, LineageDiffEdge{From: edge.From, To: edge.To})
		}
	}
	order := func(edges []LineageDiffEdge) {
		sort.Slice(edges, func(i, j int) bool {
			if edges[i].From != edges[j].From {
				return edges[i].From < edges[j].From
			}
			return edges[i].To < edges[j].To
		})
	}
	order(diff.Added)
	order(diff.Removed)
	return diff, nil
}

// closureEdgeSet collects every direct dependency whose derived endpoint is
// in the included set and whose upstream endpoint is included as well. For an
// ancestor closure every parent of an included node is included by
// construction; the check keeps the helper correct for any node set.
func closureEdgeSet(graph map[string]*Lineage, included map[string]bool) map[lineageExportEdge]bool {
	edges := make(map[lineageExportEdge]bool)
	for name := range included {
		for _, parent := range graph[name].Parents {
			if included[parent] {
				edges[lineageExportEdge{From: parent, To: name}] = true
			}
		}
	}
	return edges
}
