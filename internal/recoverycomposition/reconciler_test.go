package recoverycomposition_test

import (
	"context"
	"reflect"
	"testing"
	"time"

	"bria/internal/domain"
	"bria/internal/recoverycomposition"
	"bria/internal/sessionruntime"
	"bria/internal/sessionsupervisor"
)

func TestReconcilerMapsNeutralRuntimeReceiptWithoutChangingIdentity(t *testing.T) {
	t.Parallel()
	binding := domain.ProviderBinding{Provider: domain.ProviderCodex, SessionID: "thread", Generation: 1}
	session := readySession(t, "logical", "/durable/workdir", binding)
	reader := acceptedTurnReaderFunc(func(_ context.Context, request sessionruntime.AcceptedTurnReadRequest) (sessionruntime.AcceptedTurnReconciliation, error) {
		want := sessionruntime.AcceptedTurnReadRequest{SessionID: "logical", Provider: domain.ProviderCodex, Workdir: "/durable/workdir", Binding: binding}
		if request != want {
			t.Fatalf("read request = %#v, want %#v", request, want)
		}
		return sessionruntime.AcceptedTurnReconciliation{Turns: []sessionruntime.ReconciledAcceptedTurn{
			{MessageID: "m1", Outcome: sessionruntime.AcceptedTurnCompleted},
			{MessageID: "m2", Outcome: sessionruntime.AcceptedTurnUnknown},
		}}, nil
	})
	reconciler, err := recoverycomposition.NewReconciler(reader, sessionLoaderFunc(func(context.Context, domain.SessionID) (domain.Session, error) {
		return session, nil
	}))
	if err != nil {
		t.Fatal(err)
	}
	got, err := reconciler.ReconcileAcceptedTurns(context.Background(), "logical", binding)
	if err != nil {
		t.Fatal(err)
	}
	want := sessionsupervisor.AcceptedTurnReconciliation{Turns: []sessionsupervisor.ReconciledAcceptedTurn{
		{MessageID: "m1", Outcome: sessionsupervisor.AcceptedTurnCompleted},
		{MessageID: "m2", Outcome: sessionsupervisor.AcceptedTurnUnknown},
	}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("result = %#v, want %#v", got, want)
	}
}

func TestReconcilerRejectsStalePersistedBindingBeforeProviderRead(t *testing.T) {
	t.Parallel()
	persisted := domain.ProviderBinding{Provider: domain.ProviderCodex, SessionID: "thread-current", Generation: 2}
	session := readySession(t, "logical", "/durable/workdir", persisted)
	called := false
	reader := acceptedTurnReaderFunc(func(context.Context, sessionruntime.AcceptedTurnReadRequest) (sessionruntime.AcceptedTurnReconciliation, error) {
		called = true
		return sessionruntime.AcceptedTurnReconciliation{}, nil
	})
	reconciler, err := recoverycomposition.NewReconciler(reader, sessionLoaderFunc(func(context.Context, domain.SessionID) (domain.Session, error) {
		return session, nil
	}))
	if err != nil {
		t.Fatal(err)
	}
	_, err = reconciler.ReconcileAcceptedTurns(context.Background(), "logical", domain.ProviderBinding{
		Provider: domain.ProviderCodex, SessionID: "thread-stale", Generation: 1,
	})
	if err == nil {
		t.Fatal("stale binding was accepted")
	}
	if called {
		t.Fatal("provider history was read before durable binding validation")
	}
}

type acceptedTurnReaderFunc func(context.Context, sessionruntime.AcceptedTurnReadRequest) (sessionruntime.AcceptedTurnReconciliation, error)

func (function acceptedTurnReaderFunc) ReadAcceptedTurns(ctx context.Context, request sessionruntime.AcceptedTurnReadRequest) (sessionruntime.AcceptedTurnReconciliation, error) {
	return function(ctx, request)
}

type sessionLoaderFunc func(context.Context, domain.SessionID) (domain.Session, error)

func (function sessionLoaderFunc) Load(ctx context.Context, sessionID domain.SessionID) (domain.Session, error) {
	return function(ctx, sessionID)
}

func readySession(t *testing.T, sessionID domain.SessionID, workdir string, binding domain.ProviderBinding) domain.Session {
	t.Helper()
	now := time.Unix(1_700_000_000, 0).UTC()
	session, err := domain.NewStartingSessionAt(sessionID, "intent", "computer", binding.Provider, workdir, now, domain.SessionLifetimeNever)
	if err != nil {
		t.Fatal(err)
	}
	session, err = session.ReadyAt(binding, now.Add(time.Second))
	if err != nil {
		t.Fatal(err)
	}
	return session
}
