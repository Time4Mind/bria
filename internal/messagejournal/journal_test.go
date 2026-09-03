package messagejournal_test

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strconv"
	"sync"
	"testing"
	"time"

	"bria/internal/messagejournal"
)

func TestInputJournalPersistsOrderLifecycleAndRetryAcrossReopen(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "messages.json")
	journal := openJournal(t, path, testLimits())

	first, inserted, err := journal.EnqueueInput(ctx, "session-a", "message-a", []byte("A"))
	if err != nil || !inserted || first.Sequence != 1 || first.Phase != messagejournal.InputPending {
		t.Fatalf("first EnqueueInput() = (%#v, %t, %v)", first, inserted, err)
	}
	second, inserted, err := journal.EnqueueInput(ctx, "session-a", "message-b", []byte("B"))
	if err != nil || !inserted || second.Sequence != 2 {
		t.Fatalf("second EnqueueInput() = (%#v, %t, %v)", second, inserted, err)
	}

	// Discarding the object without a close simulates a process disappearing
	// after EnqueueInput returned. Every successful mutation must already be on disk.
	journal = openJournal(t, path, testLimits())
	leased, err := journal.LeaseNextInput(ctx, "session-a", "executor-1", time.Unix(100, 0), time.Minute)
	if err != nil || leased.MessageID != "message-a" {
		t.Fatalf("first LeaseNextInput() = (%#v, %v)", leased, err)
	}
	if _, err := journal.LeaseNextInput(ctx, "session-a", "executor-2", time.Unix(110, 0), time.Minute); !errors.Is(err, messagejournal.ErrNoAvailable) {
		t.Fatalf("lease while head active error = %v, want ErrNoAvailable", err)
	}

	// An expired lease is recoverable after reopen and keeps the stable identity.
	journal = openJournal(t, path, testLimits())
	released, err := journal.LeaseNextInput(ctx, "session-a", "executor-2", time.Unix(161, 0), time.Minute)
	if err != nil || released.MessageID != leased.MessageID || released.Sequence != leased.Sequence {
		t.Fatalf("recovered lease = (%#v, %v), want stable first record", released, err)
	}
	accepted, err := journal.MarkInputAccepted(ctx, "session-a", "message-a", "executor-2")
	if err != nil || accepted.Phase != messagejournal.InputAccepted {
		t.Fatalf("MarkInputAccepted() = (%#v, %v)", accepted, err)
	}

	leased, err = journal.LeaseNextInput(ctx, "session-a", "executor-2", time.Unix(162, 0), time.Minute)
	if err != nil || leased.MessageID != "message-b" {
		t.Fatalf("second LeaseNextInput() = (%#v, %v)", leased, err)
	}
	if _, err := journal.MarkInputAccepted(ctx, "session-a", "message-b", "executor-2"); err != nil {
		t.Fatalf("accept second: %v", err)
	}
	if got, err := journal.CompleteInput(ctx, "session-a", "message-a"); err != nil || got.Phase != messagejournal.InputCompleted {
		t.Fatalf("CompleteInput() = (%#v, %v)", got, err)
	}
	if got, err := journal.FailInput(ctx, "session-a", "message-b"); err != nil || got.Phase != messagejournal.InputFailed {
		t.Fatalf("FailInput() = (%#v, %v)", got, err)
	}
	if got, err := journal.RetryInput(ctx, "session-a", "message-b"); err != nil || got.Phase != messagejournal.InputPending || got.Sequence != 2 {
		t.Fatalf("RetryInput() = (%#v, %v)", got, err)
	}

	reopened := openJournal(t, path, testLimits())
	inputs, err := reopened.Inputs(ctx, "session-a")
	if err != nil {
		t.Fatalf("Inputs() error = %v", err)
	}
	if len(inputs) != 2 || inputs[0].Phase != messagejournal.InputCompleted || inputs[1].Phase != messagejournal.InputPending {
		t.Fatalf("reopened inputs = %#v", inputs)
	}
	leased, err = reopened.LeaseNextInput(ctx, "session-a", "executor-3", time.Unix(300, 0), time.Minute)
	if err != nil || leased.MessageID != "message-b" || leased.Sequence != 2 {
		t.Fatalf("retried input lease after reopen = (%#v, %v)", leased, err)
	}
}

