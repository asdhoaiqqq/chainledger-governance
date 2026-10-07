package chainledger

import (
	"encoding/json"
	"io"
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"
)

// ImportLineage decodes a lineage document in the same JSON shape the exports
// produce and returns a brand-new, independent lineage graph holding exactly
// the document's nodes and direct dependencies. The returned map works with
// every existing operation: Register, Rename, Unregister, Impacts,
// ImpactsWithCutoffs, Upstreams, CommonUpstreams, Roots and both exports. The
// caller's own graph is never read or modified — import only creates a new map.
//
// The document is one single JSON object with two array fields:
//
//   - "nodes": dataset names. Every listed node exists in the new graph,
//     including independent datasets with no dependency in either direction.
//     A name listed more than once counts once.
//   - "edges": direct dependencies written {"from": upstream, "to": derived},
//     the same direction the exports use. Each edge becomes the derived
//     dataset's direct upstream (Parents) and, mirrored, the upstream's direct
//     downstream (Children). The same dependency listed more than once counts
//     once.
//
// Import scope is exactly the document: nothing is invented, inferred or
// completed. A document produced by a source-scoped export rebuilds only that
// part of the lineage — other sources and downstreams the export omitted are
// not restored. Direct relations and longer branches through intermediate
// nodes are kept together, so explanation paths are still chosen by the
// existing query rules (fewest edges, then the lexicographically smallest full
// path in Go string order).
//
// Nodes and edges may appear in the document in any order; the result is
// independent of that order. Every node's direct upstream and downstream
// lists are stored in Go string order, so a graph imported from a valid export
// document re-exports byte-identical text for the same target or source-target
// pair.
//
// Names are identified by their exact decoded JSON value: matching is
// case-sensitive and spaces, Chinese characters, quotes, backslashes and
// newlines are preserved verbatim — never trimmed or rewritten. A correctly
// paired surrogate escape (the character U+1F600 written as \ud83d\ude00)
// decodes to the same name as the character written directly, so a node and
// an edge endpoint spelled differently in the document still connect. A real
// U+FFFD character and the \ufffd escape are ordinary valid name content, and
// an escaped backslash in front of escape-looking text ("\\ud800") is just a
// backslash followed by letters, never a surrogate escape. The input must be valid
// UTF-8 and hold exactly one complete JSON object; "nodes" and "edges" must
// both be present, each as an array of the correct element type (null is not
// an array). Two empty arrays import successfully as an empty,
// ready-to-register graph.
//
// The whole document is validated before the graph is returned, so every
// failure yields a nil graph and an error describing the document problem,
// never a partial graph:
//
//   - Empty or whitespace-only text, text that is not valid UTF-8, malformed
//     JSON, more than one JSON value, a non-object document, a duplicated
//     object key, a missing or null array, or an element of the wrong type is
//     rejected.
//   - An empty node name is rejected as a missing name.
//   - A node name or edge endpoint written with an unpaired Unicode surrogate
//     escape — a high surrogate not immediately followed by its low-surrogate
//     half, or a low surrogate on its own — fails the whole import. The error
//     states the problem, quotes the offending escape exactly as written, and
//     says whether it came from a node name or an edge's "from" or "to", so
//     distinct bad names never collapse into one identical message.
//   - Both endpoints of every edge must be listed in nodes; an edge naming a
//     dataset the document does not list fails the whole import, and the error
//     names that dataset. Edges missing an endpoint are rejected the same way.
//   - A self-dependency or a dependency cycle of any length fails the whole
//     import; the error states that the lineage contains a cycle and names the
//     datasets on it.
func ImportLineage(text string) (map[string]*Lineage, error) {
	nodesRaw, edgesRaw, err := decodeLineageDocument(text)
	if err != nil {
		return nil, err
	}

	var nodeItems []json.RawMessage
	if err := json.Unmarshal(nodesRaw, &nodeItems); err != nil {
		return nil, errInvalid("lineage document nodes must be an array of JSON strings: " + err.Error())
	}
	var edgeItems []json.RawMessage
	if err := json.Unmarshal(edgesRaw, &edgeItems); err != nil {
		return nil, errInvalid(`lineage document edges must be an array of {"from": ..., "to": ...} objects: ` + err.Error())
	}

	// Build the de-duplicated node set. Each element must itself be a JSON
	// string (null is not a name). Names keep their exact decoded value: the
	// document was already checked to be valid UTF-8, so every decoded name is
	// the original value, with JSON unescaping applied (quotes, backslashes,
	// newlines and all). The raw literal is also checked for unpaired
	// surrogate escapes, which encoding/json would otherwise silently decode
	// to U+FFFD and merge distinct names into one.
	nodes := make(map[string]bool, len(nodeItems))
	for _, item := range nodeItems {
		trimmed := strings.TrimSpace(string(item))
		if !strings.HasPrefix(trimmed, `"`) {
			return nil, errInvalid("lineage document nodes must be an array of JSON strings")
		}
		var name string
		if err := json.Unmarshal(item, &name); err != nil {
			return nil, errInvalid("lineage document nodes must be an array of JSON strings: " + err.Error())
		}
		if escape := unpairedSurrogateEscape(trimmed); escape != "" {
			return nil, errInvalid("lineage document node name contains an unpaired Unicode surrogate escape: " + escape)
		}
		if name == "" {
			return nil, errInvalid("dataset name is required")
		}
		nodes[name] = true
	}

	// Resolve every edge before constructing entries. Each element must be a
	// {"from": ..., "to": ...} object; both endpoints must be listed nodes.
	// De-duplicated dependencies only, independent of document order.
	dependencies := make(map[lineageExportEdge]bool, len(edgeItems))
	for _, item := range edgeItems {
		trimmed := strings.TrimSpace(string(item))
		if !strings.HasPrefix(trimmed, "{") {
			return nil, errInvalid(`lineage document edges must be an array of {"from": ..., "to": ...} objects`)
		}
		var edge lineageExportEdge
		if err := json.Unmarshal(item, &edge); err != nil {
			return nil, errInvalid(`lineage document edges must be an array of {"from": ..., "to": ...} objects: ` + err.Error())
		}
		// The endpoint names get the same unpaired-surrogate check as node
		// names, applied to their raw JSON literals so the original escape
		// spelling survives in the error. A non-string endpoint is left to the
		// typed unmarshal above and the missing-endpoint checks below.
		var rawEdge struct {
			From json.RawMessage `json:"from"`
			To   json.RawMessage `json:"to"`
		}
		if err := json.Unmarshal(item, &rawEdge); err != nil {
			return nil, errInvalid(`lineage document edges must be an array of {"from": ..., "to": ...} objects: ` + err.Error())
		}
		if err := checkEndpointSurrogates(rawEdge.From, "from"); err != nil {
			return nil, err
		}
		if err := checkEndpointSurrogates(rawEdge.To, "to"); err != nil {
			return nil, err
		}
		if edge.From == "" {
			return nil, errInvalid(`lineage document edge is missing its upstream endpoint ("from")`)
		}
		if edge.To == "" {
			return nil, errInvalid(`lineage document edge is missing its derived endpoint ("to")`)
		}
		if !nodes[edge.From] {
			return nil, errInvalid("edge endpoint not listed in nodes: " + edge.From)
		}
		if !nodes[edge.To] {
			return nil, errInvalid("edge endpoint not listed in nodes: " + edge.To)
		}
		dependencies[edge] = true
	}

	// Materialize the new graph with direct lists in Go string order. All
	// entries exist before any edge is attached, and a rejected import returns
	// nil, so a caller can never observe a partially filled graph.
	graph := make(map[string]*Lineage, len(nodes))
	for name := range nodes {
		graph[name] = &Lineage{Dataset: name}
	}
	for edge := range dependencies {
		graph[edge.To].Parents = append(graph[edge.To].Parents, edge.From)
		graph[edge.From].Children = append(graph[edge.From].Children, edge.To)
	}
	for _, entry := range graph {
		sort.Strings(entry.Parents)
		sort.Strings(entry.Children)
	}

	// Cycles of any length (self dependencies included) are rejected with the
	// datasets on one such cycle named deterministically.
	if cycle := findLineageCycle(graph); cycle != nil {
		return nil, errInvalid("lineage contains a cycle: " + strings.Join(cycle, " -> "))
	}
	return graph, nil
}

