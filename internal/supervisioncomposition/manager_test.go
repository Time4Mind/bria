package supervisioncomposition_test

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"bria/internal/app"
	"bria/internal/domain"
	"bria/internal/sessionsupervisor"
	"bria/internal/supervisioncomposition"
)

type memoryStore struct {
	mu      sync.Mutex
	session domain.Session
}

func (store *memoryStore) List(context.Context) ([]domain.Session, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	return []domain.Session{store.session}, nil
}

func (store *memoryStore) Load(context.Context, domain.SessionID) (domain.Session, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	return store.session, nil
}

func (store *memoryStore) Replace(_ context.Context, previous, next domain.Session) error {
	store.mu.Lock()
	defer store.mu.Unlock()
	if !store.session.Equal(previous) {
		return errors.New("conflict")
	}
	store.session = next
	return nil
}

type runtimeStub struct {
	mu           sync.Mutex
	starts       int
	waits        int
	waitStarted  chan struct{}
	waitCanceled chan struct{}
}

func (runtime *runtimeStub) Start(_ context.Context, request app.StartSessionRequest) (domain.ProviderBinding, error) {
	runtime.mu.Lock()
	runtime.starts++
	runtime.mu.Unlock()
	return domain.ProviderBinding{
		Provider: request.Provider, SessionID: request.PriorBinding.SessionID,
		Generation: request.PriorBinding.Generation + 1,
	}, nil
}

func (*runtimeStub) Abort(context.Context, app.StartSessionRequest, domain.ProviderBinding) error {
	return nil
}

func (runtime *runtimeStub) Wait(ctx context.Context, _ domain.SessionID, _ domain.ProviderBinding) error {
	runtime.mu.Lock()
	runtime.waits++
	runtime.mu.Unlock()
	select {
	case runtime.waitStarted <- struct{}{}:
	default:
	}
	<-ctx.Done()
	select {
	case runtime.waitCanceled <- struct{}{}:
	default:
	}
	return ctx.Err()
}

func (runtime *runtimeStub) counts() (int, int) {
	runtime.mu.Lock()
	defer runtime.mu.Unlock()
	return runtime.starts, runtime.waits
}

type acceptedStub struct{}

func (acceptedStub) ReconcileAcceptedTurns(context.Context, domain.SessionID, domain.ProviderBinding) (sessionsupervisor.AcceptedTurnReconciliation, error) {
	return sessionsupervisor.AcceptedTurnReconciliation{}, nil
}

func TestRecoverStartupReconcilesRunningSessionOnceBeforeGenericRecovery(t *testing.T) {
	running, prior := runningSession(t)
	store := &memoryStore{session: running}
	runtime := &runtimeStub{waitStarted: make(chan struct{}, 1), waitCanceled: make(chan struct{}, 1)}
	manager := newManager(t, store, runtime)

	recovery, err := manager.RecoverStartup(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	current, _ := store.Load(context.Background(), running.ID())
	binding, bound := current.Binding()
	starts, _ := runtime.counts()
	if recovery.Recovered != 1 || len(recovery.Sessions) != 1 || starts != 1 ||
		current.Status() != domain.SessionReady || !bound || binding.SessionID != prior.SessionID || binding.Generation != prior.Generation+1 {
		t.Fatalf("recovery=%#v starts=%d current=%#v", recovery, starts, current)
	}
}

func TestLiveManagerKeepsOneWatcherAndCancelsItWhenBindingBecomesStale(t *testing.T) {
	ready, _ := readySession(t)
	store := &memoryStore{session: ready}
	runtime := &runtimeStub{waitStarted: make(chan struct{}, 1), waitCanceled: make(chan struct{}, 1)}
	manager := newManager(t, store, runtime)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- manager.Run(ctx) }()
	select {
	case <-runtime.waitStarted:
	case <-time.After(time.Second):
		t.Fatal("watcher did not start")
	}
	time.Sleep(40 * time.Millisecond)
	_, waits := runtime.counts()
	if waits != 1 {
		t.Fatalf("watchers = %d, want one", waits)
	}
	closing, err := ready.BeginClose(time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	archived, err := closing.Archive(time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Replace(context.Background(), ready, archived); err != nil {
		t.Fatal(err)
	}
	select {
	case <-runtime.waitCanceled:
	case <-time.After(time.Second):
		t.Fatal("stale watcher was not canceled")
	}
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("Run() error = %v", err)
	}
}

func newManager(t *testing.T, store *memoryStore, runtime *runtimeStub) *supervisioncomposition.Manager {
	t.Helper()
	manager, err := supervisioncomposition.New(supervisioncomposition.Options{
		LocalComputerID: "computer", Store: store, Waiter: runtime, Restarter: runtime,
		AcceptedTurns: acceptedStub{}, MaxRestartAttempts: 1, SweepInterval: 10 * time.Millisecond,
		Now: time.Now, WaitBeforeRetry: func(context.Context, int) error { return nil }, Report: func(error) {},
	})
	if err != nil {
		t.Fatal(err)
	}
	return manager
}

func runningSession(t *testing.T) (domain.Session, domain.ProviderBinding) {
	t.Helper()
	ready, binding := readySession(t)
	running, err := ready.StartWork(time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	return running, binding
}

func readySession(t *testing.T) (domain.Session, domain.ProviderBinding) {
	t.Helper()
	starting, err := domain.NewStartingSession("123e4567-e89b-12d3-a456-426614174000", "intent", "computer", domain.ProviderCodex, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	binding := domain.ProviderBinding{Provider: domain.ProviderCodex, SessionID: "thread-1", Generation: 4}
	ready, err := starting.Ready(binding)
	if err != nil {
		t.Fatal(err)
	}
	return ready, binding
}
