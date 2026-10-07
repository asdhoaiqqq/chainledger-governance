// Package chainledger implements the on-chain data governance core.
//
// This file adds local lineage snapshots: a snapshot freezes one graph as a
// versioned, content-addressed JSON document, and two snapshots can be compared
// read-only. Every byte a snapshot writes is determined solely by the graph's
// semantics — dataset names and their direct upstreams — never by record
// order, upstream order, duplicate upstreams, JSON whitespace, save time, or
// file path.
package chainledger

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
)

// SnapshotFormatVersion is the only on-disk snapshot format understood.
const SnapshotFormatVersion = 1

// SnapshotFile is the on-disk representation of one frozen lineage graph.
//
// FormatVersion is fixed at 1. ContentID addresses the graph semantics and is
// verified against Graph on every read; Graph carries the complete graph.
type SnapshotFile struct {
	FormatVersion int       `json:"formatVersion"`
	ContentID     string    `json:"contentId"`
	Graph         GraphFile `json:"graph"`
}

// RootSourceChange reports that one dataset present in BOTH versions reaches a
// different set of root sources. OldRoots and NewRoots are sorted by name and
// never nil, so empty root sets encode as [] rather than null.
type RootSourceChange struct {
	Dataset  string   `json:"dataset"`
	OldRoots []string `json:"oldRoots"`
	NewRoots []string `json:"newRoots"`
}

// CompareReport is the deterministic, read-only diff of two snapshots read in
// the fixed direction old -> new.
//
// NewDatasets and RemovedDatasets list nodes present on only one side.
// ChangedDatasets lists datasets present on both sides whose direct upstream
// set actually changed. AddedRelations and RemovedRelations list every
// directed edge present on only one side, including edges that vanish because
// one endpoint node was removed. RootSourceChanges covers only datasets common
// to both versions whose reachable root set differs; added or removed datasets
// never appear there.
type CompareReport struct {
	NewDatasets       []string           `json:"newDatasets"`
	RemovedDatasets   []string           `json:"removedDatasets"`
	ChangedDatasets   []string           `json:"changedDatasets"`
	AddedRelations    []Relation         `json:"addedRelations"`
	RemovedRelations  []Relation         `json:"removedRelations"`
	RootSourceChanges []RootSourceChange `json:"rootSourceChanges"`
}

// contentIDPrefix names the hash algorithm used for content addressing.
const contentIDPrefix = "sha256:"

// canonicalGraph serializes the graph's semantics deterministically: datasets
// sorted by name byte order with sorted, duplicate-free upstream lists, compact
// JSON with no insignificant whitespace. Two semantically equal graphs always
// produce identical bytes.
func canonicalGraph(adj adjacency) ([]byte, error) {
	gf := GraphFile{Datasets: adjacencyToDatasets(adj)}
	return json.Marshal(gf)
}

// computeContentID returns the content identifier of the graph bytes:
// "sha256:" followed by the lowercase hex SHA-256 digest.
func computeContentID(canonical []byte) string {
	sum := sha256.Sum256(canonical)
	return contentIDPrefix + hex.EncodeToString(sum[:])
}

// BuildSnapshot validates graph and freezes it into a SnapshotFile with format
// version 1 and the matching content identifier. It does not mutate graph. The
// returned document and its serialized bytes depend only on graph semantics.
//
// A dataset name or direct upstream reference that is not valid UTF-8 rejects
// the build with no snapshot and no content identifier: such bytes could not be
// serialized without silently rewriting the name, so the frozen document would
// not be a faithful, readable copy of the graph.
func BuildSnapshot(graph map[string]*Lineage) (*SnapshotFile, error) {
	// One read of the existing graph settles its legality and yields the
	// canonical relations the snapshot freezes — the same shared judgment
	// ValidateGraph, preview, apply, and graph export make.
	adj, err := validatedGraphAdjacency(graph)
	if err != nil {
		return nil, err
	}
	canonical, err := canonicalGraph(adj)
	if err != nil {
		return nil, err
	}
	return &SnapshotFile{
		FormatVersion: SnapshotFormatVersion,
		ContentID:     computeContentID(canonical),
		Graph:         GraphFile{Datasets: adjacencyToDatasets(adj)},
	}, nil
}

