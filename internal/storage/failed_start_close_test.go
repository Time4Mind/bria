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

func TestFailedStartDeletionRejectsBoundAndConcurrentReplacement(t *testing.T) {
	ctx := context.Background()
	store, err := storage.OpenSessionStore(filepath.Join(t.TempDir(), "state.json"))
	if err != nil {
		t.Fatal(err)
	}
	starting := mustStartingSession(t, "failed", "failed-intent")
	if _, _, err := store.PutStartingIfAbsent(ctx, starting); err != nil {
		t.Fatal(err)
	}
	failed, err := starting.AwaitRecoveryAt(time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Replace(ctx, starting, failed); err != nil {
		t.Fatal(err)
	}
	ready, err := failed.Recovered(domain.ProviderBinding{Provider: failed.Provider(), SessionID: "recovered", Generation: 1}, time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Replace(ctx, failed, ready); err != nil {
		t.Fatal(err)
	}
	if deleted, err := store.DeleteEmptyFailedStart(ctx, failed); deleted || !errors.Is(err, storage.ErrCompareAndSwapConflict) {
		t.Fatalf("stale delete=%t err=%v", deleted, err)
	}
	if deleted, err := store.DeleteEmptyFailedStart(ctx, ready); deleted || err == nil {
		t.Fatalf("bound delete=%t err=%v", deleted, err)
	}
	actual, err := store.Load(ctx, ready.ID())
	if err != nil || !actual.Equal(ready) {
		t.Fatalf("recovered session changed: %v", err)
	}
}
