package telegramcontroller_test

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	"bria/internal/domain"
	"bria/internal/sessionruntime"
	"bria/internal/telegramcontroller"
	"bria/internal/turnprocessing"
)

type rootGuardCustody struct {
	check func(context.Context, turnprocessing.DurableLeasedInput) error
}

func (rootGuardCustody) Accept(context.Context, turnprocessing.SessionInput) (turnprocessing.InputReceipt, error) {
	return turnprocessing.InputReceipt{}, errors.New("unexpected enqueue")
}
func (g rootGuardCustody) CheckRootInput(ctx context.Context, input turnprocessing.DurableLeasedInput) error {
	return g.check(ctx, input)
}

func TestRootInputGuardRejectsExactUnsentInputBeforeProvider(t *testing.T) {
	for _, fault := range []error{turnprocessing.ErrInputDeferred, errors.New("synthetic journal read failure"), context.Canceled} {
		t.Run(fault.Error(), func(t *testing.T) {
			ready := readySession(t, "aaaaaaaa-aaaa-4aaa-9aaa-aaaaaaaaaaaa", domain.ProviderCodex, t.TempDir(), "provider-a", 1)
			input := telegramcontroller.DurableLeasedInput{SessionID: ready.ID(), MessageID: "B", Sequence: 2, Payload: []byte("unsent")}
			guarded, sent := false, false
			guard := rootGuardCustody{check: func(ctx context.Context, got turnprocessing.DurableLeasedInput) error {
				guarded = true
				if ctx.Err() != nil || !reflect.DeepEqual(got, input) {
					t.Error("root guard received inexact input or cancelled context")
				}
				return fault
			}}
			provider := &interactiveSubmitter{submitWithCallbacks: func(context.Context, domain.SessionID, string, sessionruntime.TurnCallbacks) (sessionruntime.TurnResult, error) {
				sent = true
				return sessionruntime.TurnResult{}, errors.New("provider should not receive B")
			}}
			c := newController(t, nil, newLockedSessions(ready), provider, nil, telegramcontroller.Options{Recovered: []domain.Session{ready}, DurableInput: guard})
			defer c.Close(context.Background())
			receipt, err := c.ProcessDurableInput(context.Background(), input, telegramcontroller.DurableInputCallbacks{OnAccepted: func(context.Context, telegramcontroller.DurableInputAcceptance) error {
				t.Error("guarded input was accepted")
				return nil
			}})
			if !guarded || sent || !errors.Is(err, fault) || receipt.Accepted || receipt.SessionID != input.SessionID || receipt.MessageID != input.MessageID || receipt.Sequence != input.Sequence {
				t.Fatalf("guarded=%t sent=%t receipt=%+v err=%v", guarded, sent, receipt, err)
			}
		})
	}
}

func TestRootInputGuardAllowsRootButDoesNotBlockLiveSteer(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	ready := readySession(t, "aaaaaaaa-aaaa-4aaa-9aaa-aaaaaaaaaaaa", domain.ProviderCodex, t.TempDir(), "provider-a", 1)
	release := make(chan struct{})
	guarded := []string{}
	guard := rootGuardCustody{check: func(_ context.Context, input turnprocessing.DurableLeasedInput) error {
		guarded = append(guarded, input.MessageID)
		if input.MessageID != "root" {
			return turnprocessing.ErrInputDeferred
		}
		return nil
	}}
	c := newController(t, nil, newLockedSessions(ready), terminalFailureProvider{release: release, status: sessionruntime.StatusFailed}, nil, telegramcontroller.Options{Recovered: []domain.Session{ready}, DurableInput: guard})
	defer func() { close(release); _ = c.Close(context.Background()) }()
	for i, message := range []string{"root", "steer"} {
		receipt, err := c.ProcessDurableInput(ctx, telegramcontroller.DurableLeasedInput{SessionID: ready.ID(), MessageID: message, Sequence: uint64(i + 1), Payload: []byte(message)}, telegramcontroller.DurableInputCallbacks{OnAccepted: func(context.Context, telegramcontroller.DurableInputAcceptance) error { return nil }})
		if err != nil || !receipt.Accepted {
			t.Fatalf("live %s blocked: %+v %v", message, receipt, err)
		}
	}
	if !reflect.DeepEqual(guarded, []string{"root"}) {
		t.Fatalf("guard must apply to root only: %v", guarded)
	}
}
