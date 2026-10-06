// Package chainledger reads graph content — the set of dataset names and each
// dataset's direct upstreams — through exactly one path, wherever the graph
// document comes from.
//
// A standalone graph file (UnmarshalGraphFile) and the graph embedded in a
// snapshot (ParseSnapshot) describe the same thing, so both are decoded and
// validated by the single readGraphContent below, in one fixed order:
//
//   - the repeated-known-field scan of duplicate_fields.go, scoped to the
//     graph object by checkGraphObject (datasets once in the graph object,
//     name and upstreams once in each dataset record);
//   - the raw name-encoding scan of name_encoding.go, scoped to the graph
//     document by graph_name_encoding.go (every datasets[i].name and every
//     datasets[i].upstreams[j] literal must decode faithfully, so the
//     decoder's U+FFFD rewrite is never kept);
//   - the JSON decode into GraphFile;
//   - the structure rules of graph_rules.go (no empty names, no duplicate
//     datasets, every upstream registered, no cycles), yielding the
//     canonical normalized adjacency both readers build on.
//
// Only the wording that genuinely belongs to the enclosing document differs
// between the two readers — where the graph object sits, reported in
// duplicate-field errors, and how a malformed graph document is introduced —
// and that wording is all a graphContentScope carries. Everything the
// snapshot adds around the graph (its own required fields, the format
// version, and the content identifier, which is checked only after the graph
// has been read legally) stays in snapshot.go; the plan document has its own
// reader and never passes through here.
package chainledger

import (
	"encoding/json"
	"fmt"
)

// graphContentScope carries the per-document wording the shared graph-content
// reader needs: objectLocation describes the graph object itself in
// duplicate-field errors, and invalidJSON introduces a JSON syntax or type
// error in the document being read.
type graphContentScope struct {
	objectLocation string
	invalidJSON    string
}

// The two documents that carry graph content. Their reading rules are the
// same; only these locations and error prefixes differ.
var (
	// standaloneGraphContent is the scope of a whole graph file read by
	// UnmarshalGraphFile.
	standaloneGraphContent = graphContentScope{
		objectLocation: "at the top level of the graph",
		invalidJSON:    "invalid graph JSON",
	}
	// snapshotGraphContent is the scope of the graph embedded as a snapshot's
	// "graph" value, read by ParseSnapshot.
	snapshotGraphContent = graphContentScope{
		objectLocation: `in "graph"`,
		invalidJSON:    "invalid graph in snapshot",
	}
)

// checkGraphObject scans one graph object wherever it appears (the top level
// of a standalone graph file, or the "graph" value inside a snapshot):
// datasets may be declared only once, and each record of the datasets array
// is checked for repeated name/upstreams declarations. location names the
// graph object itself in the duplicate error.
func checkGraphObject(dec *json.Decoder, location string) error {
	return checkObjectFields(dec, location, []knownField{
		{name: "datasets", nested: func(dec *json.Decoder) error {
			return checkArrayElements(dec, func(dec *json.Decoder, index int) error {
				return checkObjectFields(dec, fmt.Sprintf("in the dataset record at index %d of \"datasets\"", index), datasetRecordFields)
			})
		}},
	})
}

// readGraphContent is the single read path for graph content: it runs the
// repeated-known-field scan over the graph object, then the raw name-encoding
// scan, then decodes the document and applies the shared structure rules,
// returning the graph's canonical adjacency (sorted, duplicate-free parent
// lists). Every check is the shared one named in this file's doc comment, so
// a standalone graph file and a snapshot's embedded graph can never be judged
// by different rules; scope supplies only the enclosing document's wording.
func readGraphContent(data []byte, scope graphContentScope) (adjacency, error) {
	if err := runFieldScan(data, func(dec *json.Decoder) error {
		return checkGraphObject(dec, scope.objectLocation)
	}); err != nil {
		return nil, err
	}
	// Every name the graph carries must survive decoding exactly as written:
	// a raw literal with invalid UTF-8 bytes or an unpaired surrogate escape
	// would be silently rewritten to U+FFFD by json.Unmarshal, and a corrupted
	// upstream could then resolve to a genuinely different dataset (an edge
	// written as "源" plus a lone \uD800 would point at the real dataset
	// "源�"). See name_encoding.go.
	if err := checkGraphNameEncoding(data); err != nil {
		return nil, err
	}
	var gf GraphFile
	if err := json.Unmarshal(data, &gf); err != nil {
		return nil, fmt.Errorf("%s: %w", scope.invalidJSON, err)
	}
	return validateGraphStructureFromFile(gf.Datasets)
}
