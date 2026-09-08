package messagejournal_test

import (
	"context"
	"errors"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"bria/internal/messagejournal"
)

func TestProvenTerminalFailureAdvancesQueueWithoutReplayAfterReopen(t *testing.T) {
	ctx := context.Background()
	for _, prior := range []messagejournal.InputPhase{messagejournal.InputAccepted, messagejournal.InputUnknown, messagejournal.InputFailed} {
		t.Run(string(prior), func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "journal.json")
			journal := openJournal(t, path, testLimits())
			for _, id := range []string{"a", "b", "c"} {
				if _, _, err := journal.EnqueueInput(ctx, "s", id, []byte("synthetic")); err != nil {
					t.Fatal(err)
				}
			}
			first, err := journal.LeaseNextInput(ctx, "s", "worker", time.Unix(10, 0), time.Minute)
			if err != nil {
				t.Fatal(err)
			}
			if prior == messagejournal.InputUnknown {
				_, err = journal.MarkInputDeliveryUnknown(ctx, "s", "a", "worker")
			} else {
				_, err = journal.MarkInputAccepted(ctx, "s", "a", "worker")
			}
			if prior == messagejournal.InputFailed {
				_, err = journal.FailInput(ctx, "s", "a")
			}
			if err != nil {
				t.Fatal(err)
			}
			if prior != messagejournal.InputAccepted {
				if _, err := journal.LeaseNextInput(ctx, "s", "other", time.Unix(100, 0), time.Minute); !errors.Is(err, messagejournal.ErrNoAvailable) {
					t.Fatalf("unproven failure unblocked: %v", err)
				}
			}
			journal = openJournal(t, path, testLimits())
			if prior == messagejournal.InputFailed {
				for _, forbidden := range []messagejournal.InputPhase{messagejournal.InputCompleted, messagejournal.InputUnknown} {
					if _, err := journal.ResolveAcceptedInput(ctx, "s", "a", first.Sequence, forbidden); !errors.Is(err, messagejournal.ErrInvalidTransition) {
						t.Fatalf("legacy failed converted without exact failure proof: %v", err)
					}
				}
			}
			if _, err := journal.ResolveAcceptedInput(ctx, "s", "a", first.Sequence+1, "terminal_failed"); !errors.Is(err, messagejournal.ErrInvalidTransition) {
				t.Fatalf("stale terminal proof: %v", err)
			}
			if _, err := journal.ResolveAcceptedInput(ctx, "s", "b", first.Sequence+1, "terminal_failed"); !errors.Is(err, messagejournal.ErrInvalidTransition) {
				t.Fatalf("unaccepted terminal proof: %v", err)
			}
			if _, err := journal.ResolveAcceptedInput(ctx, "s", "a", first.Sequence, "terminal_failed"); err != nil {
				t.Fatalf("exact terminal failure rejected: %v", err)
			}
			var wg sync.WaitGroup
			for i := 0; i < 8; i++ {
				wg.Add(1)
				go func() {
					defer wg.Done()
					if _, err := journal.ResolveAcceptedInput(ctx, "s", "a", first.Sequence, "terminal_failed"); err != nil {
						t.Error(err)
					}
				}()
			}
			wg.Wait()
			journal = openJournal(t, path, testLimits())
			if _, err := journal.RetryInput(ctx, "s", "a"); !errors.Is(err, messagejournal.ErrInvalidTransition) {
				t.Fatalf("terminal failure replay permitted: %v", err)
			}
			for _, conflict := range []messagejournal.InputPhase{messagejournal.InputUnknown, messagejournal.InputFailed, messagejournal.InputCompleted} {
				if _, err := journal.ResolveAcceptedInput(ctx, "s", "a", first.Sequence, conflict); !errors.Is(err, messagejournal.ErrInvalidTransition) {
					t.Fatalf("terminal failure overwritten: %v", err)
				}
			}
			next, err := journal.LeaseNextInput(ctx, "s", "worker", time.Unix(100, 0), time.Minute)
			if err != nil || next.MessageID != "b" || next.Sequence != 2 {
				t.Fatalf("next input order: %#v %v", next, err)
			}
			if _, err := journal.LeaseNextInput(ctx, "s", "other", time.Unix(101, 0), time.Minute); !errors.Is(err, messagejournal.ErrNoAvailable) {
				t.Fatalf("duplicate lease: %v", err)
			}
			inputs, err := journal.Inputs(ctx, "s")
			if err != nil || inputs[0].Phase != "terminal_failed" || inputs[0].MessageID != "a" || inputs[0].Sequence != 1 {
				t.Fatalf("terminal identity: %#v %v", inputs, err)
			}
		})
	}
}
