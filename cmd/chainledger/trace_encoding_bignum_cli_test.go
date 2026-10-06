package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/asdhoaiqqq/chainledger-governance/chainledger"
)

// End-to-end regression coverage for reading snapshots that also carry
// additional information in fields the reader ignores. A legal JSON number
// outside the float64 range (1e400) — at the snapshot top level, inside the
// embedded graph or a dataset record's unknown fields, or buried in unknown
// objects and arrays — carries no lineage semantics and must neither mask a
// later name-encoding failure nor change a successful trace. These tests pin
// the observable command-line contract through run(): trace (and compare)
// return a non-zero exit with empty stdout and a precise stderr, the snapshot
// file keeps every byte, no lock file appears, and a rejected trace never
// prints a root or path reconstructed after the decoder's U+FFFD rewrite.

// snapshotContentID builds the genuine content identifier a snapshot would
// assign to graphJSON, without running a command.
func snapshotContentID(t *testing.T, graphJSON string) string {
	t.Helper()
	graph, err := chainledger.UnmarshalGraphFile([]byte(graphJSON))
	if err != nil {
		t.Fatalf("UnmarshalGraphFile: %v", err)
	}
	snap, err := chainledger.BuildSnapshot(graph)
	if err != nil {
		t.Fatalf("BuildSnapshot: %v", err)
	}
	return snap.ContentID
}

// traceBignumSnapshots wraps graphJSON in a version-1 snapshot with the
// declared content id, placing oversized legal numbers in every kind of
// ignored position the snapshot reader must skip: a top-level unknown field
// before and after the known fields, nested unknown objects and arrays at the
// top level and inside the graph, and unknown fields of dataset records.
func traceBignumSnapshots(contentID, graphJSON string) map[string][]byte {
	envelope := func(graph string) string {
		return `{"formatVersion":1,"contentId":"` + contentID + `","graph":` + graph + `}`
	}
	plain := envelope(graphJSON)
	out := map[string][]byte{
		"plain":                              []byte(plain),
		"big number opens the snapshot":      []byte(`{"note":1e400,` + plain[1:]),
		"signed big number before the graph": []byte(`{"note":-1e400,` + plain[1:]),
		"big numbers nested in unknown objects and arrays": []byte(
			`{"meta":{"x":[1e400,{"y":1e999}]},"bag":[2e999,{"k":-1e400}],` + plain[1:]),
		"big number after the graph": []byte(
			plain[:len(plain)-1] + `,"tail":[1e400,{"z":1e999}]}`),
		"big number in an unknown field of the graph": []byte(
			envelope(`{"note":1e400,` + graphJSON[1:])),
		"big numbers nested inside the graph": []byte(
			envelope(`{"meta":[1e999,{"deep":-1e400}],` + graphJSON[1:])),
		"big numbers in unknown fields of dataset records": []byte(envelope(
			strings.ReplaceAll(graphJSON, `{"name":`, `{"note":[1e400,1e999],"name":`))),
		"big numbers everywhere": []byte(`{"note":1e400,"meta":[1e999,{"z":-1e400}],` +
			`"formatVersion":1,"contentId":"` + contentID + `","graph":{"note":1e400,` +
			graphJSON[1:len(graphJSON)-1] + `},"tail":[2e400]}`),
	}
	return out
}

