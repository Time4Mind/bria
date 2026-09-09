package telegramcontroller_test

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"bria/internal/domain"
	"bria/internal/sessionruntime"
	"bria/internal/telegramcontroller"
)

type persistentAcceptedObserver struct{ *acceptedObserver }

func (*persistentAcceptedObserver) SupportsAttach(domain.Provider) bool { return true }

func TestPersistentAcceptedObservationGapDoesNotPublishFalseFailure(t *testing.T) {
	for _, status := range []string{"", sessionruntime.StatusFailed} {
		t.Run("status="+status, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			ready := readySession(t, "aaaaaaaa-aaaa-4aaa-9aaa-aaaaaaaaaaaa", domain.ProviderCodex, t.TempDir(), "native", 2)
			running, err := ready.StartWork(time.Now())
			if err != nil {
				t.Fatal(err)
			}
			binding, _ := running.Binding()
			provider := &persistentAcceptedObserver{&acceptedObserver{observe: func(context.Context, domain.SessionID, domain.ProviderBinding, sessionruntime.TurnCallbacks) (sessionruntime.TurnResult, error) {
				return sessionruntime.TurnResult{TerminalStatus: status}, errors.New("synthetic observation gap")
			}}}
			var mu sync.Mutex
			var errorsSeen int
			c := newController(t, nil, newLockedSessions(running), provider, notifierFunc(func(_ context.Context, n telegramcontroller.Notification) error {
				if n.Kind == telegramcontroller.NotificationError {
					mu.Lock()
					errorsSeen++
					mu.Unlock()
				}
				return nil
			}), telegramcontroller.Options{AcceptedObserver: provider, Recoverer: deferredRecoverer(func(context.Context, domain.SessionID) (domain.Session, error) { return running, nil })})
			defer c.Close(context.Background())
			done := make(chan telegramcontroller.DurableInputProcessReceipt, 1)
			if err := c.ContinueAcceptedInput(ctx, binding, telegramcontroller.DurableLeasedInput{SessionID: running.ID(), MessageID: "accepted", Sequence: 1}, telegramcontroller.DurableInputCallbacks{OnCompleted: func(_ context.Context, r telegramcontroller.DurableInputProcessReceipt) error { done <- r; return nil }}); err != nil {
				t.Fatal(err)
			}
			select {
			case r := <-done:
				if status == "" && r.Completion != telegramcontroller.DurableInputAwaitingRecovery {
					t.Fatal("observation gap resolved accepted turn")
				}
			case <-ctx.Done():
				t.Fatal("continuation did not stop")
			}
			if err := c.Close(ctx); err != nil {
				t.Fatal(err)
			}
			mu.Lock()
			count := errorsSeen
			mu.Unlock()
			if status == "" && count != 0 {
				t.Fatal("temporary observer loss polluted notifications")
			}
			if status == sessionruntime.StatusFailed && count == 0 {
				t.Fatal("proven terminal failure was hidden")
			}
		})
	}
}