// MarshalSnapshot serializes a snapshot the same way for every save: indented
// JSON keyed by struct field order, terminated by a single newline.
func MarshalSnapshot(snap *SnapshotFile) ([]byte, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetIndent("", "  ")
	enc.SetEscapeHTML(false)
	if err := enc.Encode(snap); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// rawSnapshot mirrors SnapshotFile but keeps each field raw so ParseSnapshot
// can distinguish "field absent" from "field present with a zero value"
// (e.g. a missing formatVersion must be rejected rather than read as 0).
type rawSnapshot struct {
	FormatVersion json.RawMessage `json:"formatVersion"`
	ContentID     json.RawMessage `json:"contentId"`
	Graph         json.RawMessage `json:"graph"`
}

// isJSONNull reports whether a raw JSON value is the literal null.
func isJSONNull(raw json.RawMessage) bool {
	return string(bytes.TrimSpace(raw)) == "null"
}

// ParseSnapshot parses and strictly validates one snapshot document: required
// fields must be present and well-typed, no known field may be declared twice
// within the same object (even with identical values, a null first, or a
// spelling equivalent under Unicode case folding or JSON escaping), the
// format version must be supported, the embedded graph must be structurally
// valid (no empty names, duplicate datasets, missing upstreams, or cycles),
// and the content identifier must correspond exactly to the graph. A
// tampered, corrupt, or otherwise invalid snapshot is rejected, so a bad file
// can never be trusted as a version.
func ParseSnapshot(data []byte) (*SnapshotFile, error) {
	if err := checkDuplicateFields(data); err != nil {
		return nil, err
	}
	var raw rawSnapshot
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil, fmt.Errorf("invalid snapshot JSON: %w", err)
	}
	if len(raw.FormatVersion) == 0 {
		return nil, fmt.Errorf("%w: snapshot is missing required field %q", ErrInvalidArgument, "formatVersion")
	}
	if len(raw.ContentID) == 0 {
		return nil, fmt.Errorf("%w: snapshot is missing required field %q", ErrInvalidArgument, "contentId")
	}
	if len(raw.Graph) == 0 {
		return nil, fmt.Errorf("%w: snapshot is missing required field %q", ErrInvalidArgument, "graph")
	}
	// An explicit JSON null is present-but-untyped and must not be silently
	// treated as a zero value (in particular, graph:null must not read as an
	// empty graph).
	if isJSONNull(raw.FormatVersion) {
		return nil, fmt.Errorf("%w: snapshot field %q must be a number, not null", ErrInvalidArgument, "formatVersion")
	}
	if isJSONNull(raw.ContentID) {
		return nil, fmt.Errorf("%w: snapshot field %q must be a string, not null", ErrInvalidArgument, "contentId")
	}
	if isJSONNull(raw.Graph) {
		return nil, fmt.Errorf("%w: snapshot field %q must be an object, not null", ErrInvalidArgument, "graph")
	}

	var version int
	if err := json.Unmarshal(raw.FormatVersion, &version); err != nil {
		return nil, fmt.Errorf("%w: snapshot field %q must be a number: %v", ErrInvalidArgument, "formatVersion", err)
	}
	if version != SnapshotFormatVersion {
		return nil, fmt.Errorf("%w: unsupported snapshot format version %d, supported version is %d", ErrInvalidArgument, version, SnapshotFormatVersion)
	}

	var contentID string
	if err := json.Unmarshal(raw.ContentID, &contentID); err != nil {
		return nil, fmt.Errorf("%w: snapshot field %q must be a string", ErrInvalidArgument, "contentId")
	}
	if contentID == "" {
		return nil, fmt.Errorf("%w: snapshot field %q must not be empty", ErrInvalidArgument, "contentId")
	}

	// The embedded graph is decoded and checked by the one pipeline a
	// standalone graph file uses (see graph_document.go): the raw
	// name-encoding gate runs first on the graph's own bytes and rejects the
	// whole snapshot even when the declared content identifier happens to
	// match the rewritten graph's digest, then one JSON decode and the shared
	// structure rules (empty names, duplicate datasets, missing upstreams,
	// cycles) produce the canonical adjacency. The only snapshot-specific part
	// is the wording of a graph that is not valid JSON.
	adj, err := readGraphDocument(raw.Graph, "invalid graph in snapshot")
	if err != nil {
		return nil, err
	}

	canonical, err := canonicalGraph(adj)
	if err != nil {
		return nil, err
	}
	if want := computeContentID(canonical); contentID != want {
		return nil, fmt.Errorf("%w: content identifier %q does not match the graph (expected %q); the snapshot was altered or corrupted", ErrInvalidArgument, contentID, want)
	}

	return &SnapshotFile{
		FormatVersion: version,
		ContentID:     contentID,
		Graph:         GraphFile{Datasets: adjacencyToDatasets(adj)},
	}, nil
}

