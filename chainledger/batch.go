// Package chainledger implements the on-chain data governance core.
package chainledger

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
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
// It rejects nil lineage nodes, empty names, and references to upstreams that
// are not registered; the name/reference rules are the shared ones from
// normalizeDatasets and validateAdjacencyReferences, also used by the file and
// snapshot readers. It does not check for cycles; use validateAcyclic for that.
func graphAdjacency(graph map[string]*Lineage) (adjacency, error) {
	if graph == nil {
		return nil, fmt.Errorf("%w: create the graph map with make before validating", ErrNotInitialized)
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
// empty names, missing upstreams, and cycles. It does not mutate the graph.
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
func PreviewBatch(graph map[string]*Lineage, plan Plan) (*BatchReport, error) {
	report, _, err := computeBatch(graph, plan)
	return report, err
}

// ApplyBatch validates the plan, applies it to graph, and returns the report.
// On rejection, graph is left completely unchanged.
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
// The graph must be valid; an invalid graph returns an error.
func MarshalGraphFile(graph map[string]*Lineage) ([]byte, error) {
	adj, err := graphAdjacency(graph)
	if err != nil {
		return nil, err
	}
	gf := GraphFile{Datasets: adjacencyToDatasets(adj)}
	return json.MarshalIndent(gf, "", "  ")
}

// checkGraphFileDuplicateFields applies the shared repeated-known-field rule
// to a standalone graph file: datasets may be declared only once in the
// top-level graph object, and name and upstreams only once in each dataset
// record. This keeps UnmarshalGraphFile in lockstep with ParseSnapshot, which
// runs the identical scan on the embedded graph, so preview, apply, and
// snapshot can never disagree about a graph file that repeats a known field.
func checkGraphFileDuplicateFields(data []byte) error {
	return scanForDuplicateFields(data, func(dec *json.Decoder) error {
		return checkGraphValue(dec, "at the top level of the graph")
	})
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
func UnmarshalPlan(data []byte) (Plan, error) {
	var p Plan
	if err := checkPlanDuplicateFields(data); err != nil {
		return p, err
	}
	if err := json.Unmarshal(data, &p); err != nil {
		return p, fmt.Errorf("invalid plan JSON: %w", err)
	}
	return p, nil
}

// checkPlanDuplicateFields rejects a plan in which a known field is declared
// more than once within the same object: changes or removals at the top
// level, and name or upstreams inside each change record. The scan itself —
// UseNumber decoding, case-folded key matching, abort-on-malformed — is the
// shared one from duplicate_fields.go, so a plan can never be judged
// differently from the other documents about what counts as a repeated field,
// and an oversized number buried in an ignored unknown field cannot hide a
// later duplicate. Documents not shaped like a plan are left to the regular
// parse.
func checkPlanDuplicateFields(data []byte) error {
	return scanForDuplicateFields(data, checkPlanTopLevel)
}

// checkPlanTopLevel scans the top-level plan object for repeated
// changes/removals declarations, descending into the changes array.
func checkPlanTopLevel(dec *json.Decoder) error {
	return checkObjectValue(dec, objectSpec{
		field:    planField,
		location: "at the top level of the plan",
		descend: map[string]func(dec *json.Decoder) error{
			"changes": checkChangeRecords,
		},
	})
}

// checkChangeRecords scans one changes array, checking every record that is
// an object for repeated name/upstreams declarations.
func checkChangeRecords(dec *json.Decoder) error {
	return checkRecordArrayValue(dec, objectSpec{field: datasetField}, func(index int) string {
		return fmt.Sprintf("in the change record at index %d of \"changes\"", index)
	})
}

// planField maps a top-level plan key to the canonical name of the plan field
// it selects, matching the decoder's case-insensitive field lookup.
func planField(key string) (string, bool) {
	switch {
	case strings.EqualFold(key, "changes"):
		return "changes", true
	case strings.EqualFold(key, "removals"):
		return "removals", true
	}
	return "", false
}
