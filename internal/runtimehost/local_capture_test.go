package runtimehost

import (
	"context"
	"testing"
	"time"
)

func TestLocalExecutorCaptureUsesReadOnlyExecutionLane(t *testing.T) {
	driver := &fakeRuntimeDriver{pane: []byte("\x1b[31mhello\x1b[0m")}
	executor := newTestExecutor(t, driver)
	request := testRequest("capture-1", ActionCapture)
	result := waitSubmittedResult(t, executor, request)
	if string(result.Pane) != string(driver.pane) {
		t.Fatalf("pane = %q", result.Pane)
	}
}

func TestLocalExecutorCaptureBypassesBlockedInputFIFO(t *testing.T) {
	driver := &fakeRuntimeDriver{
		pane: []byte("ready"), firstInput: make(chan struct{}, 1), release: make(chan struct{}),
	}
	executor := newTestExecutor(t, driver)
	input := testRequest("input-blocked", ActionSendInput)
	input.Text = "long input"
	if _, err := executor.Submit(context.Background(), input); err != nil {
		t.Fatal(err)
	}
	select {
	case <-driver.firstInput:
	case <-time.After(time.Second):
		t.Fatal("input did not enter FIFO")
	}
	defer close(driver.release)
	capture := testRequest("capture-priority", ActionCapture)
	if _, err := executor.Submit(context.Background(), capture); err != nil {
		t.Fatal(err)
	}
	result := waitResult(t, executor, capture.OperationID)
	if string(result.Pane) != "ready" {
		t.Fatalf("capture pane=%q", result.Pane)
	}
}
