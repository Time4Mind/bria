//go:build !unix

package safelog

import "os"

type fileLock struct {
	file *os.File
}

func acquireFileLock(path string) (*fileLock, error) {
	file, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	return &fileLock{file: file}, nil
}

func (lock *fileLock) release() {
	_ = lock.file.Close()
}

func syncDirectory(string) error { return nil }
