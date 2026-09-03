//go:build windows

package artifactdelivery

// Windows does not support syncing a directory handle. The temporary file is
// flushed before ReplaceFile-compatible os.Rename promotes it.
func syncDirectory(string) error { return nil }
