//go:build unix

package app

import (
	"errors"
	"os"
	"syscall"
)

var errLockHeld = errors.New("lock held")

func lockFile(f *os.File) error {
	err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
	if errors.Is(err, syscall.EWOULDBLOCK) {
		return errLockHeld
	}
	return err
}

func unlockFile(f *os.File) error {
	return syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
}
