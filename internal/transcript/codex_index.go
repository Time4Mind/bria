package transcript

import (
	"bufio"
	"bytes"
	"context"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/Time4Mind/bria/internal/processlog"
)

const (
	codexIndexTTL             = 5 * time.Minute
	codexIndexRefreshInterval = 5 * time.Minute
)

type codexIndexedCandidate struct {
	path              string
	providerSessionID string
	workdir           string
	summary           string
	updatedAt         time.Time
	modified          int64
	size              int64
}

type codexIndexSnapshot struct {
	refreshedAt time.Time
	inventory   int
	byWorkdir   map[string][]codexIndexedCandidate
	bySession   map[string][]codexIndexedCandidate
	byPath      map[string]codexIndexedCandidate
}

type codexIndexFlight struct {
	done     chan struct{}
	snapshot *codexIndexSnapshot
	err      error
}

func (r *Reader) loadCodexIndex(
	ctx context.Context,
	force bool,
) (*codexIndexSnapshot, bool, error) {
	requestedAt := time.Now()
	r.codexIndexMu.Lock()
	if snapshot := r.codexIndex; snapshot != nil &&
		((!force && requestedAt.Sub(snapshot.refreshedAt) < codexIndexTTL) ||
			(force && !snapshot.refreshedAt.Before(requestedAt))) {
		r.codexIndexMu.Unlock()
		return snapshot, true, nil
	}
	if flight := r.codexFlight; flight != nil {
		if !force && r.codexIndex != nil {
			snapshot := r.codexIndex
			r.codexIndexMu.Unlock()
			return snapshot, true, nil
		}
		r.codexIndexMu.Unlock()
		select {
		case <-ctx.Done():
			return nil, false, ctx.Err()
		case <-flight.done:
			return flight.snapshot, false, flight.err
		}
	}
	flight := &codexIndexFlight{done: make(chan struct{})}
	r.codexFlight = flight
	ownerCtx := r.indexCtx
	if ownerCtx == nil {
		ownerCtx = context.Background()
	}
	stale := r.codexIndex
	r.codexIndexMu.Unlock()

	go r.runCodexIndexFlight(ownerCtx, flight)
	if !force && stale != nil {
		return stale, true, nil
	}
	select {
	case <-ctx.Done():
		return nil, false, ctx.Err()
	case <-flight.done:
		return flight.snapshot, false, flight.err
	}
}

func (r *Reader) runCodexIndexFlight(ctx context.Context, flight *codexIndexFlight) {
	snapshot, err := r.scanCodex(ctx)
	if err == nil && r.config.CodexCatalogPath != "" {
		if persistErr := r.saveCodexCatalog(snapshot); persistErr != nil {
			processlog.Servicef("bria transcript: catalog_persist outcome=error error=%v", persistErr)
		}
	}
	r.codexIndexMu.Lock()
	if err == nil {
		r.codexIndex = snapshot
	}
	flight.snapshot = snapshot
	flight.err = err
	if r.codexFlight == flight {
		r.codexFlight = nil
	}
	close(flight.done)
	r.codexIndexMu.Unlock()
}

func (r *Reader) buildCodexIndex(ctx context.Context) (*codexIndexSnapshot, error) {
	files, err := r.codexCandidates(ctx)
	if err != nil {
		return nil, err
	}
	r.codexIndexMu.Lock()
	previous := r.codexIndex
	r.codexIndexMu.Unlock()
	snapshot := &codexIndexSnapshot{
		refreshedAt: time.Now(), inventory: len(files),
		byWorkdir: make(map[string][]codexIndexedCandidate),
		bySession: make(map[string][]codexIndexedCandidate),
		byPath:    make(map[string]codexIndexedCandidate, len(files)),
	}
	for _, file := range files {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		candidate, found := codexIndexedCandidate{}, false
		if previous != nil {
			candidate, found = previous.byPath[file.path]
			found = found && candidate.modified == file.modified && candidate.size == file.size
		}
		if !found {
			meta, summary, ok := r.readCodexPreview(file.path)
			if !ok || !providerSessionIDPattern.MatchString(meta.ID) ||
				!filepath.IsAbs(meta.Workdir) || strings.ContainsRune(meta.Workdir, 0) {
				continue
			}
			candidate = codexIndexedCandidate{
				path: file.path, providerSessionID: meta.ID,
				workdir: filepath.Clean(meta.Workdir), summary: summary,
			}
		}
		candidate.modified = file.modified
		candidate.size = file.size
		candidate.updatedAt = time.Unix(0, file.modified).UTC()
		snapshot.byWorkdir[candidate.workdir] = append(snapshot.byWorkdir[candidate.workdir], candidate)
		snapshot.bySession[candidate.providerSessionID] = append(snapshot.bySession[candidate.providerSessionID], candidate)
		snapshot.byPath[candidate.path] = candidate
	}
	for workdir := range snapshot.byWorkdir {
		sort.Slice(snapshot.byWorkdir[workdir], func(i, j int) bool {
			return snapshot.byWorkdir[workdir][i].updatedAt.After(snapshot.byWorkdir[workdir][j].updatedAt)
		})
	}
	return snapshot, nil
}

func (r *Reader) readCodexPreview(path string) (codexMeta, string, bool) {
	file, err := os.Open(path)
	if err != nil {
		return codexMeta{}, "", false
	}
	defer file.Close()
	limited := io.LimitReader(file, maxMetadataReadBytes)
	reader := bufio.NewReaderSize(limited, min(r.config.MaxLineBytes, 64<<10))
	meta := codexMeta{}
	summary := ""
	for attempts := 0; attempts < 32; attempts++ {
		line, oversized, readErr := readBoundedLine(reader, r.config.MaxLineBytes)
		if !oversized {
			line = bytes.TrimSpace(line)
			if len(line) > 0 {
				if meta.ID == "" {
					if parsed, ok := parseCodexMeta([][]byte{line}); ok {
						meta = parsed
					}
				}
				if summary == "" {
					for _, event := range parseCodex([][]byte{line}, r.config.MaxBodyBytes) {
						if event.Kind == EventUserText && strings.TrimSpace(event.Text) != "" {
							summary = bounded(strings.TrimSpace(event.Text), 96)
							break
						}
					}
				}
				if meta.ID != "" && summary != "" {
					return meta, summary, true
				}
			}
		}
		if readErr != nil {
			if readErr == io.EOF {
				break
			}
			return codexMeta{}, "", false
		}
	}
	return meta, summary, meta.ID != ""
}
