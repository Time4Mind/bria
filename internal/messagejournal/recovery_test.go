package messagejournal_test

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"bria/internal/messagejournal"
)

func TestUnknownResolutionRequiresExactTupleAndNeverRequeues(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "journal.json")
	journal := openJournal(t, path, testLimits())
	input, _, err := journal.EnqueueInput(ctx, "s", "m", []byte("synthetic"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = journal.ResolveUnknownInput(ctx, "s", "m", input.Sequence, messagejournal.InputCompleted); !errors.Is(err, messagejournal.ErrInvalidTransition) {
		t.Fatalf("pending resolved: %v", err)
	}
	if _, err = journal.LeaseNextInput(ctx, "s", "worker", time.Unix(200, 0), time.Minute); err != nil {
		t.Fatal(err)
	}
	if _, err = journal.MarkInputAccepted(ctx, "s", "m", "worker"); err != nil {
		t.Fatal(err)
	}
	if _, err = journal.MarkInputUnknown(ctx, "s", "m"); err != nil {
		t.Fatal(err)
	}
	journal = openJournal(t, path, testLimits())
	if _, err = journal.CompleteInput(ctx, "s", "m"); !errors.Is(err, messagejournal.ErrInvalidTransition) {
		t.Fatalf("ordinary completion bypassed recovery: %v", err)
	}
	if _, err = journal.FailInput(ctx, "s", "m"); !errors.Is(err, messagejournal.ErrInvalidTransition) {
		t.Fatalf("ordinary failure bypassed recovery: %v", err)
	}
	for _, tc := range []struct {
		sequence uint64
		phase    messagejournal.InputPhase
	}{
		{input.Sequence + 1, messagejournal.InputCompleted},
		{0, messagejournal.InputCompleted},
		{input.Sequence, messagejournal.InputPending},
		{input.Sequence, messagejournal.InputAccepted},
	} {
		if _, err = journal.ResolveUnknownInput(ctx, "s", "m", tc.sequence, tc.phase); !errors.Is(err, messagejournal.ErrInvalidTransition) {
			t.Fatalf("unsafe transition succeeded: %v", err)
		}
	}
	if _, err = journal.LeaseNextInput(ctx, "s", "other", time.Unix(999, 0), time.Minute); !errors.Is(err, messagejournal.ErrNoAvailable) {
		t.Fatalf("unknown requeued: %v", err)
	}
	for i := 0; i < 2; i++ {
		journal = openJournal(t, path, testLimits())
		got, err := journal.ResolveUnknownInput(ctx, "s", "m", input.Sequence, messagejournal.InputCompleted)
		if err != nil || got.Phase != messagejournal.InputCompleted || got.Sequence != input.Sequence || string(got.Payload) != "synthetic" {
			t.Fatalf("recovery changed identity: %#v %v", got, err)
		}
	}
	if _, err = journal.ResolveUnknownInput(ctx, "s", "m", input.Sequence, messagejournal.InputFailed); !errors.Is(err, messagejournal.ErrInvalidTransition) {
		t.Fatalf("conflicting terminal overwritten: %v", err)
	}
}
