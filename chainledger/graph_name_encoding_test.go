package chainledger

import (
	"errors"
	"reflect"
	"strings"
	"testing"
)

// These tests pin the rule that every name a graph carries — each
// datasets[i].name and every datasets[i].upstreams[j], whether the graph
// stands alone or is embedded in a snapshot — must decode exactly as
// written. encoding/json silently rewrites invalid UTF-8 bytes and unpaired
// \u surrogate escapes to U+FFFD, so a corrupted upstream could resolve to a
// genuinely different dataset: an edge written as "源" plus a lone \uD800
// decodes as a reference to the root dataset really named "源�". The reader
// must refuse the whole input instead of guessing which dataset the file
// meant — even when no dataset name exists for the rewrite to collide with.

// corruptNameByteGraphCases is shared by the standalone-graph and
// embedded-snapshot tests: the same corrupt graph document must be rejected
// for the same field and zero-based position in both readers.
var corruptNameByteGraphCases = map[string]struct {
	data     string
	field    string
	location string
}{
	"invalid bytes in dataset name": {
		`{"datasets":[{"name":"源` + "\xff" + `","upstreams":[]}]}`,
		`"name"`, `index 0 of "datasets"`,
	},
	"invalid bytes in second dataset name": {
		`{"datasets":[{"name":"A","upstreams":[]},{"name":"b` + "\xfe" + `","upstreams":[]}]}`,
		`"name"`, `index 1 of "datasets"`,
	},
	"invalid bytes in upstream entry": {
		`{"datasets":[{"name":"A","upstreams":[]},{"name":"B","upstreams":["A","g` + "\xfe" + `"]}]}`,
		`"upstreams"`, `index 1 in the dataset record at index 1 of "datasets"`,
	},
	"invalid bytes in first dataset name of a lone record": {
		`{"datasets":[{"name":"r` + "\xc3" + `","upstreams":[]}]}`,
		`"name"`, `index 0 of "datasets"`,
	},
	"invalid bytes with no name they could collide with": {
		// "ghost" exists; "ghost" plus one broken byte decodes to "ghost�",
		// which names nothing — the reader still must not treat it as a skip.
		`{"datasets":[{"name":"ghost","upstreams":[]},{"name":"d","upstreams":["ghost` + "\xff" + `"]}]}`,
		`"upstreams"`, `index 0 in the dataset record at index 1 of "datasets"`,
	},
	"case-folded known fields are still checked": {
		`{"DATASETS":[{"NAME":"a` + "\xff" + `","UPSTREAMS":[]}]}`,
		`"name"`, `index 0 of "datasets"`,
	},
	"escaped key spelling is still checked": {
		// "datasets" and "name" are spelled with JSON escapes
		// (a = 'a', n = 'n'); the corrupt name must still be found.
		`{"dat` + "\\u0061" + `sets":[{"n` + "\\u0061" + `me":"a` + "\xff" + `","upstreams":[]}]}`,
		`"name"`, `index 0 of "datasets"`,
	},
}

func assertCorruptNameError(t *testing.T, err error, field, location, reason string) {
	t.Helper()
	if !errors.Is(err, ErrInvalidArgument) {
		t.Fatalf("err = %v, want ErrInvalidArgument", err)
	}
	msg := err.Error()
	if !strings.Contains(msg, reason) {
		t.Errorf("err = %q, want it to say %q", msg, reason)
	}
	if reason == "invalid UTF-8 bytes" && strings.Contains(msg, "surrogate") {
		t.Errorf("err = %q, byte corruption must not be reported as a surrogate problem", msg)
	}
	if reason == "unpaired surrogate" && strings.Contains(msg, "invalid UTF-8 bytes") {
		t.Errorf("err = %q, a surrogate problem must not be reported as invalid bytes", msg)
	}
	if !strings.Contains(msg, field) {
		t.Errorf("err = %q, want it to name field %s", msg, field)
	}
	if !strings.Contains(msg, location) {
		t.Errorf("err = %q, want it to locate %s", msg, location)
	}
	if strings.Contains(msg, "�") {
		t.Errorf("err = %q, must not contain the replacement character", msg)
	}
}

