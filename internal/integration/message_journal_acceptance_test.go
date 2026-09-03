package integration_test

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"bria/internal/messagejournal"
)

// TestMessageJournalSurvivesReopenWithoutReorderingOrRetryingUnknownDelivery
// covers the durable hand-off contract at its public boundary. Input and
// output share one per-session sequence, and an ambiguous external write must
// remain blocked after process restart until a caller explicitly retries it.
func TestMessageJournalSurvivesReopenWithoutReorderingOrRetryingUnknownDelivery(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "messages.json")
	journal, err := messagejournal.Open(path, messagejournal.DefaultLimits())
	if err != nil {
		t.Fatalf("open empty journal: %v", err)
	}

	firstInput, inserted, err := journal.EnqueueInput(ctx, "session-1", "message-1", []byte("first"))
	if err != nil || !inserted {
		t.Fatalf("enqueue first input = (%#v, %v, %v), want inserted", firstInput, inserted, err)
	}
	firstOutput, inserted, err := journal.EnqueueOutput(ctx, "session-1", "operation-1", "final", []byte("answer"))
	if err != nil || !inserted {
		t.Fatalf("enqueue first output = (%#v, %v, %v), want inserted", firstOutput, inserted, err)
	}
	secondInput, inserted, err := journal.EnqueueInput(ctx, "session-1", "message-2", []byte("second"))
	if err != nil || !inserted {
		t.Fatalf("enqueue second input = (%#v, %v, %v), want inserted", secondInput, inserted, err)
	}
	secondOutput, inserted, err := journal.EnqueueOutput(ctx, "session-1", "operation-2", "file", []byte("artifact"))
	if err != nil || !inserted {
		t.Fatalf("enqueue second output = (%#v, %v, %v), want inserted", secondOutput, inserted, err)
	}
	if got, want := []uint64{firstInput.Sequence, firstOutput.Sequence, secondInput.Sequence, secondOutput.Sequence}, []uint64{1, 2, 3, 4}; !equalUint64s(got, want) {
		t.Fatalf("cross-direction sequence = %v, want %v", got, want)
	}

	now := time.Date(2026, 9, 2, 12, 0, 0, 0, time.UTC)
	leased, err := journal.LeaseNextOutput(ctx, "session-1", "telegram-worker", now, time.Minute)
	if err != nil || leased.OperationID != firstOutput.OperationID {
		t.Fatalf("lease first output = (%#v, %v), want operation-1", leased, err)
	}
	unknown, err := journal.MarkOutputUnknown(ctx, "session-1", firstOutput.OperationID, "telegram-worker")
	if err != nil || unknown.Phase != messagejournal.OutputUnknown {
		t.Fatalf("mark output unknown = (%#v, %v)", unknown, err)
	}

	reopened, err := messagejournal.Open(path, messagejournal.DefaultLimits())
	if err != nil {
		t.Fatalf("reopen journal: %v", err)
	}
	inputs, err := reopened.Inputs(ctx, "session-1")
	if err != nil {
		t.Fatalf("read reopened inputs: %v", err)
	}
	if len(inputs) != 2 || inputs[0].MessageID != "message-1" || inputs[0].Sequence != 1 ||
		inputs[1].MessageID != "message-2" || inputs[1].Sequence != 3 {
		t.Fatalf("reopened inputs = %#v, want original ordered records at sequences 1 and 3", inputs)
	}
	outputs, err := reopened.Outputs(ctx, "session-1")
	if err != nil {
		t.Fatalf("read reopened outputs: %v", err)
	}
	if len(outputs) != 2 || outputs[0].OperationID != "operation-1" || outputs[0].Sequence != 2 ||
		outputs[0].Phase != messagejournal.OutputUnknown || outputs[1].OperationID != "operation-2" ||
		outputs[1].Sequence != 4 || outputs[1].Phase != messagejournal.OutputPending {
		t.Fatalf("reopened outputs = %#v, want unknown operation-1 before pending operation-2", outputs)
	}
	if _, err := reopened.LeaseNextOutput(ctx, "session-1", "restart-worker", now.Add(2*time.Minute), time.Minute); !errors.Is(err, messagejournal.ErrNoAvailable) {
		t.Fatalf("automatic lease after unknown = %v, want ErrNoAvailable", err)
	}

	retried, err := reopened.RetryOutput(ctx, "session-1", firstOutput.OperationID)
	if err != nil || retried.Phase != messagejournal.OutputPending || retried.Sequence != firstOutput.Sequence {
		t.Fatalf("explicit retry = (%#v, %v), want same sequence pending", retried, err)
	}
	leased, err = reopened.LeaseNextOutput(ctx, "session-1", "restart-worker", now.Add(2*time.Minute), time.Minute)
	if err != nil || leased.OperationID != firstOutput.OperationID || leased.Sequence != firstOutput.Sequence {
		t.Fatalf("lease after explicit retry = (%#v, %v), want original operation-1", leased, err)
	}
}

func equalUint64s(left, right []uint64) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}
