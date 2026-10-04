package chainledger

import (
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
)

// These tests pin the rule that a graph's raw name literals — in a
// standalone graph file and in the graph embedded in a snapshot — must
// decode exactly as written. encoding/json silently rewrites invalid UTF-8
// bytes and unpaired \u surrogate escapes to U+FFFD, so a corrupted upstream
// reference could resolve to a dataset the file never named: with a root
// really named "源�" in the graph, an upstream written as "源" plus a lone
// \uD800 escape decodes to "源�" and reads as an edge to that root. Both
// graph readers must refuse the whole document instead.

// corruptSnapshotDocument wraps rawGraphJSON (which is allowed to carry
// corrupt name literals) as a version-1 snapshot. The declared content id is
// the digest of the graph the decoder would silently REPLACE the corrupt
// names with, computed from a plain (rewriting) decode: the document is the
// exact shape that would read as valid if the reader only checked the content
// identifier. When the replaced graph is not itself structurally valid (for
// example a low-before-high pair decodes to two replacement characters and
// dangles), a placeholder id is used; the encoding check must still reject
// the snapshot before any structure or id check matters.
func corruptSnapshotDocument(t *testing.T, rawGraphJSON string) []byte {
	t.Helper()
	var gf GraphFile
	if err := json.Unmarshal([]byte(rawGraphJSON), &gf); err != nil {
		t.Fatalf("plain decode of the test graph failed: %v", err)
	}
	contentID := "sha256:placeholder"
	if adj, err := validateGraphStructureFromFile(gf.Datasets); err == nil {
		canon, err := canonicalGraph(adj)
		if err != nil {
			t.Fatalf("canonical replaced graph: %v", err)
		}
		contentID = computeContentID(canon)
	}
	doc := fmt.Sprintf(`{"formatVersion":1,"contentId":%q,"graph":%s}`, contentID, rawGraphJSON)
	return []byte(doc)
}

// TestUnmarshalGraphFileRejectsCorruptNameBytes: a raw dataset name or
// upstream literal carrying bytes that are not valid UTF-8 rejects the whole
// graph file, naming name versus upstreams and the zero-based positions.
func TestUnmarshalGraphFileRejectsCorruptNameBytes(t *testing.T) {
	cases := map[string]struct {
		data     string
		field    string
		location string
	}{
		"name in first record": {
			`{"datasets":[{"name":"source` + "\xff" + `","upstreams":[]}]}`,
			`"name"`, `dataset record at index 0`,
		},
		"name in second record": {
			`{"datasets":[{"name":"A","upstreams":[]},{"name":"b` + "\xfe" + `","upstreams":[]}]}`,
			`"name"`, `dataset record at index 1`,
		},
		"upstream entry": {
			`{"datasets":[{"name":"A","upstreams":[]},{"name":"B","upstreams":["A","g` + "\xfe" + `"]}]}`,
			`"upstreams"`, `index 1 of the dataset record at index 1`,
		},
		"upstream entry in first record": {
			`{"datasets":[{"name":"A","upstreams":["ok","x` + "\xc3" + `"]}]}`,
			`"upstreams"`, `index 1 of the dataset record at index 0`,
		},
		"case-folded known field is still checked": {
			`{"DATASETS":[{"NAME":"a` + "\xff" + `","UPSTREAMS":[]}]}`,
			`"name"`, `dataset record at index 0`,
		},
		"escaped key spelling is still checked": {
			`{"datasets":[{"name":"a` + "\xff" + `","upstreams":[]}]}`,
			`"name"`, `dataset record at index 0`,
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := UnmarshalGraphFile([]byte(tc.data))
			if !errors.Is(err, ErrInvalidArgument) {
				t.Fatalf("err = %v, want ErrInvalidArgument", err)
			}
			msg := err.Error()
			if !strings.Contains(msg, "invalid UTF-8 bytes") {
				t.Errorf("err = %q, want it to say the bytes are invalid UTF-8", msg)
			}
			if strings.Contains(msg, "surrogate") {
				t.Errorf("err = %q, byte corruption must not be reported as a surrogate problem", msg)
			}
			if !strings.Contains(msg, tc.field) {
				t.Errorf("err = %q, want it to name field %s", msg, tc.field)
			}
			if !strings.Contains(msg, tc.location) {
				t.Errorf("err = %q, want it to locate %s", msg, tc.location)
			}
			if strings.Contains(msg, "�") {
				t.Errorf("err = %q, must not contain the replacement character", msg)
			}
		})
	}
}

