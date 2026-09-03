//go:build !darwin && !dragonfly && !freebsd && !linux && !netbsd && !openbsd

package messagejournal

import "errors"

func withExclusiveFileLock(_ string, _ func() error) error {
	return errors.New("message journal requires an operating system advisory file lock")
}
