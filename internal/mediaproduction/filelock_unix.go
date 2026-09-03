//go:build !windows

package mediaproduction

import (
	"errors"
	"os"
	"syscall"
)

func acquirePhotoFileLock(path string) (func(), error) {
	if info, err := os.Lstat(path); err == nil {
		if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
			return nil, ErrInvalidConfiguration
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	handle, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	info, err := handle.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm() != 0o600 || !ownedByCurrentUser(info) {
		_ = handle.Close()
		return nil, ErrInvalidConfiguration
	}
	for {
		err = syscall.Flock(int(handle.Fd()), syscall.LOCK_EX)
		if !errors.Is(err, syscall.EINTR) {
			break
		}
	}
	if err != nil {
		_ = handle.Close()
		return nil, err
	}
	return func() {
		_ = syscall.Flock(int(handle.Fd()), syscall.LOCK_UN)
		_ = handle.Close()
	}, nil
}
