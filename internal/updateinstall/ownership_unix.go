//go:build darwin || dragonfly || freebsd || linux || netbsd || openbsd

package updateinstall

import (
	"os"
	"syscall"
)

func privateInstallDirectory(path string) bool {
	info, err := os.Lstat(path)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm() != 0o700 {
		return false
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	return ok && stat.Uid == uint32(os.Geteuid())
}
