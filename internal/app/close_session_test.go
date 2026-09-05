package app_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"bria/internal/app"
	"bria/internal/domain"
)

func TestCloseReadyConfirmsExactProcessExitBeforeArchiving(t *testing.T) {
	now := time.Date(2026, 9, 3, 9, 0, 0, 0, time.UTC)
	ready := readySession(t, now.Add(-time.Hour))
	store := &lifecycleStore{session: ready}
	starter := &lifecycleStarter{}
	closer, err := app.NewSessionCloser(store, starter, func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}

	result, err := closer.Close(context.Background(), ready.ID())
	if err != nil {
		t.Fatalf("Close() error = %v", err)
	}
	if result.Scheduled || result.Session.Status() != domain.SessionArchived || !store.session.Equal(result.Session) {
		t.Fatalf("close result = %#v, persisted %#v", result, store.session.Snapshot())
	}
	if starter.abortCalls != 1 || starter.lastAbortRequest.Mode != app.SessionStartResume {
		t.Fatalf("abort calls = %d, request = %#v", starter.abortCalls, starter.lastAbortRequest)
	}
	prior, _ := ready.Binding()
	if starter.lastAbortBinding != prior || starter.lastAbortRequest.PriorBinding == nil || *starter.lastAbortRequest.PriorBinding != prior {
		t.Fatalf("abort did not bind exact process: %#v / %#v", starter.lastAbortRequest, starter.lastAbortBinding)
	}
	if got, want := store.statuses, []domain.SessionStatus{domain.SessionClosing, domain.SessionArchived}; !equalStatuses(got, want) {
		t.Fatalf("persisted statuses = %v, want %v", got, want)
	}
}

func TestCloseRunningSchedulesArchiveWithoutInterruptingWork(t *testing.T) {
	now := time.Date(2026, 9, 3, 9, 0, 0, 0, time.UTC)
	running, err := readySession(t, now.Add(-time.Hour)).StartWork(now.Add(-time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	store := &lifecycleStore{session: running}
	starter := &lifecycleStarter{}
	closer, _ := app.NewSessionCloser(store, starter, func() time.Time { return now })

	result, err := closer.Close(context.Background(), running.ID())
	if err != nil {
		t.Fatalf("Close() error = %v", err)
	}
	if !result.Scheduled || result.Session.Status() != domain.SessionClosingAfterWork || starter.abortCalls != 0 {
		t.Fatalf("scheduled close = %#v, aborts %d", result, starter.abortCalls)
	}
}

func TestCloseAbortFailurePersistsRecoveryTargetAndDoesNotArchive(t *testing.T) {
	now := time.Date(2026, 9, 3, 9, 0, 0, 0, time.UTC)
	ready := readySession(t, now.Add(-time.Hour))
	store := &lifecycleStore{session: ready}
	abortErr := errors.New("exit unconfirmed")
	starter := &lifecycleStarter{abortErr: abortErr}
	closer, _ := app.NewSessionCloser(store, starter, func() time.Time { return now })

	result, err := closer.Close(context.Background(), ready.ID())
	if !errors.Is(err, abortErr) {
		t.Fatalf("Close() error = %v, want %v", err, abortErr)
	}
	if result.Session.Status() != domain.SessionAwaitingRecovery || store.session.Status() != domain.SessionAwaitingRecovery {
		t.Fatalf("failed close state = %#v / %#v", result.Session.Snapshot(), store.session.Snapshot())
	}
	if target, ok := store.session.RecoveryTarget(); !ok || target != domain.SessionClosing {
		t.Fatalf("recovery target = %q, %t", target, ok)
	}
}

func TestCloseAwaitingRecoveryConfirmsExactProcessExitBeforeArchiving(t *testing.T) {
	now := time.Date(2026, 9, 5, 15, 46, 0, 0, time.UTC)
	ready := readySession(t, now.Add(-time.Hour))
	awaiting, err := ready.AwaitRecoveryAt(now.Add(-time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	store := &lifecycleStore{session: awaiting}
	starter := &lifecycleStarter{}
	closer, _ := app.NewSessionCloser(store, starter, func() time.Time { return now })

	result, err := closer.Close(context.Background(), awaiting.ID())
	if err != nil {
		t.Fatalf("Close() error = %v", err)
	}
	if result.Session.Status() != domain.SessionArchived || starter.abortCalls != 1 {
		t.Fatalf("close awaiting recovery = %#v, aborts %d", result, starter.abortCalls)
	}
	prior, _ := awaiting.Binding()
	if starter.lastAbortBinding != prior || starter.lastAbortRequest.PriorBinding == nil || *starter.lastAbortRequest.PriorBinding != prior {
		t.Fatalf("abort did not target exact recovering process: %#v / %#v", starter.lastAbortRequest, starter.lastAbortBinding)
	}
	if got, want := store.statuses, []domain.SessionStatus{domain.SessionClosing, domain.SessionArchived}; !equalStatuses(got, want) {
		t.Fatalf("persisted statuses = %v, want %v", got, want)
	}
}

func TestCloseBindinglessAwaitingRecoveryArchivesWithoutProviderAbort(t *testing.T) {
	now := time.Date(2026, 9, 5, 15, 46, 0, 0, time.UTC)
	starting, err := domain.NewStartingSessionAt("logical-start-failure", "intent-start-failure", "local", domain.ProviderClaude, "/work", now.Add(-time.Hour), domain.SessionLifetimeNever)
	if err != nil {
		t.Fatal(err)
	}
	awaiting, err := starting.AwaitRecoveryAt(now.Add(-time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	store := &lifecycleStore{session: awaiting}
	starter := &lifecycleStarter{}
	closer, _ := app.NewSessionCloser(store, starter, func() time.Time { return now })

	result, err := closer.Close(context.Background(), awaiting.ID())
	if err != nil {
		t.Fatalf("Close() error = %v", err)
	}
	if result.Session.Status() != domain.SessionArchived || starter.abortCalls != 0 {
		t.Fatalf("binding-less close = %#v, aborts %d", result, starter.abortCalls)
	}
}

func readySession(t *testing.T, createdAt time.Time) domain.Session {
	t.Helper()
	session, err := domain.NewStartingSessionAt("logical-close", "intent-close", "local", domain.ProviderClaude, "/work", createdAt, domain.SessionLifetimeNever)
	if err != nil {
		t.Fatal(err)
	}
	session, err = session.ReadyAt(domain.ProviderBinding{Provider: domain.ProviderClaude, SessionID: "original-close", Generation: 3}, createdAt.Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	return session
}

func equalStatuses(left, right []domain.SessionStatus) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}
