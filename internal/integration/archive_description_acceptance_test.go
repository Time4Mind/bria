package integration_test

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"bria/internal/domain"
	"bria/internal/storage"
	"bria/internal/telegramcontroller"
)

func TestArchiveDescriptionReadsExactPersistedPromptsAfterRestart(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "state.json")
	store, err := storage.OpenSessionStore(path)
	if err != nil {
		t.Fatal(err)
	}
	starting, err := domain.NewStartingSessionAt("11111111-1111-4111-9111-111111111111", "archive-preview", "local", domain.ProviderCodex, "/never-context", time.Now().UTC(), domain.SessionLifetimeNever)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.PutStartingIfAbsent(ctx, starting); err != nil {
		t.Fatal(err)
	}
	for _, write := range []func() error{
		func() error {
			return store.AppendCardHistory(ctx, starting.ID(), "👨‍💻 assistant misleading role prefix")
		},
		func() error { return store.SetCardPrompt(ctx, starting.ID(), "m1", "🙋‍♂ raw voice") },
		func() error { return store.SetCardPrompt(ctx, starting.ID(), "m1", "👨‍💻 processed voice") },
		func() error { return store.AppendCardHistory(ctx, starting.ID(), "tool output") },
		func() error {
			return store.SetCardPrompt(ctx, starting.ID(), "m2", "❌ Ошибка препроцессинга\n🙅‍♂ original caption")
		},
	} {
		if err := write(); err != nil {
			t.Fatal(err)
		}
	}
	ready, err := starting.ReadyAt(domain.ProviderBinding{Provider: domain.ProviderCodex, SessionID: "provider", Generation: 1}, time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	closing, err := ready.BeginClose(time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	archived, err := closing.Archive(time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Replace(ctx, starting, archived); err != nil {
		t.Fatal(err)
	}
	reopened, err := storage.OpenSessionStore(path)
	if err != nil {
		t.Fatal(err)
	}
	c, err := telegramcontroller.New(42, 42, "local", staticCreator{session: archived}, reopened, &capturingSubmitter{calls: make(chan submittedTurn, 1)}, discardNotifier{}, telegramcontroller.Options{UIState: reopened})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close(ctx)
	r, err := c.HandleSemanticAction(ctx, telegramcontroller.SemanticAction{Kind: telegramcontroller.SemanticMenuArchive})
	if err != nil || r.Surface == nil {
		t.Fatalf("archive after restart: %+v %v", r, err)
	}
	if !strings.Contains(r.Surface.Text, "· processed voice<br>· original caption") {
		t.Fatalf("persisted prompt preview missing: %q", r.Surface.Text)
	}
	for _, forbidden := range []string{"raw voice", "assistant misleading", "tool output", "/never-context", "Ошибка препроцессинга"} {
		if strings.Contains(r.Surface.Text, forbidden) {
			t.Fatalf("non-user/stale data %q in archive: %q", forbidden, r.Surface.Text)
		}
	}
}
