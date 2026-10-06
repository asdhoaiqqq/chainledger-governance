package chainledger

import (
	"encoding/json"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"unicode/utf8"
)

// Registration only rejects empty names, so names carrying invalid UTF-8 bytes
// can enter the graph. Such a name cannot be handed to a JSON reader without
// loss (encoding/json substitutes U+FFFD), so exporting a lineage that
// contains one must fail outright rather than report success.

// An invalid name on the target itself fails the export with an empty string;
// the error renders the actual offending bytes instead of the U+FFFD that a
// JSON reader would see.
func TestExportUpstreamLineageInvalidUTF8Target(t *testing.T) {
	bad := "report\xffbroken"
	graph := buildRegisteredGraph(t, [][]string{
		{"raw"},
		{bad, "raw"},
	})
	before := snapshotExportGraph(graph)

	out, err := ExportUpstreamLineage(graph, bad)
	if err == nil {
		t.Fatalf("export succeeded with %q, want an encoding error", out)
	}
	if out != "" {
		t.Errorf("failed export returned partial content %q, want empty string", out)
	}
	if !strings.Contains(err.Error(), "not valid UTF-8") {
		t.Errorf("error %q must state that the name encoding is invalid", err)
	}
	// The real bytes must appear, Go-escaped so even the unprintable 0xff byte
	// is distinguishable; a replacement-character rendering would lose it.
	if !strings.Contains(err.Error(), strconv.Quote(bad)) {
		t.Errorf("error %q must name the exact bytes %s", err, strconv.Quote(bad))
	}
	if strings.ContainsRune(err.Error(), '�') {
		t.Errorf("error %q describes the name with the replacement character", err)
	}
	assertGraphUnchanged(t, before, graph)
}

// An invalid name anywhere in the ancestor closure fails the whole export,
// including an indirect upstream several branches away.
func TestExportUpstreamLineageInvalidUTF8Upstream(t *testing.T) {
	bad := "raw\x80bad"
	graph := buildRegisteredGraph(t, [][]string{
		{bad},
		{"a", bad},
		{"b", bad},
		{"mid", "b"},
		{"report", "a", "mid"},
	})
	before := snapshotExportGraph(graph)

	out, err := ExportUpstreamLineage(graph, "report")
	if err == nil {
		t.Fatalf("export succeeded with %q, want an encoding error", out)
	}
	if out != "" {
		t.Errorf("failed export returned partial content %q, want empty string", out)
	}
	if !strings.Contains(err.Error(), strconv.Quote(bad)) {
		t.Errorf("error %q must name the offending upstream %s", err, strconv.Quote(bad))
	}
	assertGraphUnchanged(t, before, graph)
}

// Different invalid bytes must be distinguishable in the error text: Go
// escaping shows the bytes separately instead of collapsing both onto U+FFFD.
func TestExportUpstreamLineageInvalidUTF8DistinguishesBytes(t *testing.T) {
	cases := []string{
		"name\xff",
		"name\x80",
		"name\xe2\x82", // truncated multibyte sequence
	}
	var messages []string
	for _, bad := range cases {
		graph := buildRegisteredGraph(t, [][]string{{bad}})
		_, err := ExportUpstreamLineage(graph, bad)
		if err == nil {
			t.Fatalf("export of %s succeeded, want error", strconv.Quote(bad))
		}
		messages = append(messages, err.Error())
	}
	seen := map[string]bool{}
	for i, msg := range messages {
		if seen[msg] {
			t.Fatalf("byte-distinct names %s produced a shared error message %q", strconv.Quote(cases[i]), msg)
		}
		seen[msg] = true
	}
}

// When several included names are invalid, the error always names the one
// earliest in Go string order of the raw names, regardless of registration
// order or stored upstream-list order.
func TestExportUpstreamLineageInvalidUTF8DeterministicFirst(t *testing.T) {
	first := "bad\x80" // 0x80 sorts before 0xff
	later := "bad\xff"
	buildA := func() map[string]*Lineage {
		return buildRegisteredGraph(t, [][]string{
			{first},
			{later},
			{"report", first, later},
		})
	}
	buildB := func() map[string]*Lineage {
		return buildRegisteredGraph(t, [][]string{
			{later},
			{first},
			{"report", later, first},
		})
	}

	_, errA := ExportUpstreamLineage(buildA(), "report")
	_, errB := ExportUpstreamLineage(buildB(), "report")
	if errA == nil || errB == nil {
		t.Fatalf("both exports must fail, got %v, %v", errA, errB)
	}
	if errA.Error() != errB.Error() {
		t.Errorf("error depends on registration/list order:\n%q\n%q", errA, errB)
	}
	if !strings.Contains(errA.Error(), strconv.Quote(first)) {
		t.Errorf("error %q must name the first-sorted invalid name %s, not %s",
			errA, strconv.Quote(first), strconv.Quote(later))
	}
	if strings.Contains(errA.Error(), strconv.Quote(later)) {
		t.Errorf("error %q must not name the later invalid name %s", errA, strconv.Quote(later))
	}
}

