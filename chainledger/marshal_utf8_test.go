package chainledger

import (
	"errors"
	"reflect"
	"strings"
	"testing"
)

// The fixtures below bypass Register only where the graph shape requires it;
// Register itself accepts invalid-UTF-8 names, so buildGraph works for most
// cases. Invalid UTF-8 can only exist in an in-memory graph: the JSON readers
// never produce it (encoding/json replaces invalid bytes while decoding).

// TestMarshalGraphFileRejectsInvalidUTF8 verifies the export refuses the whole
// graph when any dataset name or direct upstream reference is not valid UTF-8,
// instead of silently rewriting the bytes to U+FFFD.
func TestMarshalGraphFileRejectsInvalidUTF8(t *testing.T) {
	t.Run("root with invalid UTF-8 name", func(t *testing.T) {
		graph := buildGraph(t, P("chain\xFF"))
		data, err := MarshalGraphFile(graph)
		if !errors.Is(err, ErrInvalidArgument) {
			t.Fatalf("err = %v, want ErrInvalidArgument", err)
		}
		if data != nil {
			t.Fatalf("rejected export returned bytes: %s", data)
		}
		// The original bytes must be recognizable: %q renders them as \xff,
		// never as the replacement character the JSON encoder would emit.
		if !strings.Contains(err.Error(), `chain\xff`) {
			t.Errorf("error %q does not show the original invalid bytes", err)
		}
		if strings.Contains(err.Error(), "�") {
			t.Errorf("error %q shows the rewritten name, not the original", err)
		}
	})

	t.Run("derived dataset with invalid UTF-8 name", func(t *testing.T) {
		graph := buildGraph(t,
			P("root"),
			P("deri\xFEved", "root"),
		)
		data, err := MarshalGraphFile(graph)
		if !errors.Is(err, ErrInvalidArgument) {
			t.Fatalf("err = %v, want ErrInvalidArgument", err)
		}
		if data != nil {
			t.Fatalf("rejected export returned bytes: %s", data)
		}
		if !strings.Contains(err.Error(), `deri\xfeved`) {
			t.Errorf("error %q does not show the original invalid bytes", err)
		}
	})

	t.Run("direct upstream reference with invalid UTF-8 name", func(t *testing.T) {
		// The upstream node exists under its invalid name, so this is purely
		// the encoding rule, not a missing-upstream rejection.
		graph := buildGraph(t,
			P("up\xFFstream"),
			P("down", "up\xFFstream"),
		)
		data, err := MarshalGraphFile(graph)
		if !errors.Is(err, ErrInvalidArgument) {
			t.Fatalf("err = %v, want ErrInvalidArgument", err)
		}
		if data != nil {
			t.Fatalf("rejected export returned bytes: %s", data)
		}
		if !strings.Contains(err.Error(), `up\xffstream`) {
			t.Errorf("error %q does not show the original invalid bytes", err)
		}
	})

	t.Run("single invalid name rejects even without any collision", func(t *testing.T) {
		// One node, no other name it could collapse into: the export must
		// still fail, because the bytes would not be a faithful copy.
		graph := buildGraph(t, P("only\xC0node"))
		if _, err := MarshalGraphFile(graph); !errors.Is(err, ErrInvalidArgument) {
			t.Fatalf("err = %v, want ErrInvalidArgument", err)
		}
	})

	t.Run("distinct invalid names must not collapse into identical records", func(t *testing.T) {
		// "prefix\xFF" and "prefix\xFE" would both surface as "prefix�" in
		// JSON: two different datasets exported as two identical records.
		graph := buildGraph(t, P("prefix\xFF"), P("prefix\xFE"))
		data, err := MarshalGraphFile(graph)
		if !errors.Is(err, ErrInvalidArgument) {
			t.Fatalf("err = %v, want ErrInvalidArgument", err)
		}
		if data != nil {
			t.Fatalf("rejected export returned bytes: %s", data)
		}
	})

	t.Run("invalid name on a disconnected branch rejects whole graph", func(t *testing.T) {
		// A<-B is healthy; the bad node is unrelated. The export must not
		// silently deliver only the healthy part.
		graph := buildGraph(t,
			P("A"),
			P("B", "A"),
			P("bad\x80node"),
		)
		data, err := MarshalGraphFile(graph)
		if !errors.Is(err, ErrInvalidArgument) {
			t.Fatalf("err = %v, want ErrInvalidArgument", err)
		}
		if data != nil {
			t.Fatalf("rejected export returned bytes: %s", data)
		}
	})
}

