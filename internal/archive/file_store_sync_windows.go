//go:build windows

package archive

// Windows does not expose a portable directory fsync. The artifact and
// manifest files are individually flushed before the atomic rename.
func syncDirectory(string) error { return nil }
