package runtimehost

import (
	"context"
	"testing"
	"time"
)

func newTestExecutor(t *testing.T, driver RuntimeDriver) *LocalExecutor {
	t.Helper()
	executor, err := NewLocalExecutor("node-a", driver, NewMemoryOperationStore())
	if err != nil {
		t.Fatal(err)
	}
	if err := executor.Register(RuntimeBinding{
		NodeID: "node-a", SessionID: "session-a", Generation: 4,
		TmuxTarget: "@7", Backend: "claude",
	}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		if err := executor.Shutdown(ctx); err != nil {
			t.Errorf("shutdown: %v", err)
		}
	})
	return executor
}

func testRequest(operationID string, action Action) Request {
	return Request{
		OperationID: operationID, ActorID: 42, NodeID: "node-a",
		SessionID: "session-a", ExpectedGeneration: 4,
		Action: action, Backend: "claude",
	}
}

func waitSubmittedResult(t *testing.T, executor *LocalExecutor, request Request) Result {
	t.Helper()
	if _, err := executor.Submit(context.Background(), request); err != nil {
		t.Fatal(err)
	}
	return waitResult(t, executor, request.OperationID)
}

func waitResult(t *testing.T, executor *LocalExecutor, operationID string) Result {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		result, found, err := executor.LookupResult(context.Background(), operationID)
		if found {
			if err != nil {
				t.Fatalf("operation %s: %v", operationID, err)
			}
			return result
		}
		if err != nil {
			t.Fatal(err)
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("operation %s did not complete", operationID)
	return Result{}
}
