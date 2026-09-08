package durableflow_test

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"bria/internal/durableflow"
	"bria/internal/messagejournal"
)

func TestAsyncCompletionDuringProcessorReturnPreservesTerminal(t *testing.T) {
	for _, outcome := range []durableflow.InputProcessState{durableflow.InputProcessCompleted, "terminal_failed", durableflow.InputProcessFailed, durableflow.InputProcessUnknown} {
		t.Run(string(outcome), func(t *testing.T) {
			ctx := context.Background()
			path := filepath.Join(t.TempDir(), "journal.json")
			journal := openJournal(t, path)
			flow := newFlow(t, journal, nil, nil, time.Unix(10, 0))
			if _, err := flow.EnqueueInput(ctx, "s", "m", []byte("synthetic")); err != nil {
				t.Fatal(err)
			}
			done := make(chan error, 1)
			processor := inputProcessorFunc(func(ctx context.Context, input durableflow.ProviderInput, callbacks durableflow.InputProcessCallbacks) (durableflow.InputProcessResult, error) {
				receipt := durableflow.InputProcessResult{SessionID: input.SessionID, MessageID: input.MessageID, Sequence: input.Sequence, State: outcome}
				if err := callbacks.OnCompleted(ctx, receipt); err == nil {
					t.Fatal("unaccepted pending input completed")
				}
				if err := callbacks.OnAccepted(ctx, handoffReceipt(input, durableflow.HandoffAccepted)); err != nil {
					return receipt, err
				}
				go func() { done <- callbacks.OnCompleted(ctx, receipt) }()
				return durableflow.InputProcessResult{SessionID: input.SessionID, MessageID: input.MessageID, Sequence: input.Sequence, State: durableflow.InputProcessAccepted}, nil
			})
			result, err := flow.ProcessNextInput(ctx, "s", processor)
			if err != nil || result.State != durableflow.InputProcessAccepted {
				t.Fatalf("early result: %#v %v", result, err)
			}
			if err := <-done; err != nil {
				t.Fatal(err)
			}
			inputs, err := openJournal(t, path).Inputs(ctx, "s")
			if err != nil || len(inputs) != 1 || inputs[0].Phase != messagejournal.InputPhase(outcome) {
				t.Fatalf("lost terminal: %#v %v", inputs, err)
			}
			if _, err := journal.LeaseNextInput(ctx, "s", "other", time.Unix(999, 0), time.Minute); !errors.Is(err, messagejournal.ErrNoAvailable) {
				t.Fatalf("replay after terminal: %v", err)
			}
		})
	}
}

func TestLateCompletionRequiresThisProcessorsDurableAcceptance(t *testing.T) {
	for _, terminal := range []durableflow.InputProcessState{durableflow.InputProcessCompleted, durableflow.InputProcessTerminalFailed} {
		t.Run(string(terminal), func(t *testing.T) { lateCompletionRequiresAcceptance(t, terminal) })
	}
}