// assertTraceRejectedSnapshot runs a trace query against the snapshot and
// requires the name-encoding failure contract: non-zero exit, empty stdout,
// a stderr naming the snapshot file and the corrupt field/position/reason
// (with byte and surrogate problems distinguished), the snapshot bytes
// untouched, and no lock file created. The failure must be reported as the
// name defect — not as an identifier mismatch or a missing upstream.
func assertTraceRejectedSnapshot(t *testing.T, snapPath, query string, original []byte, field, location, reason string) {
	t.Helper()
	stdout, stderr, exit := runTraceCommand(t, snapPath, query)
	if exit == 0 {
		t.Fatalf("trace exit = 0, want non-zero; stdout = %s", stdout)
	}
	if stdout != "" {
		t.Errorf("stdout = %q, want empty on rejection (no rewritten root or path)", stdout)
	}
	for _, want := range []string{snapPath, field, location, reason} {
		if !strings.Contains(stderr, want) {
			t.Errorf("stderr = %q, want it to contain %q", stderr, want)
		}
	}
	switch reason {
	case "invalid UTF-8 bytes":
		if strings.Contains(stderr, "surrogate") {
			t.Errorf("stderr = %q, byte corruption must not read as a surrogate problem", stderr)
		}
	case "unpaired surrogate":
		if strings.Contains(stderr, "invalid UTF-8 bytes") {
			t.Errorf("stderr = %q, a surrogate problem must not read as invalid bytes", stderr)
		}
	}
	for _, masked := range []string{"content identifier", "is not registered", "does not exist", "invalid snapshot JSON"} {
		if strings.Contains(stderr, masked) {
			t.Errorf("stderr = %q, the name defect was masked by %q", stderr, masked)
		}
	}
	if got := readFile(t, snapPath); got != string(original) {
		t.Errorf("snapshot changed after rejected trace:\n got %s\nwant %s", got, original)
	}
	if _, err := os.Stat(snapPath + ".lock"); !os.IsNotExist(err) {
		t.Errorf("trace created a lock file %q: %v", snapPath+".lock", err)
	}
}

// TestCLITraceBigNumberUnknownFieldsDoNotMaskCorruptNames drives every
// big-number placement x every corrupt-name position through trace: a corrupt
// dataset name or upstream (invalid bytes or an unpaired surrogate) must
// reject the whole snapshot with the file, field, zero-based dataset record
// position, and for an upstream its array position, regardless of the ignored
// numbers around it.
func TestCLITraceBigNumberUnknownFieldsDoNotMaskCorruptNames(t *testing.T) {
	const bogusID = "sha256:0000000000000000000000000000000000000000000000000000000000000000"
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
		"low surrogate written before its high surrogate": {
			`{"datasets":[{"name":"A","upstreams":[]},{"name":"B","upstreams":["A\udc00\ud83d"]}]}`,
			`"upstreams"`, `index 0 in the dataset record at index 1 of "datasets"`, "unpaired surrogate",
		},
	}

	for graphName, gc := range corruptGraphs {
		for placementName, doc := range traceBignumSnapshots(bogusID, gc.graph) {
			t.Run(graphName+" / "+placementName, func(t *testing.T) {
				snapPath := writeRawFile(t, "snap.json", doc)
				assertTraceRejectedSnapshot(t, snapPath, "B", doc, gc.field, gc.location, gc.reason)
			})
		}
	}
}