func TestInputEnqueueIsBoundedAtomicAndIdempotent(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "messages.json")
	limits := testLimits()
	limits.MaxPendingInputsPerSession = 2
	journal := openJournal(t, path, limits)

	first, _, err := journal.EnqueueInput(ctx, "session-a", "message-a", []byte("A"))
	if err != nil {
		t.Fatalf("enqueue first: %v", err)
	}
	if _, _, err := journal.EnqueueInput(ctx, "session-a", "message-b", []byte("B")); err != nil {
		t.Fatalf("enqueue second: %v", err)
	}
	replayed, inserted, err := journal.EnqueueInput(ctx, "session-a", "message-a", []byte("A"))
	if err != nil || inserted || replayed.Sequence != first.Sequence {
		t.Fatalf("idempotent enqueue = (%#v, %t, %v)", replayed, inserted, err)
	}
	if _, _, err := journal.EnqueueInput(ctx, "session-a", "message-a", []byte("changed")); !errors.Is(err, messagejournal.ErrConflict) {
		t.Fatalf("conflicting duplicate error = %v, want ErrConflict", err)
	}
	if _, _, err := journal.EnqueueInput(ctx, "session-a", "message-c", []byte("C")); !errors.Is(err, messagejournal.ErrQueueFull) {
		t.Fatalf("overflow error = %v, want ErrQueueFull", err)
	}

	reopened := openJournal(t, path, limits)
	inputs, err := reopened.Inputs(ctx, "session-a")
	if err != nil || len(inputs) != 2 || string(inputs[0].Payload) != "A" || string(inputs[1].Payload) != "B" {
		t.Fatalf("inputs after rejected writes = (%#v, %v)", inputs, err)
	}

	leased, err := reopened.LeaseNextInput(ctx, "session-a", "executor", time.Unix(1, 0), time.Minute)
	if err != nil {
		t.Fatalf("lease first: %v", err)
	}
	if _, err := reopened.MarkInputAccepted(ctx, "session-a", leased.MessageID, "executor"); err != nil {
		t.Fatalf("accept first: %v", err)
	}
	if _, inserted, err := reopened.EnqueueInput(ctx, "session-a", "message-c", []byte("C")); err != nil || !inserted {
		t.Fatalf("enqueue after provider acceptance = (inserted %t, %v)", inserted, err)
	}
	inputs, err = reopened.Inputs(ctx, "session-a")
	if err != nil || len(inputs) != 3 || inputs[0].Phase != messagejournal.InputAccepted {
		t.Fatalf("accepted record was removed or changed: (%#v, %v)", inputs, err)
	}
}

func TestFailedInputBlocksLaterDeliveryUntilExplicitRetry(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "messages.json")
	journal := openJournal(t, path, testLimits())
	for _, item := range []struct{ id, body string }{{"message-a", "A"}, {"message-b", "B"}} {
		if _, _, err := journal.EnqueueInput(ctx, "session-a", item.id, []byte(item.body)); err != nil {
			t.Fatalf("enqueue %s: %v", item.id, err)
		}
	}
	leased, err := journal.LeaseNextInput(ctx, "session-a", "executor", time.Unix(1, 0), time.Minute)
	if err != nil {
		t.Fatalf("lease first: %v", err)
	}
	if _, err := journal.MarkInputDeliveryFailed(ctx, "session-a", leased.MessageID, "executor"); err != nil {
		t.Fatalf("fail first delivery: %v", err)
	}

	reopened := openJournal(t, path, testLimits())
	if _, err := reopened.LeaseNextInput(ctx, "session-a", "executor", time.Unix(100, 0), time.Minute); !errors.Is(err, messagejournal.ErrNoAvailable) {
		t.Fatalf("lease after failed head error = %v, want ErrNoAvailable", err)
	}
	if _, err := reopened.RetryInput(ctx, "session-a", "message-a"); err != nil {
		t.Fatalf("retry first: %v", err)
	}
	leased, err = reopened.LeaseNextInput(ctx, "session-a", "executor", time.Unix(101, 0), time.Minute)
	if err != nil || leased.MessageID != "message-a" || leased.Sequence != 1 {
		t.Fatalf("lease after retry = (%#v, %v), want first record", leased, err)
	}
}

