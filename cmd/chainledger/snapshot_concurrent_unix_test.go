//go:build unix

package main

// Cross-process regression coverage for concurrent `snapshot` saves.
//
// The in-process test TestCLISnapshotConcurrentSameContent only proves that
// goroutines inside one command process serialize. These tests instead launch
// two INDEPENDENT command binaries that submit to the SAME snapshot target with
// overlapping execution:
//
//   - an external helper process first takes the sibling flock the snapshot
//     command uses, so neither command can enter its check-then-write section;
//   - both commands are started and confirmed (via /proc/locks) blocked on that
//     very lock before the helper is killed and the kernel hands the lock to
//     one of them;
//   - the two saves are therefore simultaneous submissions that race the lock,
//     never two saves run one after the other, and no winner is preselected.
//
// Everything asserted here is observable user behavior: per-command exit code,
// exact stdout/stderr, and the final snapshot file. No chain network is used.

import (
	"bufio"
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/asdhoaiqqq/chainledger-governance/chainledger"
)

// lockHelperMode is the hidden argument under which the test binary itself
// acts as an external lock holder on the snapshot target's sibling lock file.
const lockHelperMode = "--chainledger-test-lock-helper"

// lockHelperTTL bounds a helper whose parent died before killing it; a normal
// test always kills it explicitly far sooner.
const lockHelperTTL = 60 * time.Second

// snapshotCLIBin is the freshly built real command binary, shared by every
// test so `go build` runs once per `go test` invocation.
var snapshotCLIBin string

// TestMain builds the command binary once and also implements the external
// lock-holder helper mode (the test binary re-enters here as a subprocess).
func TestMain(m *testing.M) {
	if len(os.Args) >= 2 {
		switch os.Args[1] {
		case lockHelperMode:
			os.Exit(runLockHelper(os.Args[2:]))
		}
		// Platform-specific extra helper modes (e.g. the Linux mount-namespace
		// write-fault helper). On platforms without such a mode this is a no-op.
		if code, handled := dispatchExtraTestHelperMode(os.Args[1], os.Args[2:]); handled {
			os.Exit(code)
		}
	}

	dir, err := os.MkdirTemp("", "chainledger-snapshot-bin-")
	if err != nil {
		fmt.Fprintln(os.Stderr, "cannot make temp build dir:", err)
		os.Exit(1)
	}
	snapshotCLIBin = filepath.Join(dir, "chainledger")
	build := exec.Command("go", "build", "-o", snapshotCLIBin, ".")
	build.Stderr = os.Stderr
	if err := build.Run(); err != nil {
		fmt.Fprintln(os.Stderr, "cannot build chainledger test binary:", err)
		os.RemoveAll(dir)
		os.Exit(1)
	}

	code := m.Run()
	os.RemoveAll(dir)
	os.Exit(code)
}

// runLockHelper takes an exclusive kernel flock on the very sibling lock file
// the snapshot command uses. It deliberately performs the flock with a raw
// syscall rather than reusing the product's acquireLock: the gate is test
// infrastructure and must keep working even when a deliberate fault disables
// locking inside the command under test. A flock on the same file inode is
// kernel-level and code-agnostic, so this still blocks the commands' own
// acquireLock calls. It prints "locked" once the gate is closed and holds the
// lock until killed.
func runLockHelper(args []string) int {
	if len(args) != 1 {
		fmt.Fprintln(os.Stderr, "usage: test-binary", lockHelperMode, "<lock-file>")
		return 2
	}
	f, err := os.OpenFile(args[0], os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		fmt.Fprintln(os.Stderr, "helper cannot open lock:", err)
		return 1
	}
	defer f.Close()
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX); err != nil {
		fmt.Fprintln(os.Stderr, "helper cannot flock:", err)
		return 1
	}
	defer syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
	// os.Stdout is an unbuffered os.File, so Println reaches the pipe
	// immediately; do NOT call os.Stdout.Sync() — fsync on a pipe fails with
	// EINVAL and must not abort the helper before it has held the lock.
	fmt.Println("locked")
	time.Sleep(lockHelperTTL)
	return 0
}

