//go:build !linux

package orphanresume

// Cleanup is a no-op without a portable, safe process-table API.
func Cleanup(sessionID, workdir string) error { return nil }
