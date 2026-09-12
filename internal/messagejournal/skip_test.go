package messagejournal_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"bria/internal/messagejournal"
)

type inputRecoveryJournal interface {
	BeginInputRecovery(context.Context, string, string, bool) (bool, error)
	CommitInputRecoverySkip(context.Context, string, string) error
	CommitInputRecoveryAttach(context.Context, string, string) error
}

func recoveryJournal(t *testing.T, journal *messagejournal.Journal) inputRecoveryJournal {
	t.Helper()
	recovery, ok := any(journal).(inputRecoveryJournal)
	if !ok {
		t.Fatal("message journal does not expose the durable input recovery boundary")
	}
	return recovery
}

func TestInputRecoverySkipsOnlyCapturedUnresolvedPrefix(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "journal.json")
	document := `{"version":2,"sessions":[{"session_id":"s","next_sequence":6,"inputs":[` +
		`{"message_id":"completed","sequence":1,"payload":"YQ==","phase":"completed","lease":{}},` +
		`{"message_id":"terminal","sequence":2,"payload":"Yg==","phase":"terminal_failed","lease":{}},` +
		`{"message_id":"accepted","sequence":3,"payload":"Yw==","phase":"accepted","lease":{}},` +
		`{"message_id":"failed","sequence":4,"payload":"ZA==","phase":"failed","lease":{}},` +
		`{"message_id":"unknown","sequence":5,"payload":"ZQ==","phase":"unknown","lease":{}},` +
		`{"message_id":"pending-before","sequence":6,"payload":"Zg==","phase":"pending","lease":{"owner":"crashed","until_unix_nano":999999999999}}],"outputs":[]}]}`
	if err := os.WriteFile(path, []byte(document), 0o600); err != nil {
		t.Fatal(err)
	}
	journal := openJournal(t, path, testLimits())
	recovery := recoveryJournal(t, journal)
	active, err := recovery.BeginInputRecovery(ctx, "s", "recovery-a", false)
	if err != nil || !active {
		t.Fatalf("BeginInputRecovery() = %t, %v", active, err)
	}
	if _, _, err := journal.EnqueueInput(ctx, "s", "pending-after", []byte("new")); err != nil {
		t.Fatal(err)
	}
	if _, err := journal.LeaseNextInput(ctx, "s", "other", time.Unix(100, 0), time.Minute); !errors.Is(err, messagejournal.ErrNoAvailable) {
		t.Fatalf("open recovery boundary allowed lease: %v", err)
	}
	if err := recovery.CommitInputRecoverySkip(ctx, "s", "recovery-a"); err != nil {
		t.Fatal(err)
	}

	reopened := openJournal(t, path, testLimits())
	inputs, err := reopened.Inputs(ctx, "s")
	if err != nil || len(inputs) != 7 {
		t.Fatalf("Inputs() = %+v, %v", inputs, err)
	}
	want := []messagejournal.InputPhase{
		messagejournal.InputCompleted,
		messagejournal.InputTerminalFailed,
		messagejournal.InputSkipped,
		messagejournal.InputSkipped,
		messagejournal.InputSkipped,
		messagejournal.InputSkipped,
		messagejournal.InputPending,
	}
	for index := range want {
		if inputs[index].Phase != want[index] || inputs[index].Lease != (messagejournal.Lease{}) {
			t.Fatalf("input[%d] = %+v, want phase %q without lease", index, inputs[index], want[index])
		}
	}
	next, err := reopened.LeaseNextInput(ctx, "s", "recovered-worker", time.Unix(101, 0), time.Minute)
	if err != nil || next.MessageID != "pending-after" || next.Sequence != 7 {
		t.Fatalf("post-recovery lease = %+v, %v", next, err)
	}
}

func TestAttachedInputRecoveryPreservesAcceptedAndDefinitelyUnsentPending(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "journal.json")
	document := `{"version":2,"sessions":[{"session_id":"s","next_sequence":3,"inputs":[` +
		`{"message_id":"accepted","sequence":1,"payload":"YQ==","phase":"accepted","lease":{}},` +
		`{"message_id":"unknown","sequence":2,"payload":"Yg==","phase":"unknown","lease":{}},` +
		`{"message_id":"queued","sequence":3,"payload":"Yw==","phase":"pending","lease":{}}],"outputs":[]}]}`
	if err := os.WriteFile(path, []byte(document), 0o600); err != nil {
		t.Fatal(err)
	}
	recovery := recoveryJournal(t, openJournal(t, path, testLimits()))
	if active, err := recovery.BeginInputRecovery(ctx, "s", "exact-attach", false); err != nil || !active {
		t.Fatalf("begin = %t, %v", active, err)
	}
	if err := recovery.CommitInputRecoveryAttach(ctx, "s", "exact-attach"); err != nil {
		t.Fatal(err)
	}
	reopened := openJournal(t, path, testLimits())
	if err := recoveryJournal(t, reopened).CommitInputRecoveryAttach(ctx, "s", "exact-attach"); err != nil {
		t.Fatalf("replayed attach commit = %v", err)
	}
	inputs, err := reopened.Inputs(ctx, "s")
	if err != nil || len(inputs) != 3 || inputs[0].Phase != messagejournal.InputAccepted || inputs[1].Phase != messagejournal.InputSkipped || inputs[2].Phase != messagejournal.InputPending {
		t.Fatalf("attached phases = %+v, %v", inputs, err)
	}
	next, err := reopened.LeaseNextInput(ctx, "s", "worker", time.Unix(100, 0), time.Minute)
	if err != nil || next.MessageID != "queued" {
		t.Fatalf("queued successor = %+v, %v", next, err)
	}
}