// lockGate is an external process holding the snapshot target's lock. Releasing
// the gate (killing the holder) lets the kernel grant the lock to exactly one
// of the waiting commands.
type lockGate struct {
	cmd *exec.Cmd
}

// holdLockGate starts the helper and blocks until it has confirmed (by
// printing "locked") that it holds the lock, so any snapshot command started
// afterwards is guaranteed to block.
func holdLockGate(t *testing.T, lockPath string) *lockGate {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Fatalf("locate test binary: %v", err)
	}
	cmd := exec.Command(exe, lockHelperMode, lockPath)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatalf("helper stdout pipe: %v", err)
	}
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		t.Fatalf("start lock helper: %v", err)
	}
	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
	})

	ready := make(chan bool, 1)
	go func() {
		sc := bufio.NewScanner(stdout)
		ready <- sc.Scan() && sc.Text() == "locked"
	}()
	select {
	case ok := <-ready:
		if !ok {
			t.Fatal("lock helper exited without reporting the lock")
		}
	case <-time.After(10 * time.Second):
		t.Fatal("lock helper never acquired the lock")
	}
	return &lockGate{cmd: cmd}
}

// release kills the holder; its process death releases the flock atomically.
func (g *lockGate) release() {
	_ = g.cmd.Process.Kill()
	_ = g.cmd.Wait()
}

// blockedWaiterPIDs returns the PIDs of processes with an FLOCK request queued
// (the "/proc/locks" lines starting with "->") on the given lock file's inode.
// The holder's own granted lock has no "->" prefix and is excluded.
func blockedWaiterPIDs(t *testing.T, lockPath string) map[int]bool {
	t.Helper()
	fi, err := os.Stat(lockPath)
	if err != nil {
		t.Fatalf("stat lock file: %v", err)
	}
	st, ok := fi.Sys().(*syscall.Stat_t)
	if !ok {
		t.Fatalf("lock file stat is not a unix stat_t: %T", fi.Sys())
	}
	inodeSuffix := ":" + strconv.FormatUint(st.Ino, 10)

	data, err := os.ReadFile("/proc/locks")
	if err != nil {
		t.Fatalf("read /proc/locks: %v", err)
	}
	pids := make(map[int]bool)
	for _, line := range strings.Split(string(data), "\n") {
		fields := strings.Fields(line)
		// A blocked waiter looks like:
		// "772: -> FLOCK ADVISORY WRITE 1234 103:02:2887301 0 EOF"
		// while the holder's granted line has no "->". Locate the arrow and
		// the FLOCK record that follows it.
		arrow := -1
		for i, f := range fields {
			if f == "->" {
				arrow = i
				break
			}
		}
		if arrow < 0 {
			continue
		}
		flockIdx := -1
		for i := arrow + 1; i < len(fields); i++ {
			if fields[i] == "FLOCK" {
				flockIdx = i
				break
			}
		}
		if flockIdx < 0 || flockIdx+4 >= len(fields) {
			continue
		}
		// Layout after FLOCK: ADVISORY WRITE <pid> <dev>:<inode> 0 EOF
		pidField, idField := fields[flockIdx+3], fields[flockIdx+4]
		// Match the lock inode; the waiter PID is in any case one of our
		// command processes, which never flock any other file.
		if !strings.HasSuffix(idField, inodeSuffix) {
			continue
		}
		if pid, err := strconv.Atoi(pidField); err == nil {
			pids[pid] = true
		}
	}
	return pids
}

// waitUntilAllBlocked polls until every expected PID is queued waiting on the
// lock file. It proves both commands are simultaneously parked at the start of
// their serialized section before the test lets the race resolve.
func waitUntilAllBlocked(t *testing.T, lockPath string, want map[int]bool) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		got := blockedWaiterPIDs(t, lockPath)
		missing := false
		for pid := range want {
			if !got[pid] {
				missing = true
			}
		}
		if !missing {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for commands %v to block on %q; blocked waiters: %v",
		want, lockPath, blockedWaiterPIDs(t, lockPath))
}

