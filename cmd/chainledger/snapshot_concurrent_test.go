//go:build unix

package main

// Cross-process regression coverage for the snapshot save rules.
//
// TestCLISnapshotConcurrentSameContent only starts goroutines inside one test
// process; flock(2) locks are tied to the open file description, so goroutines
// in one process share the lock and that test never exercises the independent
// -process path these rules promise (README "两个保存同时指向同一目标时…").
//
// The tests here run the real `snapshot` command as two separate processes
// (re-executed test binary, see TestMain) pointed at the same target, with
// genuinely overlapping execution. To make the overlap deterministic, a small
// "gate" helper process takes an exclusive flock on the SAME sibling lock file
// the production command uses (<target>.lock). Each snapshot process is
// confirmed to have reached and blocked on that lock (on Linux by watching
// /proc/<pid>/fd for the lock fd; on other unix hosts by a fixed settle delay)
// before the gate releases, so the two commands are always queued concurrently
// on the production lock while the target is still absent. The kernel then
// serializes the check-then-write sections exactly as it would for two commands
// a user launched by hand.

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/asdhoaiqqq/chainledger-governance/chainledger"
)

const (
	// reexecEnv marks a test-binary process that must act as a command helper
	// instead of running the test suite. Its value is always "1"; the helper
	// mode is argv[1].
	reexecEnv = "CHAINLEDGER_TEST_REEXEC"

	modeSnapshot = "snapshot-helper"
	modeGate     = "lock-gate"
)

// TestMain runs the suite normally, except in a re-executed helper process
// where it impersonates a standalone chainledger command. This lets the tests
// drive the exact production run() entry point in independent OS processes.
func TestMain(m *testing.M) {
	if os.Getenv(reexecEnv) == "1" {
		os.Exit(runTestHelper(os.Args[1:]))
	}
	os.Exit(m.Run())
}

// runTestHelper dispatches one re-executed helper process.
func runTestHelper(args []string) int {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "test helper: missing mode")
		return 2
	}
	switch args[0] {
	case modeSnapshot:
		// snapshot-helper <graph.json> <snapshot.json> — the unmodified
		// production snapshot command, in its own process.
		if len(args) != 3 {
			fmt.Fprintf(os.Stderr, "usage: %s <graph.json> <snapshot.json>\n", modeSnapshot)
			return 2
		}
		return run([]string{"snapshot", args[1], args[2]})
	case modeGate:
		// lock-gate <lockfile> <held-signal-dir> <release-signal-dir>
		if len(args) != 4 {
			fmt.Fprintf(os.Stderr, "usage: %s <lockfile> <held-dir> <release-dir>\n", modeGate)
			return 2
		}
		return runLockGate(args[1], args[2], args[3])
	default:
		fmt.Fprintf(os.Stderr, "test helper: unknown mode %q\n", args[0])
		return 2
	}
}

// runLockGate holds an exclusive flock on the production lock path until told
// to release. It creates heldDir once the lock is held (so the parent knows
// snapshot processes will block) and releases the lock as soon as releaseDir
// appears. It deliberately uses the same syscall and lock file as the
// production command, so queued helpers are contending on the real lock.
func runLockGate(lockPath, heldDir, releaseDir string) int {
	f, err := os.OpenFile(lockPath, os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		fmt.Fprintf(os.Stderr, "lock gate: open %q: %v\n", lockPath, err)
		return 1
	}
	defer f.Close()
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX); err != nil {
		fmt.Fprintf(os.Stderr, "lock gate: flock %q: %v\n", lockPath, err)
		return 1
	}
	if err := os.Mkdir(heldDir, 0o755); err != nil {
		fmt.Fprintf(os.Stderr, "lock gate: signal held: %v\n", err)
		return 1
	}
	for {
		if _, err := os.Stat(releaseDir); err == nil {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_UN); err != nil {
		fmt.Fprintf(os.Stderr, "lock gate: unlock: %v\n", err)
		return 1
	}
	return 0
}

// proc is one started helper process with its own captured streams.
type proc struct {
	cmd    *exec.Cmd
	out    bytes.Buffer
	errBuf bytes.Buffer
}

// helperCommand prepares a re-executed test binary in the given mode.
func helperCommand(t *testing.T, mode string, args ...string) *exec.Cmd {
	t.Helper()
	bin := os.Args[0]
	if !filepath.IsAbs(bin) {
		if abs, err := filepath.Abs(bin); err == nil {
			if _, statErr := os.Stat(abs); statErr == nil {
				bin = abs
			}
		}
	}
	cmd := exec.Command(bin, append([]string{mode}, args...)...)
	cmd.Env = append(os.Environ(), reexecEnv+"=1")
	return cmd
}

