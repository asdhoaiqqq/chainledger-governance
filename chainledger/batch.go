// Package chainledger implements the on-chain data governance core.
package chainledger

import (
	"encoding/json"
	"fmt"
	"sort"
	"unicode/utf8"
)

// PlanChange is one record in a lineage adjustment plan: the dataset name and
// the complete list of its direct upstreams after the batch is applied.
//
// A change may name a dataset that does not exist yet (it is registered) or an
// existing dataset (its direct upstreams are replaced wholesale). Datasets not
// mentioned in the plan keep their current direct upstreams.
type PlanChange struct {
	Name      string   `json:"name"`
	Upstreams []string `json:"upstreams"`
}

// Plan is a batch of lineage adjustments read from a JSON file.
//
// Removals names datasets that must be absent from the final graph. Deleting a
// name that does not currently exist is a no-op (it is not reported as an
// actual removal), so a successful plan can be applied again with empty change
// and impact lists. A name listed in Removals must not also appear in Changes.
type Plan struct {
	Changes  []PlanChange `json:"changes"`
	Removals []string     `json:"removals"`
}

// Relation is a directed lineage edge: Upstream -> Downstream, meaning the
// downstream dataset derives from the upstream dataset.
type Relation struct {
	Upstream   string `json:"upstream"`
	Downstream string `json:"downstream"`
}

// GraphDataset is one dataset record in the on-disk graph JSON.
type GraphDataset struct {
	Name      string   `json:"name"`
	Upstreams []string `json:"upstreams"`
}

// GraphFile is the on-disk JSON representation of a lineage graph.
type GraphFile struct {
	Datasets []GraphDataset `json:"datasets"`
}

// BatchReport is the deterministic report produced by preview and apply.
//
// All slices are non-nil (empty lists encode as [] in JSON). Datasets and name
// lists are sorted by name byte order; relation lists are sorted by upstream
// then downstream.
type BatchReport struct {
	FinalGraph          GraphFile  `json:"finalGraph"`
	NewDatasets         []string   `json:"newDatasets"`
	ChangedDatasets     []string   `json:"changedDatasets"`
	RemovedDatasets     []string   `json:"removedDatasets"`
	AddedRelations      []Relation `json:"addedRelations"`
	RemovedRelations    []Relation `json:"removedRelations"`
	AffectedDownstreams []string   `json:"affectedDownstreams"`
}

// graphAdjacency derives the normalized parent map from the in-memory graph.
// It rejects nil lineage nodes, empty names, names that are not valid UTF-8
// (dataset names or direct upstream references), and references to upstreams
// that are not registered; the name/reference rules are the shared ones from
// normalizeDatasets and validateAdjacencyReferences, also used by the file and
// snapshot readers. It does not check for cycles; use validateAcyclic for that.
func graphAdjacency(graph map[string]*Lineage) (adjacency, error) {
	if graph == nil {
		return nil, fmt.Errorf("%w: create the graph map with make before validating", ErrNotInitialized)
	}
	// Check encoding before anything that would quote these names or turn them
	// into JSON: a missing-upstream check must not label an unregistered,
	// non-UTF-8 upstream as ErrNotFound when the name itself is unusable, and
	// json.Marshal would silently rewrite an invalid byte into U+FFFD. The
	// check is read-only and reports the offending raw bytes.
	if err := validateInMemoryNamesUTF8(graph); err != nil {
		return nil, err
	}
	records := make([]GraphDataset, 0, len(graph))
	for name, entry := range graph {
		if entry == nil {
			return nil, fmt.Errorf("%w: dataset %q points to a nil lineage node", ErrInvalidArgument, name)
		}
		if name == "" {
			return nil, fmt.Errorf("%w: dataset name is required", ErrInvalidArgument)
		}
		records = append(records, GraphDataset{Name: name, Upstreams: entry.Parents})
	}
	adj, err := normalizeDatasets(records)
	if err != nil {
		return nil, err
	}
	if err := validateAdjacencyReferences(adj); err != nil {
		return nil, err
	}
	return adj, nil
}

