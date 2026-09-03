//go:build darwin || dragonfly || freebsd || linux || netbsd || openbsd

package telegramnotify

import (
	"errors"
	"os"
	"syscall"
)

func withPartReceiptFileLock(path string, action func() error) error {
	if info, err := os.Lstat(path); err == nil {
		if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
			return errors.New("invalid part receipt lock file")
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	lock, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return err
	}
	defer lock.Close()
	if err := lock.Chmod(0o600); err != nil {
		return err
	}
	for {
		err = syscall.Flock(int(lock.Fd()), syscall.LOCK_EX)
		if !errors.Is(err, syscall.EINTR) {
			break
		}
	}
	if err != nil {
		return err
	}
	defer syscall.Flock(int(lock.Fd()), syscall.LOCK_UN) //nolint:errcheck
	return action()
}
