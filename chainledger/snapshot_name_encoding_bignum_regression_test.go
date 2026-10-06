package chainledger

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

// Regression coverage for reading frozen snapshots that carry additional
// information in unknown fields. A legal JSON number outside the float64
// range (1e400) — syntactically valid but too large for an ordinary float —
// may sit at the snapshot top level, inside the embedded graph, inside a
// dataset record's own unknown fields, or nested in unknown objects and
// arrays. Such content carries no lineage: it must never make the raw
// name-encoding scan abort and thereby mask a corrupt name or upstream that
// appears later in the same document, and it must never change the content
// identifier or the result of a source trace.

// assertSnapshotNameCorruption rejects one snapshot document with the
// graph name-encoding error for the given field/zero-based position and
// corruption kind — and with none of the errors a masked scan could surface
// instead (a float-range parse error, a content-identifier mismatch, or a
// missing-upstream error reached after the decoder's U+FFFD rewrite).
func assertSnapshotNameCorruption(t *testing.T, data, field, location, reason string) {
	t.Helper()
	_, err := ParseSnapshot([]byte(data))
	if !errors.Is(err, ErrInvalidArgument) {
		t.Fatalf("ParseSnapshot err = %v, want ErrInvalidArgument naming the corrupt graph name", err)
	}
	assertCorruptNameError(t, err, field, location, reason)
	msg := err.Error()
	for _, masked := range []string{
		"invalid snapshot JSON",
		"cannot unmarshal number",
		"does not match the graph",
		"does not exist in the final graph",
	} {
		if strings.Contains(msg, masked) {
			t.Errorf("err = %q, the name-encoding defect was masked by %q", msg, masked)
		}
	}
}

// decoratedSnapshot wraps one graph object in a version-1 snapshot envelope
// with the given declared content id and one placement of ignored
// oversized-number content.
func decoratedSnapshot(contentID, graphJSON, placement string) string {
	envelope := `{"formatVersion":1,"contentId":"` + contentID + `","graph":` + graphJSON + `}`
	switch placement {
	case "plain":
		return envelope
	case "big number at the top level":
		return `{"note":1e400,` + envelope[1:]
	case "signed big numbers nested at the top level":
		return `{"meta":{"x":[1e400,{"y":1e999}]},"bag":[-1e400,2e999],` + envelope[1:]
	case "big number in an unknown graph field":
		// Inject an unknown key carrying big numbers into the graph object.
		return `{"formatVersion":1,"contentId":"` + contentID +
			`","graph":{"meta":[1e999,{"z":-1e400}],` + graphJSON[1:] + `}`
	case "big numbers inside every dataset record":
		// Prefix every dataset record with an unknown field of its own.
		records := strings.ReplaceAll(graphJSON, `{"name"`, `{"vals":[1e999,{"z":-1e400}],"name"`)
		return `{"formatVersion":1,"contentId":"` + contentID + `","graph":` + records + `}`
	default:
		panic("unknown placement " + placement)
	}
}

