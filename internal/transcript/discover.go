package transcript

import (
	"context"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

type Candidate struct {
	ProviderSessionID string    `json:"provider_session_id"`
	Summary           string    `json:"summary,omitempty"`
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
	request := Request{Backend: backend, ProviderSessionID: "placeholder", Workdir: workdir}
	if err := validateRequest(request); err != nil {
		return Discovery{}, err
	}
	if offset < 0 || limit < 1 || limit > 32 {
		return Discovery{}, ErrInvalidRequest
	}
	var candidates []Candidate
	var err error
	switch backend {
	case BackendClaude:
		candidates, err = r.discoverClaude(workdir)
	case BackendCodex:
		candidates, err = r.discoverCodex(ctx, workdir)
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

func (r *Reader) discoverCodex(ctx context.Context, workdir string) ([]Candidate, error) {
	files, err := r.codexCandidates(ctx)
	if err == ErrTranscriptNotFound {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	result := make([]Candidate, 0)
	for _, file := range files {
		meta, ok := readCodexMeta(file.path, r.config.MaxLineBytes)
		if ok && filepath.Clean(meta.Workdir) == filepath.Clean(workdir) && providerSessionIDPattern.MatchString(meta.ID) {
			result = append(result, Candidate{
				ProviderSessionID: meta.ID, UpdatedAt: time.Unix(0, file.modified).UTC(),
			})
		}
	}
	return result, nil
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
