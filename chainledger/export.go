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

// lineageExportDocument is the JSON shape produced by the lineage exporters:
// the set of dataset names involved in the exported derivation, plus every
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
// of registration and stored-list order. Only the selected nodes are
// inspected: an unselected dataset may hold an invalid name without blocking
// this export. A real replacement character (U+FFFD) that is itself part of a
// name is valid UTF-8 and exports normally.
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
	included := ancestorSet(graph, target)
	return renderLineageExport(graph, included)
}

// ExportScopedUpstreamLineage exports only the part of target's derivation
// that a specified source takes part in, as a JSON document with the same
// shape and ordering rules as ExportUpstreamLineage. The document holds every
// node and every direct dependency lying on at least one existing derivation
// route from source to target, source and target themselves included; each
// node and edge appears exactly once.
//
// Every branch is preserved: a longer route from source to target is not
// shortened away by a direct source -> target dependency or by a shorter
// parallel route. For example, when source derives report directly, also
// reaches report through a, and reaches it through b and mid, scoping the
// export to source and report keeps all three routes complete. A source-side
// dependency that does not lead to target stays out: if a additionally
// depends on an independent source extra, neither extra nor the extra -> a
// edge appears. Other downstreams of source that cannot reach target are
// excluded just as source's own ancestors and target's own downstreams are —
// the document ends at its two named endpoints.
//
// Names match by exact registered value (case-sensitive). source is validated
// before target: an empty name is rejected as a missing name and an
// unregistered name (also against an empty or nil graph) is rejected with an
// error naming it; failures return an empty string. When both endpoints are
// registered but no derivation route runs from source to target — including a
// source that is actually downstream of target, since reverse dependencies
// are not reachability — the export still succeeds with both "nodes" and
// "edges" empty arrays; no isolated endpoint is kept. source and target being
// the same registered dataset yields that one node and an empty edges array.
//
// The UTF-8 rule is exactly ExportUpstreamLineage's, applied only to the
// nodes the scoped document actually contains: an invalid name on a selected
// node fails the whole export (empty string, error quoting the offending
// bytes), while an invalid name on an unselected node — an ancestor of
// source, a side input like extra, or a downstream that cannot reach
// target — never blocks it. Endpoint validation still precedes encoding
// validation. The export is read-only: it changes no node, edge, relationship
// or stored list order.
func ExportScopedUpstreamLineage(graph map[string]*Lineage, source, target string) (string, error) {
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

	// A node belongs to the scoped document exactly when some derivation route
	// from source reaches target through it: source must reach it by following
	// child edges and it must reach target by following parent edges. The
	// intersection of the two closures is therefore precisely the union of all
	// source -> ... -> target routes' node sets. Register rejects cycles, so
	// neither walk can run forever. An edge between two selected nodes sits on
	// a complete source-to-target route by construction, so keeping every such
	// direct dependency preserves all routes, longer ones included, with no
	// side inputs or unreachable branches leaking in.
	downstreamOfSource := descendantSet(graph, source)
	upstreamOfTarget := ancestorSet(graph, target)
	included := make(map[string]bool)
	for name := range downstreamOfSource {
		if upstreamOfTarget[name] {
			included[name] = true
		}
	}
	return renderLineageExport(graph, included)
}

// ancestorSet collects start and every dataset reachable from it by following
// parent edges — start itself and its whole upstream closure. The returned
// set doubles as the visited set for the walk.
func ancestorSet(graph map[string]*Lineage, start string) map[string]bool {
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

// descendantSet collects start and every dataset reachable from it by
// following child edges — start itself and its whole downstream closure. The
// returned set doubles as the visited set for the walk.
func descendantSet(graph map[string]*Lineage, start string) map[string]bool {
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

// renderLineageExport builds the deterministic JSON document for one selected
// node set. Both the full and the source-scoped upstream exports share this
// rule set: Go-string node order, every direct dependency among the selected
// nodes once ordered by from then to, strict UTF-8 validation of selected
// names, and empty (non-null) arrays when the selection is empty. The graph is
// only read.
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
	// shared replacement glyph. Nodes outside the selection are never
	// examined.
	for _, name := range nodes {
		if !utf8.ValidString(name) {
			return "", errInvalid(fmt.Sprintf(
				"dataset name is not valid UTF-8 and cannot be exported losslessly: %q", name))
		}
	}

	// Every direct dependency between included nodes is an edge. The edge set
	// is chosen by its two endpoints, so a parent outside the selection
	// contributes nothing even though the full export of this child would
	// include it. The dedupe set guards the one-edge-once rule even if a stored
	// list ever repeated a name.
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
