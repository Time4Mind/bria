// Package workspace provides bounded, node-local project discovery.
package workspace

import (
	"context"
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
	sortDirectories(candidates)
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
