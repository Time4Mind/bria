package durablecomposition_test

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"bria/internal/durablecomposition"
	"bria/internal/durableflow"
	"bria/internal/messagejournal"
	"bria/internal/telegramcontroller"
	"bria/internal/turnprocessing"
)

func TestReopenedRootCustodyAllowsNewRootWithoutReplayingAcceptedPrior(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "journal.json")
	journal, err := messagejournal.Open(path, messagejournal.DefaultLimits())
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"a", "b"} {
		if _, _, err = journal.EnqueueInput(ctx, "s", id, []byte("synthetic")); err != nil {
			t.Fatal(err)
		}
	}
	if _, err = journal.LeaseNextInput(ctx, "s", "worker", time.Unix(10, 0), time.Minute); err != nil {
		t.Fatal(err)
	}
	if _, err = journal.MarkInputAccepted(ctx, "s", "a", "worker"); err != nil {
		t.Fatal(err)
	}
	journal, err = messagejournal.Open(path, messagejournal.DefaultLimits())
	if err != nil {
		t.Fatal(err)
	}
	flow, err := durableflow.New(journal, nil, nil, durableflow.Options{Owner: "worker", LeaseDuration: time.Minute, Now: func() time.Time { return time.Unix(20, 0) }})
	if err != nil {
		t.Fatal(err)
	}
	custody := durablecomposition.InputCustody{Flow: flow}
	submitted := false
	processor := durablecomposition.NewControllerInputProcessor(asyncProcessor(func(ctx context.Context, in telegramcontroller.DurableLeasedInput, callbacks telegramcontroller.DurableInputCallbacks) (telegramcontroller.DurableInputProcessReceipt, error) {
		if in.MessageID != "b" {
			t.Fatalf("replayed accepted A: %+v", in)
		}
		receipt := telegramcontroller.DurableInputProcessReceipt{SessionID: in.SessionID, MessageID: in.MessageID, Sequence: in.Sequence}
		// Same optional boundary used after the controller's live-steer branch.
		if guard, ok := any(custody).(turnprocessing.DurableRootInputGuard); ok {
			if err := guard.CheckRootInput(ctx, in); err != nil {
				return receipt, err
			}
		}
		submitted = true
		if err := callbacks.OnAccepted(ctx, telegramcontroller.DurableInputAcceptance{SessionID: in.SessionID, MessageID: in.MessageID, Sequence: in.Sequence}); err != nil {
			return receipt, err
		}
		receipt.Accepted, receipt.Completion = true, telegramcontroller.DurableInputSucceeded
		return receipt, nil
	}))
	result, err := flow.ProcessNextInput(ctx, "s", processor)
	if err != nil || result.State != durableflow.InputProcessCompleted || !submitted {
		t.Fatalf("fresh root remained blocked or replayed A: result=%+v err=%v submitted=%t", result, err, submitted)
	}
	inputs, err := journal.Inputs(ctx, "s")
	if err != nil || inputs[0].Phase != messagejournal.InputAccepted || inputs[1].Phase != messagejournal.InputCompleted {
		t.Fatalf("new root custody: %+v %v", inputs, err)
	}
	if _, err = flow.ProcessNextInput(ctx, "s", processor); !errors.Is(err, messagejournal.ErrNoAvailable) {
		t.Fatalf("duplicate submission: %v", err)
	}
}
