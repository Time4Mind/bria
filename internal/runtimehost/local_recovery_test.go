package runtimehost

import (
	"context"
	"testing"
	"time"
)

func TestPrepareRecoveryReplacesArchivedGenerationAndQueuesInput(t *testing.T) {
	driver := &fakeRuntimeDriver{}
	executor := newTestExecutor(t, driver)
	prepared := RuntimeBinding{
		NodeID: "node-a", SessionID: "session-a", Generation: 5, Backend: "claude",
	}
	if err := executor.PrepareRecovery(prepared); err != nil {
		t.Fatal(err)
	}
	request := testRequest("restored-input", ActionSendInput)
	request.ExpectedGeneration = 5
	request.Text = "queued while restoring"
	if _, err := executor.Submit(context.Background(), request); err != nil {
		t.Fatal(err)
	}
	time.Sleep(10 * time.Millisecond)
	if got := driver.snapshot(); len(got) != 0 {
		t.Fatalf("driver called before restored runtime attached: %#v", got)
	}
	prepared.TmuxTarget = "@8"
	if err := executor.Register(prepared); err != nil {
		t.Fatal(err)
	}
	result := waitResult(t, executor, request.OperationID)
	if !result.Delivered {
		t.Fatalf("queued restored operation=%+v", result)
	}
	calls := driver.snapshot()
	if len(calls) != 1 || calls[0].target != "@8" || calls[0].value != request.Text {
		t.Fatalf("restored driver calls=%#v", calls)
	}
}