// TestParseSnapshotBigNumberCannotMaskCorruptGraphNames drives every
// placement of ignored oversized-number content against every corrupt
// business-name position (a dataset name and an upstream, at the first and
// second zero-based record positions, with invalid bytes and with unpaired
// surrogate escapes). The snapshot must always be refused for exactly that
// name defect — never for the number, a missing upstream, or the declared
// identifier — and the error must name the field, record index, and (for an
// upstream) array index.
func TestParseSnapshotBigNumberCannotMaskCorruptGraphNames(t *testing.T) {
	const bogusID = "sha256:0000000000000000000000000000000000000000000000000000000000000000"
	placements := []string{
		"plain",
		"big number at the top level",
		"signed big numbers nested at the top level",
		"big number in an unknown graph field",
		"big numbers inside every dataset record",
	}
	cases := map[string]struct {
		graph    string
		field    string
		location string
		reason   string
	}{
		"invalid bytes in first dataset name": {
			`{"datasets":[{"name":"a` + "\xff" + `","upstreams":[]}]}`,
			`"name"`, `index 0 of "datasets"`, "invalid UTF-8 bytes",
		},
		"unpaired surrogate in first dataset name": {
			`{"datasets":[{"name":"a\ud800","upstreams":[]}]}`,
			`"name"`, `index 0 of "datasets"`, "unpaired surrogate",
		},
		"lone low surrogate in first dataset name": {
			`{"datasets":[{"name":"\udc00","upstreams":[]}]}`,
			`"name"`, `index 0 of "datasets"`, "unpaired surrogate",
		},
		"invalid bytes in second dataset name": {
			`{"datasets":[{"name":"A","upstreams":[]},{"name":"b` + "\xfe" + `","upstreams":[]}]}`,
			`"name"`, `index 1 of "datasets"`, "invalid UTF-8 bytes",
		},
		"surrogate in second dataset name": {
			`{"datasets":[{"name":"A","upstreams":[]},{"name":"b\ud800","upstreams":[]}]}`,
			`"name"`, `index 1 of "datasets"`, "unpaired surrogate",
		},
		"invalid bytes in second upstream of second record": {
			`{"datasets":[{"name":"A","upstreams":[]},{"name":"B","upstreams":["A","g` + "\xfe" + `"]}]}`,
			`"upstreams"`, `index 1 in the dataset record at index 1 of "datasets"`, "invalid UTF-8 bytes",
		},
		"lone high surrogate upstream": {
			`{"datasets":[{"name":"A","upstreams":[]},{"name":"B","upstreams":["\ud800"]}]}`,
			`"upstreams"`, `index 0 in the dataset record at index 1 of "datasets"`, "unpaired surrogate",
		},
		"low surrogate written before its high surrogate upstream": {
			`{"datasets":[{"name":"A","upstreams":[]},{"name":"B","upstreams":["A\udc00\ud83d"]}]}`,
			`"upstreams"`, `index 0 in the dataset record at index 1 of "datasets"`, "unpaired surrogate",
		},
	}

	for _, placement := range placements {
		for caseName, tc := range cases {
			t.Run(placement+" / "+caseName, func(t *testing.T) {
				data := decoratedSnapshot(bogusID, tc.graph, placement)
				assertSnapshotNameCorruption(t, data, tc.field, tc.location, tc.reason)
			})
		}
	}
}

// TestParseSnapshotBigNumberCorruptUpstreamCannotCollideWithRealRoot is the
// source-misidentification regression specific to frozen snapshots: the
// embedded graph really contains a root dataset named "源�", and another
// dataset's direct upstream is written as "源" plus a broken byte or a lone
// surrogate escape. Even when (a) replacing the corruption with U+FFFD would
// make the edge resolve to the genuine root, (b) the snapshot's declared
// content identifier is exactly the digest of that rewritten graph, and (c)
// the document additionally carries legal 1e400 values everywhere unknown
// fields are allowed, the snapshot must be refused for the name encoding —
// never accepted, and never refused only because the identifier mismatches
// or the upstream does not exist. A corrupted reference with no root it
// could collide with is refused on exactly the same terms.
func TestParseSnapshotBigNumberCorruptUpstreamCannotCollideWithRealRoot(t *testing.T) {
	// Content identifier of the graph the decoder would rewrite the corrupt
	// reference into: root 源� with d -> 源�.
	rewritten := `{"datasets":[{"name":"源�","upstreams":[]},{"name":"d","upstreams":["源�"]}]}`
	id := snapshotOf(t, rewritten).ContentID

	corruptGraphs := map[string]struct {
		graph  string
		reason string
	}{
		"lone high surrogate would resolve to the real root": {
			`{"datasets":[{"name":"源�","upstreams":[]},{"name":"d","upstreams":["源\uD800"]}]}`,
			"unpaired surrogate",
		},
		"lone low surrogate would resolve to the real root": {
			`{"datasets":[{"name":"源�","upstreams":[]},{"name":"d","upstreams":["源\uDC00"]}]}`,
			"unpaired surrogate",
		},
		"low surrogate before its high would resolve to the real root": {
			`{"datasets":[{"name":"源�","upstreams":[]},{"name":"d","upstreams":["源\uDC00\uD83D"]}]}`,
			"unpaired surrogate",
		},
		"broken byte would resolve to the real root": {
			`{"datasets":[{"name":"源�","upstreams":[]},{"name":"d","upstreams":["源` + "\xff" + `"]}]}`,
			"invalid UTF-8 bytes",
		},
		"lone surrogate names no dataset at all": {
			`{"datasets":[{"name":"A","upstreams":[]},{"name":"d","upstreams":["ghost\ud800"]}]}`,
			"unpaired surrogate",
		},
		"broken byte names no dataset at all": {
			`{"datasets":[{"name":"A","upstreams":[]},{"name":"d","upstreams":["ghost` + "\xfe" + `"]}]}`,
			"invalid UTF-8 bytes",
		},
	}

	const location = `index 0 in the dataset record at index 1 of "datasets"`
	for name, tc := range corruptGraphs {
		t.Run(name, func(t *testing.T) {
			for _, placement := range []string{
				"big number at the top level",
				"big number in an unknown graph field",
				"big numbers inside every dataset record",
			} {
				t.Run(placement, func(t *testing.T) {
					data := decoratedSnapshot(id, tc.graph, placement)
					assertSnapshotNameCorruption(t, data, `"upstreams"`, location, tc.reason)
				})
			}
		})
	}

	// Control: the same declared identifier genuinely belongs to the clean
	// rewritten document, so the clean document decorated with the same big
	// numbers still parses — proving the refusal above is the encoding, not
	// the identifier or the numbers.
	for _, placement := range []string{
		"plain",
		"big number at the top level",
		"big number in an unknown graph field",
		"big numbers inside every dataset record",
	} {
		t.Run("clean graph accepted / "+placement, func(t *testing.T) {
			snap, err := ParseSnapshot([]byte(decoratedSnapshot(id, rewritten, placement)))
			if err != nil {
				t.Fatalf("the equivalent clean snapshot must parse with the same id: %v", err)
			}
			if snap.ContentID != id {
				t.Errorf("content id = %q, want %q", snap.ContentID, id)
			}
		})
	}
}

