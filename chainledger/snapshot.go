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
	if err := ValidateGraph(graph); err != nil {
		return nil, err
	}
	adj, err := graphAdjacency(graph)
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

	var gf GraphFile
	if err := json.Unmarshal(raw.Graph, &gf); err != nil {
		return nil, fmt.Errorf("invalid graph in snapshot: %w", err)
	}
	// The embedded graph's names must survive decoding exactly as written,
	// just like a standalone graph file. The decoder would silently rewrite
	// invalid UTF-8 bytes and unpaired surrogate escapes to U+FFFD, so the
	// check runs against the RAW document and must precede the content-id
	// verification: a snapshot whose declared identifier happens to match the
	// REPLACED graph is still corrupt and must be refused. See
	// graph_name_encoding.go.
	if err := checkSnapshotGraphNameEncoding(data); err != nil {
		return nil, err
	}
	// The embedded graph obeys exactly the same structural rules as a
	// standalone graph file, so the two readers can never judge one graph
	// differently.
	adj, err := validateGraphStructureFromFile(gf.Datasets)
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

	added, removed := diffRelations(oldAdj, newAdj)

	var newNodes, removedNodes, changedNodes []string
	for name := range newAdj {
		if oldParents, ok := oldAdj[name]; !ok {
			newNodes = append(newNodes, name)
		} else if !stringSliceEqual(oldParents, newAdj[name]) {
			changedNodes = append(changedNodes, name)
		}
	}
	for name := range oldAdj {
		if _, ok := newAdj[name]; !ok {
			removedNodes = append(removedNodes, name)
		}
	}
	sort.Strings(newNodes)
	sort.Strings(removedNodes)
	sort.Strings(changedNodes)

	common := make([]string, 0)
	for name := range oldAdj {
		if _, ok := newAdj[name]; ok {
			common = append(common, name)
		}
	}
	sort.Strings(common)

	rootChanges := make([]RootSourceChange, 0)
	for _, name := range common {
		oldRoots := rootSources(name, oldAdj)
		newRoots := rootSources(name, newAdj)
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

// rootSources returns the root datasets reachable from node by following
// direct upstream edges. A node without upstreams is itself a root and its own
// source, so it is included. Nodes reached by more than one path count once.
// The result is sorted by name and never nil.
func rootSources(node string, adj adjacency) []string {
	visited := make(map[string]bool)
	var roots []string
	var walk func(string)
	walk = func(current string) {
		if visited[current] {
			return
		}
		visited[current] = true
		parents := adj[current]
		if len(parents) == 0 {
			roots = append(roots, current)
			return
		}
		for _, parent := range parents {
			walk(parent)
		}
	}
	walk(node)
	sort.Strings(roots)
	return orEmptyStrings(roots)
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