func TestInputRecoveryIsIdempotentAcrossReopenAndRejectsWrongToken(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "journal.json")
	journal := openJournal(t, path, testLimits())
	if _, _, err := journal.EnqueueInput(ctx, "s", "old", []byte("private")); err != nil {
		t.Fatal(err)
	}
	if active, err := recoveryJournal(t, journal).BeginInputRecovery(ctx, "s", "stable-token", false); err != nil || !active {
		t.Fatalf("begin = %t, %v", active, err)
	}

	reopened := openJournal(t, path, testLimits())
	if active, err := recoveryJournal(t, reopened).BeginInputRecovery(ctx, "s", "stable-token", false); err != nil || !active {
		t.Fatalf("replayed begin = %t, %v", active, err)
	}
	if err := recoveryJournal(t, reopened).CommitInputRecoverySkip(ctx, "s", "wrong-token"); !errors.Is(err, messagejournal.ErrConflict) {
		t.Fatalf("wrong token commit = %v, want ErrConflict", err)
	}
	if err := recoveryJournal(t, reopened).CommitInputRecoverySkip(ctx, "s", "stable-token"); err != nil {
		t.Fatal(err)
	}
	if err := recoveryJournal(t, openJournal(t, path, testLimits())).CommitInputRecoverySkip(ctx, "s", "stable-token"); err != nil {
		t.Fatalf("replayed commit = %v", err)
	}
	if _, err := reopened.RetryInput(ctx, "s", "old"); !errors.Is(err, messagejournal.ErrInvalidTransition) {
		t.Fatalf("RetryInput(skipped) = %v, want ErrInvalidTransition", err)
	}
	late, err := reopened.ResolveAcceptedInput(ctx, "s", "old", 1, messagejournal.InputCompleted)
	if !errors.Is(err, messagejournal.ErrInputSkipped) || late.Phase != messagejournal.InputSkipped {
		t.Fatalf("late ResolveAcceptedInput(skipped) = %+v, %v", late, err)
	}
}

func TestOpenRecoveryBoundaryTransfersOwnershipWithoutWidening(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "journal.json")
	journal := openJournal(t, path, testLimits())
	if _, _, err := journal.EnqueueInput(ctx, "s", "old", []byte("private")); err != nil {
		t.Fatal(err)
	}
	if active, err := recoveryJournal(t, journal).BeginInputRecovery(ctx, "s", "first-owner", false); err != nil || !active {
		t.Fatalf("first begin = %t, %v", active, err)
	}
	if _, _, err := journal.EnqueueInput(ctx, "s", "after-cutoff", []byte("new")); err != nil {
		t.Fatal(err)
	}
	reopened := openJournal(t, path, testLimits())
	if active, err := recoveryJournal(t, reopened).BeginInputRecovery(ctx, "s", "replacement-owner", false); err != nil || !active {
		t.Fatalf("replacement begin = %t, %v", active, err)
	}
	if err := recoveryJournal(t, reopened).CommitInputRecoverySkip(ctx, "s", "first-owner"); !errors.Is(err, messagejournal.ErrConflict) {
		t.Fatalf("stale owner commit = %v, want ErrConflict", err)
	}
	if err := recoveryJournal(t, reopened).CommitInputRecoverySkip(ctx, "s", "replacement-owner"); err != nil {
		t.Fatal(err)
	}
	inputs, err := reopened.Inputs(ctx, "s")
	if err != nil || len(inputs) != 2 || inputs[0].Phase != messagejournal.InputSkipped || inputs[1].Phase != messagejournal.InputPending {
		t.Fatalf("replacement commit widened cutoff: %+v, %v", inputs, err)
	}
}

