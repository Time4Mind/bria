package transcript

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
	"unicode"
)

type codexCandidate struct {
	path     string
	modified int64
	size     int64
}

func (r *Reader) resolveClaude(request Request) (string, error) {
	project := encodeClaudeWorkdir(filepath.Clean(request.Workdir))
	candidate := filepath.Join(
		r.config.ClaudeProjectsRoot,
		project,
		request.ProviderSessionID+".jsonl",
	)
	return safeRegularFile(r.config.ClaudeProjectsRoot, candidate)
}

func encodeClaudeWorkdir(workdir string) string {
	var encoded strings.Builder
	for _, character := range workdir {
		if unicode.IsLetter(character) && character <= unicode.MaxASCII ||
			unicode.IsDigit(character) && character <= unicode.MaxASCII || character == '-' {
			encoded.WriteRune(character)
		} else {
			encoded.WriteByte('-')
		}
	}
	return encoded.String()
}

func (r *Reader) resolveCodex(ctx context.Context, request Request) (string, error) {
	key := resolveCacheKey{
		backend: BackendCodex, sessionID: request.ProviderSessionID,
		workdir: filepath.Clean(request.Workdir),
	}
	if path, negative, ok := r.cachedResolve(key); ok {
		if negative {
			return "", ErrTranscriptNotFound
		}
		if verified, valid := r.verifyCodexPath(path, request); valid {
			return verified, nil
		}
		r.invalidateResolve(key, path)
	}

	for attempt := 0; attempt < 2; attempt++ {
		index, cacheHit, err := r.loadCodexIndex(ctx, attempt > 0)
		if err != nil {
			if errors.Is(err, ErrTranscriptNotFound) {
				r.rememberNegativeResolve(key)
			}
			return "", err
		}
		matched := false
		for _, candidate := range index.bySession[request.ProviderSessionID] {
			if err := ctx.Err(); err != nil {
				return "", err
			}
			if candidate.workdir != filepath.Clean(request.Workdir) {
				continue
			}
			matched = true
			path, valid := r.verifyCodexPath(candidate.path, request)
			if valid {
				r.rememberResolve(key, path)
				return path, nil
			}
		}
		if attempt == 0 && !cacheHit && !matched {
			break
		}
	}
	r.rememberNegativeResolve(key)
	return "", ErrTranscriptNotFound
}

func (r *Reader) verifyCodexPath(candidate string, request Request) (string, bool) {
	path, err := safeRegularFile(r.config.CodexSessionsRoot, candidate)
	if err != nil {
		return "", false
	}
	meta, ok := readCodexMeta(path, r.config.MaxLineBytes)
	if !ok || meta.ID != request.ProviderSessionID || meta.Workdir == "" ||
		!filepath.IsAbs(meta.Workdir) ||
		filepath.Clean(meta.Workdir) != filepath.Clean(request.Workdir) {
		return "", false
	}
	return path, true
}

func (r *Reader) cachedResolve(key resolveCacheKey) (path string, negative, ok bool) {
	r.resolveMu.Lock()
	defer r.resolveMu.Unlock()
	entry, ok := r.resolveCache[key]
	if !ok {
		return "", false, false
	}
	if entry.path != "" {
		r.touchResolveLocked(key)
		return entry.path, false, true
	}
	if time.Now().Before(entry.expiresAt) {
		r.touchResolveLocked(key)
		return "", true, true
	}
	r.deleteResolveLocked(key)
	return "", false, false
}

func (r *Reader) rememberResolve(key resolveCacheKey, path string) {
	r.resolveMu.Lock()
	r.rememberResolveLocked(key, resolveCacheEntry{path: path})
	r.resolveMu.Unlock()
}

func (r *Reader) rememberNegativeResolve(key resolveCacheKey) {
	r.resolveMu.Lock()
	r.rememberResolveLocked(key, resolveCacheEntry{expiresAt: time.Now().Add(negativeResolveTTL)})
	r.resolveMu.Unlock()
}

func (r *Reader) invalidateResolve(key resolveCacheKey, stalePath string) {
	r.resolveMu.Lock()
	if entry, ok := r.resolveCache[key]; ok && entry.path == stalePath {
		r.deleteResolveLocked(key)
	}
	r.resolveMu.Unlock()
}

