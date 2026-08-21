package transcript

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"
)

func TestCodexResolveCacheKeepsVerifiedSessionPath(t *testing.T) {
	layout := newTestLayout(t)
	workdir := "/srv/project"
	sessionID := "codex-cache"
	ownPath := filepath.Join(layout.codex, "2026", "08", "09", "rollout-own.jsonl")
	writeTestFile(t, ownPath, `{"type":"session_meta","payload":{"id":"codex-cache","cwd":"/srv/project"}}
{"type":"event_msg","payload":{"type":"user_message","message":"own"}}
`)
	reader := newTestReader(t, layout, nil)
	request := Request{Backend: BackendCodex, ProviderSessionID: sessionID, Workdir: workdir}
	if _, err := reader.Read(context.Background(), request); err != nil {
		t.Fatal(err)
	}
	foreignPath := filepath.Join(layout.codex, "2026", "08", "10", "rollout-foreign.jsonl")
	writeTestFile(t, foreignPath, `{"type":"session_meta","payload":{"id":"other-session","cwd":"/srv/project"}}
{"type":"event_msg","payload":{"type":"user_message","message":"foreign"}}
`)

	events, err := reader.Read(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 1 || events[0].Text != "own" {
		t.Fatalf("cache selected the wrong transcript: %#v", events)
	}
	key := resolveCacheKey{backend: BackendCodex, sessionID: sessionID, workdir: workdir}
	reader.resolveMu.Lock()
	cached := reader.resolveCache[key].path
	reader.resolveMu.Unlock()
	resolvedOwn, err := filepath.EvalSymlinks(ownPath)
	if err != nil {
		t.Fatal(err)
	}
	if cached != resolvedOwn {
		t.Fatalf("cached path = %q, want %q", cached, resolvedOwn)
	}
}

func TestCodexResolveCacheHitSkipsIndexScan(t *testing.T) {
	layout := newTestLayout(t)
	request := Request{Backend: BackendCodex, ProviderSessionID: "codex-hit", Workdir: "/srv/project"}
	path := filepath.Join(layout.codex, "2026", "08", "10", "rollout-hit.jsonl")
	writeTestFile(t, path, `{"type":"session_meta","payload":{"id":"codex-hit","cwd":"/srv/project"}}
`)
	reader := newTestReader(t, layout, nil)
	if _, err := reader.resolveCodex(context.Background(), request); err != nil {
		t.Fatal(err)
	}

	scanCalls := 0
	reader.scanCodex = func(context.Context) (*codexIndexSnapshot, error) {
		scanCalls++
		return nil, errors.New("unexpected index scan on resolve-cache hit")
	}
	if _, err := reader.resolveCodex(context.Background(), request); err != nil {
		t.Fatalf("cached resolve failed: %v", err)
	}
	if scanCalls != 0 {
		t.Fatalf("cache hit triggered %d index scans", scanCalls)
	}
}

func TestCodexResolveCacheRejectsMetadataMismatch(t *testing.T) {
	layout := newTestLayout(t)
	request := Request{Backend: BackendCodex, ProviderSessionID: "codex-mismatch", Workdir: "/srv/project"}
	path := filepath.Join(layout.codex, "2026", "08", "10", "rollout-mismatch.jsonl")
	writeTestFile(t, path, `{"type":"session_meta","payload":{"id":"codex-mismatch","cwd":"/srv/project"}}
`)
	reader := newTestReader(t, layout, nil)
	if _, err := reader.resolveCodex(context.Background(), request); err != nil {
		t.Fatal(err)
	}

	writeTestFile(t, path, `{"type":"session_meta","payload":{"id":"different-session","cwd":"/srv/project"}}
`)
	if _, err := reader.resolveCodex(context.Background(), request); !errors.Is(err, ErrTranscriptNotFound) {
		t.Fatalf("metadata mismatch returned %v, want transcript not found", err)
	}
}

func TestCodexResolveCacheInvalidatesDeletedPathAndFindsReplacement(t *testing.T) {
	layout := newTestLayout(t)
	request := Request{Backend: BackendCodex, ProviderSessionID: "codex-moved", Workdir: "/srv/project"}
	oldPath := filepath.Join(layout.codex, "2026", "08", "09", "rollout-old.jsonl")
	writeTestFile(t, oldPath, `{"type":"session_meta","payload":{"id":"codex-moved","cwd":"/srv/project"}}
{"type":"event_msg","payload":{"type":"user_message","message":"old"}}
`)
	reader := newTestReader(t, layout, nil)
	if _, err := reader.Read(context.Background(), request); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(oldPath); err != nil {
		t.Fatal(err)
	}
	newPath := filepath.Join(layout.codex, "2026", "08", "10", "rollout-new.jsonl")
	writeTestFile(t, newPath, `{"type":"session_meta","payload":{"id":"codex-moved","cwd":"/srv/project"}}
{"type":"event_msg","payload":{"type":"user_message","message":"new"}}
`)
	events, err := reader.Read(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 1 || events[0].Text != "new" {
		t.Fatalf("deleted cache entry was not replaced: %#v", events)
	}
}

func TestCodexResolveCacheRejectsSymlinkReplacement(t *testing.T) {
	layout := newTestLayout(t)
	request := Request{Backend: BackendCodex, ProviderSessionID: "codex-link", Workdir: "/srv/project"}
	cachedPath := filepath.Join(layout.codex, "2026", "08", "10", "rollout-link.jsonl")
	contents := `{"type":"session_meta","payload":{"id":"codex-link","cwd":"/srv/project"}}
{"type":"event_msg","payload":{"type":"user_message","message":"content"}}
`
	writeTestFile(t, cachedPath, contents)
	reader := newTestReader(t, layout, nil)
	if _, err := reader.Read(context.Background(), request); err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(t.TempDir(), "outside.jsonl")
	writeTestFile(t, outside, contents)
	if err := os.Remove(cachedPath); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, cachedPath); err != nil {
		t.Fatal(err)
	}
	_, err := reader.Read(context.Background(), request)
	if !errors.Is(err, ErrTranscriptNotFound) {
		t.Fatalf("got %v, want transcript not found", err)
	}
}

func TestCodexResolveCacheSupportsConcurrentReaders(t *testing.T) {
	layout := newTestLayout(t)
	request := Request{Backend: BackendCodex, ProviderSessionID: "codex-concurrent", Workdir: "/srv/project"}
	path := filepath.Join(layout.codex, "2026", "08", "10", "rollout-concurrent.jsonl")
	writeTestFile(t, path, `{"type":"session_meta","payload":{"id":"codex-concurrent","cwd":"/srv/project"}}
{"type":"event_msg","payload":{"type":"user_message","message":"ok"}}
`)
	reader := newTestReader(t, layout, nil)
	var wait sync.WaitGroup
	errorsFound := make(chan error, 24)
	for range 24 {
		wait.Add(1)
		go func() {
			defer wait.Done()
			events, err := reader.Read(context.Background(), request)
			if err == nil && (len(events) != 1 || events[0].Text != "ok") {
				err = errors.New("unexpected concurrent read result")
			}
			if err != nil {
				errorsFound <- err
			}
		}()
	}
	wait.Wait()
	close(errorsFound)
	for err := range errorsFound {
		t.Error(err)
	}
}

func TestCodexNegativeCacheStillHonorsCancellation(t *testing.T) {
	layout := newTestLayout(t)
	reader := newTestReader(t, layout, nil)
	request := Request{Backend: BackendCodex, ProviderSessionID: "missing", Workdir: "/srv/project"}
	if _, err := reader.Read(context.Background(), request); !errors.Is(err, ErrTranscriptNotFound) {
		t.Fatalf("prime negative cache: got %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := reader.Read(ctx, request); !errors.Is(err, context.Canceled) {
		t.Fatalf("got %v, want context canceled", err)
	}
}
