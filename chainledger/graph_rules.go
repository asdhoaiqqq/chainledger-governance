// Package chainledger graph structure rules shared by every graph reader.
//
// Both on-disk graph files (UnmarshalGraphFile) and the graph embedded in a
// snapshot (ParseSnapshot) describe the same thing: a set of dataset names and
// each dataset's direct upstreams. The rules below are the single authority for
// turning that description into the validated, normalized adjacency both
// readers build on, so the two file formats can never disagree about whether
// one graph is legal or about which lineage relations it expresses:
//
//   - an empty dataset name is rejected;
//   - a dataset declared more than once is rejected;
//   - an upstream that is not itself a declared dataset is rejected, with the
//     referrer and the referenced name both in the error;
//   - a direct self-dependency or any indirect dependency cycle is rejected,
//     with the datasets on the cycle named;
//   - names are case-sensitive and surrounding whitespace is kept verbatim;
//   - every name's RAW string literal must decode faithfully: bytes that are
//     not valid UTF-8, or a \u escape forming an unpaired surrogate (a lone
//     high or low surrogate, or a low surrogate before its high surrogate),
//     reject the whole document — the decoder's U+FFFD rewrite is never kept
//     and the offending node or edge is never skipped, even when the rewrite
//     would collide with a real dataset or match the snapshot's declared
//     content identifier; that raw scan is shared by every graph and plan
//     reader and lives in name_encoding.go (the graph document scope is in
//     graph_name_encoding.go);
//   - record order and upstream order are irrelevant, duplicate upstreams count
//     as one relationship, and an empty upstream list marks a root, so an
//     empty graph stays legal;
//   - a known field declared twice within the same object (datasets in the
//     graph object, name or upstreams in a dataset record) is rejected,
//     however the two declarations are spelled; that JSON-level scan is
//     shared by every document reader and lives in duplicate_fields.go.
//
// The snapshot-only envelope rules (required fields, format version, content
// identifier, and duplicates of the snapshot's own fields) live in
// snapshot.go; this file is only the common graph structure, plus the shared
// old -> new node classification (diffDatasetNodes) that the batch report and
// the snapshot comparison both build on.
package chainledger

import (
	"fmt"
	"sort"
	"strings"
	"unicode/utf8"
)

// adjacency maps a dataset name to its sorted, duplicate-free parent names.
type adjacency map[string][]string

// validateInMemoryNamesUTF8 rejects an in-memory graph whose dataset name or
// direct upstream reference is not valid UTF-8.
//
// Such names cannot exist in a file read by the JSON readers (json.Unmarshal
// replaces invalid bytes with U+FFFD), but they can exist in a graph built
// through Register or by hand. They must never reach an exporter: encoding/json
// would silently replace every invalid byte with U+FFFD, so two different
// names ("p\xff" and "p\xfe") could serialize identically and a re-read graph
// would no longer be a faithful copy. The whole graph is rejected instead.
//
// Nodes are visited in name byte order and each node's parents in their stored
// order, so the first reported name is stable regardless of map iteration.
// The offending name is quoted with %q, which keeps the raw bytes readable
// ("p\xff" differs from "p\xfe", and a genuinely valid "p�" is shown as
// the replacement character itself). The graph is only read, never modified.
// Nil lineage nodes are skipped here; the caller rejects those separately.
func validateInMemoryNamesUTF8(graph map[string]*Lineage) error {
	names := make([]string, 0, len(graph))
	for name := range graph {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		if !utf8.ValidString(name) {
			return fmt.Errorf("%w: dataset name %q is not valid UTF-8, so the graph cannot be exported faithfully", ErrInvalidArgument, name)
		}
		entry := graph[name]
		if entry == nil {
			continue
		}
		for _, parent := range entry.Parents {
			if !utf8.ValidString(parent) {
				return fmt.Errorf("%w: dataset %q references upstream %q whose name is not valid UTF-8", ErrInvalidArgument, name, parent)
			}
		}
	}
	return nil
}

