package integration_test

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"bria/internal/app"
	"bria/internal/domain"
	"bria/internal/storage"
	"bria/internal/telegramstate"
)

type failedStartCloseStarter struct{ calls int }

func (s *failedStartCloseStarter) Start(context.Context, app.StartSessionRequest) (domain.ProviderBinding, error) {
	s.calls++
	return domain.ProviderBinding{}, nil
}
func (s *failedStartCloseStarter) Abort(context.Context, app.StartSessionRequest, domain.ProviderBinding) error {
	s.calls++
	return nil
}

func TestCloseEmptyFailedStartPersistsDeletionWithoutProviderMutation(t *testing.T) {
	for _, mode := range []string{"empty", "content", "unproven"} {
		t.Run(mode, func(t *testing.T) {
			retain := mode != "empty"
			ctx := context.Background()
			path := filepath.Join(t.TempDir(), "sessions.json")
			store, err := storage.OpenSessionStore(path)
			if err != nil {
				t.Fatal(err)
			}
			now := time.Now().UTC()
			starting, err := domain.NewStartingSessionAt("failed-start", "failed-intent", "local", domain.ProviderClaude, "/workspace", now, domain.SessionLifetime12Hours)
			if err != nil {
				t.Fatal(err)
			}
			if _, _, err := store.PutStartingIfAbsent(ctx, starting); err != nil {
				t.Fatal(err)
			}
			failed, err := starting.AwaitRecoveryAt(now.Add(time.Second))
			if err != nil {
				t.Fatal(err)
			}
			if err := store.Replace(ctx, starting, failed); err != nil {
				t.Fatal(err)
			}
			if mode == "content" {
				if err := store.AppendCardHistory(ctx, failed.ID(), "retained user content"); err != nil {
					t.Fatal(err)
				}
			}
			if mode == "unproven" {
				if err := store.UpdateTelegramUI(ctx, func(ui *telegramstate.State) error {
					card := ui.Cards[failed.ID()]
					card.EmptyCloseEligible = false
					ui.Cards[failed.ID()] = card
					return nil
				}); err != nil {
					t.Fatal(err)
				}
			}
			if err := store.SetNodeActiveSession(ctx, failed.ComputerID(), failed.ID()); err != nil {
				t.Fatal(err)
			}
			store, err = storage.OpenSessionStore(path)
			if err != nil {
				t.Fatal(err)
			}
			starter := &failedStartCloseStarter{}
			closer, err := app.NewSessionCloser(store, starter, func() time.Time { return now.Add(2 * time.Second) })
			if err != nil {
				t.Fatal(err)
			}
			result, closeErr := closer.Close(ctx, failed.ID())
			if retain {
				if closeErr == nil || result.Deleted {
					t.Fatalf("nonempty result=%#v err=%v", result, closeErr)
				}
			} else if closeErr != nil || !result.Deleted {
				t.Fatalf("empty result=%#v err=%v", result, closeErr)
			}
			if starter.calls != 0 {
				t.Fatalf("provider calls=%d", starter.calls)
			}
			reopened, err := storage.OpenSessionStore(path)
			if err != nil {
				t.Fatal(err)
			}
			sessions, err := reopened.List(ctx)
			if err != nil {
				t.Fatal(err)
			}
			if retain {
				if len(sessions) != 1 || !sessions[0].Equal(failed) {
					t.Fatalf("retained sessions=%v", sessions)
				}
			} else {
				if len(sessions) != 0 {
					t.Fatalf("deleted sessions=%v", sessions)
				}
				ui, err := reopened.LoadTelegramUI(ctx)
				if err != nil {
					t.Fatal(err)
				}
				if _, exists := ui.Cards[failed.ID()]; exists {
					t.Fatal("deleted card retained")
				}
				if ui.ActiveSession == failed.ID() || ui.ActiveSessions[failed.ComputerID()] == failed.ID() || len(ui.RecentSessions[failed.ComputerID()]) != 0 {
					t.Fatal("deleted selection retained")
				}
				if _, err := closer.Close(ctx, failed.ID()); err == nil {
					t.Fatal("generic absence treated as successful close")
				}
			}
		})
	}
}
