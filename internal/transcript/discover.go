package transcript

import (
	"context"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/Time4Mind/bria/internal/processlog"
)

type Candidate struct {
	ProviderSessionID string    `json:"provider_session_id"`
	Summary           string    `json:"summary,omitempty"`
	CreatedAt         time.Time `json:"created_at,omitempty"`
	UpdatedAt         time.Time `json:"updated_at"`
}

type Discovery struct {
	Candidates []Candidate `json:"candidates"`
	Total      int         `json:"total"`
}

// Discover returns recent resumable sessions belonging to one exact workdir.
// It exposes provider identities, never provider-owned file paths.
func (r *Reader) Discover(
	ctx context.Context,
	backend Backend,
	workdir string,
	offset, limit int,
) (Discovery, error) {
	return r.discover(ctx, backend, workdir, offset, limit, false)
}

// DiscoverFresh bypasses the short-lived Codex inventory cache. Session-start
// reconciliation uses it when a provider identity may have been created after
// the last UI lookup.
func (r *Reader) DiscoverFresh(
	ctx context.Context,
	backend Backend,
	workdir string,
	offset, limit int,
) (Discovery, error) {
	return r.discover(ctx, backend, workdir, offset, limit, true)
}

func (r *Reader) discover(
	ctx context.Context,
	backend Backend,
	workdir string,
	offset, limit int,
	force bool,
) (result Discovery, resultErr error) {
	startedAt := time.Now()
	cacheHit := false
	inventory := 0
	defer func() {
		outcome := discoverOutcome(resultErr)
		processlog.Outcomef(processlog.Detail, outcome,
			"bria transcript: discover_timing backend=%s total_ms=%d cache=%t "+
				"inventory=%d matched=%d returned=%d offset=%d outcome=%s",
			backend, time.Since(startedAt).Milliseconds(), cacheHit, inventory,
			result.Total, len(result.Candidates), offset, outcome,
		)
	}()
	request := Request{Backend: backend, ProviderSessionID: "placeholder", Workdir: workdir}
	if err := validateRequest(request); err != nil {
		return Discovery{}, err
	}
	if offset < 0 || limit < 1 || limit > 32 {
		return Discovery{}, ErrInvalidRequest
	}
	if backend == BackendCodex {
		result, cacheHit, inventory, resultErr = r.discoverCodexPage(
			ctx, filepath.Clean(workdir), offset, limit, force,
		)
		return result, resultErr
	}
	var candidates []Candidate
	var err error
	switch backend {
	case BackendClaude:
		candidates, err = r.discoverClaude(workdir)
	default:
		return Discovery{}, ErrUnsupportedBackend
	}
	if err != nil {
		return Discovery{}, err
	}
	sort.Slice(candidates, func(i, j int) bool {
		return candidates[i].UpdatedAt.After(candidates[j].UpdatedAt)
	})
	total := len(candidates)
	if offset >= total {
		return Discovery{Candidates: []Candidate{}, Total: total}, nil
	}
	end := min(total, offset+limit)
	candidates = candidates[offset:end]
	for index := range candidates {
		candidates[index].Summary = r.candidateSummary(ctx, backend, workdir, candidates[index].ProviderSessionID)
	}
	return Discovery{Candidates: candidates, Total: total}, nil
}

func discoverOutcome(err error) string {
	if err != nil {
		return "error"
	}
	return "ok"
}

func (r *Reader) discoverClaude(workdir string) ([]Candidate, error) {
	directory := filepath.Join(r.config.ClaudeProjectsRoot, encodeClaudeWorkdir(filepath.Clean(workdir)))
	entries, err := os.ReadDir(directory)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	result := make([]Candidate, 0, len(entries))
	for _, entry := range entries {
		id := strings.TrimSuffix(entry.Name(), ".jsonl")
		if entry.Type()&os.ModeSymlink != 0 || entry.IsDir() || filepath.Ext(entry.Name()) != ".jsonl" ||
			!providerSessionIDPattern.MatchString(id) {
			continue
		}
		info, infoErr := entry.Info()
		if infoErr == nil && info.Mode().IsRegular() {
			result = append(result, Candidate{ProviderSessionID: id, UpdatedAt: info.ModTime().UTC()})
		}
	}
	return result, nil
}

func (r *Reader) discoverCodexPage(
	ctx context.Context,
	workdir string,
	offset, limit int,
	force bool,
) (Discovery, bool, int, error) {
	index, cacheHit, err := r.loadCodexIndex(ctx, force)
	if err == ErrTranscriptNotFound {
		return Discovery{Candidates: []Candidate{}}, cacheHit, 0, nil
	}
	if err != nil {
		return Discovery{}, cacheHit, 0, err
	}
	candidates := index.byWorkdir[workdir]
	total := len(candidates)
	if offset >= total {
		return Discovery{Candidates: []Candidate{}, Total: total}, cacheHit, index.inventory, nil
	}
	end := min(total, offset+limit)
	result := make([]Candidate, 0, end-offset)
	for _, candidate := range candidates[offset:end] {
		request := Request{
			Backend: BackendCodex, ProviderSessionID: candidate.providerSessionID,
			Workdir: workdir,
		}
		path, valid := r.verifyCodexPath(candidate.path, request)
		if !valid {
			if !force {
				return r.discoverCodexPage(ctx, workdir, offset, limit, true)
			}
			continue
		}
		r.rememberResolve(resolveCacheKey{
			backend: BackendCodex, sessionID: candidate.providerSessionID, workdir: workdir,
		}, path)
		summary := candidate.summary
		if summary == "" {
			if first, readErr := r.readFirstUserTextPath(ctx, BackendCodex, path); readErr == nil {
				summary = bounded(strings.TrimSpace(first), 96)
			}
		}
		result = append(result, Candidate{
			ProviderSessionID: candidate.providerSessionID,
			Summary:           summary, CreatedAt: candidate.createdAt, UpdatedAt: candidate.updatedAt,
		})
	}
	return Discovery{Candidates: result, Total: total}, cacheHit, index.inventory, nil
}

func (r *Reader) candidateSummary(ctx context.Context, backend Backend, workdir, id string) string {
	events, err := r.Read(ctx, Request{Backend: backend, ProviderSessionID: id, Workdir: workdir})
	if err != nil {
		return ""
	}
	for _, event := range events {
		if event.Kind == EventUserText && strings.TrimSpace(event.Text) != "" {
			return bounded(strings.TrimSpace(event.Text), 96)
		}
	}
	return ""
}
