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
// snapshot.go; this file is only the common graph structure.
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
// firstMissingReference for that.
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

// firstMissingReference is the single authority for the direct-upstream rule,
// shared by every graph that gets checked: the graph read from a file, the
// graph embedded in a snapshot, a live in-memory graph, and the final graph a
// batch converges on. Every direct upstream a dataset names must be a dataset
// that exists in the graph being checked; nothing else is judged here (the
// cycle rule lives in validateAcyclic).
//
// Referrers are visited in sorted name byte order and each referrer's parents
// in sorted byte order, so the first reported (referrer, upstream) pair is
// stable regardless of map iteration, record order, or upstream input order.
// Names are compared verbatim — case and surrounding whitespace matter — and
// a parent list is expected to already be normalized, so a repeated upstream
// is one relationship and can only be reported once. An empty graph or a
// dataset with no parents yields ok == false.
func firstMissingReference(adj adjacency) (referrer, referenced string, ok bool) {
	names := make([]string, 0, len(adj))
	for name := range adj {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		for _, parent := range adj[name] {
			if _, exists := adj[parent]; !exists {
				return name, parent, true
			}
		}
	}
	return "", "", false
}

// missingReferenceError rejects an existing graph — one read from a file,
// embedded in a snapshot, or held in memory — that references a name that is
// not a dataset of that graph. The name is described as "not registered", the
// wording every existing-graph check has always used; the referrer and the
// referenced name are both quoted, and the failure is ErrNotFound so Go callers
// can recognize it with errors.Is.
func missingReferenceError(referrer, referenced string) error {
	return fmt.Errorf("%w: dataset %q references upstream %q which is not registered", ErrNotFound, referrer, referenced)
}

// finalGraphMissingReferenceError rejects a batch whose final graph — the
// graph after every add, replacement, and removal — still references a name
// that is absent from that final graph. This is the same direct-upstream rule
// as missingReferenceError (both run firstMissingReference), but judged on the
// final graph and worded for that stage: a dataset newly added in the same
// batch may reference another dataset declared later in the plan, while a
// retained dataset that still points at a removed name is rejected with no
// automatic edge removal or conversion to a root. The referrer and the
// referenced name are both quoted, and the failure is ErrNotFound.
func finalGraphMissingReferenceError(referrer, referenced string) error {
	return fmt.Errorf("%w: dataset %q references upstream %q which does not exist in the final graph", ErrNotFound, referrer, referenced)
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
	return validateGraphAdjacency(adj)
}

// validateGraphAdjacency runs the structure rules that apply to an existing
// graph after its records have been normalized (empty names and duplicate
// declarations already rejected): every direct upstream must be a declared
// dataset, and the parent edges must be acyclic. It is the shared tail of both
// existing-graph structure authorities, validateGraphStructureFromFile for
// parsed documents and validatedGraphAdjacency for live in-memory graphs, so
// the two inputs can never disagree about whether one set of direct-upstream
// relations is legal. The batch path checks the same direct-upstream rule on
// its final graph with firstMissingReference directly; the two stages stay
// distinct because an existing graph is rejected before any plan is judged.
func validateGraphAdjacency(adj adjacency) (adjacency, error) {
	if referrer, referenced, missing := firstMissingReference(adj); missing {
		return nil, missingReferenceError(referrer, referenced)
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
