//go:build !darwin && !dragonfly && !freebsd && !linux && !netbsd && !openbsd

package telegramnotify

import "errors"

func withPartReceiptFileLock(_ string, _ func() error) error {
	return errors.New("part receipt store requires an operating system advisory file lock")
}
