//go:build !darwin && !linux

package files

import "os"

// Platforms without a portable O_NOFOLLOW still receive before/after identity
// checks and rejection of every observed symlink component.
func openNoFollow(path string) (*os.File, error) {
	return os.Open(path)
}