// TestCLITraceCorruptUpstreamBigNumberCannotCollideWithRealSource is the
// source-mistaking regression at the command line: the graph really has a
// root named "源�", and another dataset's upstream is written as "源" plus a
// broken byte or a lone surrogate. After the decoder's rewrite the reference
// would resolve to that root AND the declared content identifier equals the
// rewritten graph's digest — yet trace must fail on the name encoding with
// ignored big numbers present, print nothing on stdout, and leave the file
// bytes alone. A corrupt name with no dataset it could collide with fails the
// same way.
func TestCLITraceCorruptUpstreamBigNumberCannotCollideWithRealSource(t *testing.T) {
	// Genuine identifier of the graph the decoder rewrites the corrupt edge to.
	rewritten := `{"datasets":[{"name":"源�","upstreams":[]},{"name":"d","upstreams":["源�"]}]}`
	matchID := snapshotContentID(t, rewritten)
	const location = `index 0 in the dataset record at index 1 of "datasets"`

	corruptEdges := map[string]struct {
		edge   string
		reason string
	}{
		"lone high surrogate": {`"源\uD800"`, "unpaired surrogate"},
		"lone low surrogate":  {`"源\uDC00"`, "unpaired surrogate"},
		"low before high":     {`"源\uDC00\uD83D"`, "unpaired surrogate"},
		"broken byte":         {`"源` + "\xff" + `"`, "invalid UTF-8 bytes"},
	}
	for edgeName, ec := range corruptEdges {
		corruptGraph := `{"datasets":[{"name":"源�","upstreams":[]},{"name":"d","upstreams":[` + ec.edge + `]}]}`
		for placementName, doc := range traceBignumSnapshots(matchID, corruptGraph) {
			t.Run(edgeName+" / "+placementName, func(t *testing.T) {
				snapPath := writeRawFile(t, "snap.json", doc)
				assertTraceRejectedSnapshot(t, snapPath, "d", doc,
					`"upstreams"`, location, ec.reason)
			})
		}
	}

	// The matched identifier is genuine: the clean rewritten snapshot traces
	// successfully under it, proving the corrupt files were not refused for an
	// identifier mismatch.
	cleanPath := writeFile(t, "clean.json",
		`{"formatVersion":1,"contentId":"`+matchID+`","graph":`+rewritten+`}`)
	stdout, stderr, exit := runTraceCommand(t, cleanPath, "d")
	if exit != 0 {
		t.Fatalf("the equivalent clean snapshot must trace: %s", stderr)
	}
	var cleanReport chainledger.TraceReport
	if err := json.Unmarshal([]byte(stdout), &cleanReport); err != nil {
		t.Fatalf("clean trace stdout is not JSON: %v\n%s", err, stdout)
	}
	if !reflect.DeepEqual(cleanReport.Sources,
		[]chainledger.SourceTrace{{Root: "源�", Path: []string{"d", "源�"}}}) {
		t.Errorf("clean sources = %+v, want [d -> 源�]", cleanReport.Sources)
	}

	// No collision target: nothing is named the rewrite result. The corrupt
	// raw literal is still the reported reason, with big numbers ignored.
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
			for placementName, doc := range traceBignumSnapshots("sha256:00", nc.graph) {
				t.Run(placementName, func(t *testing.T) {
					snapPath := writeRawFile(t, "snap.json", doc)
					assertTraceRejectedSnapshot(t, snapPath, "B", doc,
						`"upstreams"`, location, nc.reason)
				})
			}
		})
	}
}