// TestParseSnapshotBigNumberCorruptDatasetNameRejectedWithoutCollision pins
// the record-name half of the same rule where the corruption could not
// possibly resolve to anything: the snapshot is still rejected for the name
// rather than read as a different graph.
func TestParseSnapshotBigNumberCorruptDatasetNameRejectedWithoutCollision(t *testing.T) {
	id := snapshotOf(t, `{"datasets":[]}`).ContentID
	graph := `{"datasets":[{"name":"ghost` + "\xff" + `","upstreams":[]}]}`
	for _, placement := range []string{
		"big number at the top level",
		"signed big numbers nested at the top level",
		"big number in an unknown graph field",
		"big numbers inside every dataset record",
	} {
		t.Run(placement, func(t *testing.T) {
			assertSnapshotNameCorruption(t,
				decoratedSnapshot(id, graph, placement),
				`"name"`, `index 0 of "datasets"`, "invalid UTF-8 bytes")
		})
	}
}

// TestParseSnapshotGenuineUnicodeNamesWithBigNumbers are the legal-name
// counterparts: a real U+FFFD written directly or as its equivalent escape,
// a correctly paired surrogate escape, CJK text, and a name that merely
// contains a literal backslash followed by ordinary letters all read
// verbatim, while the same ignored 1e400 content is present. Direct and
// equivalent-escape spellings produce the same frozen graph and hence the
// same root sources and paths.
func TestParseSnapshotGenuineUnicodeNamesWithBigNumbers(t *testing.T) {
	graph := `{"datasets":[{"name":"源�","upstreams":[]},{"name":"ledger😀","upstreams":["源�"]}]}`
	id := snapshotOf(t, graph).ContentID

	// "源" written directly with the genuine U+FFFD spelled as its escape.
	escapedGraph := `{"datasets":[{"name":"` + "\\u6e90\\ufffd" +
		`","upstreams":[]},{"name":"ledger` + "\\ud83d\\ude00" +
		`","upstreams":["` + "\\u6e90\\ufffd" + `"]}]}`
	if escapedGraph == graph {
		t.Fatal("test premise broken: the two spellings must differ on disk")
	}

	// A name that genuinely contains backslash, 'u', 'd', '8', '0', '0': the
	// doubled backslash is plain text, not a surrogate escape.
	literalGraph := `{"datasets":[{"name":"ledger` + "\\ud83d\\ude00" +
		`","upstreams":[]},{"name":"x\\ud800","upstreams":["ledger` +
		"\\ud83d\\ude00" + `"]}]}`

	var baseReport []byte
	for _, tc := range []struct {
		name  string
		graph string
	}{
		{"direct", graph},
		{"equivalent escapes", escapedGraph},
	} {
		t.Run(tc.name, func(t *testing.T) {
			for _, placement := range []string{
				"plain",
				"big number at the top level",
				"big number in an unknown graph field",
				"big numbers inside every dataset record",
			} {
				t.Run(placement, func(t *testing.T) {
					snap, err := ParseSnapshot([]byte(decoratedSnapshot(id, tc.graph, placement)))
					if err != nil {
						t.Fatalf("ParseSnapshot rejected a legal Unicode graph: %v", err)
					}
					if snap.ContentID != id {
						t.Errorf("content id = %q, want %q; unknown numbers must not change identity", snap.ContentID, id)
					}
					report, err := TraceSources(snap, "ledger😀")
					if err != nil {
						t.Fatalf("TraceSources: %v", err)
					}
					out, err := json.Marshal(report)
					if err != nil {
						t.Fatalf("marshal report: %v", err)
					}
					if baseReport == nil {
						baseReport = out
					} else if string(out) != string(baseReport) {
						t.Errorf("trace report differs from the first legal spelling:\n%s\n%s", out, baseReport)
					}
				})
			}
		})
	}

	t.Run("literal backslash name is not an escape", func(t *testing.T) {
		literalID := snapshotOf(t, literalGraph).ContentID
		snap, err := ParseSnapshot([]byte(decoratedSnapshot(literalID, literalGraph,
			"big numbers inside every dataset record")))
		if err != nil {
			t.Fatalf("a literal backslash name must stay legal: %v", err)
		}
		report, err := TraceSources(snap, `x\ud800`)
		if err != nil {
			t.Fatalf("the literal-backslash dataset must be traceable: %v", err)
		}
		if len(report.Sources) != 1 || report.Sources[0].Root != "ledger😀" {
			t.Fatalf("sources = %+v, want the single root ledger😀", report.Sources)
		}
		want := []string{`x\ud800`, "ledger😀"}
		if paths := report.Sources[0].Path; !stringSliceEqual(paths, want) {
			t.Errorf("path = %v, want %v kept verbatim", paths, want)
		}
	})
}

