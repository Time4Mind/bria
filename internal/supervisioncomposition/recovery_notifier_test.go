package supervisioncomposition_test

import (
	"context"
	"sync"
	"testing"
	"time"

	"bria/internal/domain"
	"bria/internal/sessionsupervisor"
	"bria/internal/supervisioncomposition"
)

func newNotifierManager(store supervisioncomposition.Store, runtime *exitRuntime, reconciler sessionsupervisor.AcceptedTurnReconciler) (*supervisioncomposition.Manager, error) {
	return supervisioncomposition.New(supervisioncomposition.Options{LocalComputerID: "computer", Store: store, Restarter: runtime, Waiter: runtime, AcceptedTurns: reconciler, MaxRestartAttempts: 1, SweepInterval: time.Hour, Now: time.Now, WaitBeforeRetry: func(context.Context, int) error { return nil }, Report: func(error) {}})
}

type gatedRecoveryStore struct {
	*memoryStore
	once   sync.Once
	before chan struct{}
	commit <-chan struct{}
}

func (s *gatedRecoveryStore) Replace(ctx context.Context, old, next domain.Session) error {
	if next.Status() == domain.SessionAwaitingRecovery {
		s.once.Do(func() { close(s.before); <-s.commit })
	}
	return s.memoryStore.Replace(ctx, old, next)
}

func TestLiveRecoveryNotifiesExactSessionAfterCommitOutsideLocks(t *testing.T) {
	for _, unknown := range []bool{true, false} {
		t.Run(map[bool]string{true: "awaiting", false: "ready"}[unknown], func(t *testing.T) {
			running, _ := runningSession(t)
			commit := make(chan struct{})
			before := make(chan struct{})
			store := &gatedRecoveryStore{memoryStore: &memoryStore{session: running}, before: before, commit: commit}
			runtime := &exitRuntime{}
			manager, err := newNotifierManager(store, runtime, &recoveryReconciler{unknown: unknown})
			if err != nil {
				t.Fatal(err)
			}
			setter, ok := any(manager).(interface {
				SetRecoveryNotifier(func(context.Context, domain.SessionID))
			})
			if !ok {
				t.Fatal("live recovery has no independent card notifier")
			}
			notified := make(chan domain.SessionID, 2)
			setter.SetRecoveryNotifier(func(ctx context.Context, id domain.SessionID) {
				current, err := store.Load(ctx, id)
				want := domain.SessionReady
				if unknown {
					want = domain.SessionAwaitingRecovery
				}
				if err != nil || current.Status() != want {
					t.Error("notifier ran before durable recovery outcome")
				}
				manager.SetObserver(nil) // callback must not hold the manager's worker lock
				if !unknown {
					if _, err := manager.RecoverSession(ctx, id); err != nil {
						t.Error(err)
					}
				} // nor the recovery gate
				notified <- id
			})
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			done := make(chan error, 1)
			go func() { done <- manager.Run(ctx) }()
			select {
			case <-before:
			case <-time.After(time.Second):
				t.Fatal("awaiting transition was not attempted")
			}
			select {
			case <-notified:
				t.Error("card refreshed before state commit")
			default:
			}
			close(commit)
			select {
			case id := <-notified:
				if id != running.ID() {
					t.Fatal("notifier targeted a different session")
				}
			case <-time.After(time.Second):
				t.Fatal("committed recovery did not refresh the card")
			}
			cancel()
			<-done
			select {
			case <-notified:
				t.Fatal("manual or cancelled watch duplicated live refresh")
			default:
			}
		})
	}
}
