package telegramcontroller_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"bria/internal/domain"
	"bria/internal/sessionruntime"
	"bria/internal/telegramcontroller"
)

func TestFinalRetryNotificationDoesNotBlockClose(t *testing.T) {
	for _, stage := range []string{"error", "restored"} {
		t.Run(stage, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			ready := readySession(t, "aaaaaaaa-aaaa-4aaa-9aaa-aaaaaaaaaaaa", domain.ProviderCodex, t.TempDir(), "provider-a", 1)
			state := &retryFinalState{failures: 1, attempt: make(chan struct{}, 8)}
			entered := make(chan struct{}, 1)
			release := make(chan struct{})
			notifier := notifierFunc(func(notifyCtx context.Context, n telegramcontroller.Notification) error {
				if n.OperationID != "root:final-history-"+stage {
					return nil
				}
				entered <- struct{}{}
				select {
				case <-notifyCtx.Done():
					return notifyCtx.Err()
				case <-release:
					return errors.New("test cleanup released notifier")
				}
			})
			provider := &interactiveSubmitter{submitWithCallbacks: func(_ context.Context, _ domain.SessionID, _ string, cb sessionruntime.TurnCallbacks) (sessionruntime.TurnResult, error) {
				if err := cb.OnAccepted(cb.MessageID); err != nil {
					return sessionruntime.TurnResult{}, err
				}
				return sessionruntime.TurnResult{TerminalStatus: sessionruntime.StatusCompleted, Final: "retained final"}, nil
			}}
			c := newController(t, nil, newLockedSessions(ready), provider, notifier, telegramcontroller.Options{Recovered: []domain.Session{ready}, UIState: state})
			completed := make(chan telegramcontroller.DurableInputProcessReceipt, 2)
			accepted := false
			defer func() {
				close(release)
				cleanupCtx, stop := context.WithTimeout(context.Background(), time.Second)
				defer stop()
				_ = c.Close(cleanupCtx)
				if accepted {
					select {
					case <-completed:
					case <-cleanupCtx.Done():
						t.Error("accepted turn did not settle after notifier cleanup")
					}
				}
			}()
			receipt, err := c.ProcessDurableInput(ctx, telegramcontroller.DurableLeasedInput{
				SessionID: ready.ID(), MessageID: "root", Sequence: 1, Payload: []byte("one request"),
			}, telegramcontroller.DurableInputCallbacks{
				OnAccepted: func(context.Context, telegramcontroller.DurableInputAcceptance) error { return nil },
				OnCompleted: func(_ context.Context, result telegramcontroller.DurableInputProcessReceipt) error {
					completed <- result
					return nil
				},
			})
			accepted = receipt.Accepted
			if err != nil || !accepted {
				t.Fatalf("acceptance=%+v err=%v", receipt, err)
			}
			select {
			case <-entered:
			case <-ctx.Done():
				t.Fatal("final retry notification was not reached")
			}
			closeCtx, stop := context.WithTimeout(context.Background(), 500*time.Millisecond)
			defer stop()
			if err := c.Close(closeCtx); err != nil {
				t.Fatalf("Close blocked on final-history-%s notification: %v", stage, err)
			}
		})
	}
}
