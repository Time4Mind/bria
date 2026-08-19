package runtimehost

import (
	"context"
	"errors"
	"reflect"
	"sync"
	"testing"
	"time"
)

type driverCall struct {
	action      string
	target      string
	operationID string
	value       string
}

type fakeRuntimeDriver struct {
	mu         sync.Mutex
	calls      []driverCall
	firstInput chan struct{}
	release    chan struct{}
	pane       []byte
	err        error
}

func (d *fakeRuntimeDriver) SendLiteral(
	_ context.Context,
	target string,
	operationID string,
	text string,
) error {
	d.record(driverCall{"literal", target, operationID, text})
	if d.firstInput != nil {
		select {
		case d.firstInput <- struct{}{}:
			<-d.release
		default:
		}
	}
	return d.err
}

func (d *fakeRuntimeDriver) SendKey(_ context.Context, target, key string) error {
	d.record(driverCall{"key", target, "", key})
	return d.err
}

func (d *fakeRuntimeDriver) Close(_ context.Context, target string) error {
	d.record(driverCall{"close", target, "", ""})
	return d.err
}

func (d *fakeRuntimeDriver) OpenTerminal(_ context.Context, target string) error {
	d.record(driverCall{"terminal", target, "", ""})
	return d.err
}

func (d *fakeRuntimeDriver) CapturePane(_ context.Context, target string) ([]byte, error) {
	d.record(driverCall{"capture", target, "", ""})
	return append([]byte(nil), d.pane...), d.err
}

func (d *fakeRuntimeDriver) record(call driverCall) {
	d.mu.Lock()
	d.calls = append(d.calls, call)
	d.mu.Unlock()
}

func (d *fakeRuntimeDriver) snapshot() []driverCall {
	d.mu.Lock()
	defer d.mu.Unlock()
	return append([]driverCall(nil), d.calls...)
}

func TestLocalExecutorAcknowledgesAndSerializesSessionFIFO(t *testing.T) {
	driver := &fakeRuntimeDriver{
		firstInput: make(chan struct{}, 1),
		release:    make(chan struct{}),
	}
	executor := newTestExecutor(t, driver)
	first := testRequest("op-1", ActionSendInput)
	first.Text = "first"
	second := testRequest("op-2", ActionSendInput)
	second.Text = "second"

	receipt, err := executor.Submit(context.Background(), first)
	if err != nil || !receipt.Accepted {
		t.Fatalf("submit first: receipt=%+v err=%v", receipt, err)
	}
	select {
	case <-driver.firstInput:
	case <-time.After(time.Second):
		t.Fatal("first input did not start")
	}
	start := time.Now()
	receipt, err = executor.Submit(context.Background(), second)
	if err != nil || !receipt.Accepted {
		t.Fatalf("submit second: receipt=%+v err=%v", receipt, err)
	}
	if time.Since(start) > 100*time.Millisecond {
		t.Fatal("submit waited for the blocked runtime worker")
	}
	close(driver.release)
	waitResult(t, executor, first.OperationID)
	waitResult(t, executor, second.OperationID)

	calls := driver.snapshot()
	want := []string{"first", "second"}
	got := []string{calls[0].value, calls[1].value}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("FIFO order = %v, want %v", got, want)
	}
}

func TestLocalExecutorDeduplicatesQueuedAndCompletedInput(t *testing.T) {
	driver := &fakeRuntimeDriver{
		firstInput: make(chan struct{}, 1),
		release:    make(chan struct{}),
	}
	executor := newTestExecutor(t, driver)
	request := testRequest("same-operation", ActionSendInput)
	request.Text = "do not duplicate"
	if _, err := executor.Submit(context.Background(), request); err != nil {
		t.Fatal(err)
	}
	<-driver.firstInput
	receipt, err := executor.Submit(context.Background(), request)
	if err != nil || !receipt.Duplicate {
		t.Fatalf("queued duplicate: receipt=%+v err=%v", receipt, err)
	}
	close(driver.release)
	waitResult(t, executor, request.OperationID)
	receipt, err = executor.Submit(context.Background(), request)
	if err != nil || !receipt.Duplicate {
		t.Fatalf("completed duplicate: receipt=%+v err=%v", receipt, err)
	}
	if got := len(driver.snapshot()); got != 1 {
		t.Fatalf("driver calls = %d, want 1", got)
	}

	changed := request
	changed.Text = "different"
	if _, err := executor.Submit(context.Background(), changed); !errors.Is(err, ErrOperationIDConflict) {
		t.Fatalf("changed duplicate error = %v, want conflict", err)
	}
}

