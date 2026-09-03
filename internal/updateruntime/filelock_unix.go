//go:build darwin || dragonfly || freebsd || linux || netbsd || openbsd

package updateruntime

import (
	"errors"
	"fmt"
	"os"
	"syscall"
)

func withStageFileLock(path string, action func() error) error {
	lock, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return fmt.Errorf("open update stage lock: %w", err)
	}
	defer lock.Close()
	if err := lock.Chmod(0o600); err != nil {
		return fmt.Errorf("protect update stage lock: %w", err)
	}
	for {
		err = syscall.Flock(int(lock.Fd()), syscall.LOCK_EX)
		if !errors.Is(err, syscall.EINTR) {
			break
		}
	}
	if err != nil {
		return fmt.Errorf("acquire update stage lock: %w", err)
	}
	defer syscall.Flock(int(lock.Fd()), syscall.LOCK_UN) //nolint:errcheck
	return action()
}
