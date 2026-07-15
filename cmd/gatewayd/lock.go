package main

import (
	"fmt"
	"os"
	"syscall"
)

// acquireLock takes an exclusive, non-blocking advisory lock on path,
// creating it if needed, so at most one gatewayd instance runs against
// a given data directory at a time.
func acquireLock(path string) (*os.File, error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		f.Close()
		return nil, fmt.Errorf("gatewayd: another instance is already running (lock %s held): %w", path, err)
	}
	return f, nil
}

// releaseLock releases a lock taken by acquireLock.
func releaseLock(f *os.File) {
	_ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
	_ = f.Close()
}
