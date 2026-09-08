package acceptedinput_test

import (
	"context"
	"errors"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"bria/internal/acceptedinput"
	"bria/internal/messagejournal"
)

type blockedAcceptanceJournal struct {
	*messagejournal.Journal
	started, release chan struct{}
	once             sync.Once
	fault            error
}

func (j *blockedAcceptanceJournal) MarkInputAccepted(context.Context, string, string, string) (messagejournal.Input, error) {
	j.once.Do(func() { close(j.started) })
	<-j.release
	return messagejournal.Input{}, j.fault
}

func TestFenceBlocksOnlyObservedAttemptDuringCommitAndAfterExpiry(t *testing.T) {
	ctx := context.Background()
	j, err := messagejournal.Open(filepath.Join(t.TempDir(), "journal.json"), messagejournal.DefaultLimits())
	if err != nil {
		t.Fatal(err)
	}
	for _, session := range []string{"s", "other"} {
		if _, _, err := j.EnqueueInput(ctx, session, "a", []byte("a")); err != nil {
			t.Fatal(err)
		}
		if _, err := j.LeaseNextInput(ctx, session, "worker", time.Unix(10, 0), time.Minute); err != nil {
			t.Fatal(err)
		}
	}
	fault := errors.New("synthetic blocked commit failure")
	blocked := &blockedAcceptanceJournal{Journal: j, started: make(chan struct{}), release: make(chan struct{}), fault: fault}
	var fence acceptedinput.Fence
	done := make(chan error, 1)
	go func() { done <- fence.Commit(ctx, blocked, "s", "a", 1, "worker") }()
	select {
	case <-blocked.started:
	case <-time.After(2 * time.Second):
		t.Fatal("commit did not reach write")
	}
	defer close(blocked.release)
	if _, err := fence.Lease(ctx, j, "s", "new-worker", time.Unix(999, 0), time.Minute); !errors.Is(err, acceptedinput.ErrUncommitted) {
		t.Fatalf("in-flight ACK was replayable: %v", err)
	}
	// The same expired lease without an observed ACK still has normal recovery.
	other, err := fence.Lease(ctx, j, "other", "new-worker", time.Unix(999, 0), time.Minute)
	if err != nil || other.MessageID != "a" || other.Lease.Owner != "new-worker" {
		t.Fatalf("unrelated expired lease blocked: %#v %v", other, err)
	}
	// Release through cleanup, then verify both retry attempts fail without mutation.
	t.Cleanup(func() {
		select {
		case err := <-done:
			if !errors.Is(err, fault) {
				t.Errorf("commit fault lost: %v", err)
			}
		case <-time.After(2 * time.Second):
			t.Error("commit did not finish")
		}
		if _, err := fence.Lease(ctx, j, "s", "new-worker", time.Unix(999, 0), time.Minute); !errors.Is(err, acceptedinput.ErrUncommitted) {
			t.Errorf("failed ACK was replayable: %v", err)
		}
		inputs, err := j.Inputs(ctx, "s")
		if err != nil || len(inputs) != 1 || inputs[0].Phase != messagejournal.InputPending || inputs[0].Lease.Owner != "worker" {
			t.Errorf("lease marker changed: %#v %v", inputs, err)
		}
	})
}
