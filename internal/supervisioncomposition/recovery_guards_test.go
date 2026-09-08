package supervisioncomposition_test

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"bria/internal/app"
	"bria/internal/domain"
	"bria/internal/supervisioncomposition"
)

func TestManualRecoveryConcurrentClicksResumeExactlyOnce(t *testing.T) {
	running, prior := runningSession(t)
	awaiting, err := running.AwaitRecovery()
	if err != nil {
		t.Fatal(err)
	}
	store := &memoryStore{session: awaiting}
	runtime := &runtimeStub{}
	reconciler := &recoveryReconciler{}
	manager := recoveryManager(t, store, runtime, runtime, reconciler)
	var calls sync.WaitGroup
	for i := 0; i < 12; i++ {
		calls.Add(1)
		go func() {
			defer calls.Done()
			session, err := manager.RecoverSession(context.Background(), running.ID())
			if err != nil || session.Status() != domain.SessionReady {
				t.Error("concurrent manual recovery did not return ready")
			}
		}()
	}
	calls.Wait()
	current, _ := store.Load(context.Background(), running.ID())
	binding, _ := current.Binding()
	starts, _ := runtime.counts()
	if starts != 1 || binding.SessionID != prior.SessionID || binding.Generation != prior.Generation+1 {
		t.Fatal("manual clicks resumed more than one exact provider generation")
	}
}

type failedStartRuntime struct{ runtimeStub }

func (*failedStartRuntime) Start(context.Context, app.StartSessionRequest) (domain.ProviderBinding, error) {
	return domain.ProviderBinding{}, errors.New("private-start-error")
}

func TestOrdinaryStartupFailureObservesPersistedAwaitingState(t *testing.T) {
	ready, _ := readySession(t)
	runtime := &failedStartRuntime{}
	manager := recoveryManager(t, &memoryStore{session: ready}, runtime, runtime, &recoveryReconciler{})
	observer := &recoveryObserver{}
	manager.SetObserver(observer)
	result, err := manager.RecoverStartup(context.Background())
	if err != nil || result.Awaiting != 1 {
		t.Fatal("ordinary failed recovery did not remain awaiting")
	}
	if len(observer.events) != 1 || observer.events[0].Outcome.String() != "awaiting_recovery" || observer.events[0].Reason.String() != "startup_recovery" {
		t.Fatal("ordinary startup failure has no observed awaiting outcome")
	}
}

func TestManualRecoveryRejectsOtherIdentityAndActiveWork(t *testing.T) {
	for _, kind := range []string{"wrong-id", "other-node", "running", "unbound"} {
		t.Run(kind, func(t *testing.T) {
			running, _ := runningSession(t)
			current := running
			id := running.ID()
			switch kind {
			case "wrong-id":
				id = "223e4567-e89b-12d3-a456-426614174000"
			case "other-node":
				snapshot := running.Snapshot()
				snapshot.ComputerID = "other"
				var err error
				current, err = domain.RestoreSession(snapshot)
				if err != nil {
					t.Fatal(err)
				}
			case "unbound":
				var err error
				current, err = domain.NewStartingSession(id, "other-intent", "computer", domain.ProviderCodex, t.TempDir())
				if err != nil {
					t.Fatal(err)
				}
				current, err = current.AwaitRecovery()
				if err != nil {
					t.Fatal(err)
				}
			}
			store := &memoryStore{session: current}
			runtime := &runtimeStub{}
			reconciler := &recoveryReconciler{}
			manager := recoveryManager(t, store, runtime, runtime, reconciler)
			if _, err := manager.RecoverSession(context.Background(), id); !errors.Is(err, supervisioncomposition.ErrInvalidOptions) {
				t.Fatal("unsafe manual target lost the manager's invalid-request contract")
			}
			after, _ := store.Load(context.Background(), current.ID())
			starts, _ := runtime.counts()
			if starts != 0 || !after.Equal(current) || reconciler.calls != 0 {
				t.Fatal("rejected recovery mutated state or provider")
			}
		})
	}
}

type failedWaitRuntime struct{ runtimeStub }

func (*failedWaitRuntime) Wait(context.Context, domain.SessionID, domain.ProviderBinding) error {
	return errors.New("private-uncertain-exit")
}

func TestLiveWaitFailureNeverInventsSuccessfulExit(t *testing.T) {
	ready, _ := readySession(t)
	store := &memoryStore{session: ready}
	runtime := &failedWaitRuntime{}
	manager := recoveryManager(t, store, runtime, runtime, &recoveryReconciler{})
	observer := &forwardRecoveryObserver{observer: &recoveryObserver{}, done: make(chan struct{}, 2)}
	manager.SetObserver(observer)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- manager.Run(ctx) }()
	select {
	case <-observer.done:
	case <-time.After(time.Second):
		cancel()
		t.Fatal("wait failure was not observed")
	}
	cancel()
	<-done
	current, _ := store.Load(context.Background(), ready.ID())
	starts, _ := runtime.counts()
	if !current.Equal(ready) || starts != 0 {
		t.Fatal("uncertain wait was treated as a confirmed exit")
	}
}

func TestManualRecoveryCancellationDoesNotWaitForAnotherReconciliation(t *testing.T) {
	running, _ := runningSession(t)
	awaiting, err := running.AwaitRecovery()
	if err != nil {
		t.Fatal(err)
	}
	store := &memoryStore{session: awaiting}
	runtime := &runtimeStub{}
	entered, release := make(chan struct{}, 2), make(chan struct{})
	manager := recoveryManager(t, store, runtime, runtime, &recoveryReconciler{entered: entered, release: release})
	first := make(chan error, 1)
	go func() { _, err := manager.RecoverSession(context.Background(), running.ID()); first <- err }()
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("first reconciliation did not start")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	second := make(chan error, 1)
	go func() { _, err := manager.RecoverSession(ctx, running.ID()); second <- err }()
	returned := false
	select {
	case err := <-second:
		returned = true
		if !errors.Is(err, context.Canceled) {
			t.Error("cancelled queued recovery succeeded")
		}
	case <-time.After(100 * time.Millisecond):
	}
	close(release)
	if err := <-first; err != nil {
		t.Fatal(err)
	}
	if !returned {
		<-second
		t.Fatal("cancelled recovery waited for unrelated reconciliation")
	}
}
