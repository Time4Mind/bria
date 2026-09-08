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

func TestKnownUnsentDeferredInputRemainsPendingBehindAcceptedUntilRecovery(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "journal.json")
	journal := openJournal(t, path)
	flow := newFlow(t, journal, nil, nil, time.Unix(10, 0))
	for _, id := range []string{"a", "b", "c"} {
		if _, err := flow.EnqueueInput(ctx, "s", id, []byte("synthetic")); err != nil {
			t.Fatal(err)
		}
	}
	first, err := journal.LeaseNextInput(ctx, "s", "worker-a", time.Unix(10, 0), time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := journal.MarkInputAccepted(ctx, "s", "a", "worker-a"); err != nil {
		t.Fatal(err)
	}
	processor := inputProcessorFunc(func(ctx context.Context, input durableflow.ProviderInput, callbacks durableflow.InputProcessCallbacks) (durableflow.InputProcessResult, error) {
		if input.MessageID != "b" {
			t.Fatalf("wrong waiting input: %#v", input)
		}
		if _, err := journal.MarkInputUnknown(ctx, "s", "a"); err != nil {
			t.Fatal(err)
		}
		return durableflow.InputProcessResult{SessionID: input.SessionID, MessageID: input.MessageID, Sequence: input.Sequence, State: "deferred"}, nil
	})
	result, err := flow.ProcessNextInput(ctx, "s", processor)
	if err != nil || result.State != "deferred" {
		t.Fatalf("unsent deferred outcome: %#v %v", result, err)
	}
	journal = openJournal(t, path)
	inputs, err := journal.Inputs(ctx, "s")
	if err != nil || len(inputs) != 3 || inputs[0].Phase != messagejournal.InputAccepted || inputs[1].Phase != messagejournal.InputPending || inputs[1].Lease.Owner != "" || inputs[2].Phase != messagejournal.InputPending {
		t.Fatalf("unsent B lost custody: %#v %v", inputs, err)
	}
	if ready, err := rootInputReady(flow, ctx, "s", "b", 2); err != nil || ready {
		t.Fatalf("accepted A admitted new root: %v %v", ready, err)
	}
	if _, err := journal.ResolveAcceptedInput(ctx, "s", "a", first.Sequence, messagejournal.InputTerminalFailed); err != nil {
		t.Fatal(err)
	}
	next, err := journal.LeaseNextInput(ctx, "s", "later", time.Unix(100, 0), time.Minute)
	if err != nil || next.MessageID != "b" || next.Sequence != 2 {
		t.Fatalf("recovery did not release B in order: %#v %v", next, err)
	}
}

func TestAcceptedOrMismatchedReceiptMustNeverReleaseDeferredLease(t *testing.T) {
	for _, mode := range []string{"accepted", "wrong-session", "wrong-message", "wrong-sequence", "process-error"} {
		t.Run(mode, func(t *testing.T) {
			ctx := context.Background()
			path := filepath.Join(t.TempDir(), "journal.json")
			journal := openJournal(t, path)
			flow := newFlow(t, journal, nil, nil, time.Unix(10, 0))
			if _, err := flow.EnqueueInput(ctx, "s", "m", []byte("synthetic")); err != nil {
				t.Fatal(err)
			}
			processor := inputProcessorFunc(func(ctx context.Context, input durableflow.ProviderInput, cb durableflow.InputProcessCallbacks) (durableflow.InputProcessResult, error) {
				result := durableflow.InputProcessResult{SessionID: input.SessionID, MessageID: input.MessageID, Sequence: input.Sequence, State: "deferred"}
				switch mode {
				case "accepted":
					if err := cb.OnAccepted(ctx, handoffReceipt(input, durableflow.HandoffAccepted)); err != nil {
						t.Fatal(err)
					}
				case "wrong-session":
					result.SessionID = "other"
				case "wrong-message":
					result.MessageID = "other"
				case "wrong-sequence":
					result.Sequence++
				case "process-error":
					return result, errors.New("ambiguous provider failure")
				}
				return result, nil
			})
			result, err := flow.ProcessNextInput(ctx, "s", processor)
			want := messagejournal.InputUnknown
			if mode == "accepted" {
				want = messagejournal.InputAccepted
			}
			if string(result.State) != string(want) || err == nil {
				t.Fatalf("invalid deferral: %#v %v", result, err)
			}
			inputs, err := openJournal(t, path).Inputs(ctx, "s")
			if err != nil || len(inputs) != 1 || inputs[0].Phase != want {
				t.Fatalf("unsafe requeue: %#v %v", inputs, err)
			}
			if _, err := journal.LeaseNextInput(ctx, "s", "other", time.Unix(100, 0), time.Minute); !errors.Is(err, messagejournal.ErrNoAvailable) {
				t.Fatalf("invalid deferred input replayable: %v", err)
			}
		})
	}
}
