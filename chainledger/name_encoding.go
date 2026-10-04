// Package chainledger guards every raw-JSON name reader against the same
// silent name substitution: this file is the one implementation shared by the
// standalone graph reader (UnmarshalGraphFile), the graph embedded in a
// snapshot (ParseSnapshot), and the adjustment-plan reader (UnmarshalPlan).
//
// encoding/json decodes every JSON string by replacing each invalid UTF-8
// byte — and each \u escape that is an unpaired surrogate — with U+FFFD, so
// the decoded document no longer carries the names the file actually wrote.
// The rewritten name can then collide with a real one:
//
//   - in a graph, when one dataset's direct upstream is written as "源" plus
//     one lone \uD800 escape, the decoder turns it into "源�" and the edge is
//     silently accepted as a reference to the root dataset genuinely named
//     "源�";
//   - in a plan, a removals entry written as "源" plus one broken byte decodes
//     to "源�" and would delete the dataset genuinely named "源�".
//
// The reader must not guess which dataset the file meant, so every name that
// actually takes part in lineage — in a graph each datasets[i].name and every
// datasets[i].upstreams[j]; in a plan each changes[i].name, every
// changes[i].upstreams[j], and every removals[i] — is checked against its RAW
// string literal before the document is accepted, whether the graph stands
// alone or is the "graph" value embedded in a snapshot. A literal carrying
// bytes that are not valid UTF-8, or a \u escape forming an unpaired surrogate
// (a lone high or low surrogate, or a low surrogate written before its high
// surrogate), rejects the whole input: the decoder's rewrite is never kept,
// the offending record or entry is never skipped, a removal that names no
// existing dataset is still refused, and a snapshot whose declared content
// identifier happens to equal the rewritten graph's digest is still refused.
//
// Only the raw literal can tell corruption apart from a genuine name: the
// replacement character itself ("�", or the equivalent JSON escape) is legal
// and stays accepted, as is every correctly paired surrogate escape. Names
// that merely LOOK like escapes (a backslash followed by an ordinary letter)
// are plain text to JSON and are never rejected for that reason.
//
// The scan walks the document with exactly the same field recognition the
// readers and the duplicate-field scan use — keys are compared after JSON
// unescaping with Unicode case folding (strings.EqualFold), so a name field
// spelled "Name" is checked exactly like "name" — while the folding applies
// to field names only and dataset names themselves stay case-sensitive with
// surrounding spaces kept. Unknown fields and everything nested inside them
// are skipped; their strings never name a dataset and are not checked. A
// document too malformed to walk aborts the scan and is left to the regular
// parse, exactly as in duplicate_fields.go.
package chainledger

import (
	"encoding/json"
	"fmt"
	"strings"
	"unicode/utf8"
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
		return checkNameObject(dec, []knownNameField{{
			key: "datasets",
			scan: func(dec *json.Decoder) error {
				return checkNameRecordArray(dec, "datasets", "dataset record")
			},
		}})
	})
}

// checkPlanNameEncoding scans the raw plan JSON and rejects the whole plan
// when any name that participates in the adjustment — changes[i].name, any
// changes[i].upstreams[j], or any removals[i] — is written with a string
// literal the decoder cannot read faithfully (invalid UTF-8 bytes or an
// unpaired surrogate escape). The error names the field and the zero-based
// record or array position, and says which of the two corruptions occurred.
func checkPlanNameEncoding(data []byte) error {
	return runFieldScan(data, func(dec *json.Decoder) error {
		return checkNameObject(dec, []knownNameField{
			{
				key: "changes",
				scan: func(dec *json.Decoder) error {
					return checkNameRecordArray(dec, "changes", "change record")
				},
			},
			{
				key: "removals",
				scan: func(dec *json.Decoder) error {
					return checkNameStringArray(dec, func(item int) string {
						return fmt.Sprintf("field %q at index %d", "removals", item)
					})
				},
			},
		})
	})
}

// knownNameField is one field a name reader recognizes at one fixed object
// scope. key is matched after JSON unescaping with Unicode case folding, the
// same way the decoder resolves field names; scan walks the field's value.
// Every other key at the scope is unknown and its value is skipped with all
// of its nested content.
type knownNameField struct {
	key  string
	scan func(dec *json.Decoder) error
}

// checkNameObject scans the object the decoder is positioned at, restricted
// to fields: each known field's value is walked by its scan, and every other
// value is skipped with everything nested inside it. A value that is not an
// object is consumed and left for the regular parse to report. This is the
// one field-walk every name reader uses, so the graph and plan readers can
// never drift apart about field recognition, unknown-field skipping, or the
// order fields are visited.
func checkNameObject(dec *json.Decoder, fields []knownNameField) error {
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
		if field := matchKnownNameField(key, fields); field != nil {
			if err := field.scan(dec); err != nil {
				return err
			}
			continue
		}
		// Unknown field: ignored with everything nested inside it.
		if err := skipValue(dec); err != nil {
			return err
		}
	}
	_, err = scanToken(dec) // closing '}'
	return err
}

// matchKnownNameField returns the known name field key selects, matching the
// decoder's case-insensitive field lookup, or nil if key is unknown here.
func matchKnownNameField(key string, fields []knownNameField) *knownNameField {
	for i := range fields {
		if strings.EqualFold(key, fields[i].key) {
			return &fields[i]
		}
	}
	return nil
}

// checkNameRecordArray scans one array of name/upstreams records — a graph's
// datasets or a plan's changes — checking every element at its zero-based
// position. A value that is not an array is consumed and left for the regular
// parse to report.
func checkNameRecordArray(dec *json.Decoder, arrayField, recordKind string) error {
	return checkArrayElements(dec, func(dec *json.Decoder, index int) error {
		return checkNameUpstreamsRecord(dec, index, arrayField, recordKind)
	})
}

