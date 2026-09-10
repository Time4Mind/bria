package telegramcontroller_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"bria/internal/domain"
	"bria/internal/telegramcontroller"
)

type failingRecoveryProjection struct {
	projectionUIState
	fail bool
}

func (state *failingRecoveryProjection) LoadCardHistory(ctx context.Context, id domain.SessionID) ([]string, error) {
	if state.fail {
		return nil, errors.New("synthetic projection failure")
	}
	return state.projectionUIState.LoadCardHistory(ctx, id)
}

func TestAutomaticRecoveryKeepsNewInputInCustodyWithoutSubmitting(t *testing.T) {
	ctx := context.Background()
	ready := readySession(t, "aaaaaaaa-aaaa-4aaa-9aaa-aaaaaaaaaaaa", domain.ProviderCodex, t.TempDir(), "same-cli", 1)
	running, err := ready.StartWork(time.Now())
	if err != nil {
		t.Fatal(err)
	}
	awaiting, err := running.AwaitRecoveryAt(time.Now())
	if err != nil {
		t.Fatal(err)
	}
	for _, automatic := range []bool{false, true} {
		t.Run(map[bool]string{false: "no-auto-recoverer", true: "auto-recoverer"}[automatic], func(t *testing.T) {
			var inputs []telegramcontroller.SessionInput
			provider := &acceptedObserver{}
			options := telegramcontroller.Options{DurableInput: durableInputFunc(func(_ context.Context, input telegramcontroller.SessionInput) (telegramcontroller.InputReceipt, error) {
				inputs = append(inputs, input)
				return telegramcontroller.InputReceipt{SessionID: input.SessionID, MessageID: input.MessageID, Sequence: 2, Inserted: true}, nil
			})}
			if automatic {
				options.Recoverer = deferredRecoverer(func(context.Context, domain.SessionID) (domain.Session, error) { return awaiting, nil })
			}
			c := newController(t, nil, newLockedSessions(awaiting), provider, nil, options)
			defer c.Close(ctx)
			if _, err := c.HandleSemanticAction(ctx, telegramcontroller.SemanticAction{Kind: telegramcontroller.SemanticSelect, SessionID: awaiting.ID()}); err != nil {
				t.Fatal(err)
			}
			result, err := c.Handle(ctx, message(73, "next exact request"))
			if err != nil {
				t.Fatal(err)
			}
			if automatic {
				if len(inputs) != 1 || inputs[0].SessionID != awaiting.ID() || inputs[0].MessageID != "telegram-update:73" || string(inputs[0].Payload) != "next exact request" {
					t.Fatal("new request was discarded instead of durably queued")
				}
				if strings.Contains(result.Status.Text, "Выбери восстановление") {
					t.Fatal("automatic recovery demands manual intervention")
				}
			} else if len(inputs) != 0 {
				t.Fatal("unavailable recovery accepted unsupported input")
			}
			if provider.submits.Load() != 0 {
				t.Fatal("queued input forwarded before exact attach")
			}
		})
	}
}

func TestAutomaticRecoveryDoesNotRejectDurablyQueuedInputWhenCardProjectionFails(t *testing.T) {
	ctx := context.Background()
	ready := readySession(t, "bbbbbbbb-bbbb-4bbb-9bbb-bbbbbbbbbbbb", domain.ProviderCodex, t.TempDir(), "same-cli", 1)
	running, err := ready.StartWork(time.Now())
	if err != nil {
		t.Fatal(err)
	}
	awaiting, err := running.AwaitRecoveryAt(time.Now())
	if err != nil {
		t.Fatal(err)
	}
	var inputs []telegramcontroller.SessionInput
	ui := &failingRecoveryProjection{}
	c := newController(t, nil, newLockedSessions(awaiting), &acceptedObserver{}, nil, telegramcontroller.Options{
		UIState: ui,
		DurableInput: durableInputFunc(func(_ context.Context, input telegramcontroller.SessionInput) (telegramcontroller.InputReceipt, error) {
			inputs = append(inputs, input)
			return telegramcontroller.InputReceipt{SessionID: input.SessionID, MessageID: input.MessageID, Sequence: 3, Inserted: true}, nil
		}),
		Recoverer: deferredRecoverer(func(context.Context, domain.SessionID) (domain.Session, error) { return awaiting, nil }),
	})
	defer c.Close(ctx)
	if _, err := c.HandleSemanticAction(ctx, telegramcontroller.SemanticAction{Kind: telegramcontroller.SemanticSelect, SessionID: awaiting.ID()}); err != nil {
		t.Fatal(err)
	}
	ui.fail = true
	result, err := c.Handle(ctx, message(74, "durable while recovery waits"))
	if err != nil || result.Kind != "skip" {
		t.Fatalf("durable input was turned into a poison update: result=%#v err=%v", result, err)
	}
	if len(inputs) != 1 || inputs[0].SessionID != awaiting.ID() || inputs[0].MessageID != "telegram-update:74" {
		t.Fatalf("durable input=%#v, want exact queued identity", inputs)
	}
}
