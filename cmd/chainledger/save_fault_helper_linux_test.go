//go:build linux

package main

// Offline, deterministic write-fault injection for the graph-file save path.
//
// The save contract (README "血缘批量调整") promises that when the final graph
// cannot be written back — the temporary file cannot be created in the graph's
// directory, or the fully written temp file cannot be renamed over the
// original — apply exits non-zero, reports the graph file and the save error on
// stderr, prints no success report, and leaves the original graph file
// byte-for-byte intact (temp-file + atomic-rename save). Real disk failures
// cannot be relied on in tests, so this file supplies the two failure points
// without touching the product and without any network or external service.
//
//   - Temp-file creation failure needs no namespace: a read-only directory
//     (0500) still lets the command read the graph and plan but refuses to
//     create the temp file. That test lives in
//     batch_apply_save_failure_linux_test.go.
//
//   - Rename failure is harder to provoke on purpose: the new content must
//     already have been written to the temp file, and only the final rename
//     must fail. A bind mount of the graph file onto ITSELF is exactly that
//     condition inside a private mount namespace: reads and writes to the
//     directory and to a separate temp file keep working, the temp file is
//     fully written, but renaming over the mount point fails with EBUSY
//     ("device or resource busy"). The mount exists only inside a private
//     user+mount namespace, so it needs no privilege, never reaches the host
//     mount table, and disappears the moment the helper process exits.

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"syscall"
	"testing"
)

// writeFaultHelperMode is the hidden argument under which the test binary
// re-executes itself inside a private user+mount namespace, bind-mounts one
// file onto itself, and then runs the real command binary. The mount point
// blocks rename-over-it with EBUSY while reads and ordinary file creation in
// the same directory stay available.
const writeFaultHelperMode = "--chainledger-test-mountfault-helper"

// dispatchExtraTestHelperMode handles test-binary re-entry modes beyond the
// lock helper. It returns (exit code, true) when mode is recognized.
func dispatchExtraTestHelperMode(mode string, args []string) (int, bool) {
	if mode != writeFaultHelperMode {
		return 0, false
	}
	os.Exit(runMountFaultHelper(args))
	return 0, true // unreachable; os.Exit never returns
}

// runMountFaultHelper args: <bind-target> <command-binary> [command args...].
func runMountFaultHelper(args []string) int {
	if len(args) < 2 {
		fmt.Fprintln(os.Stderr, "usage: test-binary", writeFaultHelperMode, "<bind-target> <command> [args...]")
		return 2
	}
	target := args[0]
	binary := args[1]
	cmdArgs := args[2:]

	// Make every mount in this new namespace private first, so the bind mount
	// can never propagate to a peer namespace or the host mount table.
	if err := syscall.Mount("", "/", "", syscall.MS_REC|syscall.MS_PRIVATE, ""); err != nil {
		fmt.Fprintln(os.Stderr, "fault helper cannot privatize mounts:", err)
		return 10
	}
	// Bind the graph file onto itself. Its directory stays writable, so the
	// command creates and fully writes the temp file; only the final
	// temp-file -> graph-file rename is refused (EBUSY).
	if err := syscall.Mount(target, target, "none", syscall.MS_BIND|syscall.MS_REC, ""); err != nil {
		fmt.Fprintln(os.Stderr, "fault helper cannot bind-mount target:", err)
		return 11
	}
	// Replace the helper process image with the real command; the mount lives
	// for the command's lifetime and is reverted automatically when it exits.
	if err := syscall.Exec(binary, append([]string{binary}, cmdArgs...), os.Environ()); err != nil {
		fmt.Fprintln(os.Stderr, "fault helper cannot exec command:", err)
		return 12
	}
	return 0
}

// runInMountNamespace starts the built command binary with argv in a private
// user+mount namespace where mountTarget is bind-mounted onto itself. The
// caller receives the child's exit code, stdout, and stderr. Unprivileged user
// namespaces map only the invoking uid/gid (as root inside the namespace), so
// this needs no privilege and works fully offline.
func runInMountNamespace(t *testing.T, binary string, argv []string, mountTarget string) (stdout, stderr string, exitCode int, startErr error) {
	t.Helper()
	self, err := os.Executable()
	if err != nil {
		t.Fatalf("locate test binary: %v", err)
	}
	helperArgs := append([]string{writeFaultHelperMode, mountTarget, binary}, argv...)
	cmd := exec.Command(self, helperArgs...)
	cmd.SysProcAttr = &syscall.SysProcAttr{
		Cloneflags: syscall.CLONE_NEWNS | syscall.CLONE_NEWUSER,
		// Map root in the new namespace to the invoking user on the host: file
		// permissions and ownership the command observes are unchanged.
		UidMappings: []syscall.SysProcIDMap{{ContainerID: 0, HostID: os.Getuid(), Size: 1}},
		GidMappings: []syscall.SysProcIDMap{{ContainerID: 0, HostID: os.Getgid(), Size: 1}},
	}
	var outBuf, errBuf bytes.Buffer
	cmd.Stdout, cmd.Stderr = &outBuf, &errBuf
	if err := cmd.Run(); err != nil {
		if cmd.ProcessState == nil {
			// The process never started (e.g. user namespaces are disabled by
			// the kernel): an environment limit the caller can skip on.
			return outBuf.String(), errBuf.String(), -1, err
		}
	}
	return outBuf.String(), errBuf.String(), cmd.ProcessState.ExitCode(), nil
}
