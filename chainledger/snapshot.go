// Package chainledger implements the on-chain data governance core.
package chainledger

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
)

// SnapshotFormatVersion is the only snapshot format version this build writes
// and accepts. Snapshots carrying any other version are rejected.
const SnapshotFormatVersion = 1

// Snapshot is the on-disk snapshot file: a fixed format version, a content
// identifier derived from the graph semantics, and the full graph.
type Snapshot struct {
	FormatVersion int       `json:"formatVersion"`
	ContentID     string    `json:"contentId"`
	Graph         GraphFile `json:"graph"`
}

// RootSourceChange records a dataset whose root sources differ between the
// old and the new snapshot, together with the old and new root sets.
type RootSourceChange struct {
	Dataset  string   `json:"dataset"`
	OldRoots []string `json:"oldRoots"`
	NewRoots []string `json:"newRoots"`
}

// CompareReport is the deterministic result of comparing two snapshots in the
// fixed old-to-new direction.
//
// All slices are non-nil (empty lists encode as [] in JSON). Datasets and name
// lists are sorted by name byte order; relation lists are sorted by upstream
// then downstream; root changes are sorted by dataset name.
type CompareReport struct {
	AddedDatasets     []string           `json:"addedDatasets"`
	DeletedDatasets   []string           `json:"deletedDatasets"`
	ChangedDatasets   []string           `json:"changedDatasets"`
	AddedRelations    []Relation         `json:"addedRelations"`
	RemovedRelations  []Relation         `json:"removedRelations"`
	RootSourceChanges []RootSourceChange `json:"rootSourceChanges"`
}

// BuildSnapshot captures the current graph as a snapshot. The graph must be
// valid; an invalid graph returns an error. The snapshot's graph and content
// identifier are canonical: datasets sorted by name, upstreams sorted and
// deduplicated, so semantically equivalent graphs produce identical snapshots.
// The graph itself is not modified.
func BuildSnapshot(graph map[string]*Lineage) (Snapshot, error) {
	adj, err := graphAdjacency(graph)
	if err != nil {
		return Snapshot{}, err
	}
	return Snapshot{
		FormatVersion: SnapshotFormatVersion,
		ContentID:     ContentID(adj),
		Graph:         GraphFile{Datasets: adjacencyToDatasets(adj)},
	}, nil
}

