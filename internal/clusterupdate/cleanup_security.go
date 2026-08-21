package clusterupdate

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

func secureCleanupRoot(path string) (string, bool, error) {
	if strings.TrimSpace(path) == "" || !filepath.IsAbs(path) {
		return "", false, errors.New("cleanup root must be absolute")
	}
	clean := filepath.Clean(path)
	info, err := os.Lstat(clean)
	if errors.Is(err, os.ErrNotExist) {
		return clean, false, nil
	}
	if err != nil {
		return "", false, err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return "", false, errors.New("cleanup root must be a real directory")
	}
	resolved, err := filepath.EvalSymlinks(clean)
	if err != nil {
		return "", false, err
	}
	return filepath.Clean(resolved), true, nil
}

func secureCleanupDirectory(path string) (string, bool, error) { return secureCleanupRoot(path) }

func removeOwnedEntry(path, root string) error {
	if !pathWithin(root, path) {
		return errors.New("refusing to remove path outside cleanup root")
	}
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("inspect removable artifact %q: %w", path, err)
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return errors.New("refusing to remove symlink artifact")
	}
	if info.IsDir() {
		if err := os.RemoveAll(path); err != nil {
			return fmt.Errorf("remove artifact directory %q: %w", path, err)
		}
		return nil
	}
	if err := os.Remove(path); err != nil {
		return fmt.Errorf("remove artifact %q: %w", path, err)
	}
	return nil
}

func pathWithin(root, path string) bool {
	relative, err := filepath.Rel(filepath.Clean(root), filepath.Clean(path))
	return err == nil && relative != ".." && relative != "" &&
		!strings.HasPrefix(relative, ".."+string(os.PathSeparator))
}

func cleanupNow(now time.Time) time.Time {
	if now.IsZero() {
		return time.Now().UTC()
	}
	return now.UTC()
}

func olderThan(modTime, now time.Time, age time.Duration) bool {
	return !modTime.After(now.Add(-age))
}

func finishCleanupReport(report *CleanupReport) {
	sort.Strings(report.Removed)
	sort.Strings(report.Skipped)
}
