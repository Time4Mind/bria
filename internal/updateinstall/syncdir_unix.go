//go:build !windows

package updateinstall

import "os"

func syncInstallDirectory(path string) error {
	directory, err := os.Open(path)
	if err != nil {
		return err
	}
	defer directory.Close()
	return directory.Sync()
}
