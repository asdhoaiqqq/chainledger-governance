package chainledger

import (
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
)

// Regression coverage for reading snapshots that carry additional
// information in fields the reader ignores. A legal JSON number outside the
// float64 range (1e400) — placed at the snapshot top level, inside the
// embedded graph or a dataset record's unknown fields, or buried in unknown
// objects and arrays at any depth — carries no lineage semantics and must not
// interfere with the raw name-encoding gate that runs over the frozen graph:
// a later name or upstream whose raw literal contains invalid UTF-8 bytes or
// an unpaired surrogate must still reject the whole snapshot with the same
// field, zero-based dataset-record (and upstream array) position, and the same
// byte-vs-surrogate wording, before the content identifier is ever compared.
//
// The trace query path inherits the guarantee because TraceSources only runs
// on a snapshot that passed ParseSnapshot: legal snapshots decorated with
// those same big numbers must produce the same content identifier and
// byte-identical root sources and paths, including for names that genuinely
// contain a U+FFFD or a backslash that merely looks like an escape.

// assertSnapshotNameCorruption parses data as a snapshot and requires the
// name-encoding rejection for field at location with reason ("invalid UTF-8
// bytes" or "unpaired surrogate") — and none of the failures a masked scan or
// a later validation stage could surface instead.
func assertSnapshotNameCorruption(t *testing.T, data, field, location, reason string) {
	t.Helper()
	_, err := ParseSnapshot([]byte(data))
	if err == nil {
		t.Fatalf("ParseSnapshot accepted a snapshot carrying a corrupt name")
	}
	if !errors.Is(err, ErrInvalidArgument) {
		t.Fatalf("err = %v, want ErrInvalidArgument naming the corrupt name", err)
	}
	msg := err.Error()
	for _, want := range []string{field, location, reason} {
		if !strings.Contains(msg, want) {
			t.Errorf("err = %q, want it to contain %q", msg, want)
		}
	}
	switch reason {
	case "invalid UTF-8 bytes":
		if strings.Contains(msg, "surrogate") {
			t.Errorf("err = %q, byte corruption must not read as a surrogate problem", msg)
		}
	case "unpaired surrogate":
		if strings.Contains(msg, "invalid UTF-8 bytes") {
			t.Errorf("err = %q, a surrogate problem must not read as invalid bytes", msg)
		}
	}
	// The rejection must be the name defect itself: a big number skipped with
	// float conversion (UseNumber) must not surface as a parse error, the gate
	// runs before the structural checks, and the identifier comparison never
	// happens.
	for _, masked := range []string{"invalid snapshot JSON", "cannot unmarshal number", "content identifier", "is not registered"} {
		if strings.Contains(msg, masked) {
			t.Errorf("err = %q, the name corruption was masked by %q", msg, masked)
		}
	}
	if strings.Contains(msg, "�") {
		t.Errorf("err = %q, must not contain the replacement character", msg)
	}
}

// snapshotBignumPlacements wraps one raw graph object (already carrying the
// corrupt name) in a version-1 snapshot envelope, placing oversized legal
// numbers in every kind of ignored position the reader must skip: a top-level
// unknown field before and after the known fields, unknown objects and arrays
// at the top level and inside the graph, and unknown fields of dataset
// records. The declared content id is deliberately bogus but non-empty; the
// name gate runs before the id comparison.
func snapshotBignumPlacements(contentID string, graphJSON string) map[string]string {
	envelope := func(graph string) string {
		return `{"formatVersion":1,"contentId":"` + contentID + `","graph":` + graph + `}`
	}
	return map[string]string{
		"plain": envelope(graphJSON),
		"big number opens the snapshot": `{"note":1e400,` +
			envelope(graphJSON)[1:],
		"signed big number before the graph": `{"note":-1e400,` +
			envelope(graphJSON)[1:],
		"big numbers nested in unknown objects and arrays": `{"meta":{"x":[1e400,{"y":1e999}]},"bag":[2e999,{"k":-1e400}],` +
			envelope(graphJSON)[1:],
		"big number after the graph": envelope(graphJSON)[:len(envelope(graphJSON))-1] +
			`,"tail":[1e400,{"z":1e999}]}`,
		"big number in an unknown field of the graph": envelope(
			`{"note":1e400,` + graphJSON[1:]),
		"big numbers nested inside the graph": envelope(
			`{"meta":[1e999,{"deep":-1e400}],` + graphJSON[1:]),
		"big numbers in unknown fields of dataset records": envelope(
			// Every record of the graphs passed to this helper opens as
			// {"name":..., so the unknown field is prepended to each record
			// verbatim, leaving the corrupt name or upstream in place.
			strings.ReplaceAll(graphJSON, `{"name":`, `{"note":[1e400,1e999],"name":`)),
		"big numbers everywhere": `{"note":1e400,"meta":[1e999,{"z":-1e400}],` +
			`"formatVersion":1,"contentId":"` + contentID + `","graph":{"note":1e400,` +
			graphJSON[1:len(graphJSON)-1] + `},"tail":[2e400]}`,
	}
}