// checkNameStringArray scans one array of plain name strings — the upstreams
// of one record or the plan's removals — checking every element at its
// zero-based position. location renders the element's location phrase. A
// value that is not an array is consumed and left for the regular parse to
// report.
func checkNameStringArray(dec *json.Decoder, location func(item int) string) error {
	return checkArrayElements(dec, func(dec *json.Decoder, item int) error {
		return checkNameStringEncoding(dec, location(item))
	})
}

// checkNameUpstreamsRecord scans one record that carries a checked "name"
// string and a checked "upstreams" string array — a change record in a plan
// (arrayField "changes", recordKind "change record") or a dataset record in a
// graph (arrayField "datasets", recordKind "dataset record"). The raw
// literals of the name and of every upstream element are validated; every
// other field is skipped. index is the record's zero-based position in its
// array, used together with the array field and record kind in error
// locations, so the plan and graph readers can never judge one record shape
// differently.
func checkNameUpstreamsRecord(dec *json.Decoder, index int, arrayField, recordKind string) error {
	return checkNameObject(dec, []knownNameField{
		{
			key: "name",
			scan: func(dec *json.Decoder) error {
				location := fmt.Sprintf("field %q in the %s at index %d of %q",
					"name", recordKind, index, arrayField)
				return checkNameStringEncoding(dec, location)
			},
		},
		{
			key: "upstreams",
			scan: func(dec *json.Decoder) error {
				return checkNameStringArray(dec, func(item int) string {
					return fmt.Sprintf("field %q at index %d in the %s at index %d of %q",
						"upstreams", item, recordKind, index, arrayField)
				})
			},
		},
	})
}

// checkNameStringEncoding consumes one value and, when it is a string
// literal, validates its raw bytes with validateNameStringLiteral. A
// non-string value is skipped: the regular parse reports the type error.
func checkNameStringEncoding(dec *json.Decoder, location string) error {
	var raw json.RawMessage
	if err := dec.Decode(&raw); err != nil {
		return errAbortScan
	}
	if len(raw) < 2 || raw[0] != '"' {
		return nil
	}
	return validateNameStringLiteral(raw, location)
}

// validateNameStringLiteral checks one raw JSON string literal (surrounding
// quotes included) for the two corruptions the decoder would silently
// rewrite to U+FFFD: bytes that are not valid UTF-8, and \u escapes forming
// an unpaired surrogate. The literal is known to be syntactically valid
// JSON (the regular parse already accepted the document), so escapes are
// well-formed; anything else unexpected aborts the scan and is left to that
// parse. location identifies the field and position in the error.
func validateNameStringLiteral(raw []byte, location string) error {
	end := len(raw) - 1 // index of the closing quote
	for i := 1; i < end; {
		c := raw[i]
		switch {
		case c == '\\':
			if i+1 >= end {
				return errAbortScan
			}
			if raw[i+1] != 'u' {
				i += 2 // \", \\, \/, \b, \f, \n, \r, \t
				continue
			}
			if i+6 > end {
				return errAbortScan
			}
			cp, ok := hex4(raw[i+2 : i+6])
			if !ok {
				return errAbortScan
			}
			i += 6
			switch {
			case isHighSurrogate(cp):
				// A high surrogate is legal only as the first half of a
				// pair: the very next escape must be its low surrogate.
				if i+6 <= end && raw[i] == '\\' && raw[i+1] == 'u' {
					if low, ok := hex4(raw[i+2 : i+6]); ok && isLowSurrogate(low) {
						i += 6
						continue
					}
				}
				return unpairedSurrogateError(location)
			case isLowSurrogate(cp):
				// A lone low surrogate — including one written before its
				// high surrogate — never forms a pair.
				return unpairedSurrogateError(location)
			}
		case c < utf8.RuneSelf:
			i++
		default:
			// A genuine U+FFFD decodes with size 3 and is accepted; only a
			// byte the decoder cannot map (size 1) is corruption.
			r, size := utf8.DecodeRune(raw[i:end])
			if r == utf8.RuneError && size == 1 {
				return invalidUTF8BytesError(location)
			}
			i += size
		}
	}
	return nil
}

// hex4 parses exactly four hexadecimal digits (one \u escape unit).
func hex4(b []byte) (rune, bool) {
	var v rune
	for _, c := range b {
		v <<= 4
		switch {
		case c >= '0' && c <= '9':
			v |= rune(c - '0')
		case c >= 'a' && c <= 'f':
			v |= rune(c-'a') + 10
		case c >= 'A' && c <= 'F':
			v |= rune(c-'A') + 10
		default:
			return 0, false
		}
	}
	return v, true
}

func isHighSurrogate(cp rune) bool { return cp >= 0xD800 && cp <= 0xDBFF }
func isLowSurrogate(cp rune) bool  { return cp >= 0xDC00 && cp <= 0xDFFF }

// invalidUTF8BytesError reports a name whose raw string literal carries
// bytes that are not valid UTF-8.
func invalidUTF8BytesError(location string) error {
	return fmt.Errorf("%w: %s contains invalid UTF-8 bytes", ErrInvalidArgument, location)
}

// unpairedSurrogateError reports a name whose \u escapes form an unpaired
// surrogate (a lone high or low surrogate, or a low surrogate before its
// high surrogate).
func unpairedSurrogateError(location string) error {
	return fmt.Errorf("%w: %s contains an unpaired surrogate escape", ErrInvalidArgument, location)
}
