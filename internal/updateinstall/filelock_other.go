//go:build !darwin && !dragonfly && !freebsd && !linux && !netbsd && !openbsd

package updateinstall

import "errors"

func withInstallFileLock(string, func() error) error {
	return errors.New("packaged update installation requires an operating system advisory file lock")
}