// ValidateGraph checks the in-memory graph for structural problems: nil nodes,
// empty names, names that are not valid UTF-8 (dataset names or direct upstream
// references), missing upstreams, and cycles. It does not mutate the graph.
//
// A graph that fails validation cannot be used as the basis for a batch: an
// adjustment plan must not be allowed to paper over pre-existing corruption.
func ValidateGraph(graph map[string]*Lineage) error {
	adj, err := graphAdjacency(graph)
	if err != nil {
		return err
	}
	return validateAcyclic(adj)
}

// validatePlanNamesUTF8 rejects a plan in which any name that takes part in
// the adjustment — a change record's name, one of its direct upstream names,
// or a removals entry — is not valid UTF-8. It is the in-memory counterpart
// of the raw-literal scan the plan reader runs (checkPlanNameEncoding): a
// Plan built directly in Go never passed through that reader, so without
// this gate PreviewBatch and ApplyBatch could register datasets the graph
// exporter must refuse and produce reports whose JSON silently rewrites the
// bad bytes to U+FFFD (two distinct corrupted names would then print as one).
//
// Plan order is checked as given: the name of record i comes before its
// upstreams, and removals follow all changes, so the first reported position
// is the earliest offending name the caller submitted. The error names the
// field ("name", "upstreams", or "removals"), the zero-based record or array
// position, and — for an upstream — its zero-based position inside that
// record. The offending name is quoted with %q, which renders the original
// bytes as \x escapes rather than replacing them with U+FFFD, so "p\xff" and
// "p\xfe" stay distinguishable. The plan is only read, never normalized or
// mutated. Register keeps accepting raw Go strings; this gate belongs only
// to the batch boundary, where an unusable name would enter a graph and a
// report that are promised exportable.
func validatePlanNamesUTF8(plan Plan) error {
	for i, change := range plan.Changes {
		if !utf8.ValidString(change.Name) {
			return fmt.Errorf("%w: field %q in the change record at index %d of %q contains invalid UTF-8 bytes: %q",
				ErrInvalidArgument, "name", i, "changes", change.Name)
		}
		for j, upstream := range change.Upstreams {
			if !utf8.ValidString(upstream) {
				// Report the unusable name itself, never a missing-upstream
				// error: the encoding defect takes precedence over whether
				// the referenced dataset exists in the final graph.
				return fmt.Errorf("%w: field %q at index %d in the change record at index %d of %q contains invalid UTF-8 bytes: %q",
					ErrInvalidArgument, "upstreams", j, i, "changes", upstream)
			}
		}
	}
	for i, name := range plan.Removals {
		if !utf8.ValidString(name) {
			return fmt.Errorf("%w: field %q at index %d contains invalid UTF-8 bytes: %q",
				ErrInvalidArgument, "removals", i, name)
		}
	}
	return nil
}

