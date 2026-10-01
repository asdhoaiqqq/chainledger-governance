package chainledger

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
)

// Batch adjustment entry points validate one proposed lineage graph against
// one current graph as a single all-or-nothing unit, so multi-dataset changes
// (including datasets referencing each other inside the same batch) are judged
// by the shape the graph will have AFTER every replacement lands.
var (
	// ErrMalformedGraph is returned when a serialized graph cannot be used:
	// bad JSON, duplicate datasets, empty names, missing upstreams or cycles.
	ErrMalformedGraph = errors.New("malformed lineage graph")
	// ErrMalformedPlan is returned when an adjustment plan is invalid:
	// duplicate dataset declarations, empty names or unknown upstreams.
	ErrMalformedPlan = errors.New("malformed adjustment plan")
)

// GraphJSON is the on-disk shape of a lineage graph: one record per dataset
// holding the dataset name and its direct upstream (parent) names.
type GraphJSON struct {
	Nodes []GraphNode `json:"nodes"`
}

// GraphNode is one dataset record in a serialized lineage graph.
type GraphNode struct {
	Name      string   `json:"name"`
	Upstreams []string `json:"upstreams"`
}

// PlanJSON is the on-disk shape of an adjustment plan: records may register
// new datasets or replace every direct upstream of an existing dataset.
type PlanJSON struct {
	Adjustments []Adjustment `json:"adjustments"`
}

// Adjustment is one proposed replacement: the dataset named Name ends up with
// exactly Upstreams as its direct parents (an empty list makes it a root).
type Adjustment struct {
	Name      string   `json:"name"`
	Upstreams []string `json:"upstreams"`
}

// Relationship is one directed upstream->dataset edge.
type Relationship struct {
	Upstream   string `json:"upstream"`
	Downstream string `json:"downstream"`
}

// BatchReport is the deterministic result of previewing or applying a plan.
//
// FinalGraph is the graph as it exists after every replacement. NewDatasets
// lists datasets the plan introduced; ChangedDatasets lists pre-existing
// datasets whose direct upstream SET actually changed (input order or mere
// duplicate upstreams never count). AddedRelationships and RemovedRelationships
// are the edge-level diff. AffectedDatasets are other downstream datasets
// reachable from a new or directly-changed dataset in the graph before or
// after the change, excluding the new and directly-changed names themselves.
// Every list is sorted and every empty list serializes as [].
type BatchReport struct {
	FinalGraph           GraphJSON      `json:"final_graph"`
	NewDatasets          []string       `json:"new_datasets"`
	ChangedDatasets      []string       `json:"changed_datasets"`
	AddedRelationships   []Relationship `json:"added_relationships"`
	RemovedRelationships []Relationship `json:"removed_relationships"`
	AffectedDatasets     []string       `json:"affected_datasets"`
}

// GraphData is a decoded lineage graph: node name -> sorted unique parents.
type GraphData struct {
	parents map[string][]string
}

// PlanData is a decoded adjustment plan: dataset name -> sorted unique wanted
// parents, in record-independent form (declaration order never matters).
type PlanData struct {
	parents map[string][]string
}

// ParseGraph decodes and validates a serialized lineage graph. A graph is
// rejected (not silently repaired) when it contains a duplicated dataset, an
// empty dataset or upstream name, an upstream that is not itself a dataset, or
// a cycle. The empty graph (no nodes) is valid.
func ParseGraph(data []byte) (*GraphData, error) {
	raw := GraphJSON{}
	if err := unmarshalNonUnknown(data, &raw); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrMalformedGraph, err)
	}
	parents := make(map[string][]string, len(raw.Nodes))
	for i, node := range raw.Nodes {
		if node.Name == "" {
			return nil, fmt.Errorf("%w: dataset name is required (record #%d)", ErrMalformedGraph, i+1)
		}
		if _, dup := parents[node.Name]; dup {
			return nil, fmt.Errorf("%w: dataset %q is declared more than once", ErrMalformedGraph, node.Name)
		}
		parents[node.Name] = uniqueSorted(node.Upstreams)
	}
	if err := validateReferences(parents, "graph"); err != nil {
		return nil, err
	}
	if cycle := findCycle(parents); len(cycle) > 0 {
		return nil, fmt.Errorf("%w: graph already contains a cycle (%s); fix the graph before adjusting it", ErrMalformedGraph, formatCycle(cycle))
	}
	return &GraphData{parents: parents}, nil
}

