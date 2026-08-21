package clusterupdate

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

const staleInstallLockAge = 10 * time.Minute

type installLock struct {
	path  string
	owner string
}

func acquireInstallLock(installRoot string, now time.Time) (*installLock, error) {
	path := filepath.Join(installRoot, ".install.lock")
	owner := fmt.Sprint(os.Getpid())
	for attempt := 0; attempt < 2; attempt++ {
		if err := os.Mkdir(path, 0o700); err == nil {
			if err := os.WriteFile(filepath.Join(path, "owner"), []byte(owner+"\n"), 0o600); err != nil {
				_ = os.Remove(path)
				return nil, err
			}
			return &installLock{path: path, owner: owner}, nil
		} else if !errors.Is(err, os.ErrExist) {
			return nil, err
		}
		info, err := os.Stat(path)
		if err != nil {
			return nil, errors.New("another Bria installation is active")
		}
		ownerData, _ := os.ReadFile(filepath.Join(path, "owner"))
		ownerRecord := strings.TrimSpace(string(ownerData))
		ownerPIDText := strings.SplitN(ownerRecord, "|", 2)[0]
		ownerPID, ownerErr := strconv.Atoi(ownerPIDText)
		if ownerErr == nil && ownerPID > 1 && installLockOwnerAlive(ownerPID) {
			return nil, errors.New("another Bria installation is active")
		}
		if now.Sub(info.ModTime()) < staleInstallLockAge && ownerErr != nil {
			return nil, errors.New("another Bria installation is active")
		}
		stale := fmt.Sprintf("%s.stale.%d.%d", path, os.Getpid(), now.UnixNano())
		if err := os.Rename(path, stale); err != nil {
			return nil, errors.New("another Bria installation is active")
		}
		_ = os.Remove(filepath.Join(stale, "owner"))
		_ = os.Remove(stale)
	}
	return nil, errors.New("another Bria installation is active")
}

func (lock *installLock) Close() error {
	if lock == nil || lock.path == "" {
		return nil
	}
	data, err := os.ReadFile(filepath.Join(lock.path, "owner"))
	if err != nil || strings.TrimSpace(string(data)) != lock.owner {
		return nil
	}
	if err := os.Remove(filepath.Join(lock.path, "owner")); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return os.Remove(lock.path)
}