// normalizeDatasets builds the normalized parent adjacency from parsed dataset
// records. It rejects empty dataset names and datasets declared more than
// once; every surviving upstream list is sorted and deduplicated. It does not
// check that upstreams resolve to a declared dataset; use
// validateAdjacencyReferences for that.
func normalizeDatasets(datasets []GraphDataset) (adjacency, error) {
	adj := make(adjacency, len(datasets))
	for _, ds := range datasets {
		if ds.Name == "" {
			return nil, fmt.Errorf("%w: graph contains a dataset with an empty name", ErrInvalidArgument)
		}
		if _, exists := adj[ds.Name]; exists {
			return nil, fmt.Errorf("%w: dataset %q is declared more than once in the graph", ErrInvalidArgument, ds.Name)
		}
		adj[ds.Name] = uniqueSorted(ds.Upstreams)
	}
	return adj, nil
}

// validateAdjacencyReferences rejects an edge whose upstream is not a declared
// dataset. Referrers are visited in sorted order with their parents sorted, so
// the first reported (referrer, upstream) pair is stable regardless of map
// iteration or record order.
func validateAdjacencyReferences(adj adjacency) error {
	names := make([]string, 0, len(adj))
	for name := range adj {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		for _, parent := range adj[name] {
			if _, ok := adj[parent]; !ok {
				return fmt.Errorf("%w: dataset %q references upstream %q which is not registered", ErrNotFound, name, parent)
			}
		}
	}
	return nil
}

// validateGraphStructureFromFile runs every graph-structure rule shared by the
// file readers: it normalizes the records, rejects empty names, duplicate
// datasets, and unregistered upstreams, then rejects cycles. The returned
// adjacency is the canonical form of the graph's semantics.
func validateGraphStructureFromFile(datasets []GraphDataset) (adjacency, error) {
	adj, err := normalizeDatasets(datasets)
	if err != nil {
		return nil, err
	}
	if err := validateAdjacencyReferences(adj); err != nil {
		return nil, err
	}
	if err := validateAcyclic(adj); err != nil {
		return nil, err
	}
	return adj, nil
}

// validateAcyclic reports an error if the parent adjacency contains a cycle
// (including a dataset that depends on itself). The error message names the
// datasets involved.
func validateAcyclic(adj adjacency) error {
	const (
		white = 0
		gray  = 1
		black = 2
	)
	color := make(map[string]int, len(adj))
	var stack []string

	var dfs func(node string) error
	dfs = func(node string) error {
		color[node] = gray
		stack = append(stack, node)
		for _, parent := range adj[node] {
			switch color[parent] {
			case gray:
				// parent is an ancestor of node in the current DFS path, so
				// following parent edges from parent leads back to parent.
				idx := 0
				for stack[idx] != parent {
					idx++
				}
				cycle := make([]string, 0, len(stack)-idx+1)
				cycle = append(cycle, stack[idx:]...)
				cycle = append(cycle, parent)
				return fmt.Errorf("%w: dependency cycle %s", ErrCycle, strings.Join(cycle, " -> "))
			case white:
				if err := dfs(parent); err != nil {
					return err
				}
			}
		}
		color[node] = black
		stack = stack[:len(stack)-1]
		return nil
	}

	// Iterate over a sorted snapshot so the first reported cycle is stable
	// across runs regardless of map iteration order.
	names := make([]string, 0, len(adj))
	for name := range adj {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		if color[name] == white {
			if err := dfs(name); err != nil {
				return err
			}
		}
	}
	return nil
}

// datasetDiff is the old -> new classification of dataset nodes shared by the
// batch report (computeBatch) and the snapshot comparison (CompareSnapshots),
// so the two reports can never disagree about what changed between an older
// and a newer version of one graph. Each list is sorted by name byte order
// and may be nil when empty; callers normalize nil to an empty list for JSON.
type datasetDiff struct {
	New     []string // present only in the new graph
	Removed []string // present only in the old graph
	Changed []string // present in both, direct upstream set differs
}

