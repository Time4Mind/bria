package app

import (
	"context"
	"errors"
	"testing"

	"bria/internal/domain"
)

func TestRecoverPersistedSessionsResumesExactReadyBindingAndRetriesAwaiting(t *testing.T) {
	ready, err := domain.NewStartingSession("ready", "intent-ready", "local", domain.ProviderClaude, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	ready, err = ready.Ready(domain.ProviderBinding{Provider: domain.ProviderClaude, SessionID: "old", Generation: 4})
	if err != nil {
		t.Fatal(err)
	}
	awaiting, err := domain.NewStartingSession("awaiting", "intent-awaiting", "local", domain.ProviderCodex, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	awaiting, err = awaiting.AwaitRecovery()
	if err != nil {
		t.Fatal(err)
	}
	store := &recoveryStore{sessions: []domain.Session{ready, awaiting}}
	starter := &recoveryStarter{}
	result, err := RecoverPersistedSessionsForComputer(context.Background(), "local", store, starter)
	if err != nil {
		t.Fatal(err)
	}
	if result.Recovered != 2 || result.Awaiting != 0 {
		t.Fatalf("result = %+v", result)
	}
	if got, want := starter.requests[0].Mode, SessionStartResume; got != want {
		t.Fatalf("ready recovery mode = %q, want %q", got, want)
	}
	if got := starter.requests[0].PriorBinding; got == nil || got.SessionID != "old" || got.Generation != 4 {
		t.Fatalf("ready recovery prior binding = %+v, want exact old binding", got)
	}
	if got, want := starter.requests[1].Mode, SessionStartNew; got != want {
		t.Fatalf("binding-less recovery mode = %q, want %q", got, want)
	}
}

func TestRecoverPersistedSessionsPersistsFailedStart(t *testing.T) {
	session, err := domain.NewStartingSession("s", "intent", "local", domain.ProviderClaude, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	session, err = session.Ready(domain.ProviderBinding{Provider: domain.ProviderClaude, SessionID: "old", Generation: 1})
	if err != nil {
		t.Fatal(err)
	}
	store := &recoveryStore{sessions: []domain.Session{session}}
	starter := &recoveryStarter{err: errors.New("auth unavailable")}
	result, err := RecoverPersistedSessionsForComputer(context.Background(), "local", store, starter)
	if err != nil || result.Awaiting != 1 {
		t.Fatalf("result=%+v err=%v", result, err)
	}
	if store.sessions[0].Status() != domain.SessionAwaitingRecovery {
		t.Fatalf("status=%q", store.sessions[0].Status())
	}
	if binding, ok := store.sessions[0].Binding(); !ok || binding.SessionID != "old" || binding.Generation != 1 {
		t.Fatalf("failed recovery binding = (%+v, %t), want exact prior binding", binding, ok)
	}
}

func TestRecoverPersistedSessionsRejectsReplacementProviderSession(t *testing.T) {
	session, err := domain.NewStartingSession("s", "intent", "local", domain.ProviderCodex, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	prior := domain.ProviderBinding{Provider: domain.ProviderCodex, SessionID: "original-thread", Generation: 7}
	session, err = session.Ready(prior)
	if err != nil {
		t.Fatal(err)
	}
	store := &recoveryStore{sessions: []domain.Session{session}}
	starter := &recoveryStarter{replacementSessionID: "new-thread"}

	result, err := RecoverPersistedSessionsForComputer(context.Background(), "local", store, starter)
	if err != nil {
		t.Fatal(err)
	}
	if result.Recovered != 0 || result.Awaiting != 1 {
		t.Fatalf("result = %+v, want one awaiting recovery", result)
	}
	got := store.sessions[0]
	if got.Status() != domain.SessionAwaitingRecovery {
		t.Fatalf("status = %q, want %q", got.Status(), domain.SessionAwaitingRecovery)
	}
	if binding, ok := got.Binding(); !ok || binding != prior {
		t.Fatalf("persisted binding = (%+v, %t), want exact prior %+v", binding, ok, prior)
	}
	if starter.abortCalls != 1 {
		t.Fatalf("abort calls = %d, want 1", starter.abortCalls)
	}
}

func TestRecoverPersistedClosingSessionConfirmsExitAndArchivesInsteadOfResurrecting(t *testing.T) {
	session, err := domain.NewStartingSession("closing", "intent-closing", "local", domain.ProviderCodex, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	prior := domain.ProviderBinding{Provider: domain.ProviderCodex, SessionID: "original-thread", Generation: 4}
	session, err = session.Ready(prior)
	if err != nil {
		t.Fatal(err)
	}
	session, err = session.BeginClose(session.StateChangedAt())
	if err != nil {
		t.Fatal(err)
	}
	store := &recoveryStore{sessions: []domain.Session{session}}
	starter := &recoveryStarter{}

	result, err := RecoverPersistedSessionsForComputer(context.Background(), "local", store, starter)
	if err != nil {
		t.Fatalf("RecoverPersistedSessionsForComputer() error = %v", err)
	}
	if result.Recovered != 0 || result.FinalizedClosing != 1 || len(result.Sessions) != 0 {
		t.Fatalf("result = %#v, want one finalized closing session and no live recovery", result)
	}
	if starter.abortCalls != 1 {
		t.Fatalf("abort calls = %d, want 1", starter.abortCalls)
	}
	if store.sessions[0].Status() != domain.SessionArchived {
		t.Fatalf("persisted status = %q, want archived", store.sessions[0].Status())
	}
}

func TestRecoverPersistedSessionsSkipsSessionsOwnedByOtherComputers(t *testing.T) {
	local, err := domain.NewStartingSession("local", "intent-local", "computer-a", domain.ProviderCodex, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	remote, err := domain.NewStartingSession("remote", "intent-remote", "computer-b", domain.ProviderClaude, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	store := &recoveryStore{sessions: []domain.Session{local, remote}}
	starter := &recoveryStarter{}

	result, err := RecoverPersistedSessionsForComputer(context.Background(), "computer-a", store, starter)
	if err != nil {
		t.Fatal(err)
	}
	if result.Recovered != 1 || result.SkippedRemote != 1 {
		t.Fatalf("result = %+v, want one recovered and one skipped remote", result)
	}
	if len(starter.requests) != 1 || starter.requests[0].ComputerID != "computer-a" {
		t.Fatalf("start requests = %+v, want only local computer", starter.requests)
	}
	if !store.sessions[1].Equal(remote) {
		t.Fatal("remote session was changed")
	}
}

type recoveryStore struct{ sessions []domain.Session }

func (s *recoveryStore) List(context.Context) ([]domain.Session, error) {
	return append([]domain.Session(nil), s.sessions...), nil
}
func (s *recoveryStore) Replace(_ context.Context, expected, next domain.Session) error {
	for i, current := range s.sessions {
		if current.Equal(expected) {
			s.sessions[i] = next
			return nil
		}
	}
	return errors.New("conflict")
}

type recoveryStarter struct {
	err                  error
	seq                  int
	replacementSessionID string
	requests             []StartSessionRequest
	abortCalls           int
}

func (s *recoveryStarter) Start(_ context.Context, request StartSessionRequest) (domain.ProviderBinding, error) {
	s.requests = append(s.requests, request)
	if s.err != nil {
		return domain.ProviderBinding{}, s.err
	}
	s.seq++
	providerSessionID := string(request.SessionID) + "-provider"
	generation := uint64(s.seq)
	if request.PriorBinding != nil {
		providerSessionID = request.PriorBinding.SessionID
		generation = request.PriorBinding.Generation + 1
	}
	if s.replacementSessionID != "" {
		providerSessionID = s.replacementSessionID
	}
	return domain.ProviderBinding{Provider: request.Provider, SessionID: providerSessionID, Generation: generation}, nil
}
func (s *recoveryStarter) Abort(context.Context, StartSessionRequest, domain.ProviderBinding) error {
	s.abortCalls++
	return nil
}
