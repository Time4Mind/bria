//go:build !darwin && !dragonfly && !freebsd && !linux && !netbsd && !openbsd

package updateruntime

func privateRuntimeDirectory(string) bool { return false }
