// Command compare-upstream-lineage-example demonstrates the chainledger
// lineage library's upstream lineage comparison: given two lineage JSON
// documents (the lineage before and after a change) and a target dataset name,
// CompareUpstreamLineage reports which of the target's complete upstream
// dependencies were added and which removed.
//
// This is a library feature, not a command-line query: both documents and the
// target name are fixed in code. Run it from the repository root with:
//
//	go run ./examples/compare-upstream-lineage
package main

import (
	"fmt"
	"os"

	"github.com/asdhoaiqqq/chainledger-governance/chainledger"
)

func main() {
	// Scenario 1: s reaches t both directly and through the longer route
	// s -> m -> t. The change deletes only the longer route's last edge m -> t;
	// the direct s -> t relation survives. The comparison must still report
	// m -> t, and because that deletion detaches the whole m segment from t's
	// derivation, s -> m (which belonged to t's before scope) is removed too.
	before := `{
	  "nodes": ["s", "m", "t", "view", "lone"],
	  "edges": [
	    {"from": "s", "to": "t"},
	    {"from": "s", "to": "m"},
	    {"from": "m", "to": "t"},
	    {"from": "t", "to": "view"}
	  ]
	}`
	after := `{
	  "nodes": ["s", "m", "t", "view", "lone"],
	  "edges": [
	    {"from": "s", "to": "t"},
	    {"from": "s", "to": "m"},
	    {"from": "t", "to": "view"}
	  ]
	}`
	show("longer route truncated (s -> t direct survives, s -> m -> t dropped)", before, after, "t")

	// Scenario 2: t's direct upstreams do not change — it still derives from m
	// — but m's own source is swapped from old to new. The actual changed
	// dependencies show even though t's direct upstream list is identical.
	swappedBefore := `{"nodes":["old","new","m","t"],"edges":[` +
		`{"from":"m","to":"t"},{"from":"old","to":"m"}]}`
	swappedAfter := `{"nodes":["old","new","m","t"],"edges":[` +
		`{"from":"m","to":"t"},{"from":"new","to":"m"}]}`
	show("ancestor source swapped while t's direct upstreams stay m", swappedBefore, swappedAfter, "t")

	// No change is a success with two empty lists, never a failure.
	unchanged := `{"nodes":["a","t"],"edges":[{"from":"a","to":"t"}]}`
	show("nothing changed", unchanged, unchanged, "t")

	// Failures identify the cause and, for documents, which one was rejected.
	fail := func(label, beforeText, afterText, target string) {
		_, err := chainledger.CompareUpstreamLineage(beforeText, afterText, target)
		if err == nil {
			fmt.Fprintf(os.Stderr, "%s unexpectedly succeeded\n", label)
			os.Exit(1)
		}
		fmt.Printf("%s:\n  refused: %v\n\n", label, err)
	}
	good := unchanged
	cyclic := `{"nodes":["x"],"edges":[{"from":"x","to":"x"}]}`
	notThere := `{"nodes":["a"],"edges":[]}`
	fail("empty target name", good, good, "")
	fail("target missing from the before document", notThere, good, "t")
	fail("target missing from the after document", good, notThere, "t")
	fail("after document carries a self-dependency cycle", good, cyclic, "t")
}

func show(label, before, after, target string) {
	diff, err := chainledger.CompareUpstreamLineage(before, after, target)
	if err != nil {
		fmt.Fprintf(os.Stderr, "CompareUpstreamLineage(%q): %v\n", target, err)
		os.Exit(1)
	}
	fmt.Printf("%s:\n", label)
	fmt.Printf("  added   (%d):\n", len(diff.Added))
	for _, d := range diff.Added {
		fmt.Printf("    %s -> %s\n", d.From, d.To)
	}
	fmt.Printf("  removed (%d):\n", len(diff.Removed))
	for _, d := range diff.Removed {
		fmt.Printf("    %s -> %s\n", d.From, d.To)
	}
	fmt.Println()
}
