package sessioncatalog_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"bria/internal/app"
	"bria/internal/claudestore"
	"bria/internal/domain"
	providercodex "bria/internal/provider/codex"
	"bria/internal/sessioncatalog"
	"bria/internal/sessiondiscovery/claudeindex"
	"bria/internal/sessionruntime"
	"bria/internal/storage"
	"bria/internal/telegramcontroller"
)

func TestProviderStoresComposeIntoRestartSafeUnifiedArchive(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 9, 3, 10, 0, 0, 0, time.UTC)
	codexID := "019978f0-6400-7000-8000-000000000051"
	claudeID := "00000000-0000-4000-8000-000000000052"
	codexLister := &onePageCodexLister{page: providercodex.ThreadListPage{Threads: []providercodex.ThreadSummary{{
		ID: codexID, Cwd: "/work/codex", CreatedAt: now, UpdatedAt: now.Add(time.Minute),
	}}}}
	codexSource, err := providercodex.NewDiscoverySource(codexLister, domain.ComputerID("macbook"), providercodex.DiscoveryOptions{})
	if err != nil {
		t.Fatal(err)
	}

	claudeRoot := t.TempDir()
	project := filepath.Join(claudeRoot, "-work-claude")
	if err := os.Mkdir(project, 0o700); err != nil {
		t.Fatal(err)
	}
	transcript := `{"type":"user","userType":"external","promptSource":"sdk","sessionId":"` + claudeID +
		`","cwd":"/work/claude","timestamp":"2026-09-03T10:02:00Z","uuid":"telegram-update:1","parentUuid":"","message":{"role":"user","content":"private"}}` + "\n"
	if err := os.WriteFile(filepath.Join(project, claudeID+".jsonl"), []byte(transcript), 0o600); err != nil {
		t.Fatal(err)
	}
	claudeStore, err := claudestore.NewTranscriptStore(claudeRoot, claudestore.TranscriptStoreOptions{})
	if err != nil {
		t.Fatal(err)
	}
	claudeSource, err := claudeindex.New(claudeStore, domain.ComputerID("macbook"))
	if err != nil {
		t.Fatal(err)
	}

	archivePath := filepath.Join(t.TempDir(), "archive.json")
	archive, err := sessioncatalog.OpenFileStore(archivePath)
	if err != nil {
		t.Fatal(err)
	}
	entries, err := archive.Synchronize(ctx, codexSource, claudeSource)
	if err != nil {
		t.Fatalf("Synchronize(provider stores) error = %v", err)
	}
	if len(entries) != 2 || entries[0].Record.Provider != domain.ProviderClaude || entries[1].Record.Provider != domain.ProviderCodex {
		t.Fatalf("unified archive entries = %#v", entries)
	}
	if entries[0].Record.ProviderSessionID != claudeID || entries[0].Record.Workdir != "/work/claude" ||
		entries[1].Record.ProviderSessionID != codexID || entries[1].Record.Workdir != "/work/codex" {
		t.Fatalf("archive lost exact provider metadata: %#v", entries)
	}

	restarted, err := sessioncatalog.OpenFileStore(archivePath)
	if err != nil {
		t.Fatal(err)
	}
	sessions, err := restarted.ArchivedSessions(ctx)
	if err != nil || len(sessions) != 2 {
		t.Fatalf("ArchivedSessions after restart = (%#v, %v)", sessions, err)
	}
	for index, session := range sessions {
		binding, ok := session.Binding()
		if !ok || binding.Provider != entries[index].Record.Provider || binding.SessionID != entries[index].Record.ProviderSessionID || binding.Generation != 1 {
			t.Fatalf("session %d binding = %#v/%t, entry = %#v", index, binding, ok, entries[index])
		}
	}
	if codexLister.requests != 1 {
		t.Fatalf("Codex thread/list requests = %d, want 1 bounded state-db page", codexLister.requests)
	}

	mainStore, err := storage.OpenSessionStore(filepath.Join(t.TempDir(), "sessions.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := mainStore.ImportArchived(ctx, sessions); err != nil {
		t.Fatalf("ImportArchived() error = %v", err)
	}
	starter := &exactResumeStarter{}
	resumer, err := app.NewArchivedSessionResumer(mainStore, starter, domain.SessionLifetimeNever, func() time.Time {
		return now.Add(2 * time.Hour)
	})
	if err != nil {
		t.Fatal(err)
	}
	controller, err := telegramcontroller.New(
		1, 1, "macbook", unusedCreator{}, mainStore, unusedSubmitter{}, unusedNotifier{},
		telegramcontroller.Options{ArchivedResumer: resumer},
	)
	if err != nil {
		t.Fatal(err)
	}
	defer controller.Close(context.Background())
	target := sessions[0]
	if _, err := controller.ResumeArchived(ctx, target.ID()); err != nil {
		t.Fatalf("controller.ResumeArchived() error = %v", err)
	}
	persisted, err := mainStore.Load(ctx, target.ID())
	if err != nil {
		t.Fatal(err)
	}
	binding, ok := persisted.Binding()
	if !ok || persisted.Status() != domain.SessionReady || binding.SessionID != target.Snapshot().Binding.SessionID || binding.Generation != 2 {
		t.Fatalf("persisted exact resume = %#v", persisted.Snapshot())
	}
	if starter.request.Mode != app.SessionStartResume || starter.request.PriorBinding == nil ||
		starter.request.PriorBinding.SessionID != target.Snapshot().Binding.SessionID || starter.request.Workdir != target.Workdir() {
		t.Fatalf("provider resume request lost discovered binding: %#v", starter.request)
	}
}

type onePageCodexLister struct {
	page     providercodex.ThreadListPage
	requests int
}

func (lister *onePageCodexLister) ListThreads(context.Context, providercodex.ThreadListRequest) (providercodex.ThreadListPage, error) {
	lister.requests++
	return lister.page, nil
}

type exactResumeStarter struct {
	request app.StartSessionRequest
}

func (starter *exactResumeStarter) Start(_ context.Context, request app.StartSessionRequest) (domain.ProviderBinding, error) {
	starter.request = request
	if request.Mode != app.SessionStartResume || request.PriorBinding == nil {
		return domain.ProviderBinding{}, errors.New("not an exact resume")
	}
	return domain.ProviderBinding{
		Provider: request.Provider, SessionID: request.PriorBinding.SessionID,
		Generation: request.PriorBinding.Generation + 1,
	}, nil
}

func (*exactResumeStarter) Abort(context.Context, app.StartSessionRequest, domain.ProviderBinding) error {
	return nil
}

type unusedCreator struct{}

func (unusedCreator) Create(context.Context, app.ConfirmedSessionIntent) (app.CreateSessionResult, error) {
	return app.CreateSessionResult{}, errors.New("unused")
}

type unusedSubmitter struct{}

func (unusedSubmitter) Submit(context.Context, domain.SessionID, string) (sessionruntime.TurnResult, error) {
	return sessionruntime.TurnResult{}, errors.New("unused")
}

type unusedNotifier struct{}

func (unusedNotifier) Notify(context.Context, telegramcontroller.Notification) error { return nil }
