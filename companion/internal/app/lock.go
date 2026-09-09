package app

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

// ErrAlreadyRunning is returned when another companion instance holds the
// data-directory lock.
var ErrAlreadyRunning = errors.New("another companion instance is already running")

// instanceLock is an OS-level advisory lock on a file in the data directory.
// The lock dies with the process, so a crashed companion never leaves a stale
// lock behind.
type instanceLock struct {
	f *os.File
}

// acquireInstanceLock takes the single-instance lock for dataDir, failing
// immediately with ErrAlreadyRunning when it is held elsewhere.
func acquireInstanceLock(dataDir string) (*instanceLock, error) {
	path := filepath.Join(dataDir, "companion.lock")
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, fmt.Errorf("opening instance lock: %w", err)
	}
	if err := lockFile(f); err != nil {
		f.Close()
		if errors.Is(err, errLockHeld) {
			return nil, ErrAlreadyRunning
		}
		return nil, fmt.Errorf("acquiring instance lock: %w", err)
	}
	return &instanceLock{f: f}, nil
}

// release drops the lock. The lock file itself is left in place — removing
// it would race with another instance acquiring it.
func (l *instanceLock) release() error {
	if err := unlockFile(l.f); err != nil {
		l.f.Close()
		return fmt.Errorf("releasing instance lock: %w", err)
	}
	return l.f.Close()
}