// decodeLineageDocument runs the document-level checks and returns the raw
// JSON values of the nodes and edges arrays: the text must be non-empty, valid
// UTF-8, exactly one complete JSON object with no duplicated object key, and
// both fields must be present as arrays (null rejected, element types are
// checked by the caller's typed unmarshal).
func decodeLineageDocument(text string) (json.RawMessage, json.RawMessage, error) {
	if strings.TrimSpace(text) == "" {
		return nil, nil, errInvalid("lineage document is empty: expected one JSON object with nodes and edges arrays")
	}
	// encoding/json silently rewrites invalid UTF-8 into U+FFFD instead of
	// failing, and two byte-different names could then collapse into one, so
	// the whole text is checked before anything is decoded.
	if !utf8.ValidString(text) {
		return nil, nil, errInvalid("lineage document is not valid UTF-8")
	}
	if err := rejectDuplicateKeys(text); err != nil {
		return nil, nil, err
	}

	var raw map[string]json.RawMessage
	if err := json.Unmarshal([]byte(text), &raw); err != nil {
		return nil, nil, errInvalid("lineage document must be one complete JSON object: " + err.Error())
	}

	nodes, ok := raw["nodes"]
	if !ok {
		return nil, nil, errInvalid(`lineage document is missing the "nodes" array`)
	}
	edges, ok := raw["edges"]
	if !ok {
		return nil, nil, errInvalid(`lineage document is missing the "edges" array`)
	}
	if err := requireJSONArray(nodes, "nodes"); err != nil {
		return nil, nil, err
	}
	if err := requireJSONArray(edges, "edges"); err != nil {
		return nil, nil, err
	}
	return nodes, edges, nil
}

