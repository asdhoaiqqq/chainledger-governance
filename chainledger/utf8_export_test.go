package chainledger

import (
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
	"unicode/utf8"
)

// These tests pin the rule that an in-memory graph whose names are not valid
// UTF-8 can never be exported or snapshotted: encoding/json would silently
// rewrite each invalid byte into U+FFFD, so two different names could collapse
// into one and a re-read graph would not be a copy of the original. Both
// exporters must refuse the whole graph instead.

// invalidUTF8GraphCase places one invalid name in each location that reaches an
// exporter: a root dataset key, a derived dataset key, and a direct upstream
// reference (both registered and missing from the node set).
var invalidUTF8GraphCases = map[string]func() map[string]*Lineage{
	"invalid root dataset name": func() map[string]*Lineage {
		return map[string]*Lineage{
			"root\xff": {Dataset: "root\xff"},
			"derived":  {Dataset: "derived", Parents: []string{"root\xff"}},
		}
	},
	"invalid derived dataset name": func() map[string]*Lineage {
		return map[string]*Lineage{
			"root":    {Dataset: "root"},
			"der\xfe": {Dataset: "der\xfe", Parents: []string{"root"}},
		}
	},
	"invalid direct upstream reference, registered": func() map[string]*Lineage {
		// The referenced upstream is itself a node, so its key and the
		// referrer's Parents entry both carry the same invalid bytes.
		return map[string]*Lineage{
			"up\xff": {Dataset: "up\xff"},
			"down":   {Dataset: "down", Parents: []string{"up\xff"}},
		}
	},
	"invalid direct upstream reference, not registered": func() map[string]*Lineage {
		// An unregistered upstream is normally ErrNotFound, but an unusable
		// name must surface as ErrInvalidArgument regardless.
		return map[string]*Lineage{
			"down": {Dataset: "down", Parents: []string{"ghost\xff"}},
		}
	},
}

func TestMarshalGraphFileRejectsInvalidUTF8(t *testing.T) {
	for name, build := range invalidUTF8GraphCases {
		t.Run(name, func(t *testing.T) {
			graph := build()
			out, err := MarshalGraphFile(graph)
			if err == nil {
				t.Fatalf("MarshalGraphFile accepted a graph with invalid UTF-8 and returned %s", out)
			}
			if !errors.Is(err, ErrInvalidArgument) {
				t.Fatalf("err = %v, want errors.Is ErrInvalidArgument", err)
			}
			if out != nil {
				t.Fatalf("export returned %d bytes despite the error: %s", len(out), out)
			}
			// The error must carry the original bytes, not their replacement:
			// %q renders invalid bytes as \x escapes and must not print U+FFFD.
			msg := err.Error()
			if strings.Contains(msg, "�") {
				t.Fatalf("error shows the replacement character instead of the original bytes: %q", msg)
			}
			if !strings.Contains(msg, `\x`) {
				t.Fatalf("error does not quote the original offending bytes: %q", msg)
			}
		})
	}

	t.Run("single invalid name with no possible collision", func(t *testing.T) {
		// Even one root alone, with no other name to collide with, must fail.
		graph := map[string]*Lineage{"only\xff": {Dataset: "only\xff"}}
		if out, err := MarshalGraphFile(graph); err == nil {
			t.Fatalf("single-node graph with invalid UTF-8 was exported as %s", out)
		}
	})
}

func TestBuildSnapshotRejectsInvalidUTF8(t *testing.T) {
	for name, build := range invalidUTF8GraphCases {
		t.Run(name, func(t *testing.T) {
			graph := build()
			snap, err := BuildSnapshot(graph)
			if err == nil {
				t.Fatalf("BuildSnapshot accepted a graph with invalid UTF-8: %#v", snap)
			}
			if !errors.Is(err, ErrInvalidArgument) {
				t.Fatalf("err = %v, want errors.Is ErrInvalidArgument", err)
			}
			if snap != nil {
				t.Fatalf("BuildSnapshot returned snapshot %#v despite the error", snap)
			}
			msg := err.Error()
			if strings.Contains(msg, "�") || !strings.Contains(msg, `\x`) {
				t.Fatalf("error must name the original bytes, got %q", msg)
			}
		})
	}

	t.Run("single invalid name with no possible collision", func(t *testing.T) {
		graph := map[string]*Lineage{"only\xfe": {Dataset: "only\xfe"}}
		if snap, err := BuildSnapshot(graph); err == nil {
			t.Fatalf("single-node graph with invalid UTF-8 produced snapshot %#v", snap)
		}
	})
}

