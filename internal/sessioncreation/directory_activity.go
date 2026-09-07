package sessioncreation

import (
	"context"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

const directoryActivityRefreshInterval = 2 * time.Minute
const directoryActivityDepth = 2

// A project tree can contain generated/vendor trees even when their parent is
// ignored. Four levels captures normal source changes while keeping the idle
// index bounded on large workspaces.
const projectActivityDepth = 4
const activityScanEntryLimit = 20000

type directoryActivityScanner func(context.Context, string) (time.Time, error)

type directoryActivityIndex struct {
	snapshot atomic.Value

	mu      sync.Mutex
	known   map[string]struct{}
	wake    chan struct{}
	cancel  context.CancelFunc
	done    chan struct{}
	scanner directoryActivityScanner
}

type rankedDirectory struct {
	directory Directory
	modified  time.Time
}

func newDirectoryActivityIndex() *directoryActivityIndex {
	return newDirectoryActivityIndexWithScanner(newestTreeModification)
}

func newDirectoryActivityIndexWithScanner(scanner directoryActivityScanner) *directoryActivityIndex {
	index := &directoryActivityIndex{
		known: make(map[string]struct{}), wake: make(chan struct{}, 1), scanner: scanner,
	}
	index.snapshot.Store(map[string]time.Time{})
	return index
}

func (index *directoryActivityIndex) start(parent context.Context, seed string) {
	index.mu.Lock()
	if index.cancel != nil {
		index.mu.Unlock()
		return
	}
	ctx, cancel := context.WithCancel(parent)
	index.cancel = cancel
	index.done = make(chan struct{})
	done := index.done
	index.mu.Unlock()
	go func() {
		defer close(done)
		index.run(ctx, seed)
	}()
}

func (index *directoryActivityIndex) close() {
	index.mu.Lock()
	cancel, done := index.cancel, index.done
	index.mu.Unlock()
	if cancel == nil {
		return
	}
	cancel()
	<-done
}

func (index *directoryActivityIndex) run(ctx context.Context, seed string) {
	index.discover(seed)
	index.refresh(ctx)
	ticker := time.NewTicker(directoryActivityRefreshInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-index.wake:
			index.refresh(ctx)
		case <-ticker.C:
			index.discover(seed)
			index.refresh(ctx)
		}
	}
}

func (index *directoryActivityIndex) discover(root string) {
	entries, err := os.ReadDir(root)
	if err != nil {
		return
	}
	paths := make([]string, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() && entry.Type()&os.ModeSymlink == 0 {
			paths = append(paths, filepath.Join(root, entry.Name()))
		}
	}
	index.track(paths, false)
}

func (index *directoryActivityIndex) track(paths []string, notify bool) {
	added := false
	index.mu.Lock()
	for _, path := range paths {
		if _, ok := index.known[path]; ok {
			continue
		}
		index.known[path] = struct{}{}
		added = true
	}
	index.mu.Unlock()
	if added && notify {
		select {
		case index.wake <- struct{}{}:
		default:
		}
	}
}

func (index *directoryActivityIndex) refresh(ctx context.Context) {
	index.mu.Lock()
	paths := make([]string, 0, len(index.known))
	for path := range index.known {
		paths = append(paths, path)
	}
	index.mu.Unlock()
	sort.Strings(paths)

	current := index.snapshot.Load().(map[string]time.Time)
	next := make(map[string]time.Time, len(current)+len(paths))
	for path, modified := range current {
		next[path] = modified
	}
	changed := false
	for _, path := range paths {
		if ctx.Err() != nil {
			return
		}
		modified, err := index.scanner(ctx, path)
		if err != nil {
			continue
		}
		next[path] = modified
		changed = true
	}
	if changed {
		index.snapshot.Store(next)
	}
}

func (index *directoryActivityIndex) sort(directories []Directory) {
	activity := index.snapshot.Load().(map[string]time.Time)
	ranked := make([]rankedDirectory, len(directories))
	paths := make([]string, len(directories))
	for position := range directories {
		path := directories[position].Path
		modified, ok := activity[path]
		if !ok {
			if info, err := os.Lstat(path); err == nil {
				modified = info.ModTime()
			}
		}
		ranked[position] = rankedDirectory{directory: directories[position], modified: modified}
		paths[position] = path
	}
	index.track(paths, true)
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
	for position := range ranked {
		directories[position] = ranked[position].directory
	}
}

func newestTreeModification(ctx context.Context, root string) (time.Time, error) {
	latest := time.Time{}
	maxDepth := activityScanDepth(root)
	entries := 0
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
		entries++
		if entries > activityScanEntryLimit {
			return filepath.SkipDir
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