func lateCompletionRequiresAcceptance(t *testing.T, terminal durableflow.InputProcessState) {
	t.Helper()
	for _, accepted := range []bool{false, true} {
		name := "unaccepted"
		if accepted {
			name = "accepted"
		}
		t.Run(name, func(t *testing.T) {
			ctx := context.Background()
			path := filepath.Join(t.TempDir(), "journal.json")
			journal := openJournal(t, path)
			flow := newFlow(t, journal, nil, nil, time.Unix(10, 0))
			if _, err := flow.EnqueueInput(ctx, "s", "m", []byte("synthetic")); err != nil {
				t.Fatal(err)
			}
			var late func(context.Context, durableflow.InputProcessResult) error
			var receipt durableflow.InputProcessResult
			processor := inputProcessorFunc(func(ctx context.Context, input durableflow.ProviderInput, callbacks durableflow.InputProcessCallbacks) (durableflow.InputProcessResult, error) {
				late = callbacks.OnCompleted
				receipt = durableflow.InputProcessResult{SessionID: input.SessionID, MessageID: input.MessageID, Sequence: input.Sequence, State: terminal}
				if accepted {
					if err := callbacks.OnAccepted(ctx, handoffReceipt(input, durableflow.HandoffAccepted)); err != nil {
						return receipt, err
					}
				}
				return receipt, errors.New("synthetic processor failure")
			})
			if _, err := flow.ProcessNextInput(ctx, "s", processor); err == nil {
				t.Fatal("missing process failure")
			}
			before, err := openJournal(t, path).Inputs(ctx, "s")
			if err != nil || len(before) != 1 || before[0].Phase != messagejournal.InputUnknown {
				t.Fatalf("before: %#v %v", before, err)
			}
			lateErr := late(ctx, receipt)
			after, err := openJournal(t, path).Inputs(ctx, "s")
			want := messagejournal.InputUnknown
			if accepted {
				want = messagejournal.InputPhase(terminal)
			}
			if err != nil || len(after) != 1 || after[0].Phase != want || (lateErr == nil) != accepted {
				t.Fatalf("late completion: accepted=%t error=%v physical=%#v read=%v", accepted, lateErr, after, err)
			}
			if _, err := journal.LeaseNextInput(ctx, "s", "other", time.Unix(999, 0), time.Minute); !errors.Is(err, messagejournal.ErrNoAvailable) {
				t.Fatalf("input replayable: %v", err)
			}
		})
	}
}

func TestExactAcceptanceReplayAfterTerminalFailurePreservesAckWithoutReplay(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "journal.json")
	journal := openJournal(t, path)
	flow := newFlow(t, journal, nil, nil, time.Unix(10, 0))
	for _, id := range []string{"a", "b"} {
		if _, err := flow.EnqueueInput(ctx, "s", id, []byte("synthetic")); err != nil {
			t.Fatal(err)
		}
	}
	processor := inputProcessorFunc(func(ctx context.Context, input durableflow.ProviderInput, callbacks durableflow.InputProcessCallbacks) (durableflow.InputProcessResult, error) {
		if err := callbacks.OnAccepted(ctx, handoffReceipt(input, durableflow.HandoffAccepted)); err != nil {
			return durableflow.InputProcessResult{}, err
		}
		return durableflow.InputProcessResult{SessionID: input.SessionID, MessageID: input.MessageID, Sequence: input.Sequence, State: durableflow.InputProcessTerminalFailed}, nil
	})
	terminal, err := flow.ProcessNextInput(ctx, "s", processor)
	if err != nil || terminal.State != durableflow.InputProcessTerminalFailed {
		t.Fatalf("terminal: %#v %v", terminal, err)
	}
	for i := 0; i < 2; i++ {
		journal = openJournal(t, path)
		flow = newFlow(t, journal, nil, nil, time.Unix(100, 0))
		if _, err := flow.RecordLeasedInputAccepted(ctx, "s", "a", terminal.Sequence+1); !errors.Is(err, durableflow.ErrInvalidHandoff) {
			t.Fatalf("stale acceptance: %v", err)
		}
		ack, err := flow.RecordLeasedInputAccepted(ctx, "s", "a", terminal.Sequence)
		if err != nil || ack.State != durableflow.HandoffAccepted || ack.MessageID != "a" || ack.Sequence != terminal.Sequence {
			t.Fatalf("lost exact acceptance ack: %#v %v", ack, err)
		}
		inputs, err := journal.Inputs(ctx, "s")
		if err != nil || len(inputs) != 2 || inputs[0].Phase != messagejournal.InputTerminalFailed || inputs[1].Phase != messagejournal.InputPending {
			t.Fatalf("acceptance replay changed journal: %#v %v", inputs, err)
		}
	}
	if err := flow.RetryInput(ctx, "s", "a"); !errors.Is(err, messagejournal.ErrInvalidTransition) {
		t.Fatalf("old terminal replayable: %v", err)
	}
	next, err := journal.LeaseNextInput(ctx, "s", "worker", time.Unix(100, 0), time.Minute)
	if err != nil || next.MessageID != "b" {
		t.Fatalf("wrong next input: %#v %v", next, err)
	}
}
