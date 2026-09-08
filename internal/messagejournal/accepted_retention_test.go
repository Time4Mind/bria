package messagejournal_test

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"bria/internal/messagejournal"
)

func TestAcceptedUnknownOutcomeRetainsAcceptanceWithoutWrite(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "journal.json")
	j := openJournal(t, path, testLimits())
	in, _, err := j.EnqueueInput(ctx, "s", "m", []byte("synthetic"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := j.LeaseNextInput(ctx, "s", "worker", time.Unix(10, 0), time.Minute); err != nil {
		t.Fatal(err)
	}
	if _, err := j.MarkInputAccepted(ctx, "s", "m", "worker"); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := j.ResolveAcceptedInput(ctx, "s", "m", in.Sequence+1, messagejournal.InputUnknown); !errors.Is(err, messagejournal.ErrInvalidTransition) {
		t.Fatal("stale sequence accepted")
	}
	for _, exact := range []bool{false, true} {
		var got messagejournal.Input
		if exact {
			got, err = j.ResolveAcceptedInput(ctx, "s", "m", in.Sequence, messagejournal.InputUnknown)
		} else {
			got, err = j.MarkInputUnknown(ctx, "s", "m")
		}
		if err != nil || got.Phase != messagejournal.InputAccepted {
			t.Fatalf("acceptance lost: phase=%s err=%v", got.Phase, err)
		}
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	afterInfo, err := os.Stat(path)
	if err != nil || !bytes.Equal(before, after) || !os.SameFile(info, afterInfo) || !info.ModTime().Equal(afterInfo.ModTime()) {
		t.Fatal("no-op rewrote journal")
	}
	j = openJournal(t, path, testLimits())
	if _, err := j.RetryInput(ctx, "s", "m"); !errors.Is(err, messagejournal.ErrInvalidTransition) {
		t.Fatal("accepted request replayable")
	}
	for range 2 {
		got, err := j.ResolveAcceptedInput(ctx, "s", "m", in.Sequence, messagejournal.InputCompleted)
		if err != nil || got.Phase != messagejournal.InputCompleted {
			t.Fatalf("late terminal: %#v %v", got, err)
		}
	}
	if _, err := j.ResolveAcceptedInput(ctx, "s", "m", in.Sequence, messagejournal.InputUnknown); !errors.Is(err, messagejournal.ErrInvalidTransition) {
		t.Fatal("completed downgraded")
	}
}
