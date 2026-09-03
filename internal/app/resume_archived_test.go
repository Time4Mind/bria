package app_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"bria/internal/app"
	"bria/internal/domain"
)

func TestResumeArchivedStartsExactOriginalProviderSessionAndPersistsReady(t *testing.T) {
	now := time.Date(2026, 9, 3, 8, 0, 0, 0, time.UTC)
	archived := archivedSession(t, now.Add(-time.Hour))
	store := &lifecycleStore{session: archived}
	prior, _ := archived.Binding()
	resumedBinding := prior
	resumedBinding.Generation++
	starter := &lifecycleStarter{start: func(request app.StartSessionRequest) (domain.ProviderBinding, error) {
		if request.Mode != app.SessionStartResume || request.PriorBinding == nil || *request.PriorBinding != prior {
			t.Fatalf("resume request = %#v, want exact prior binding %#v", request, prior)
		}
		return resumedBinding, nil
	}}
	resumer, err := app.NewArchivedSessionResumer(store, starter, domain.SessionLifetime6Hours, func() time.Time { return now })
	if err != nil {
		t.Fatalf("NewArchivedSessionResumer() error = %v", err)
	}

	got, err := resumer.Resume(context.Background(), archived.ID())
	if err != nil {
		t.Fatalf("Resume() error = %v", err)
	}
	if got.Status() != domain.SessionReady || !got.Equal(store.session) {
		t.Fatalf("resumed session = %#v, persisted %#v", got.Snapshot(), store.session.Snapshot())
	}
	if binding, ok := got.Binding(); !ok || binding != resumedBinding {
		t.Fatalf("resumed binding = %#v, %t, want %#v", binding, ok, resumedBinding)
	}
	if resumedAt, ok := got.LastResumedAt(); !ok || !resumedAt.Equal(now) {
		t.Fatalf("last resumed = %v, %t, want %v", resumedAt, ok, now)
	}
	if deadline, ok := got.Deadline(); !ok || !deadline.Equal(now.Add(6*time.Hour)) {
		t.Fatalf("deadline = %v, %t", deadline, ok)
	}
}

func TestResumeArchivedFailureLeavesArchiveBitForBitUnchanged(t *testing.T) {
	now := time.Date(2026, 9, 3, 8, 0, 0, 0, time.UTC)
	archived := archivedSession(t, now.Add(-time.Hour))
	store := &lifecycleStore{session: archived}
	startErr := errors.New("provider refused exact resume")
	starter := &lifecycleStarter{start: func(app.StartSessionRequest) (domain.ProviderBinding, error) {
		return domain.ProviderBinding{}, startErr
	}}
	resumer, err := app.NewArchivedSessionResumer(store, starter, domain.SessionLifetimeNever, func() time.Time { return now })
	if err != nil {
		t.Fatalf("NewArchivedSessionResumer() error = %v", err)
	}

	if _, err := resumer.Resume(context.Background(), archived.ID()); !errors.Is(err, startErr) {
		t.Fatalf("Resume() error = %v, want %v", err, startErr)
	}
	if !store.session.Equal(archived) || store.replaceCalls != 0 {
		t.Fatalf("failed resume mutated archive: %#v", store.session.Snapshot())
	}
}

func TestResumeArchivedRejectsReplacementAndAbortsItWithoutMutatingArchive(t *testing.T) {
	now := time.Date(2026, 9, 3, 8, 0, 0, 0, time.UTC)
	archived := archivedSession(t, now.Add(-time.Hour))
	store := &lifecycleStore{session: archived}
	replacement := domain.ProviderBinding{Provider: domain.ProviderCodex, SessionID: "replacement", Generation: 2}
	starter := &lifecycleStarter{start: func(app.StartSessionRequest) (domain.ProviderBinding, error) { return replacement, nil }}
	resumer, err := app.NewArchivedSessionResumer(store, starter, domain.SessionLifetimeNever, func() time.Time { return now })
	if err != nil {
		t.Fatalf("NewArchivedSessionResumer() error = %v", err)
	}

	if _, err := resumer.Resume(context.Background(), archived.ID()); err == nil {
		t.Fatal("Resume() accepted a replacement provider session")
	}
	if starter.abortCalls != 1 || !store.session.Equal(archived) || store.replaceCalls != 0 {
		t.Fatalf("replacement cleanup = aborts %d, replaces %d, session %#v", starter.abortCalls, store.replaceCalls, store.session.Snapshot())
	}
}

func archivedSession(t *testing.T, createdAt time.Time) domain.Session {
	t.Helper()
	session, err := domain.NewStartingSessionAt("logical", "intent", "local", domain.ProviderCodex, "/work", createdAt, domain.SessionLifetimeNever)
	if err != nil {
		t.Fatal(err)
	}
	session, err = session.ReadyAt(domain.ProviderBinding{Provider: domain.ProviderCodex, SessionID: "original", Generation: 1}, createdAt.Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	session, err = session.BeginClose(createdAt.Add(2 * time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	session, err = session.Archive(createdAt.Add(3 * time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	return session
}

type lifecycleStore struct {
	session      domain.Session
	replaceCalls int
	replaceErr   error
	statuses     []domain.SessionStatus
}

func (store *lifecycleStore) Load(context.Context, domain.SessionID) (domain.Session, error) {
	return store.session, nil
}

func (store *lifecycleStore) Replace(_ context.Context, expected, next domain.Session) error {
	store.replaceCalls++
	if store.replaceErr != nil {
		return store.replaceErr
	}
	if !store.session.Equal(expected) {
		return errors.New("replace conflict")
	}
	store.session = next
	store.statuses = append(store.statuses, next.Status())
	return nil
}

type lifecycleStarter struct {
	start            func(app.StartSessionRequest) (domain.ProviderBinding, error)
	abortCalls       int
	abortErr         error
	lastAbortRequest app.StartSessionRequest
	lastAbortBinding domain.ProviderBinding
}

func (starter *lifecycleStarter) Start(_ context.Context, request app.StartSessionRequest) (domain.ProviderBinding, error) {
	return starter.start(request)
}

func (starter *lifecycleStarter) Abort(_ context.Context, request app.StartSessionRequest, binding domain.ProviderBinding) error {
	starter.abortCalls++
	starter.lastAbortRequest = request
	starter.lastAbortBinding = binding
	return starter.abortErr
}
