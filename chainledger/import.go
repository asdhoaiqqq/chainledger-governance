package chainledger

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"sort"
	"unicode/utf8"
)

// ImportLineage reads one lineage JSON document in the format written by
// ExportUpstreamLineage and ExportSourceTargetLineage and returns a brand-new,
// independent lineage graph holding exactly the document's nodes and direct
// dependencies. The caller's graph is never read or modified: the result can be
// registered into, renamed, unregistered and queried straight away with
// Register, Rename, Unregister, Impacts, Upstreams, CommonUpstreams and the
// exporters.
//
// The document's top level is one JSON object with two arrays:
//
//   - "nodes": dataset name strings. Every listed node exists in the imported
//     graph, including one with no dependency at all in either direction.
//   - "edges": {"from": upstream, "to": derived} objects in the actual
//     derivation direction. Each edge appears both as the derived dataset's
//     direct upstream and as the source dataset's direct downstream.
//
// Import scope is strictly the document: a source-scoped export that kept only
// part of a lineage imports as that part alone, with no other sources or
// downstreams reconstructed. Both a direct relation and the longer branches
// through intermediate datasets are kept together when both are listed; the
// query features still choose explanation paths by shortest distance and the
// existing name-comparison rules over the imported graph.
//
// Document order never matters: duplicate node names and duplicate direct
// dependencies collapse to one, and every node's direct upstream and downstream
// lists are stored in Go string order, so importing the same document in any
// arrangement yields the same graph. Names are identified by their exact
// JSON-decoded value, case-sensitive, preserving spaces, Chinese characters,
// quotes, backslashes and newlines without trimming or rewriting.
//
// The text must be valid UTF-8 and must contain exactly one complete JSON
// object whose "nodes" and "edges" are both arrays of the right item types.
// Two empty arrays import successfully as an empty graph that is ready for
// registrations; this is not a failure. The import fails — with a nil graph and
// never a partial graph — on empty or whitespace-only text, malformed JSON, a
// non-object document, a missing or wrongly typed "nodes"/"edges" array (a
// wrong item type, an extra JSON value or trailing data counts too), an empty
// node name, or an edge endpoint not listed in "nodes" (the error names it).
// A self dependency or a dependency cycle of any length also fails the whole
// import; the error states that the lineage is cyclic and names the datasets on
// the cycle.
func ImportLineage(text string) (map[string]*Lineage, error) {
	// The text is a document handed in by another program, so validity is
	// checked before anything is built: a rejected import must never return a
	// partially populated graph.
	if !utf8.ValidString(text) {
		return nil, errInvalid("lineage document is not valid UTF-8")
	}
	trimmed := bytes.TrimSpace([]byte(text))
	if len(trimmed) == 0 {
		return nil, errInvalid("lineage document is empty: a JSON object with nodes and edges arrays is required")
	}
	// The document must be one JSON object: reject a bare null, array, number
	// or string before decoding so the error describes the document's shape.
	if trimmed[0] != '{' {
		return nil, errInvalid("lineage document is not a valid JSON object")
	}

	// Decode into RawMessage slices (rather than the export structs) so type
	// mismatches surface as document errors: a null, a number, a string, or an
	// array holding a non-string would otherwise be silently coerced or
	// dropped. Unknown top-level keys are ignored: the required contract is the
	// two arrays, not the absence of anything else.
	var raw struct {
		Nodes *[]json.RawMessage `json:"nodes"`
		Edges *[]json.RawMessage `json:"edges"`
	}
	decoder := json.NewDecoder(bytes.NewReader([]byte(text)))
	if err := decoder.Decode(&raw); err != nil {
		return nil, errInvalid("lineage document is not a valid JSON object: " + err.Error())
	}
	// The document must be one complete object: a second JSON value after it is
	// a malformed document rather than silently ignored input.
	if token, err := decoder.Token(); err != io.EOF {
		if err != nil {
			return nil, errInvalid("lineage document is not a valid JSON object: " + err.Error())
		}
		return nil, errInvalid(fmt.Sprintf(
			"lineage document has unexpected trailing data after the JSON object: %v", token))
	}
	if raw.Nodes == nil {
		return nil, errInvalid(`lineage document is missing a "nodes" array`)
	}
	if raw.Edges == nil {
		return nil, errInvalid(`lineage document is missing an "edges" array`)
	}

	// Decode the node names one by one, rejecting anything that is not a JSON
	// string. Duplicate names collapse to a single node; empty names fail.
	nodes := make([]string, 0, len(*raw.Nodes))
	known := make(map[string]bool, len(*raw.Nodes))
	for i, item := range *raw.Nodes {
		if isJSONNull(item) {
			return nil, errInvalid(fmt.Sprintf(
				"lineage document nodes[%d] must be a JSON string, got null", i))
		}
		var name string
		if err := json.Unmarshal(item, &name); err != nil {
			return nil, errInvalid(fmt.Sprintf(
				"lineage document nodes[%d] must be a JSON string: %s", i, err.Error()))
		}
		if name == "" {
			return nil, errInvalid(fmt.Sprintf(
				"lineage document nodes[%d] is an empty dataset name", i))
		}
		if !known[name] {
			known[name] = true
			nodes = append(nodes, name)
		}
	}

	// Decode every edge's two string endpoints in document order. A duplicate
	// dependency collapses to one regardless of presentation order. Both
	// endpoints must be listed nodes; the check walks the edges in document
	// order so the first bad edge is the one reported, before any cycle check.
	edges := make([]lineageExportEdge, 0, len(*raw.Edges))
	seenEdge := make(map[lineageExportEdge]bool, len(*raw.Edges))
	for i, item := range *raw.Edges {
		if isJSONNull(item) {
			return nil, errInvalid(fmt.Sprintf(
				"lineage document edges[%d] must be an object with string from/to, got null", i))
		}
		// Decode the endpoints individually so a missing key and a non-string
		// endpoint each get their own precise document error instead of both
		// reading as an empty name. Unknown keys on an edge are ignored.
		var edgeFields map[string]json.RawMessage
		if err := json.Unmarshal(item, &edgeFields); err != nil {
			return nil, errInvalid(fmt.Sprintf(
				"lineage document edges[%d] must be an object with string from/to: %s", i, err.Error()))
		}
		from, err := decodeLineageName(edgeFields["from"])
		if err != nil {
			return nil, errInvalid(fmt.Sprintf(
				"lineage document edges[%d].from must be a JSON string: %s", i, err.Error()))
		}
		to, err := decodeLineageName(edgeFields["to"])
		if err != nil {
			return nil, errInvalid(fmt.Sprintf(
				"lineage document edges[%d].to must be a JSON string: %s", i, err.Error()))
		}
		edge := lineageExportEdge{From: from, To: to}
		if edge.From == "" {
			return nil, errInvalid(fmt.Sprintf(
				"lineage document edges[%d] has an empty from dataset name", i))
		}
		if edge.To == "" {
			return nil, errInvalid(fmt.Sprintf(
				"lineage document edges[%d] has an empty to dataset name", i))
		}
		if !known[edge.From] {
			return nil, errInvalid(fmt.Sprintf(
				"lineage document edges[%d] endpoint is not listed in nodes: %s", i, edge.From))
		}
		if !known[edge.To] {
			return nil, errInvalid(fmt.Sprintf(
				"lineage document edges[%d] endpoint is not listed in nodes: %s", i, edge.To))
		}
		if edge.From == edge.To {
			return nil, errInvalid("lineage document is cyclic: dataset " + edge.From + " depends on itself")
		}
		if !seenEdge[edge] {
			seenEdge[edge] = true
			edges = append(edges, edge)
		}
	}

	// Build the adjacency lists first; a cycle is then found with one DFS over
	// the exact graph the document describes. Nothing is returned until the
	// walk proves the graph is acyclic, so a cyclic document never leaks a
	// usable partial graph.
	parents := make(map[string][]string, len(nodes))
	children := make(map[string][]string, len(nodes))
	for _, name := range nodes {
		parents[name] = []string{}
		children[name] = []string{}
	}
	for _, edge := range edges {
		parents[edge.To] = append(parents[edge.To], edge.From)
		children[edge.From] = append(children[edge.From], edge.To)
	}
	for _, name := range nodes {
		sort.Strings(parents[name])
		sort.Strings(children[name])
	}

	if cycle := findLineageCycle(nodes, parents); cycle != nil {
		return nil, errInvalid("lineage document is cyclic: " +
			"these datasets form a dependency cycle " + fmt.Sprintf("%v", cycle))
	}

	graph := make(map[string]*Lineage, len(nodes))
	for _, name := range nodes {
		graph[name] = &Lineage{
			Dataset:  name,
			Parents:  parents[name],
			Children: children[name],
		}
	}
	return graph, nil
}

