package updateinstall

import (
	"context"
	"os"
	"path/filepath"
	"sync"
)

type InstallLocker interface {
	WithLock(context.Context, func() error) error
}

var installLocks sync.Map

type FileInstallLock struct {
	path  string
	mutex *sync.Mutex
}

func OpenFileInstallLock(installRoot string) (*FileInstallLock, error) {
	if !filepath.IsAbs(installRoot) {
		return nil, ErrInvalidInstaller
	}
	root, err := filepath.EvalSymlinks(filepath.Clean(installRoot))
	if err != nil {
		return nil, ErrInvalidInstaller
	}
	return OpenFileInstallLockAtPath(filepath.Join(root, ".update-install.lock"))
}

// OpenFileInstallLockAtPath opens an install lock at the caller-selected path.
// The path must be the exact, clean absolute path frozen in runtime
// configuration; it is never derived from an install root.
func OpenFileInstallLockAtPath(path string) (*FileInstallLock, error) {
	if !filepath.IsAbs(path) || filepath.Clean(path) != path {
		return nil, ErrInvalidInstaller
	}
	parent := filepath.Dir(path)
	if !privateInstallDirectory(parent) {
		return nil, ErrInvalidInstaller
	}
	if info, err := os.Lstat(path); err != nil {
		if !os.IsNotExist(err) {
			return nil, ErrInvalidInstaller
		}
	} else if !info.Mode().IsRegular() || info.Mode().Perm() != 0o600 {
		return nil, ErrInvalidInstaller
	}
	value, _ := installLocks.LoadOrStore(path, &sync.Mutex{})
	return &FileInstallLock{path: path, mutex: value.(*sync.Mutex)}, nil
}

func (l *FileInstallLock) WithLock(ctx context.Context, action func() error) error {
	if l == nil || l.path == "" || l.mutex == nil || action == nil {
		return ErrInvalidInstaller
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	l.mutex.Lock()
	defer l.mutex.Unlock()
	if err := ctx.Err(); err != nil {
		return err
	}
	return withInstallFileLock(l.path, action)
}
