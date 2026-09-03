//go:build windows

package mediaproduction

import (
	"errors"
	"os"
)

func acquirePhotoFileLock(path string) (func(), error) {
	if info, err := os.Lstat(path); err == nil {
		if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
			return nil, ErrInvalidConfiguration
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	return func() {}, nil
}
