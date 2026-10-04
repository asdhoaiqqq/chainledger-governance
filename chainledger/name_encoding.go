// Package chainledger guards every reader of lineage-bearing names against
// the same silent name substitution.
//
// encoding/json decodes every JSON string by replacing each invalid UTF-8
// byte — and each \u escape that is an unpaired surrogate — with U+FFFD, so
// the decoded document no longer names the datasets the file actually wrote.
// A corrupted reference can then collide with a real node: when one
// dataset's direct upstream is written as "源" plus one lone \uD800 escape,
// the decoder turns it into "源�" and the edge is silently accepted as a
// reference to the root dataset genuinely named "源�"; a corrupted removals
// entry could delete a dataset the user never named. The reader must not
// guess which dataset the file meant, so every name that takes part in
// lineage relations — each datasets[i].name and every
// datasets[i].upstreams[j] in a standalone graph file or in the graph
// embedded in a snapshot, and in a plan each changes[i].name, every
// changes[i].upstreams[j], and every removals[i] (even one that names a
// dataset that does not exist) — is checked against its RAW string literal
// before the document is accepted. A literal carrying bytes that are not
// valid UTF-8, or a \u escape forming an unpaired surrogate (a lone high or
// low surrogate, or a low surrogate written before its high surrogate),
// rejects the whole input: the decoder's rewrite is never kept, the
// offending node or edge is never skipped, and a snapshot whose declared
// content identifier happens to equal the rewritten graph's digest is still
// refused.
//
// Only the raw literal can tell corruption apart from a genuine name: the
// replacement character itself ("�", or the equivalent JSON escape) is legal
// and stays accepted, as is every correctly paired surrogate escape. Names
// that merely LOOK like escapes (a backslash followed by an ordinary letter,
// including a doubled backslash before "ud800") are plain text to JSON and
// are never rejected for that reason.
//
// This file is the single implementation of that rule, shared by the graph
// and plan document scopes in graph_name_encoding.go and
// plan_name_encoding.go. Field recognition is the reader's own — keys are
// compared after JSON unescaping with Unicode case folding via the shared
// walker in duplicate_fields.go (scanObjectFields/checkArrayElements) — so a
// field spelled "Name" is checked exactly like "name", unknown fields and
// everything nested inside them are skipped, and the graph and plan readers
// can never drift apart about which strings are names, how a record is read,
// or how an error is located. A document too malformed to walk is left to
// the regular parse, exactly as in duplicate_fields.go.
package chainledger

import (
	"encoding/json"
	"fmt"
	"unicode/utf8"
)

// graphNameFields is the field scope of one dataset record — the same shape
// whether the record sits in a graph's "datasets" array or in a plan's
// "changes" array — for the raw name-encoding scan: both its "name" string
// and every "upstreams" string element are lineage names and must each
// decode faithfully. Location wording is supplied per array field and
// record kind, so a dataset record and a change record judged at the same
// position fail with the same rule and only the document-specific nouns
// differ.
func graphNameFields(index int, arrayField, recordKind string) []knownField {
	return []knownField{
		{name: "name", nested: func(dec *json.Decoder) error {
			location := fmt.Sprintf("field %q in the %s at index %d of %q", "name", recordKind, index, arrayField)
			return checkNameStringEncoding(dec, location)
		}},
		{name: "upstreams", nested: func(dec *json.Decoder) error {
			return checkArrayElements(dec, func(dec *json.Decoder, item int) error {
				location := fmt.Sprintf("field %q at index %d in the %s at index %d of %q", "upstreams", item, recordKind, index, arrayField)
				return checkNameStringEncoding(dec, location)
			})
		}},
	}
}

// scanNameUpstreamsRecords validates every record of a "name" + "upstreams"
// array: datasets in a graph (arrayField "datasets", recordKind "dataset
// record") or changes in a plan ("changes", "change record"). The decoder
// must be positioned at the field's array value.
func scanNameUpstreamsRecords(dec *json.Decoder, arrayField, recordKind string) error {
	return checkArrayElements(dec, func(dec *json.Decoder, index int) error {
		return scanObjectFields(dec, graphNameFields(index, arrayField, recordKind))
	})
}

// scanRemovalsNames validates every element of a plan's "removals" array.
// A removal is a lineage name even when the dataset it names does not
// exist, so its raw literal is checked like every other name. The decoder
// must be positioned at the field's array value.
func scanRemovalsNames(dec *json.Decoder) error {
	return checkArrayElements(dec, func(dec *json.Decoder, index int) error {
		location := fmt.Sprintf("field %q at index %d", "removals", index)
		return checkNameStringEncoding(dec, location)
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