func (r *Reader) rememberResolveLocked(key resolveCacheKey, entry resolveCacheEntry) {
	if _, exists := r.resolveCache[key]; exists {
		r.deleteResolveLocked(key)
	}
	r.resolveCache[key] = entry
	r.resolveOrder = append(r.resolveOrder, key)
	for len(r.resolveOrder) > maxResolveCacheEntries {
		oldest := r.resolveOrder[0]
		r.resolveOrder = r.resolveOrder[1:]
		delete(r.resolveCache, oldest)
	}
}

func (r *Reader) touchResolveLocked(key resolveCacheKey) {
	for index, current := range r.resolveOrder {
		if current == key {
			r.resolveOrder = append(r.resolveOrder[:index], r.resolveOrder[index+1:]...)
			break
		}
	}
	r.resolveOrder = append(r.resolveOrder, key)
}

func (r *Reader) deleteResolveLocked(key resolveCacheKey) {
	delete(r.resolveCache, key)
	for index, current := range r.resolveOrder {
		if current == key {
			r.resolveOrder = append(r.resolveOrder[:index], r.resolveOrder[index+1:]...)
			return
		}
	}
}

func (r *Reader) codexCandidates(ctx context.Context) ([]codexCandidate, error) {
	root, err := filepath.Abs(r.config.CodexSessionsRoot)
	if err != nil {
		return nil, fmt.Errorf("resolve Codex root: %w", err)
	}
	root, err = filepath.EvalSymlinks(root)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, ErrTranscriptNotFound
		}
		return nil, fmt.Errorf("resolve Codex root: %w", err)
	}
	entries := make([]codexCandidate, 0)
	err = filepath.WalkDir(root, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			if os.IsNotExist(walkErr) {
				return nil
			}
			return walkErr
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if path == root {
			return nil
		}
		relative, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		depth := len(strings.Split(relative, string(filepath.Separator)))
		if entry.IsDir() {
			if entry.Type()&os.ModeSymlink != 0 || depth > 3 {
				return filepath.SkipDir
			}
			return nil
		}
		if depth != 4 || entry.Type()&os.ModeSymlink != 0 ||
			!strings.HasPrefix(entry.Name(), "rollout-") || filepath.Ext(entry.Name()) != ".jsonl" {
			return nil
		}
		if len(entries) >= r.config.MaxCodexFiles {
			return filepath.SkipAll
		}
		info, err := entry.Info()
		if err != nil || !info.Mode().IsRegular() {
			return nil
		}
		entries = append(entries, codexCandidate{
			path: path, modified: info.ModTime().UnixNano(), size: info.Size(),
		})
		return nil
	})
	if err != nil {
		if os.IsNotExist(err) {
			return nil, ErrTranscriptNotFound
		}
		return nil, fmt.Errorf("scan Codex transcripts: %w", err)
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].modified > entries[j].modified })
	return entries, nil
}

type codexMeta struct {
	ID        string
	Workdir   string
	CreatedAt time.Time
}

func readCodexMeta(path string, maxLineBytes int) (codexMeta, bool) {
	lines, err := readLeadingJSONLLines(path, 32, maxLineBytes)
	if err != nil {
		return codexMeta{}, false
	}
	return parseCodexMeta(lines)
}

func parseCodexMeta(lines [][]byte) (codexMeta, bool) {
	for _, line := range lines {
		var row struct {
			Type    string `json:"type"`
			Payload struct {
				ID        string `json:"id"`
				SessionID string `json:"session_id"`
				CWD       string `json:"cwd"`
				Timestamp string `json:"timestamp"`
			} `json:"payload"`
		}
		if json.Unmarshal(line, &row) != nil || row.Type != "session_meta" {
			continue
		}
		id := row.Payload.ID
		if id == "" {
			id = row.Payload.SessionID
		}
		if id != "" {
			createdAt, _ := time.Parse(time.RFC3339Nano, row.Payload.Timestamp)
			return codexMeta{ID: id, Workdir: row.Payload.CWD, CreatedAt: createdAt}, true
		}
	}
	return codexMeta{}, false
}
