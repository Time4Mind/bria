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
	maxPathBytes      = 4096
	listingWireBudget = 32 << 10
)

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
	result := make([]Directory, 0, min(len(entries), MaxEntries))
	wireCost := 0
	for _, entry := range entries {
		if len(result) >= MaxEntries || strings.HasPrefix(entry.Name(), ".") ||
			entry.Type()&os.ModeSymlink != 0 || !entry.IsDir() {
			continue
		}
		info, infoErr := entry.Info()
		if infoErr != nil {
			continue
		}
		childPath := filepath.Join(path, entry.Name())
		itemCost := 6*(len(entry.Name())+len(childPath)) + 128
		if wireCost+itemCost > listingWireBudget {
			break
		}
		wireCost += itemCost
		result = append(result, Directory{Name: entry.Name(), Path: childPath, UpdatedAt: info.ModTime().UTC()})
	}
	sort.Slice(result, func(i, j int) bool {
		if result[i].UpdatedAt.Equal(result[j].UpdatedAt) {
			return result[i].Name < result[j].Name
		}
		return result[i].UpdatedAt.After(result[j].UpdatedAt)
	})
	return result, nil
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
