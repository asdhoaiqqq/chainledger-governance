package chainledger

import (
	"errors"
	"strings"
	"testing"
)

// TestUnmarshalGraphFileRejectsDuplicateFields: a known field declared twice
// within the same object makes the whole graph file ambiguous and must be
// rejected, even when the duplicates carry identical values, the first is
// null, or the surviving value would pass every other check. Scope: datasets
// at the top level of the graph object, name and upstreams inside each
// dataset record — the same graph-level rule the snapshot reader enforces.
func TestUnmarshalGraphFileRejectsDuplicateFields(t *testing.T) {
	cases := map[string]struct {
		data     string
		field    string
		location string
	}{
		"datasets twice empty": {
			`{"datasets":[],"datasets":[]}`,
			"datasets", "top level",
		},
		"empty datasets then real": {
			`{"datasets":[],"datasets":[{"name":"A","upstreams":[]}]}`,
			"datasets", "top level",
		},
		"null datasets then valid": {
			`{"datasets":null,"datasets":[{"name":"A","upstreams":[]}]}`,
			"datasets", "top level",
		},
		"name twice same value": {
			`{"datasets":[{"name":"A","name":"A","upstreams":[]}]}`,
			"name", "index 0",
		},
		"null name then valid": {
			`{"datasets":[{"name":null,"name":"A","upstreams":[]}]}`,
			"name", "index 0",
		},
		"upstreams twice": {
			`{"datasets":[{"name":"A","upstreams":[],"upstreams":[]}]}`,
			"upstreams", "index 0",
		},
		"upstream list then empty list": {
			// Silently keeping only one declaration would either invent or
			// drop the A -> B relationship.
			`{"datasets":[{"name":"A","upstreams":["B"],"upstreams":[]},{"name":"B","upstreams":[]}]}`,
			"upstreams", "index 0",
		},
		"escaped name key": {
			// na\u006de decodes to "name" before the comparison.
			`{"datasets":[{"na\u006de":"A","name":"A","upstreams":[]}]}`,
			"name", "index 0",
		},
		"escaped datasets key": {
			// data\u0073ets decodes to "datasets" before the comparison.
			`{"data\u0073ets":[],"datasets":[]}`,
			"datasets", "top level",
		},
		"case variant name": {
			`{"datasets":[{"Name":"A","name":"A","upstreams":[]}]}`,
			"name", "index 0",
		},
		"case variant datasets": {
			`{"Datasets":[],"datasets":[]}`,
			"datasets", "top level",
		},
		"unicode fold upstreams": {
			// The long s folds to s, so this key selects upstreams.
			`{"datasets":[{"name":"A","upſtreams":[],"upstreams":[]}]}`,
			"upstreams", "index 0",
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := UnmarshalGraphFile([]byte(tc.data))
			if !errors.Is(err, ErrInvalidArgument) {
				t.Fatalf("err = %v, want ErrInvalidArgument", err)
			}
			if !strings.Contains(err.Error(), `"`+tc.field+`"`) {
				t.Errorf("err = %q, want it to name field %q", err, tc.field)
			}
			if !strings.Contains(err.Error(), tc.location) {
				t.Errorf("err = %q, want it to locate %q", err, tc.location)
			}
		})
	}

	// The duplicate lives in the second dataset record; the error must locate
	// it by datasets array index.
	indexed := `{"datasets":[{"name":"A","upstreams":[]},{"name":"B","upstreams":["A"],"upstreams":["A"]}]}`
	_, err := UnmarshalGraphFile([]byte(indexed))
	if err == nil || !strings.Contains(err.Error(), "index 1") {
		t.Errorf("err = %v, want the datasets index of the offending record", err)
	}
}