// TestTraceSourcesBigNumberUnknownFieldsDoNotChangeReport: for a fully legal
// graph, adding ignored oversized-number content at every allowed place
// changes neither the parsed snapshot's content identifier nor a single byte
// of the trace report for the same queried dataset.
func TestTraceSourcesBigNumberUnknownFieldsDoNotChangeReport(t *testing.T) {
	graph := traceGraphJSON()
	id := snapshotOf(t, graph).ContentID
	queries := []string{"T", "A", "R"}

	// Base reports come from the plain document: one marshaled report per
	// query, computed through the same ParseSnapshot + TraceSources path.
	plain, err := ParseSnapshot([]byte(decoratedSnapshot(id, graph, "plain")))
	if err != nil {
		t.Fatalf("ParseSnapshot plain: %v", err)
	}
	base := make(map[string][]byte, len(queries))
	for _, query := range queries {
		report, err := TraceSources(plain, query)
		if err != nil {
			t.Fatalf("TraceSources plain %q: %v", query, err)
		}
		out, err := json.Marshal(report)
		if err != nil {
			t.Fatalf("marshal report: %v", err)
		}
		base[query] = out
	}

	for _, placement := range []string{
		"big number at the top level",
		"signed big numbers nested at the top level",
		"big number in an unknown graph field",
		"big numbers inside every dataset record",
	} {
		t.Run(placement, func(t *testing.T) {
			snap, err := ParseSnapshot([]byte(decoratedSnapshot(id, graph, placement)))
			if err != nil {
				t.Fatalf("ParseSnapshot: %v", err)
			}
			if snap.ContentID != id {
				t.Errorf("content id = %q, want %q", snap.ContentID, id)
			}
			for _, query := range queries {
				report, err := TraceSources(snap, query)
				if err != nil {
					t.Fatalf("TraceSources %q: %v", query, err)
				}
				out, err := json.Marshal(report)
				if err != nil {
					t.Fatalf("marshal report: %v", err)
				}
				if string(out) != string(base[query]) {
					t.Errorf("trace %q with %s differs:\n got %s\nwant %s", query, placement, out, base[query])
				}
			}
		})
	}
}

// traceGraphJSON is the core-package copy of the diamond the CLI trace tests
// use: T reaches roots R (directly and via A and B) and Z (via A).
func traceGraphJSON() string {
	return `{"datasets":[
		{"name":"T","upstreams":["R","B","A"]},
		{"name":"A","upstreams":["R","Z"]},
		{"name":"B","upstreams":["R"]},
		{"name":"R","upstreams":[]},
		{"name":"Z","upstreams":[]},
		{"name":"S","upstreams":[]}
	]}`
}
