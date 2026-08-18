package processlog

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"
)

func cleanupExpired(root string, now time.Time, open map[string]bool) error {
	entries, err := os.ReadDir(root)
	if err != nil {
		return fmt.Errorf("read process log root: %w", err)
	}
	var result error
	for _, entry := range entries {
		if entry.IsDir() || entry.Type()&os.ModeSymlink != 0 {
			continue
		}
		item, ok := policyForName(entry.Name())
		if !ok {
			continue
		}
		path := filepath.Join(root, entry.Name())
		info, infoErr := entry.Info()
		if infoErr != nil {
			result = errors.Join(result, infoErr)
			continue
		}
		if now.Sub(info.ModTime()) <= item.retention {
			continue
		}
		if open[path] {
			// A supervisor can keep its raw fallback inode open for the whole
			// process lifetime. Truncate expired evidence in place so the same
			// descriptor remains usable without violating retention.
			if truncateErr := os.Truncate(path, 0); truncateErr != nil {
				result = errors.Join(result, truncateErr)
				continue
			}
			if timeErr := os.Chtimes(path, now, now); timeErr != nil {
				result = errors.Join(result, timeErr)
			}
			continue
		}
		if removeErr := os.Remove(path); removeErr != nil && !errors.Is(removeErr, os.ErrNotExist) {
			result = errors.Join(result, removeErr)
		}
	}
	return result
}

func policyForName(name string) (policy, bool) {
	for _, item := range policies {
		if strings.HasPrefix(name, string(item.level)+"-") && strings.HasSuffix(name, ".log") {
			return item, true
		}
	}
	return policy{}, false
}

func adoptRawLog(root string, now time.Time) (string, error) {
	marker := filepath.Join(root, ".tiered-logging-v1")
	_, markerErr := os.Stat(marker)
	firstAdoption := errors.Is(markerErr, os.ErrNotExist)
	if markerErr != nil && !firstAdoption {
		return "", fmt.Errorf("inspect process log migration marker: %w", markerErr)
	}
	path := filepath.Join(root, "node.log")
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		if firstAdoption {
			return "", writeAdoptionMarker(marker)
		}
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("inspect supervisor node log: %w", err)
	}
	if !info.Mode().IsRegular() {
		return "", nil
	}
	if firstAdoption {
		// The pre-tier node.log contains mostly high-volume timing records. Keep
		// a copy under the six-hour detail policy, then truncate the supervisor's
		// open inode before adopting it as critical fallback evidence.
		legacy := filepath.Join(root, fmt.Sprintf(
			"detail-legacy-%s-%d.log", now.Format("20060102T150405"), os.Getpid(),
		))
		if err := copyRawLog(path, legacy); err != nil {
			return "", err
		}
		if err := os.Truncate(path, 0); err != nil {
			return "", fmt.Errorf("truncate migrated supervisor node log: %w", err)
		}
	}
	destination := filepath.Join(root, fmt.Sprintf(
		"critical-raw-%s-%d.log", now.Format("20060102T150405"), os.Getpid(),
	))
	if err := os.Rename(path, destination); err != nil {
		return "", fmt.Errorf("adopt supervisor node log: %w", err)
	}
	if firstAdoption {
		if err := writeAdoptionMarker(marker); err != nil {
			return "", err
		}
	}
	return destination, nil
}

func copyRawLog(source, destination string) error {
	input, err := os.Open(source)
	if err != nil {
		return fmt.Errorf("open legacy node log: %w", err)
	}
	defer input.Close()
	output, err := os.OpenFile(destination, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return fmt.Errorf("create legacy detail log: %w", err)
	}
	_, copyErr := io.Copy(output, input)
	closeErr := output.Close()
	if copyErr != nil || closeErr != nil {
		_ = os.Remove(destination)
		return fmt.Errorf("copy legacy node log: %w", errors.Join(copyErr, closeErr))
	}
	return nil
}

func writeAdoptionMarker(path string) error {
	if err := os.WriteFile(path, []byte("tiered process logs enabled\n"), 0o600); err != nil {
		return fmt.Errorf("write process log migration marker: %w", err)
	}
	return nil
}