// procResult is everything observable about one finished command process.
type procResult struct {
	label       string
	stdout      string
	stderr      string
	exitCode    int
	sourceGraph string // path of the graph the command read
}

// waitCommandProcess waits for cmd to finish, failing the test if it hangs.
func waitCommandProcess(t *testing.T, cmd *exec.Cmd) {
	t.Helper()
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case <-done:
	case <-time.After(30 * time.Second):
		_ = cmd.Process.Kill()
		t.Fatalf("snapshot command %d did not finish after the lock was released", cmd.Process.Pid)
	}
}

// launchConcurrentSnapshotPair runs two independent snapshot command binaries
// against the same target with genuinely overlapping execution. Both are
// blocked together on the external gate before either can inspect the target;
// the gate is then released and the kernel decides the order. The returned
// results are bound to firstGraph / secondGraph in launch order.
func launchConcurrentSnapshotPair(t *testing.T, target, firstGraph, secondGraph string) (procResult, procResult) {
	t.Helper()
	// The barrier is proven, not guessed: both processes must be observed
	// queued on the lock. That observation uses Linux's /proc/locks, which is
	// absent on other unix kernels; skip there instead of timing out.
	if _, err := os.Stat("/proc/locks"); err != nil {
		t.Skipf("cross-process lock barrier needs Linux /proc/locks: %v", err)
	}
	lockPath := target + ".lock"
	gate := holdLockGate(t, lockPath)

	var out1, err1, out2, err2 bytes.Buffer
	cmd1 := exec.Command(snapshotCLIBin, "snapshot", firstGraph, target)
	cmd1.Stdout, cmd1.Stderr = &out1, &err1
	cmd2 := exec.Command(snapshotCLIBin, "snapshot", secondGraph, target)
	cmd2.Stdout, cmd2.Stderr = &out2, &err2

	if err := cmd1.Start(); err != nil {
		t.Fatalf("start first snapshot command: %v", err)
	}
	if err := cmd2.Start(); err != nil {
		t.Fatalf("start second snapshot command: %v", err)
	}
	waitUntilAllBlocked(t, lockPath, map[int]bool{
		cmd1.Process.Pid: true,
		cmd2.Process.Pid: true,
	})

	gate.release()
	waitCommandProcess(t, cmd1)
	waitCommandProcess(t, cmd2)

	return procResult{
			label:       "first",
			stdout:      out1.String(),
			stderr:      err1.String(),
			exitCode:    cmd1.ProcessState.ExitCode(),
			sourceGraph: firstGraph,
		}, procResult{
			label:       "second",
			stdout:      out2.String(),
			stderr:      err2.String(),
			exitCode:    cmd2.ProcessState.ExitCode(),
			sourceGraph: secondGraph,
		}
}

// mustSnapshotFromGraph builds the expected snapshot for a graph JSON string
// the same way the command does, so the test can compare content id and the
// normalized graph without hardcoding hashes.
func mustSnapshotFromGraph(t *testing.T, graphJSON string) *chainledger.SnapshotFile {
	t.Helper()
	graph, err := chainledger.UnmarshalGraphFile([]byte(graphJSON))
	if err != nil {
		t.Fatalf("invalid reference graph: %v", err)
	}
	snap, err := chainledger.BuildSnapshot(graph)
	if err != nil {
		t.Fatalf("build reference snapshot: %v", err)
	}
	return snap
}

// rootNames traces dataset in snap and returns the sorted root sources the
// stored lineage actually reaches.
func rootNames(t *testing.T, snap *chainledger.SnapshotFile, dataset string) []string {
	t.Helper()
	report, err := chainledger.TraceSources(snap, dataset)
	if err != nil {
		t.Fatalf("trace %q: %v", dataset, err)
	}
	roots := make([]string, 0, len(report.Sources))
	for _, src := range report.Sources {
		roots = append(roots, src.Root)
	}
	return roots
}

