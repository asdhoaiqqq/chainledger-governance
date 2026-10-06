package chainledger

import (
	"encoding/json"
	"sort"
	"strconv"
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
// The export is read-only: it changes no node, edge or stored list order. An
// empty target is rejected as a missing name; a target absent from the graph
// (including an empty or nil graph) is rejected with an error naming it.
// Failures return an empty string and never a partial document.
//
// A name is written verbatim only when a JSON reader can recover its exact
// registered bytes: every included name must be valid UTF-8. Registration only
// rejects empty names, so a graph may still hold names with invalid UTF-8
// bytes, which encoding/json would silently replace with U+FFFD; two distinct
// names could then collapse into one after reading. Instead the whole export
// fails with an empty string and an error naming the first offending dataset
// in Go string order among the included nodes, so the reported dataset is
// independent of registration and stored-list order. The name is quoted with
// Go escaping, which renders every raw byte (including unprintable ones, e.g.
// "\xff") rather than substituting a replacement character, so different bad
// bytes produce different error text. Only nodes in this target's ancestor
// closure are checked: unrelated datasets and the target's downstreams never
// block the export. A name containing the actual U+FFFD character is valid
// UTF-8 and exports normally.
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

	// Every included name must survive a JSON round trip byte-for-byte.
	// encoding/json replaces invalid UTF-8 with U+FFFD instead of erroring,
	// which would report success for a document that loses (or merges) names;
	// reject that case explicitly before any document is produced. The nodes
	// are already in Go string order, so the first invalid one is the same
	// dataset whatever the registration or stored-list orders were; only this
	// export's closure was collected, leaving unrelated nodes unchecked.
	for _, name := range nodes {
		if !utf8.ValidString(name) {
			return "", errInvalid("dataset name is not valid UTF-8: " + strconv.Quote(name))
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
