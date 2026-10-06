//go:build unix && !linux

package main

// extraTestHelper is the re-entrant subprocess hook TestMain dispatches to.
// The only extra helper today (the bind-mount apply helper) is Linux-only, so
// on other Unix platforms it is a no-op stub; the Linux implementation lives
// in batch_save_failure_linux_test.go.
func extraTestHelper(args []string) (int, bool) {
	return 0, false
}
