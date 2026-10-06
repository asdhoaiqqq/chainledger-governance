package chainledger

import (
	"reflect"
	"sort"
	"strings"
	"testing"
)

// A name containing an invalid UTF-8 byte cannot be handed to a JSON reader
// losslessly: encoding/json would rewrite the byte to U+FFFD and report
// success. Exporting a target whose own name is invalid must instead fail
// outright, return no document, and describe the offending bytes.
func TestExportUpstreamLineageRejectsInvalidUTF8Target(t *testing.T) {
	bad := "a\xffb"
	graph := buildRegisteredGraph(t, [][]string{
		{bad},
	})
	before := snapshotExportGraph(graph)

	out, err := ExportUpstreamLineage(graph, bad)
	if err == nil {
		t.Fatalf("export with invalid target succeeded with %q, want an encoding error", out)
	}
	if out != "" {
		t.Errorf("failed export returned partial content %q, want empty string", out)
	}
	// The error must expose the actual byte (\xff) in Go-quoted form rather
	// than a replacement glyph that cannot be told apart from any other bad
	// byte.
	if !strings.Contains(err.Error(), `"a\xffb"`) {
		t.Errorf("error %q must quote the original bytes, want substring %q", err, `"a\xffb"`)
	}
	assertGraphUnchanged(t, before, graph)
}

// An invalid name anywhere in the exported closure — here a transitive
// upstream — fails the whole export; nothing partial is returned.
func TestExportUpstreamLineageRejectsInvalidUTF8Ancestor(t *testing.T) {
	rootBad := "root\xff"
	graph := buildRegisteredGraph(t, [][]string{
		{rootBad},
		{"mid", rootBad},
		{"report", "mid"},
	})
	before := snapshotExportGraph(graph)

	out, err := ExportUpstreamLineage(graph, "report")
	if err == nil {
		t.Fatalf("export with invalid ancestor succeeded with %q, want an encoding error", out)
	}
	if out != "" {
		t.Errorf("failed export returned partial content %q, want empty string", out)
	}
	if !strings.Contains(err.Error(), `"root\xff"`) {
		t.Errorf("error %q must name the invalid upstream by its bytes", err)
	}
	assertGraphUnchanged(t, before, graph)
}

// Two names broken by different bytes must produce distinguishable errors;
// %q renders each byte separately instead of one shared replacement glyph.
func TestExportUpstreamLineageDistinguishesBadBytes(t *testing.T) {
	ff := "x\xff"
	fe := "x\xfe"
	g1 := buildRegisteredGraph(t, [][]string{{ff}})
	g2 := buildRegisteredGraph(t, [][]string{{fe}})

	_, err1 := ExportUpstreamLineage(g1, ff)
	_, err2 := ExportUpstreamLineage(g2, fe)
	if err1 == nil || err2 == nil {
		t.Fatalf("both invalid names must be rejected, got %v and %v", err1, err2)
	}
	if err1.Error() == err2.Error() {
		t.Fatalf("different bad bytes produced the same error %q", err1)
	}
	if !strings.Contains(err1.Error(), `\xff`) {
		t.Errorf("error %q must show byte 0xff", err1)
	}
	if !strings.Contains(err2.Error(), `\xfe`) {
		t.Errorf("error %q must show byte 0xfe", err2)
	}
}

// When several included names are invalid, the one sorting first in Go string
// order is the one reported, regardless of registration order or the order of
// the target's stored upstream list: identical lineage, identical error.
func TestExportUpstreamLineageFirstInvalidNameOrderIndependent(t *testing.T) {
	fe := "x\xfe"
	ff := "x\xff"
	first := buildRegisteredGraph(t, [][]string{
		{"root"},
		{fe, "root"},
		{ff, "root"},
		{"report", fe, ff},
	})
	// Same graph, but built in a different order with report's upstream list
	// reversed.
	second := buildRegisteredGraph(t, [][]string{
		{"root"},
		{ff, "root"},
		{fe, "root"},
		{"report", ff, fe},
	})

	_, err1 := ExportUpstreamLineage(first, "report")
	_, err2 := ExportUpstreamLineage(second, "report")
	if err1 == nil || err2 == nil {
		t.Fatalf("exports with invalid upstreams must fail, got %v and %v", err1, err2)
	}
	if err1.Error() != err2.Error() {
		t.Errorf("error depends on registration/list order:\nfirst:  %q\nsecond: %q", err1, err2)
	}
	// "\xfe" sorts before "\xff" in Go string order.
	if !strings.Contains(err1.Error(), `"x\xfe"`) {
		t.Errorf("error %q must report the string-smallest invalid name x\\xfe", err1)
	}
}

