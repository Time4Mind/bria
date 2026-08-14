//go:build !windows

package providerbinding

import (
	"errors"
	"os"
	"syscall"
)

type fileLock struct{ file *os.File }

func acquireFileLock(path string) (*fileLock, error) {
	file, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, err
	}
	if err := syscall.Flock(int(file.Fd()), syscall.LOCK_EX); err != nil {
		file.Close()
		return nil, err
	}
	return &fileLock{file: file}, nil
}

func (l *fileLock) Close() error {
	return errors.Join(syscall.Flock(int(l.file.Fd()), syscall.LOCK_UN), l.file.Close())
}