// startSnapshotProc launches an independent snapshot command process.
func startSnapshotProc(t *testing.T, graphPath, target string) *proc {
	t.Helper()
	p := &proc{}
	p.cmd = helperCommand(t, modeSnapshot, graphPath, target)
	p.cmd.Stdout = &p.out
	p.cmd.Stderr = &p.errBuf
	if err := p.cmd.Start(); err != nil {
		t.Fatalf("start snapshot helper: %v", err)
	}
	t.Cleanup(func() {
		_ = p.cmd.Process.Kill()
		_ = p.cmd.Wait()
	})
	return p
}

// waitResult waits for the process and returns its exit code and output. A
// non-zero exit is not a test failure here: callers assert on it.
func waitResult(t *testing.T, p *proc) procResult {
	t.Helper()
	if err := p.cmd.Wait(); err != nil {
		if _, ok := err.(*exec.ExitError); !ok {
			t.Fatalf("wait snapshot helper: %v", err)
		}
	}
	state := p.cmd.ProcessState
	if state == nil {
		t.Fatalf("snapshot helper has no process state")
	}
	return procResult{
		exit:   state.ExitCode(),
		stdout: p.out.String(),
		stderr: p.errBuf.String(),
	}
}

// procResult is the observable outcome of one command process.
type procResult struct {
	exit   int
	stdout string
	stderr string
}

// gate is the external holder of the production lock file.
type gate struct {
	cmd        *exec.Cmd
	releaseDir string
	errBuf     bytes.Buffer
}

// holdLockGate starts a gate that holds lockPath exclusively until released.
// It returns only after the lock is provably held.
func holdLockGate(t *testing.T, dir, lockPath string) *gate {
	t.Helper()
	heldDir := filepath.Join(dir, "gate-held")
	releaseDir := filepath.Join(dir, "gate-release")
	g := &gate{releaseDir: releaseDir}
	g.cmd = helperCommand(t, modeGate, lockPath, heldDir, releaseDir)
	g.cmd.Stdout = nil
	g.cmd.Stderr = &g.errBuf
	if err := g.cmd.Start(); err != nil {
		t.Fatalf("start lock gate: %v", err)
	}
	t.Cleanup(func() {
		_ = g.cmd.Process.Kill()
		_ = g.cmd.Wait()
	})
	waitForSignal(t, heldDir, "gate to acquire the lock")
	return g
}

// release unlocks and reaps the gate.
func (g *gate) release(t *testing.T) {
	t.Helper()
	if err := os.Mkdir(g.releaseDir, 0o755); err != nil {
		t.Fatalf("signal gate release: %v", err)
	}
	if err := g.cmd.Wait(); err != nil {
		t.Fatalf("lock gate exited with error: %v (stderr: %s)", err, g.errBuf.String())
	}
}

// waitForSignal blocks until dir exists or the deadline passes.
func waitForSignal(t *testing.T, dir, what string) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		if _, err := os.Stat(dir); err == nil {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s (%q)", what, dir)
		}
		time.Sleep(2 * time.Millisecond)
	}
}

// processGone reports whether the helper has already exited — including as an
// unreaped zombie — WITHOUT consuming it through Wait, so the caller can still
// collect its stdout/stderr afterward. On Linux a zombie's /proc/<pid>/fd is
// unreadable, so state is taken from /proc/<pid>/stat instead.
func processGone(p *proc) bool {
	if p.cmd.ProcessState != nil {
		return true
	}
	if runtime.GOOS == "linux" {
		data, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", p.cmd.Process.Pid))
		if err != nil {
			return false
		}
		// Field 3 (run state) follows the parenthesised comm field.
		if i := bytes.LastIndexByte(data, ')'); i >= 0 && i+2 < len(data) {
			switch data[i+2] {
			case 'Z', 'X': // zombie, dead
				return true
			}
		}
		return false
	}
	return p.cmd.Process.Signal(syscall.Signal(0)) != nil
}