// TestSnapshotBigNumberInUnknownFieldDoesNotMaskCorruptNames: every placement
// of an oversized-but-legal number in ignored content must leave the later
// name defect fully visible, in every lineage-name position of the embedded
// graph (a dataset name and an upstream entry), for both kinds of corruption.
func TestSnapshotBigNumberInUnknownFieldDoesNotMaskCorruptNames(t *testing.T) {
	const bogusID = "sha256:0000000000000000000000000000000000000000000000000000000000000000"

	// The first two graphs are reused across the placement map (whose
	// record-decoration variant spells out its own graph); the decoration is
	// ignored either way, so the corrupt name is what must be reported.
	corruptGraphs := map[string]struct {
		graph    string
		field    string
		location string
		reason   string
	}{
		"invalid bytes in a dataset name": {
			`{"datasets":[{"name":"A","upstreams":[]},{"name":"b` + "\xff" + `","upstreams":[]}]}`,
			`"name"`, `index 1 of "datasets"`, "invalid UTF-8 bytes",
		},
		"lone surrogate in a dataset name": {
			`{"datasets":[{"name":"A","upstreams":[]},{"name":"b\ud800","upstreams":[]}]}`,
			`"name"`, `index 1 of "datasets"`, "unpaired surrogate",
		},
		"invalid bytes in an upstream entry": {
			`{"datasets":[{"name":"A","upstreams":[]},{"name":"B","upstreams":["A","g` + "\xfe" + `"]}]}`,
			`"upstreams"`, `index 1 in the dataset record at index 1 of "datasets"`, "invalid UTF-8 bytes",
		},
		"lone surrogate in an upstream entry": {
			`{"datasets":[{"name":"A","upstreams":[]},{"name":"B","upstreams":["\udfff"]}]}`,
			`"upstreams"`, `index 0 in the dataset record at index 1 of "datasets"`, "unpaired surrogate",
		},
		"low surrogate written before its high surrogate upstream": {
			`{"datasets":[{"name":"A","upstreams":[]},{"name":"B","upstreams":["A\udc00\ud83d"]}]}`,
			`"upstreams"`, `index 0 in the dataset record at index 1 of "datasets"`, "unpaired surrogate",
		},
	}

	for graphName, gc := range corruptGraphs {
		for placementName, doc := range snapshotBignumPlacements(bogusID, gc.graph) {
			t.Run(graphName+" / "+placementName, func(t *testing.T) {
				assertSnapshotNameCorruption(t, doc, gc.field, gc.location, gc.reason)
			})
		}
	}

	// The big number may also sit in the SAME record as the corrupt name,
	// before the field that fails — both before the record's name and inside
	// its upstreams-adjacent unknown content.
	t.Run("big number in the same record as the corrupt name", func(t *testing.T) {
		doc := `{"formatVersion":1,"contentId":"` + bogusID + `","graph":{"datasets":[` +
			`{"vals":[1e999,-1e400],"name":"a` + "\xff" + `","upstreams":[]}]}}`
		assertSnapshotNameCorruption(t, doc, `"name"`, `index 0 of "datasets"`, "invalid UTF-8 bytes")
	})
	t.Run("big number right before a corrupt upstream", func(t *testing.T) {
		doc := `{"formatVersion":1,"contentId":"` + bogusID +
			`","graph":{"datasets":[{"name":"A","upstreams":[]},{"note":1e400,"name":"B","upstreams":["ok","g` + "\xfe" + `"]}]}}`
		assertSnapshotNameCorruption(t, doc, `"upstreams"`,
			`index 1 in the dataset record at index 1 of "datasets"`, "invalid UTF-8 bytes")
	})
}

