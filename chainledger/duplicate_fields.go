// Package chainledger implements the on-chain data governance core.
//
// This file is the single implementation of the repeated-known-field rule
// shared by every JSON document the readers understand (snapshot, standalone
// graph file, and adjustment plan): within one object, a field the reader
// recognizes may be declared only once. A repeated declaration is ambiguous —
// the regular decoder would silently keep only the last value — so the whole
// document is refused, even when the duplicates carry identical values, the
// first is null, or the surviving value would pass every other check. Unknown
// fields keep their ignore-everything behavior and may repeat freely.
//
// Each document supplies only its own field scope (which keys are known at
// each level, and which known field's value nests another checked object or
// array) and its own error locations; the walking, matching, and error
// formatting below are common, so the three readers can never drift apart
// about what counts as a duplicate.
//
// Matching rules, common to every document:
//
//   - Field names are compared after JSON string unescaping and with the same
//     Unicode simple case-folding the decoder applies to its case-insensitive
//     field lookup (strings.EqualFold), so "name" and "Name" collide, and so
//     do spellings that only differ in a fold-equivalent rune such as
//     "dataſets" (long s) and "datasets". Dataset names themselves stay
//     case-sensitive; this folding applies to field names only.
//   - The decoder uses UseNumber so numbers are kept verbatim instead of being
//     converted to float64: a perfectly legal JSON number outside the float64
//     range (such as 1e400), even one buried in an ignored unknown field's
//     value, must not make the decoder error out and abort the scan —
//     otherwise an unrelated large number placed before a repeated known
//     field would hide that duplication. Malformed number syntax still fails
//     and is left to the regular parse to report.
//   - A document too malformed to keep walking aborts the scan; the regular
//     parse then reports the syntax or type error instead, so this check
//     never changes how invalid JSON fails.
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

// runFieldScan runs check over data with the scan decoder (UseNumber, so an
// oversized-but-legal number anywhere in the document cannot abort the walk).
// An aborted scan is not an error: the document is left to the regular parse,
// which reports the structural problem.
func runFieldScan(data []byte, check func(dec *json.Decoder) error) error {
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()
	if err := check(dec); err != nil && !errors.Is(err, errAbortScan) {
		return err
	}
	return nil
}

// knownField is one field an object reader recognizes: name is the canonical
// spelling reported in duplicate errors (matching is case-folded, so any
// equivalent spelling selects it), and nested, when non-nil, checks the
// field's value as another checked object or array. A known field without a
// nested check has its value skipped like any unknown field's.
type knownField struct {
	name   string
	nested func(dec *json.Decoder) error
}

// matchKnownField returns the known field key selects, matching the decoder's
// case-insensitive field lookup, or nil if key is unknown.
func matchKnownField(key string, fields []knownField) *knownField {
	for i := range fields {
		if strings.EqualFold(key, fields[i].name) {
			return &fields[i]
		}
	}
	return nil
}

// checkObjectFields scans the object the decoder is positioned at: each known
// field may be declared at most once, known fields with a nested check descend
// into their value, and every other value is skipped. location describes the
// object itself in duplicate errors. A value that is not an object is consumed
// and left for the regular parse to report.
func checkObjectFields(dec *json.Decoder, location string, fields []knownField) error {
	tok, err := scanToken(dec)
	if err != nil {
		return err
	}
	if delim, ok := tok.(json.Delim); !ok || delim != '{' {
		return skipRest(dec, tok) // not an object: the regular parse reports it
	}
	seen := make(map[string]bool, len(fields))
	for dec.More() {
		key, err := scanKey(dec)
		if err != nil {
			return err
		}
		if field := matchKnownField(key, fields); field != nil {
			if seen[field.name] {
				return duplicateFieldError(field.name, location)
			}
			seen[field.name] = true
			if field.nested != nil {
				if err := field.nested(dec); err != nil {
					return err
				}
				continue
			}
		}
		if err := skipValue(dec); err != nil {
			return err
		}
	}
	_, err = scanToken(dec) // closing '}'
	return err
}

// checkArrayElements scans the array the decoder is positioned at, running
// check on every element with its zero-based index. A value that is not an
// array is consumed and left for the regular parse to report.
func checkArrayElements(dec *json.Decoder, check func(dec *json.Decoder, index int) error) error {
	tok, err := scanToken(dec)
	if err != nil {
		return err
	}
	if delim, ok := tok.(json.Delim); !ok || delim != '[' {
		return skipRest(dec, tok) // not an array: the regular parse reports it
	}
	for index := 0; dec.More(); index++ {
		if err := check(dec, index); err != nil {
			return err
		}
	}
	_, err = scanToken(dec) // closing ']'
	return err
}

// scanObjectFields walks the object the decoder is positioned at using exactly
// the same key recognition and value skipping as checkObjectFields — keys
// matched after JSON unescaping with Unicode case folding, known fields with a
// nested checker descended into, every other value skipped with whatever it
// nests — but WITHOUT the once-only rule: a recognized field is checked on
// every occurrence instead of rejecting the second one. The raw name-encoding
// scan (name_encoding.go) uses this walker; it runs only after the
// duplicate-field scan has already rejected repeated known fields, while it
// must still validate the literals of every known value the document carries.
// A value that is not an object is consumed and left for the regular parse.
func scanObjectFields(dec *json.Decoder, fields []knownField) error {
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
		if field := matchKnownField(key, fields); field != nil && field.nested != nil {
			if err := field.nested(dec); err != nil {
				return err
			}
			continue
		}
		// Unknown field, or a known field without a value checker: ignored
		// with everything nested inside it.
		if err := skipValue(dec); err != nil {
			return err
		}
	}
	_, err = scanToken(dec) // closing '}'
	return err
}

// datasetRecordFields is the field scope of one dataset record — the same
// shape whether the record sits in a graph's "datasets" array or in a plan's
// "changes" array — so both readers judge one record identically.
var datasetRecordFields = []knownField{{name: "name"}, {name: "upstreams"}}

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