// TestCLITraceLegalNamesWithBigNumbersByteIdentical pins the legal side end
// to end: a genuine U+FFFD (direct or as its equivalent escape), correctly
// paired surrogate escapes, and a name that merely looks like an escape (a
// real backslash followed by letters) all trace to the same roots and paths.
// Adding the ignored oversized-number content to the same legal snapshot
// leaves the content identifier and every byte of the trace report intact.
func TestCLITraceLegalNamesWithBigNumbersByteIdentical(t *testing.T) {
	directGraph := `{"datasets":[{"name":"源�","upstreams":[]},{"name":"ledger😀","upstreams":["源�"]}]}`
	escapedGraph := `{"datasets":[{"name":"` + "\\u6e90\\ufffd" + `","upstreams":[]},` +
		`{"name":"ledger` + "\\ud83d\\ude00" + `","upstreams":["` + "\\u6e90\\ufffd" + `"]}]}`
	id := snapshotContentID(t, directGraph)

	variants := traceBignumSnapshots(id, directGraph)
	// Replace the plain entry's graph spellings with equivalent escapes while
	// keeping the same identifier; add an escape-only variant too.
	variants["equivalent escapes with big numbers"] = []byte(
		`{"note":1e400,"meta":[1e999,{"z":-1e400}],"formatVersion":1,"contentId":"` + id +
			`","graph":` + escapedGraph + `}`)

	queries := map[string][]chainledger.SourceTrace{
		"ledger😀": {{Root: "源�", Path: []string{"ledger😀", "源�"}}},
		"源�":      {{Root: "源�", Path: []string{"源�"}}},
	}
	base := make(map[string]string)
	for variantName, doc := range variants {
		snapPath := writeRawFile(t, "snap.json", doc)
		// The hand-written variant is itself a valid snapshot carrying the
		// same identity; unknown fields never change the content identifier.
		parsed, err := chainledger.ParseSnapshot(doc)
		if err != nil {
			t.Fatalf("%s: variant is not a valid snapshot: %v", variantName, err)
		}
		if parsed.ContentID != id {
			t.Errorf("%s: content id = %q, want %q", variantName, parsed.ContentID, id)
		}
		for query, wantSources := range queries {
			t.Run(variantName+"/"+query, func(t *testing.T) {
				stdout, stderr, exit := runTraceCommand(t, snapPath, query)
				if exit != 0 {
					t.Fatalf("trace %q exit = %d, stderr = %s", query, exit, stderr)
				}
				if prev, ok := base[query]; !ok {
					base[query] = stdout
				} else if stdout != prev {
					t.Errorf("trace %q report differs across snapshots:\n%s\n%s", query, stdout, prev)
				}
				var report chainledger.TraceReport
				if err := json.Unmarshal([]byte(stdout), &report); err != nil {
					t.Fatalf("stdout is not JSON: %v\n%s", err, stdout)
				}
				if !reflect.DeepEqual(report.Sources, wantSources) {
					t.Errorf("sources = %+v, want %+v", report.Sources, wantSources)
				}
				if report.ContentID != id {
					t.Errorf("report content id = %q, want %q", report.ContentID, id)
				}
				if got := readFile(t, snapPath); got != string(doc) {
					t.Error("trace modified the snapshot bytes")
				}
			})
		}
	}

	// A name really containing a backslash and letters is plain text: it must
	// remain its own dataset with the verbatim name when traced, even with big
	// numbers present.
	lookalikeGraph := `{"datasets":[{"name":"x\\ud800","upstreams":[]},{"name":"y","upstreams":["x\\ud800"]}]}`
	lookalikeID := snapshotContentID(t, lookalikeGraph)
	for name, doc := range map[string][]byte{
		"plain": []byte(`{"formatVersion":1,"contentId":"` + lookalikeID + `","graph":` + lookalikeGraph + `}`),
		"with big numbers": []byte(`{"note":1e400,"formatVersion":1,"contentId":"` + lookalikeID +
			`","graph":{"datasets":[{"vals":[1e999],"name":"x\\ud800","upstreams":[]},{"name":"y","upstreams":["x\\ud800"],"note":-1e400}]}}`),
	} {
		t.Run("backslash lookalike / "+name, func(t *testing.T) {
			snapPath := writeRawFile(t, "snap.json", doc)
			stdout, stderr, exit := runTraceCommand(t, snapPath, "y")
			if exit != 0 {
				t.Fatalf("trace exit = %d, stderr = %s", exit, stderr)
			}
			var report chainledger.TraceReport
			if err := json.Unmarshal([]byte(stdout), &report); err != nil {
				t.Fatalf("stdout is not JSON: %v\n%s", err, stdout)
			}
			if !reflect.DeepEqual(report.Sources,
				[]chainledger.SourceTrace{{Root: `x\ud800`, Path: []string{"y", `x\ud800`}}}) {
				t.Errorf("sources = %+v, want the verbatim backslash name", report.Sources)
			}
		})
	}
}

