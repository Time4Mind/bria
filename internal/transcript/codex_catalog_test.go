package transcript

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"
)

func TestCodexCatalogRestoresLookupBeforeBackgroundRefresh(t *testing.T) {
	layout := newTestLayout(t)
	catalogPath := filepath.Join(t.TempDir(), "catalog.json")
	transcriptPath := filepath.Join(layout.codex, "2026", "08", "21", "rollout-known.jsonl")
	writeTestFile(t, transcriptPath, `{"type":"session_meta","payload":{"id":"known-session","cwd":"/srv/project"}}
{"type":"event_msg","payload":{"type":"user_message","message":"known"}}
`)
	reader := newTestReader(t, layout, func(config *Config) { config.CodexCatalogPath = catalogPath })
	if _, err := reader.Read(context.Background(), Request{
		Backend: BackendCodex, ProviderSessionID: "known-session", Workdir: "/srv/project",
	}); err != nil {
		t.Fatal(err)
	}
	reader.codexIndexMu.Lock()
	snapshot := reader.codexIndex
	reader.codexIndexMu.Unlock()
	if err := reader.saveCodexCatalog(snapshot); err != nil {
		t.Fatal(err)
	}

	restored := newTestReader(t, layout, func(config *Config) { config.CodexCatalogPath = catalogPath })
	restored.scanCodex = func(context.Context) (*codexIndexSnapshot, error) {
		return nil, errors.New("background refresh must not block catalog lookup")
	}
	events, err := restored.Read(context.Background(), Request{
		Backend: BackendCodex, ProviderSessionID: "known-session", Workdir: "/srv/project",
	})
	if err != nil || len(events) != 1 || events[0].Text != "known" {
		t.Fatalf("catalog events=%#v err=%v", events, err)
	}
}

func TestCodexIndexOwnerOutlivesCanceledWaiter(t *testing.T) {
	layout := newTestLayout(t)
	reader := newTestReader(t, layout, nil)
	started := make(chan struct{})
	release := make(chan struct{})
	reader.scanCodex = func(context.Context) (*codexIndexSnapshot, error) {
		close(started)
		<-release
		return &codexIndexSnapshot{
			refreshedAt: time.Now(), byWorkdir: map[string][]codexIndexedCandidate{},
			bySession: map[string][]codexIndexedCandidate{}, byPath: map[string]codexIndexedCandidate{},
		}, nil
	}
	ctx, cancel := context.WithCancel(context.Background())
	result := make(chan error, 1)
	go func() {
		_, _, err := reader.loadCodexIndex(ctx, false)
		result <- err
	}()
	<-started
	cancel()
	if err := <-result; !errors.Is(err, context.Canceled) {
		t.Fatalf("waiter error=%v", err)
	}
	close(release)
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		reader.codexIndexMu.Lock()
		ready := reader.codexIndex != nil && reader.codexFlight == nil
		reader.codexIndexMu.Unlock()
		if ready {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("index owner was canceled with waiter")
}
