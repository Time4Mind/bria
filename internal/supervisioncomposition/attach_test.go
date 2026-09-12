package supervisioncomposition_test

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"bria/internal/app"
	"bria/internal/domain"
	"bria/internal/sessionsupervisor"
	"bria/internal/supervisioncomposition"
)

type attachRuntime struct {
	*runtimeStub
	unavailable atomic.Bool
}

func (*attachRuntime) SupportsAttach(domain.Provider) bool { return true }
func (r *attachRuntime) Attach(_ context.Context, req app.StartSessionRequest) (domain.ProviderBinding, error) {
	if r.unavailable.Load() {
		return domain.ProviderBinding{}, errors.New("temporary observer unavailable")
	}
	next := *req.PriorBinding
	next.Generation++
	return next, nil
}
func (*attachRuntime) Detach(context.Context, app.StartSessionRequest, domain.ProviderBinding) error {
	return nil
}

func TestAttachStartupAndSweepNeverDeclarePersistedLiveCLIExited(t *testing.T) {
	ready, prior := readySession(t)
	store := &memoryStore{session: ready}
	runtime := &attachRuntime{runtimeStub: &runtimeStub{}}
	clock := newRecoveryClock(ready.StateChangedAt())
	runtime.unavailable.Store(true)
	manager, err := supervisioncomposition.New(supervisioncomposition.Options{
		LocalComputerID: "computer", Store: store, Restarter: runtime, Waiter: runtime,
		AcceptedTurns: acceptedStub{}, MaxRestartAttempts: 1, SweepInterval: 5 * time.Millisecond,
		Now: clock.Now, WaitBeforeRetry: func(context.Context, int) error { return nil }, Report: func(error) {},
	})
	if err != nil {
		t.Fatal(err)
	}
	result, err := manager.RecoverStartup(context.Background())
	if err != nil || result.Awaiting != 1 || runtime.confirmations() != 0 {
		t.Errorf("startup fabricated exit or lost pending attach: %+v err=%v confirmed=%d", result, err, runtime.confirmations())
	}
	runtime.unavailable.Store(false)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- manager.Run(ctx) }()
	t.Cleanup(func() {
		cancel()
		select {
		case <-done:
		case <-time.After(time.Second):
			t.Error("manager shutdown hung")
		}
	})
	time.Sleep(30 * time.Millisecond)
	currentBeforeRetry, loadErr := store.Load(ctx, ready.ID())
	retainedBeforeRetry, retained := currentBeforeRetry.Binding()
	if loadErr != nil || currentBeforeRetry.Status() != domain.SessionAwaitingRecovery || !retained || retainedBeforeRetry != prior || runtime.confirmations() != 0 {
		t.Fatalf("startup failure changed session before cooldown: %+v err=%v confirmed=%d", currentBeforeRetry, loadErr, runtime.confirmations())
	}
	clock.Advance(time.Minute)
	deadline := time.After(time.Second)
	ticker := time.NewTicker(time.Millisecond)
	defer ticker.Stop()
	for {
		current, err := store.Load(ctx, ready.ID())
		binding, _ := current.Binding()
		if err == nil && current.Status() == domain.SessionReady && binding.Generation == prior.Generation+1 {
			starts, _ := runtime.counts()
			if starts != 0 || runtime.confirmations() != 0 {
				t.Fatal("reattach replaced the original live CLI")
			}
			return
		}
		select {
		case <-deadline:
			t.Fatal("awaiting attach was not retried by sweep")
		case <-ticker.C:
		}
	}
}

type blockedAttachRuntime struct {
	*attachRuntime
	entered chan struct{}
}

func (r *blockedAttachRuntime) Attach(ctx context.Context, _ app.StartSessionRequest) (domain.ProviderBinding, error) {
	close(r.entered)
	<-ctx.Done()
	return domain.ProviderBinding{}, ctx.Err()
}