// TestBuildSnapshotRejectsInvalidUTF8 verifies snapshot generation refuses the
// same graphs the export refuses: no snapshot object and no content identifier
// may be produced for a graph whose names cannot be frozen faithfully.
func TestBuildSnapshotRejectsInvalidUTF8(t *testing.T) {
	t.Run("invalid root name", func(t *testing.T) {
		graph := buildGraph(t, P("chain\xFF"))
		snap, err := BuildSnapshot(graph)
		if !errors.Is(err, ErrInvalidArgument) {
			t.Fatalf("err = %v, want ErrInvalidArgument", err)
		}
		if snap != nil {
			t.Fatalf("rejected graph produced snapshot: %+v", snap)
		}
	})

	t.Run("invalid upstream reference", func(t *testing.T) {
		graph := buildGraph(t,
			P("up\xFEstream"),
			P("down", "up\xFEstream"),
		)
		snap, err := BuildSnapshot(graph)
		if !errors.Is(err, ErrInvalidArgument) {
			t.Fatalf("err = %v, want ErrInvalidArgument", err)
		}
		if snap != nil {
			t.Fatalf("rejected graph produced snapshot: %+v", snap)
		}
	})

	t.Run("colliding-after-replacement roots", func(t *testing.T) {
		graph := buildGraph(t, P("prefix\xFF"), P("prefix\xFE"))
		snap, err := BuildSnapshot(graph)
		if !errors.Is(err, ErrInvalidArgument) {
			t.Fatalf("err = %v, want ErrInvalidArgument", err)
		}
		if snap != nil {
			t.Fatalf("rejected graph produced snapshot with content id %q", snap.ContentID)
		}
	})
}

// TestInvalidUTF8RejectionIsReadOnly verifies a rejected export or snapshot
// leaves the caller's graph exactly as it was: node set, names, and the
// content and order of the parent/child lists.
func TestInvalidUTF8RejectionIsReadOnly(t *testing.T) {
	graph := buildGraph(t,
		P("root"),
		P("deri\xFFved", "root"),
		P("leaf", "deri\xFFved"),
	)
	before := cloneGraph(graph)

	if _, err := MarshalGraphFile(graph); err == nil {
		t.Fatalf("expected export to reject invalid UTF-8")
	}
	if !reflect.DeepEqual(graph, before) {
		t.Fatalf("graph mutated by rejected export:\n got %#v\nwant %#v", graph, before)
	}

	if _, err := BuildSnapshot(graph); err == nil {
		t.Fatalf("expected snapshot to reject invalid UTF-8")
	}
	if !reflect.DeepEqual(graph, before) {
		t.Fatalf("graph mutated by rejected snapshot:\n got %#v\nwant %#v", graph, before)
	}
}

// TestValidUTF8NamesExportUnchanged verifies names that are valid UTF-8 —
// including a genuine U+FFFD, non-ASCII scripts, emoji, and distinct Unicode
// compositions of the same visible character — are kept verbatim and never
// rejected or normalized for looking like an invalid name.
func TestValidUTF8NamesExportUnchanged(t *testing.T) {
	t.Run("genuine replacement character is a normal name", func(t *testing.T) {
		graph := buildGraph(t,
			P("chain�"),
			P("deri�ved", "chain�"),
		)
		data, err := MarshalGraphFile(graph)
		if err != nil {
			t.Fatalf("genuine U+FFFD name rejected: %v", err)
		}
		parsed, err := UnmarshalGraphFile(data)
		if err != nil {
			t.Fatalf("reader rejected exported graph: %v", err)
		}
		if !reflect.DeepEqual(parsed, graph) {
			t.Fatalf("round-trip mismatch:\n got %#v\nwant %#v", parsed, graph)
		}
	})

	t.Run("non-ASCII scripts and emoji survive verbatim", func(t *testing.T) {
		graph := buildGraph(t,
			P("链上数据"),
			P("derived-🚀", "链上数据"),
		)
		data, err := MarshalGraphFile(graph)
		if err != nil {
			t.Fatalf("non-ASCII names rejected: %v", err)
		}
		parsed, err := UnmarshalGraphFile(data)
		if err != nil {
			t.Fatalf("reader rejected exported graph: %v", err)
		}
		if !reflect.DeepEqual(parsed, graph) {
			t.Fatalf("round-trip mismatch:\n got %#v\nwant %#v", parsed, graph)
		}
	})

	t.Run("distinct Unicode compositions are not normalized", func(t *testing.T) {
		// "é" as one code point (U+00E9) and as "e" + combining accent
		// (U+0065 U+0301) look identical but are different names; both must
		// survive as separate records with their original bytes.
		precomposed := "café"
		decomposed := "café"
		graph := buildGraph(t,
			P(precomposed),
			P(decomposed),
			P("report", precomposed, decomposed),
		)
		data, err := MarshalGraphFile(graph)
		if err != nil {
			t.Fatalf("distinct compositions rejected: %v", err)
		}
		parsed, err := UnmarshalGraphFile(data)
		if err != nil {
			t.Fatalf("reader rejected exported graph: %v", err)
		}
		if !reflect.DeepEqual(parsed, graph) {
			t.Fatalf("names were normalized:\n got %#v\nwant %#v", parsed, graph)
		}
		if _, err := BuildSnapshot(graph); err != nil {
			t.Fatalf("snapshot rejected distinct compositions: %v", err)
		}
	})
}