// Two names that differ only in an invalid byte (0xFF vs 0xFE) would both be
// serialized as the same "prefix�" node; their rejection messages must still
// tell the two original names apart.
func TestInvalidUTF8NamesRemainDistinguishableInErrors(t *testing.T) {
	// Document why the gate exists: without it the JSON encoder does exactly
	// the silent collision the task describes: both names marshal identically
	// and decode back to the same replacement-character name.
	a, _ := json.Marshal("p\xff")
	b, _ := json.Marshal("p\xfe")
	if string(a) != string(b) {
		t.Fatalf("test premise changed: json.Marshal gives %s and %s", a, b)
	}
	var decoded string
	if err := json.Unmarshal(a, &decoded); err != nil {
		t.Fatalf("decoding marshaled name: %v", err)
	}
	if decoded != "p"+string(utf8.RuneError) {
		t.Fatalf("marshaled name decodes to %q, not the replacement name", decoded)
	}

	graphFF := map[string]*Lineage{"p\xff": {Dataset: "p\xff"}}
	graphFE := map[string]*Lineage{"p\xfe": {Dataset: "p\xfe"}}
	_, errFF := MarshalGraphFile(graphFF)
	_, errFE := MarshalGraphFile(graphFE)
	if errFF == nil || errFE == nil {
		t.Fatalf("expected both graphs rejected, got %v, %v", errFF, errFE)
	}
	if errFF.Error() == errFE.Error() {
		t.Fatalf("0xFF and 0xFE produced the same error message: %q", errFF)
	}
	if !strings.Contains(errFF.Error(), `\xff`) || !strings.Contains(errFE.Error(), `\xfe`) {
		t.Fatalf("errors do not show the distinct original bytes:\n%q\n%q", errFF, errFE)
	}
}

// The reported name must be stable no matter how the map chooses to iterate.
func TestInvalidUTF8ReportedNameIsDeterministic(t *testing.T) {
	graph := map[string]*Lineage{
		"z\xff": {Dataset: "z\xff"},
		"a\xfe": {Dataset: "a\xfe"},
	}
	first, err := MarshalGraphFile(graph)
	if err == nil {
		t.Fatalf("expected rejection, got %s", first)
	}
	want := err.Error()
	for i := 0; i < 20; i++ {
		if _, err := MarshalGraphFile(graph); err.Error() != want {
			t.Fatalf("error message varied across calls:\n%q\n%q", want, err)
		}
		if _, err := BuildSnapshot(graph); err.Error() != want {
			t.Fatalf("BuildSnapshot reports a different name than MarshalGraphFile:\n%q\n%q", want, err)
		}
	}
}

// nodeCopy is a value snapshot of one lineage node for deep comparisons.
type nodeCopy struct {
	Dataset  string
	Parents  []string
	Children []string
}

func snapshotGraph(graph map[string]*Lineage) map[string]nodeCopy {
	cp := make(map[string]nodeCopy, len(graph))
	for name, entry := range graph {
		if entry == nil {
			cp[name] = nodeCopy{}
			continue
		}
		cp[name] = nodeCopy{
			Dataset:  entry.Dataset,
			Parents:  append([]string(nil), entry.Parents...),
			Children: append([]string(nil), entry.Children...),
		}
	}
	return cp
}

// Rejecting an export must not normalize, reorder, truncate, or otherwise
// touch the caller's graph — including deliberately unsorted lists.
func TestRejectedExportLeavesGraphUntouched(t *testing.T) {
	graph := map[string]*Lineage{
		"root\xff": {Dataset: "root\xff", Children: []string{"later", "earlier"}},
		"down":     {Dataset: "down", Parents: []string{"mid\xff", "first"}, Children: []string{}},
	}
	before := snapshotGraph(graph)

	if out, err := MarshalGraphFile(graph); err == nil {
		t.Fatalf("expected rejection, got %s", out)
	}
	if snap, err := BuildSnapshot(graph); err == nil {
		t.Fatalf("expected rejection, got %#v", snap)
	}

	if after := snapshotGraph(graph); !reflect.DeepEqual(after, before) {
		t.Fatalf("graph changed during a rejected export:\nbefore %#v\nafter  %#v", before, after)
	}
}

// A legal graph is also only read during export.
func TestSuccessfulExportLeavesGraphUntouched(t *testing.T) {
	graph := make(map[string]*Lineage)
	if err := Register(graph, Dataset{Name: "root"}, nil); err != nil {
		t.Fatalf("Register root: %v", err)
	}
	if err := Register(graph, Dataset{Name: "derived"}, []string{"root"}); err != nil {
		t.Fatalf("Register derived: %v", err)
	}
	before := snapshotGraph(graph)
	if _, err := MarshalGraphFile(graph); err != nil {
		t.Fatalf("MarshalGraphFile: %v", err)
	}
	if _, err := BuildSnapshot(graph); err != nil {
		t.Fatalf("BuildSnapshot: %v", err)
	}
	if after := snapshotGraph(graph); !reflect.DeepEqual(after, before) {
		t.Fatalf("legal graph changed during export:\nbefore %#v\nafter  %#v", before, after)
	}
}

