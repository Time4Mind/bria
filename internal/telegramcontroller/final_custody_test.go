package telegramcontroller_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"bria/internal/domain"
	"bria/internal/sessionruntime"
	"bria/internal/telegramcontroller"
	"bria/internal/turnprocessing"
)

func TestFinalCustodyFailureCannotCompleteAcceptedInputOrReleaseNextRoot(t *testing.T) {
	for _, invalidReceipt := range []bool{false, true} {
		ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
		defer cancel()
		ready := readySession(t, "aaaaaaaa-aaaa-4aaa-9aaa-aaaaaaaaaaaa", domain.ProviderCodex, t.TempDir(), "provider-a", 1)
		providerCalls := make(chan string, 4)
		provider := &interactiveSubmitter{submitWithCallbacks: func(_ context.Context, _ domain.SessionID, _ string, cb sessionruntime.TurnCallbacks) (sessionruntime.TurnResult, error) {
			providerCalls <- cb.MessageID
			if err := cb.OnAccepted(cb.MessageID); err != nil {
				return sessionruntime.TurnResult{}, err
			}
			return sessionruntime.TurnResult{TerminalStatus: sessionruntime.StatusCompleted, Final: "saved answer"}, nil
		}}
		c := newController(t, nil, newLockedSessions(ready), provider, nil, telegramcontroller.Options{
			Recovered: []domain.Session{ready}, UIState: &retryFinalState{attempt: make(chan struct{}, 8)},
			DurableOutput: durableOutputFunc(func(_ context.Context, out telegramcontroller.OutgoingNotification) (telegramcontroller.OutputReceipt, error) {
				if out.Kind == telegramcontroller.NotificationFinal {
					if invalidReceipt {
						return telegramcontroller.OutputReceipt{SessionID: out.SessionID, OperationID: "other:final", Sequence: 1}, nil
					}
					return telegramcontroller.OutputReceipt{}, errors.New("synthetic journal unavailable")
				}
				return telegramcontroller.OutputReceipt{SessionID: out.SessionID, OperationID: out.OperationID, Sequence: 1}, nil
			}),
		})
		defer c.Close(context.Background())
		completed := make(chan telegramcontroller.DurableInputProcessReceipt, 2)
		callbacks := telegramcontroller.DurableInputCallbacks{
			OnAccepted: func(context.Context, telegramcontroller.DurableInputAcceptance) error { return nil },
			OnCompleted: func(_ context.Context, receipt telegramcontroller.DurableInputProcessReceipt) error {
				completed <- receipt
				return nil
			},
		}
		input := telegramcontroller.DurableLeasedInput{SessionID: ready.ID(), MessageID: "root-A", Sequence: 1, Payload: []byte("question")}
		if receipt, err := c.ProcessDurableInput(ctx, input, callbacks); err != nil || !receipt.Accepted {
			t.Fatalf("acceptance=%+v err=%v", receipt, err)
		}
		select {
		case receipt := <-completed:
			if !receipt.Accepted || receipt.Completion != telegramcontroller.DurableInputAwaitingRecovery {
				t.Fatalf("final without durable output completed input: %+v", receipt)
			}
		case <-ctx.Done():
			t.Fatal("accepted outcome did not settle")
		}
		input.MessageID, input.Sequence = "root-B", 2
		if _, err := c.ProcessDurableInput(ctx, input, callbacks); !errors.Is(err, turnprocessing.ErrInputDeferred) {
			t.Fatalf("next root was not deferred: %v", err)
		}
		if len(providerCalls) != 1 {
			t.Fatal("provider replayed or accepted next root without final output custody")
		}
	}
}