// assertNoLeftoverTempFiles fails if the atomic-write temp pattern left any
// half snapshot behind in the target directory.
func assertNoLeftoverTempFiles(t *testing.T, dir string) {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read target dir: %v", err)
	}
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), ".chainledger-") {
			t.Errorf("rejected/failed save left a temporary file %q", filepath.Join(dir, entry.Name()))
		}
	}
}

// writeRoundInput writes one source graph file and returns its path and exact
// bytes, so the test can later prove the source graph was never modified.
func writeRoundInput(t *testing.T, dir, name, content string) (path string, raw []byte) {
	t.Helper()
	path = filepath.Join(dir, name)
	raw = []byte(content)
	if err := os.WriteFile(path, raw, 0o644); err != nil {
		t.Fatalf("write %s: %v", name, err)
	}
	return path, raw
}

// Two valid but semantically different graphs: the same derived datasets
// "report"/"events" reach different root sources.
const concurrentGraphRootA = `{"datasets":[` +
	`{"name":"report","upstreams":["events"]},` +
	`{"name":"events","upstreams":["chain-A"]},` +
	`{"name":"chain-A","upstreams":[]}]}`

const concurrentGraphRootB = `{"datasets":[` +
	`{"name":"report","upstreams":["events"]},` +
	`{"name":"events","upstreams":["chain-B"]},` +
	`{"name":"chain-B","upstreams":[]}]}`