// computeBatch validates the graph and plan, then computes the final adjacency
// and the diff report. It does not mutate graph. The returned adjacency is the
// graph as it will be after the batch; callers that want to commit it should
// pass it to applyAdjacency.
func computeBatch(graph map[string]*Lineage, plan Plan) (*BatchReport, adjacency, error) {
	if err := ValidateGraph(graph); err != nil {
		return nil, nil, err
	}
	original, err := graphAdjacency(graph)
	if err != nil {
		return nil, nil, err
	}

	// Check every name the plan carries before anything that would quote it,
	// hash it, or turn the report into JSON: the file reader already rejects
	// these at the raw-literal level, but a plan constructed directly through
	// the Go API bypasses that reader, and without this gate it could register
	// a dataset whose name cannot be exported — two names differing only in an
	// invalid byte ("p\xff" vs "p\xfe") would even render identically once the
	// report is marshaled. This is the in-memory counterpart of
	// checkPlanNameEncoding; see plan_name_encoding.go.
	if err := validatePlanNamesUTF8(plan); err != nil {
		return nil, nil, err
	}

	// Validate the plan: empty names and duplicate declarations reject the
	// whole batch, naming the dataset and the reason.
	declarations := make(map[string]int, len(plan.Changes))
	for _, change := range plan.Changes {
		if change.Name == "" {
			return nil, nil, fmt.Errorf("%w: plan contains a dataset with an empty name", ErrInvalidArgument)
		}
		declarations[change.Name]++
		if declarations[change.Name] > 1 {
			return nil, nil, fmt.Errorf("%w: dataset %q is declared %d times in the plan", ErrInvalidArgument, change.Name, declarations[change.Name])
		}
	}

	// Validate removals: empty names, duplicate names, and names also declared
	// as changes all reject the whole batch with the name and reason.
	removalDecls := make(map[string]int, len(plan.Removals))
	for _, name := range plan.Removals {
		if name == "" {
			return nil, nil, fmt.Errorf("%w: plan removals contain a dataset with an empty name", ErrInvalidArgument)
		}
		removalDecls[name]++
		if removalDecls[name] > 1 {
			return nil, nil, fmt.Errorf("%w: dataset %q is listed %d times in removals", ErrInvalidArgument, name, removalDecls[name])
		}
		if _, ok := declarations[name]; ok {
			return nil, nil, fmt.Errorf("%w: dataset %q appears in both changes and removals", ErrInvalidArgument, name)
		}
	}

	// Build the final adjacency: start from the original parents, replace each
	// planned dataset's parents with its normalized upstreams.
	final := make(adjacency, len(original)+len(plan.Changes))
	for name, parents := range original {
		final[name] = append([]string(nil), parents...)
	}
	for name := range removalDecls {
		delete(final, name)
	}
	var newNames []string
	var changedNames []string
	for _, change := range plan.Changes {
		wanted := uniqueSorted(change.Upstreams)
		old, existed := original[change.Name]
		switch {
		case !existed:
			newNames = append(newNames, change.Name)
		case !stringSliceEqual(old, wanted):
			changedNames = append(changedNames, change.Name)
		}
		final[change.Name] = wanted
	}
	sort.Strings(newNames)
	sort.Strings(changedNames)

	// Datasets actually removed: names in removals that existed in the
	// original graph. Deleting a name that does not exist is a no-op and is
	// not reported, so a successful plan can be applied again with empty lists.
	var removedNames []string
	for name := range removalDecls {
		if _, existed := original[name]; existed {
			removedNames = append(removedNames, name)
		}
	}
	sort.Strings(removedNames)

	// Every upstream must resolve to a dataset that exists in the FINAL graph
	// (new datasets may reference each other regardless of plan order; a
	// retained dataset that still references a deleted name is rejected here,
	// with both the referrer and the referenced name in the error). Iterate
	// over sorted names so the reported pair is deterministic.
	names := make([]string, 0, len(final))
	for name := range final {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		for _, parent := range final[name] {
			if _, ok := final[parent]; !ok {
				return nil, nil, fmt.Errorf("%w: dataset %q references upstream %q which does not exist in the final graph", ErrNotFound, name, parent)
			}
		}
	}
	// The final graph must be acyclic, judged after all replacements.
	if err := validateAcyclic(final); err != nil {
		return nil, nil, err
	}

	added, removed := diffRelations(original, final)
	affected := affectedDownstreams(original, final, newNames, changedNames, removedNames)

	report := &BatchReport{
		FinalGraph:          GraphFile{Datasets: adjacencyToDatasets(final)},
		NewDatasets:         newNames,
		ChangedDatasets:     changedNames,
		RemovedDatasets:     removedNames,
		AddedRelations:      added,
		RemovedRelations:    removed,
		AffectedDownstreams: affected,
	}
	// Guarantee non-nil slices so JSON encodes empty lists as [].
	if report.NewDatasets == nil {
		report.NewDatasets = []string{}
	}
	if report.ChangedDatasets == nil {
		report.ChangedDatasets = []string{}
	}
	if report.RemovedDatasets == nil {
		report.RemovedDatasets = []string{}
	}
	if report.AddedRelations == nil {
		report.AddedRelations = []Relation{}
	}
	if report.RemovedRelations == nil {
		report.RemovedRelations = []Relation{}
	}
	if report.AffectedDownstreams == nil {
		report.AffectedDownstreams = []string{}
	}
	return report, final, nil
}

