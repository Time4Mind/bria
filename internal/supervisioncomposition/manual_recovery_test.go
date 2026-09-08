package supervisioncomposition_test

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"bria/internal/controllertelemetry"
	"bria/internal/domain"
	"bria/internal/sessionsupervisor"
	"bria/internal/supervisioncomposition"
)

type recoveryReconciler struct {
	mu      sync.Mutex
	calls   int
	entered chan struct{}
	release <-chan struct{}
	unknown bool
	prior   domain.ProviderBinding
}

func (r *recoveryReconciler) ReconcileAcceptedTurns(_ context.Context, _ domain.SessionID, binding domain.ProviderBinding) (sessionsupervisor.AcceptedTurnReconciliation, error) {
	r.mu.Lock()
	r.calls++
	r.prior = binding
	r.mu.Unlock()
	if r.entered != nil {
		r.entered <- struct{}{}
	}
	if r.release != nil {
		<-r.release
	}
	outcome := sessionsupervisor.AcceptedTurnCompleted
	if r.unknown {
		outcome = sessionsupervisor.AcceptedTurnUnknown
	}
	return sessionsupervisor.AcceptedTurnReconciliation{Turns: []sessionsupervisor.ReconciledAcceptedTurn{{MessageID: "private-accepted-input", Outcome: outcome}}}, nil
}

type exitRuntime struct{ runtimeStub }

func (*exitRuntime) Wait(context.Context, domain.SessionID, domain.ProviderBinding) error { return nil }

func recoveryManager(t *testing.T, store *memoryStore, runtime sessionsupervisor.Restarter, waiter sessionsupervisor.ProcessWaiter, reconciler sessionsupervisor.AcceptedTurnReconciler) *supervisioncomposition.Manager {
	t.Helper()
	m, err := supervisioncomposition.New(supervisioncomposition.Options{LocalComputerID: "computer", Store: store, Restarter: runtime, Waiter: waiter, AcceptedTurns: reconciler, MaxRestartAttempts: 1, SweepInterval: time.Hour, Now: time.Now, WaitBeforeRetry: func(context.Context, int) error { return nil }, Report: func(error) {}})
	if err != nil {
		t.Fatal(err)
	}
	return m
}

func TestManualRecoveryRejectsCancelledReadyRequest(t *testing.T) {
	ready, _ := readySession(t)
	runtime := &runtimeStub{}
	m := newManager(t, &memoryStore{session: ready}, runtime)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := m.RecoverSession(ctx, ready.ID())
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled ready recovery error=%v", err)
	}
}

func TestManualRecoveryDoesNotRaceLiveReconciliation(t *testing.T) {
	running, prior := runningSession(t)
	store := &memoryStore{session: running}
	runtime := &exitRuntime{}
	release := make(chan struct{})
	entered := make(chan struct{}, 4)
	r := &recoveryReconciler{entered: entered, release: release}
	m := recoveryManager(t, store, runtime, runtime, r)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- m.Run(ctx) }()
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("live reconciliation not reached")
	}
	manual := make(chan error, 1)
	go func() { _, err := m.RecoverSession(context.Background(), running.ID()); manual <- err }()
	duplicate := false
	select {
	case <-entered:
		duplicate = true
	case <-time.After(50 * time.Millisecond):
	}
	close(release)
	select {
	case err := <-manual:
		if err != nil {
			t.Error(err)
		}
	case <-time.After(time.Second):
		t.Fatal("manual recovery stuck")
	}
	cancel()
	<-done
	if duplicate {
		t.Error("manual recovery concurrently reconciled the same accepted input as live watcher")
	}
	current, _ := store.Load(context.Background(), running.ID())
	binding, _ := current.Binding()
	starts, _ := runtime.counts()
	if starts != 1 || binding.SessionID != prior.SessionID || binding.Generation != prior.Generation+1 || current.Status() != domain.SessionReady {
		t.Fatalf("recovery starts=%d status=%s generation=%d", starts, current.Status(), binding.Generation)
	}
}

func TestManualRecoveryUnknownAndReadyAreSafeAndObservable(t *testing.T) {
	for _, unknown := range []bool{true, false} {
		t.Run(map[bool]string{true: "unknown", false: "proven"}[unknown], func(t *testing.T) {
			running, prior := runningSession(t)
			awaiting, err := running.AwaitRecovery()
			if err != nil {
				t.Fatal(err)
			}
			store := &memoryStore{session: awaiting}
			runtime := &runtimeStub{}
			r := &recoveryReconciler{unknown: unknown}
			m := recoveryManager(t, store, runtime, runtime, r)
			observer := &recoveryObserver{}
			setter, ok := any(m).(interface {
				SetObserver(controllertelemetry.Observer)
			})
			if !ok {
				t.Fatal("manager has no optional recovery observer")
			}
			setter.SetObserver(observer)
			current, err := m.RecoverSession(controllertelemetry.WithOperation(context.Background(), "private-recovery-operation"), running.ID())
			starts, _ := runtime.counts()
			if unknown {
				if !errors.Is(err, sessionsupervisor.ErrReconciliationRequired) || current.Status() != domain.SessionAwaitingRecovery || starts != 0 {
					t.Fatalf("unknown outcome status=%s starts=%d err=%v", current.Status(), starts, err)
				}
			} else {
				if err != nil || current.Status() != domain.SessionReady || starts != 1 {
					t.Fatalf("proven recovery status=%s starts=%d err=%v", current.Status(), starts, err)
				}
				if _, err := m.RecoverSession(context.Background(), running.ID()); err != nil {
					t.Fatal(err)
				}
				starts, _ = runtime.counts()
				if starts != 1 {
					t.Fatal("ready retry restarted provider")
				}
			}
			if r.prior != prior {
				t.Fatal("reconciliation used a different binding")
			}
			observer.mu.Lock()
			defer observer.mu.Unlock()
			if len(observer.events) == 0 || observer.events[0].Stage.String() != "session.recovery_outcome" || observer.events[0].Reason.String() != "manual_recovery" {
				t.Fatal("missing typed manual outcome")
			}
		})
	}
}

type recoveryObserver struct {
	mu     sync.Mutex
	events []controllertelemetry.Event
}

func (o *recoveryObserver) ObserveControllerEvent(_ context.Context, e controllertelemetry.Event) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.events = append(o.events, e)
}