// Only the exported closure is judged: a bad name on an unrelated dataset, or
// on the target's downstream, must not block the target's upstream document.
func TestExportUpstreamLineageInvalidUTF8OutsideClosureIgnored(t *testing.T) {
	badDownstream := "view\xff"
	badUnrelated := "loner\xfe"
	graph := buildRegisteredGraph(t, [][]string{
		{"raw"},
		{"report", "raw"},
		{badDownstream, "report"},
		{badUnrelated},
	})
	before := snapshotExportGraph(graph)

	out := mustExport(t, graph, "report")
	doc := parseExport(t, out)
	wantNodes := []string{"raw", "report"}
	if !reflect.DeepEqual(doc.Nodes, wantNodes) {
		t.Errorf("nodes = %v, want %v", doc.Nodes, wantNodes)
	}
	wantEdges := [][2]string{{"raw", "report"}}
	if got := exportEdgeNames(doc); !reflect.DeepEqual(got, wantEdges) {
		t.Errorf("edges = %v, want %v", got, wantEdges)
	}
	assertGraphUnchanged(t, before, graph)

	// The bad downstream itself still cannot be exported: its own closure
	// contains its invalid name.
	if out, err := ExportUpstreamLineage(graph, badDownstream); err == nil {
		t.Errorf("export of invalid-named downstream succeeded with %q", out)
	} else if out != "" {
		t.Errorf("failed export returned partial content %q", out)
	}
}

// Missing-name and not-found checks keep priority over encoding: an empty or
// unregistered target reports its existing error even when the graph also
// holds invalid names, both inside and outside the would-be closure.
func TestExportUpstreamLineageInvalidUTF8ErrorPrecedence(t *testing.T) {
	graph := buildRegisteredGraph(t, [][]string{
		{"raw"},
		{"report", "raw"},
		{"bad\xff", "raw"},
	})
	cases := []struct {
		name    string
		target  string
		wantErr string
	}{
		{"empty target", "", "dataset name is required"},
		{"unregistered target", "ghost", "dataset not found: ghost"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			out, err := ExportUpstreamLineage(graph, tc.target)
			if err == nil {
				t.Fatalf("ExportUpstreamLineage(%q) succeeded with %q", tc.target, out)
			}
			if err.Error() != tc.wantErr {
				t.Errorf("error = %q, want %q", err, tc.wantErr)
			}
			if out != "" {
				t.Errorf("failed export returned partial content %q", out)
			}
		})
	}
}

// Valid-but-fancy names export losslessly: Chinese, emoji, quotes, backslashes,
// newlines and a genuine U+FFFD character all survive a JSON round trip with
// the exact registered value, and the real replacement character is not itself
// treated as a bad byte.
func TestExportUpstreamLineageUnicodeRoundTrip(t *testing.T) {
	names := []string{
		"数据集-α",
		"report😀",
		`say "hi"`,
		`a\b`,
		"line1\nline2",
		"real�char", // an actual U+FFFD rune, valid UTF-8
		"raw",
	}
	graph := buildRegisteredGraph(t, [][]string{
		{names[6]},
		{names[0]},
		{names[1], names[0], names[6]},
		{names[2], names[6]},
		{names[3], names[2]},
		{names[4], names[3], names[1]},
		{names[5], names[4]},
	})
	for _, name := range names {
		if !utf8.ValidString(name) {
			t.Fatalf("test setup: %s is not valid UTF-8", strconv.Quote(name))
		}
	}

	out := mustExport(t, graph, names[5])

	// Byte-level check: the genuine U+FFFD is written verbatim in the name
	// (node and edge), not emitted as a JSON backslash-u-fffd escape; a real
	// character is therefore not treated as a bad byte.
	if !strings.Contains(out, `"real�char"`) {
		t.Errorf("export must write the real U+FFFD character verbatim: %s", out)
	}
	if strings.Contains(out, "\\ufffd") {
		t.Errorf("export must not emit U+FFFD escapes for valid names: %s", out)
	}

	var doc struct {
		Nodes []string `json:"nodes"`
		Edges []struct {
			From string `json:"from"`
			To   string `json:"to"`
		} `json:"edges"`
	}
	if err := json.Unmarshal([]byte(out), &doc); err != nil {
		t.Fatalf("exported text is not valid JSON: %v\n%s", err, out)
	}

	registered := map[string]bool{}
	for _, name := range names {
		registered[name] = true
	}
	got := map[string]bool{}
	for _, name := range doc.Nodes {
		got[name] = true
		if !registered[name] {
			t.Errorf("unexpected or altered node %s in export", strconv.Quote(name))
		}
	}
	if len(got) != len(registered) {
		t.Errorf("nodes = %v, want the %d registered names", doc.Nodes, len(registered))
	}
	for _, e := range doc.Edges {
		if !registered[e.From] || !registered[e.To] {
			t.Errorf("edge %s -> %s references a name not registered verbatim",
				strconv.Quote(e.From), strconv.Quote(e.To))
		}
	}
}