// TestCLICompareRejectsCorruptSnapshotWithBigNumbers: compare reads both
// snapshots strictly, so a corrupt name hidden behind ignored big numbers
// rejects the comparison on either side with empty stdout and both files
// untouched.
func TestCLICompareRejectsCorruptSnapshotWithBigNumbers(t *testing.T) {
	dir := t.TempDir()
	good := filepath.Join(dir, "good.json")
	if _, stderr, exit := snapshotIntoDir(t, dir, abcGraph, "good.json"); exit != 0 {
		t.Fatalf("seed good snapshot: %s", stderr)
	}
	if err := os.Remove(good + ".lock"); err != nil {
		t.Fatalf("remove seed lock: %v", err)
	}

	corruptGraph := `{"datasets":[{"name":"源�","upstreams":[]},{"name":"d","upstreams":["源\ud800"]}]}`
	id := snapshotContentID(t, `{"datasets":[{"name":"源�","upstreams":[]},{"name":"d","upstreams":["源�"]}]}`)
	bad := filepath.Join(dir, "bad.json")
	badBytes := traceBignumSnapshots(id, corruptGraph)["big numbers everywhere"]
	if err := os.WriteFile(bad, badBytes, 0o644); err != nil {
		t.Fatalf("write bad snapshot: %v", err)
	}
	goodOriginal := readFile(t, good)

	for _, tc := range []struct {
		name string
		args []string
	}{
		{"corrupt old snapshot", []string{"compare", bad, good}},
		{"corrupt new snapshot", []string{"compare", good, bad}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			stdout, stderr, exit := captureStdout(t, func() int { return run(tc.args) })
			if exit == 0 {
				t.Fatalf("%v succeeded, want failure", tc.args)
			}
			if stdout != "" {
				t.Errorf("%v wrote a report on failure: %q", tc.args, stdout)
			}
			for _, want := range []string{bad, `"upstreams"`, `index 1 of "datasets"`, "unpaired surrogate"} {
				if !strings.Contains(stderr, want) {
					t.Errorf("stderr = %q, want it to contain %q", stderr, want)
				}
			}
			if got := readFile(t, bad); got != string(badBytes) {
				t.Errorf("corrupt snapshot changed:\n%s", got)
			}
			if got := readFile(t, good); got != goodOriginal {
				t.Errorf("good snapshot changed:\n%s", got)
			}
			if _, err := os.Stat(bad + ".lock"); !os.IsNotExist(err) {
				t.Errorf("compare created a lock file: %v", err)
			}
		})
	}
}

// TestCLISnapshotSourceGraphWithBigNumbersStaysCanonical: saving a source
// graph that carries oversized numbers only in unknown fields succeeds; the
// frozen snapshot keeps only graph semantics (so its identifier matches the
// clean graph's), and a later trace reports the same root source.
func TestCLISnapshotSourceGraphWithBigNumbersStaysCanonical(t *testing.T) {
	cleanGraph := `{"datasets":[{"name":"A","upstreams":[]},{"name":"B","upstreams":["A"]}]}`
	decorated := `{"note":1e400,"meta":[1e999,{"z":-1e400}],` +
		`"datasets":[{"vals":[1e999],"name":"A","upstreams":[]},{"name":"B","upstreams":["A"],"note":-1e400}],"tail":[2e400]}`
	wantID := snapshotContentID(t, cleanGraph)

	dir := t.TempDir()
	graphPath := filepath.Join(dir, "graph.json")
	if err := os.WriteFile(graphPath, []byte(decorated), 0o644); err != nil {
		t.Fatalf("write decorated graph: %v", err)
	}
	snapPath := filepath.Join(dir, "snap.json")
	stdout, stderr, exit := captureStdout(t, func() int {
		return run([]string{"snapshot", graphPath, snapPath})
	})
	if exit != 0 {
		t.Fatalf("snapshot of a decorated graph failed: %s", stderr)
	}
	if strings.TrimSpace(stdout) != wantID {
		t.Errorf("snapshot id = %q, want %q", strings.TrimSpace(stdout), wantID)
	}
	data := readFile(t, snapPath)
	parsed, err := chainledger.ParseSnapshot([]byte(data))
	if err != nil {
		t.Fatalf("saved snapshot invalid: %v", err)
	}
	if parsed.ContentID != wantID {
		t.Errorf("saved content id = %q, want %q", parsed.ContentID, wantID)
	}
	if strings.Contains(data, "1e400") || strings.Contains(data, "note") || strings.Contains(data, "vals") {
		t.Errorf("unknown fields leaked into the canonical snapshot:\n%s", data)
	}

	traceOut, stderr, exit := runTraceCommand(t, snapPath, "B")
	if exit != 0 {
		t.Fatalf("trace after decorated snapshot failed: %s", stderr)
	}
	var report chainledger.TraceReport
	if err := json.Unmarshal([]byte(traceOut), &report); err != nil {
		t.Fatalf("trace stdout is not JSON: %v\n%s", err, traceOut)
	}
	if !reflect.DeepEqual(report.Sources,
		[]chainledger.SourceTrace{{Root: "A", Path: []string{"B", "A"}}}) {
		t.Errorf("sources = %+v, want [B -> A]", report.Sources)
	}
}
