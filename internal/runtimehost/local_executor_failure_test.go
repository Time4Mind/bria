package runtimehost

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestLocalExecutorNeverRepeatsFailedInput(t *testing.T) {
	driver := &fakeRuntimeDriver{err: errors.New("driver failed")}
	executor := newTestExecutor(t, driver)
	request := testRequest("failed-input", ActionSendInput)
	request.Text = "only once"
	if _, err := executor.Submit(context.Background(), request); err != nil {
		t.Fatal(err)
	}
	waitCompleted(t, executor.store, request.OperationID)
	receipt, err := executor.Submit(context.Background(), request)
	if err != nil || !receipt.Duplicate {
		t.Fatalf("failed duplicate: receipt=%+v err=%v", receipt, err)
	}
	if got := len(driver.snapshot()); got != 1 {
		t.Fatalf("driver calls = %d, want 1", got)
	}
}

func TestLocalExecutorRefusesRecoveredPendingOperation(t *testing.T) {
	driver := &fakeRuntimeDriver{}
	store := NewMemoryOperationStore()
	request := testRequest("uncertain-input", ActionSendInput)
	request.Text = "must not be replayed"
	if _, _, err := store.CreatePending(
		request.OperationID, requestFingerprint(request), request.Action,
	); err != nil {
		t.Fatal(err)
	}
	executor, err := NewLocalExecutor("node-a", driver, store)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = executor.Shutdown(context.Background())
	})
	if err := executor.Register(RuntimeBinding{
		NodeID: "node-a", SessionID: "session-a", Generation: 4,
		TmuxTarget: "@7", Backend: "claude",
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := executor.Submit(context.Background(), request); !errors.Is(err, ErrOperationOutcomeUnknown) {
		t.Fatalf("recovered pending error = %v", err)
	}
	if len(driver.snapshot()) != 0 {
		t.Fatal("uncertain operation was replayed")
	}
}

func waitCompleted(t *testing.T, store OperationStore, operationID string) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		record, found, err := store.Lookup(operationID)
		if err != nil {
			t.Fatal(err)
		}
		if found && record.State == OperationCompleted {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("operation %s did not complete", operationID)
}
