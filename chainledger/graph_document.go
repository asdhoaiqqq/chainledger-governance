// Package chainledger reads a graph object the same way no matter which
// document carries it.
//
// A standalone graph file and the "graph" value embedded in a snapshot
// describe exactly the same content: the same set of datasets and each
// dataset's direct upstreams. This file is the SINGLE read pipeline for that
// content, so the two documents can never disagree about how the graph's bytes
// are decoded or checked:
//
//  1. the raw name-encoding gate (checkGraphNameEncoding) runs on the graph's
//     raw bytes, so a string literal with invalid UTF-8 bytes or an unpaired
//     surrogate escape rejects the whole input before json.Unmarshal could
//     rewrite it to U+FFFD — the decoder's rewrite is never kept even when it
//     would collide with a real dataset or match the snapshot's declared
//     content identifier;
//
//  2. the bytes are decoded exactly once into a GraphFile — a syntactically
//     invalid graph is reported with the reading document's own wording
//     (invalidJSONLabel, e.g. "invalid graph JSON" for a standalone file or
//     "invalid graph in snapshot" for an embedded graph);
//
//  3. validateGraphStructureFromFile runs every graph structure rule (empty
//     names, duplicate datasets, missing upstreams, cycles) and returns the
//     canonical parent adjacency: records sorted and deduplicated, roots with
//     no upstreams, and an empty graph legal.
//
// The pipeline takes the graph's own bytes: the whole document for a
// standalone graph file, the already-extracted "graph" value for a snapshot.
//
// What deliberately stays OUTSIDE this pipeline, because it belongs to the
// surrounding document rather than to the graph content:
//
//   - the repeated-known-field scan (duplicate_fields.go). Its recognition
//     rule is already shared (checkGraphObject), but each reader runs it with
//     its own locations — "at the top level of the graph" for a standalone
//     file, in "graph" inside a snapshot — and the snapshot folds the graph
//     scan into the one pass that also checks formatVersion, contentId, and
//     graph at the envelope's top level;
//   - every snapshot envelope rule (required fields, null checks, format
//     version, and the content identifier comparison), which stays in
//     snapshot.go and runs before and around this pipeline exactly as before.
//
// Neither reader decodes or validates the graph any other way, so batch
// preview/apply, snapshot saves, snapshot comparison, and source tracing all
// interpret names, upstream relations, and legality by one rule set.
package chainledger

import (
	"encoding/json"
	"fmt"
)

// readGraphDocument decodes and validates one graph object's raw JSON bytes,
// running the graph content rules in their fixed order: the raw name-encoding
// gate, one JSON decode, then every structure rule. It returns the graph's
// canonical parent adjacency.
//
// invalidJSONLabel is the reading document's own wording for a graph that is
// not syntactically valid JSON; every other rejection (a corrupt name literal
// or an illegal structure) carries the same error regardless of which
// document the graph arrived in, because those rules have only one
// implementation. The caller is responsible for document-level checks this
// pipeline does not own: the standalone graph file and the snapshot each run
// their repeated-known-field scan first, and the snapshot additionally checks
// its envelope and — only after this succeeds — its content identifier.
func readGraphDocument(raw []byte, invalidJSONLabel string) (adjacency, error) {
	// Every name the graph carries must survive decoding exactly as written;
	// the gate runs on the raw bytes and before the decode, so a corrupted
	// upstream can never be rewritten into a reference to a different, real
	// dataset and a matching declared content identifier can never mask it.
	if err := checkGraphNameEncoding(raw); err != nil {
		return nil, err
	}
	var gf GraphFile
	if err := json.Unmarshal(raw, &gf); err != nil {
		return nil, fmt.Errorf("%s: %w", invalidJSONLabel, err)
	}
	return validateGraphStructureFromFile(gf.Datasets)
}
