//go:build unix

package main

import (
	"os"
	"syscall"
)

// fileLock is an exclusive, process-advisory lock guarding a snapshot target
// against concurrent saves. It serializes the check-existing-then-write
// sequence so two simultaneous saves of different content can never both
// "win": the first writes, the second observes the existing content and
// refuses, and two saves of identical content both succeed without rewriting
// the file.
type fileLock struct {
	f *os.File
}

// acquireLock creates the lock file if needed and blocks until an exclusive
// lock is held.
func acquireLock(path string) (*fileLock, error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		return nil, err
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX); err != nil {
		f.Close()
		return nil, err
	}
	return &fileLock{f: f}, nil
}

// release unlocks and closes the lock file; the file itself is left behind so
// future saves can lock the same stable path.
func (l *fileLock) release() {
	_ = syscall.Flock(int(l.f.Fd()), syscall.LOCK_UN)
	_ = l.f.Close()
}