// TestUnmarshalGraphFileRejectsCorruptNameBytes: a raw name or upstream
// literal carrying bytes that are not valid UTF-8 rejects the whole graph
// file. The error names the field and the zero-based record (and upstream
// array) position and says the bytes are invalid UTF-8.
func TestUnmarshalGraphFileRejectsCorruptNameBytes(t *testing.T) {
	for name, tc := range corruptNameByteGraphCases {
		t.Run(name, func(t *testing.T) {
			_, err := UnmarshalGraphFile([]byte(tc.data))
			assertCorruptNameError(t, err, tc.field, tc.location, "invalid UTF-8 bytes")
		})
	}
}

// TestUnmarshalGraphFileRejectsUnpairedSurrogates: a \u escape forming an
// unpaired surrogate — a lone high or low surrogate, a low surrogate before
// its high surrogate, or a high surrogate not immediately followed by its
// low surrogate — rejects the whole graph in every name position.
func TestUnmarshalGraphFileRejectsUnpairedSurrogates(t *testing.T) {
	cases := map[string]struct {
		data     string
		field    string
		location string
	}{
		"lone high surrogate in name": {
			`{"datasets":[{"name":"a\ud800x","upstreams":[]}]}`,
			`"name"`, `index 0 of "datasets"`,
		},
		"high surrogate at end of name": {
			`{"datasets":[{"name":"a\ud800","upstreams":[]}]}`,
			`"name"`, `index 0 of "datasets"`,
		},
		"lone low surrogate in name": {
			`{"datasets":[{"name":"a\udc00","upstreams":[]}]}`,
			`"name"`, `index 0 of "datasets"`,
		},
		"low surrogate before high surrogate": {
			`{"datasets":[{"name":"a\udc00\ud800","upstreams":[]}]}`,
			`"name"`, `index 0 of "datasets"`,
		},
		"high surrogate then non-surrogate character": {
			`{"datasets":[{"name":"a\ud800A","upstreams":[]}]}`,
			`"name"`, `index 0 of "datasets"`,
		},
		"high surrogate then another high surrogate": {
			`{"datasets":[{"name":"a\ud800\ud801","upstreams":[]}]}`,
			`"name"`, `index 0 of "datasets"`,
		},
		"surrogate in second dataset name": {
			`{"datasets":[{"name":"A","upstreams":[]},{"name":"\ud800","upstreams":[]}]}`,
			`"name"`, `index 1 of "datasets"`,
		},
		"lone high surrogate in upstream entry": {
			`{"datasets":[{"name":"A","upstreams":[]},{"name":"B","upstreams":["ok","\udfff"]}]}`,
			`"upstreams"`, `index 1 in the dataset record at index 1 of "datasets"`,
		},
		"lone low surrogate in upstream entry": {
			`{"datasets":[{"name":"A","upstreams":[]},{"name":"B","upstreams":["\udc00"]}]}`,
			`"upstreams"`, `index 0 in the dataset record at index 1 of "datasets"`,
		},
		"low-before-high pair in upstream entry": {
			`{"datasets":[{"name":"A","upstreams":[]},{"name":"B","upstreams":["A\udc00\ud83d"]}]}`,
			`"upstreams"`, `index 0 in the dataset record at index 1 of "datasets"`,
		},
		"corrupt upstream with no dataset it could collide with": {
			`{"datasets":[{"name":"A","upstreams":[]},{"name":"B","upstreams":["ghost\ud800"]}]}`,
			`"upstreams"`, `index 0 in the dataset record at index 1 of "datasets"`,
		},
		"case-folded field still checked": {
			`{"DataSets":[{"Name":"\ud800","Upstreams":[]}]}`,
			`"name"`, `index 0 of "datasets"`,
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := UnmarshalGraphFile([]byte(tc.data))
			assertCorruptNameError(t, err, tc.field, tc.location, "unpaired surrogate")
		})
	}
}

// TestUnmarshalGraphFileCorruptNameRejectsWholeGraph: one corrupt name
// refuses the whole graph — legal sibling records never survive on their
// own.
func TestUnmarshalGraphFileCorruptNameRejectsWholeGraph(t *testing.T) {
	data := `{"datasets":[{"name":"A","upstreams":[]},{"name":"b` + "\xff" + `","upstreams":[]}]}`
	if _, err := UnmarshalGraphFile([]byte(data)); err == nil {
		t.Fatal("a graph mixing legal records with one corrupt name must be rejected as a whole")
	}
}