// ParsePlan decodes and validates a serialized adjustment plan. It rejects a
// duplicated dataset declaration, an empty dataset or upstream name, and an
// upstream that will not exist in the fully-replaced graph (current plus the
// datasets the plan introduces). Legality of the final shape (cycles) is
// checked by Preview/Apply against the given current graph.
func ParsePlan(data []byte, current *GraphData) (*PlanData, error) {
	if current == nil {
		current = &GraphData{parents: map[string][]string{}}
	}
	raw := PlanJSON{}
	if err := unmarshalNonUnknown(data, &raw); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrMalformedPlan, err)
	}
	parents := make(map[string][]string, len(raw.Adjustments))
	order := make([]string, 0, len(raw.Adjustments))
	for i, adj := range raw.Adjustments {
		if adj.Name == "" {
			return nil, fmt.Errorf("%w: adjustment #%d has an empty dataset name", ErrMalformedPlan, i+1)
		}
		if _, dup := parents[adj.Name]; dup {
			return nil, fmt.Errorf("%w: dataset %q is declared more than once in the plan", ErrMalformedPlan, adj.Name)
		}
		for _, up := range adj.Upstreams {
			if up == "" {
				return nil, fmt.Errorf("%w: dataset %q lists an empty upstream name", ErrMalformedPlan, adj.Name)
			}
		}
		parents[adj.Name] = uniqueSorted(adj.Upstreams)
		order = append(order, adj.Name)
	}

	// An upstream must exist once every replacement has landed: either it is a
	// dataset already in the current graph, or the plan registers it.
	for _, name := range order {
		for _, up := range parents[name] {
			if _, isCurrent := current.parents[up]; isCurrent {
				continue
			}
			if _, isNew := parents[up]; isNew {
				continue
			}
			return nil, fmt.Errorf("%w: dataset %q references upstream %q which does not exist in the current graph and is not registered by the plan", ErrMalformedPlan, name, up)
		}
	}
	return &PlanData{parents: parents}, nil
}

// Preview computes the report of applying plan to current without changing
// current. It rejects the whole batch when the resulting graph would contain
// a self dependency or an indirect cycle. Preview and Apply share this exact
// computation, so their verdicts and reports for the same inputs are identical.
func Preview(current *GraphData, plan *PlanData) (*BatchReport, error) {
	final := composeFinal(current, plan)
	if cycle := findCycle(final); len(cycle) > 0 {
		return nil, fmt.Errorf("%w: applying the plan would create a cycle (%s)", ErrMalformedPlan, formatCycle(cycle))
	}

	var newDatasets, changedDatasets []string
	for name := range plan.parents {
		if _, existed := current.parents[name]; existed {
			if !sameStringSet(current.parents[name], plan.parents[name]) {
				changedDatasets = append(changedDatasets, name)
			}
		} else {
			newDatasets = append(newDatasets, name)
		}
	}
	sort.Strings(newDatasets)
	sort.Strings(changedDatasets)
	// Every new or directly-changed dataset is both a traversal origin and an
	// exclusion from the impact list.
	directDatasets := unionSorted(newDatasets, changedDatasets)

	oldEdges := edgeSet(current.parents)
	newEdges := edgeSet(final)
	var added, removed []Relationship
	for edge := range newEdges {
		if !oldEdges[edge] {
			added = append(added, Relationship{Upstream: edge.up, Downstream: edge.down})
		}
	}
	for edge := range oldEdges {
		if !newEdges[edge] {
			removed = append(removed, Relationship{Upstream: edge.up, Downstream: edge.down})
		}
	}
	sortRelationships(added)
	sortRelationships(removed)

	report := &BatchReport{
		FinalGraph:           graphToJSON(final),
		NewDatasets:          nonNilStrings(newDatasets),
		ChangedDatasets:      nonNilStrings(changedDatasets),
		AddedRelationships:   nonNilRelationships(added),
		RemovedRelationships: nonNilRelationships(removed),
		AffectedDatasets:     nonNilStrings(affectedDatasets(current.parents, final, directDatasets, directDatasets)),
	}
	return report, nil
}

