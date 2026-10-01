//go:build !unix

package main

// fileLock is a no-op on platforms without flock. Concurrent-save safety is
// provided on unix hosts, which is the supported offline environment; on other
// platforms saves still validate and atomically rename, they simply cannot
// cross-process serialize.
type fileLock struct{}

func acquireLock(path string) (*fileLock, error) { return &fileLock{}, nil }

func (l *fileLock) release() {}
