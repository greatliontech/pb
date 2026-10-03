//go:build !unix && !windows

package flock

import (
	"errors"
	"os"
)

// TryLock has no file lock on this platform.
func TryLock(*os.File) (bool, error) {
	return false, errors.New("flock: no file lock on this platform")
}

// Unlock has nothing to release on this platform.
func Unlock(*os.File) error { return nil }