// hasLockFd reports whether p currently has lockPath open, observed through
// /proc. A zombie's fd directory is not readable, which reads as "not open".
func hasLockFd(t *testing.T, p *proc, lockPath string) bool {
	t.Helper()
	fdDir := fmt.Sprintf("/proc/%d/fd", p.cmd.Process.Pid)
	fds, err := os.ReadDir(fdDir)
	if err != nil {
		return false
	}
	for _, fd := range fds {
		dest, err := os.Readlink(filepath.Join(fdDir, fd.Name()))
		if err == nil && dest == lockPath {
			return true
		}
	}
	return false
}

// waitUntilQueuedOnLock blocks until p is queued on the production lock while
// the gate still holds it, i.e. the helper has opened the lock file and is
// parked inside acquireLock (flock LOCK_EX) and therefore has not yet inspected
// the target. "Opened the lock file" alone is not enough: the fd must stay
// open while the process stays alive for a short settle window, so a build in
// which flock does not actually block is detected here (the helper would
// finish, close the fd, and turn into a zombie) rather than mistaken for a
// genuine overlap.
func waitUntilQueuedOnLock(t *testing.T, p *proc, lockPath string) {
	t.Helper()
	if runtime.GOOS != "linux" {
		// No /proc on other unix hosts: give the helper time to reach flock,
		// then require it to still be running while the gate holds the lock.
		time.Sleep(300 * time.Millisecond)
		if processGone(p) {
			t.Fatalf("snapshot helper finished before the gate released the lock "+
				"(stderr: %q)", p.errBuf.String())
		}
		return
	}
	deadline := time.Now().Add(10 * time.Second)
	openSince := time.Time{}
	for {
		if processGone(p) {
			t.Fatalf("snapshot helper did not block on the production lock while "+
				"the gate held it, so cross-process saves are not serialized "+
				"(stderr: %q)", p.errBuf.String())
		}
		if hasLockFd(t, p, lockPath) {
			if openSince.IsZero() {
				openSince = time.Now()
			}
			if time.Since(openSince) >= 25*time.Millisecond {
				return
			}
		} else {
			openSince = time.Time{}
			if time.Now().After(deadline) {
				t.Fatalf("snapshot helper never reached lock %q (stderr: %q)",
					lockPath, p.errBuf.String())
			}
		}
		time.Sleep(2 * time.Millisecond)
	}
}

// runConcurrentSaves launches two independent snapshot processes against one
// absent target while a gate holds the production lock, confirms both are
// queued on that lock, and then releases. The two submissions therefore
// overlap in time and are serialized only by the production locking. Inputs
// are written first and their original bytes are returned so callers can prove
// the saves never modify source graphs.
func runConcurrentSaves(t *testing.T, dir, graphJSON1, graphJSON2 string) (procResult, procResult, string, string, string) {
	t.Helper()
	graphPath1 := filepath.Join(dir, "graph-1.json")
	graphPath2 := filepath.Join(dir, "graph-2.json")
	if err := os.WriteFile(graphPath1, []byte(graphJSON1), 0o644); err != nil {
		t.Fatalf("write graph 1: %v", err)
	}
	if err := os.WriteFile(graphPath2, []byte(graphJSON2), 0o644); err != nil {
		t.Fatalf("write graph 2: %v", err)
	}
	target := filepath.Join(dir, "snap.json")
	lockPath := target + ".lock"

	gate := holdLockGate(t, dir, lockPath)
	p1 := startSnapshotProc(t, graphPath1, target)
	waitUntilQueuedOnLock(t, p1, lockPath)
	p2 := startSnapshotProc(t, graphPath2, target)
	waitUntilQueuedOnLock(t, p2, lockPath)
	gate.release(t)

	r1 := waitResult(t, p1)
	r2 := waitResult(t, p2)
	return r1, r2, graphPath1, graphPath2, target
}

// expectedSnapshot builds the content id and exact on-disk bytes a save of
// graphJSON must produce, using the public package API rather than the saved
// file, so expectations are independent of the result under test.
func expectedSnapshot(t *testing.T, graphJSON string) (contentID string, data []byte) {
	t.Helper()
	graph, err := chainledger.UnmarshalGraphFile([]byte(graphJSON))
	if err != nil {
		t.Fatalf("unmarshal expected graph: %v", err)
	}
	snap, err := chainledger.BuildSnapshot(graph)
	if err != nil {
		t.Fatalf("build expected snapshot: %v", err)
	}
	data, err = chainledger.MarshalSnapshot(snap)
	if err != nil {
		t.Fatalf("marshal expected snapshot: %v", err)
	}
	return snap.ContentID, data
}

