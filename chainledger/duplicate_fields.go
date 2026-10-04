// Package chainledger repeated-known-field scan shared by every JSON reader.
//
// The graph file (UnmarshalGraphFile), the lineage adjustment plan
// (UnmarshalPlan), and the snapshot (ParseSnapshot) all reject a document in
// which a known field is declared more than once within the same object: the
// decoder would silently keep only the last declaration, so the repetition is
// ambiguous and the whole file is refused — even when the duplicates carry
// identical values, the first is null, or the surviving value is a legal
// empty list. This file is the single implementation of that rule; each
// reader supplies only its own field set and the wording that locates the
// offending object (document top level, the graph embedded in a snapshot, or
// one record inside an array, by zero-based index), so the three formats can
// never drift apart about what counts as a repeated field.
//
// Field names are compared after JSON string unescaping and with the same
// Unicode simple case-folding the decoder applies to its case-insensitive
// field lookup (equivalent to strings.EqualFold), so "name" and "Name"
// collide, and so do spellings that only differ in a fold-equivalent rune
// such as "dataſets" (long s) and "datasets". The folding applies to field
// names only: dataset names stay case-sensitive and keep their surrounding
// whitespace. Unknown fields keep their ignore-everything behavior and may
// repeat freely, even when their values contain keys that match known fields.
package chainledger

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

// errAbortScan stops the duplicate-field scan when the document is too
// malformed to keep walking; the regular parse then reports the syntax or
// type error instead.
var errAbortScan = errors.New("chainledger: abort duplicate field scan")

// scanForDuplicateFields runs root over a token decoder for data and reports
// the repeated known field it finds, if any.
//
// The decoder uses UseNumber so numbers are kept verbatim instead of being
// converted to float64: a perfectly legal JSON number outside the float64
// range (such as 1e400), even one buried in an ignored unknown field's value,
// must not make the decoder error out and abort the whole scan — otherwise an
// unrelated large number placed before a repeated known field would hide that
// duplication. Malformed number syntax still fails and is left to the regular
// parse to report.
//
// A document too malformed to scan aborts with errAbortScan, which is
// swallowed here: the regular parse that every reader runs next reports the
// structural problem with its usual error.
func scanForDuplicateFields(data []byte, root func(dec *json.Decoder) error) error {
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()
	if err := root(dec); err != nil && !errors.Is(err, errAbortScan) {
		return err
	}
	return nil
}

// objectSpec describes how one JSON object is scanned for repeated known
// fields: which keys count as known fields (field), how the object itself is
// described in a duplicate error (location), and which known fields hold
// nested structures that must be scanned rather than skipped (descend, keyed
// by canonical field name).
type objectSpec struct {
	field    func(key string) (canonical string, known bool)
	location string
	descend  map[string]func(dec *json.Decoder) error
}

// checkObjectValue scans one JSON value that should be an object, rejecting
// any known field declared twice within it. Values of fields listed in
// spec.descend are scanned by the corresponding function; every other value
// is skipped. A value that is not an object is consumed and left to the
// regular parse, which reports the type problem.
func checkObjectValue(dec *json.Decoder, spec objectSpec) error {
	tok, err := scanToken(dec)
	if err != nil {
		return err
	}
	if delim, ok := tok.(json.Delim); !ok || delim != '{' {
		return skipRest(dec, tok)
	}
	seen := make(map[string]bool, len(spec.descend)+2)
	for dec.More() {
		key, err := scanKey(dec)
		if err != nil {
			return err
		}
		field, known := spec.field(key)
		if known {
			if seen[field] {
				return duplicateFieldError(field, spec.location)
			}
			seen[field] = true
		}
		if scan, ok := spec.descend[field]; ok {
			if err := scan(dec); err != nil {
				return err
			}
		} else if err := skipValue(dec); err != nil {
			return err
		}
	}
	_, err = scanToken(dec) // closing '}'
	return err
}