// TestUnmarshalGraphFileCorruptUpstreamCannotCollideWithRealDataset is the
// headline regression: the graph already has a root dataset genuinely named
// "源�"; another dataset's direct upstream is written as "源" plus a lone
// surrogate escape (or a broken byte). The decoder would rewrite the
// reference to "源�" and the edge would resolve; the reader must refuse.
func TestUnmarshalGraphFileCorruptUpstreamCannotCollideWithRealDataset(t *testing.T) {
	for _, edge := range []string{
		`"源\uD800"`,       // lone high surrogate
		`"源\uDC00"`,       // lone low surrogate
		`"源\uDC00\uD83D"`, // low surrogate written before its high surrogate
	} {
		t.Run(edge, func(t *testing.T) {
			data := `{"datasets":[{"name":"源�","upstreams":[]},{"name":"d","upstreams":[` + edge + `]}]}`
			_, err := UnmarshalGraphFile([]byte(data))
			if err == nil {
				t.Fatal("corrupt upstream must not be read as a reference to the genuine 源� dataset")
			}
			msg := err.Error()
			if !strings.Contains(msg, "upstreams") || !strings.Contains(msg, "index 1 of \"datasets\"") {
				t.Errorf("err = %v, must name the upstreams field and the second dataset record", err)
			}
		})
	}
	data := `{"datasets":[{"name":"源�","upstreams":[]},{"name":"d","upstreams":["源` + "\xff" + `"]}]}`
	if _, err := UnmarshalGraphFile([]byte(data)); err == nil {
		t.Fatal("a raw invalid-byte upstream must not be read as a reference to 源�")
	}
}

// TestUnmarshalGraphFileAcceptsGenuineUnicodeNames: names that are legal
// stay byte-for-byte intact — a genuine replacement character (written
// directly or as its escape), correctly paired surrogate escapes, CJK,
// emoji, combining marks, casing, surrounding spaces, and names that merely
// look like escapes (a backslash before an ordinary letter). Equivalent
// spellings resolve to the same dataset; no normalization is applied.
func TestUnmarshalGraphFileAcceptsGenuineUnicodeNames(t *testing.T) {
	t.Run("genuine names decode verbatim", func(t *testing.T) {
		cases := map[string]struct {
			data string
			want string
		}{
			"genuine replacement character": {
				`{"datasets":[{"name":"源�","upstreams":[]}]}`,
				"源�",
			},
			"replacement character via escape": {
				// 源 written directly, the genuine U+FFFD written as its JSON escape.
				`{"datasets":[{"name":"源` + "\\ufffd" + `","upstreams":[]}]}`,
				"源�",
			},
			"paired surrogate escape": {
				`{"datasets":[{"name":"ledger` + "\\ud83d\\ude00" + `","upstreams":[]}]}`,
				"ledger😀",
			},
			"paired surrogate at string start and end": {
				`{"datasets":[{"name":"` + "\\ud83d\\ude00x\\ud83c\\udf10" + `","upstreams":[]}]}`,
				"😀x🌐",
			},
			"cjk combining marks casing and spaces": {
				`{"datasets":[{"name":" 数据 Éé ","upstreams":[]}]}`,
				" 数据 Éé ",
			},
			"backslash before ordinary letter": {
				`{"datasets":[{"name":"a\\nb","upstreams":[]}]}`,
				`a\nb`,
			},
			"backslash before what looks like a surrogate escape": {
				// The name really contains backslash, 'u', 'd', '8', '0', '0'.
				`{"datasets":[{"name":"x\\ud800","upstreams":[]}]}`,
				`x\ud800`,
			},
		}
		for name, tc := range cases {
			t.Run(name, func(t *testing.T) {
				graph, err := UnmarshalGraphFile([]byte(tc.data))
				if err != nil {
					t.Fatalf("UnmarshalGraphFile rejected a legal name: %v", err)
				}
				entry, ok := graph[tc.want]
				if !ok {
					t.Fatalf("graph has no dataset %q; datasets %v", tc.want, graph)
				}
				if entry.Dataset != tc.want {
					t.Errorf("dataset = %q, want %q kept verbatim", entry.Dataset, tc.want)
				}
			})
		}
	})

	t.Run("equivalent escape spellings are the same edge", func(t *testing.T) {
		// Direct text versus the same names written with JSON escapes
		// (源 = 源, U+FFFD = �).
		direct := `{"datasets":[{"name":"源�","upstreams":[]},{"name":"d","upstreams":["源�"]}]}`
		escaped := `{"datasets":[{"name":"` + "\\u6e90\\ufffd" + `","upstreams":[]},{"name":"d","upstreams":["` + "\\u6e90\\ufffd" + `"]}]}`
		if direct == escaped {
			t.Fatal("test premise broken: the two spellings must differ as text")
		}
		gDirect, err := UnmarshalGraphFile([]byte(direct))
		if err != nil {
			t.Fatalf("direct spelling rejected: %v", err)
		}
		gEscaped, err := UnmarshalGraphFile([]byte(escaped))
		if err != nil {
			t.Fatalf("escaped spelling rejected: %v", err)
		}
		if got := gEscaped["d"].Parents; !reflect.DeepEqual(got, []string{"源�"}) {
			t.Errorf("escaped edge decoded to %v, want [源�]", got)
		}
		if !reflect.DeepEqual(gDirect["d"].Parents, gEscaped["d"].Parents) {
			t.Errorf("equivalent spellings produced different edges: %v vs %v",
				gDirect["d"].Parents, gEscaped["d"].Parents)
		}
	})

	t.Run("case spaces and combining marks are not normalized", func(t *testing.T) {
		// "A", " a ", "a", "é" (single codepoint) and "é" (e plus
		// combining acute) are five different names; none may merge.
		data := `{"datasets":[{"name":"A","upstreams":[]},{"name":" a ","upstreams":["A"]},{"name":"a","upstreams":["A"]},{"name":"é","upstreams":["A"]},{"name":"é","upstreams":["A"]}]}`
		graph, err := UnmarshalGraphFile([]byte(data))
		if err != nil {
			t.Fatalf("distinct-looking names must stay distinct datasets: %v", err)
		}
		if len(graph) != 5 {
			t.Errorf("graph merged distinct names: got %d datasets, want 5", len(graph))
		}
	})
}