// TestUnmarshalGraphFileRejectsUnpairedSurrogates: a \u escape forming an
// unpaired surrogate — a lone high or low surrogate, a low surrogate before
// its high surrogate, or a high surrogate not immediately followed by its low
// surrogate — rejects the whole graph in every name position.
func TestUnmarshalGraphFileRejectsUnpairedSurrogates(t *testing.T) {
	cases := map[string]struct {
		data     string
		field    string
		location string
	}{
		"lone high surrogate in name": {
			`{"datasets":[{"name":"a\ud800x","upstreams":[]}]}`,
			`"name"`, `dataset record at index 0`,
		},
		"high surrogate at end of name": {
			`{"datasets":[{"name":"a\ud800","upstreams":[]}]}`,
			`"name"`, `dataset record at index 0`,
		},
		"lone low surrogate in name": {
			`{"datasets":[{"name":"a\udc00","upstreams":[]}]}`,
			`"name"`, `dataset record at index 0`,
		},
		"low surrogate before high surrogate": {
			`{"datasets":[{"name":"a\udc00\ud800","upstreams":[]}]}`,
			`"name"`, `dataset record at index 0`,
		},
		"high surrogate then non-surrogate escape": {
			`{"datasets":[{"name":"a\ud800A","upstreams":[]}]}`,
			`"name"`, `dataset record at index 0`,
		},
		"high surrogate then another high surrogate": {
			`{"datasets":[{"name":"a\ud800\ud801","upstreams":[]}]}`,
			`"name"`, `dataset record at index 0`,
		},
		"surrogate in second record's name": {
			`{"datasets":[{"name":"A","upstreams":[]},{"name":"\ud800","upstreams":[]}]}`,
			`"name"`, `dataset record at index 1`,
		},
		"lone high surrogate in upstream": {
			`{"datasets":[{"name":"A","upstreams":[]},{"name":"B","upstreams":["A","\ud800"]}]}`,
			`"upstreams"`, `index 1 of the dataset record at index 1`,
		},
		"lone low surrogate in upstream": {
			`{"datasets":[{"name":"A","upstreams":["\udfff"]}]}`,
			`"upstreams"`, `index 0 of the dataset record at index 0`,
		},
		"low surrogate before high surrogate in upstream": {
			`{"datasets":[{"name":"A","upstreams":["\udc00\ud800"]}]}`,
			`"upstreams"`, `index 0 of the dataset record at index 0`,
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := UnmarshalGraphFile([]byte(tc.data))
			if !errors.Is(err, ErrInvalidArgument) {
				t.Fatalf("err = %v, want ErrInvalidArgument", err)
			}
			msg := err.Error()
			if !strings.Contains(msg, "unpaired surrogate") {
				t.Errorf("err = %q, want it to say the surrogate escape is unpaired", msg)
			}
			if strings.Contains(msg, "invalid UTF-8 bytes") {
				t.Errorf("err = %q, a surrogate problem must not be reported as invalid bytes", msg)
			}
			if !strings.Contains(msg, tc.field) {
				t.Errorf("err = %q, want it to name field %s", msg, tc.field)
			}
			if !strings.Contains(msg, tc.location) {
				t.Errorf("err = %q, want it to locate %s", msg, tc.location)
			}
		})
	}
}

// TestUnmarshalGraphFileCorruptUpstreamCannotCollideWithRealRoot is the bug
// being fixed: a root really named "源�" and another dataset whose direct
// upstream is written as "源" plus a lone surrogate must not be read as an
// edge to that root. The same input must fail for invalid bytes and even
// when nothing exists for the corruption to collide with.
func TestUnmarshalGraphFileCorruptUpstreamCannotCollideWithRealRoot(t *testing.T) {
	t.Run("lone surrogate collides with a genuine root", func(t *testing.T) {
		data := `{"datasets":[{"name":"源�","upstreams":[]},{"name":"down","upstreams":["源\ud800"]}]}`
		if _, err := UnmarshalGraphFile([]byte(data)); err == nil {
			t.Fatal("corrupt upstream was read as a reference to the root named 源�")
		}
	})
	t.Run("broken byte collides with a genuine root", func(t *testing.T) {
		data := `{"datasets":[{"name":"源�","upstreams":[]},{"name":"down","upstreams":["源` + "\xff" + `"]}]}`
		if _, err := UnmarshalGraphFile([]byte(data)); err == nil {
			t.Fatal("corrupt upstream was read as a reference to the root named 源�")
		}
	})
	t.Run("no possible collision still fails", func(t *testing.T) {
		for _, data := range []string{
			`{"datasets":[{"name":"only\xff","upstreams":[]}]}`,
			`{"datasets":[{"name":"only","upstreams":["ghost\ud800"]}]}`,
		} {
			if _, err := UnmarshalGraphFile([]byte(data)); err == nil {
				t.Fatalf("a graph with a corrupt name and no collision was accepted: %s", data)
			}
		}
	})
}

