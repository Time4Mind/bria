//go:build linux

package runnerhost

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"syscall"
)

func SocketOwnerUID(path string) (int, error) {
	parent, err := os.Stat(filepath.Dir(path))
	if err != nil {
		return -1, fmt.Errorf("inspect runner socket directory: %w", err)
	}
	if parent.Mode().Perm()&0o002 != 0 {
		return -1, errors.New("runner socket directory must not be world-writable")
	}
	info, err := os.Lstat(path)
	if err != nil {
		return -1, fmt.Errorf("inspect runner socket: %w", err)
	}
	if info.Mode()&os.ModeSocket == 0 {
		return -1, errors.New("runner endpoint is not a Unix socket")
	}
	if info.Mode().Perm()&0o007 != 0 {
		return -1, errors.New("runner socket must not be accessible to other users")
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return -1, errors.New("runner socket owner is unavailable")
	}
	return int(stat.Uid), nil
}