// TestUnmarshalGraphFileIgnoresCorruptStringsInUnknownFields: unknown
// fields and everything nested inside them keep their ignore-everything
// behavior — their strings never name a dataset and are not checked, even
// when they spell a known field name one level down.
func TestUnmarshalGraphFileIgnoresCorruptStringsInUnknownFields(t *testing.T) {
	cases := map[string]string{
		"corrupt string in unknown top-level field": `{"meta":"a\ud800","datasets":[{"name":"A","upstreams":[]}]}`,
		"corrupt bytes in unknown record field":     `{"datasets":[{"name":"A","note":"x` + "\xff" + `","upstreams":[]}]}`,
		"known field names nested inside unknown field": `{"also":{"datasets":[{"name":"\ud800","upstreams":["g` + "\xff" + `"]}]},` +
			`"datasets":[{"name":"A","upstreams":[]}]}`,
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

// snapshotEnvelope wraps a raw graph object in a version-1 snapshot
// envelope with the given declared content id.
func snapshotEnvelope(contentID string, graphJSON string) []byte {
	return []byte(`{"formatVersion":1,"contentId":"` + contentID + `","graph":` + graphJSON + `}`)
}

// TestParseSnapshotRejectsCorruptGraphNames applies the same corrupt-graph
// cases to the graph embedded in a snapshot: the whole snapshot is refused
// before the content identifier is consulted, with identical field and
// position wording as a standalone graph file.
func TestParseSnapshotRejectsCorruptGraphNames(t *testing.T) {
	const bogusID = "sha256:0000000000000000000000000000000000000000000000000000000000000000"
	for name, tc := range corruptNameByteGraphCases {
		t.Run("bytes/"+name, func(t *testing.T) {
			_, err := ParseSnapshot(snapshotEnvelope(bogusID, tc.data))
			assertCorruptNameError(t, err, tc.field, tc.location, "invalid UTF-8 bytes")
		})
	}
	surrogates := map[string]struct {
		data     string
		field    string
		location string
	}{
		"lone high surrogate in name": {
			`{"datasets":[{"name":"a\ud800","upstreams":[]}]}`,
			`"name"`, `index 0 of "datasets"`,
		},
		"lone low surrogate in name": {
			`{"datasets":[{"name":"a\udc00","upstreams":[]}]}`,
			`"name"`, `index 0 of "datasets"`,
		},
		"low before high in name": {
			`{"datasets":[{"name":"a\udc00\ud800","upstreams":[]}]}`,
			`"name"`, `index 0 of "datasets"`,
		},
		"lone surrogate in upstream entry": {
			`{"datasets":[{"name":"A","upstreams":[]},{"name":"B","upstreams":["A","\ud800"]}]}`,
			`"upstreams"`, `index 1 in the dataset record at index 1 of "datasets"`,
		},
	}
	for name, tc := range surrogates {
		t.Run("surrogate/"+name, func(t *testing.T) {
			_, err := ParseSnapshot(snapshotEnvelope(bogusID, tc.data))
			assertCorruptNameError(t, err, tc.field, tc.location, "unpaired surrogate")
		})
	}
}

// TestParseSnapshotCorruptNameRejectedEvenWhenContentIDMatches: even a
// snapshot whose declared content identifier is the genuine identifier of
// the graph AS REWRITTEN by the decoder (the root genuinely named "源�" and
// an edge the rewrite makes resolve to it) must be refused for the corrupt
// name, rather than accepted because the digests line up.
func TestParseSnapshotCorruptNameRejectedEvenWhenContentIDMatches(t *testing.T) {
	// Build the product-computed identifier of the post-rewrite graph.
	rewritten := `{"datasets":[{"name":"源�","upstreams":[]},{"name":"d","upstreams":["源�"]}]}`
	graph, err := UnmarshalGraphFile([]byte(rewritten))
	if err != nil {
		t.Fatalf("UnmarshalGraphFile rewritten: %v", err)
	}
	snap, err := BuildSnapshot(graph)
	if err != nil {
		t.Fatalf("BuildSnapshot: %v", err)
	}
	corruptGraph := `{"datasets":[{"name":"源�","upstreams":[]},{"name":"d","upstreams":["源\uD800"]}]}`
	if _, err := ParseSnapshot(snapshotEnvelope(snap.ContentID, corruptGraph)); err == nil {
		t.Fatal("snapshot accepted a corrupt-name graph whose rewritten digest matches the declared id")
	}
	// The equivalent clean document parses under that same identifier,
	// proving the identifier really did match the rewritten graph.
	valid, err := ParseSnapshot(snapshotEnvelope(snap.ContentID, rewritten))
	if err != nil {
		t.Fatalf("the equivalent clean snapshot must still parse: %v", err)
	}
	if valid.ContentID != snap.ContentID {
		t.Errorf("content id = %q, want %q", valid.ContentID, snap.ContentID)
	}
}

// TestParseSnapshotAcceptsGenuineUnicodeGraphNames: a frozen graph keeps
// genuine replacement characters, paired surrogate escapes, CJK and emoji
// names, and corrupt strings confined to unknown fields do not count.
func TestParseSnapshotAcceptsGenuineUnicodeGraphNames(t *testing.T) {
	graphJSON := `{"datasets":[{"name":"源�","upstreams":[]},{"name":"ledger😀","upstreams":["源�"]}]}`
	graph, err := UnmarshalGraphFile([]byte(graphJSON))
	if err != nil {
		t.Fatalf("UnmarshalGraphFile: %v", err)
	}
	snap, err := BuildSnapshot(graph)
	if err != nil {
		t.Fatalf("BuildSnapshot: %v", err)
	}
	data, err := MarshalSnapshot(snap)
	if err != nil {
		t.Fatalf("MarshalSnapshot: %v", err)
	}
	parsed, err := ParseSnapshot(data)
	if err != nil {
		t.Fatalf("ParseSnapshot rejected legal Unicode graph names: %v", err)
	}
	var foundEmoji bool
	for _, ds := range parsed.Graph.Datasets {
		if ds.Name == "ledger😀" {
			foundEmoji = true
			if !reflect.DeepEqual(ds.Upstreams, []string{"源�"}) {
				t.Errorf("upstreams = %v, want [源�]", ds.Upstreams)
			}
		}
	}
	if !foundEmoji {
		t.Errorf("snapshot lost the emoji dataset: %+v", parsed.Graph.Datasets)
	}

	// Corrupt strings confined to unknown fields do not reject the snapshot.
	legalWithUnknown := []byte(`{"formatVersion":1,"contentId":"` + snap.ContentID + `","note":"a\ud800",` +
		`"graph":{"meta":["x` + "\xff" + `"],"datasets":[{"name":"源�","upstreams":[]},{"name":"ledger😀","upstreams":["源�"]}]}}`)
	if _, err := ParseSnapshot(legalWithUnknown); err != nil {
		t.Errorf("unknown-field strings must not trigger the name check: %v", err)
	}
}