// TestUnmarshalGraphFileCorruptNameRejectsWholeGraph: one corrupt name
// refuses the entire graph — legal records and relations are not kept while
// the bad node is dropped.
func TestUnmarshalGraphFileCorruptNameRejectsWholeGraph(t *testing.T) {
	data := `{"datasets":[{"name":"A","upstreams":[]},{"name":"b` + "\xff" + `","upstreams":["A"]},{"name":"C","upstreams":["A"]}]}`
	if _, err := UnmarshalGraphFile([]byte(data)); err == nil {
		t.Fatal("a graph mixing legal records with one corrupt name must be rejected as a whole")
	}
}

// TestUnmarshalGraphFileAcceptsGenuineUnicodeNames: names that are legal
// stay byte-for-byte intact — a genuine replacement character (written
// directly or as its escape), correctly paired surrogate escapes, CJK,
// emoji, combining marks, casing, surrounding spaces, and names that merely
// look like escapes (a backslash before an ordinary letter).
func TestUnmarshalGraphFileAcceptsGenuineUnicodeNames(t *testing.T) {
	cases := map[string]struct {
		data string
		want map[string][]string
	}{
		"genuine replacement character": {
			`{"datasets":[{"name":"源�","upstreams":[]}]}`,
			map[string][]string{"源�": nil},
		},
		"replacement character via escape": {
			`{"datasets":[{"name":"\u6e90\ufffd","upstreams":[]}]}`,
			map[string][]string{"源�": nil},
		},
		"paired surrogate escape": {
			`{"datasets":[{"name":"ledger\ud83d\ude00","upstreams":[]}]}`,
			map[string][]string{"ledger😀": nil},
		},
		"paired surrogate at string start and end": {
			`{"datasets":[{"name":"\ud83d\ude00x\ud83c\udf10","upstreams":[]}]}`,
			map[string][]string{"😀x🌐": nil},
		},
		"cjk combining marks casing and spaces": {
			`{"datasets":[{"name":" 数据 Éé ","upstreams":[]}]}`,
			map[string][]string{" 数据 Éé ": nil},
		},
		"backslash before ordinary letter": {
			`{"datasets":[{"name":"a\\nb","upstreams":[]}]}`,
			map[string][]string{`a\nb`: nil},
		},
		"backslash before what looks like a surrogate escape": {
			// The name really contains backslash, 'u', 'd', '8', '0', '0'; it
			// must not be rejected for looking like an escape.
			`{"datasets":[{"name":"x\\ud800","upstreams":[]}]}`,
			map[string][]string{`x\ud800`: nil},
		},
		"escaped backslash then a real escape": {
			`{"datasets":[{"name":"x\\\ud83d\ude00","upstreams":[]}]}`,
			map[string][]string{`x\😀`: nil},
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			graph, err := UnmarshalGraphFile([]byte(tc.data))
			if err != nil {
				t.Fatalf("UnmarshalGraphFile rejected a legal name: %v", err)
			}
			got := make(map[string][]string, len(graph))
			for n, entry := range graph {
				got[n] = entry.Parents
			}
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("graph decoded to %#v, want %#v", got, tc.want)
			}
		})
	}
}

// TestUnmarshalGraphFileEquivalentEscapesSameGraph: legal text written
// directly and the same text written with JSON escapes describe the same
// graph; the reader performs no Unicode normalization.
func TestUnmarshalGraphFileEquivalentEscapesSameGraph(t *testing.T) {
	direct := `{"datasets":[{"name":"数据集","upstreams":[]},{"name":"派生","upstreams":["数据集","源�"]},{"name":"源�","upstreams":[]}]}`
	escaped := `{"datasets":[{"name":"\u6e90\ufffd","upstreams":[]},{"name":"\u6d3e\u751f","upstreams":["\u6570\u636e\u96c6","\u6e90\ufffd"]},{"name":"\u6570\u636e\u96c6","upstreams":[]}]}`
	gDirect, err := UnmarshalGraphFile([]byte(direct))
	if err != nil {
		t.Fatalf("direct spelling rejected: %v", err)
	}
	gEscaped, err := UnmarshalGraphFile([]byte(escaped))
	if err != nil {
		t.Fatalf("escaped spelling rejected: %v", err)
	}
	jDirect, _ := MarshalGraphFile(gDirect)
	jEscaped, _ := MarshalGraphFile(gEscaped)
	if string(jDirect) != string(jEscaped) {
		t.Fatalf("equivalent spellings produced different graphs:\n%s\n%s", jDirect, jEscaped)
	}
}

