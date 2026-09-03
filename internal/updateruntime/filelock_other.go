//go:build !darwin && !dragonfly && !freebsd && !linux && !netbsd && !openbsd

package updateruntime

import "errors"

func withStageFileLock(string, func() error) error {
	return errors.New("update stager requires an operating system advisory file lock")
}