func TestShutdownCancelsPendingAttachWithoutStartingOrClosingCLI(t *testing.T) {
	ready, _ := readySession(t)
	awaiting, err := ready.AwaitRecoveryAt(time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	store := &memoryStore{session: awaiting}
	runtime := &blockedAttachRuntime{attachRuntime: &attachRuntime{runtimeStub: &runtimeStub{}}, entered: make(chan struct{})}
	manager, err := supervisioncomposition.New(supervisioncomposition.Options{
		LocalComputerID: "computer", Store: store, Restarter: runtime, Waiter: runtime,
		AcceptedTurns: acceptedStub{}, MaxRestartAttempts: 1, SweepInterval: time.Hour,
		Now: time.Now, WaitBeforeRetry: func(context.Context, int) error { return nil }, Report: func(error) {},
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- manager.Run(ctx) }()
	select {
	case <-runtime.entered:
	case <-time.After(time.Second):
		t.Fatal("pending attach did not start")
	}
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("shutdown = %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("shutdown did not cancel attach")
	}
	current, err := store.Load(context.Background(), ready.ID())
	starts, _ := runtime.counts()
	if err != nil || !current.Equal(awaiting) || starts != 0 {
		t.Fatal("cancelled attach changed retained live-session identity")
	}
}

type retainedRecoveryRuntime struct {
	*runtimeStub
	attaches  atomic.Int32
	exitFirst atomic.Bool
}

func (*retainedRecoveryRuntime) SupportsAttach(domain.Provider) bool { return true }

func (runtime *retainedRecoveryRuntime) Attach(_ context.Context, request app.StartSessionRequest) (domain.ProviderBinding, error) {
	runtime.attaches.Add(1)
	next := *request.PriorBinding
	next.Generation++
	return next, nil
}

func (*retainedRecoveryRuntime) Detach(context.Context, app.StartSessionRequest, domain.ProviderBinding) error {
	return nil
}

func (runtime *retainedRecoveryRuntime) Wait(ctx context.Context, id domain.SessionID, binding domain.ProviderBinding) error {
	if runtime.exitFirst.CompareAndSwap(true, false) {
		return nil
	}
	return runtime.runtimeStub.Wait(ctx, id, binding)
}

func TestSweepDoesNotCancelAcceptedTurnRecoveryAfterInternalBindingHandoff(t *testing.T) {
	for _, initial := range []string{"awaiting", "running"} {
		t.Run(initial, func(t *testing.T) {
			testSweepDoesNotCancelAcceptedTurnRecoveryAfterInternalBindingHandoff(t, initial)
		})
	}
}

func testSweepDoesNotCancelAcceptedTurnRecoveryAfterInternalBindingHandoff(t *testing.T, initial string) {
	ready, prior := readySession(t)
	current := ready
	if initial == "running" {
		current, _ = ready.StartWork(time.Now().UTC())
	} else {
		current, _ = ready.AwaitRecoveryAt(time.Now().UTC())
	}
	store := &memoryStore{session: current}
	runtime := &retainedRecoveryRuntime{runtimeStub: &runtimeStub{waitStarted: make(chan struct{}, 1), waitCanceled: make(chan struct{}, 1)}}
	runtime.exitFirst.Store(initial == "running")
	entered := make(chan struct{}, 1)
	release := make(chan struct{})
	canceled := make(chan struct{}, 1)
	manager, err := supervisioncomposition.New(supervisioncomposition.Options{
		LocalComputerID: "computer", Store: store, Restarter: runtime, Waiter: runtime,
		AcceptedTurns: &recoveryReconciler{unknown: true}, MaxRestartAttempts: 1, SweepInterval: 5 * time.Millisecond,
		ShouldContinueAcceptedTurns: func(context.Context, domain.Session, domain.ProviderBinding, sessionsupervisor.AcceptedTurnReconciliation) (bool, error) {
			return true, nil
		},
		ContinueAcceptedTurns: func(ctx context.Context, _ domain.Session, _ domain.ProviderBinding, _ sessionsupervisor.AcceptedTurnReconciliation) error {
			select {
			case entered <- struct{}{}:
			default:
			}
			select {
			case <-release:
				return nil
			case <-ctx.Done():
				select {
				case canceled <- struct{}{}:
				default:
				}
				return ctx.Err()
			}
		},
		Now: time.Now, WaitBeforeRetry: func(context.Context, int) error { return nil }, Report: func(error) {},
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- manager.Run(ctx) }()
	defer stopRecoveryManager(t, cancel, done)
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("accepted recovery did not start")
	}
	time.Sleep(40 * time.Millisecond)
	select {
	case <-canceled:
		t.Fatal("sweep canceled accepted recovery after its own binding handoff")
	default:
	}
	if got := runtime.attaches.Load(); got != 1 {
		t.Fatalf("attach attempts = %d, want one retained recovery", got)
	}
	current, err = store.Load(ctx, ready.ID())
	binding, bound := current.Binding()
	if err != nil || current.Status() != domain.SessionRunning || !bound || binding.Generation != prior.Generation+1 {
		t.Fatalf("recovery handoff = status %q binding %#v bound %t error %v", current.Status(), binding, bound, err)
	}
	close(release)
	select {
	case <-runtime.waitStarted:
	case <-time.After(time.Second):
		t.Fatal("settled recovery did not install the new binding watcher")
	}
}

func TestFailedAcceptedRecoveryBackoffUsesReplacementBinding(t *testing.T) {
	ready, prior := readySession(t)
	awaiting, err := ready.AwaitRecoveryAt(time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	store := &memoryStore{session: awaiting}
	runtime := &retainedRecoveryRuntime{runtimeStub: &runtimeStub{}}
	failed := make(chan struct{}, 1)
	clock := newRecoveryClock(time.Now().UTC())
	manager, err := supervisioncomposition.New(supervisioncomposition.Options{
		LocalComputerID: "computer", Store: store, Restarter: runtime, Waiter: runtime,
		AcceptedTurns: &recoveryReconciler{unknown: true}, MaxRestartAttempts: 1, SweepInterval: 5 * time.Millisecond,
		ShouldContinueAcceptedTurns: func(context.Context, domain.Session, domain.ProviderBinding, sessionsupervisor.AcceptedTurnReconciliation) (bool, error) {
			return true, nil
		},
		ContinueAcceptedTurns: func(context.Context, domain.Session, domain.ProviderBinding, sessionsupervisor.AcceptedTurnReconciliation) error {
			select {
			case failed <- struct{}{}:
			default:
			}
			return errors.New("synthetic accepted observation failure")
		},
		Now: clock.Now, WaitBeforeRetry: func(context.Context, int) error { return nil }, Report: func(error) {},
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- manager.Run(ctx) }()
	defer stopRecoveryManager(t, cancel, done)
	select {
	case <-failed:
	case <-time.After(time.Second):
		t.Fatal("accepted recovery failure was not reached")
	}
	time.Sleep(40 * time.Millisecond)
	if got := runtime.attaches.Load(); got != 1 {
		t.Fatalf("attach attempts before cooldown = %d, want one", got)
	}
	current, err := store.Load(ctx, ready.ID())
	binding, bound := current.Binding()
	if err != nil || current.Status() != domain.SessionAwaitingRecovery || !bound || binding.Generation != prior.Generation+1 {
		t.Fatalf("failed recovery custody = status %q binding %#v bound %t error %v", current.Status(), binding, bound, err)
	}
}