// assertNoLeftoverTempFiles fails if an atomic-write temp file was abandoned
// in dir: a refused or committed save must never leave half a snapshot beside
// the target.
func assertNoLeftoverTempFiles(t *testing.T, dir string) {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read dir: %v", err)
	}
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), ".chainledger-") {
			t.Errorf("abandoned temp file left beside target: %q", entry.Name())
		}
	}
}

// graphParents maps dataset name to its direct upstreams as stored in snap.
func graphParents(snap *chainledger.SnapshotFile) map[string][]string {
	parents := make(map[string][]string, len(snap.Graph.Datasets))
	for _, ds := range snap.Graph.Datasets {
		parents[ds.Name] = ds.Upstreams
	}
	return parents
}

// Two legal graphs that are semantically different: the same derived dataset
// depends on a different root source in each. Every name in one version except
// "derived" is absent from the other, so a mixed or swapped result is easy to
// detect.
const (
	graphRootX = `{"datasets":[{"name":"rootX","upstreams":[]},{"name":"derived","upstreams":["rootX"]}]}`
	graphRootY = `{"datasets":[{"name":"rootY","upstreams":[]},{"name":"derived","upstreams":["rootY"]}]}`
)

// TestCrossProcessDifferentContentExactlyOneWins covers the headline rule:
// two independent processes save semantically different graphs to one absent
// target with overlapping execution — exactly one commits and one is refused,
// regardless of which input is queued first. The winner's stdout identifies
// the content actually on disk; the loser's stdout is empty and its stderr
// explains that the target already holds different content, naming the path.
// The final file is a complete, readable snapshot of exactly the winner's
// graph, and neither source graph is touched.
func TestCrossProcessDifferentContentExactlyOneWins(t *testing.T) {
	idX, bytesX := expectedSnapshot(t, graphRootX)
	idY, bytesY := expectedSnapshot(t, graphRootY)
	if idX == idY {
		t.Fatal("test inputs must have different content identifiers")
	}

	// Run with each input queued first, repeated to guard against scheduling
	// flukes. The assertions never assume which side wins.
	for _, tc := range []struct {
		name   string
		first  string
		second string
	}{
		{"rootX input queued first", graphRootX, graphRootY},
		{"rootY input queued first", graphRootY, graphRootX},
	} {
		for iteration := 0; iteration < 3; iteration++ {
			t.Run(fmt.Sprintf("%s/iteration-%d", tc.name, iteration+1), func(t *testing.T) {
				dir := t.TempDir()
				r1, r2, graphPath1, graphPath2, target := runConcurrentSaves(t, dir, tc.first, tc.second)

				winners := 0
				var winner procResult
				for _, r := range []procResult{r1, r2} {
					if r.exit == 0 {
						winners++
						winner = r
					}
				}
				if winners != 1 {
					t.Fatalf("exactly one save must succeed, got %d: r1=%+v r2=%+v", winners, r1, r2)
				}
				loser := r1
				if winner == r1 {
					loser = r2
				}

				// Identify the winner from the outcomes, then require every
				// artifact to correspond to that same graph.
				wantID, wantBytes, wantRoot := idY, bytesY, "rootY"
				otherRoot := "rootX"
				if strings.TrimSpace(winner.stdout) == idX {
					wantID, wantBytes, wantRoot, otherRoot = idX, bytesX, "rootX", "rootY"
				}

				// Winner: zero exit, stdout carries only the winning content id.
				if winner.exit != 0 {
					t.Fatalf("winner exit = %d, stderr = %q", winner.exit, winner.stderr)
				}
				if winner.stdout != wantID+"\n" {
					t.Errorf("winner stdout = %q, want exactly %q", winner.stdout, wantID)
				}
				if winner.stderr != "" {
					t.Errorf("winner stderr = %q, want empty", winner.stderr)
				}

				// Loser: non-zero exit, empty stdout, stderr names the target
				// and the fact that it already holds different content.
				if loser.exit == 0 {
					t.Errorf("loser exit = 0, want non-zero")
				}
				if loser.stdout != "" {
					t.Errorf("loser stdout = %q, want empty", loser.stdout)
				}
				if !strings.Contains(loser.stderr, target) {
					t.Errorf("loser stderr = %q, must name target %q", loser.stderr, target)
				}
				if !strings.Contains(loser.stderr, "already holds a different snapshot") {
					t.Errorf("loser stderr = %q, must explain the target holds different content", loser.stderr)
				}

				// Final file: parses under the existing rules and is byte for
				// byte the winner's snapshot — no mix of the other version.
				final, err := os.ReadFile(target)
				if err != nil {
					t.Fatalf("read final snapshot: %v", err)
				}
				snap, err := chainledger.ParseSnapshot(final)
				if err != nil {
					t.Fatalf("final snapshot is not readable: %v", err)
				}
				if snap.ContentID != wantID {
					t.Errorf("final content id = %q, want winner id %q", snap.ContentID, wantID)
				}
				if !bytes.Equal(final, wantBytes) {
					t.Errorf("final snapshot bytes do not match the winning graph")
				}
				parents := graphParents(snap)
				gotDerived := parents["derived"]
				if len(gotDerived) != 1 || gotDerived[0] != wantRoot {
					t.Errorf("derived upstreams = %v, want [%s]", gotDerived, wantRoot)
				}
				if _, ok := parents[wantRoot]; !ok {
					t.Errorf("winning root %q missing from final graph", wantRoot)
				}
				if _, ok := parents[otherRoot]; ok {
					t.Errorf("losing root %q leaked into the final graph", otherRoot)
				}

				// Source graphs keep their original bytes; no temp debris.
				if got := readFile(t, graphPath1); got != tc.first {
					t.Errorf("source graph 1 was modified")
				}
				if got := readFile(t, graphPath2); got != tc.second {
					t.Errorf("source graph 2 was modified")
				}
				assertNoLeftoverTempFiles(t, dir)

				t.Logf("winner content id: %s (queued-first input: %s)", wantID,
					map[bool]string{true: "yes", false: "no"}[winner == r1])
			})
		}
	}
}