// TestSnapshotCorruptUpstreamBigNumberCannotCollideWithRealSource is the
// lineage-specific regression: the frozen graph really contains a root
// dataset named "源�", while another dataset's direct upstream is written as
// "源" plus one broken byte or a lone surrogate. After the decoder's U+FFFD
// rewrite the edge would resolve to that genuine root AND the snapshot's
// declared content identifier equals the rewritten graph's digest — yet the
// snapshot must be refused for the name encoding, not accepted, not rejected
// for the identifier mismatch, and not rejected as a missing upstream.
// Ignored big numbers placed anywhere around it change neither the verdict
// nor the error. Without any dataset the rewrite could collide with, the same
// corruption must be rejected for the same reason.
func TestSnapshotCorruptUpstreamBigNumberCannotCollideWithRealSource(t *testing.T) {
	// Genuine content identifier of the graph AS REWRITTEN by the decoder.
	rewritten := `{"datasets":[{"name":"源�","upstreams":[]},{"name":"d","upstreams":["源�"]}]}`
	matchID := snapshotOf(t, rewritten).ContentID

	corruptEdges := map[string]struct {
		edge   string
		reason string
	}{
		"lone high surrogate": {`"源\uD800"`, "unpaired surrogate"},
		"lone low surrogate":  {`"源\uDC00"`, "unpaired surrogate"},
		"low before high":     {`"源\uDC00\uD83D"`, "unpaired surrogate"},
		"broken byte":         {`"源` + "\xff" + `"`, "invalid UTF-8 bytes"},
	}
	const location = `index 0 in the dataset record at index 1 of "datasets"`

	for edgeName, ec := range corruptEdges {
		corruptGraph := `{"datasets":[{"name":"源�","upstreams":[]},{"name":"d","upstreams":[` + ec.edge + `]}]}`
		for placementName, doc := range snapshotBignumPlacements(matchID, corruptGraph) {
			t.Run(edgeName+" / "+placementName, func(t *testing.T) {
				assertSnapshotNameCorruption(t, doc, `"upstreams"`, location, ec.reason)
			})
		}
	}

	// The matching identifier is genuine, not invented: under that same
	// declared id the clean, rewritten snapshot reads successfully. This
	// proves the corrupt documents above were NOT refused for an id mismatch.
	clean, err := ParseSnapshot([]byte(`{"formatVersion":1,"contentId":"` + matchID + `","graph":` + rewritten + `}`))
	if err != nil {
		t.Fatalf("the equivalent clean snapshot must read under the matched id: %v", err)
	}
	if clean.ContentID != matchID {
		t.Errorf("content id = %q, want %q", clean.ContentID, matchID)
	}

	// No collision target at all: no dataset named "源�" exists, and even the
	// rewrite's graph would be missing an upstream. The corrupt raw literal is
	// still what rejects the snapshot — before structure and id checks — with
	// big numbers ignored around it.
	noCollision := map[string]struct {
		graph  string
		reason string
	}{
		"lone surrogate names no dataset": {
			`{"datasets":[{"name":"keep","upstreams":[]},{"name":"B","upstreams":["源\ud800"]}]}`,
			"unpaired surrogate",
		},
		"broken byte names no dataset": {
			`{"datasets":[{"name":"keep","upstreams":[]},{"name":"B","upstreams":["源` + "\xff" + `"]}]}`,
			"invalid UTF-8 bytes",
		},
	}
	for name, nc := range noCollision {
		t.Run(name, func(t *testing.T) {
			for placementName, doc := range snapshotBignumPlacements(bogusNonEmptyID(), nc.graph) {
				t.Run(placementName, func(t *testing.T) {
					assertSnapshotNameCorruption(t, doc, `"upstreams"`, location, nc.reason)
				})
			}
		})
	}
}

// bogusNonEmptyID returns a syntactically shaped but non-matching content
// identifier; the name gate runs before the comparison, so its value is
// irrelevant as long as it is non-empty.
func bogusNonEmptyID() string {
	return "sha256:00"
}

// TestSnapshotBigNumberCorruptNameErrorByteStable: the error reported for one
// fixed corrupt name embedded in a snapshot must be byte-for-byte identical
// whether ignored big-number content surrounds it or not — the skip must not
// leak positions, numbers, or any other trace into the name error.
func TestSnapshotBigNumberCorruptNameErrorByteStable(t *testing.T) {
	const id = "sha256:00"
	corrupt := []struct {
		name  string
		graph string
	}{
		{"name bytes", `{"datasets":[{"name":"a` + "\xff" + `","upstreams":[]}]}`},
		{"name surrogate", `{"datasets":[{"name":"a\ud800","upstreams":[]}]}`},
		{"upstream bytes", `{"datasets":[{"name":"A","upstreams":[]},{"name":"B","upstreams":["g` + "\xfe" + `"]}]}`},
		{"upstream surrogate", `{"datasets":[{"name":"A","upstreams":[]},{"name":"B","upstreams":["\udfff"]}]}`},
	}
	for _, c := range corrupt {
		t.Run(c.name, func(t *testing.T) {
			var wantMsg string
			for _, doc := range snapshotBignumPlacements(id, c.graph) {
				_, err := ParseSnapshot([]byte(doc))
				if err == nil {
					t.Fatal("corrupt snapshot accepted")
				}
				if wantMsg == "" {
					wantMsg = err.Error()
				} else if err.Error() != wantMsg {
					t.Errorf("error:\n%q\nwant:\n%q", err.Error(), wantMsg)
				}
			}
		})
	}
}

