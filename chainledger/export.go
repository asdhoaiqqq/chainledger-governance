package chainledger

import (
	"encoding/json"
	"fmt"
	"sort"
	"unicode/utf8"
)

// lineageExportEdge is one direct dependency in an exported lineage document,
// written in the actual derivation direction: From is the upstream dataset and
// To is the dataset derived from it.
type lineageExportEdge struct {
	From string `json:"from"`
	To   string `json:"to"`
}

// lineageExportDocument is the JSON shape produced by ExportUpstreamLineage:
// the full set of dataset names involved in a target's derivation, plus every
// direct dependency between them.
type lineageExportDocument struct {
	Nodes []string            `json:"nodes"`
	Edges []lineageExportEdge `json:"edges"`
}

// ExportUpstreamLineage exports the complete upstream lineage of target as a
// JSON document, for other programs to consume. Where Upstreams explains each
// source with a single shortest path, this export preserves every branch the
// target actually depends on: even when a source already reaches the target
// directly, the longer routes through intermediate datasets stay in the
// document.
//
// The document's top level holds two arrays:
//
//   - "nodes": the names of target itself and every direct and indirect
//     upstream of it, each exactly once, ordered by Go string order.
//   - "edges": every direct dependency between the included nodes, each
//     exactly once, written {"from": upstream, "to": derived} in the actual
//     derivation direction and ordered by "from" and then "to", again in Go
//     string order.
//
// The target's downstreams and datasets that take no part in its derivation
// never appear. A target with no upstreams exports successfully with itself
// as the only node and an empty (non-null) edges array. The ordering is fully
// determined by the names, so neither registration order nor the stored
// upstream list order can change the output; names are matched by exact
// registered value (case-sensitive) and written verbatim, with JSON escaping
// applied so a reader recovers the original bytes.
//
// Every name in the exported set must be valid UTF-8: JSON cannot carry an
// invalid byte sequence losslessly (a JSON encoder would replace it with the
// replacement character and two distinct names could collapse into one), so a
// success guarantees the reader recovers every name exactly as registered. If
// any included node's name is not valid UTF-8, the whole export fails; the
// error identifies the offending name in Go-quoted form (%q), which renders
// each non-displayable byte as its own \xNN escape, so different bad bytes
// never share one replacement glyph. When several names are invalid, the one
// sorting first in Go string order is reported, keeping the error independent
// of registration and stored-list order. Only the target and its ancestors are
// inspected: an unrelated dataset or a downstream of the target may hold an
// invalid name without blocking this export. A real replacement character
// (U+FFFD) that is itself part of a name is valid UTF-8 and exports normally.
//
// The export is read-only: it changes no node, edge or stored list order. An
// empty target is rejected as a missing name; a target absent from the graph
// (including an empty or nil graph) is rejected with an error naming it, and
// those checks take precedence over name encoding. Failures return an empty
// string and never a partial document.
func ExportUpstreamLineage(graph map[string]*Lineage, target string) (string, error) {
	if target == "" {
		return "", errInvalid("dataset name is required")
	}
	if _, ok := graph[target]; !ok {
		return "", errInvalid("dataset not found: " + target)
	}

	// Collect the target plus its whole ancestor closure by walking parent
	// edges. Register rejects cycles, so every walk terminates; the included
	// set doubles as the visited set.
	included := map[string]bool{target: true}
	queue := []string{target}
	for len(queue) > 0 {
		node := queue[0]
		queue = queue[1:]
		for _, parent := range graph[node].Parents {
			if !included[parent] {
				included[parent] = true
				queue = append(queue, parent)
			}
		}
	}

	nodes := make([]string, 0, len(included))
	for name := range included {
		nodes = append(nodes, name)
	}
	sort.Strings(nodes)

	// JSON text must round-trip every name byte for byte. encoding/json would
	// silently rewrite an invalid UTF-8 byte sequence into U+FFFD rather than
	// fail, and two byte-different names could then decode to the same value,
	// so check the included names ourselves before any document is produced.
	// Nodes are already in Go string order, so the first invalid one is the
	// deterministic answer regardless of registration or upstream-list order;
	// %q exposes the actual bytes (non-displayable ones as \xNN) instead of a
	// shared replacement glyph. Nodes outside this closure are never examined.
	for _, name := range nodes {
		if !utf8.ValidString(name) {
			return "", errInvalid(fmt.Sprintf(
				"dataset name is not valid UTF-8 and cannot be exported losslessly: %q", name))
		}
	}

	// Every direct dependency between included nodes is an edge. A parent of
	// an included node is itself included by construction, so walking the
	// parents of each included node covers exactly the dependencies among the
	// selected nodes. The dedupe set guards the one-edge-once rule even if a
	// stored list ever repeated a name.
	edges := make([]lineageExportEdge, 0)
	seen := make(map[lineageExportEdge]bool)
	for _, name := range nodes {
		for _, parent := range graph[name].Parents {
			edge := lineageExportEdge{From: parent, To: name}
			if !seen[edge] {
				seen[edge] = true
				edges = append(edges, edge)
			}
		}
	}
	sort.Slice(edges, func(i, j int) bool {
		if edges[i].From != edges[j].From {
			return edges[i].From < edges[j].From
		}
		return edges[i].To < edges[j].To
	})

	data, err := json.Marshal(lineageExportDocument{Nodes: nodes, Edges: edges})
	if err != nil {
		return "", err
	}
	return string(data), nil
}