// PreviewBatch validates the graph and plan and returns the report WITHOUT
// mutating graph. Preview and apply for the same graph and plan produce
// identical reports.
//
// As well as the structural plan rules (empty names, duplicate declarations,
// unresolvable upstreams, cycles), every name the plan carries — a change
// record's name, any of its direct upstreams, and every removals entry — must
// be valid UTF-8 even when the plan was constructed directly in Go instead of
// read by UnmarshalPlan. A name that is not valid UTF-8 rejects the whole
// batch with ErrInvalidArgument before any dataset could be registered or any
// report produced, so a success report can always be marshaled to JSON and an
// applied graph can always be exported. See validatePlanNamesUTF8.
func PreviewBatch(graph map[string]*Lineage, plan Plan) (*BatchReport, error) {
	report, _, err := computeBatch(graph, plan)
	return report, err
}

// ApplyBatch validates the plan, applies it to graph, and returns the report.
// On rejection — including a plan that names a dataset, upstream, or removal
// with bytes that are not valid UTF-8 — graph is left completely unchanged.
func ApplyBatch(graph map[string]*Lineage, plan Plan) (*BatchReport, error) {
	report, final, err := computeBatch(graph, plan)
	if err != nil {
		return nil, err
	}
	applyAdjacency(graph, final)
	return report, nil
}

// applyAdjacency rewrites the in-memory graph to match the final adjacency:
// nodes absent from final are removed, parents are replaced, and children are
// rebuilt from the parent edges.
func applyAdjacency(graph map[string]*Lineage, final adjacency) {
	for name := range graph {
		if _, ok := final[name]; !ok {
			delete(graph, name)
		}
	}
	for name, parents := range final {
		entry := graph[name]
		if entry == nil {
			entry = &Lineage{}
			graph[name] = entry
		}
		entry.Dataset = name
		entry.Parents = append([]string(nil), parents...)
		entry.Children = nil
	}
	for name, parents := range final {
		for _, parent := range parents {
			graph[parent].Children = append(graph[parent].Children, name)
		}
	}
	for name := range graph {
		graph[name].Children = uniqueSorted(graph[name].Children)
	}
}

// diffRelations returns the directed edges present in final but not original
// (added) and present in original but not final (removed). Both lists are
// sorted by upstream then downstream.
func diffRelations(original, final adjacency) (added, removed []Relation) {
	origEdges := edgeSet(original)
	finalEdges := edgeSet(final)
	for e := range finalEdges {
		if !origEdges[e] {
			added = append(added, e)
		}
	}
	for e := range origEdges {
		if !finalEdges[e] {
			removed = append(removed, e)
		}
	}
	sortRelations(added)
	sortRelations(removed)
	return added, removed
}

// edgeSet collapses the adjacency into a set of upstream->downstream edges.
func edgeSet(adj adjacency) map[Relation]bool {
	set := make(map[Relation]bool)
	for downstream, parents := range adj {
		for _, upstream := range parents {
			set[Relation{Upstream: upstream, Downstream: downstream}] = true
		}
	}
	return set
}

// sortRelations sorts by upstream then downstream.
func sortRelations(rels []Relation) {
	sort.Slice(rels, func(i, j int) bool {
		if rels[i].Upstream != rels[j].Upstream {
			return rels[i].Upstream < rels[j].Upstream
		}
		return rels[i].Downstream < rels[j].Downstream
	})
}