// requireJSONArray rejects a field that is null or whose value is not a JSON
// array; element types inside the array are validated by typed unmarshalling.
func requireJSONArray(raw json.RawMessage, field string) error {
	trimmed := strings.TrimSpace(string(raw))
	switch {
	case trimmed == "null":
		return errInvalid("lineage document field " + field + " must be an array, got null")
	case !strings.HasPrefix(trimmed, "["):
		return errInvalid("lineage document field " + field + " must be an array")
	}
	return nil
}

// checkEndpointSurrogates applies the unpaired-surrogate rule to one raw edge
// endpoint value, naming the endpoint ("from" or "to") in the error. A value
// that is not a JSON string at all is skipped: the typed unmarshal and the
// missing-endpoint checks already cover it.
func checkEndpointSurrogates(raw json.RawMessage, endpoint string) error {
	trimmed := strings.TrimSpace(string(raw))
	if !strings.HasPrefix(trimmed, `"`) {
		return nil
	}
	if escape := unpairedSurrogateEscape(trimmed); escape != "" {
		return errInvalid(`lineage document edge "` + endpoint + `" name contains an unpaired Unicode surrogate escape: ` + escape)
	}
	return nil
}

// unpairedSurrogateEscape scans a valid JSON string literal (surrounding
// quotes included) for a \uXXXX escape in the UTF-16 surrogate range that does
// not form a valid pair, and returns the first offending escape exactly as
// written — or "" when every surrogate escape is properly paired. A high
// surrogate (D800–DBFF) is valid only when the very next thing in the string
// is a low-surrogate escape (DC00–DFFF); a lone high surrogate, a lone low
// surrogate, a reversed pair and a pair separated by other characters are all
// unpaired. encoding/json decodes a lone surrogate to U+FFFD instead of
// failing, which would silently merge byte-different names into one, so the
// raw literal is checked before the decoded value is trusted. An escaped
// backslash (\\) is consumed as a unit, so text following it — as in the
// literal "\\ud800" — is never mistaken for an escape introducer.
func unpairedSurrogateEscape(literal string) string {
	s := literal[1 : len(literal)-1] // the caller guarantees a quoted string
	for i := 0; i < len(s); {
		if s[i] != '\\' {
			i++
			continue
		}
		if s[i+1] != 'u' {
			i += 2 // \\, \", \n, ... — the next byte belongs to this escape
			continue
		}
		// The literal already decoded successfully, so four hex digits follow.
		cp, _ := strconv.ParseUint(s[i+2:i+6], 16, 32)
		switch {
		case cp >= 0xD800 && cp <= 0xDBFF:
			if i+12 <= len(s) && s[i+6] == '\\' && s[i+7] == 'u' {
				lo, _ := strconv.ParseUint(s[i+8:i+12], 16, 32)
				if lo >= 0xDC00 && lo <= 0xDFFF {
					i += 12 // a complete pair encoding one character
					continue
				}
			}
			return s[i : i+6]
		case cp >= 0xDC00 && cp <= 0xDFFF:
			return s[i : i+6] // a low surrogate with no high surrogate before it
		}
		i += 6
	}
	return ""
}

// objectFrame tracks one container during the structural key scan: whether it
// is an object (arrays need no key tracking), whether the next string token is
// that object's key, and the keys already seen in it.
type objectFrame struct {
	object    bool
	expectKey bool
	keys      map[string]bool
}

