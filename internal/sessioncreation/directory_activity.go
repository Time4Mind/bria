package sessioncreation

import (
	"context"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

const directoryActivityTTL = 2 * time.Second
const directoryActivityDepth = 2
const projectActivityDepth = 12

type directoryActivityEntry struct {
	modified time.Time
	checked  time.Time
}

type directoryActivityCache struct {
	mu      sync.Mutex
	entries map[string]directoryActivityEntry
}

type rankedDirectory struct {
	directory Directory
	modified  time.Time
}

func newDirectoryActivityCache() *directoryActivityCache {
	return &directoryActivityCache{entries: make(map[string]directoryActivityEntry)}
}

func (cache *directoryActivityCache) sort(ctx context.Context, directories []Directory) error {
	ranked := make([]rankedDirectory, len(directories))
	for index := range directories {
		latest, err := cache.latest(ctx, directories[index].Path)
		if err != nil {
			return err
		}
		ranked[index] = rankedDirectory{directory: directories[index], modified: latest}
	}
	sort.SliceStable(ranked, func(i, j int) bool {
		if !ranked[i].modified.Equal(ranked[j].modified) {
			return ranked[i].modified.After(ranked[j].modified)
		}
		left, right := strings.ToLower(ranked[i].directory.Name), strings.ToLower(ranked[j].directory.Name)
		if left != right {
			return left < right
		}
		return ranked[i].directory.Path < ranked[j].directory.Path
	})
	for index := range ranked {
		directories[index] = ranked[index].directory
	}
	return nil
}

func (cache *directoryActivityCache) latest(ctx context.Context, root string) (time.Time, error) {
	now := time.Now()
	cache.mu.Lock()
	cached, ok := cache.entries[root]
	cache.mu.Unlock()
	if ok && now.Sub(cached.checked) < directoryActivityTTL {
		return cached.modified, nil
	}
	latest, err := newestTreeModification(ctx, root)
	if err != nil {
		return time.Time{}, err
	}
	cache.mu.Lock()
	cache.entries[root] = directoryActivityEntry{modified: latest, checked: now}
	cache.mu.Unlock()
	return latest, nil
}

func newestTreeModification(ctx context.Context, root string) (time.Time, error) {
	latest := time.Time{}
	maxDepth := activityScanDepth(root)
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		if walkErr != nil {
			if entry != nil && entry.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if entry.Type()&fs.ModeSymlink != 0 {
			return nil
		}
		if path != root && entry.IsDir() && ignoredActivityDirectory(entry.Name()) {
			return filepath.SkipDir
		}
		info, err := entry.Info()
		if err != nil {
			if entry.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if info.ModTime().After(latest) {
			latest = info.ModTime()
		}
		if entry.IsDir() && treeDepth(root, path) >= maxDepth {
			return filepath.SkipDir
		}
		return nil
	})
	return latest, err
}

func activityScanDepth(root string) int {
	for _, marker := range []string{".git", "go.mod", "Cargo.toml", "package.json", "pyproject.toml"} {
		if _, err := os.Lstat(filepath.Join(root, marker)); err == nil {
			return projectActivityDepth
		}
	}
	return directoryActivityDepth
}

func ignoredActivityDirectory(name string) bool {
	switch name {
	case ".git", ".cache", ".venv", "node_modules", "vendor", "target", "dist", "build":
		return true
	}
	return false
}

func treeDepth(root, path string) int {
	relative, err := filepath.Rel(root, path)
	if err != nil || relative == "." {
		return 0
	}
	return len(strings.Split(relative, string(filepath.Separator)))
}
