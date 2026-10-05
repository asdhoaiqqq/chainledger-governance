package chainledger

import (
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
)

// TestReadersShareGraphStructureRules is a parity guard for the consolidation:
// the standalone graph file reader and the snapshot graph reader must agree on
// every graph's validity, on the sentinel error category when invalid, and on
// the normalized graph when valid.
func TestReadersShareGraphStructureRules(t *testing.T) {
	graphs := map[string]string{
		"empty":                 `{"datasets":[]}`,
		"single root":           `{"datasets":[{"name":"A","upstreams":[]}]}`,
		"chain shuffled":        `{"datasets":[{"name":"C","upstreams":["B","A","B"]},{"name":"A","upstreams":[]},{"name":"B","upstreams":["A"]}]}`,
		"case and spaces":       `{"datasets":[{"name":" A ","upstreams":[]},{"name":"a","upstreams":[" A "]}]}`,
		"empty name":            `{"datasets":[{"name":"","upstreams":[]}]}`,
		"duplicate dataset":     `{"datasets":[{"name":"A","upstreams":[]},{"name":"A","upstreams":[]}]}`,
		"missing upstream":      `{"datasets":[{"name":"A","upstreams":["ghost"]}]}`,
		"direct self cycle":     `{"datasets":[{"name":"A","upstreams":["A"]}]}`,
		"indirect cycle":        `{"datasets":[{"name":"A","upstreams":["B"]},{"name":"B","upstreams":["A"]}]}`,
		"empty upstream string": `{"datasets":[{"name":"A","upstreams":[""]}]}`,
		"datasets field twice":  `{"datasets":[],"datasets":[{"name":"A","upstreams":[]}]}`,
		"name field twice":      `{"datasets":[{"name":"A","name":"A","upstreams":[]}]}`,
		"upstreams field twice": `{"datasets":[{"name":"A","upstreams":[],"upstreams":[]}]}`,
	}

	category := func(err error) string {
		switch {
		case err == nil:
			return ""
		case errors.Is(err, ErrInvalidArgument):
			return "invalid"
		case errors.Is(err, ErrNotFound):
			return "notfound"
		case errors.Is(err, ErrCycle):
			return "cycle"
		default:
			return "other"
		}
	}

	for name, data := range graphs {
		t.Run(name, func(t *testing.T) {
			fileGraph, fileErr := UnmarshalGraphFile([]byte(data))

			// For a valid graph, marshal a correctly-addressed snapshot so the
			// snapshot-only content-id rule passes and only the shared graph
			// rules are exercised. For an invalid graph the structure is
			// rejected before content-id integrity, so a dummy id suffices.
			var snapDoc []byte
			if fileErr == nil {
				b, err := MarshalSnapshot(snapshotOf(t, data))
				if err != nil {
					t.Fatalf("marshal snapshot: %v", err)
				}
				snapDoc = b
			} else {
				snapDoc = []byte(`{"formatVersion":1,"contentId":"sha256:deadbeef","graph":` + data + `}`)
			}
			snap, snapErr := ParseSnapshot(snapDoc)

			if category(fileErr) != category(snapErr) {
				t.Fatalf("category mismatch: file=%v snapshot=%v", fileErr, snapErr)
			}
			if fileErr != nil {
				return
			}

			// Valid graph: both readers keep the same normalized graph.
			fileAdj, err := validatedGraphAdjacency(fileGraph)
			if err != nil {
				t.Fatalf("validatedGraphAdjacency: %v", err)
			}
			fileBytes, err := json.Marshal(GraphFile{Datasets: adjacencyToDatasets(fileAdj)})
			if err != nil {
				t.Fatalf("marshal file graph: %v", err)
			}
			snapAdj := adjacencyFromValidFile(snap.Graph)
			snapBytes, err := json.Marshal(GraphFile{Datasets: adjacencyToDatasets(snapAdj)})
			if err != nil {
				t.Fatalf("marshal snapshot graph: %v", err)
			}
			if !reflect.DeepEqual(fileBytes, snapBytes) {
				t.Fatalf("normalized graph differs:\n file %s\n snap %s", fileBytes, snapBytes)
			}
		})
	}
}

