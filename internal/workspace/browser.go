// Package workspace provides bounded, node-local project discovery.
package workspace

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

var ErrUnsafePath = errors.New("unsafe workspace path")

var privateHomeDirectories = map[string]struct{}{
	"Desktop": {}, "Documents": {}, "Downloads": {}, "Library": {},
	"Movies": {}, "Music": {}, "Pictures": {},
}

const MaxEntries = 256

const (
	maxPathBytes               = 4096
	listingWireBudget          = 32 << 10
	secondLevelMaxCandidates   = 1024
	secondLevelMaxEntries      = 1024
	secondLevelRefreshInterval = 30 * time.Second
)

type Directory struct {
	Name      string    `json:"name"`
	Path      string    `json:"path"`
	UpdatedAt time.Time `json:"updated_at"`
}

type Browser struct {
	home            string
	scanSecondLevel func(string, time.Time) time.Time
	activityMu      sync.RWMutex
	activity        map[string]cachedActivity
	refreshing      map[string]bool
}

type cachedActivity struct {
	updatedAt   time.Time
	refreshedAt time.Time
}

func NewBrowser(home string) (*Browser, error) {
	home, err := canonicalDirectory(home)
	if err != nil {
		return nil, err
	}
	return &Browser{
		home: home, scanSecondLevel: latestSecondLevelActivity,
		activity: make(map[string]cachedActivity), refreshing: make(map[string]bool),
	}, nil
}

func (b *Browser) Home() string { return b.home }

func (b *Browser) Resolve(path string) (string, error) { return canonicalDirectory(path) }

func (b *Browser) List(ctx context.Context, path string) ([]Directory, error) {
	if ctx == nil {
		return nil, errors.New("workspace list context is required")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	path, err := canonicalDirectory(path)
	if err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(path)
	if err != nil {
		return nil, err
	}
	candidates := make([]Directory, 0, len(entries))
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), ".") ||
			entry.Type()&os.ModeSymlink != 0 {
			continue
		}
		// Read only the immediate directory's metadata. Ranking must never
		// traverse descendants from the Telegram polling path.
		info, infoErr := entry.Info()
		if infoErr != nil || !info.IsDir() {
			continue
		}
		candidates = append(candidates, Directory{
			Name:      entry.Name(),
			Path:      filepath.Join(path, entry.Name()),
			UpdatedAt: info.ModTime().UTC(),
		})
	}
	for index := range candidates {
		if updatedAt, found := b.cachedSecondLevelActivity(candidates[index].Path); found &&
			updatedAt.After(candidates[index].UpdatedAt) {
			candidates[index].UpdatedAt = updatedAt
		}
	}
	sortDirectories(candidates)
	b.scheduleSecondLevelRefresh(candidates)
	if len(candidates) > MaxEntries {
		candidates = candidates[:MaxEntries]
	}

	result := make([]Directory, 0, len(candidates))
	wireCost := 0
	for _, candidate := range candidates {
		itemCost := 6*(len(candidate.Name)+len(candidate.Path)) + 128
		if wireCost+itemCost > listingWireBudget {
			break
		}
		wireCost += itemCost
		result = append(result, candidate)
	}
	return result, nil
}

// scheduleSecondLevelRefresh keeps the additional directory read outside the
// Telegram polling path. It refreshes only immediate entries of each visible
// directory and never descends into them.
func (b *Browser) scheduleSecondLevelRefresh(candidates []Directory) {
	now := time.Now()
	refresh := make([]Directory, 0, min(len(candidates), secondLevelMaxCandidates))
	b.activityMu.Lock()
	for _, candidate := range candidates {
		if len(refresh) == secondLevelMaxCandidates {
			break
		}
		if !b.canScanSecondLevel(candidate.Path) {
			continue
		}
		cached := b.activity[candidate.Path]
		if b.refreshing[candidate.Path] ||
			(!cached.refreshedAt.IsZero() && now.Sub(cached.refreshedAt) < secondLevelRefreshInterval) {
			continue
		}
		b.refreshing[candidate.Path] = true
		refresh = append(refresh, candidate)
	}
	b.activityMu.Unlock()
	if len(refresh) == 0 {
		return
	}
	go func() {
		for _, candidate := range refresh {
			updatedAt := b.scanSecondLevel(candidate.Path, candidate.UpdatedAt)
			b.activityMu.Lock()
			b.activity[candidate.Path] = cachedActivity{
				updatedAt: updatedAt.UTC(), refreshedAt: time.Now(),
			}
			delete(b.refreshing, candidate.Path)
			b.activityMu.Unlock()
		}
	}()
}

func (b *Browser) canScanSecondLevel(path string) bool {
	if filepath.Dir(path) != b.home {
		return true
	}
	_, private := privateHomeDirectories[filepath.Base(path)]
	return !private
}

func (b *Browser) cachedSecondLevelActivity(path string) (time.Time, bool) {
	b.activityMu.RLock()
	defer b.activityMu.RUnlock()
	activity, found := b.activity[path]
	return activity.updatedAt, found
}

func latestSecondLevelActivity(path string, latest time.Time) time.Time {
	directory, err := os.Open(path)
	if err != nil {
		return latest
	}
	entries, readErr := directory.Readdir(secondLevelMaxEntries)
	closeErr := directory.Close()
	if (readErr != nil && len(entries) == 0) || closeErr != nil {
		return latest
	}
	for _, info := range entries {
		if strings.HasPrefix(info.Name(), ".") || info.Mode()&os.ModeSymlink != 0 {
			continue
		}
		if !info.IsDir() && !info.Mode().IsRegular() {
			continue
		}
		if info.ModTime().After(latest) {
			latest = info.ModTime().UTC()
		}
	}
	return latest
}

func sortDirectories(directories []Directory) {
	sort.Slice(directories, func(i, j int) bool {
		if directories[i].UpdatedAt.Equal(directories[j].UpdatedAt) {
			return directories[i].Name < directories[j].Name
		}
		return directories[i].UpdatedAt.After(directories[j].UpdatedAt)
	})
}

func Parent(path string) (string, error) {
	path, err := canonicalDirectory(path)
	if err != nil {
		return "", err
	}
	return filepath.Dir(path), nil
}

func canonicalDirectory(path string) (string, error) {
	if !filepath.IsAbs(path) || len(path) > maxPathBytes || strings.ContainsRune(path, 0) {
		return "", ErrUnsafePath
	}
	path, err := filepath.EvalSymlinks(filepath.Clean(path))
	if err != nil {
		return "", err
	}
	info, err := os.Stat(path)
	if err != nil || !info.IsDir() {
		return "", ErrUnsafePath
	}
	return path, nil
}
