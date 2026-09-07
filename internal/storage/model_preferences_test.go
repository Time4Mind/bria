package storage_test

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"bria/internal/domain"
	"bria/internal/storage"
)

type modelPreferencesStore interface {
	SetModelPreferences(context.Context, domain.SessionID, string, string) error
	ProviderModelPreferences(context.Context, domain.SessionID) (string, string, error)
}

func TestModelPreferencesPersistWithoutChangingLifecycleAndGuardCAS(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "state.json")
	store, err := storage.OpenSessionStore(path)
	if err != nil {
		t.Fatal(err)
	}
	prefs, ok := any(store).(modelPreferencesStore)
	if !ok {
		t.Fatal("durable model preferences API missing")
	}
	starting := mustStartingSession(t, "model", "model-intent")
	if _, _, err := store.PutStartingIfAbsent(ctx, starting); err != nil {
		t.Fatal(err)
	}
	if err := prefs.SetModelPreferences(ctx, starting.ID(), "gpt-5", "high"); err == nil {
		t.Fatal("starting session accepted preference change")
	}
	ready, err := starting.ReadyAt(domain.ProviderBinding{Provider: starting.Provider(), SessionID: "provider", Generation: 1}, time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Replace(ctx, starting, ready); err != nil {
		t.Fatal(err)
	}
	if err := prefs.SetModelPreferences(ctx, ready.ID(), "gpt-5", "high"); err != nil {
		t.Fatal(err)
	}
	reopened, err := storage.OpenSessionStore(path)
	if err != nil {
		t.Fatal(err)
	}
	model, effort, err := any(reopened).(modelPreferencesStore).ProviderModelPreferences(ctx, ready.ID())
	if err != nil || model != "gpt-5" || effort != "high" {
		t.Fatalf("preferences = %q %q, %v", model, effort, err)
	}
	stored, err := reopened.Load(ctx, ready.ID())
	if err != nil {
		t.Fatal(err)
	}
	if stored.Status() != ready.Status() || !stored.StateChangedAt().Equal(ready.StateChangedAt()) {
		t.Fatal("preferences changed lifecycle")
	}
	running, err := ready.StartWork(time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	if err := reopened.Replace(ctx, ready, running); !errors.Is(err, storage.ErrCompareAndSwapConflict) {
		t.Fatalf("stale CAS = %v", err)
	}
	running, err = stored.StartWork(time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	if err := reopened.Replace(ctx, stored, running); err != nil {
		t.Fatal(err)
	}
	if err := prefs.SetModelPreferences(ctx, ready.ID(), "other", "low"); err == nil {
		t.Fatal("running session accepted preference change")
	}
	closing, err := running.CloseAfterWork(time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	if err := reopened.Replace(ctx, running, closing); err != nil {
		t.Fatal(err)
	}
	if err := prefs.SetModelPreferences(ctx, ready.ID(), "other", "low"); err == nil {
		t.Fatal("closing session accepted preference change")
	}
	if _, _, err := prefs.ProviderModelPreferences(ctx, "missing"); !errors.Is(err, storage.ErrSessionNotFound) {
		t.Fatalf("missing session = %v", err)
	}
	canceled, cancel := context.WithCancel(ctx)
	cancel()
	if err := prefs.SetModelPreferences(canceled, ready.ID(), "other", "low"); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation = %v", err)
	}
}
