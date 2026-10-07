// This file adds snapshot-based source tracing: given one validated snapshot
// and a dataset name, it reports every root source reachable from that
// dataset through direct upstream edges, together with one shortest path per
// root. The answer depends only on the snapshot's graph semantics, so a later
// change to the live graph never alters what a frozen version reports.
package chainledger

import (
	"fmt"
)

// SourceTrace links one reachable root source to the path that reaches it.
//
// Path starts at the queried dataset and ends at Root, listing every dataset
// name along the way, both endpoints included. When the queried dataset is
// itself a root, Path is the single-element array holding its own name.
type SourceTrace struct {
	Root string   `json:"root"`
	Path []string `json:"path"`
}

// TraceReport is the deterministic, read-only answer of a source trace
// against one snapshot.
//
// ContentID is the snapshot's own content identifier, Dataset is the queried
// name exactly as given, and Sources lists every reachable root once, sorted
// by root name byte order.
type TraceReport struct {
	ContentID string        `json:"contentId"`
	Dataset   string        `json:"dataset"`
	Sources   []SourceTrace `json:"sources"`
}

// TraceSources traces the root sources of dataset within the graph frozen in
// snap. The snapshot is assumed to have passed ParseSnapshot; it is only read,
// never modified.
//
// The root lookup itself is the one shared rule traceRootSources, the same
// finder the snapshot comparison uses to judge whether a dataset's sources
// changed, so the two reports can never explain a root differently.
//
// Every reachable root is reported exactly once with one shortest path (fewest
// direct relations). Among equally short paths the one chosen is the smallest
// when the names are compared element by element from the start of the path in
// UTF-8 byte order. Roots sharing intermediate datasets keep their own paths.
//
// An empty dataset name or a name absent from the snapshot is an error.
func TraceSources(snap *SnapshotFile, dataset string) (*TraceReport, error) {
	if dataset == "" {
		return nil, fmt.Errorf("%w: dataset name to trace must not be empty", ErrInvalidArgument)
	}
	adj := adjacencyFromValidFile(snap.Graph)
	if _, ok := adj[dataset]; !ok {
		return nil, fmt.Errorf("%w: dataset %q is not in the snapshot", ErrNotFound, dataset)
	}

	return &TraceReport{
		ContentID: snap.ContentID,
		Dataset:   dataset,
		Sources:   traceRootSources(dataset, adj),
	}, nil
}