func TestUnknownInputInVersionOneJournalBlocksUntilExplicitRetry(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "messages.json")
	document := `{"version":1,"sessions":[{"session_id":"session-a","next_sequence":2,"inputs":[` +
		`{"message_id":"message-a","sequence":1,"payload":"QQ==","phase":"unknown","lease":{}},` +
		`{"message_id":"message-b","sequence":2,"payload":"Qg==","phase":"pending","lease":{}}],"outputs":[]}]}`
	if err := os.WriteFile(path, []byte(document), 0o600); err != nil {
		t.Fatalf("write unknown input fixture: %v", err)
	}
	journal := openJournal(t, path, testLimits())
	inputs, err := journal.Inputs(ctx, "session-a")
	if err != nil || len(inputs) != 2 || inputs[0].Phase != messagejournal.InputPhase("unknown") {
		t.Fatalf("reopened unknown inputs = (%#v, %v)", inputs, err)
	}
	if _, err := journal.LeaseNextInput(ctx, "session-a", "executor", time.Unix(100, 0), time.Minute); !errors.Is(err, messagejournal.ErrNoAvailable) {
		t.Fatalf("lease after unknown head = %v, want ErrNoAvailable", err)
	}
	retried, err := journal.RetryInput(ctx, "session-a", "message-a")
	if err != nil || retried.Phase != messagejournal.InputPending || retried.Sequence != 1 {
		t.Fatalf("RetryInput(unknown) = (%#v, %v)", retried, err)
	}
	leased, err := journal.LeaseNextInput(ctx, "session-a", "executor", time.Unix(101, 0), time.Minute)
	if err != nil || leased.MessageID != "message-a" || leased.Sequence != 1 {
		t.Fatalf("lease explicitly retried unknown = (%#v, %v)", leased, err)
	}
}

func TestPendingInputLeaseCanBeReleasedWithoutChangingOrder(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "messages.json")
	journal := openJournal(t, path, testLimits())
	if _, _, err := journal.EnqueueInput(ctx, "session-a", "message-a", []byte("A")); err != nil {
		t.Fatalf("enqueue: %v", err)
	}
	if _, err := journal.LeaseNextInput(ctx, "session-a", "executor-1", time.Unix(1, 0), time.Minute); err != nil {
		t.Fatalf("lease: %v", err)
	}
	if got, err := journal.ReleaseInputLease(ctx, "session-a", "message-a", "executor-1"); err != nil || got.Phase != messagejournal.InputPending || got.Lease.Owner != "" {
		t.Fatalf("ReleaseInputLease() = (%#v, %v)", got, err)
	}
	reopened := openJournal(t, path, testLimits())
	leased, err := reopened.LeaseNextInput(ctx, "session-a", "executor-2", time.Unix(2, 0), time.Minute)
	if err != nil || leased.MessageID != "message-a" || leased.Sequence != 1 {
		t.Fatalf("lease after release and reopen = (%#v, %v)", leased, err)
	}
}

func TestStructuredAttachmentRefsPersistAcrossReopenWithoutPaths(t *testing.T) {
	path := filepath.Join(t.TempDir(), "journal.json")
	journal := openJournal(t, path, messagejournal.DefaultLimits())
	attachments := []messagejournal.AttachmentRef{{
		Reference: "photo-custody-42", Size: 1234,
		SHA256: "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef",
	}}
	input, inserted, err := journal.EnqueueInputWithAttachments(context.Background(), "session-a", "message-a", []byte("inspect photo"), attachments)
	if err != nil || !inserted || !reflect.DeepEqual(input.Attachments, attachments) {
		t.Fatalf("EnqueueInputWithAttachments() = (%#v, %t, %v)", input, inserted, err)
	}
	reopened := openJournal(t, path, messagejournal.DefaultLimits())
	inputs, err := reopened.Inputs(context.Background(), "session-a")
	if err != nil || len(inputs) != 1 || !reflect.DeepEqual(inputs[0].Attachments, attachments) {
		t.Fatalf("reopened inputs = (%#v, %v)", inputs, err)
	}
	changed := append([]messagejournal.AttachmentRef(nil), attachments...)
	changed[0].Size++
	if _, _, err := reopened.EnqueueInputWithAttachments(context.Background(), "session-a", "message-a", []byte("inspect photo"), changed); !errors.Is(err, messagejournal.ErrConflict) {
		t.Fatalf("conflicting attachment replay error = %v", err)
	}
	invalid := attachments
	invalid[0].Reference = filepath.Join(t.TempDir(), "photo.jpg")
	if _, _, err := reopened.EnqueueInputWithAttachments(context.Background(), "session-a", "message-b", nil, invalid); err == nil {
		t.Fatal("journal accepted absolute attachment path")
	}
}