// checkRecordArrayValue scans one JSON array whose elements are records
// matching recordSpec; recordLocation formats the per-record location from
// its zero-based index. An element that is not an object is consumed and left
// to the regular parse, which reports the type problem.
func checkRecordArrayValue(dec *json.Decoder, recordSpec objectSpec, recordLocation func(index int) string) error {
	tok, err := scanToken(dec)
	if err != nil {
		return err
	}
	if delim, ok := tok.(json.Delim); !ok || delim != '[' {
		return skipRest(dec, tok) // not an array: the regular parse reports it
	}
	for index := 0; dec.More(); index++ {
		spec := recordSpec
		spec.location = recordLocation(index)
		if err := checkObjectValue(dec, spec); err != nil {
			return err
		}
	}
	_, err = scanToken(dec) // closing ']'
	return err
}

// checkGraphValue scans one graph object for a repeated datasets declaration,
// descending into each dataset record of the datasets array. location names
// the graph object itself in the duplicate error ("in \"graph\"" when the
// graph is embedded in a snapshot, the top-level description for a standalone
// graph file). It is shared by the graph file reader and the snapshot reader,
// so the two can never judge one graph differently.
func checkGraphValue(dec *json.Decoder, location string) error {
	return checkObjectValue(dec, objectSpec{
		field:    graphField,
		location: location,
		descend: map[string]func(dec *json.Decoder) error{
			"datasets": checkDatasetRecords,
		},
	})
}

// checkDatasetRecords scans one datasets array, checking every record that is
// an object for repeated name/upstreams declarations.
func checkDatasetRecords(dec *json.Decoder) error {
	return checkRecordArrayValue(dec, objectSpec{field: datasetField}, func(index int) string {
		return fmt.Sprintf("in the dataset record at index %d of \"datasets\"", index)
	})
}

// graphField maps a graph-object key to the canonical name of the graph field
// it selects, matching the decoder's case-insensitive field lookup.
func graphField(key string) (string, bool) {
	if strings.EqualFold(key, "datasets") {
		return "datasets", true
	}
	return "", false
}

// datasetField maps a dataset-record key to the canonical name of the record
// field it selects, matching the decoder's case-insensitive field lookup. A
// plan change record carries the same fields as a dataset record, so both
// readers share this mapping and stay in lockstep.
func datasetField(key string) (string, bool) {
	switch {
	case strings.EqualFold(key, "name"):
		return "name", true
	case strings.EqualFold(key, "upstreams"):
		return "upstreams", true
	}
	return "", false
}

// duplicateFieldError reports a known field declared twice, naming the field
// and where in the document the repetition occurred.
func duplicateFieldError(field, location string) error {
	return fmt.Errorf("%w: field %q is declared more than once %s", ErrInvalidArgument, field, location)
}

// scanToken reads one token; any decode failure aborts the scan so the
// regular parse can report the malformed document.
func scanToken(dec *json.Decoder) (json.Token, error) {
	tok, err := dec.Token()
	if err != nil {
		return nil, errAbortScan
	}
	return tok, nil
}

// scanKey reads the next key of the object currently being walked.
func scanKey(dec *json.Decoder) (string, error) {
	tok, err := scanToken(dec)
	if err != nil {
		return "", err
	}
	key, ok := tok.(string)
	if !ok {
		return "", errAbortScan
	}
	return key, nil
}

// skipValue consumes one complete JSON value.
func skipValue(dec *json.Decoder) error {
	tok, err := scanToken(dec)
	if err != nil {
		return err
	}
	return skipRest(dec, tok)
}

// skipRest consumes the remainder of a value whose first token is tok.
func skipRest(dec *json.Decoder, tok json.Token) error {
	delim, ok := tok.(json.Delim)
	if !ok {
		return nil // scalar, fully consumed already
	}
	var closer json.Delim
	switch delim {
	case '{':
		closer = '}'
		for dec.More() {
			if _, err := scanKey(dec); err != nil {
				return err
			}
			if err := skipValue(dec); err != nil {
				return err
			}
		}
	case '[':
		closer = ']'
		for dec.More() {
			if err := skipValue(dec); err != nil {
				return err
			}
		}
	default:
		return errAbortScan
	}
	tok, err := scanToken(dec)
	if err != nil {
		return err
	}
	if end, ok := tok.(json.Delim); !ok || end != closer {
		return errAbortScan
	}
	return nil
}