func TestLocalExecutorRejectsStaleAndUnavailableTargets(t *testing.T) {
	driver := &fakeRuntimeDriver{}
	executor := newTestExecutor(t, driver)
	stale := testRequest("stale", ActionStop)
	stale.ExpectedGeneration++
	if _, err := executor.Submit(context.Background(), stale); !errors.Is(err, ErrStaleRuntime) {
		t.Fatalf("stale error = %v", err)
	}
	unavailable := testRequest("missing", ActionStop)
	unavailable.SessionID = "missing"
	if _, err := executor.Submit(context.Background(), unavailable); !errors.Is(err, ErrRuntimeUnavailable) {
		t.Fatalf("unavailable error = %v", err)
	}
	if len(driver.snapshot()) != 0 {
		t.Fatal("driver was called for an invalid target")
	}
}

func TestLocalExecutorClearResetsNamingAndProviderBinding(t *testing.T) {
	driver := &fakeRuntimeDriver{}
	executor := newTestExecutor(t, driver)
	request := testRequest("clear-1", ActionClear)
	result := waitSubmittedResult(t, executor, request)
	if !result.ResetNaming || !result.ResetProviderBinding {
		t.Fatalf("clear result = %+v", result)
	}
	want := []driverCall{
		{"key", "@7", "", "Escape"},
		{"literal", "@7", "clear-1-clear", "/clear"},
	}
	if got := driver.snapshot(); !reflect.DeepEqual(got, want) {
		t.Fatalf("clear calls = %#v, want %#v", got, want)
	}
	next := testRequest("after-clear", ActionSendInput)
	next.ExpectedGeneration++
	next.Text = "new context"
	result = waitSubmittedResult(t, executor, next)
	if !result.Delivered {
		t.Fatalf("operation after clear = %+v", result)
	}
}

func TestLocalExecutorQueuesCommandsUntilRecoveryAttachesRuntime(t *testing.T) {
	driver := &fakeRuntimeDriver{}
	executor, err := NewLocalExecutor("node-a", driver, NewMemoryOperationStore())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		if err := executor.Shutdown(ctx); err != nil {
			t.Errorf("shutdown: %v", err)
		}
	})
	binding := RuntimeBinding{
		NodeID: "node-a", SessionID: "session-a", Generation: 4, Backend: "claude",
	}
	if err := executor.Prepare(binding); err != nil {
		t.Fatal(err)
	}
	request := testRequest("queued-during-recovery", ActionSendInput)
	request.Text = "wait for runtime"
	start := time.Now()
	if _, err := executor.Submit(context.Background(), request); err != nil {
		t.Fatal(err)
	}
	if time.Since(start) > 100*time.Millisecond {
		t.Fatal("submit waited for recovery")
	}
	time.Sleep(10 * time.Millisecond)
	if got := driver.snapshot(); len(got) != 0 {
		t.Fatalf("driver called before attach: %#v", got)
	}
	binding.TmuxTarget = "@7"
	if err := executor.Register(binding); err != nil {
		t.Fatal(err)
	}
	result := waitResult(t, executor, request.OperationID)
	if !result.Delivered {
		t.Fatalf("queued result = %+v", result)
	}
	calls := driver.snapshot()
	if len(calls) != 1 || calls[0].value != request.Text {
		t.Fatalf("driver calls = %#v", calls)
	}
}

func TestUnregisterCompletesPreparedOperationsAsUnavailable(t *testing.T) {
	driver := &fakeRuntimeDriver{}
	executor, err := NewLocalExecutor("node-a", driver, NewMemoryOperationStore())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		if err := executor.Shutdown(ctx); err != nil {
			t.Errorf("shutdown: %v", err)
		}
	})
	binding := RuntimeBinding{
		NodeID: "node-a", SessionID: "session-a", Generation: 4, Backend: "codex",
	}
	if err := executor.Prepare(binding); err != nil {
		t.Fatal(err)
	}
	request := testRequest("abandoned-start", ActionSendInput)
	request.ExpectedGeneration = 4
	request.Backend = "codex"
	request.Text = "never delivered"
	if _, err := executor.Submit(context.Background(), request); err != nil {
		t.Fatal(err)
	}
	if err := executor.Unregister("node-a", "session-a", 4); err != nil {
		t.Fatal(err)
	}
	_, found, err := executor.LookupResult(context.Background(), request.OperationID)
	if !found || !errors.Is(err, ErrRuntimeUnavailable) {
		t.Fatalf("abandoned operation found=%v err=%v", found, err)
	}
	if len(driver.snapshot()) != 0 {
		t.Fatal("abandoned operation reached runtime")
	}
}