func TestOutputJournalBlocksUnknownUntilExplicitRetryAndPersistsReceipts(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "messages.json")
	journal := openJournal(t, path, testLimits())
	first, inserted, err := journal.EnqueueOutput(ctx, "session-a", "operation-a", "final", []byte("first"))
	if err != nil || !inserted || first.Sequence != 1 || first.Phase != messagejournal.OutputPending {
		t.Fatalf("first EnqueueOutput() = (%#v, %t, %v)", first, inserted, err)
	}
	second, inserted, err := journal.EnqueueOutput(ctx, "session-a", "operation-b", "notification", []byte("second"))
	if err != nil || !inserted || second.Sequence != 2 {
		t.Fatalf("second EnqueueOutput() = (%#v, %t, %v)", second, inserted, err)
	}
	replayed, inserted, err := journal.EnqueueOutput(ctx, "session-a", "operation-a", "final", []byte("first"))
	if err != nil || inserted || replayed.Sequence != first.Sequence {
		t.Fatalf("idempotent output enqueue = (%#v, %t, %v)", replayed, inserted, err)
	}
	if _, _, err := journal.EnqueueOutput(ctx, "session-a", "operation-a", "final", []byte("changed")); !errors.Is(err, messagejournal.ErrConflict) {
		t.Fatalf("conflicting output duplicate error = %v, want ErrConflict", err)
	}

	leased, err := journal.LeaseNextOutput(ctx, "session-a", "telegram", time.Unix(10, 0), time.Minute)
	if err != nil || leased.OperationID != "operation-a" {
		t.Fatalf("first LeaseNextOutput() = (%#v, %v)", leased, err)
	}
	if got, err := journal.MarkOutputUnknown(ctx, "session-a", leased.OperationID, "telegram"); err != nil || got.Phase != messagejournal.OutputUnknown {
		t.Fatalf("MarkOutputUnknown() = (%#v, %v)", got, err)
	}

	journal = openJournal(t, path, testLimits())
	if _, err := journal.LeaseNextOutput(ctx, "session-a", "telegram", time.Unix(1000, 0), time.Minute); !errors.Is(err, messagejournal.ErrNoAvailable) {
		t.Fatalf("automatic lease after unknown error = %v, want ErrNoAvailable", err)
	}
	if got, err := journal.RetryOutput(ctx, "session-a", "operation-a"); err != nil || got.Phase != messagejournal.OutputPending {
		t.Fatalf("explicit RetryOutput() = (%#v, %v)", got, err)
	}
	leased, err = journal.LeaseNextOutput(ctx, "session-a", "telegram", time.Unix(1001, 0), time.Minute)
	if err != nil || leased.OperationID != "operation-a" {
		t.Fatalf("lease after explicit retry = (%#v, %v)", leased, err)
	}
	if got, err := journal.ConfirmOutput(ctx, "session-a", leased.OperationID, "telegram", "telegram-message-42"); err != nil || got.Phase != messagejournal.OutputConfirmed || got.Receipt != "telegram-message-42" {
		t.Fatalf("ConfirmOutput() = (%#v, %v)", got, err)
	}
	leased, err = journal.LeaseNextOutput(ctx, "session-a", "telegram", time.Unix(1002, 0), time.Minute)
	if err != nil || leased.OperationID != "operation-b" {
		t.Fatalf("ordered second lease = (%#v, %v)", leased, err)
	}
	if got, err := journal.MarkOutputFailed(ctx, "session-a", leased.OperationID, "telegram"); err != nil || got.Phase != messagejournal.OutputFailed {
		t.Fatalf("MarkOutputFailed() = (%#v, %v)", got, err)
	}

	reopened := openJournal(t, path, testLimits())
	outputs, err := reopened.Outputs(ctx, "session-a")
	if err != nil || len(outputs) != 2 || outputs[0].Receipt != "telegram-message-42" || outputs[1].Phase != messagejournal.OutputFailed {
		t.Fatalf("reopened outputs = (%#v, %v)", outputs, err)
	}
	if got, err := reopened.RetryOutput(ctx, "session-a", "operation-b"); err != nil || got.Phase != messagejournal.OutputPending {
		t.Fatalf("retry failed output = (%#v, %v)", got, err)
	}
	leased, err = reopened.LeaseNextOutput(ctx, "session-a", "telegram-2", time.Unix(2000, 0), time.Minute)
	if err != nil || leased.OperationID != "operation-b" || leased.Sequence != 2 {
		t.Fatalf("retried failed output lease = (%#v, %v)", leased, err)
	}
}

