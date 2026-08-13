package runtimehost

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/Time4Mind/bria/internal/interactive"
)

func TestLocalExecutorVerifiesPromptBeforeSendingInteractiveKey(t *testing.T) {
	pane := []byte("Would you like to proceed?\n  1. Yes\nEsc to cancel\n")
	driver := &fakeRuntimeDriver{pane: pane}
	executor := newTestExecutor(t, driver)
	prompt, ok := interactive.Detect(pane)
	if !ok {
		t.Fatal("test prompt was not detected")
	}
	request := testRequest("key-1", ActionSendKey)
	request.Key = KeyEnter
	request.ExpectedPromptHash = prompt.Hash
	result := waitSubmittedResult(t, executor, request)
	if string(result.Pane) != string(pane) {
		t.Fatalf("pane=%q", result.Pane)
	}
	calls := driver.snapshot()
	if len(calls) != 3 || calls[0].action != "capture" ||
		calls[1].action != "key" || calls[1].value != "Enter" || calls[2].action != "capture" {
		t.Fatalf("calls=%#v", calls)
	}
}

func TestLocalExecutorRejectsStaleInteractivePromptWithoutKey(t *testing.T) {
	driver := &fakeRuntimeDriver{pane: []byte("ordinary output\n")}
	executor := newTestExecutor(t, driver)
	request := testRequest("stale-key", ActionSendKey)
	request.Key = KeyCtrlC
	request.ExpectedPromptHash = "0123456789abcdef0123456789abcdef"
	if _, err := executor.Submit(context.Background(), request); err != nil {
		t.Fatal(err)
	}
	var found bool
	var err error
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		_, found, err = executor.LookupResult(context.Background(), request.OperationID)
		if found {
			break
		}
		time.Sleep(time.Millisecond)
	}
	if !found || !errors.Is(err, ErrStaleRuntime) {
		t.Fatalf("found=%v err=%v", found, err)
	}
	for _, call := range driver.snapshot() {
		if call.action == "key" {
			t.Fatalf("stale prompt emitted key: %#v", call)
		}
	}
}
