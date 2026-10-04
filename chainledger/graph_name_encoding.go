// Package chainledger implements the on-chain data governance core.
//
// This file guards the graph readers against the same silent name
// substitution the plan reader already refuses (see plan_name_encoding.go).
// encoding/json decodes every JSON string by replacing each invalid UTF-8
// byte — and each \u escape that is an unpaired surrogate — with U+FFFD, so
// a graph read from disk no longer contains the names the file actually
// wrote. A corrupted reference can then resolve to a dataset it never named:
// with a root really named "源�" in the graph, an upstream written as "源"
// plus one broken \uD800 escape decodes to "源�" and is silently treated as
// an edge to that root, even though the writer never declared it. The same
// rewrite lets a snapshot whose declared content identifier matches the
// REPLACED graph read as valid, even though its on-disk names are corrupt.
//
// Every name the graph semantics depend on — each datasets[i].name and every
// datasets[i].upstreams[j] — is therefore checked against its RAW string
// literal before the graph is accepted, in both places a graph is read: a
// standalone graph file (UnmarshalGraphFile) and the graph embedded in a
// snapshot (ParseSnapshot). A literal carrying bytes that are not valid
// UTF-8, or a \u escape forming an unpaired surrogate (a lone high or low
// surrogate, or a low surrogate written before its high surrogate), rejects
// the whole document, even when no other name exists for the corruption to
// collide with. Neither the offending record nor its edges are skipped: the
// entire input fails, so a rejected graph can never be previewed, applied,
// snapshotted, compared, or traced, and a corrupted snapshot is refused even
// when its content identifier happens to match the replaced graph.
//
// Only the raw literal can tell corruption apart from a genuine name: the
// replacement character itself ("�", or the equivalent JSON escape) is legal
// and stays accepted, as is every correctly paired surrogate escape. Names
// that merely LOOK like escapes (a backslash followed by an ordinary letter)
// are plain text to JSON and are never rejected for that reason.
//
// The scan walks the document with the same field recognition the graph
// readers and the duplicate-field scan use — known fields matched after JSON
// unescaping with Unicode case folding — so a name spelled "Name" or "name"
// is checked exactly like "name". Unknown fields and everything nested
// inside them are skipped; their strings are not graph names and are not
// checked. A document too malformed to walk is left to the regular parse,
// exactly as in duplicate_fields.go.
package chainledger

import (
	"encoding/json"
	"fmt"
	"strings"
)

// graphNameContext says where a checked graph sits, so an error names both
// the document kind and the exact record. It is appended after the record
// location; empty for a standalone graph file, whose reader already names the
// file itself.
const (
	standaloneGraphContext = ""
	snapshotGraphContext   = " in the graph embedded in the snapshot"
)

// checkStandaloneGraphNameEncoding scans one standalone graph file: the
// decoder is positioned at the document, whose top-level object holds the
// "datasets" array.
func checkStandaloneGraphNameEncoding(data []byte) error {
	return runFieldScan(data, func(dec *json.Decoder) error {
		return checkGraphObjectNameEncoding(dec, standaloneGraphContext)
	})
}

// checkSnapshotGraphNameEncoding scans one snapshot document: the graph names
// live inside the top-level "graph" object; the snapshot's own fields
// (formatVersion, contentId) carry no graph names and are skipped like any
// unknown field here.
func checkSnapshotGraphNameEncoding(data []byte) error {
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
			if !strings.EqualFold(key, "graph") {
				if err := skipValue(dec); err != nil {
					return err
				}
				continue
			}
			if err := checkGraphObjectNameEncoding(dec, snapshotGraphContext); err != nil {
				return err
			}
		}
		_, err = scanToken(dec) // closing '}'
		return err
	})
}

// checkGraphObjectNameEncoding scans the graph object the decoder is
// positioned at: its "datasets" array is walked record by record, and each
// record's name and upstreams literals are validated. Every other field is
// skipped. context names where this graph object sits in the document.
func checkGraphObjectNameEncoding(dec *json.Decoder, context string) error {
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
		if !strings.EqualFold(key, "datasets") {
			// Unknown field: ignored with everything nested inside it.
			if err := skipValue(dec); err != nil {
				return err
			}
			continue
		}
		if err := checkArrayElements(dec, func(dec *json.Decoder, index int) error {
			return checkDatasetRecordNameEncoding(dec, index, context)
		}); err != nil {
			return err
		}
	}
	_, err = scanToken(dec) // closing '}'
	return err
}

// checkDatasetRecordNameEncoding scans one dataset record of the "datasets"
// array: the raw literal of its "name" value and of every element of its
// "upstreams" array are checked; every other field is skipped. index is the
// record's zero-based position in the datasets array, used in error
// locations.
func checkDatasetRecordNameEncoding(dec *json.Decoder, index int, context string) error {
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
		switch {
		case strings.EqualFold(key, "name"):
			location := fmt.Sprintf("field %q of the dataset record at index %d of %q%s",
				"name", index, "datasets", context)
			if err := checkNameStringEncoding(dec, location); err != nil {
				return err
			}
		case strings.EqualFold(key, "upstreams"):
			err := checkArrayElements(dec, func(dec *json.Decoder, item int) error {
				location := fmt.Sprintf("field %q at index %d of the dataset record at index %d of %q%s",
					"upstreams", item, index, "datasets", context)
				return checkNameStringEncoding(dec, location)
			})
			if err != nil {
				return err
			}
		default:
			if err := skipValue(dec); err != nil {
				return err
			}
		}
	}
	_, err = scanToken(dec) // closing '}'
	return err
}