// Semantically identical inputs dressed up differently: record order, upstream
// order, duplicate upstreams, and JSON whitespace must all collapse to one
// content id. Two overlapping saves of such a pair both succeed, report the
// same id, and the newly created snapshot still follows the existing sort and
// dedupe rules.
var sameContentVariantPairs = []struct {
	name string
	a    string
	b    string
}{
	{
		name: "record order, duplicate upstream, whitespace",
		a:    abcGraph,
		b:    `{ "datasets": [ {"upstreams":["B","B"],"name":"C"}, {"upstreams":[],"name":"A"}, {"name":"B","upstreams":["A"]} ] }`,
	},
	{
		name: "upstream order, duplicate, padded whitespace",
		a:    `{"datasets":[{"name":"A","upstreams":[]},{"name":"B","upstreams":[]},{"name":"C","upstreams":["B","A"]}]}`,
		b:    `{ "datasets" : [ {"name":"C","upstreams":["A","B","A"]}, {"name":"B","upstreams":[]}, {"name":"A","upstreams":[]} ] }`,
	},
}

func TestCrossProcessSameContentBothSucceed(t *testing.T) {
	for _, tc := range sameContentVariantPairs {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			wantID, wantBytes := expectedSnapshot(t, tc.a)
			if otherID, _ := expectedSnapshot(t, tc.b); otherID != wantID {
				t.Fatalf("variants are not semantically equal: %q vs %q", wantID, otherID)
			}

			r1, r2, graphPath1, graphPath2, target := runConcurrentSaves(t, dir, tc.a, tc.b)

			for label, r := range map[string]procResult{"first": r1, "second": r2} {
				if r.exit != 0 {
					t.Errorf("%s save exit = %d, stderr = %q", label, r.exit, r.stderr)
					continue
				}
				if r.stdout != wantID+"\n" {
					t.Errorf("%s save stdout = %q, want exactly %q", label, r.stdout, wantID)
				}
				if r.stderr != "" {
					t.Errorf("%s save stderr = %q, want empty", label, r.stderr)
				}
			}

			final, err := os.ReadFile(target)
			if err != nil {
				t.Fatalf("read final snapshot: %v", err)
			}
			snap, err := chainledger.ParseSnapshot(final)
			if err != nil {
				t.Fatalf("final snapshot is not readable: %v", err)
			}
			if !bytes.Equal(final, wantBytes) {
				t.Errorf("new snapshot bytes do not follow canonical sort/dedupe output")
			}
			// Explicit order/dedupe checks on the normalized graph.
			for i := 1; i < len(snap.Graph.Datasets); i++ {
				if snap.Graph.Datasets[i-1].Name >= snap.Graph.Datasets[i].Name {
					t.Errorf("datasets not sorted by name: %q before %q",
						snap.Graph.Datasets[i-1].Name, snap.Graph.Datasets[i].Name)
				}
			}
			for _, ds := range snap.Graph.Datasets {
				for i := 1; i < len(ds.Upstreams); i++ {
					if ds.Upstreams[i-1] >= ds.Upstreams[i] {
						t.Errorf("upstreams of %q not sorted and deduplicated: %v", ds.Name, ds.Upstreams)
					}
				}
			}

			if got := readFile(t, graphPath1); got != tc.a {
				t.Errorf("source graph 1 was modified")
			}
			if got := readFile(t, graphPath2); got != tc.b {
				t.Errorf("source graph 2 was modified")
			}
			assertNoLeftoverTempFiles(t, dir)
		})
	}
}