func TestConcurrentStoresAssignUniqueMonotonicSequences(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "messages.json")
	limits := testLimits()
	const writers = 48
	stores := make([]*messagejournal.Journal, writers)
	for index := range stores {
		stores[index] = openJournal(t, path, limits)
	}

	start := make(chan struct{})
	errorsSeen := make(chan error, writers)
	var group sync.WaitGroup
	for index := range stores {
		index := index
		group.Add(1)
		go func() {
			defer group.Done()
			<-start
			_, _, err := stores[index].EnqueueInput(ctx, "session-a", fmt.Sprintf("message-%02d", index), []byte{byte(index)})
			errorsSeen <- err
		}()
	}
	close(start)
	group.Wait()
	close(errorsSeen)
	for err := range errorsSeen {
		if err != nil {
			t.Fatalf("concurrent EnqueueInput() error = %v", err)
		}
	}

	reopened := openJournal(t, path, limits)
	inputs, err := reopened.Inputs(ctx, "session-a")
	if err != nil || len(inputs) != writers {
		t.Fatalf("Inputs() = %d records, %v", len(inputs), err)
	}
	for index, input := range inputs {
		if input.Sequence != uint64(index+1) {
			t.Fatalf("input %d sequence = %d, want %d", index, input.Sequence, index+1)
		}
	}
}

func TestConcurrentProcessesDoNotLoseCommittedEnqueues(t *testing.T) {
	if os.Getenv("BRIA_MESSAGEJOURNAL_CHILD") == "1" {
		runProcessWriter(t)
		return
	}
	t.Parallel()
	directory := t.TempDir()
	path := filepath.Join(directory, "messages.json")
	startPath := filepath.Join(directory, "start")
	const writers = 8
	const recordsPerWriter = 12
	type child struct {
		command *exec.Cmd
		output  bytes.Buffer
	}
	children := make([]child, writers)
	for index := range children {
		command := exec.Command(os.Args[0], "-test.run=^TestConcurrentProcessesDoNotLoseCommittedEnqueues$")
		command.Env = append(os.Environ(),
			"BRIA_MESSAGEJOURNAL_CHILD=1",
			"BRIA_MESSAGEJOURNAL_PATH="+path,
			"BRIA_MESSAGEJOURNAL_START="+startPath,
			"BRIA_MESSAGEJOURNAL_WRITER="+strconv.Itoa(index),
		)
		command.Stdout = &children[index].output
		command.Stderr = &children[index].output
		children[index].command = command
		if err := command.Start(); err != nil {
			t.Fatalf("start writer %d: %v", index, err)
		}
	}
	if err := os.WriteFile(startPath, []byte("start"), 0o600); err != nil {
		t.Fatalf("release child writers: %v", err)
	}
	for index := range children {
		if err := children[index].command.Wait(); err != nil {
			t.Fatalf("writer %d: %v\n%s", index, err, children[index].output.String())
		}
	}

	journal := openJournal(t, path, processTestLimits())
	inputs, err := journal.Inputs(context.Background(), "session-a")
	if err != nil || len(inputs) != writers*recordsPerWriter {
		t.Fatalf("reopened process-shared journal = %d records, %v; want %d", len(inputs), err, writers*recordsPerWriter)
	}
	for index, input := range inputs {
		if input.Sequence != uint64(index+1) {
			t.Fatalf("process-shared input %d sequence = %d", index, input.Sequence)
		}
	}
}

func runProcessWriter(t *testing.T) {
	t.Helper()
	path := os.Getenv("BRIA_MESSAGEJOURNAL_PATH")
	startPath := os.Getenv("BRIA_MESSAGEJOURNAL_START")
	writer, err := strconv.Atoi(os.Getenv("BRIA_MESSAGEJOURNAL_WRITER"))
	if err != nil {
		t.Fatalf("parse writer index: %v", err)
	}
	deadline := time.Now().Add(10 * time.Second)
	for {
		if _, err := os.Stat(startPath); err == nil {
			break
		} else if !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("stat writer barrier: %v", err)
		}
		if time.Now().After(deadline) {
			t.Fatal("writer barrier timeout")
		}
		time.Sleep(time.Millisecond)
	}
	journal := openJournal(t, path, processTestLimits())
	for index := 0; index < 12; index++ {
		id := fmt.Sprintf("writer-%02d-message-%02d", writer, index)
		if _, _, err := journal.EnqueueInput(context.Background(), "session-a", id, []byte(id)); err != nil {
			t.Fatalf("enqueue %s: %v", id, err)
		}
	}
}

