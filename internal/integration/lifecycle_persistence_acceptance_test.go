package integration_test

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"bria/internal/domain"
	"bria/internal/storage"
)

// TestLifecycleStateMachineRoundTripsEveryDurableState verifies the physical
// persistence boundary for the complete lifecycle vocabulary. It deliberately
// does not claim that Telegram already exposes every transition as a product
// use case; exact restart recovery is exercised separately through app/runtime.
func TestLifecycleStateMachineRoundTripsEveryDurableState(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "sessions.json")
	timestamp := time.Date(2026, 9, 2, 9, 0, 0, 0, time.UTC)
	current, err := domain.NewStartingSessionAt(
		"lifecycle-session", "lifecycle-intent", "computer-1", domain.ProviderCodex,
		"/workspace/bria", timestamp, domain.SessionLifetime12Hours,
	)
	if err != nil {
		t.Fatalf("create starting session: %v", err)
	}
	store, err := storage.OpenSessionStore(path)
	if err != nil {
		t.Fatalf("open lifecycle store: %v", err)
	}
	if stored, inserted, err := store.PutStartingIfAbsent(ctx, current); err != nil || !inserted || !stored.Equal(current) {
		t.Fatalf("persist starting session = (%#v, %v, %v)", stored.Snapshot(), inserted, err)
	}
	assertPhysicalLifecycleState(t, path, current, domain.SessionStarting)

	nextTime := func() time.Time {
		timestamp = timestamp.Add(time.Minute)
		return timestamp
	}
	binding1 := domain.ProviderBinding{Provider: domain.ProviderCodex, SessionID: "codex-thread-original", Generation: 1}
	next, err := current.ReadyAt(binding1, nextTime())
	current = replaceAndReopenLifecycle(t, path, current, next, err, domain.SessionReady)

	next, err = current.StartWork(nextTime())
	current = replaceAndReopenLifecycle(t, path, current, next, err, domain.SessionRunning)
	next, err = current.BeginStop(nextTime())
	current = replaceAndReopenLifecycle(t, path, current, next, err, domain.SessionStopping)
	next, err = current.CloseAfterWork(nextTime())
	current = replaceAndReopenLifecycle(t, path, current, next, err, domain.SessionClosingAfterWork)
	next, err = current.BeginClose(nextTime())
	current = replaceAndReopenLifecycle(t, path, current, next, err, domain.SessionClosing)
	next, err = current.Archive(nextTime())
	current = replaceAndReopenLifecycle(t, path, current, next, err, domain.SessionArchived)

	next, err = current.BeginResume(nextTime())
	current = replaceAndReopenLifecycle(t, path, current, next, err, domain.SessionResuming)
	next, err = current.FailResume(nextTime())
	current = replaceAndReopenLifecycle(t, path, current, next, err, domain.SessionResumeFailed)
	next, err = current.ReturnToArchive(nextTime())
	current = replaceAndReopenLifecycle(t, path, current, next, err, domain.SessionArchived)
	next, err = current.BeginResume(nextTime())
	current = replaceAndReopenLifecycle(t, path, current, next, err, domain.SessionResuming)

	resumedAt := nextTime()
	binding2 := binding1
	binding2.Generation = 2
	next, err = current.ResumeReady(binding2, resumedAt, domain.SessionLifetime24Hours)
	current = replaceAndReopenLifecycle(t, path, current, next, err, domain.SessionReady)
	if got, ok := current.LastResumedAt(); !ok || !got.Equal(resumedAt) {
		t.Fatalf("durable last resumed at = (%s, %v), want %s", got, ok, resumedAt)
	}
	if got, ok := current.Deadline(); !ok || !got.Equal(resumedAt.Add(24*time.Hour)) {
		t.Fatalf("durable resumed deadline = (%s, %v), want %s", got, ok, resumedAt.Add(24*time.Hour))
	}

	next, err = current.AwaitRecoveryAt(nextTime())
	current = replaceAndReopenLifecycle(t, path, current, next, err, domain.SessionAwaitingRecovery)
	if target, ok := current.RecoveryTarget(); !ok || target != domain.SessionReady {
		t.Fatalf("durable recovery target = (%q, %v), want ready", target, ok)
	}
	binding3 := binding2
	binding3.Generation = 3
	next, err = current.Recovered(binding3, nextTime())
	current = replaceAndReopenLifecycle(t, path, current, next, err, domain.SessionReady)
	if got, ok := current.Binding(); !ok || got != binding3 {
		t.Fatalf("durable recovered binding = (%#v, %v), want %#v", got, ok, binding3)
	}
}

func replaceAndReopenLifecycle(
	t *testing.T,
	path string,
	current domain.Session,
	next domain.Session,
	transitionErr error,
	wantStatus domain.SessionStatus,
) domain.Session {
	t.Helper()
	if transitionErr != nil {
		t.Fatalf("build transition %q -> %q: %v", current.Status(), wantStatus, transitionErr)
	}
	store, err := storage.OpenSessionStore(path)
	if err != nil {
		t.Fatalf("reopen before transition %q: %v", wantStatus, err)
	}
	if err := store.Replace(context.Background(), current, next); err != nil {
		t.Fatalf("persist transition %q -> %q: %v", current.Status(), wantStatus, err)
	}
	return assertPhysicalLifecycleState(t, path, next, wantStatus)
}

func assertPhysicalLifecycleState(
	t *testing.T,
	path string,
	want domain.Session,
	wantStatus domain.SessionStatus,
) domain.Session {
	t.Helper()
	reopened, err := storage.OpenSessionStore(path)
	if err != nil {
		t.Fatalf("reopen lifecycle store at %q: %v", wantStatus, err)
	}
	got, err := reopened.Load(context.Background(), want.ID())
	if err != nil {
		t.Fatalf("load lifecycle state %q: %v", wantStatus, err)
	}
	if got.Status() != wantStatus || !got.Equal(want) {
		t.Fatalf("physical lifecycle state = %#v, want %#v", got.Snapshot(), want.Snapshot())
	}
	return got
}
