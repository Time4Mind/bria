package telegramcontroller_test

import (
	"bria/internal/domain"
	"bria/internal/sessionruntime"
	"bria/internal/telegramcontroller"
	"bria/internal/turncontinuation"
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"
)

func TestAcceptedContinuationBatchStopsBeforeNextOnGapCommitFailureOrStaleBinding(t *testing.T) {
	for _, fault := range []string{"observation", "commit", "stale"} {
		t.Run(fault, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			ready := readySession(t, "aaaaaaaa-aaaa-4aaa-9aaa-aaaaaaaaaaaa", domain.ProviderCodex, t.TempDir(), "native", 2)
			running, err := ready.StartWork(time.Now())
			if err != nil {
				t.Fatal(err)
			}
			binding, _ := running.Binding()
			sessions := newLockedSessions(running)
			var observations, finishes, wakes atomic.Int32
			provider := &persistentAcceptedObserver{&acceptedObserver{observe: func(context.Context, domain.SessionID, domain.ProviderBinding, sessionruntime.TurnCallbacks) (sessionruntime.TurnResult, error) {
				observations.Add(1)
				if fault == "observation" {
					return sessionruntime.TurnResult{}, errors.New("synthetic observer loss")
				}
				return sessionruntime.TurnResult{TerminalStatus: sessionruntime.StatusCompleted, Final: "known final"}, nil
			}}}
			c := newController(t, nil, sessions, provider, nil, telegramcontroller.Options{UIState: &retryFinalState{attempt: make(chan struct{}, 8)}, AcceptedObserver: provider, Recoverer: deferredRecoverer(func(context.Context, domain.SessionID) (domain.Session, error) { return running, nil }), TurnLifecycle: turnLifecycleFunc{finish: func(context.Context, domain.SessionID) (domain.Session, bool, error) {
				finishes.Add(1)
				return ready, false, nil
			}}})
			defer c.Close(context.Background())
			receipts := make(chan telegramcontroller.DurableInputProcessReceipt, 4)
			member := func(message, turn string, seq uint64) turncontinuation.Member {
				return turncontinuation.Member{Input: telegramcontroller.DurableLeasedInput{SessionID: running.ID(), MessageID: message, Sequence: seq}, TurnID: turn, Accepted: true, Callbacks: telegramcontroller.DurableInputCallbacks{OnCompleted: func(_ context.Context, r telegramcontroller.DurableInputProcessReceipt) error {
					if r.MessageID == "root" && fault == "stale" {
						sessions.Set(readySession(t, string(running.ID()), domain.ProviderCodex, ready.Workdir(), "native", 3))
					}
					receipts <- r
					if fault == "commit" {
						return errors.New("synthetic journal commit failure")
					}
					return nil
				}}}
			}
			if err := c.ContinueAcceptedBatch(ctx, binding, []turncontinuation.Member{member("root", "a", 1), member("steer", "a", 2), member("next", "b", 3)}, func() { wakes.Add(1) }); err != nil {
				t.Fatal(err)
			}
			receiptCount := 2
			if fault != "observation" {
				receiptCount = 1
			}
			for i := 0; i < receiptCount; i++ {
				select {
				case r := <-receipts:
					if fault == "observation" && r.Completion != telegramcontroller.DurableInputAwaitingRecovery {
						t.Fatal("observation gap lost acceptance")
					}
				case <-ctx.Done():
					t.Fatal("missing group member receipt")
				}
			}
			if err := c.Close(ctx); err != nil {
				t.Fatal(err)
			}
			if observations.Load() != 1 || finishes.Load() != 0 || wakes.Load() != 0 {
				t.Fatal("unsettled batch advanced, became Ready, or woke pending input")
			}
			if fault == "stale" && len(receipts) != 0 {
				t.Fatal("stale binding mutated another member's custody")
			}
		})
	}
}
