package telegramcontroller_test

import (
	"bria/internal/domain"
	"bria/internal/sessionruntime"
	"bria/internal/telegramcontroller"
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestPersistentMainAndSteerRetryOnlyKnownTerminalJournalCompletion(t *testing.T) {
	for _, target := range []string{"root", "steer"} {
		t.Run(target, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
			defer cancel()
			ready := readySession(t, "aaaaaaaa-aaaa-4aaa-9aaa-aaaaaaaaaaaa", domain.ProviderCodex, t.TempDir(), "native", 2)
			release := make(chan struct{})
			var once sync.Once
			defer once.Do(func() { close(release) })
			var accepted, observed atomic.Int32
			observer := &persistentAcceptedObserver{&acceptedObserver{observe: func(context.Context, domain.SessionID, domain.ProviderBinding, sessionruntime.TurnCallbacks) (sessionruntime.TurnResult, error) {
				observed.Add(1)
				return sessionruntime.TurnResult{}, errors.New("normal finalization must not observe again")
			}}}
			c := newController(t, nil, newLockedSessions(ready), terminalFailureProvider{release: release, status: sessionruntime.StatusFailed}, nil, telegramcontroller.Options{Recovered: []domain.Session{ready}, AcceptedObserver: observer, Recoverer: deferredRecoverer(func(context.Context, domain.SessionID) (domain.Session, error) { return ready, nil })})
			defer c.Close(context.Background())
			var mu sync.Mutex
			faulted := false
			done := make(chan string, 4)
			callbacks := telegramcontroller.DurableInputCallbacks{OnAccepted: func(context.Context, telegramcontroller.DurableInputAcceptance) error { accepted.Add(1); return nil }, OnCompleted: func(_ context.Context, r telegramcontroller.DurableInputProcessReceipt) error {
				mu.Lock()
				defer mu.Unlock()
				if r.Completion != telegramcontroller.DurableInputTerminalFailed {
					return errors.New("terminal proof changed")
				}
				if r.MessageID == target && !faulted {
					faulted = true
					return errors.New("synthetic local journal failure")
				}
				done <- r.MessageID
				return nil
			}}
			for i, id := range []string{"root", "steer"} {
				r, err := c.ProcessDurableInput(ctx, telegramcontroller.DurableLeasedInput{SessionID: ready.ID(), MessageID: id, Sequence: uint64(i + 1), Payload: []byte(id)}, callbacks)
				if err != nil || !r.Accepted {
					t.Fatalf("input acceptance failed: %v", err)
				}
			}
			once.Do(func() { close(release) })
			seen := map[string]bool{}
			for len(seen) < 2 {
				select {
				case id := <-done:
					if seen[id] {
						t.Fatal("duplicate successful completion")
					}
					seen[id] = true
				case <-ctx.Done():
					t.Fatal("known main/steer completion was not retried")
				}
			}
			if accepted.Load() != 2 || observed.Load() != 0 {
				t.Fatal("local retry replayed provider work")
			}
		})
	}
}