// checkDuplicateFields applies the shared repeated-known-field rule (see
// duplicate_fields.go) to a snapshot, supplying the snapshot's own field
// scope and locations: formatVersion, contentId, and graph at the top level,
// datasets inside graph, and name and upstreams inside each dataset record.
// The embedded graph is scanned by the same checkGraphObject a standalone
// graph file uses, so the two readers can never judge one graph differently.
func checkDuplicateFields(data []byte) error {
	return runFieldScan(data, func(dec *json.Decoder) error {
		return checkObjectFields(dec, "at the top level of the snapshot", []knownField{
			{name: "formatVersion"},
			{name: "contentId"},
			{name: "graph", nested: func(dec *json.Decoder) error {
				return checkGraphObject(dec, `in "graph"`)
			}},
		})
	})
}

// CompareSnapshots returns the fixed-direction diff from old to new. Both
// inputs are assumed to have passed ParseSnapshot; the report is fully
// deterministic and identical for semantically identical comparisons.
func CompareSnapshots(oldSnap, newSnap *SnapshotFile) *CompareReport {
	oldAdj := adjacencyFromValidFile(oldSnap.Graph)
	newAdj := adjacencyFromValidFile(newSnap.Graph)

	// Dataset-level changes follow the shared old -> new rule the batch report
	// uses (see diffDatasets), so comparing two snapshots classifies new,
	// removed, and directly changed datasets exactly the way previewing or
	// applying the batch that turns one version into the other does.
	newNodes, removedNodes, changedNodes := diffDatasets(oldAdj, newAdj)

	added, removed := diffRelations(oldAdj, newAdj)

	common := make([]string, 0)
	for name := range oldAdj {
		if _, ok := newAdj[name]; ok {
			common = append(common, name)
		}
	}
	sort.Strings(common)

	// A common dataset's root set changes exactly when the shared root-source
	// lookup (see rootSourceTraces) reports different roots in the two frozen
	// versions, so oldRoots/newRoots are by construction the same root names a
	// TraceSources run reports in the corresponding snapshot. A reroute or a
	// shortcut that leaves the root set untouched reports nothing; losing the
	// last reachable route to a root is reported here even when the dataset's
	// own direct upstreams did not change.
	rootChanges := make([]RootSourceChange, 0)
	for _, name := range common {
		oldRoots := rootSourceNames(name, oldAdj)
		newRoots := rootSourceNames(name, newAdj)
		if !stringSliceEqual(oldRoots, newRoots) {
			rootChanges = append(rootChanges, RootSourceChange{
				Dataset:  name,
				OldRoots: oldRoots,
				NewRoots: newRoots,
			})
		}
	}

	return &CompareReport{
		NewDatasets:       orEmptyStrings(newNodes),
		RemovedDatasets:   orEmptyStrings(removedNodes),
		ChangedDatasets:   orEmptyStrings(changedNodes),
		AddedRelations:    orEmptyRelations(added),
		RemovedRelations:  orEmptyRelations(removed),
		RootSourceChanges: rootChanges,
	}
}

// adjacencyFromValidFile builds normalized parent adjacency from an already
// validated graph file.
func adjacencyFromValidFile(gf GraphFile) adjacency {
	adj := make(adjacency, len(gf.Datasets))
	for _, ds := range gf.Datasets {
		adj[ds.Name] = uniqueSorted(ds.Upstreams)
	}
	return adj
}

func orEmptyStrings(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}

func orEmptyRelations(r []Relation) []Relation {
	if r == nil {
		return []Relation{}
	}
	return r
}