// diffDatasetNodes classifies the dataset nodes of two normalized adjacencies
// in the fixed direction old -> new:
//
//   - a name only the new graph carries is new — even when it arrives with
//     upstreams, it is never also an upstream change;
//   - a name only the old graph carries is removed — it is never also an
//     upstream change;
//   - a name both graphs carry is changed only when its direct upstream set
//     actually differs; a dataset that survives but had its upstreams cleared
//     became a root and counts as changed, not removed.
//
// Names are case-sensitive and compared verbatim. Both adjacencies are
// normalized (sorted, duplicate-free parent lists), so record order, upstream
// order, and repeated upstreams carry no semantics and cannot manufacture a
// difference; comparing a graph with itself — empty or not — yields three
// empty lists. Mentioning a dataset is not what makes it changed: only the
// two graphs decide.
func diffDatasetNodes(oldAdj, newAdj adjacency) datasetDiff {
	var diff datasetDiff
	for name, newParents := range newAdj {
		oldParents, ok := oldAdj[name]
		switch {
		case !ok:
			diff.New = append(diff.New, name)
		case !stringSliceEqual(oldParents, newParents):
			diff.Changed = append(diff.Changed, name)
		}
	}
	for name := range oldAdj {
		if _, ok := newAdj[name]; !ok {
			diff.Removed = append(diff.Removed, name)
		}
	}
	sort.Strings(diff.New)
	sort.Strings(diff.Removed)
	sort.Strings(diff.Changed)
	return diff
}

// materializeLineage turns validated, normalized parent adjacency into the
// in-memory lineage structure every graph reader and every successful batch
// leave behind, so both operations follow exactly one relationship rule:
//
//   - one node exists per adjacency key, carrying an independent copy of its
//     sorted, duplicate-free parent list;
//   - children are derived in a second pass from the parent edges, so every
//     dataset's direct upstreams correspond to those upstreams' direct
//     downstreams, with each downstream registered once.
//
// into is optional: when nil a fresh graph map is allocated and returned
// (graph reads); when an existing graph is given (batch applies), nodes absent
// from adjacency are deleted and the surviving *Lineage records are reused in
// place, so Go callers that already hold the map or a node pointer keep reading
// the updated relationships through their own records. Replacing a node's
// lists with fresh copies keeps the result independent of adjacency: editing
// the plan's final graph or a previously returned report cannot move the
// materialized graph.
func materializeLineage(into map[string]*Lineage, adj adjacency) map[string]*Lineage {
	if into == nil {
		into = make(map[string]*Lineage, len(adj))
	}
	// Nodes absent from the adjacency no longer exist; delete them before any
	// edge is rebuilt, so a removed dataset cannot leave its relationships in
	// a retained node's lists.
	for name := range into {
		if _, ok := adj[name]; !ok {
			delete(into, name)
		}
	}
	// Install the parent lists first, clearing children. A full rebuild below
	// derives every child from the parent edges; that wholesale derivation is
	// what guarantees replaced edges vanish from the old upstream, new
	// upstreams list the downstream once, and untouched relations survive.
	for name, parents := range adj {
		entry := into[name]
		if entry == nil {
			entry = &Lineage{}
			into[name] = entry
		}
		entry.Dataset = name
		entry.Parents = append([]string(nil), parents...)
		entry.Children = nil
	}
	for name, parents := range adj {
		for _, parent := range parents {
			into[parent].Children = append(into[parent].Children, name)
		}
	}
	for name := range into {
		into[name].Children = uniqueSorted(into[name].Children)
	}
	return into
}

// lineageFromAdjacency rebuilds a fresh in-memory graph from validated,
// normalized parent adjacency. It is the read path of the shared relationship
// rule in materializeLineage; the result is independent of the caller's record
// slices.
func lineageFromAdjacency(adj adjacency) map[string]*Lineage {
	return materializeLineage(nil, adj)
}
