// Package chainledger — faithful-reading gate for adjustment plan names.
//
// Decoding a JSON string into a Go string is lossy at exactly the two spots
// this file guards: encoding/json silently rewrites every invalid UTF-8 byte
// to U+FFFD, and rewrites an unpaired \u surrogate escape (a lone high or low
// surrogate, or a low surrogate followed by a high one) to U+FFFD as well. A
// plan name corrupted either way can collapse onto the name of a real dataset
// — a removals entry written as "源" plus one invalid byte would decode to
// "源�" and delete a dataset that legitimately carries that name. The reader
// must not guess which dataset the user meant: any plan whose adjustment
// names cannot be read faithfully is refused as a whole, even when the
// corrupted name would only have matched nothing.
//
// The gate inspects the exact source bytes of every name that takes part in
// the batch — each change record's "name", every item of its "upstreams", and
// every item of "removals" — by decoding the plan into json.RawMessage leaves
// first. RawMessage keeps the value's original bytes, so the raw string
// literal is validated before any replacement can happen. Because the raw
// decode uses the same struct shape and tags as the real parse, field
// recognition (JSON unescaping plus the decoder's case-insensitive lookup)
// and the ignore-everything treatment of unknown fields are identical by
// construction; strings inside unknown fields are never inspected.
//
// Only encoding is judged here. A genuine U+FFFD character (literal or
// "�") is valid UTF-8 and stays a legal name; CJK, emoji, combining
// marks, casing, and surrounding spaces are all kept verbatim with no
// normalization, and a correctly paired surrogate escape still expresses its
// astral character. A backslash written as "\\" followed by an ordinary "u"
// is just text and is never mistaken for an escape.
package chainledger

import (
	"encoding/json"
	"fmt"
	"unicode/utf8"
)

// rawPlan mirrors Plan with json.RawMessage leaves so the reader can inspect
// the exact source bytes of every adjustment name before the lossy string
// decode. Field matching and unknown-field handling are the decoder's own,
// identical to the real parse.
type rawPlan struct {
	Changes  []rawPlanChange   `json:"changes"`
	Removals []json.RawMessage `json:"removals"`
}

// rawPlanChange mirrors PlanChange with raw leaves.
type rawPlanChange struct {
	Name      json.RawMessage   `json:"name"`
	Upstreams []json.RawMessage `json:"upstreams"`
}

// checkPlanNamesUTF8 rejects a plan whose adjustment names cannot be read
// faithfully: any change-record name, upstream item, or removals item whose
// raw string contains invalid UTF-8 bytes or an unpaired \u surrogate escape
// refuses the whole plan. The error names the field and the zero-based record
// or item index, and says which of the two corruptions was found.
//
// A document that does not match the plan shape (malformed JSON, or a known
// field carrying a value of the wrong type) is left for the regular parse,
// which reports the syntax or type error with its usual message; this gate
// never changes how those plans fail.
func checkPlanNamesUTF8(data []byte) error {
	var raw rawPlan
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil
	}
	for i := range raw.Changes {
		where := fmt.Sprintf("in the change record at index %d of \"changes\"", i)
		if err := validatePlanName(raw.Changes[i].Name, fmt.Sprintf("field \"name\" %s", where)); err != nil {
			return err
		}
		for j := range raw.Changes[i].Upstreams {
			if err := validatePlanName(raw.Changes[i].Upstreams[j], fmt.Sprintf("the item at index %d of field \"upstreams\" %s", j, where)); err != nil {
				return err
			}
		}
	}
	for k := range raw.Removals {
		if err := validatePlanName(raw.Removals[k], fmt.Sprintf("the item at index %d of field \"removals\"", k)); err != nil {
			return err
		}
	}
	return nil
}

// validatePlanName checks one raw JSON value that the plan uses as a dataset
// name. A value that is not a JSON string is ignored: the regular parse
// reports the type error. For a string, the raw content between the quotes is
// scanned: bytes outside escapes must be valid UTF-8, and every \u escape
// that starts a surrogate pair must be completed by the matching low
// surrogate escape. The document already passed the decoder's syntax scan, so
// escape shapes other than an unpaired surrogate cannot occur here.
func validatePlanName(raw json.RawMessage, where string) error {
	if len(raw) < 2 || raw[0] != '"' || raw[len(raw)-1] != '"' {
		return nil
	}
	s := raw[1 : len(raw)-1]
	for i := 0; i < len(s); {
		c := s[i]
		switch {
		case c == '\\':
			if i+1 < len(s) && s[i+1] == 'u' {
				unit, _ := parseHex4(s[i+2:])
				switch {
				case isHighSurrogate(unit):
					pair, ok := parseSurrogateEscape(s[i+6:])
					if !ok || !isLowSurrogate(pair) {
						return unpairedSurrogateError(where, "high", unit)
					}
					i += 12
				case isLowSurrogate(unit):
					return unpairedSurrogateError(where, "low", unit)
				default:
					i += 6
				}
			} else {
				// Any other escape (\\, \", \n, ...) encodes ordinary text.
				i += 2
			}
		case c < utf8.RuneSelf:
			i++
		default:
			r, size := utf8.DecodeRune(s[i:])
			if r == utf8.RuneError && size == 1 {
				return fmt.Errorf("%w: %s contains the invalid UTF-8 byte 0x%02x", ErrInvalidArgument, where, c)
			}
			i += size
		}
	}
	return nil
}

// unpairedSurrogateError reports a \u escape whose surrogate half has no
// mate, naming the field location, which half was found, and its code unit.
func unpairedSurrogateError(where, half string, unit rune) error {
	return fmt.Errorf("%w: %s contains an unpaired %s surrogate escape \\u%04x", ErrInvalidArgument, where, half, unit)
}

// isHighSurrogate reports whether unit is a high (lead) surrogate code unit.
func isHighSurrogate(unit rune) bool { return unit >= 0xD800 && unit <= 0xDBFF }

// isLowSurrogate reports whether unit is a low (trail) surrogate code unit.
func isLowSurrogate(unit rune) bool { return unit >= 0xDC00 && unit <= 0xDFFF }

// parseSurrogateEscape reads one "\uXXXX" escape at the start of s.
func parseSurrogateEscape(s []byte) (rune, bool) {
	if len(s) < 6 || s[0] != '\\' || s[1] != 'u' {
		return 0, false
	}
	return parseHex4(s[2:])
}

// parseHex4 reads exactly four hexadecimal digits from the start of s.
func parseHex4(s []byte) (rune, bool) {
	if len(s) < 4 {
		return 0, false
	}
	var unit rune
	for _, c := range s[:4] {
		var digit byte
		switch {
		case c >= '0' && c <= '9':
			digit = c - '0'
		case c >= 'a' && c <= 'f':
			digit = c - 'a' + 10
		case c >= 'A' && c <= 'F':
			digit = c - 'A' + 10
		default:
			return 0, false
		}
		unit = unit<<4 | rune(digit)
	}
	return unit, true
}
