//go:build unix

package flock

import (
	"errors"
	"os"
	"syscall"
)

// TryLock takes the file's lock, exclusive, without waiting: false
// where another descriptor holds it.
func TryLock(f *os.File) (bool, error) {
	err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
	if errors.Is(err, syscall.EWOULDBLOCK) {
		return false, nil
	}
	return err == nil, err
}

// Unlock releases the file's lock.
func Unlock(f *os.File) error {
	return syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
}
