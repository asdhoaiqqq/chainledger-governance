//go:build !linux

package main

// dispatchExtraTestHelperMode is the no-op fallback on platforms without the
// Linux mount-namespace write-fault helper. No extra re-entry test-binary
// modes exist there.
func dispatchExtraTestHelperMode(mode string, args []string) (int, bool) {
	return 0, false
}
