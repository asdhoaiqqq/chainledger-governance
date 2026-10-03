package chainledger

import (
	"encoding/json"
	"errors"
	"reflect"
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
			fileAdj, err := graphAdjacency(fileGraph)
			if err != nil {
				t.Fatalf("graphAdjacency: %v", err)
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
