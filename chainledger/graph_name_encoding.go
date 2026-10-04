// Package chainledger applies the shared raw name-encoding rule (see
// name_encoding.go) to a graph object, whether it stands alone as a graph
// file or is embedded as a snapshot's "graph" value.
//
// Every name a graph carries — each datasets[i].name and every
// datasets[i].upstreams[j] — is checked against its RAW string literal
// before the graph is accepted. A literal carrying bytes that are not valid
// UTF-8, or a \u escape forming an unpaired surrogate (a lone high or low
// surrogate, or a low surrogate written before its high surrogate), rejects
// the whole input: the decoder's U+FFFD rewrite is never kept, the offending
// node or edge is never skipped, and a snapshot whose declared content
// identifier happens to equal the rewritten graph's digest is still refused.
//
// Field recognition, record reading, unknown-field skipping, locations, and
// the byte-vs-surrogate error wording are the shared machinery in
// name_encoding.go and duplicate_fields.go, so a standalone graph file and
// an embedded snapshot graph are judged by exactly the same code.
package chainledger

import (
	"encoding/json"
)

// checkGraphNameEncoding scans the raw JSON of one graph object — either the
// whole of a standalone graph file or the "graph" value embedded in a
// snapshot — and rejects the entire document when any dataset name or direct
// upstream reference is written with a string literal the decoder cannot
// read faithfully (invalid UTF-8 bytes or an unpaired surrogate escape).
// The error names the field and the zero-based dataset record position, and
// for an upstream the zero-based array position too, and says which of the
// two corruptions occurred.
func checkGraphNameEncoding(data []byte) error {
	return runFieldScan(data, func(dec *json.Decoder) error {
		return scanObjectFields(dec, []knownField{
			{name: "datasets", nested: func(dec *json.Decoder) error {
				return scanNameUpstreamsRecords(dec, "datasets", "dataset record")
			}},
		})
	})
}