// Apply returns the graph produced by applying plan to current. The graph
// returned is the same FinalGraph carried by the report; callers persist it
// with MarshalGraph. current itself is never modified.
func Apply(current *GraphData, plan *PlanData) (*GraphData, *BatchReport, error) {
	report, err := Preview(current, plan)
	if err != nil {
		return nil, nil, err
	}
	final := composeFinal(current, plan)
	return &GraphData{parents: final}, report, nil
}

// MarshalGraph renders a graph in the canonical JSON form: datasets sorted by
// name (byte order), upstream lists sorted by name. Empty lists render as [].
// The same graph always produces the same bytes regardless of insertion or
// declaration order.
func MarshalGraph(graph *GraphData) ([]byte, error) {
	return marshalCanonical(graphToJSON(graph.parents))
}

// MarshalReport renders a report in the canonical JSON form with every list
// sorted and every empty list rendered as []. Equal reports are byte-equal.
func MarshalReport(report *BatchReport) ([]byte, error) {
	return marshalCanonical(report)
}

// composeFinal overlays every planned replacement onto a copy of the current
// parent sets. Datasets absent from the plan keep their original upstreams.
func composeFinal(current *GraphData, plan *PlanData) map[string][]string {
	final := make(map[string][]string, len(current.parents)+len(plan.parents))
	for name, parents := range current.parents {
		final[name] = append([]string(nil), parents...)
	}
	for name, parents := range plan.parents {
		final[name] = append([]string(nil), parents...)
	}
	return final
}

// edge is one directed up->down relationship.
type edge struct{ up, down string }

func edgeSet(parents map[string][]string) map[edge]bool {
	set := make(map[edge]bool)
	for down, ups := range parents {
		for _, up := range ups {
			set[edge{up: up, down: down}] = true
		}
	}
	return set
}