// TestUnmarshalGraphFileRejectsDuplicateFields: a known field declared twice
// within the same object makes the whole graph file ambiguous and must be
// rejected, even when the duplicates carry identical values, the first is
// null, or the surviving value alone would form a legal graph. Field names
// are recognized after JSON unescaping and with the decoder's Unicode case
// folding, so escaped or fold-equivalent spellings of a second declaration
// still collide.
func TestUnmarshalGraphFileRejectsDuplicateFields(t *testing.T) {
	const (
		longS       = "ſ" // U+017F LATIN SMALL LETTER LONG S, folds to ASCII "s"
		longSEscape = "\\" + "u017f"
	)

	cases := map[string]struct {
		data     string
		field    string
		location string
	}{
		"datasets twice identical": {
			`{"datasets":[{"name":"A","upstreams":[]}],"datasets":[{"name":"A","upstreams":[]}]}`,
			"datasets", "top level",
		},
		"real datasets then empty replacement": {
			`{"datasets":[{"name":"A","upstreams":[]}],"datasets":[]}`,
			"datasets", "top level",
		},
		"null datasets then real": {
			`{"datasets":null,"datasets":[{"name":"A","upstreams":[]}]}`,
			"datasets", "top level",
		},
		"name twice same value": {
			`{"datasets":[{"name":"A","name":"A","upstreams":[]}]}`,
			"name", `index 0 of "datasets"`,
		},
		"null name then valid": {
			`{"datasets":[{"name":null,"name":"A","upstreams":[]}]}`,
			"name", `index 0 of "datasets"`,
		},
		"upstreams then empty would look like root": {
			`{"datasets":[{"name":"B","upstreams":["A"],"upstreams":[]},{"name":"A","upstreams":[]}]}`,
			"upstreams", `index 0 of "datasets"`,
		},
		"null upstreams then valid": {
			`{"datasets":[{"name":"A","upstreams":[]},{"name":"B","upstreams":null,"upstreams":["A"]}]}`,
			"upstreams", `index 1 of "datasets"`,
		},
		"escaped name key": {
			`{"datasets":[{"na\u006de":"A","name":"A","upstreams":[]}]}`,
			"name", `index 0 of "datasets"`,
		},
		"case variant name": {
			`{"datasets":[{"Name":"A","name":"A","upstreams":[]}]}`,
			"name", `index 0 of "datasets"`,
		},
		"case variant datasets": {
			`{"Datasets":[],"datasets":[{"name":"A","upstreams":[]}]}`,
			"datasets", "top level",
		},
		"case variant upstreams": {
			`{"datasets":[{"name":"A","UPSTREAMS":[],"upstreams":[]}]}`,
			"upstreams", `index 0 of "datasets"`,
		},
		"long s datasets": {
			`{"data` + longS + `ets":[],"datasets":[{"name":"A","upstreams":[]}]}`,
			"datasets", "top level",
		},
		"long s upstreams via JSON escape": {
			`{"datasets":[{"name":"A","up` + longSEscape + `treams":[],"upstreams":[]}]}`,
			"upstreams", `index 0 of "datasets"`,
		},
		"big number in unknown field before duplicated datasets": {
			`{"note":1e400,"datasets":[],"datasets":[{"name":"A","upstreams":[]}]}`,
			"datasets", "top level",
		},
		"big number nested in record before duplicated upstreams": {
			`{"datasets":[{"name":"A","vals":{"deep":[1e999]},"upstreams":[],"upstreams":[]}]}`,
			"upstreams", `index 0 of "datasets"`,
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			// Every "escape" case really must carry ASCII escape bytes.
			if strings.Contains(name, "escape") && !strings.Contains(tc.data, longSEscape) && !strings.Contains(tc.data, `\u006d`) {
				t.Fatalf("test case %q does not actually use a JSON escape", name)
			}
			_, err := UnmarshalGraphFile([]byte(tc.data))
			if !errors.Is(err, ErrInvalidArgument) {
				t.Fatalf("err = %v, want ErrInvalidArgument", err)
			}
			if !strings.Contains(err.Error(), `"`+tc.field+`"`) {
				t.Errorf("err = %q, want it to name duplicated field %q", err, tc.field)
			}
			if !strings.Contains(err.Error(), tc.location) {
				t.Errorf("err = %q, want it to locate the duplicate %q", err, tc.location)
			}
		})
	}
}

// TestUnmarshalGraphFileDuplicateTolerances: repetitions that do NOT create
// ambiguity stay legal — unknown fields may repeat anywhere (even holding
// duplicate keys or oversized numbers inside), each dataset record declares
// its own name, and duplicate names inside one upstreams array still count as
// one relationship. A known field written once in a case-variant or
// fold-equivalent spelling is still recognized.
func TestUnmarshalGraphFileDuplicateTolerances(t *testing.T) {
	cases := map[string]string{
		"empty graph":                     `{"datasets":[]}`,
		"unknown top-level field repeats": `{"meta":"x","meta":"y","datasets":[{"name":"A","upstreams":[]}]}`,
		"unknown record field repeats":    `{"datasets":[{"name":"A","note":1,"note":2,"upstreams":[]}]}`,
		"known keys nested inside unknown field": `{"meta":{"name":"A","name":"B","datasets":[],"upstreams":null},` +
			`"datasets":[{"name":"A","upstreams":[]}]}`,
		"big numbers in unknown fields":          `{"note":1e400,"datasets":[{"name":"A","upstreams":[],"extra":[1e999,{"z":-1e400}]}]}`,
		"case variant spellings declared once":   `{"Datasets":[{"Name":"A","Upstreams":[]},{"NAME":"B","UPSTREAMS":["A"]}]}`,
		"duplicate names inside upstreams array": `{"datasets":[{"name":"A","upstreams":[]},{"name":"B","upstreams":["A","A"]}]}`,
	}
	for name, data := range cases {
		t.Run(name, func(t *testing.T) {
			graph, err := UnmarshalGraphFile([]byte(data))
			if err != nil {
				t.Fatalf("UnmarshalGraphFile rejected a legal document: %v", err)
			}
			if name == "duplicate names inside upstreams array" {
				if got := graph["B"].Parents; len(got) != 1 || got[0] != "A" {
					t.Errorf("B.Parents = %v, want [A] (duplicates count once)", got)
				}
			}
		})
	}

	// Dataset names stay case-sensitive and keep their spaces; only field
	// names fold.
	graph, err := UnmarshalGraphFile([]byte(`{"datasets":[{"name":" A ","upstreams":[]},{"name":"a","upstreams":[" A "]}]}`))
	if err != nil {
		t.Fatalf("case/space-sensitive dataset names rejected: %v", err)
	}
	if got := graph["a"].Parents; len(got) != 1 || got[0] != " A " {
		t.Errorf(`graph["a"].Parents = %v, want [" A "]`, got)
	}
}