// TestUnmarshalGraphFileLargeNumberCannotMaskDuplicates: a legal JSON number
// outside the float64 range (1e400) sitting in an ignored unknown field must
// not abort the duplicate-field scan and hide a repeated known field placed
// after it — neither at the top level nor inside a dataset record.
func TestUnmarshalGraphFileLargeNumberCannotMaskDuplicates(t *testing.T) {
	cases := map[string]struct {
		data  string
		field string
	}{
		"big number before duplicated datasets": {
			`{"note":1e400,"datasets":[],"datasets":[{"name":"A","upstreams":[]}]}`,
			"datasets",
		},
		"big number nested in unknown object": {
			`{"meta":{"deep":[1e400]},"datasets":null,"datasets":[]}`,
			"datasets",
		},
		"big number inside record before duplicated upstreams": {
			`{"datasets":[{"name":"A","meta":[1e400],"upstreams":[],"upstreams":[]}]}`,
			"upstreams",
		},
		"duplicate before the big number": {
			`{"datasets":[],"datasets":[],"note":1e400}`,
			"datasets",
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := UnmarshalGraphFile([]byte(tc.data))
			if !errors.Is(err, ErrInvalidArgument) {
				t.Fatalf("err = %v, want ErrInvalidArgument", err)
			}
			if !strings.Contains(err.Error(), `"`+tc.field+`"`) {
				t.Errorf("err = %q, want it to name field %q", err, tc.field)
			}
		})
	}
}

// TestUnmarshalGraphFileDuplicateTolerances: repetitions that do NOT create
// ambiguity stay legal — unknown extension fields may repeat and may contain
// anything (including objects with name/upstreams keys and legal large
// numbers), duplicate upstream names still count as one relationship, and
// dataset names stay case-sensitive with whitespace kept.
func TestUnmarshalGraphFileDuplicateTolerances(t *testing.T) {
	cases := map[string]string{
		"empty graph":                                      `{"datasets":[]}`,
		"unknown top-level field twice":                    `{"note":1,"note":2,"datasets":[]}`,
		"unknown object with known-looking keys":           `{"meta":{"name":"x","name":"y","upstreams":[1],"upstreams":[2]},"datasets":[{"name":"A","upstreams":[]}]}`,
		"unknown field in record twice":                    `{"datasets":[{"name":"A","upstreams":[],"tag":"x","tag":"y"}]}`,
		"unknown object in record with known-looking keys": `{"datasets":[{"name":"A","upstreams":[],"meta":{"name":"n","upstreams":[1e400],"name":"m"}}]}`,
		"duplicate upstream names":                         `{"datasets":[{"name":"A","upstreams":["B","B"]},{"name":"B","upstreams":[]}]}`,
		"records each with name and upstreams":             `{"datasets":[{"name":"A","upstreams":[]},{"name":"B","upstreams":["A"]}]}`,
		"dataset names case sensitive":                     `{"datasets":[{"name":"A","upstreams":[]},{"name":"a","upstreams":["A"]}]}`,
		"dataset names keep whitespace":                    `{"datasets":[{"name":" A ","upstreams":[]},{"name":"A","upstreams":[" A "]}]}`,
	}
	for name, data := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := UnmarshalGraphFile([]byte(data)); err != nil {
				t.Fatalf("valid graph rejected: %v", err)
			}
		})
	}
}

// TestGraphFileAndSnapshotAgreeOnDuplicateFields: the standalone graph file
// reader and the snapshot reader must reach the same verdict when the same
// graph document is in play — a graph file that is rejected for a repeated
// known field is also rejected when embedded as the graph of a snapshot, and
// a tolerated document is accepted by both.
func TestGraphFileAndSnapshotAgreeOnDuplicateFields(t *testing.T) {
	documents := map[string]string{
		"datasets twice":        `{"datasets":[],"datasets":[{"name":"A","upstreams":[]}]}`,
		"name twice":            `{"datasets":[{"name":"A","name":"A","upstreams":[]}]}`,
		"upstreams twice":       `{"datasets":[{"name":"A","upstreams":[],"upstreams":[]}]}`,
		"escaped case variant":  `{"datasets":[{"NaMe":"A","name":"A","upstreams":[]}]}`,
		"valid graph":           `{"datasets":[{"name":"A","upstreams":[]},{"name":"B","upstreams":["A","A"]}]}`,
		"valid with extensions": `{"note":1,"note":2,"datasets":[{"name":"A","upstreams":[],"tag":{"name":"n","name":"m"}}]}`,
	}
	for name, data := range documents {
		t.Run(name, func(t *testing.T) {
			_, fileErr := UnmarshalGraphFile([]byte(data))

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
			_, snapErr := ParseSnapshot(snapDoc)

			if (fileErr == nil) != (snapErr == nil) {
				t.Fatalf("verdict mismatch: file err = %v, snapshot err = %v", fileErr, snapErr)
			}
		})
	}
}
