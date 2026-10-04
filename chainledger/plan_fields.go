// Package chainledger duplicate-known-field rules for batch plan files.
//
// A plan shares the same ambiguity hazard as a graph file or snapshot: a
// repeated JSON key keeps only one declaration silently. UnmarshalPlan would
// otherwise honor only the last "changes" or "removals" at the top level, and
// within a change record only the last "name" or "upstreams", so an earlier
// adjustment could vanish, target a different dataset, or have its upstreams
// cleared. The scan below rejects any plan in which a known field is declared
// more than once within the same object:
//
//   - changes and removals in the top-level plan object;
//   - name and upstreams in each record of the changes array.
//
// The whole plan is refused even when the duplicates carry identical values,
// one of them is null, or the surviving value is a legal empty list: an empty
// "changes" declared after a real one must not turn the batch into an empty
// plan. Field names are recognized exactly the way the regular decode
// recognizes them — after JSON string unescaping and with Unicode
// simple case-folding (strings.EqualFold) — so "Changes" and "changes"
// collide, as do spellings that differ only in a fold-equivalent rune. This
// concerns field names only; dataset name VALUES stay case-sensitive and keep
// their surrounding whitespace, and a dataset repeated inside one upstreams
// array is still just one relationship. Unknown fields keep their
// ignore-everything behavior, including keys repeated inside them and
// oversized JSON numbers such as 1e400.
package chainledger

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

// checkPlanFileDuplicateFields rejects a plan that declares a known field more
// than once within the same object. Documents too malformed to walk are left
// to the regular JSON parse, which reports the syntax or type error.
func checkPlanFileDuplicateFields(data []byte) error {
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()
	err := checkPlanObject(dec)
	if errors.Is(err, errAbortScan) {
		return nil
	}
	return err
}

// checkPlanObject scans the top-level plan object for repeated
// changes/removals declarations, descending into a changes value to inspect
// its records.
func checkPlanObject(dec *json.Decoder) error {
	tok, err := scanToken(dec)
	if err != nil {
		return err
	}
	if delim, ok := tok.(json.Delim); !ok || delim != '{' {
		return nil // not an object: the regular parse reports the type error
	}
	seen := make(map[string]bool, 2)
	for dec.More() {
		key, err := scanKey(dec)
		if err != nil {
			return err
		}
		field, known := planField(key)
		if known {
			if seen[field] {
				return duplicateFieldError(field, "at the top level of the plan")
			}
			seen[field] = true
		}
		if field == "changes" {
			if err := checkChangesValue(dec); err != nil {
				return err
			}
		} else if err := skipValue(dec); err != nil {
			return err
		}
	}
	_, err = scanToken(dec) // closing '}'
	return err
}

// checkChangesValue scans one changes array, checking every element that is an
// object for repeated name/upstreams declarations.
func checkChangesValue(dec *json.Decoder) error {
	tok, err := scanToken(dec)
	if err != nil {
		return err
	}
	if delim, ok := tok.(json.Delim); !ok || delim != '[' {
		return skipRest(dec, tok) // not an array: the regular parse reports it
	}
	for index := 0; dec.More(); index++ {
		if err := checkChangeRecord(dec, index); err != nil {
			return err
		}
	}
	_, err = scanToken(dec) // closing ']'
	return err
}

// checkChangeRecord scans one element of the changes array. If it is an
// object, name and upstreams must each be declared at most once.
func checkChangeRecord(dec *json.Decoder, index int) error {
	tok, err := scanToken(dec)
	if err != nil {
		return err
	}
	if delim, ok := tok.(json.Delim); !ok || delim != '{' {
		return skipRest(dec, tok) // not an object: the regular parse reports it
	}
	seen := make(map[string]bool, 2)
	for dec.More() {
		key, err := scanKey(dec)
		if err != nil {
			return err
		}
		if field, known := planChangeField(key); known {
			if seen[field] {
				return duplicateFieldError(field, fmt.Sprintf("in the change record at index %d of \"changes\"", index))
			}
			seen[field] = true
		}
		if err := skipValue(dec); err != nil {
			return err
		}
	}
	_, err = scanToken(dec) // closing '}'
	return err
}

// planField maps a top-level plan key to the canonical name of the field it
// selects, matching the decoder's case-insensitive field lookup.
func planField(key string) (string, bool) {
	switch {
	case strings.EqualFold(key, "changes"):
		return "changes", true
	case strings.EqualFold(key, "removals"):
		return "removals", true
	}
	return "", false
}

// planChangeField maps a change-record key to the canonical name of the field
// it selects, matching the decoder's case-insensitive field lookup.
func planChangeField(key string) (string, bool) {
	switch {
	case strings.EqualFold(key, "name"):
		return "name", true
	case strings.EqualFold(key, "upstreams"):
		return "upstreams", true
	}
	return "", false
}
