package runtimehost

import (
	"bytes"
	"context"
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
	if err := store.Complete("interactive-1", "mutating", Result{
		Pane: pane, ResolvedText: "resolved voice prompt", GeneratedName: "generated name",
		Detail: "runtime operation delivered", Delivered: true,
	}, nil); err != nil {
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
	now = now.Add(operationPayloadRetention + time.Second)
	report, err := store.maintain(context.Background(), now)
	if err != nil {
		t.Fatal(err)
	}
	if report.Deleted != 101 || report.PayloadTrimmed != 1 {
		t.Fatalf("cleanup report=%+v", report)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	fragmented, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}

	store, err = openBoltOperationStoreWithCompaction(path, clock, 1)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	after, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if after.Size() >= fragmented.Size()/2 {
		t.Fatalf("startup compaction did not reclaim prior free pages: before=%d after=%d",
			fragmented.Size(), after.Size())
	}
	if _, found, err := store.Lookup("pane-99"); err != nil || found {
		t.Fatalf("expired capture found=%v err=%v", found, err)
	}
	if _, found, err := store.Lookup("pane-legacy"); err != nil || found {
		t.Fatalf("legacy capture found=%v err=%v", found, err)
	}
	mutation, found, err := store.Lookup("interactive-1")
	if err != nil || !found || mutation.State != OperationCompleted ||
		len(mutation.Result.Pane) != 0 || mutation.Result.ResolvedText != "" ||
		mutation.Result.GeneratedName != "" || mutation.Result.Detail != "" ||
		!mutation.Result.Delivered {
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

func TestBoltOperationStoreExpiresPendingAndCompletedRecordsAfterThirtyDays(t *testing.T) {
	path := filepath.Join(t.TempDir(), "runtime-operations.db")
	now := time.Date(2026, time.August, 1, 12, 0, 0, 0, time.UTC)
	clock := func() time.Time { return now }
	store, err := openBoltOperationStore(path, clock)
	if err != nil {
		t.Fatal(err)
	}
	if _, created, err := store.CreatePending("pending-old", "pending", ActionSendInput); err != nil || !created {
		t.Fatalf("create pending: created=%v err=%v", created, err)
	}
	if _, created, err := store.CreatePending("completed-old", "completed", ActionStop); err != nil || !created {
		t.Fatalf("create completed: created=%v err=%v", created, err)
	}
	if err := store.Complete("completed-old", "completed", Result{Accepted: true, Delivered: true}, nil); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}

	now = now.Add(operationRecordRetention - time.Second)
	store, err = openBoltOperationStore(path, clock)
	if err != nil {
		t.Fatal(err)
	}
	for _, operationID := range []string{"pending-old", "completed-old"} {
		if _, found, err := store.Lookup(operationID); err != nil || !found {
			t.Fatalf("record %s expired early found=%v err=%v", operationID, found, err)
		}
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}

	now = now.Add(2 * time.Second)
	store, err = openBoltOperationStore(path, clock)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	for _, operationID := range []string{"pending-old", "completed-old"} {
		if _, found, err := store.Lookup(operationID); err != nil || found {
			t.Fatalf("record %s retained after cutoff found=%v err=%v", operationID, found, err)
		}
	}
}

func TestBoltOperationStoreGivesLegacyRecordsThirtyDayMigrationGrace(t *testing.T) {
	path := filepath.Join(t.TempDir(), "runtime-operations.db")
	now := time.Date(2026, time.August, 1, 12, 0, 0, 0, time.UTC)
	clock := func() time.Time { return now }
	store, err := openBoltOperationStore(path, clock)
	if err != nil {
		t.Fatal(err)
	}
	legacyCompletedAt := now.Add(-365 * 24 * time.Hour)
	if err := store.db.Update(func(tx *bolt.Tx) error {
		bucket := tx.Bucket(operationBucket)
		if err := putOperationRecord(bucket, "legacy-pending", OperationRecord{
			Fingerprint: "legacy", Action: ActionSendInput, State: OperationPending,
		}); err != nil {
			return err
		}
		return putOperationRecord(bucket, "legacy-completed", OperationRecord{
			Fingerprint: "legacy-completed", Action: ActionStop, State: OperationCompleted,
			CompletedAt: legacyCompletedAt, Result: Result{Delivered: true},
		})
	}); err != nil {
		t.Fatal(err)
	}
	report, err := store.maintain(context.Background(), now)
	if err != nil {
		t.Fatal(err)
	}
	if report.Deleted != 0 {
		t.Fatalf("legacy record deleted during migration: %+v", report)
	}
	for _, operationID := range []string{"legacy-pending", "legacy-completed"} {
		record, found, lookupErr := store.Lookup(operationID)
		if lookupErr != nil || !found || !record.CreatedAt.Equal(now) {
			t.Fatalf("migrated %s=%+v found=%v err=%v", operationID, record, found, lookupErr)
		}
	}
	now = now.Add(operationRecordRetention)
	report, err = store.maintain(context.Background(), now)
	if err != nil || report.Deleted != 2 || report.PendingDeleted != 1 {
		t.Fatalf("expiry report=%+v err=%v", report, err)
	}
	if _, found, err := store.Lookup("legacy-pending"); err != nil || found {
		t.Fatalf("legacy record retained found=%v err=%v", found, err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestBoltOperationStoreProtectsActivePendingUntilDurableCompletion(t *testing.T) {
	path := filepath.Join(t.TempDir(), "runtime-operations.db")
	now := time.Date(2026, time.August, 1, 12, 0, 0, 0, time.UTC)
	clock := func() time.Time { return now }
	store, err := openBoltOperationStore(path, clock)
	if err != nil {
		t.Fatal(err)
	}
	driver := &fakeRuntimeDriver{}
	executor, err := NewLocalExecutor("node-a", driver, store)
	if err != nil {
		t.Fatal(err)
	}
	executor.now = clock
	binding := RuntimeBinding{
		NodeID: "node-a", SessionID: "session-a", Generation: 4,
		Workdir: t.TempDir(), Backend: "claude",
	}
	if err := executor.PrepareRecovery(binding); err != nil {
		t.Fatal(err)
	}
	request := testRequest("active-old", ActionSendInput)
	request.Text = "run once"
	if _, err := executor.Submit(context.Background(), request); err != nil {
		t.Fatal(err)
	}
	now = now.Add(operationRecordRetention)
	report, err := store.maintainProtected(
		context.Background(), now, executor.ActiveOperationIDs(),
	)
	if err != nil || report.ProtectedPending != 1 || report.Deleted != 0 {
		t.Fatalf("protected maintenance report=%+v err=%v", report, err)
	}
	record, found, err := store.Lookup(request.OperationID)
	if err != nil || !found || record.State != OperationPending {
		t.Fatalf("protected record=%+v found=%v err=%v", record, found, err)
	}
	binding.TmuxTarget = "@7"
	if err := executor.Register(binding); err != nil {
		t.Fatal(err)
	}
	result := waitResult(t, executor, request.OperationID)
	if !result.Delivered {
		t.Fatalf("result=%+v", result)
	}
	if calls := driver.snapshot(); len(calls) != 1 {
		t.Fatalf("driver calls=%#v", calls)
	}
	receipt, err := executor.Submit(context.Background(), request)
	if err != nil || !receipt.Duplicate {
		t.Fatalf("duplicate receipt=%+v err=%v", receipt, err)
	}
	if err := executor.Shutdown(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestBoltOperationStoreMaintenanceStreamsAcrossBoundedBatches(t *testing.T) {
	path := filepath.Join(t.TempDir(), "runtime-operations.db")
	now := time.Date(2026, time.August, 1, 12, 0, 0, 0, time.UTC)
	clock := func() time.Time { return now }
	store, err := openBoltOperationStore(path, clock)
	if err != nil {
		t.Fatal(err)
	}
	count := operationMaintenanceMaxScan + 17
	if err := store.db.Update(func(tx *bolt.Tx) error {
		bucket := tx.Bucket(operationBucket)
		for index := 0; index < count; index++ {
			operationID := fmt.Sprintf("pending-%05d", index)
			if err := putOperationRecord(bucket, operationID, OperationRecord{
				Fingerprint: operationID, Action: ActionSendInput,
				State: OperationPending, CreatedAt: now,
			}); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	now = now.Add(operationRecordRetention)
	report, err := store.maintain(context.Background(), now)
	if err != nil {
		t.Fatal(err)
	}
	if report.Scanned != operationMaintenanceMaxScan ||
		report.Deleted != operationMaintenanceMaxScan ||
		report.PendingDeleted != operationMaintenanceMaxScan || report.Remaining != 17 {
		t.Fatalf("first maintenance report=%+v want max=%d", report, operationMaintenanceMaxScan)
	}
	report, err = store.maintain(context.Background(), now)
	if err != nil || report.Scanned != 17 || report.Deleted != 17 ||
		report.PendingDeleted != 17 || report.Remaining != 0 {
		t.Fatalf("continued maintenance report=%+v err=%v", report, err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestBoltOperationStorePeriodicMaintenanceRunsOnTick(t *testing.T) {
	path := filepath.Join(t.TempDir(), "runtime-operations.db")
	now := time.Date(2026, time.August, 1, 12, 0, 0, 0, time.UTC)
	clock := func() time.Time { return now }
	store, err := openBoltOperationStore(path, clock)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if _, created, err := store.CreatePending(
		"periodic-old", "pending", ActionSendInput,
	); err != nil || !created {
		t.Fatalf("create pending: created=%v err=%v", created, err)
	}
	now = now.Add(operationRecordRetention)
	ctx, cancel := context.WithCancel(context.Background())
	ticks := make(chan time.Time)
	done := make(chan struct{})
	go func() {
		defer close(done)
		store.runMaintenance(ctx, ticks, nil)
	}()
	ticks <- now
	deadline := time.NewTimer(time.Second)
	defer deadline.Stop()
	poll := time.NewTicker(time.Millisecond)
	defer poll.Stop()
	for {
		_, found, lookupErr := store.Lookup("periodic-old")
		if lookupErr != nil {
			cancel()
			t.Fatal(lookupErr)
		}
		if !found {
			break
		}
		select {
		case <-deadline.C:
			cancel()
			t.Fatal("periodic maintenance did not delete expired record")
		case <-poll.C:
		}
	}
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("periodic maintenance did not stop after cancellation")
	}
}