// affectedDownstreams computes the impact set: starting from the seed datasets
// (new, actually changed, or actually removed), follow downstream (children)
// edges in BOTH the original and the final graphs. Datasets that are
// themselves seeds are excluded from the output; every other reachable dataset
// is included once.
//
// Walking both graphs matters when relations are deleted: a dataset that was
// reachable downstream before the change (and thus loses a lineage path) is
// still affected even if the edge is gone in the final graph. Removed datasets
// are seeds too, so a downstream that lost its upstream node is counted.
func affectedDownstreams(original, final adjacency, newNames, changedNames, removedNames []string) []string {
	seeds := make(map[string]bool, len(newNames)+len(changedNames)+len(removedNames))
	for _, name := range newNames {
		seeds[name] = true
	}
	for _, name := range changedNames {
		seeds[name] = true
	}
	for _, name := range removedNames {
		seeds[name] = true
	}

	origChildren := invertAdjacency(original)
	finalChildren := invertAdjacency(final)

	seen := make(map[string]bool)
	queue := make([]string, 0, len(seeds))
	for seed := range seeds {
		queue = append(queue, seed)
	}
	for len(queue) > 0 {
		node := queue[0]
		queue = queue[1:]
		for _, child := range origChildren[node] {
			if !seen[child] {
				seen[child] = true
				queue = append(queue, child)
			}
		}
		for _, child := range finalChildren[node] {
			if !seen[child] {
				seen[child] = true
				queue = append(queue, child)
			}
		}
	}

	out := make([]string, 0, len(seen))
	for name := range seen {
		if !seeds[name] {
			out = append(out, name)
		}
	}
	sort.Strings(out)
	return out
}

// invertAdjacency maps each dataset to the datasets that depend on it (its
// children), with each child list sorted by name.
func invertAdjacency(adj adjacency) map[string][]string {
	children := make(map[string][]string, len(adj))
	for name, parents := range adj {
		for _, parent := range parents {
			children[parent] = append(children[parent], name)
		}
	}
	for name := range children {
		sort.Strings(children[name])
	}
	return children
}

// adjacencyToDatasets renders the final adjacency as sorted, JSON-ready
// records with non-nil upstream lists.
func adjacencyToDatasets(adj adjacency) []GraphDataset {
	names := make([]string, 0, len(adj))
	for name := range adj {
		names = append(names, name)
	}
	sort.Strings(names)
	ds := make([]GraphDataset, 0, len(names))
	for _, name := range names {
		parents := adj[name]
		if parents == nil {
			parents = []string{}
		}
		ds = append(ds, GraphDataset{Name: name, Upstreams: parents})
	}
	return ds
}

