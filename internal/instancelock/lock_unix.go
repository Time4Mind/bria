//go:build darwin || dragonfly || freebsd || linux || netbsd || openbsd

package instancelock

import (
	"errors"
	"os"
	"syscall"
)

func acquirePlatform(path string) (func() error, error) {
	info, err := os.Lstat(path)
	if err == nil && info.Mode()&os.ModeSymlink != 0 {
		return nil, ErrLockUnavailable
	}
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, ErrLockUnavailable
	}

	fd, err := syscall.Open(
		path,
		syscall.O_RDWR|syscall.O_CREAT|syscall.O_CLOEXEC|syscall.O_NOFOLLOW,
		0o600,
	)
	if err != nil {
		return nil, ErrLockUnavailable
	}
	file := os.NewFile(uintptr(fd), "bria-instance-lock")
	closeOnError := func(result error) (func() error, error) {
		_ = file.Close()
		return nil, result
	}
	openedInfo, err := file.Stat()
	if err != nil || !openedInfo.Mode().IsRegular() {
		return closeOnError(ErrLockUnavailable)
	}
	if err := file.Chmod(0o600); err != nil {
		return closeOnError(ErrLockUnavailable)
	}
	if err := syscall.Flock(fd, syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		if errors.Is(err, syscall.EWOULDBLOCK) || errors.Is(err, syscall.EAGAIN) {
			return closeOnError(ErrAlreadyLocked)
		}
		return closeOnError(ErrLockUnavailable)
	}

	return func() error {
		unlockErr := syscall.Flock(fd, syscall.LOCK_UN)
		closeErr := file.Close()
		if unlockErr != nil || closeErr != nil {
			return ErrLockUnavailable
		}
		return nil
	}, nil
}
