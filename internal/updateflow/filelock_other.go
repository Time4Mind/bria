//go:build !darwin && !dragonfly && !freebsd && !linux && !netbsd && !openbsd

package updateflow

import "errors"

func withStateFileLock(string, func() error) error {
	return errors.New("update flow store requires an operating system advisory file lock")
}