func TestOpenRejectsOversizedOrNonCanonicalDocument(t *testing.T) {
	t.Parallel()
	limits := testLimits()
	directory := t.TempDir()
	oversized := filepath.Join(directory, "oversized.json")
	if err := os.WriteFile(oversized, bytes.Repeat([]byte("x"), int(limits.MaxFileBytes)+1), 0o600); err != nil {
		t.Fatalf("write oversized fixture: %v", err)
	}
	if _, err := messagejournal.Open(oversized, limits); !errors.Is(err, messagejournal.ErrInvalidFormat) {
		t.Fatalf("Open(oversized) error = %v, want ErrInvalidFormat", err)
	}

	unknown := filepath.Join(directory, "unknown.json")
	if err := os.WriteFile(unknown, []byte(`{"version":1,"sessions":[],"extra":true}`), 0o600); err != nil {
		t.Fatalf("write unknown-field fixture: %v", err)
	}
	if _, err := messagejournal.Open(unknown, limits); !errors.Is(err, messagejournal.ErrInvalidFormat) {
		t.Fatalf("Open(unknown field) error = %v, want ErrInvalidFormat", err)
	}

	reordered := filepath.Join(directory, "reordered.json")
	reorderedDocument := `{"version":1,"sessions":[{"session_id":"session-a","next_sequence":2,"inputs":[` +
		`{"message_id":"message-b","sequence":2,"payload":"Qg==","phase":"pending","lease":{}},` +
		`{"message_id":"message-a","sequence":1,"payload":"QQ==","phase":"pending","lease":{}}],"outputs":[]}]}`
	if err := os.WriteFile(reordered, []byte(reorderedDocument), 0o600); err != nil {
		t.Fatalf("write reordered fixture: %v", err)
	}
	if _, err := messagejournal.Open(reordered, limits); !errors.Is(err, messagejournal.ErrInvalidFormat) {
		t.Fatalf("Open(reordered records) error = %v, want ErrInvalidFormat", err)
	}
}

func TestFailedOversizedEnqueueDoesNotReplaceLastCommittedJournal(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "messages.json")
	limits := testLimits()
	limits.MaxFileBytes = 1024
	journal := openJournal(t, path, limits)
	if _, _, err := journal.EnqueueInput(ctx, "session-a", "message-a", []byte("kept")); err != nil {
		t.Fatalf("enqueue baseline: %v", err)
	}
	info, err := os.Lstat(path)
	if err != nil {
		t.Fatalf("lstat committed journal: %v", err)
	}
	if !info.Mode().IsRegular() || info.Mode().Perm() != 0o600 {
		t.Fatalf("journal mode = %v, want regular 0600", info.Mode())
	}
	if _, _, err := journal.EnqueueInput(ctx, "session-a", "message-b", bytes.Repeat([]byte("x"), 900)); !errors.Is(err, messagejournal.ErrJournalFull) {
		t.Fatalf("oversized enqueue error = %v, want ErrJournalFull", err)
	}

	reopened := openJournal(t, path, limits)
	inputs, err := reopened.Inputs(ctx, "session-a")
	if err != nil || len(inputs) != 1 || inputs[0].MessageID != "message-a" || string(inputs[0].Payload) != "kept" {
		t.Fatalf("journal after failed oversized enqueue = (%#v, %v)", inputs, err)
	}
	matches, err := filepath.Glob(filepath.Join(filepath.Dir(path), ".messages.json.tmp-*"))
	if err != nil || len(matches) != 0 {
		t.Fatalf("temporary files after commit = (%v, %v)", matches, err)
	}
}

func openJournal(t *testing.T, path string, limits messagejournal.Limits) *messagejournal.Journal {
	t.Helper()
	journal, err := messagejournal.Open(path, limits)
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	return journal
}

func testLimits() messagejournal.Limits {
	limits := messagejournal.DefaultLimits()
	limits.MaxPendingInputsPerSession = 128
	limits.MaxInputsPerSession = 256
	limits.MaxOutputsPerSession = 256
	limits.MaxFileBytes = 1 << 20
	return limits
}

func processTestLimits() messagejournal.Limits {
	limits := testLimits()
	limits.MaxPendingInputsPerSession = 128
	return limits
}
