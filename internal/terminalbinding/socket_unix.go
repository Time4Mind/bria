//go:build linux || darwin

package terminalbinding

import (
	"os"
	"path/filepath"
	"strings"
	"syscall"
)

func private(info os.FileInfo, directory bool) bool {
	stat, ok := info.Sys().(*syscall.Stat_t)
	return ok && stat.Uid == uint32(os.Getuid()) && info.Mode().Perm()&0077 == 0 &&
		(directory && info.IsDir() || !directory && info.Mode().IsRegular())
}

func trustedDirectory(info os.FileInfo) bool {
	stat, ok := info.Sys().(*syscall.Stat_t)
	return ok && stat.Uid == uint32(os.Getuid()) && info.IsDir() && info.Mode().Perm()&0022 == 0
}

func SocketIdentity(path string) (uint64, uint64, error) {
	if !filepath.IsAbs(path) || filepath.Base(path) != "socket" ||
		!strings.HasPrefix(filepath.Base(filepath.Dir(path)), "bria-terminal-") {
		return 0, 0, ErrMismatch
	}
	dir, err := os.Lstat(filepath.Dir(path))
	if err != nil || !private(dir, true) {
		return 0, 0, ErrMismatch
	}
	info, err := os.Lstat(path)
	if err != nil {
		return 0, 0, ErrUnavailable
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || info.Mode()&os.ModeSocket == 0 || stat.Uid != uint32(os.Getuid()) {
		return 0, 0, ErrMismatch
	}
	return uint64(stat.Dev), stat.Ino, nil
}