// Only nodes in the target's own upstream closure are judged. An unrelated
// dataset and the target's downstreams may carry invalid names without
// blocking this export.
func TestExportUpstreamLineageIgnoresInvalidNamesOutsideClosure(t *testing.T) {
	graph := buildRegisteredGraph(t, [][]string{
		{"good"},
		{"unrelated\xff"},
		{"downstream\xfe", "good"},
	})

	out := mustExport(t, graph, "good")
	if want := `{"nodes":["good"],"edges":[]}`; out != want {
		t.Errorf("export = %s, want %s", out, want)
	}
}

// Missing-name and not-found checks keep priority over encoding validation,
// even while the graph holds invalid names.
func TestExportUpstreamLineageErrorsPrecedence(t *testing.T) {
	graph := buildRegisteredGraph(t, [][]string{
		{"bad\xff"},
	})
	for _, tc := range []struct {
		target  string
		wantErr string
	}{
		{"", "dataset name is required"},
		{"ghost", "dataset not found: ghost"},
	} {
		out, err := ExportUpstreamLineage(graph, tc.target)
		if err == nil {
			t.Fatalf("ExportUpstreamLineage(%q) succeeded with %q", tc.target, out)
		}
		if err.Error() != tc.wantErr {
			t.Errorf("target %q: error = %q, want %q", tc.target, err, tc.wantErr)
		}
		if out != "" {
			t.Errorf("target %q returned partial content %q", tc.target, out)
		}
	}
}

// Valid names with multibyte text, emoji, JSON-special characters and a real
// replacement character (U+FFFD) export normally and decode back to exactly
// the registered values. The real U+FFFD rune must not be mistaken for a bad
// byte, even though a lone 0xff byte in another dataset is one.
func TestExportUpstreamLineageRoundTripsUnusualValidNames(t *testing.T) {
	chinese := "数据集"
	emoji := "report 🚀"
	special := "q \" \\ \n end"
	replacement := "repaired�name"
	loneFF := "lone\xff"
	graph := buildRegisteredGraph(t, [][]string{
		{chinese},
		{emoji, chinese},
		{special, emoji},
		{replacement, special},
		{loneFF}, // invalid name registered, but outside replacement's closure
	})

	out := mustExport(t, graph, replacement)
	doc := parseExport(t, out)
	wantNodes := []string{chinese, emoji, replacement, special}
	sort.Strings(wantNodes) // the export's own ordering rule
	if !reflect.DeepEqual(doc.Nodes, wantNodes) {
		t.Errorf("nodes = %q, want %q", doc.Nodes, wantNodes)
	}
	for _, name := range doc.Nodes {
		if strings.ContainsRune(name, '�') && name != replacement {
			t.Errorf("valid name was mangled to a replacement rune: %q", name)
		}
	}

	// A node whose name is the genuine U+FFFD rune exports alone and survives
	// the JSON round trip; a node whose name carries a real lone 0xff byte is
	// rejected. The two never collapse into the same documented node.
	solo := buildRegisteredGraph(t, [][]string{{replacement}})
	soloOut := mustExport(t, solo, replacement)
	soloDoc := parseExport(t, soloOut)
	if !reflect.DeepEqual(soloDoc.Nodes, []string{replacement}) {
		t.Errorf("real U+FFFD name did not round-trip: %q", soloDoc.Nodes)
	}
	if _, err := ExportUpstreamLineage(buildRegisteredGraph(t, [][]string{{loneFF}}), loneFF); err == nil {
		t.Fatal("name with a lone 0xff byte must not export")
	}
}