// TestCrossProcessSnapshotDifferentContentExactlyOneWins submits two
// independent command processes simultaneously against a not-yet-existing
// target, each reading a legal but semantically different graph. Exactly one
// must win; which one is not fixed. The test runs both launch orders and
// accepts either winner, fully checking outcomes against the winner each time.
func TestCrossProcessSnapshotDifferentContentExactlyOneWins(t *testing.T) {
	wantA := mustSnapshotFromGraph(t, concurrentGraphRootA)
	wantB := mustSnapshotFromGraph(t, concurrentGraphRootB)
	if wantA.ContentID == wantB.ContentID {
		t.Fatal("test setup: the two graphs must have different content identifiers")
	}

	const rounds = 8
	wins := map[string]int{"chain-A": 0, "chain-B": 0}
	for round := 0; round < rounds; round++ {
		dir := t.TempDir()
		graphAPath, graphABytes := writeRoundInput(t, dir, "graph-a.json", concurrentGraphRootA)
		graphBPath, graphBBytes := writeRoundInput(t, dir, "graph-b.json", concurrentGraphRootB)
		target := filepath.Join(dir, "snapshot.json")
		if _, err := os.Stat(target); !os.IsNotExist(err) {
			t.Fatalf("round %d: target must not exist before the saves", round)
		}

		// Alternate launch order; thanks to the gate both are always blocked
		// together, so the order cannot predetermine the winner.
		var first, second procResult
		if round%2 == 0 {
			first, second = launchConcurrentSnapshotPair(t, target, graphAPath, graphBPath)
		} else {
			first, second = launchConcurrentSnapshotPair(t, target, graphBPath, graphAPath)
		}
		results := []procResult{first, second}

		var winner, loser *procResult
		for i := range results {
			switch results[i].exitCode {
			case 0:
				if winner != nil {
					t.Fatalf("round %d: both saves succeeded (%s and %s), want exactly one",
						round, winner.label, results[i].label)
				}
				w := results[i]
				winner = &w
			default:
				l := results[i]
				loser = &l
			}
		}
		if winner == nil {
			t.Fatalf("round %d: both saves failed, want exactly one winner\nstderr1: %s\nstderr2: %s",
				round, first.stderr, second.stderr)
		}
		if loser == nil {
			t.Fatalf("round %d: no save was rejected", round)
		}

		// Identify the winning graph from the path the winning command read.
		var winnerRoot string
		var winnerWant *chainledger.SnapshotFile
		switch winner.sourceGraph {
		case graphAPath:
			winnerRoot, winnerWant = "chain-A", wantA
		case graphBPath:
			winnerRoot, winnerWant = "chain-B", wantB
		default:
			t.Fatalf("round %d: winner read unknown graph %q", round, winner.sourceGraph)
		}
		loserRoot := map[string]string{"chain-A": "chain-B", "chain-B": "chain-A"}[winnerRoot]
		wins[winnerRoot]++

		// Winner: exit 0 and stdout contains ONLY the id of what it saved.
		if winner.stdout != winnerWant.ContentID+"\n" {
			t.Errorf("round %d: winner stdout = %q, want exactly %q",
				round, winner.stdout, winnerWant.ContentID)
		}
		if winner.stderr != "" {
			t.Errorf("round %d: winner stderr = %q, want empty", round, winner.stderr)
		}
		// Loser: non-zero exit, empty stdout, stderr explains the target
		// already holds different content and names the target path.
		if loser.exitCode == 0 {
			t.Errorf("round %d: rejected save returned exit code 0, want non-zero", round)
		}
		if loser.stdout != "" {
			t.Errorf("round %d: loser stdout = %q, want empty", round, loser.stdout)
		}
		if !strings.Contains(loser.stderr, "already holds a different snapshot") {
			t.Errorf("round %d: loser stderr = %q, want it to explain the target already holds a different snapshot",
				round, loser.stderr)
		}
		if !strings.Contains(loser.stderr, target) {
			t.Errorf("round %d: loser stderr = %q, want it to name the target path %q",
				round, loser.stderr, target)
		}

		// The target must be one complete, readable snapshot — never a half
		// file — and the winner's printed id, the file's contentId, and the
		// stored graph must all describe the SAME graph.
		finalBytes, err := os.ReadFile(target)
		if err != nil {
			t.Fatalf("round %d: target missing/unreadable after both saves: %v", round, err)
		}
		finalSnap, err := chainledger.ParseSnapshot(finalBytes)
		if err != nil {
			t.Fatalf("round %d: target is not a valid snapshot after the race:\n%s\nerr: %v",
				round, finalBytes, err)
		}
		if finalSnap.ContentID != winnerWant.ContentID {
			t.Errorf("round %d: target contentId %q != winner %q (loser root %s)",
				round, finalSnap.ContentID, winnerWant.ContentID, loserRoot)
		}
		if !reflect.DeepEqual(finalSnap.Graph, winnerWant.Graph) {
			t.Errorf("round %d: stored graph does not match the winning graph %q:\ngot:  %+v\nwant: %+v",
				round, winnerRoot, finalSnap.Graph, winnerWant.Graph)
		}
		// The other version's root must not have leaked into the winner.
		for _, ds := range finalSnap.Graph.Datasets {
			if ds.Name == loserRoot {
				t.Errorf("round %d: loser root %q leaked into the winning snapshot", round, loserRoot)
			}
		}
		// Direct upstream chain preserved end to end: the shared derived
		// dataset traces to the winner's root only.
		if got := rootNames(t, finalSnap, "report"); !reflect.DeepEqual(got, []string{winnerRoot}) {
			t.Errorf("round %d: report roots = %v, want [%s]", round, got, winnerRoot)
		}
		if got := rootNames(t, finalSnap, "events"); !reflect.DeepEqual(got, []string{winnerRoot}) {
			t.Errorf("round %d: events roots = %v, want [%s]", round, got, winnerRoot)
		}
		// The on-disk bytes are exactly the canonical snapshot of the winner.
		wantBytes, err := chainledger.MarshalSnapshot(winnerWant)
		if err != nil {
			t.Fatalf("marshal expected snapshot: %v", err)
		}
		if !bytes.Equal(finalBytes, wantBytes) {
			t.Errorf("round %d: target bytes are not the canonical winner bytes", round)
		}

		// Both source graphs remain byte-for-byte untouched.
		if got, err := os.ReadFile(graphAPath); err != nil || !bytes.Equal(got, graphABytes) {
			t.Errorf("round %d: source graph A was modified", round)
		}
		if got, err := os.ReadFile(graphBPath); err != nil || !bytes.Equal(got, graphBBytes) {
			t.Errorf("round %d: source graph B was modified", round)
		}
		assertNoLeftoverTempFiles(t, dir)
	}
	t.Logf("rounds=%d winner distribution: chain-A=%d chain-B=%d (either winner is correct)",
		rounds, wins["chain-A"], wins["chain-B"])
}