// TestUnmarshalGraphFileIgnoresCorruptStringsInUnknownFields: unknown fields
// and everything nested inside them keep their ignore-everything behavior —
// their strings are not graph names and are not checked.
func TestUnmarshalGraphFileIgnoresCorruptStringsInUnknownFields(t *testing.T) {
	cases := map[string]string{
		"corrupt string in unknown top-level field":      `{"meta":"a\ud800","datasets":[{"name":"A","upstreams":[]}]}`,
		"corrupt bytes in unknown record field":          `{"datasets":[{"name":"A","note":"x` + "\xff" + `","upstreams":[]}]}`,
		"known field names nested inside unknown field":  `{"also":{"datasets":[{"name":"\ud800","upstreams":["g` + "\xff" + `"]}]},"datasets":[{"name":"A","upstreams":[]}]}`,
		"deeply nested corrupt strings in unknown field": `{"datasets":[{"name":"A","upstreams":[],"extra":{"deep":["\udc00",{"k":"v` + "\xfe" + `"}]}}]}`,
		"unknown field that merely prefixes a known one": `{"datasetsX":[{"name":"\ud800"}],"datasets":[{"name":"A","upstreams":[]}]}`,
	}
	for name, data := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := UnmarshalGraphFile([]byte(data)); err != nil {
				t.Fatalf("unknown-field strings must not be checked, got %v", err)
			}
		})
	}
}

// TestParseSnapshotRejectsCorruptGraphNames: a snapshot's embedded graph is
// checked exactly like a standalone graph file. Rejection names the embedded
// graph, the field, and the record/array positions. Crucially, the declared
// content identifier matches the graph the decoder would REPLACE the corrupt
// names with — only the raw-name check stands between the file and
// acceptance.
func TestParseSnapshotRejectsCorruptGraphNames(t *testing.T) {
	cases := map[string]struct {
		graph    string
		field    string
		location string
		reason   string
	}{
		"invalid bytes in record name, matching content id": {
			`{"datasets":[{"name":"源` + "\xff" + `","upstreams":[]}]}`,
			`"name"`, `dataset record at index 0`, "invalid UTF-8 bytes",
		},
		"lone surrogate in record name, matching content id": {
			`{"datasets":[{"name":"源\ud800","upstreams":[]}]}`,
			`"name"`, `dataset record at index 0`, "unpaired surrogate",
		},
		"corrupt upstream colliding with a real root, matching content id": {
			`{"datasets":[{"name":"down","upstreams":["源\ud800"]},{"name":"源�","upstreams":[]}]}`,
			`"upstreams"`, `index 0 of the dataset record at index 0`, "unpaired surrogate",
		},
		"invalid-byte upstream colliding with a real root, matching content id": {
			`{"datasets":[{"name":"down","upstreams":["源` + "\xff" + `"]},{"name":"源�","upstreams":[]}]}`,
			`"upstreams"`, `index 0 of the dataset record at index 0`, "invalid UTF-8 bytes",
		},
		"second upstream in second record, matching content id": {
			// The corrupted reference rewrites to the genuine root "x�", so the
			// replaced graph is valid and the declared id matches it.
			`{"datasets":[{"name":"A","upstreams":[]},{"name":"B","upstreams":["A","x\udc00"]},{"name":"x�","upstreams":[]}]}`,
			`"upstreams"`, `index 1 of the dataset record at index 1`, "unpaired surrogate",
		},
		"low-before-high surrogate whose replaced graph dangles": {
			// The replaced name is two replacement characters, so no content
			// id can match a valid graph; the encoding error must still lead.
			`{"datasets":[{"name":"A","upstreams":[]},{"name":"B","upstreams":["A\udc00\ud800"]}]}`,
			`"upstreams"`, `index 0 of the dataset record at index 1`, "unpaired surrogate",
		},
		"corrupt name with no collision, matching content id": {
			`{"datasets":[{"name":"only` + "\xfe" + `","upstreams":[]}]}`,
			`"name"`, `dataset record at index 0`, "invalid UTF-8 bytes",
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			data := corruptSnapshotDocument(t, tc.graph)
			_, err := ParseSnapshot(data)
			if !errors.Is(err, ErrInvalidArgument) {
				t.Fatalf("err = %v, want ErrInvalidArgument", err)
			}
			msg := err.Error()
			if !strings.Contains(msg, tc.reason) {
				t.Errorf("err = %q, want reason %q", msg, tc.reason)
			}
			if !strings.Contains(msg, tc.field) {
				t.Errorf("err = %q, want it to name field %s", msg, tc.field)
			}
			if !strings.Contains(msg, tc.location) {
				t.Errorf("err = %q, want it to locate %s", msg, tc.location)
			}
			if !strings.Contains(msg, "embedded in the snapshot") {
				t.Errorf("err = %q, want it to name the graph embedded in the snapshot", msg)
			}
			if strings.Contains(msg, "content identifier") {
				t.Errorf("err = %q, encoding corruption must not be reported as an id mismatch", msg)
			}
		})
	}
}

