package transcript

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"time"
)

const maxCodexCatalogBytes = 1 << 20

type codexCatalog struct {
	Version int                 `json:"version"`
	Entries []codexCatalogEntry `json:"entries"`
}

type codexCatalogEntry struct {
	Path              string `json:"path"`
	ProviderSessionID string `json:"provider_session_id"`
	Workdir           string `json:"workdir"`
	Modified          int64  `json:"modified"`
	Size              int64  `json:"size"`
	CreatedAt         int64  `json:"created_at,omitempty"`
}

func (r *Reader) loadCodexCatalog() (*codexIndexSnapshot, error) {
	info, err := os.Lstat(r.config.CodexCatalogPath)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() || info.Size() > maxCodexCatalogBytes {
		return nil, errors.New("Codex transcript catalog is unsafe or too large")
	}
	encoded, err := os.ReadFile(r.config.CodexCatalogPath)
	if err != nil {
		return nil, err
	}
	if len(encoded) > maxCodexCatalogBytes {
		return nil, errors.New("Codex transcript catalog is too large")
	}
	var catalog codexCatalog
	if json.Unmarshal(encoded, &catalog) != nil || catalog.Version != 1 ||
		len(catalog.Entries) > r.config.MaxCodexFiles {
		return nil, errors.New("Codex transcript catalog is invalid")
	}
	snapshot := &codexIndexSnapshot{
		refreshedAt: time.Time{}, inventory: len(catalog.Entries),
		byWorkdir: make(map[string][]codexIndexedCandidate),
		bySession: make(map[string][]codexIndexedCandidate),
		byPath:    make(map[string]codexIndexedCandidate),
	}
	for _, entry := range catalog.Entries {
		if !providerSessionIDPattern.MatchString(entry.ProviderSessionID) ||
			!filepath.IsAbs(entry.Path) || !filepath.IsAbs(entry.Workdir) {
			continue
		}
		candidate := codexIndexedCandidate{
			path: filepath.Clean(entry.Path), providerSessionID: entry.ProviderSessionID,
			workdir: filepath.Clean(entry.Workdir), modified: entry.Modified, size: entry.Size,
			updatedAt: time.Unix(0, entry.Modified).UTC(),
		}
		if entry.CreatedAt > 0 {
			candidate.createdAt = time.Unix(0, entry.CreatedAt).UTC()
		}
		if _, duplicate := snapshot.byPath[candidate.path]; duplicate {
			continue
		}
		snapshot.byPath[candidate.path] = candidate
		snapshot.byWorkdir[candidate.workdir] = append(snapshot.byWorkdir[candidate.workdir], candidate)
		snapshot.bySession[candidate.providerSessionID] = append(snapshot.bySession[candidate.providerSessionID], candidate)
	}
	for workdir := range snapshot.byWorkdir {
		sort.Slice(snapshot.byWorkdir[workdir], func(i, j int) bool {
			return snapshot.byWorkdir[workdir][i].updatedAt.After(
				snapshot.byWorkdir[workdir][j].updatedAt,
			)
		})
	}
	return snapshot, nil
}

func (r *Reader) saveCodexCatalog(snapshot *codexIndexSnapshot) error {
	if snapshot == nil {
		return errors.New("Codex transcript catalog snapshot is missing")
	}
	catalog := codexCatalog{Version: 1, Entries: make([]codexCatalogEntry, 0, len(snapshot.byPath))}
	for _, candidate := range snapshot.byPath {
		entry := codexCatalogEntry{
			Path: candidate.path, ProviderSessionID: candidate.providerSessionID,
			Workdir: candidate.workdir, Modified: candidate.modified, Size: candidate.size,
		}
		if !candidate.createdAt.IsZero() {
			entry.CreatedAt = candidate.createdAt.UnixNano()
		}
		catalog.Entries = append(catalog.Entries, entry)
	}
	encoded, err := json.Marshal(catalog)
	if err != nil {
		return err
	}
	if len(encoded) > maxCodexCatalogBytes {
		return errors.New("Codex transcript catalog is too large")
	}
	path := r.config.CodexCatalogPath
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return fmt.Errorf("create Codex transcript catalog directory: %w", err)
	}
	temporary, err := os.CreateTemp(filepath.Dir(path), ".codex-transcript-catalog-*")
	if err != nil {
		return err
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)
	if err := temporary.Chmod(0600); err != nil {
		temporary.Close()
		return err
	}
	if _, err := temporary.Write(encoded); err != nil {
		temporary.Close()
		return err
	}
	if err := temporary.Sync(); err != nil {
		temporary.Close()
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	return os.Rename(temporaryPath, path)
}
