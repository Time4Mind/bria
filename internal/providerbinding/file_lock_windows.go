//go:build windows

package providerbinding

import (
	"errors"
	"os"

	"golang.org/x/sys/windows"
)

type fileLock struct {
	file       *os.File
	overlapped windows.Overlapped
}

func acquireFileLock(path string) (*fileLock, error) {
	file, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, err
	}
	lock := &fileLock{file: file}
	if err := windows.LockFileEx(
		windows.Handle(file.Fd()), windows.LOCKFILE_EXCLUSIVE_LOCK, 0, 1, 0, &lock.overlapped,
	); err != nil {
		file.Close()
		return nil, err
	}
	return lock, nil
}

func (l *fileLock) Close() error {
	unlock := windows.UnlockFileEx(windows.Handle(l.file.Fd()), 0, 1, 0, &l.overlapped)
	return errors.Join(unlock, l.file.Close())
}
