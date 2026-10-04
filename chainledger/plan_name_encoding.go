// Package chainledger implements the on-chain data governance core.
//
// This file guards the plan reader against a silent name substitution.
// encoding/json decodes every JSON string by replacing each invalid UTF-8
// byte — and each \u escape that is an unpaired surrogate — with U+FFFD, so
// the decoded plan no longer contains the names the user actually wrote. A
// corrupted name can then collide with a real dataset: a removals entry
// written as "源" plus one broken byte decodes to "源�" and would delete the
// dataset genuinely named "源�". The reader must not guess which dataset the
// user meant, so any name that takes part in the lineage adjustment — the
// name of each change record, every upstreams entry, and every removals
// entry — is checked against its RAW string literal before the plan is
// accepted: a literal carrying bytes that are not valid UTF-8, or a \u
// escape forming an unpaired surrogate (a lone high or low surrogate, or a
// low surrogate written before its high surrogate), rejects the whole plan,
// even when the name would only have matched a dataset that does not exist.
//
// Only the raw literal can tell corruption apart from a genuine name: the
// replacement character itself ("�", or the equivalent JSON escape) is legal and
// stays accepted, as is every correctly paired surrogate escape. Names that
// merely LOOK like escapes (a backslash followed by an ordinary letter) are
// plain text to JSON and are never rejected for that reason.
//
// The scan walks the document with the same field recognition the plan
// reader and the duplicate-field scan use — known fields matched after JSON
// unescaping with Unicode case folding — so a name spelled "Name" or
// "name" is checked exactly like "name". Unknown fields and everything
// nested inside them are skipped; their strings never took part in the
// adjustment and are not checked. A document too malformed to walk is left
// to the regular parse, exactly as in duplicate_fields.go.
package chainledger

import (
	"encoding/json"
	"fmt"
	"strings"
	"unicode/utf8"
)

// checkPlanNameEncoding scans the raw plan JSON and rejects the whole plan
// when any name that participates in the adjustment — changes[i].name, any
// changes[i].upstreams[j], or any removals[i] — is written with a string
// literal the decoder cannot read faithfully (invalid UTF-8 bytes or an
// unpaired surrogate escape). The error names the field and the zero-based
// record or array position, and says which of the two corruptions occurred.
func checkPlanNameEncoding(data []byte) error {
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
			switch {
			case strings.EqualFold(key, "changes"):
				err := checkArrayElements(dec, func(dec *json.Decoder, index int) error {
					return checkNameUpstreamsRecordEncoding(dec, index, "changes", "change record")
				})
				if err != nil {
					return err
				}
			case strings.EqualFold(key, "removals"):
				err := checkArrayElements(dec, func(dec *json.Decoder, index int) error {
					location := fmt.Sprintf("field %q at index %d", "removals", index)
					return checkNameStringEncoding(dec, location)
				})
				if err != nil {
					return err
				}
			default:
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

// checkNameUpstreamsRecordEncoding scans one record that carries a checked
// "name" string and a checked "upstreams" string array — a change record in a
// plan (arrayField "changes", recordKind "change record") or a dataset record
// in a graph (arrayField "datasets", recordKind "dataset record"). The raw
// literals of the name and of every upstream element are validated; every
// other field is skipped. index is the record's zero-based position in its
// array, used together with the array field and record kind in error
// locations, so the plan and graph readers can never judge one record shape
// differently.
func checkNameUpstreamsRecordEncoding(dec *json.Decoder, index int, arrayField, recordKind string) error {
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
			location := fmt.Sprintf("field %q in the %s at index %d of %q", "name", recordKind, index, arrayField)
			if err := checkNameStringEncoding(dec, location); err != nil {
				return err
			}
		case strings.EqualFold(key, "upstreams"):
			err := checkArrayElements(dec, func(dec *json.Decoder, item int) error {
				location := fmt.Sprintf("field %q at index %d in the %s at index %d of %q", "upstreams", item, recordKind, index, arrayField)
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