// Two byte-different encodings of ONE graph: different record order, upstream
// order, a duplicated upstream, and JSON whitespace. Semantically identical,
// so they share one content id and one canonical byte form.
const equivalentGraphForm1 = `{"datasets":[` +
	`{"name":"derived","upstreams":["root-b","root-a","root-b"]},` +
	`{"name":"root-a","upstreams":[]},` +
	`{"name":"root-b","upstreams":[]}]}`

const equivalentGraphForm2 = `{
  "datasets": [
    { "name": "root-b", "upstreams": [ ] },
    { "name": "derived", "upstreams": ["root-a", "root-b"] },
    { "name": "root-a", "upstreams": [] }
  ]
}`

// TestCrossProcessSnapshotSemanticallyEqualBothSucceed submits two independent
// processes simultaneously with semantically equal inputs (different record
// order, upstream order, duplicate upstream, whitespace). Both must succeed and
// report the same id, and the created snapshot follows the existing sorting
// and de-duplication rules.
func TestCrossProcessSnapshotSemanticallyEqualBothSucceed(t *testing.T) {
	want1 := mustSnapshotFromGraph(t, equivalentGraphForm1)
	want2 := mustSnapshotFromGraph(t, equivalentGraphForm2)
	if want1.ContentID != want2.ContentID {
		t.Fatalf("test setup: equivalent graphs got different ids %q vs %q",
			want1.ContentID, want2.ContentID)
	}
	wantBytes, err := chainledger.MarshalSnapshot(want1)
	if err != nil {
		t.Fatalf("marshal canonical snapshot: %v", err)
	}

	dir := t.TempDir()
	graph1Path, _ := writeRoundInput(t, dir, "form1.json", equivalentGraphForm1)
	graph2Path, graph2Raw := writeRoundInput(t, dir, "form2.json", equivalentGraphForm2)
	target := filepath.Join(dir, "snapshot.json")

	first, second := launchConcurrentSnapshotPair(t, target, graph1Path, graph2Path)
	for _, res := range []procResult{first, second} {
		if res.exitCode != 0 {
			t.Errorf("%s save of semantically equal content exited %d: %s",
				res.label, res.exitCode, res.stderr)
		}
		if res.stdout != want1.ContentID+"\n" {
			t.Errorf("%s stdout = %q, want exactly %q", res.label, res.stdout, want1.ContentID)
		}
		if res.stderr != "" {
			t.Errorf("%s stderr = %q, want empty", res.label, res.stderr)
		}
	}

	finalBytes, err := os.ReadFile(target)
	if err != nil {
		t.Fatalf("target unreadable: %v", err)
	}
	finalSnap, err := chainledger.ParseSnapshot(finalBytes)
	if err != nil {
		t.Fatalf("target is not a valid snapshot: %v\n%s", err, finalBytes)
	}
	if finalSnap.ContentID != want1.ContentID {
		t.Errorf("target contentId %q, want %q", finalSnap.ContentID, want1.ContentID)
	}
	if !bytes.Equal(finalBytes, wantBytes) {
		t.Errorf("created snapshot bytes do not follow canonical sorting/dedup:\ngot:\n%s\nwant:\n%s",
			finalBytes, wantBytes)
	}
	// Explicitly verify ordering and duplicate removal inside the graph.
	gotNames := make([]string, 0, len(finalSnap.Graph.Datasets))
	for _, ds := range finalSnap.Graph.Datasets {
		gotNames = append(gotNames, ds.Name)
	}
	if !sort.StringsAreSorted(gotNames) {
		t.Errorf("datasets not sorted by name: %v", gotNames)
	}
	for _, ds := range finalSnap.Graph.Datasets {
		if !sort.StringsAreSorted(ds.Upstreams) {
			t.Errorf("upstreams of %q not sorted: %v", ds.Name, ds.Upstreams)
		}
		seen := map[string]bool{}
		for _, u := range ds.Upstreams {
			if seen[u] {
				t.Errorf("upstream %q duplicated under %q", u, ds.Name)
			}
			seen[u] = true
		}
	}
	if got := rootNames(t, finalSnap, "derived"); !reflect.DeepEqual(got, []string{"root-a", "root-b"}) {
		t.Errorf("derived roots = %v, want [root-a root-b]", got)
	}
	if got, err := os.ReadFile(graph2Path); err != nil || !bytes.Equal(got, graph2Raw) {
		t.Errorf("source graph form2 was modified")
	}
	assertNoLeftoverTempFiles(t, dir)
}