// ContentID computes the content identifier of a graph: a sha256 over the
// canonical JSON encoding of the graph's dataset names and direct upstream
// relationships. Equivalent graphs — different record order, upstream order,
// duplicate upstreams, or JSON whitespace — produce the same identifier. The
// identifier depends only on graph semantics, never on save time or file path.
func ContentID(adj adjacency) string {
	canonical := GraphFile{Datasets: adjacencyToDatasets(adj)}
	data, err := json.Marshal(canonical)
	if err != nil {
		// adjacencyToDatasets only emits strings; marshalling cannot fail.
		panic(err)
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

// MarshalSnapshot serializes the snapshot to deterministic JSON. The content
// identifier is re-derived from the graph and must match, so a snapshot with a
// mismatched identifier can never be written.
func MarshalSnapshot(snap Snapshot) ([]byte, error) {
	if snap.FormatVersion != SnapshotFormatVersion {
		return nil, fmt.Errorf("%w: unsupported snapshot format version %d (want %d)", ErrInvalidArgument, snap.FormatVersion, SnapshotFormatVersion)
	}
	adj, err := graphFileAdjacency(snap.Graph)
	if err != nil {
		return nil, err
	}
	if err := validateAcyclic(adj); err != nil {
		return nil, err
	}
	if want := ContentID(adj); snap.ContentID != want {
		return nil, fmt.Errorf("%w: snapshot contentId %q does not match graph content %q", ErrInvalidArgument, snap.ContentID, want)
	}
	return json.MarshalIndent(snap, "", "  ")
}

// UnmarshalSnapshot parses and validates a snapshot file. It rejects invalid
// JSON, missing required fields, unsupported format versions, graphs with empty
// names, duplicate nodes, missing upstreams, or cycles, and a content
// identifier that does not match the graph. The returned snapshot's graph is
// canonical regardless of the input's record or upstream order.
func UnmarshalSnapshot(data []byte) (Snapshot, error) {
	var raw struct {
		FormatVersion *int             `json:"formatVersion"`
		ContentID     *string          `json:"contentId"`
		Graph         *json.RawMessage `json:"graph"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return Snapshot{}, fmt.Errorf("invalid snapshot JSON: %w", err)
	}
	if raw.FormatVersion == nil {
		return Snapshot{}, fmt.Errorf("%w: snapshot is missing required field %q", ErrInvalidArgument, "formatVersion")
	}
	if *raw.FormatVersion != SnapshotFormatVersion {
		return Snapshot{}, fmt.Errorf("%w: unsupported snapshot format version %d (want %d)", ErrInvalidArgument, *raw.FormatVersion, SnapshotFormatVersion)
	}
	if raw.ContentID == nil {
		return Snapshot{}, fmt.Errorf("%w: snapshot is missing required field %q", ErrInvalidArgument, "contentId")
	}
	if raw.Graph == nil {
		return Snapshot{}, fmt.Errorf("%w: snapshot is missing required field %q", ErrInvalidArgument, "graph")
	}
	var gf GraphFile
	if err := json.Unmarshal(*raw.Graph, &gf); err != nil {
		return Snapshot{}, fmt.Errorf("invalid snapshot graph JSON: %w", err)
	}
	adj, err := graphFileAdjacency(gf)
	if err != nil {
		return Snapshot{}, err
	}
	if err := validateAcyclic(adj); err != nil {
		return Snapshot{}, err
	}
	want := ContentID(adj)
	if *raw.ContentID != want {
		return Snapshot{}, fmt.Errorf("%w: snapshot contentId %q does not match graph content %q", ErrInvalidArgument, *raw.ContentID, want)
	}
	return Snapshot{
		FormatVersion: *raw.FormatVersion,
		ContentID:     *raw.ContentID,
		Graph:         GraphFile{Datasets: adjacencyToDatasets(adj)},
	}, nil
}

// SnapshotHasContent reports whether data is a valid snapshot whose content
// identifier matches wantID. Corrupt files, mismatched identifiers, and invalid
// graphs all report false.
func SnapshotHasContent(data []byte, wantID string) bool {
	snap, err := UnmarshalSnapshot(data)
	if err != nil {
		return false
	}
	return snap.ContentID == wantID
}

// CompareSnapshots compares the old snapshot to the new snapshot in the fixed
// old-to-new direction. Both snapshots must have been validated by
// UnmarshalSnapshot.
//
// The report lists added, deleted, and directly changed datasets, added and
// removed direct relations (including relations that disappear with a deleted
// node), and — for datasets present in both versions — changes to the set of
// root sources reachable via upstream edges. Added and deleted datasets are not
// included in the root-source change list.
func CompareSnapshots(oldSnap, newSnap Snapshot) (CompareReport, error) {
	oldAdj, err := graphFileAdjacency(oldSnap.Graph)
	if err != nil {
		return CompareReport{}, err
	}
	newAdj, err := graphFileAdjacency(newSnap.Graph)
	if err != nil {
		return CompareReport{}, err
	}

	oldNames := sortedNames(oldAdj)
	newNames := sortedNames(newAdj)
	oldSet := toSet(oldNames)
	newSet := toSet(newNames)

	var added, deleted, changed []string
	for _, name := range newNames {
		if !oldSet[name] {
			added = append(added, name)
		}
	}
	for _, name := range oldNames {
		if !newSet[name] {
			deleted = append(deleted, name)
		}
	}
	for _, name := range newNames {
		if oldSet[name] && !stringSliceEqual(oldAdj[name], newAdj[name]) {
			changed = append(changed, name)
		}
	}

	addedRels, removedRels := diffRelations(oldAdj, newAdj)

	oldRoots := rootSourceSets(oldAdj)
	newRoots := rootSourceSets(newAdj)
	var rootChanges []RootSourceChange
	for _, name := range newNames {
		if !oldSet[name] {
			continue
		}
		if !stringSliceEqual(oldRoots[name], newRoots[name]) {
			rootChanges = append(rootChanges, RootSourceChange{
				Dataset:  name,
				OldRoots: oldRoots[name],
				NewRoots: newRoots[name],
			})
		}
	}

	report := CompareReport{
		AddedDatasets:     added,
		DeletedDatasets:   deleted,
		ChangedDatasets:   changed,
		AddedRelations:    addedRels,
		RemovedRelations:  removedRels,
		RootSourceChanges: rootChanges,
	}
	// Guarantee non-nil slices so JSON encodes empty lists as [].
	if report.AddedDatasets == nil {
		report.AddedDatasets = []string{}
	}
	if report.DeletedDatasets == nil {
		report.DeletedDatasets = []string{}
	}
	if report.ChangedDatasets == nil {
		report.ChangedDatasets = []string{}
	}
	if report.AddedRelations == nil {
		report.AddedRelations = []Relation{}
	}
	if report.RemovedRelations == nil {
		report.RemovedRelations = []Relation{}
	}
	if report.RootSourceChanges == nil {
		report.RootSourceChanges = []RootSourceChange{}
	}
	for i := range report.RootSourceChanges {
		if report.RootSourceChanges[i].OldRoots == nil {
			report.RootSourceChanges[i].OldRoots = []string{}
		}
		if report.RootSourceChanges[i].NewRoots == nil {
			report.RootSourceChanges[i].NewRoots = []string{}
		}
	}
	return report, nil
}

// rootSourceSets computes, for every dataset, the sorted set of root sources
// reachable by following upstream edges. A root's set contains itself; a
// non-root's set is the union of its parents' sets. Multiple paths to the same
// root count once. The graph is assumed acyclic (validated on load).
func rootSourceSets(adj adjacency) map[string][]string {
	memo := make(map[string][]string, len(adj))
	var walk func(name string) []string
	walk = func(name string) []string {
		if roots, ok := memo[name]; ok {
			return roots
		}
		parents := adj[name]
		if len(parents) == 0 {
			memo[name] = []string{name}
			return memo[name]
		}
		set := make(map[string]bool)
		for _, parent := range parents {
			for _, root := range walk(parent) {
				set[root] = true
			}
		}
		out := make([]string, 0, len(set))
		for root := range set {
			out = append(out, root)
		}
		sort.Strings(out)
		memo[name] = out
		return out
	}
	for name := range adj {
		walk(name)
	}
	return memo
}

// sortedNames returns the dataset names sorted by byte order.
func sortedNames(adj adjacency) []string {
	names := make([]string, 0, len(adj))
	for name := range adj {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}
