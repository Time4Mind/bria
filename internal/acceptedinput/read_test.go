package acceptedinput_test

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"bria/internal/acceptedinput"
	"bria/internal/messagejournal"
)

func TestExactCustodySnapshotAllowsNewRootBehindAcceptedPrior(t *testing.T) {
	ctx := context.Background()
	j, err := messagejournal.Open(filepath.Join(t.TempDir(), "journal.json"), messagejournal.DefaultLimits())
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"a", "b"} {
		if _, _, err := j.EnqueueInput(ctx, "s", id, []byte(id)); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := j.LeaseNextInput(ctx, "s", "worker", time.Unix(10, 0), time.Minute); err != nil {
		t.Fatal(err)
	}
	if err := acceptedinput.VerifyAcceptance(ctx, j, "s", "a", 1); !errors.Is(err, acceptedinput.ErrInvalidReceipt) {
		t.Fatal("pending claimed accepted")
	}
	if _, err := j.MarkInputAccepted(ctx, "s", "a", "worker"); err != nil {
		t.Fatal(err)
	}
	if err := acceptedinput.VerifyAcceptance(ctx, j, "s", "a", 1); err != nil {
		t.Fatal(err)
	}
	if _, _, err := acceptedinput.Lookup(ctx, j, "s", "a", 2); !errors.Is(err, acceptedinput.ErrInvalidReceipt) {
		t.Fatal("stale tuple matched")
	}
	if ready, err := acceptedinput.RootReady(ctx, j, "s", "b", 2); !ready || err != nil {
		t.Fatalf("accepted prior blocked new root: %v %v", ready, err)
	}
	if _, err := j.ResolveAcceptedInput(ctx, "s", "a", 1, messagejournal.InputTerminalFailed); err != nil {
		t.Fatal(err)
	}
	if err := acceptedinput.VerifyAcceptance(ctx, j, "s", "a", 1); err != nil {
		t.Fatal(err)
	}
	if ready, err := acceptedinput.RootReady(ctx, j, "s", "b", 2); !ready || err != nil {
		t.Fatalf("terminal blocked root: %v %v", ready, err)
	}
	canceled, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := acceptedinput.RootReady(canceled, j, "s", "b", 2); !errors.Is(err, context.Canceled) {
		t.Fatal("canceled read succeeded")
	}
}

func TestReplayFencedUnknownDoesNotBlockNewerRoot(t *testing.T) {
	ctx := context.Background()
	j, err := messagejournal.Open(filepath.Join(t.TempDir(), "journal.json"), messagejournal.DefaultLimits())
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"old", "next"} {
		if _, _, err := j.EnqueueInput(ctx, "s", id, []byte(id)); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := j.LeaseNextInput(ctx, "s", "worker", time.Unix(10, 0), time.Minute); err != nil {
		t.Fatal(err)
	}
	if _, err := j.MarkInputDeliveryUnknown(ctx, "s", "old", "worker"); err != nil {
		t.Fatal(err)
	}
	if ready, err := acceptedinput.RootReady(ctx, j, "s", "next", 2); err != nil || !ready {
		t.Fatalf("replay-fenced unknown blocked newer root: ready=%t err=%v", ready, err)
	}
	inputs, err := j.Inputs(ctx, "s")
	if err != nil || inputs[0].Phase != messagejournal.InputUnknown {
		t.Fatalf("old unknown lost replay fence: %+v err=%v", inputs, err)
	}
}

func TestSkippedRecoveryInputIsTerminalForAcceptanceAndNewRoot(t *testing.T) {
	ctx := context.Background()
	j, err := messagejournal.Open(filepath.Join(t.TempDir(), "journal.json"), messagejournal.DefaultLimits())
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"old", "next"} {
		if _, _, err := j.EnqueueInput(ctx, "s", id, []byte(id)); err != nil {
			t.Fatal(err)
		}
	}
	if active, err := j.BeginInputRecovery(ctx, "s", "recovery", false); err != nil || !active {
		t.Fatalf("begin recovery = %t, %v", active, err)
	}
	if err := j.CommitInputRecoverySkip(ctx, "s", "recovery"); err != nil {
		t.Fatal(err)
	}
	if err := acceptedinput.VerifyAcceptance(ctx, j, "s", "old", 1); !errors.Is(err, acceptedinput.ErrInvalidReceipt) {
		t.Fatalf("skipped receipt was accepted: %v", err)
	}
	if err := acceptedinput.Commit(ctx, j, "s", "old", 1, "stale-worker"); !errors.Is(err, acceptedinput.ErrInvalidReceipt) {
		t.Fatalf("late acceptance was accepted: %v", err)
	}
	if _, _, err := j.EnqueueInput(ctx, "s", "after", []byte("after")); err != nil {
		t.Fatal(err)
	}
	if ready, err := acceptedinput.RootReady(ctx, j, "s", "after", 3); err != nil || !ready {
		t.Fatalf("skipped prefix blocked new root: ready=%t err=%v", ready, err)
	}
}