// rejectDuplicateKeys tokenizes the text and rejects any JSON object that
// repeats a key, any document whose top-level value is not an object, and any
// trailing JSON value after the closing object. encoding/json accepts all
// three silently (last key wins; null decodes into a map as no value at all;
// trailing values are rejected by Unmarshal but with a generic message), so
// the explicit scan makes document problems deterministic.
func rejectDuplicateKeys(text string) error {
	dec := json.NewDecoder(strings.NewReader(text))
	var stack []objectFrame
	rootClosed := false
	for {
		tok, err := dec.Token()
		if err == io.EOF {
			if len(stack) != 0 || !rootClosed {
				return errInvalid("lineage document must be one complete JSON object")
			}
			return nil
		}
		// Anything at all after the root object closes is a second value or
		// garbage, whether tokenized as a token or as a scan error.
		if rootClosed {
			return errInvalid("lineage document must hold exactly one JSON object")
		}
		if err != nil {
			return errInvalid("lineage document must be one complete JSON object: " + err.Error())
		}

		switch t := tok.(type) {
		case json.Delim:
			switch t {
			case '{':
				stack = append(stack, objectFrame{object: true, expectKey: true, keys: map[string]bool{}})
			case '[':
				if len(stack) == 0 {
					return errInvalid("lineage document must be a JSON object with nodes and edges arrays, got an array")
				}
				stack = append(stack, objectFrame{})
			case '}', ']':
				if len(stack) == 0 {
					return errInvalid("lineage document has an unmatched " + string(t))
				}
				stack = stack[:len(stack)-1]
				if len(stack) == 0 {
					if t != '}' {
						return errInvalid("lineage document must be a JSON object with nodes and edges arrays")
					}
					rootClosed = true
				} else if stack[len(stack)-1].object {
					// A nested container was this object member's value; the
					// next member, if any, starts with a key.
					stack[len(stack)-1].expectKey = true
				}
			}
		case string:
			if len(stack) == 0 {
				return errInvalid("lineage document must be a JSON object with nodes and edges arrays, got a string")
			}
			top := &stack[len(stack)-1]
			if !top.object {
				continue // string element of an array; members are delimiter-tracked
			}
			if !top.expectKey {
				top.expectKey = true // string value consumed; a key may follow
				continue
			}
			if top.keys[t] {
				return errInvalid("lineage document contains a duplicated object key: " + t)
			}
			top.keys[t] = true
			top.expectKey = false
		default: // number, bool, nil
			if len(stack) == 0 {
				return errInvalid("lineage document must be a JSON object with nodes and edges arrays")
			}
			if stack[len(stack)-1].object {
				stack[len(stack)-1].expectKey = true
			}
		}
	}
}

// findLineageCycle returns the datasets on one directed cycle written in the
// actual derivation direction (upstream -> derived), with the smallest-named
// dataset on the cycle first and repeated last, or nil when the graph is
// acyclic. Every node on a cycle reaches itself by following parent edges, so
// a depth-first walk with a per-path stack catches a back edge of any length,
// including a self dependency. Nodes are visited in Go string order and parent
// lists are already sorted, so the reported cycle is independent of the
// document's node and edge ordering.
func findLineageCycle(graph map[string]*Lineage) []string {
	color := make(map[string]int, len(graph)) // 0 unseen, 1 on current path, 2 done
	var stack []string
	var found []string

	var visit func(node string) bool
	visit = func(node string) bool {
		color[node] = 1
		stack = append(stack, node)
		for _, parent := range graph[node].Parents {
			switch color[parent] {
			case 0:
				if visit(parent) {
					return true
				}
			case 1:
				// A back edge to a node on the current path closes a cycle:
				// parent ... node, with node deriving from parent.
				start := 0
				for stack[start] != parent {
					start++
				}
				found = append([]string(nil), stack[start:]...)
				return true
			}
		}
		stack = stack[:len(stack)-1]
		color[node] = 2
		return false
	}

	names := make([]string, 0, len(graph))
	for name := range graph {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		if color[name] == 0 && visit(name) {
			return derivationCycle(found)
		}
	}
	return nil
}

// derivationCycle converts a closed cycle segment discovered while following
// parent edges into the derivation direction, then rotates it so the
// smallest-named dataset comes first (and stays last). A parent-direction
// segment [p0, p1, ..., pk] closes with pk deriving from p0, so the derivation
// runs p0 -> pk -> ... -> p1 -> p0.
func derivationCycle(parentPath []string) []string {
	body := make([]string, 0, len(parentPath)+1)
	body = append(body, parentPath[0])
	for i := len(parentPath) - 1; i >= 1; i-- {
		body = append(body, parentPath[i])
	}

	min := 0
	for i := 1; i < len(body); i++ {
		if body[i] < body[min] {
			min = i
		}
	}
	rotated := make([]string, 0, len(body)+1)
	rotated = append(rotated, body[min:]...)
	rotated = append(rotated, body[:min]...)
	rotated = append(rotated, body[min])
	return rotated
}