// stringSliceEqual reports whether a and b contain the same strings in the
// same order. Both are expected to be sorted and duplicate-free.
func stringSliceEqual(a, b []string) bool {
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

// MarshalGraphFile serializes the in-memory graph to the on-disk JSON format.
// The graph must satisfy every structure rule a reader enforces; nil nodes,
// empty names, names that are not valid UTF-8 (dataset names or direct upstream
// references), missing upstreams, and dependency cycles (including a dataset
// that lists itself) reject the export with no bytes returned, so a successful
// result can always be read back with UnmarshalGraphFile. The check is
// read-only: the in-memory graph is never modified.
func MarshalGraphFile(graph map[string]*Lineage) ([]byte, error) {
	adj, err := graphAdjacency(graph)
	if err != nil {
		return nil, err
	}
	if err := validateAcyclic(adj); err != nil {
		return nil, err
	}
	gf := GraphFile{Datasets: adjacencyToDatasets(adj)}
	return json.MarshalIndent(gf, "", "  ")
}

// UnmarshalGraphFile parses the on-disk graph JSON and rebuilds the in-memory
// graph (parents and derived children). The graph is checked with the same
// structural rules a snapshot enforces (empty names, duplicate datasets,
// missing upstreams, and cycles); a structurally invalid graph is rejected so
// it cannot be used as the basis for a batch. As in a snapshot, a known field
// declared twice within the same object — datasets in the graph object, name
// or upstreams in a dataset record — is ambiguous and rejects the whole file,
// even when the duplicates carry identical values.
func UnmarshalGraphFile(data []byte) (map[string]*Lineage, error) {
	if err := checkGraphFileDuplicateFields(data); err != nil {
		return nil, err
	}
	// Every name the graph carries must survive decoding exactly as written:
	// a raw literal with invalid UTF-8 bytes or an unpaired surrogate escape
	// would be silently rewritten to U+FFFD by json.Unmarshal, and a corrupted
	// upstream could then resolve to a genuinely different dataset (an edge
	// written as "源" plus a lone \uD800 would point at the real dataset
	// "源�"). See graph_name_encoding.go.
	if err := checkGraphNameEncoding(data); err != nil {
		return nil, err
	}
	var gf GraphFile
	if err := json.Unmarshal(data, &gf); err != nil {
		return nil, fmt.Errorf("invalid graph JSON: %w", err)
	}
	adj, err := validateGraphStructureFromFile(gf.Datasets)
	if err != nil {
		return nil, err
	}
	// Rebuild children from the parent edges so the in-memory graph is
	// consistent regardless of how the file was produced.
	return lineageFromAdjacency(adj), nil
}

// UnmarshalPlan parses the plan JSON. The plan structure (empty names,
// duplicate declarations) is validated later by computeBatch so that preview
// and apply share identical rejection behavior.
//
// A known field declared twice within the same object — changes or removals
// at the top level, name or upstreams inside one change record — is
// ambiguous: the reader would silently keep only the last declaration and
// drop the earlier adjustment. The whole plan is refused instead, even when
// the duplicates carry identical values, one is null, or the surviving value
// is a legal empty list. Field names are recognized the same way the decoder
// resolves them (after JSON unescaping, with Unicode case folding), so a
// second declaration spelled "Changes" or "changes" still collides;
// dataset names themselves stay case-sensitive. Unknown fields keep their
// ignore-everything behavior and may repeat freely.
//
// Every name that takes part in the adjustment — each change record's name,
// every upstreams entry, and every removals entry — must also survive
// decoding exactly as written: a raw string literal carrying bytes that are
// not valid UTF-8, or a \u escape forming an unpaired surrogate, would be
// silently rewritten to U+FFFD by the decoder, and the corrupted name could
// then collide with a genuinely different dataset (a removals entry meant
// for no one could delete the dataset really named "�"). The whole plan is
// refused instead, naming the field and the zero-based record or array
// position, even when the corrupted name would only have matched a dataset
// that does not exist. A genuine "�", correctly paired surrogate escapes,
// and names that merely look like escapes (a backslash before an ordinary
// letter) stay legal; unknown fields and their nested strings are not
// checked. See plan_name_encoding.go.
func UnmarshalPlan(data []byte) (Plan, error) {
	var p Plan
	if err := checkPlanDuplicateFields(data); err != nil {
		return p, err
	}
	if err := json.Unmarshal(data, &p); err != nil {
		return p, fmt.Errorf("invalid plan JSON: %w", err)
	}
	if err := checkPlanNameEncoding(data); err != nil {
		return Plan{}, err
	}
	return p, nil
}

// checkGraphFileDuplicateFields applies the shared repeated-known-field rule
// (see duplicate_fields.go) to a standalone graph file, supplying the graph
// file's own location: datasets may be declared only once in the top-level
// graph object, and name and upstreams only once in each dataset record. The
// scan is the same checkGraphObject ParseSnapshot runs on the embedded graph,
// so preview, apply, and snapshot can never disagree about a graph that
// repeats a known field.
func checkGraphFileDuplicateFields(data []byte) error {
	return runFieldScan(data, func(dec *json.Decoder) error {
		return checkGraphObject(dec, "at the top level of the graph")
	})
}

// checkPlanDuplicateFields applies the shared repeated-known-field rule (see
// duplicate_fields.go) to a plan, supplying the plan's own field scope and
// locations: changes or removals at the top level, and name or upstreams
// inside each change record. A change record carries the same fields as a
// dataset record, so both use the shared datasetRecordFields scope and a plan
// can never be judged differently from the other documents about what counts
// as a repeated field.
func checkPlanDuplicateFields(data []byte) error {
	return runFieldScan(data, func(dec *json.Decoder) error {
		return checkObjectFields(dec, "at the top level of the plan", []knownField{
			{name: "changes", nested: func(dec *json.Decoder) error {
				return checkArrayElements(dec, func(dec *json.Decoder, index int) error {
					return checkObjectFields(dec, fmt.Sprintf("in the change record at index %d of \"changes\"", index), datasetRecordFields)
				})
			}},
			{name: "removals"},
		})
	})
}
