package storage_test

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"bria/internal/domain"
	"bria/internal/storage"
	"bria/internal/telegramstate"
)

type emptyCloseStore interface {
	DeleteEmptyClosing(context.Context, domain.Session) (bool, error)
	HasEmptyCloseEligibility(context.Context, domain.SessionID) (bool, error)
}

func TestEmptyCloseDeletesDurablyAndPrunesSelection(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "state.json")
	store, _ := storage.OpenSessionStore(path)
	seam, ok := any(store).(emptyCloseStore)
	if !ok {
		t.Fatal("empty close storage seam missing")
	}
	first := mustStartingSession(t, "first", "first-intent")
	second := mustStartingSession(t, "second", "second-intent")
	for _, s := range []domain.Session{first, second} {
		if _, _, err := store.PutStartingIfAbsent(ctx, s); err != nil {
			t.Fatal(err)
		}
		if err := store.SetNodeActiveSession(ctx, s.ComputerID(), s.ID()); err != nil {
			t.Fatal(err)
		}
	}
	ready, _ := second.ReadyAt(domain.ProviderBinding{Provider: second.Provider(), SessionID: "provider", Generation: 1}, time.Now().UTC())
	if err := store.Replace(ctx, second, ready); err != nil {
		t.Fatal(err)
	}
	if deleted, err := seam.DeleteEmptyClosing(ctx, ready); err == nil || deleted {
		t.Fatal("deleted before closing")
	}
	closing, _ := ready.BeginClose(time.Now().UTC())
	if err := store.Replace(ctx, ready, closing); err != nil {
		t.Fatal(err)
	}
	if deleted, err := seam.DeleteEmptyClosing(ctx, closing); err != nil || !deleted {
		t.Fatalf("delete=%t err=%v", deleted, err)
	}
	reopened, err := storage.OpenSessionStore(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := reopened.Load(ctx, second.ID()); !errors.Is(err, storage.ErrSessionNotFound) {
		t.Fatalf("load deleted=%v", err)
	}
	state, err := reopened.LoadTelegramUI(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := state.Cards[second.ID()]; ok {
		t.Fatal("deleted card retained")
	}
	if state.ActiveSession != first.ID() || len(state.RecentSessions[first.ComputerID()]) != 1 {
		t.Fatalf("selection not restored: %#v", state)
	}
}

func TestEmptyCloseRetainsAnyRequestEvenAfterHistoryTrim(t *testing.T) {
	for _, prompt := range []string{"🙋‍♂ pending", "🙅‍♂ failed", "👨‍💻 accepted"} {
		t.Run(prompt, func(t *testing.T) {
			ctx := context.Background()
			store, _ := storage.OpenSessionStore(filepath.Join(t.TempDir(), "state.json"))
			seam, ok := any(store).(emptyCloseStore)
			if !ok {
				t.Fatal("empty close storage seam missing")
			}
			starting := mustStartingSession(t, "request", "intent")
			store.PutStartingIfAbsent(ctx, starting)
			if err := store.SetCardPrompt(ctx, starting.ID(), "update:1", prompt); err != nil {
				t.Fatal(err)
			}
			for i := 0; i < 513; i++ {
				if err := store.AppendCardHistory(ctx, starting.ID(), "output"); err != nil {
					t.Fatal(err)
				}
			}
			ready, _ := starting.ReadyAt(domain.ProviderBinding{Provider: starting.Provider(), SessionID: "provider", Generation: 1}, time.Now().UTC())
			store.Replace(ctx, starting, ready)
			closing, _ := ready.BeginClose(time.Now().UTC())
			store.Replace(ctx, ready, closing)
			if deleted, err := seam.DeleteEmptyClosing(ctx, closing); err != nil || deleted {
				t.Fatalf("request deleted=%t err=%v", deleted, err)
			}
		})
	}
}

func TestEmptyCloseUnknownEvidenceRetainedAndLatePromptRejected(t *testing.T) {
	ctx := context.Background()
	store, _ := storage.OpenSessionStore(filepath.Join(t.TempDir(), "state.json"))
	starting := mustStartingSession(t, "unknown", "unknown-intent")
	store.PutStartingIfAbsent(ctx, starting)
	if err := store.UpdateTelegramUI(ctx, func(state *telegramstate.State) error {
		card := state.Cards[starting.ID()]
		card.EmptyCloseEligible = false
		state.Cards[starting.ID()] = card
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	ready, _ := starting.ReadyAt(domain.ProviderBinding{Provider: starting.Provider(), SessionID: "provider", Generation: 1}, time.Now().UTC())
	store.Replace(ctx, starting, ready)
	closing, _ := ready.BeginClose(time.Now().UTC())
	store.Replace(ctx, ready, closing)
	if deleted, err := store.DeleteEmptyClosing(ctx, closing); err != nil || deleted {
		t.Fatalf("unknown evidence deleted=%t err=%v", deleted, err)
	}
	if err := store.SetCardPrompt(ctx, closing.ID(), "late", "late request"); err == nil {
		t.Fatal("accepted request into closing session")
	}
}