func TestRecoveryBoundaryRejectsLateExactAcceptance(t *testing.T) {
	ctx := context.Background()
	journal := openJournal(t, filepath.Join(t.TempDir(), "journal.json"), testLimits())
	if _, _, err := journal.EnqueueInput(ctx, "s", "old", []byte("private")); err != nil {
		t.Fatal(err)
	}
	leased, err := journal.LeaseNextInput(ctx, "s", "old-worker", time.Unix(1, 0), time.Minute)
	if err != nil || leased.Sequence != 1 {
		t.Fatalf("lease = %+v, %v", leased, err)
	}
	if active, err := recoveryJournal(t, journal).BeginInputRecovery(ctx, "s", "recovery", false); err != nil || !active {
		t.Fatalf("begin = %t, %v", active, err)
	}
	if got, err := journal.MarkInputAccepted(ctx, "s", "old", "old-worker"); !errors.Is(err, messagejournal.ErrInputSkipped) || got.Phase != messagejournal.InputPending {
		t.Fatalf("late acceptance = %+v, %v", got, err)
	}
	if err := recoveryJournal(t, journal).CommitInputRecoverySkip(ctx, "s", "recovery"); err != nil {
		t.Fatal(err)
	}
	if got, err := journal.MarkInputAccepted(ctx, "s", "old", "old-worker"); !errors.Is(err, messagejournal.ErrInputSkipped) || got.Phase != messagejournal.InputSkipped {
		t.Fatalf("post-commit acceptance = %+v, %v", got, err)
	}
}

func TestLegacyRecoveryRequiresBlockedHeadAndNewerPending(t *testing.T) {
	ctx := context.Background()
	journal := openJournal(t, filepath.Join(t.TempDir(), "journal.json"), testLimits())
	if _, _, err := journal.EnqueueInput(ctx, "s", "pending-only", nil); err != nil {
		t.Fatal(err)
	}
	if active, err := recoveryJournal(t, journal).BeginInputRecovery(ctx, "s", "startup-clean", true); err != nil || active {
		t.Fatalf("clean startup boundary = %t, %v", active, err)
	}
	if lease, err := journal.LeaseNextInput(ctx, "s", "worker", time.Unix(1, 0), time.Minute); err != nil || lease.MessageID != "pending-only" {
		t.Fatalf("clean pending input was blocked: %+v, %v", lease, err)
	}
	if _, err := journal.MarkInputDeliveryUnknown(ctx, "s", "pending-only", "worker"); err != nil {
		t.Fatal(err)
	}
	if active, err := recoveryJournal(t, journal).BeginInputRecovery(ctx, "s", "startup-unknown-only", true); err != nil || active {
		t.Fatalf("unknown-only startup boundary = %t, %v", active, err)
	}
	if _, _, err := journal.EnqueueInput(ctx, "s", "continued", nil); err != nil {
		t.Fatal(err)
	}
	if active, err := recoveryJournal(t, journal).BeginInputRecovery(ctx, "s", "startup-continued", true); err != nil || !active {
		t.Fatalf("continued startup boundary = %t, %v", active, err)
	}
}

func TestBackupRecordsFailsClosedAcrossRecoveryBoundary(t *testing.T) {
	ctx := context.Background()
	journal := openJournal(t, filepath.Join(t.TempDir(), "journal.json"), testLimits())
	if _, _, err := journal.EnqueueInput(ctx, "s", "old", []byte("private")); err != nil {
		t.Fatal(err)
	}
	if active, err := recoveryJournal(t, journal).BeginInputRecovery(ctx, "s", "recovery", false); err != nil || !active {
		t.Fatalf("begin = %t, %v", active, err)
	}
	if _, _, err := journal.BackupRecords(ctx, "s"); !errors.Is(err, messagejournal.ErrRecoveryInProgress) {
		t.Fatalf("open recovery backup = %v, want ErrRecoveryInProgress", err)
	}
	if err := recoveryJournal(t, journal).CommitInputRecoverySkip(ctx, "s", "recovery"); err != nil {
		t.Fatal(err)
	}
	inputs, outputs, err := journal.BackupRecords(ctx, "s")
	if err != nil || len(inputs) != 1 || inputs[0].Phase != messagejournal.InputSkipped || len(outputs) != 0 {
		t.Fatalf("committed recovery backup = %+v %+v, %v", inputs, outputs, err)
	}
}

func TestJournalRejectsVersionSpoofAndIncompleteCommittedRecovery(t *testing.T) {
	for _, fixture := range []struct {
		name string
		doc  string
	}{
		{name: "v2-with-v3-fields", doc: `{"version":2,"sessions":[{"session_id":"s","next_sequence":1,"inputs":[{"message_id":"old","sequence":1,"payload":"cHJpdmF0ZQ==","phase":"skipped","lease":{}}],"outputs":[],"input_recovery":{"token":"recovery","through_sequence":1,"phase":"committed"}}]}`},
		{name: "committed-with-pending", doc: `{"version":3,"sessions":[{"session_id":"s","next_sequence":1,"inputs":[{"message_id":"old","sequence":1,"payload":"cHJpdmF0ZQ==","phase":"pending","lease":{}}],"outputs":[],"input_recovery":{"token":"recovery","through_sequence":1,"phase":"committed"}}]}`},
	} {
		t.Run(fixture.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "journal.json")
			if err := os.WriteFile(path, []byte(fixture.doc), 0o600); err != nil {
				t.Fatal(err)
			}
			if _, err := messagejournal.Open(path, testLimits()); !errors.Is(err, messagejournal.ErrInvalidFormat) {
				t.Fatalf("Open() = %v, want ErrInvalidFormat", err)
			}
		})
	}
}