// Register remains an open ingress for any Go string, including invalid UTF-8;
// validation happens at the export boundary.
func TestRegisterAcceptsInvalidUTF8ButExportRejects(t *testing.T) {
	graph := make(map[string]*Lineage)
	if err := Register(graph, Dataset{Name: "root\xff"}, nil); err != nil {
		t.Fatalf("Register must accept the bytes the Go API was given, got %v", err)
	}
	if err := Register(graph, Dataset{Name: "derived"}, []string{"root\xff"}); err != nil {
		t.Fatalf("Register child of non-UTF-8 root: %v", err)
	}
	if _, err := MarshalGraphFile(graph); !errors.Is(err, ErrInvalidArgument) {
		t.Fatalf("MarshalGraphFile err = %v, want ErrInvalidArgument", err)
	}
	if _, err := BuildSnapshot(graph); !errors.Is(err, ErrInvalidArgument) {
		t.Fatalf("BuildSnapshot err = %v, want ErrInvalidArgument", err)
	}
}

// Names that ARE valid UTF-8 — including a genuine replacement character that
// looks like the rejected output, CJK, emoji, combining-mark variants, casing,
// and surrounding spaces — are accepted verbatim and survive a full round trip
// with no Unicode normalization.
func TestValidUnicodeNamesExportAndRoundTrip(t *testing.T) {
	if !utf8.ValidString("�") {
		t.Fatal("a literal replacement character must be valid UTF-8")
	}
	// "é" is e + combining acute accent; "é" is the precomposed
	// "é". They display alike but are different names and must stay so.
	names := []string{"�", "数据集", "ledger😀", "é", "é", "Ledger", "ledger", " spaced "}
	graph := make(map[string]*Lineage, len(names)+1)
	if err := Register(graph, Dataset{Name: names[0]}, nil); err != nil {
		t.Fatalf("Register: %v", err)
	}
	for _, name := range names[1:] {
		if err := Register(graph, Dataset{Name: name}, []string{names[0]}); err != nil {
			t.Fatalf("Register %q: %v", name, err)
		}
	}

	// Graph export round trip keeps every name and the direct relations.
	data, err := MarshalGraphFile(graph)
	if err != nil {
		t.Fatalf("MarshalGraphFile rejected valid Unicode: %v", err)
	}
	reRead, err := UnmarshalGraphFile(data)
	if err != nil {
		t.Fatalf("re-reading the exported graph: %v", err)
	}
	wantAdj := map[string][]string{}
	for _, name := range names {
		if name == names[0] {
			// A root round-trips with no parents (nil slice); only the
			// absence of an upstream list matters for semantics.
			wantAdj[name] = nil
		} else {
			wantAdj[name] = []string{names[0]}
		}
	}
	gotAdj := map[string][]string{}
	for name, entry := range reRead {
		gotAdj[name] = entry.Parents
	}
	if !reflect.DeepEqual(gotAdj, wantAdj) {
		t.Fatalf("round trip changed names/relations:\n got %#v\nwant %#v", gotAdj, wantAdj)
	}
	if !utf8.ValidString("é") || graph["é"].Parents[0] != "�" {
		t.Fatal("combining-form name must be kept byte-for-byte")
	}

	// Snapshot round trip yields the same content identifier and graph.
	snap, err := BuildSnapshot(graph)
	if err != nil {
		t.Fatalf("BuildSnapshot rejected valid Unicode: %v", err)
	}
	snapBytes, err := MarshalSnapshot(snap)
	if err != nil {
		t.Fatalf("MarshalSnapshot: %v", err)
	}
	parsed, err := ParseSnapshot(snapBytes)
	if err != nil {
		t.Fatalf("ParseSnapshot: %v", err)
	}
	if parsed.ContentID != snap.ContentID {
		t.Fatalf("content id changed across round trip: %q vs %q", parsed.ContentID, snap.ContentID)
	}
	if !reflect.DeepEqual(parsed.Graph, snap.Graph) {
		t.Fatalf("snapshot graph changed across round trip")
	}
}

// An empty graph stays legal at the export boundary after the new gate.
func TestEmptyGraphStillExportsAndSnapshots(t *testing.T) {
	empty := map[string]*Lineage{}
	data, err := MarshalGraphFile(empty)
	if err != nil {
		t.Fatalf("MarshalGraphFile(empty): %v", err)
	}
	if _, err := UnmarshalGraphFile(data); err != nil {
		t.Fatalf("re-reading exported empty graph: %v", err)
	}
	snap, err := BuildSnapshot(empty)
	if err != nil {
		t.Fatalf("BuildSnapshot(empty): %v", err)
	}
	if snap.FormatVersion != SnapshotFormatVersion || snap.ContentID == "" {
		t.Fatalf("empty graph snapshot malformed: %#v", snap)
	}
}
