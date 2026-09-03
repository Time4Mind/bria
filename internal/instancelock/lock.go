// Package instancelock prevents two Bria coordinators from owning one durable
// state document at the same time.
package instancelock

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

var (
	ErrAlreadyLocked    = errors.New("another instance already owns the state")
	ErrInvalidStatePath = errors.New("state path is invalid")
	ErrLockUnavailable  = errors.New("exclusive instance lock is unavailable")
)

// Lock holds cross-process ownership until Close. Close is idempotent.
type Lock struct {
	mu      sync.Mutex
	release func() error
	closed  bool
}

// Acquire takes an exclusive non-blocking lock for the canonical absolute
// identity of statePath. It never removes the adjacent lock file.
func Acquire(statePath string) (*Lock, error) {
	canonical, err := canonicalStatePath(statePath)
	if err != nil {
		return nil, ErrInvalidStatePath
	}
	release, err := acquirePlatform(lockFilePath(canonical))
	if err != nil {
		return nil, err
	}
	return &Lock{release: release}, nil
}

// Close releases cross-process ownership without deleting the lock file.
func (lock *Lock) Close() error {
	if lock == nil {
		return nil
	}
	lock.mu.Lock()
	defer lock.mu.Unlock()
	if lock.closed {
		return nil
	}
	lock.closed = true
	if lock.release == nil {
		return ErrLockUnavailable
	}
	if err := lock.release(); err != nil {
		return ErrLockUnavailable
	}
	return nil
}

func canonicalStatePath(statePath string) (string, error) {
	if strings.TrimSpace(statePath) == "" {
		return "", ErrInvalidStatePath
	}
	absolute, err := filepath.Abs(statePath)
	if err != nil {
		return "", ErrInvalidStatePath
	}
	parent, err := filepath.EvalSymlinks(filepath.Dir(absolute))
	if err != nil {
		return "", ErrInvalidStatePath
	}
	info, err := os.Stat(parent)
	if err != nil || !info.IsDir() {
		return "", ErrInvalidStatePath
	}
	return filepath.Join(parent, filepath.Base(absolute)), nil
}

func lockFilePath(canonicalStatePath string) string {
	return filepath.Join(
		filepath.Dir(canonicalStatePath),
		"."+filepath.Base(canonicalStatePath)+".lock",
	)
}