// TestSnapshotGenuineNamesWithBigNumbersTraceSame pins the legal side: names
// that genuinely carry a replacement character (written directly or as the
// equivalent escape), correctly paired surrogate escapes, and text that
// merely looks like a Unicode escape (a real backslash followed by letters)
// all read verbatim, and with ignored big numbers present the trace still
// reports the same roots and paths. Direct writing and equivalent escaping
// produce identical results.
func TestSnapshotGenuineNamesWithBigNumbersTraceSame(t *testing.T) {
	// Root 源� reached by a downstream named with a paired surrogate escape
	// (😀); both the genuine U+FFFD direct spelling and its \u escape occur.
	directGraph := `{"datasets":[{"name":"源�","upstreams":[]},{"name":"ledger😀","upstreams":["源�"]}]}`
	escapedGraph := `{"datasets":[{"name":"` + "\\u6e90\\ufffd" + `","upstreams":[]},` +
		`{"name":"ledger` + "\\ud83d\\ude00" + `","upstreams":["` + "\\u6e90\\ufffd" + `"]}]}`
	id := snapshotOf(t, directGraph).ContentID

	docs := map[string]string{
		"plain direct": `{"formatVersion":1,"contentId":"` + id + `","graph":` + directGraph + `}`,
		"equivalent escapes + big numbers": `{"note":1e400,"meta":[1e999,{"z":-1e400}],` +
			`"formatVersion":1,"contentId":"` + id + `","graph":` + escapedGraph + `}`,
		"big numbers in the graph and records": `{"formatVersion":1,"contentId":"` + id +
			`","graph":{"note":1e400,"datasets":[{"vals":[1e999],"name":"源�","upstreams":[]},{"name":"ledger😀","upstreams":["源�"],"meta":-1e400}]},"tail":[2e400]}`,
	}

	var wantDownstream, wantRoot string
	for name, doc := range docs {
		t.Run(name, func(t *testing.T) {
			snap, err := ParseSnapshot([]byte(doc))
			if err != nil {
				t.Fatalf("legal snapshot rejected: %v", err)
			}
			if snap.ContentID != id {
				t.Errorf("content id = %q, want %q; ignored fields must not change identity", snap.ContentID, id)
			}
			down, err := TraceSources(snap, "ledger😀")
			if err != nil {
				t.Fatalf("TraceSources ledger😀: %v", err)
			}
			gotDownstream := traceReportJSON(t, down)
			root, err := TraceSources(snap, "源�")
			if err != nil {
				t.Fatalf("TraceSources 源�: %v", err)
			}
			gotRoot := traceReportJSON(t, root)
			if wantDownstream == "" {
				wantDownstream, wantRoot = gotDownstream, gotRoot
			} else {
				if gotDownstream != wantDownstream {
					t.Errorf("downstream trace differs:\n%s\n%s", gotDownstream, wantDownstream)
				}
				if gotRoot != wantRoot {
					t.Errorf("root self-trace differs:\n%s\n%s", gotRoot, wantRoot)
				}
			}
			if !reflect.DeepEqual(down.Sources, []SourceTrace{{Root: "源�", Path: []string{"ledger😀", "源�"}}}) {
				t.Errorf("downstream sources = %+v, want the genuine 源� root", down.Sources)
			}
			if !reflect.DeepEqual(root.Sources, []SourceTrace{{Root: "源�", Path: []string{"源�"}}}) {
				t.Errorf("root sources = %+v, want itself as a single-element path", root.Sources)
			}
		})
	}

	// A name that really contains a backslash and letters is plain text, not
	// an escape: it stays its own dataset, queried directly and reached from a
	// downstream, with ignored big numbers present.
	lookalikeGraph := `{"datasets":[{"name":"x\\ud800","upstreams":[]},{"name":"y","upstreams":["x\\ud800"]}]}`
	lookalikeID := snapshotOf(t, lookalikeGraph).ContentID
	lookalikeDoc := `{"note":1e400,"formatVersion":1,"contentId":"` + lookalikeID +
		`","graph":{"datasets":[{"vals":[1e999],"name":"x\\ud800","upstreams":[]},{"name":"y","upstreams":["x\\ud800"],"note":-1e400}]}}`
	snap, err := ParseSnapshot([]byte(lookalikeDoc))
	if err != nil {
		t.Fatalf("backslash-lookalike snapshot rejected: %v", err)
	}
	report, err := TraceSources(snap, "y")
	if err != nil {
		t.Fatalf("TraceSources y: %v", err)
	}
	if !reflect.DeepEqual(report.Sources, []SourceTrace{{Root: `x\ud800`, Path: []string{"y", `x\ud800`}}}) {
		t.Errorf("sources = %+v, want the verbatim backslash name", report.Sources)
	}
	self, err := TraceSources(snap, `x\ud800`)
	if err != nil {
		t.Fatalf("TraceSources x\\ud800: %v", err)
	}
	if !reflect.DeepEqual(self.Sources, []SourceTrace{{Root: `x\ud800`, Path: []string{`x\ud800`}}}) {
		t.Errorf("self sources = %+v", self.Sources)
	}
}