// saveOnce runs one snapshot command to completion, returning its result.
func saveOnce(t *testing.T, graphPath, target string) procResult {
	t.Helper()
	var stdout, stderr bytes.Buffer
	cmd := exec.Command(snapshotCLIBin, "snapshot", graphPath, target)
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		if cmd.ProcessState == nil {
			t.Fatalf("seed snapshot failed to run: %v", err)
		}
	}
	return procResult{
		stdout:   stdout.String(),
		stderr:   stderr.String(),
		exitCode: cmd.ProcessState.ExitCode(),
	}
}

// TestCrossProcessSnapshotPreexistingSameContentUntouched seeds the target with
// a valid snapshot of the same content, then submits two simultaneous saves
// (each reading a different equivalent encoding). Both must succeed and the
// existing file must remain byte-for-byte and mtime identical — no rewrite.
func TestCrossProcessSnapshotPreexistingSameContentUntouched(t *testing.T) {
	want := mustSnapshotFromGraph(t, equivalentGraphForm1)

	dir := t.TempDir()
	graph1Path, _ := writeRoundInput(t, dir, "form1.json", equivalentGraphForm1)
	graph2Path, graph2Raw := writeRoundInput(t, dir, "form2.json", equivalentGraphForm2)
	target := filepath.Join(dir, "snapshot.json")

	seed := saveOnce(t, graph1Path, target)
	if seed.exitCode != 0 {
		t.Fatalf("seed snapshot exited %d: %s", seed.exitCode, seed.stderr)
	}
	seedBytes, err := os.ReadFile(target)
	if err != nil {
		t.Fatalf("read seed snapshot: %v", err)
	}
	infoBefore, err := os.Stat(target)
	if err != nil {
		t.Fatalf("stat seed snapshot: %v", err)
	}

	first, second := launchConcurrentSnapshotPair(t, target, graph1Path, graph2Path)
	for _, res := range []procResult{first, second} {
		if res.exitCode != 0 {
			t.Errorf("%s save over identical content exited %d: %s",
				res.label, res.exitCode, res.stderr)
		}
		if res.stdout != want.ContentID+"\n" {
			t.Errorf("%s stdout = %q, want exactly %q", res.label, res.stdout, want.ContentID)
		}
		if res.stderr != "" {
			t.Errorf("%s stderr = %q, want empty", res.label, res.stderr)
		}
	}

	gotBytes, err := os.ReadFile(target)
	if err != nil {
		t.Fatalf("read target after saves: %v", err)
	}
	if !bytes.Equal(gotBytes, seedBytes) {
		t.Errorf("identical-content concurrent saves rewrote the target:\nbefore: %x\nafter:  %x",
			seedBytes, gotBytes)
	}
	infoAfter, err := os.Stat(target)
	if err != nil {
		t.Fatalf("stat target after saves: %v", err)
	}
	if !infoAfter.ModTime().Equal(infoBefore.ModTime()) {
		t.Errorf("identical-content concurrent saves changed mtime: %s -> %s",
			infoBefore.ModTime(), infoAfter.ModTime())
	}
	// The untouched file still reads as the same valid snapshot.
	if _, err := chainledger.ParseSnapshot(gotBytes); err != nil {
		t.Errorf("preexisting snapshot unreadable after concurrent saves: %v", err)
	}
	if got, err := os.ReadFile(graph2Path); err != nil || !bytes.Equal(got, graph2Raw) {
		t.Errorf("source graph form2 was modified")
	}
	assertNoLeftoverTempFiles(t, dir)
}
