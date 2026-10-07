package chainledger

import (
	"encoding/json"
	"io"
	"sort"
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
// newlines are preserved verbatim — never trimmed or rewritten. The input
// must be valid UTF-8 and hold exactly one complete JSON object; "nodes" and
// "edges" must both be present, each as an array of the correct element type
// (null is not an array). Two empty arrays import successfully as an empty,
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
//   - A name (a node name, or an edge's "from" or "to") containing an unpaired
//     Unicode surrogate escape is rejected. encoding/json rewrites a lone
//     \uD800–\uDFFF escape to the replacement character U+FFFD, so two
//     different bad names would silently collapse into one "�" and a bad edge
//     endpoint could match a real dataset named "�". A high-surrogate escape
//     is valid only immediately followed by the low-surrogate escape that
//     completes one character; a lone high, a lone low, a reversed pair, or a
//     pair broken by other characters or escapes is unpaired. The error says
//     so, names where the escape was found (node name, edge "from" or edge
//     "to") and quotes the offending escape in its original \uXXXX spelling,
//     so distinct bad names never share one replacement glyph. A real U+FFFD
//     character written directly or as a \uFFFD escape is valid, as is a
//     correctly paired \uD83D\uDE00: both spellings decode to the same one
//     character as a literal emoji, so a node written one way and an edge the
//     other still connect. The document text "\\uD800" (an escaped backslash
//     followed by letters) is an ordinary name containing no surrogate escape
//     and is kept verbatim.
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
	// newlines and all). Unpaired \u surrogate escapes are rejected on the raw
	// text first: encoding/json accepts them and rewrites each one to U+FFFD,
	// so two different bad names would silently collapse into one.
	nodes := make(map[string]bool, len(nodeItems))
	for _, item := range nodeItems {
		trimmed := strings.TrimSpace(string(item))
		if !strings.HasPrefix(trimmed, `"`) {
			return nil, errInvalid("lineage document nodes must be an array of JSON strings")
		}
		name, err := unmarshalLineageName(item)
		if err != nil {
			return nil, err
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
		// Decode the object structurally without accepting endpoint strings:
		// the from/to values are then validated as names themselves, so an
		// unpaired surrogate escape in an endpoint fails the import instead of
		// silently matching a real "�" dataset.
		var edgeRaw struct {
			From json.RawMessage `json:"from"`
			To   json.RawMessage `json:"to"`
		}
		if err := json.Unmarshal(item, &edgeRaw); err != nil {
			return nil, errInvalid(`lineage document edges must be an array of {"from": ..., "to": ...} objects: ` + err.Error())
		}
		from, err := unmarshalEndpointName(edgeRaw.From, "from")
		if err != nil {
			return nil, err
		}
		to, err := unmarshalEndpointName(edgeRaw.To, "to")
		if err != nil {
			return nil, err
		}
		edge := lineageExportEdge{From: from, To: to}
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

// decodeLineageName decodes one JSON string token (a node name or an edge
// endpoint) into its exact Go value, rejecting unpaired \u surrogate escapes
// before json.Unmarshal can touch the text.
//
// encoding/json accepts a lone \uD800–\uDFFF escape and rewrites it to the
// replacement character U+FFFD: two byte-different bad names would then
// collapse into one "�", and a bad edge endpoint would silently match a real
// dataset named "�". The raw token is therefore scanned first. A high
// surrogate escape is valid only when it is immediately followed by the low
// surrogate escape that together represent one character; a lone high, a lone
// low, a reversed pair, or a pair broken by other escapes or characters is
// unpaired. On success ok is true with the decoded value; when the token
// contains an unpaired escape, ok is still true, name is empty and badEscape
// holds the escape in its original \uXXXX spelling; ok is false only when the
// token is not a JSON string. The token was extracted from an already
// validated document, so json.Unmarshal cannot fail for a token that starts
// with a quote.
func decodeLineageName(raw json.RawMessage) (name, badEscape string, ok bool) {
	if !strings.HasPrefix(strings.TrimSpace(string(raw)), `"`) {
		return "", "", false
	}
	if bad := firstUnpairedSurrogate(string(raw)); bad != "" {
		return "", bad, true
	}
	var decoded string
	if err := json.Unmarshal(raw, &decoded); err != nil {
		return "", "", false
	}
	return decoded, "", true
}

// firstUnpairedSurrogate scans the raw text of one JSON string token and
// returns the first unpaired \u surrogate escape in its original spelling
// ("" when every surrogate escape is correctly paired). Escaped backslashes
// are skipped as a unit, so "\\ud800" is literal backslash-plus-letters, not a
// surrogate escape.
func firstUnpairedSurrogate(token string) string {
	for i := 0; i < len(token); {
		if token[i] != '\\' {
			i++
			continue
		}
		if i+1 >= len(token) {
			i++
			continue
		}
		switch token[i+1] {
		case '\\':
			// An escaped backslash consumes both characters; whatever follows
			// is literal text, including a "\ud800"-looking sequence.
			i += 2
			continue
		case 'u':
			r, valid := decodeJSONHexU(token, i)
			if !valid {
				i += 2 // malformed token; the document-level decode already rejected it
				continue
			}
			switch {
			case r >= 0xDC00 && r <= 0xDFFF:
				return token[i : i+6] // lone low surrogate
			case r >= 0xD800 && r <= 0xDBFF:
				// A high surrogate is valid only when the very next token is a
				// low surrogate escape; any other escape, character or end of
				// string leaves it unpaired.
				next, nextValid := decodeJSONHexU(token, i+6)
				if !nextValid || next < 0xDC00 || next > 0xDFFF {
					return token[i : i+6]
				}
				i += 12
				continue
			}
			i += 6
			continue
		}
		i += 2 // any other JSON escape (\", \n, \t, ...)
	}
	return ""
}

// decodeJSONHexU decodes the \uXXXX escape beginning at token[pos] (token[pos]
// is a backslash and token[pos+1] the letter u) into its code point.
func decodeJSONHexU(token string, pos int) (rune, bool) {
	if pos+6 > len(token) || token[pos] != '\\' || token[pos+1] != 'u' {
		return 0, false
	}
	var r rune
	for k := pos + 2; k < pos+6; k++ {
		var digit rune
		switch {
		case token[k] >= '0' && token[k] <= '9':
			digit = rune(token[k] - '0')
		case token[k] >= 'a' && token[k] <= 'f':
			digit = rune(token[k]-'a') + 10
		case token[k] >= 'A' && token[k] <= 'F':
			digit = rune(token[k]-'A') + 10
		default:
			return 0, false
		}
		r = r*16 + digit
	}
	return r, true
}

// unmarshalLineageName decodes one nodes-array element that already passed the
// string-token guard. An unpaired surrogate fails the whole import with an
// error that says the node name held an unpaired surrogate escape and quotes
// the escape in its original \uXXXX spelling.
func unmarshalLineageName(raw json.RawMessage) (string, error) {
	name, badEscape, ok := decodeLineageName(raw)
	if !ok {
		return "", errInvalid("lineage document nodes must be an array of JSON strings")
	}
	if badEscape != "" {
		return "", errInvalid("lineage document node name contains an unpaired Unicode surrogate escape '" +
			badEscape + "'; surrogate escapes are valid only as a matched " +
			`\uXXXX\uYYYY high-low pair`)
	}
	return name, nil
}

// unmarshalEndpointName decodes one edge endpoint ("from" or "to"). A missing
// or null value returns "" like an absent field so the existing
// missing-endpoint checks report it; a present non-string value keeps the
// edges type error; an unpaired surrogate fails the import, naming the
// endpoint it came from and quoting the original escape.
func unmarshalEndpointName(raw json.RawMessage, endpoint string) (string, error) {
	trimmed := strings.TrimSpace(string(raw))
	if trimmed == "" || trimmed == "null" {
		return "", nil
	}
	name, badEscape, ok := decodeLineageName(raw)
	if !ok {
		return "", errInvalid(`lineage document edges must be an array of {"from": ..., "to": ...} objects: ` +
			`endpoint "` + endpoint + `" must be a JSON string`)
	}
	if badEscape != "" {
		return "", errInvalid(`lineage document edge "` + endpoint + `" endpoint contains an ` +
			"unpaired Unicode surrogate escape '" + badEscape +
			"'; surrogate escapes are valid only as a matched " +
			`\uXXXX\uYYYY high-low pair`)
	}
	return name, nil
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
