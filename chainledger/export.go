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

// lineageExportDocument is the JSON shape produced by the lineage exports:
// the set of dataset names involved in the requested derivation, plus every
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

	return renderLineageExport(graph, ancestorClosure(graph, target))
}

// ExportSourceTargetLineage exports only the part of target's upstream lineage
// that a specified registered source actually participates in, as a JSON
// document for other programs to consume. Where ExportUpstreamLineage brings
// along every source target depends on, this scoped export answers just "how
// does this one source derive this target".
//
// The document holds exactly the datasets that lie on at least one existing
// derivation route from source to target — source and target included — and
// exactly the direct dependencies carried by those routes. Concretely a node
// is included when it is both a direct or indirect downstream of source and a
// direct or indirect upstream of target; an edge is included when both of its
// endpoints are. Every node and edge appears once. Keeping the full node set
// (rather than one picked path) preserves every branch simultaneously: if
// source reaches target directly, through a, and through b and mid, limiting
// the export to source and report still keeps all three routes complete — a
// longer route is never dropped because a direct relation or a shorter route
// exists.
//
// What stays out follows from the same intersection:
//
//   - An independent source that feeds an included intermediate dataset but is
//     not itself downstream of source is excluded, and so is the edge from it:
//     if a additionally depends on extra, neither extra nor extra -> a appears.
//   - A downstream of source that cannot reach target is excluded; ancestors
//     of source and downstreams of target are excluded as well.
//
// When source and target are the same registered dataset, that dataset alone
// is exported with an empty edges array. When both are registered but no
// derivation route from source to target exists, the export still succeeds but
// holds two empty (non-null) arrays — no isolated endpoint is retained, and a
// dependency running the other way (target derives source) is not treated as
// reachability.
//
// The document shape, the {"from": upstream, "to": derived} edge direction,
// the nodes-by-name and edges-by-from-then-to Go-string ordering, the
// byte-exact name handling and the invalid-UTF-8 failure rule all match
// ExportUpstreamLineage, judged over the scoped node set only: a name outside
// the selected routes, including one on target's other ancestry, never blocks
// this export. Output is therefore fully determined by the registered names
// and independent of registration order and stored upstream-list order.
//
// Names match by exact registered value (case-sensitive). Source is validated
// first: an empty name is a missing-name error and an unregistered name
// (including against an empty or nil graph) is an error naming it; only then
// is target checked the same way. Those checks take precedence over name
// encoding. The export is read-only — it changes no node, bidirectional
// relationship or stored list order — and any failure returns an empty
// string, never a partial document.
func ExportSourceTargetLineage(graph map[string]*Lineage, source, target string) (string, error) {
	if source == "" {
		return "", errInvalid("dataset name is required")
	}
	if _, ok := graph[source]; !ok {
		return "", errInvalid("dataset not found: " + source)
	}
	if target == "" {
		return "", errInvalid("dataset name is required")
	}
	if _, ok := graph[target]; !ok {
		return "", errInvalid("dataset not found: " + target)
	}

	// A node belongs to a source -> target derivation exactly when it is
	// downstream of source AND upstream of target. Register rejects cycles, so
	// both walks terminate; the visited sets guard the loops regardless.
	downstream := descendantClosure(graph, source)
	upstream := ancestorClosure(graph, target)
	included := make(map[string]bool)
	for name := range downstream {
		if upstream[name] {
			included[name] = true
		}
	}

	return renderLineageExport(graph, included)
}

// ancestorClosure collects start and every dataset reachable from it by
// following parent edges.
func ancestorClosure(graph map[string]*Lineage, start string) map[string]bool {
	included := map[string]bool{start: true}
	queue := []string{start}
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
	return included
}

// descendantClosure collects start and every dataset reachable from it by
// following child edges.
func descendantClosure(graph map[string]*Lineage, start string) map[string]bool {
	included := map[string]bool{start: true}
	queue := []string{start}
	for len(queue) > 0 {
		node := queue[0]
		queue = queue[1:]
		for _, child := range graph[node].Children {
			if !included[child] {
				included[child] = true
				queue = append(queue, child)
			}
		}
	}
	return included
}

// renderLineageExport serializes one already-decided node set in the single
// document format shared by the lineage exports.
func renderLineageExport(graph map[string]*Lineage, included map[string]bool) (string, error) {
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
	// shared replacement glyph. Nodes outside the selected set are never
	// examined.
	for _, name := range nodes {
		if !utf8.ValidString(name) {
			return "", errInvalid(fmt.Sprintf(
				"dataset name is not valid UTF-8 and cannot be exported losslessly: %q", name))
		}
	}

	// Every direct dependency between included nodes is an edge. In the full
	// upstream export every parent of an included node is included by
	// construction; in a source-scoped export an included intermediate node may
	// also depend on an independent, excluded source, so the parent has to be
	// checked against the set. The dedupe set guards the one-edge-once rule even
	// if a stored list ever repeated a name.
	edges := make([]lineageExportEdge, 0)
	seen := make(map[lineageExportEdge]bool)
	for _, name := range nodes {
		for _, parent := range graph[name].Parents {
			if !included[parent] {
				continue
			}
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
