package artifactproduction

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
)

func preparePrivateDirectory(path string) (string, error) {
	canonical, err := validateExistingDirectory(path, true)
	if err != nil {
		return "", ErrInvalidConfiguration
	}
	info, err := os.Lstat(canonical)
	if err != nil || info.Mode().Perm() != 0o700 {
		return "", ErrInvalidConfiguration
	}
	return canonical, nil
}

func validateExistingDirectory(path string, createFinal bool) (string, error) {
	if path == "" || !filepath.IsAbs(path) || filepath.Clean(path) != path {
		return "", ErrInvalidConfiguration
	}
	volume := filepath.VolumeName(path)
	rest := strings.TrimPrefix(strings.TrimPrefix(path, volume), string(filepath.Separator))
	parts := strings.Split(rest, string(filepath.Separator))
	current := volume + string(filepath.Separator)
	created := false
	for index, part := range parts {
		if part == "" || part == "." || part == ".." {
			return "", ErrInvalidConfiguration
		}
		current = filepath.Join(current, part)
		info, err := os.Lstat(current)
		if errors.Is(err, os.ErrNotExist) && createFinal && index == len(parts)-1 {
			if err := os.Mkdir(current, 0o700); err != nil {
				return "", err
			}
			created = true
			info, err = os.Lstat(current)
		}
		if err != nil || info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
			return "", ErrInvalidConfiguration
		}
	}
	if created {
		if err := syncDirectory(filepath.Dir(current)); err != nil {
			return "", err
		}
	}
	return current, nil
}

func writePrivateAtomic(path, prefix string, encoded []byte, verify func() error) (returnErr error) {
	directory := filepath.Dir(path)
	temporary, err := os.CreateTemp(directory, prefix)
	if err != nil {
		return err
	}
	temporaryPath := temporary.Name()
	open := true
	defer func() {
		if open {
			_ = temporary.Close()
		}
		_ = os.Remove(temporaryPath)
	}()
	if err := temporary.Chmod(0o600); err != nil {
		return err
	}
	if _, err := temporary.Write(encoded); err != nil {
		return err
	}
	if err := temporary.Sync(); err != nil {
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	open = false
	if err := os.Rename(temporaryPath, path); err != nil {
		return err
	}
	if err := syncDirectory(directory); err != nil {
		return err
	}
	return verify()
}