// TestSnapshotUnknownBigNumbersDoNotChangeTraceReport: adding the ignored
// oversized-number content to one legal snapshot changes neither the content
// identifier nor the byte-for-byte trace report of any query, including a
// multi-branch graph where path tie-breaking matters.
func TestSnapshotUnknownBigNumbersDoNotChangeTraceReport(t *testing.T) {
	graph := `{"datasets":[
		{"name":"T","upstreams":["A","B"]},
		{"name":"A","upstreams":["R"]},
		{"name":"B","upstreams":["R"]},
		{"name":"R","upstreams":[]},
		{"name":"源�","upstreams":[]}
	]}`
	id := snapshotOf(t, graph).ContentID
	variants := map[string]string{
		"plain": `{"formatVersion":1,"contentId":"` + id + `","graph":` + graph + `}`,
		"big number at top": `{"note":1e400,` +
			`"formatVersion":1,"contentId":"` + id + `","graph":` + graph + `}`,
		"big numbers nested everywhere": `{"meta":{"x":[1e400,{"y":1e999}]},` +
			`"formatVersion":1,"contentId":"` + id + `","graph":{"note":[1e999,-1e400],` + graphJSONBody(graph) + `},"tail":2e400}`,
		"big numbers inside every record": `{"formatVersion":1,"contentId":"` + id + `","graph":{"datasets":[
			{"note":1e400,"name":"T","upstreams":["A","B"]},
			{"vals":[1e999],"name":"A","upstreams":["R"]},
			{"name":"B","upstreams":["R"],"meta":-1e400},
			{"name":"R","upstreams":[],"deep":{"x":1e999}},
			{"name":"源�","upstreams":[],"bag":[1e400]}
		]}}`,
	}

	queries := []string{"T", "R", "源�", "A"}
	base := make(map[string]string, len(queries))
	for variantName, doc := range variants {
		snap, err := ParseSnapshot([]byte(doc))
		if err != nil {
			t.Fatalf("%s: ParseSnapshot: %v", variantName, err)
		}
		if snap.ContentID != id {
			t.Errorf("%s: content id = %q, want %q", variantName, snap.ContentID, id)
		}
		for _, query := range queries {
			report, err := TraceSources(snap, query)
			if err != nil {
				t.Fatalf("%s: trace %q: %v", variantName, query, err)
			}
			got := traceReportJSON(t, report)
			if want, ok := base[query]; !ok {
				base[query] = got
			} else if got != want {
				t.Errorf("%s: trace %q differs:\n%s\n%s", variantName, query, got, want)
			}
		}
	}
}

// graphJSONBody strips the one-level outer braces of a graph document so a
// variant can nest additional fields at the graph-object level.
func graphJSONBody(graph string) string {
	return graph[1 : len(graph)-1]
}

// traceReportJSON marshals a trace report deterministically for byte
// comparison (the report's slices are already ordered by the core).
func traceReportJSON(t *testing.T, report *TraceReport) string {
	t.Helper()
	b, err := json.Marshal(report)
	if err != nil {
		t.Fatalf("marshal trace report: %v", err)
	}
	return string(b)
}