// TestParseSnapshotContentIDMatchingReplacedGraphIsNotAccepted is the
// snapshot half of the collision bug: assert directly that the tampered
// document's declared id really is the replaced graph's digest, yet the
// snapshot is still rejected (so the id check alone would have let it
// through).
func TestParseSnapshotContentIDMatchingReplacedGraphIsNotAccepted(t *testing.T) {
	rawGraph := `{"datasets":[{"name":"down","upstreams":["源\ud800"]},{"name":"源�","upstreams":[]}]}`
	data := corruptSnapshotDocument(t, rawGraph)

	// Premise: without the raw-name gate the plain decode yields the replaced
	// graph and the declared id matches it exactly.
	var raw struct {
		ContentID string    `json:"contentId"`
		Graph     GraphFile `json:"graph"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		t.Fatalf("plain decode: %v", err)
	}
	adj, err := validateGraphStructureFromFile(raw.Graph.Datasets)
	if err != nil {
		t.Fatalf("replaced graph must be structurally valid for the premise to hold: %v", err)
	}
	canon, _ := canonicalGraph(adj)
	if raw.ContentID != computeContentID(canon) {
		t.Fatal("test premise broken: declared id does not match the replaced graph")
	}

	if _, err := ParseSnapshot(data); err == nil {
		t.Fatal("snapshot with a corrupt name and a content id matching the replaced graph was accepted")
	}
}

// TestParseSnapshotAcceptsGenuineUnicodeGraph: legal Unicode names in the
// embedded graph round-trip with the same content id, whether written
// directly or as escapes.
func TestParseSnapshotAcceptsGenuineUnicodeGraph(t *testing.T) {
	graphJSON := `{"datasets":[{"name":"源�","upstreams":[]},{"name":"ledger😀","upstreams":["源�"]}]}`
	data := corruptSnapshotDocument(t, graphJSON)
	snap, err := ParseSnapshot(data)
	if err != nil {
		t.Fatalf("legal Unicode snapshot rejected: %v", err)
	}
	if len(snap.Graph.Datasets) != 2 {
		t.Fatalf("got %d datasets, want 2", len(snap.Graph.Datasets))
	}
}

// TestParseSnapshotIgnoresCorruptStringsInUnknownFields: corrupt strings in
// the snapshot's own unknown fields and in unknown graph/record fields are
// not graph names and do not reject the snapshot.
func TestParseSnapshotIgnoresCorruptStringsInUnknownFields(t *testing.T) {
	validGraph := `{"datasets":[{"name":"A","upstreams":[]}]}`
	id := func() string {
		adj, err := validateGraphStructureFromFile([]GraphDataset{{Name: "A"}})
		if err != nil {
			t.Fatal(err)
		}
		canon, _ := canonicalGraph(adj)
		return computeContentID(canon)
	}()
	cases := map[string]string{
		"corrupt string in unknown snapshot field": `{"note":"a\ud800","formatVersion":1,"contentId":%q,"graph":` + validGraph + `}`,
		"corrupt bytes in unknown graph field":     `{"formatVersion":1,"contentId":%q,"graph":{"datasets":[{"name":"A","upstreams":[],"x":"v` + "\xff" + `"}],"meta":["\ud800"]}}`,
		"nested known-looking names under unknown": `{"formatVersion":1,"contentId":%q,"graph":{"datasets":[{"name":"A","upstreams":[]}],"also":{"datasets":[{"name":"\ud800"}]}}}`,
	}
	for name, tmpl := range cases {
		t.Run(name, func(t *testing.T) {
			doc := fmt.Sprintf(tmpl, id)
			if _, err := ParseSnapshot([]byte(doc)); err != nil {
				t.Fatalf("unknown-field strings must not be checked, got %v", err)
			}
		})
	}
}
