//go:build !darwin && !dragonfly && !freebsd && !linux && !netbsd && !openbsd

package updateinstall

func privateInstallDirectory(string) bool { return false }
