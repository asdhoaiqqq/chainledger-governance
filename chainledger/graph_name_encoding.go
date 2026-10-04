// Package chainledger guards every graph reader against the same silent name
// substitution the plan reader already rejects (see plan_name_encoding.go).
//
// encoding/json decodes every JSON string by replacing each invalid UTF-8
// byte — and each \u escape that is an unpaired surrogate — with U+FFFD, so
// the decoded graph no longer names the datasets the file actually wrote. A
// corrupted reference can then collide with a real node: when one dataset's
// direct upstream is written as "源" plus one lone \uD800 escape, the decoder
// turns it into "源�" and the edge is silently accepted as a reference to the
// root dataset genuinely named "源�". The graph must not guess which dataset
// the file meant, so every name a graph carries — each datasets[i].name and
// every datasets[i].upstreams[j] — is checked against its RAW string literal
// before the graph is accepted, whether the graph stands alone in a graph
// file or is embedded as a snapshot's "graph" value. A literal carrying bytes
// that are not valid UTF-8, or a \u escape forming an unpaired surrogate (a
// lone high or low surrogate, or a low surrogate written before its high
// surrogate), rejects the whole input: the decoder's rewrite is never kept,
// the offending node or edge is never skipped, and a snapshot whose declared
// content identifier happens to equal the rewritten graph's digest is still
// refused.
//
// Only the raw literal can tell corruption apart from a genuine name: the
// replacement character itself ("�", or the equivalent JSON escape) is legal
// and stays accepted, as is every correctly paired surrogate escape. Names
// that merely LOOK like escapes (a backslash followed by an ordinary letter)
// are plain text to JSON and are never rejected for that reason.
//
// As in the plan scan, field recognition matches the reader's own — keys are
// compared after JSON unescaping with Unicode case folding — so a name field
// spelled "Name" is checked exactly like "name". Unknown fields and
// everything nested inside them are skipped; their strings never name a
// dataset and are not checked. A document too malformed to walk is left to
// the regular parse, exactly as in duplicate_fields.go.
package chainledger

import (
	"encoding/json"
	"strings"
)

// checkGraphNameEncoding scans the raw JSON of one graph object — either the
// whole of a standalone graph file or the "graph" value embedded in a
// snapshot — and rejects the entire document when any dataset name or direct
// upstream reference is written with a string literal the decoder cannot read
// faithfully (invalid UTF-8 bytes or an unpaired surrogate escape). The
// error names the field and the zero-based dataset record position, and for
// an upstream the zero-based array position too, and says which of the two
// corruptions occurred.
func checkGraphNameEncoding(data []byte) error {
	return runFieldScan(data, func(dec *json.Decoder) error {
		tok, err := scanToken(dec)
		if err != nil {
			return err
		}
		if delim, ok := tok.(json.Delim); !ok || delim != '{' {
			return skipRest(dec, tok) // not an object: the regular parse reports it
		}
		for dec.More() {
			key, err := scanKey(dec)
			if err != nil {
				return err
			}
			if strings.EqualFold(key, "datasets") {
				err := checkArrayElements(dec, func(dec *json.Decoder, index int) error {
					return checkNameUpstreamsRecordEncoding(dec, index, "datasets", "dataset record")
				})
				if err != nil {
					return err
				}
			} else {
				// Unknown field: ignored with everything nested inside it.
				if err := skipValue(dec); err != nil {
					return err
				}
			}
		}
		_, err = scanToken(dec) // closing '}'
		return err
	})
}
