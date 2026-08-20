// Package workspace provides bounded, node-local project discovery.
package workspace

import (
	"errors"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

var ErrUnsafePath = errors.New("unsafe workspace path")

const MaxEntries = 256

const (
	maxPathBytes              = 4096
	listingWireBudget         = 32 << 10
	activityScanMaxEntries    = 8192
	activityProjectMaxEntries = 256
	activityProjectMinEntries = 32
	activityMaxDirectoryDepth = 4
)

var ignoredActivityDirectories = map[string]struct{}{
	"__pycache__":  {},
	"build":        {},
	"coverage":     {},
	"dist":         {},
	"node_modules": {},
	"out":          {},
	"pods":         {},
	"target":       {},
	"vendor":       {},
}

type Directory struct {
	Name      string    `json:"name"`
	Path      string    `json:"path"`
	UpdatedAt time.Time `json:"updated_at"`
}

type Browser struct {
	home string
}

func NewBrowser(home string) (*Browser, error) {
	home, err := canonicalDirectory(home)
	if err != nil {
		return nil, err
	}
	return &Browser{home: home}, nil
}

func (b *Browser) Home() string { return b.home }

func (b *Browser) Resolve(path string) (string, error) { return canonicalDirectory(path) }

func (b *Browser) List(path string) ([]Directory, error) {
	path, err := canonicalDirectory(path)
	if err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(path)
	if err != nil {
		return nil, err
	}
	candidates := make([]Directory, 0, min(len(entries), MaxEntries))
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), ".") ||
			entry.Type()&os.ModeSymlink != 0 || !entry.IsDir() {
			continue
		}
		info, infoErr := entry.Info()
		if infoErr != nil {
			continue
		}
		candidates = append(candidates, Directory{
			Name: entry.Name(), Path: filepath.Join(path, entry.Name()), UpdatedAt: info.ModTime().UTC(),
		})
	}
	sortDirectories(candidates)
	if len(candidates) > MaxEntries {
		candidates = candidates[:MaxEntries]
	}
	activityBudget := activityProjectMaxEntries
	if len(candidates) > 0 {
		activityBudget = min(activityBudget, max(
			activityProjectMinEntries, activityScanMaxEntries/len(candidates),
		))
	}
	for index := range candidates {
		candidates[index].UpdatedAt = latestProjectActivity(
			candidates[index].Path, candidates[index].UpdatedAt, activityBudget,
		)
	}
	sortDirectories(candidates)

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

func sortDirectories(directories []Directory) {
	sort.Slice(directories, func(i, j int) bool {
		if directories[i].UpdatedAt.Equal(directories[j].UpdatedAt) {
			return directories[i].Name < directories[j].Name
		}
		return directories[i].UpdatedAt.After(directories[j].UpdatedAt)
	})
}

type activityDirectory struct {
	path  string
	depth int
}

func latestProjectActivity(path string, latest time.Time, maxEntries int) time.Time {
	queue := []activityDirectory{{path: path}}
	visited := 0
	for len(queue) > 0 && visited < maxEntries {
		current := queue[0]
		queue = queue[1:]
		directory, err := os.Open(current.path)
		if err != nil {
			continue
		}
		entries, readErr := directory.ReadDir(maxEntries - visited)
		closeErr := directory.Close()
		if (readErr != nil && len(entries) == 0) || closeErr != nil {
			continue
		}
		visited += len(entries)
		for _, entry := range entries {
			name := entry.Name()
			if strings.HasPrefix(name, ".") || entry.Type()&os.ModeSymlink != 0 {
				continue
			}
			if entry.IsDir() {
				if _, ignored := ignoredActivityDirectories[strings.ToLower(name)]; ignored {
					continue
				}
			}
			info, infoErr := entry.Info()
			if infoErr != nil || (!info.IsDir() && !info.Mode().IsRegular()) {
				continue
			}
			if info.ModTime().After(latest) {
				latest = info.ModTime().UTC()
			}
			if info.IsDir() && current.depth < activityMaxDirectoryDepth {
				queue = append(queue, activityDirectory{
					path: filepath.Join(current.path, name), depth: current.depth + 1,
				})
			}
		}
	}
	return latest
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
