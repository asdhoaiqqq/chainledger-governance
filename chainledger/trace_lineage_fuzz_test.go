package chainledger

import (
	"fmt"
	"math/rand"
	"reflect"
	"sort"
	"testing"
)

// This file (1) cross-checks the compact parent-pointer trace against a
// deliberately simple reference walker that keeps one full path per reached
// node — the storage shape the refactor removed — over many random graphs
// with long chains and repeatedly converging branches, and (2) benchmarks the
// trace on those shapes so the linear storage stays observable.

// referenceTraceRootSources is the pre-refactor walker kept only as a test
// oracle: breadth-first with a complete path slice per reached node. It is
// simple and obviously correct, but stores O(nodes * path length) names, which
// is exactly the duplicated prefix storage traceRootSources no longer keeps.
func referenceTraceRootSources(dataset string, adj adjacency) []SourceTrace {
	best := map[string][]string{dataset: {dataset}}
	frontier := []string{dataset}
	for len(frontier) > 0 {
		candidates := map[string][]string{}
		for _, node := range frontier {
			for _, parent := range adj[node] {
				if _, seen := best[parent]; seen {
					continue
				}
				path := append(append([]string{}, best[node]...), parent)
				if current, ok := candidates[parent]; !ok || refPathLess(path, current) {
					candidates[parent] = path
				}
			}
		}
		next := make([]string, 0, len(candidates))
		for node, path := range candidates {
			best[node] = path
			next = append(next, node)
		}
		frontier = next
	}
	sources := []SourceTrace{}
	for node, path := range best {
		if len(adj[node]) == 0 {
			sources = append(sources, SourceTrace{Root: node, Path: path})
		}
	}
	sort.Slice(sources, func(i, j int) bool { return sources[i].Root < sources[j].Root })
	return sources
}

func refPathLess(a, b []string) bool {
	for i := 0; i < len(a) && i < len(b); i++ {
		if a[i] != b[i] {
			return a[i] < b[i]
		}
	}
	return len(a) < len(b)
}

// randomDAG builds a layered DAG with fixed seed: layers nodes in order, and a
// node's parents are drawn from earlier nodes with a chance that layers far
// apart connect, producing shortcuts; every non-root layer can also merge on
// the same parents, so branches converge repeatedly.
func randomDAG(rng *rand.Rand, nodes, maxBack int, edgeProb float64) adjacency {
	names := make([]string, nodes)
	for i := range names {
		names[i] = fmt.Sprintf("n%04d", i)
	}
	adj := make(adjacency, nodes)
	for i := 0; i < nodes; i++ {
		var parents []string
		lo := i - maxBack
		if lo < 0 {
			lo = 0
		}
		for j := lo; j < i; j++ {
			if rng.Float64() < edgeProb {
				parents = append(parents, names[j])
			}
		}
		if i > 0 && len(parents) == 0 {
			parents = []string{names[i-1]} // guarantee connectivity for n0..i
		}
		sort.Strings(parents)
		adj[names[i]] = parents
	}
	return adj
}

// TestTraceMatchesFullPathReferenceOnRandomDAGs queries every node of many
// randomized DAGs (including wide converging ones and long sparse chains) and
// pins the compact trace byte-for-byte to the full-path reference.
func TestTraceMatchesFullPathReferenceOnRandomDAGs(t *testing.T) {
	seeds := []int64{1, 2, 3, 42, 1009}
	configs := []struct {
		nodes, maxBack int
		edgeProb       float64
	}{
		{60, 4, 0.35},   // small, branchy, repeated convergence
		{120, 8, 0.2},   // wider reach, shortcuts
		{150, 2, 0.45},  // dense local diamonds
		{200, 30, 0.05}, // sparse long shortcuts over deep chains
		{80, 1, 0.9},    // near-single chain with occasional rails
	}
	for _, seed := range seeds {
		for ci, cfg := range configs {
			rng := rand.New(rand.NewSource(seed*1000 + int64(ci)))
			adj := randomDAG(rng, cfg.nodes, cfg.maxBack, cfg.edgeProb)
			for name := range adj {
				got := traceRootSources(name, adj)
				want := referenceTraceRootSources(name, adj)
				if !reflect.DeepEqual(got, want) {
					t.Fatalf("seed=%d cfg=%d query=%q:\n got %+v\nwant %+v",
						seed, ci, name, got, want)
				}
			}
		}
	}
}

// chainAdj builds the single chain n0 -> n1 -> ... -> n(nodes-1).
func chainAdj(nodes int) adjacency {
	adj := make(adjacency, nodes)
	for i := 0; i < nodes; i++ {
		name := fmt.Sprintf("n%d", i)
		if i+1 < nodes {
			adj[name] = []string{fmt.Sprintf("n%d", i+1)}
		} else {
			adj[name] = nil
		}
	}
	return adj
}

// ladderAdj builds levels two-wide split/merge layers ending at one root, the
// repeatedly-converging shape: each node links to both nodes next level.
func ladderAdj(levels int) adjacency {
	adj := adjacency{"T": {"a0", "b0"}}
	for i := 0; i < levels; i++ {
		a, b := fmt.Sprintf("a%d", i), fmt.Sprintf("b%d", i)
		if i+1 < levels {
			next := []string{fmt.Sprintf("a%d", i+1), fmt.Sprintf("b%d", i+1)}
			adj[a], adj[b] = next, next
		} else {
			adj[a], adj[b] = []string{"R"}, []string{"R"}
		}
	}
	adj["R"] = nil
	return adj
}

func BenchmarkTraceLongChain(b *testing.B) {
	adj := chainAdj(5000)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = traceRootSources("n0", adj)
	}
}

func BenchmarkReferenceTraceLongChain(b *testing.B) {
	adj := chainAdj(5000)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = referenceTraceRootSources("n0", adj)
	}
}

func BenchmarkTraceConvergingLadder(b *testing.B) {
	adj := ladderAdj(2500)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = traceRootSources("T", adj)
	}
}