// findLineageCycle returns the datasets on one dependency cycle written in
// derivation order (from -> to -> ... -> from), or nil when the graph is
// acyclic. Neighbor lists are scanned in Go string order and nodes are entered
// in sorted order, so the reported cycle is independent of the document's node
// and edge arrangement.
func findLineageCycle(nodes []string, parents map[string][]string) []string {
	sorted := append([]string(nil), nodes...)
	sort.Strings(sorted)

	state := map[string]int{} // 0 unseen, 1 on the current stack, 2 done
	var stack []string
	var cycle []string

	var visit func(string) bool
	visit = func(node string) bool {
		state[node] = 1
		stack = append(stack, node)
		for _, parent := range parents[node] {
			switch state[parent] {
			case 0:
				if visit(parent) {
					return true
				}
			case 1:
				// parent is already on the stack. stack[i:] walks the cycle
				// upstream (derived -> source); reversing it lays it out in
				// derivation order, and repeating the first node closes it:
				// from -> to -> ... -> from.
				for i, n := range stack {
					if n == parent {
						loop := append([]string(nil), stack[i:]...)
						for j, k := 0, len(loop)-1; j < k; j, k = j+1, k-1 {
							loop[j], loop[k] = loop[k], loop[j]
						}
						cycle = append(loop, loop[0])
						return true
					}
				}
			}
		}
		stack = stack[:len(stack)-1]
		state[node] = 2
		return false
	}

	for _, node := range sorted {
		if state[node] == 0 && visit(node) {
			return cycle
		}
	}
	return nil
}

// isJSONNull reports whether a raw JSON value is the literal null, which would
// otherwise decode quietly into a zero value instead of failing as a type error.
func isJSONNull(item json.RawMessage) bool {
	return bytes.Equal(bytes.TrimSpace(item), []byte("null"))
}

// decodeLineageName decodes one endpoint string; an absent key (nil raw
// message) is a document error of its own rather than an empty name, and a
// JSON null must not masquerade as an empty string.
func decodeLineageName(raw json.RawMessage) (string, error) {
	if len(raw) == 0 {
		return "", fmt.Errorf("field is missing")
	}
	if isJSONNull(raw) {
		return "", fmt.Errorf("must be a JSON string, got null")
	}
	var name string
	if err := json.Unmarshal(raw, &name); err != nil {
		return "", err
	}
	return name, nil
}