// affectedDatasets computes the blast radius: starting from every new or
// directly-changed dataset, follow downstream (child) edges in BOTH the graph
// before the change and the graph after it. A dataset reached only through an
// edge the change removes is still impacted, which is why the pre-change graph
// matters. New and directly-changed datasets themselves are excluded; each
// dataset counts once regardless of how many paths reach it.
func affectedDatasets(before, after map[string][]string, roots, excluded []string) []string {
	excludedSet := toSet(excluded)
	affected := map[string]bool{}
	collect := func(parents map[string][]string) {
		children := make(map[string][]string)
		for down, ups := range parents {
			for _, up := range ups {
				children[up] = append(children[up], down)
			}
		}
		for _, start := range roots {
			seen := map[string]bool{start: true}
			stack := append([]string(nil), children[start]...)
			for len(stack) > 0 {
				node := stack[len(stack)-1]
				stack = stack[:len(stack)-1]
				if seen[node] {
					continue
				}
				seen[node] = true
				if !excludedSet[node] {
					affected[node] = true
				}
				stack = append(stack, children[node]...)
			}
		}
	}
	collect(before)
	collect(after)

	out := make([]string, 0, len(affected))
	for name := range affected {
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}

// validateReferences rejects empty upstream names and upstreams that are not
// themselves datasets. Unique sorted parents are assumed.
func validateReferences(parents map[string][]string, scope string) error {
	for name, ups := range parents {
		for _, up := range ups {
			if up == "" {
				return fmt.Errorf("%w: dataset %q has an empty upstream name", ErrMalformedGraph, name)
			}
			if _, ok := parents[up]; !ok {
				return fmt.Errorf("%w: dataset %q references upstream %q which is not present in the %s", ErrMalformedGraph, name, up, scope)
			}
		}
	}
	return nil
}

// findCycle finds a cycle following parent edges and returns its nodes in
// dependency order with the first node repeated at the end (e.g. [A B A] for
// A->B->A, [A A] for a self dependency). It returns nil for an acyclic graph.
func findCycle(parents map[string][]string) []string {
	const (
		white = 0
		gray  = 1
		black = 2
	)
	color := make(map[string]int, len(parents))
	names := make([]string, 0, len(parents))
	for name := range parents {
		names = append(names, name)
	}
	sort.Strings(names)

	var stack []string
	var dfs func(string) []string
	dfs = func(node string) []string {
		color[node] = gray
		stack = append(stack, node)
		for _, parent := range parents[node] {
			if color[parent] == gray {
				// Cycle closes from node to an ancestor already on the stack;
				// trim the stack to that ancestor and repeat it at the end.
				start := 0
				for stack[start] != parent {
					start++
				}
				cycle := append([]string(nil), stack[start:]...)
				return append(cycle, parent)
			}
			if color[parent] == white {
				if found := dfs(parent); found != nil {
					return found
				}
			}
		}
		color[node] = black
		stack = stack[:len(stack)-1]
		return nil
	}
	for _, node := range names {
		if color[node] == white {
			if found := dfs(node); found != nil {
				return found
			}
		}
	}
	return nil
}

// formatCycle renders a findCycle path as "A -> B -> A".
func formatCycle(cycle []string) string {
	var b bytes.Buffer
	for i, name := range cycle {
		if i > 0 {
			b.WriteString(" -> ")
		}
		b.WriteString(name)
	}
	return b.String()
}

func graphToJSON(parents map[string][]string) GraphJSON {
	names := make([]string, 0, len(parents))
	for name := range parents {
		names = append(names, name)
	}
	sort.Strings(names)
	nodes := make([]GraphNode, 0, len(names))
	for _, name := range names {
		ups := parents[name]
		if ups == nil {
			ups = []string{}
		}
		nodes = append(nodes, GraphNode{Name: name, Upstreams: ups})
	}
	return GraphJSON{Nodes: nodes}
}

func sortRelationships(rels []Relationship) {
	sort.Slice(rels, func(i, j int) bool {
		if rels[i].Upstream != rels[j].Upstream {
			return rels[i].Upstream < rels[j].Upstream
		}
		return rels[i].Downstream < rels[j].Downstream
	})
}

// unionSorted merges two sorted, unique slices into one sorted, unique slice.
func unionSorted(a, b []string) []string {
	out := make([]string, 0, len(a)+len(b))
	i, j := 0, 0
	for i < len(a) || j < len(b) {
		switch {
		case j == len(b) || (i < len(a) && a[i] < b[j]):
			out = append(out, a[i])
			i++
		case i == len(a) || b[j] < a[i]:
			out = append(out, b[j])
			j++
		default:
			out = append(out, a[i])
			i++
			j++
		}
	}
	return out
}

func sameStringSet(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// nonNilStrings guarantees empty lists serialize as [] rather than null.
func nonNilStrings(names []string) []string {
	if names == nil {
		return []string{}
	}
	return names
}

// nonNilRelationships guarantees empty lists serialize as [] rather than null.
func nonNilRelationships(rels []Relationship) []Relationship {
	if rels == nil {
		return []Relationship{}
	}
	return rels
}

// unmarshalNonUnknown decodes one JSON value and rejects trailing content, so
// a malformed document with extra values after it cannot be silently accepted.
func unmarshalNonUnknown(data []byte, target any) error {
	dec := json.NewDecoder(bytes.NewReader(data))
	if err := dec.Decode(target); err != nil {
		return err
	}
	if dec.More() {
		return errors.New("unexpected trailing JSON content")
	}
	return nil
}

// marshalCanonical serializes with deterministic key order (struct order) and
// no interface-map key sorting concerns.
func marshalCanonical(value any) ([]byte, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	if err := enc.Encode(value); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}
