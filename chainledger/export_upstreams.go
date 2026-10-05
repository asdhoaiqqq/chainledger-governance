package chainledger

import (
	"encoding/json"
	"sort"
)

// lineageEdgeJSON is one direct dependency in an export, written along the
// actual derivation direction: From is the upstream, To the dataset directly
// derived from it.
type lineageEdgeJSON struct {
	From string `json:"from"`
	To   string `json:"to"`
}

// upstreamExportJSON is the stable top-level shape of an upstream export:
// names of the target and every direct or indirect upstream, plus every direct
// dependency whose two ends both belong to that set.
type upstreamExportJSON struct {
	Nodes []string          `json:"nodes"`
	Edges []lineageEdgeJSON `json:"edges"`
}

// ExportUpstreams exports target's complete upstream lineage as JSON text for
// other programs to read the relationships:
//
//	{"nodes":["..."],"edges":[{"from":"upstream","to":"derived"}]}
//
// Nodes are the target itself plus every dataset directly or indirectly
// upstream of it — the full ancestor closure reached by following parent
// edges. Edges are ALL existing direct dependencies between those nodes, each
// written along the actual derivation direction (from an upstream to the
// dataset derived from it): unlike Upstreams, which keeps one shortest
// explanation path per source, the export preserves every branch the target's
// derivation actually relies on, including longer branches that parallel a
// direct edge. The target's downstreams and unrelated independent datasets
// never appear, even as edge endpoints.
//
// Ordering is deterministic and independent of registration order or of the
// stored direct-upstream list order: nodes are sorted by name in Go string
// order, and edges are sorted by From and then To in the same order. Names
// match by exact registered value (case-sensitive), are never trimmed or
// rewritten, and are JSON-encoded by encoding/json, so quotes, backslashes
// and newlines survive a JSON round trip unchanged.
//
// A target with no upstreams exports successfully with nodes holding just its
// name and edges a JSON empty array. An empty target is rejected as a missing
// name; a target absent from the graph (including a non-empty target looked up
// against an empty or nil graph) is rejected with an error naming it. A failed
// export returns an empty string, never partial output. The export is
// read-only: it changes no node, bidirectional edge or stored list order.
func ExportUpstreams(graph map[string]*Lineage, target string) (string, error) {
	if target == "" {
		return "", errInvalid("dataset name is required")
	}
	if _, ok := graph[target]; !ok {
		return "", errInvalid("dataset not found: " + target)
	}

	// Resolve the target's ancestor closure: the target itself plus every
	// dataset reachable by following parent edges. Register never accepts
	// cycles, so the visited set alone makes this parent-edge walk terminate;
	// shared ancestors reached through merging branches are visited once.
	included := map[string]bool{}
	var collect func(node string)
	collect = func(node string) {
		if included[node] {
			return
		}
		included[node] = true
		for _, parent := range graph[node].Parents {
			collect(parent)
		}
	}
	collect(target)

	// Keep every existing direct dependency whose two ends are in the closure,
	// iterating child -> parent on Parents and presenting it from upstream to
	// derived. Each edge appears exactly once: Register keeps Parents free of
	// duplicates and mirrors every parent edge with one matching child edge, so
	// walking Children as well would report the same dependency twice.
	edges := make([]lineageEdgeJSON, 0)
	for node := range included {
		for _, parent := range graph[node].Parents {
			if included[parent] {
				edges = append(edges, lineageEdgeJSON{From: parent, To: node})
			}
		}
	}
	sort.Slice(edges, func(i, j int) bool {
		if edges[i].From != edges[j].From {
			return edges[i].From < edges[j].From
		}
		return edges[i].To < edges[j].To
	})

	nodes := make([]string, 0, len(included))
	for name := range included {
		nodes = append(nodes, name)
	}
	sort.Strings(nodes)

	document := upstreamExportJSON{
		Nodes: nodes,
		Edges: edges,
	}
	text, err := json.Marshal(document)
	if err != nil {
		// Unreachable for map[string]string content, but keep the no-partial
		// contract explicit rather than returning a half-built document.
		return "", err
	}
	return string(text), nil
}
