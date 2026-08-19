package runtimehost

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	bolt "go.etcd.io/bbolt"
)

func TestBoltOperationStorePreservesPendingAcrossReopen(t *testing.T) {
	path := filepath.Join(t.TempDir(), "runtime-operations.db")
	store, err := OpenBoltOperationStore(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, created, err := store.CreatePending(
		"op-1", "fingerprint", ActionSendInput,
	); err != nil || !created {
		t.Fatalf("create pending: created=%v err=%v", created, err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	store, err = OpenBoltOperationStore(path)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	record, created, err := store.CreatePending("op-1", "fingerprint", ActionSendInput)
	if err != nil || created || record.State != OperationPending {
		t.Fatalf("reopened pending: record=%+v created=%v err=%v", record, created, err)
	}
	if _, _, err := store.CreatePending(
		"op-1", "different", ActionSendInput,
	); !errors.Is(err, ErrOperationIDConflict) {
		t.Fatalf("fingerprint conflict = %v", err)
	}
}

func TestBoltOperationStoreBoundsCapturePayloadWithoutReplayingMutations(t *testing.T) {
	path := filepath.Join(t.TempDir(), "runtime-operations.db")
	now := time.Date(2026, time.August, 19, 12, 0, 0, 0, time.UTC)
	clock := func() time.Time { return now }
	store, err := openBoltOperationStore(path, clock)
	if err != nil {
		t.Fatal(err)
	}
	pane := bytes.Repeat([]byte("terminal snapshot "), 4096)
	for index := 0; index < 100; index++ {
		operationID := fmt.Sprintf("pane-%d", index)
		fingerprint := fmt.Sprintf("capture-%d", index)
		if _, _, err := store.CreatePending(operationID, fingerprint, ActionCapture); err != nil {
			t.Fatal(err)
		}
		if err := store.Complete(operationID, fingerprint, Result{Pane: pane}, nil); err != nil {
			t.Fatal(err)
		}
	}
	if _, _, err := store.CreatePending("interactive-1", "mutating", ActionSendKey); err != nil {
		t.Fatal(err)
	}
	if err := store.Complete("interactive-1", "mutating", Result{Pane: pane}, nil); err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.CreatePending("pending-input", "pending", ActionSendInput); err != nil {
		t.Fatal(err)
	}
	if err := store.db.Update(func(tx *bolt.Tx) error {
		return putOperationRecord(tx.Bucket(operationBucket), "pane-legacy", OperationRecord{
			Fingerprint: "legacy", State: OperationCompleted, Result: Result{Pane: pane},
		})
	}); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	before, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}

	now = now.Add(operationPayloadRetention + time.Second)
	store, err = openBoltOperationStore(path, clock)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	after, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if after.Size() >= before.Size()/2 {
		t.Fatalf("compaction did not reclaim capture payload: before=%d after=%d",
			before.Size(), after.Size())
	}
	if _, found, err := store.Lookup("pane-99"); err != nil || found {
		t.Fatalf("expired capture found=%v err=%v", found, err)
	}
	if _, found, err := store.Lookup("pane-legacy"); err != nil || found {
		t.Fatalf("legacy capture found=%v err=%v", found, err)
	}
	mutation, found, err := store.Lookup("interactive-1")
	if err != nil || !found || mutation.State != OperationCompleted || len(mutation.Result.Pane) != 0 {
		t.Fatalf("mutating tombstone=%+v found=%v err=%v", mutation, found, err)
	}
	duplicate, created, err := store.CreatePending("interactive-1", "mutating", ActionSendKey)
	if err != nil || created || duplicate.State != OperationCompleted {
		t.Fatalf("mutating duplicate=%+v created=%v err=%v", duplicate, created, err)
	}
	pending, found, err := store.Lookup("pending-input")
	if err != nil || !found || pending.State != OperationPending {
		t.Fatalf("pending mutation=%+v found=%v err=%v", pending, found, err)
	}
}
