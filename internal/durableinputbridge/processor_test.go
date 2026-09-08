package durableinputbridge_test

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"

	"bria/internal/domain"
	"bria/internal/durableflow"
	"bria/internal/durableinputbridge"
	"bria/internal/turnprocessing"
)

type processorFunc func(context.Context, turnprocessing.DurableLeasedInput, turnprocessing.DurableInputCallbacks) (turnprocessing.DurableInputProcessReceipt, error)

func (f processorFunc) ProcessDurableInput(ctx context.Context, input turnprocessing.DurableLeasedInput, cb turnprocessing.DurableInputCallbacks) (turnprocessing.DurableInputProcessReceipt, error) {
	return f(ctx, input, cb)
}

func TestProcessorPreservesSynchronousAndPendingReceiptSemantics(t *testing.T) {
	for _, tc := range []struct {
		completion turnprocessing.DurableInputCompletion
		state      durableflow.InputProcessState
	}{
		{turnprocessing.DurableInputSucceeded, durableflow.InputProcessCompleted},
		{turnprocessing.DurableInputFailed, durableflow.InputProcessFailed},
		{turnprocessing.DurableInputTerminalFailed, durableflow.InputProcessTerminalFailed},
		{turnprocessing.DurableInputPending, durableflow.InputProcessAccepted},
		{turnprocessing.DurableInputUnknown, durableflow.InputProcessUnknown},
	} {
		t.Run(string(tc.completion), func(t *testing.T) {
			processor := durableinputbridge.New(processorFunc(func(ctx context.Context, input turnprocessing.DurableLeasedInput, cb turnprocessing.DurableInputCallbacks) (turnprocessing.DurableInputProcessReceipt, error) {
				return turnprocessing.DurableInputProcessReceipt{SessionID: input.SessionID, MessageID: input.MessageID, Sequence: input.Sequence, Accepted: true, Completion: tc.completion}, nil
			}))
			got, err := processor.Process(context.Background(), durableflow.ProviderInput{SessionID: "s", MessageID: "m", Sequence: 7}, durableflow.InputProcessCallbacks{OnAccepted: func(context.Context, durableflow.HandoffResult) error { return nil }})
			if err != nil || got.SessionID != "s" || got.MessageID != "m" || got.Sequence != 7 || got.State != tc.state {
				t.Fatalf("receipt changed: %#v %v", got, err)
			}
		})
	}
}

func TestCompletionNotifierFollowsSuccessfulDurableCommitOnce(t *testing.T) {
	for _, terminal := range []turnprocessing.DurableInputCompletion{turnprocessing.DurableInputSucceeded, turnprocessing.DurableInputTerminalFailed} {
		t.Run(string(terminal), func(t *testing.T) { completionNotifierFollowsCommit(t, terminal) })
	}
}

func completionNotifierFollowsCommit(t *testing.T, terminal turnprocessing.DurableInputCompletion) {
	t.Helper()
	ctx := context.Background()
	var complete func(context.Context, turnprocessing.DurableInputProcessReceipt) error
	var receipt turnprocessing.DurableInputProcessReceipt
	wakes := make(chan domain.SessionID, 16)
	var fail, committed atomic.Bool
	fail.Store(true)
	processor := durableinputbridge.New(processorFunc(func(ctx context.Context, input turnprocessing.DurableLeasedInput, cb turnprocessing.DurableInputCallbacks) (turnprocessing.DurableInputProcessReceipt, error) {
		complete = cb.OnCompleted
		receipt = turnprocessing.DurableInputProcessReceipt{SessionID: input.SessionID, MessageID: input.MessageID, Sequence: input.Sequence, Accepted: true, Completion: turnprocessing.DurableInputPending}
		return receipt, nil
	}), func(id domain.SessionID) {
		if !committed.Load() {
			t.Error("notified before durable callback succeeded")
		}
		wakes <- id
	})
	_, err := processor.Process(ctx, durableflow.ProviderInput{SessionID: "exact-session", MessageID: "m", Sequence: 1}, durableflow.InputProcessCallbacks{
		OnAccepted: func(context.Context, durableflow.HandoffResult) error { return nil },
		OnCompleted: func(context.Context, durableflow.InputProcessResult) error {
			if fail.Load() {
				return errors.New("synthetic journal failure")
			}
			committed.Store(true)
			return nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	receipt.Completion = terminal
	if err := complete(ctx, receipt); err == nil {
		t.Fatal("missing commit failure")
	}
	select {
	case <-wakes:
		t.Fatal("failed commit woke dispatcher")
	default:
	}
	fail.Store(false)
	for _, outcome := range []turnprocessing.DurableInputCompletion{turnprocessing.DurableInputFailed, turnprocessing.DurableInputUnknown} {
		blocked := receipt
		blocked.Completion = outcome
		if err := complete(ctx, blocked); err != nil {
			t.Fatal(err)
		}
		select {
		case <-wakes:
			t.Fatal("blocked outcome woke dispatcher")
		default:
		}
	}
	bad := receipt
	bad.SessionID = "wrong-session"
	if err := complete(ctx, bad); err == nil {
		t.Fatal("wrong-session terminal accepted")
	}
	select {
	case <-wakes:
		t.Fatal("wrong session woke dispatcher")
	default:
	}
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := complete(ctx, receipt); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	select {
	case id := <-wakes:
		if id != "exact-session" {
			t.Fatalf("woke %q", id)
		}
	default:
		t.Fatal("missing terminal wake")
	}
	select {
	case <-wakes:
		t.Fatal("duplicate terminal wake")
	default:
	}
}