// TestCrossProcessSameContentPreexistingTargetUntouched: when a valid snapshot
// of the same content is already in place before the overlapping submissions,
// both saves succeed and the target's bytes and mtime are preserved exactly
// (the idempotent rule holds cross-process under contention).
func TestCrossProcessSameContentPreexistingTargetUntouched(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "snap.json")

	// Seed the target with one real, independent save.
	seedGraphPath := filepath.Join(dir, "seed-graph.json")
	if err := os.WriteFile(seedGraphPath, []byte(abcGraph), 0o644); err != nil {
		t.Fatalf("write seed graph: %v", err)
	}
	seed := startSnapshotProc(t, seedGraphPath, target)
	seedResult := waitResult(t, seed)
	if seedResult.exit != 0 {
		t.Fatalf("seed save exit = %d, stderr = %q", seedResult.exit, seedResult.stderr)
	}
	wantID, _ := expectedSnapshot(t, abcGraph)

	// Pin mtime far in the past so any rewrite is detectable even on
	// filesystems with coarse timestamp granularity.
	originalBytes, err := os.ReadFile(target)
	if err != nil {
		t.Fatalf("read seed snapshot: %v", err)
	}
	pinned := time.Date(2020, time.January, 2, 3, 4, 5, 0, time.UTC)
	if err := os.Chtimes(target, pinned, pinned); err != nil {
		t.Fatalf("pin mtime: %v", err)
	}
	infoBefore, err := os.Stat(target)
	if err != nil {
		t.Fatalf("stat before: %v", err)
	}

	// Two more semantically-equal but textually different submissions,
	// overlapping on the production lock.
	variant1 := `{ "datasets": [ {"upstreams":["B","B"],"name":"C"}, {"upstreams":[],"name":"A"}, {"name":"B","upstreams":["A"]} ] }`
	variant2 := `{"datasets":[{"name":"C","upstreams":["B"]},{"name":"A","upstreams":[]},{"name":"B","upstreams":["A"]}]}`
	r1, r2, graphPath1, graphPath2, _ := runConcurrentSaves(t, dir, variant1, variant2)

	for label, r := range map[string]procResult{"first": r1, "second": r2} {
		if r.exit != 0 {
			t.Errorf("%s save exit = %d, stderr = %q", label, r.exit, r.stderr)
			continue
		}
		if r.stdout != wantID+"\n" {
			t.Errorf("%s save stdout = %q, want %q", label, r.stdout, wantID)
		}
		if r.stderr != "" {
			t.Errorf("%s save stderr = %q, want empty", label, r.stderr)
		}
	}

	gotBytes, err := os.ReadFile(target)
	if err != nil {
		t.Fatalf("read target after: %v", err)
	}
	if !bytes.Equal(gotBytes, originalBytes) {
		t.Errorf("pre-existing same-content snapshot was rewritten")
	}
	infoAfter, err := os.Stat(target)
	if err != nil {
		t.Fatalf("stat after: %v", err)
	}
	if !infoAfter.ModTime().Equal(infoBefore.ModTime()) {
		t.Errorf("pre-existing snapshot mtime changed: %s -> %s", infoBefore.ModTime(), infoAfter.ModTime())
	}

	// The untouched target still reads as a complete valid snapshot.
	if _, err := chainledger.ParseSnapshot(gotBytes); err != nil {
		t.Errorf("pre-existing snapshot no longer readable: %v", err)
	}

	if got := readFile(t, graphPath1); got != variant1 {
		t.Errorf("source graph 1 was modified")
	}
	if got := readFile(t, graphPath2); got != variant2 {
		t.Errorf("source graph 2 was modified")
	}
	assertNoLeftoverTempFiles(t, dir)
}
