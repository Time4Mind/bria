package runtimehost

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

func TestLocalExecutorShutdownCompletesUnstartedQueueAndClosesAdmission(t *testing.T) {
	store := NewMemoryOperationStore()
	executor, err := NewLocalExecutor("node-a", &fakeRuntimeDriver{}, store)
	if err != nil {
		t.Fatal(err)
	}
	executor.inputTiming = nil
	if err := executor.Prepare(RuntimeBinding{
		NodeID: "node-a", SessionID: "session-a", Generation: 4, Backend: "claude",
	}); err != nil {
		t.Fatal(err)
	}
	queued := testRequest("queued-at-shutdown", ActionSendInput)
	queued.Text = "must never reach tmux"
	if _, err := executor.Submit(context.Background(), queued); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := executor.Shutdown(ctx); err != nil {
		t.Fatal(err)
	}
	if !executor.ShutdownComplete() {
		t.Fatal("shutdown returned before terminal queue writes completed")
	}
	result, found, err := executor.LookupResult(context.Background(), queued.OperationID)
	if !found || !errors.Is(err, ErrRuntimeShuttingDown) || !result.Accepted || result.Delivered {
		t.Fatalf("queued result=%+v found=%t err=%v", result, found, err)
	}
	record, found, err := store.Lookup(queued.OperationID)
	if err != nil || !found || record.State != OperationCompleted ||
		record.Error != ErrRuntimeShuttingDown.Error() {
		t.Fatalf("queued record=%+v found=%t err=%v", record, found, err)
	}

	rejected := testRequest("after-shutdown", ActionStop)
	if _, err := executor.Submit(context.Background(), rejected); !errors.Is(err, ErrRuntimeShuttingDown) {
		t.Fatalf("submit after shutdown error=%v", err)
	}
	if _, found, err := store.Lookup(rejected.OperationID); err != nil || found {
		t.Fatalf("rejected operation persisted found=%t err=%v", found, err)
	}
	if err := executor.Register(RuntimeBinding{
		NodeID: "node-a", SessionID: "other", Generation: 1,
		TmuxTarget: "@8", Backend: "claude",
	}); !errors.Is(err, ErrRuntimeShuttingDown) {
		t.Fatalf("register after shutdown error=%v", err)
	}
}

func TestLocalExecutorShutdownPersistsTerminalQueueBeforeBoltReopen(t *testing.T) {
	path := filepath.Join(t.TempDir(), "operations.db")
	store, err := OpenBoltOperationStore(path)
	if err != nil {
		t.Fatal(err)
	}
	executor, err := NewLocalExecutor("node-a", &fakeRuntimeDriver{}, store)
	if err != nil {
		t.Fatal(err)
	}
	executor.inputTiming = nil
	if err := executor.Prepare(RuntimeBinding{
		NodeID: "node-a", SessionID: "session-a", Generation: 4, Backend: "claude",
	}); err != nil {
		t.Fatal(err)
	}
	request := testRequest("bolt-queued-at-shutdown", ActionSendInput)
	request.Text = "durable cancellation"
	if _, err := executor.Submit(context.Background(), request); err != nil {
		t.Fatal(err)
	}
	if err := executor.Shutdown(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}

	reopened, err := OpenBoltOperationStore(path)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	record, found, err := reopened.Lookup(request.OperationID)
	if err != nil || !found || record.State != OperationCompleted ||
		record.Error != ErrRuntimeShuttingDown.Error() || record.CompletedAt.IsZero() {
		t.Fatalf("reopened record=%+v found=%t err=%v", record, found, err)
	}
}

func TestLocalExecutorShutdownCancelsRunningOperationBeforeReturning(t *testing.T) {
	driver := &fakeRuntimeDriver{
		firstInput: make(chan struct{}, 1), release: make(chan struct{}),
	}
	store := NewMemoryOperationStore()
	executor, err := NewLocalExecutor("node-a", driver, store)
	if err != nil {
		t.Fatal(err)
	}
	executor.inputTiming = nil
	if err := executor.Register(RuntimeBinding{
		NodeID: "node-a", SessionID: "session-a", Generation: 4,
		TmuxTarget: "@7", Backend: "claude",
	}); err != nil {
		t.Fatal(err)
	}
	request := testRequest("running-at-shutdown", ActionSendInput)
	request.Text = "cancel me"
	if _, err := executor.Submit(context.Background(), request); err != nil {
		t.Fatal(err)
	}
	select {
	case <-driver.firstInput:
	case <-time.After(time.Second):
		t.Fatal("runtime operation did not start")
	}

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := executor.Shutdown(ctx); err != nil {
		t.Fatal(err)
	}
	record, found, err := store.Lookup(request.OperationID)
	if err != nil || !found || record.State != OperationCompleted ||
		record.Error != context.Canceled.Error() {
		t.Fatalf("running record=%+v found=%t err=%v", record, found, err)
	}
	if got := len(driver.snapshot()); got != 1 {
		t.Fatalf("driver calls=%d want=1", got)
	}
}

func TestInputOutcomeClassifiesShutdownCancellation(t *testing.T) {
	for _, err := range []error{ErrRuntimeShuttingDown, context.Canceled} {
		if got := inputOutcome(Result{}, err, "tmux_send"); got != "cancelled" {
			t.Fatalf("error=%v outcome=%q", err, got)
		}
	}
}

func TestLocalExecutorSubmitShutdownRaceLeavesNoCreatedPendingOperation(t *testing.T) {
	store := NewMemoryOperationStore()
	executor, err := NewLocalExecutor("node-a", &fakeRuntimeDriver{}, store)
	if err != nil {
		t.Fatal(err)
	}
	executor.inputTiming = nil
	if err := executor.Prepare(RuntimeBinding{
		NodeID: "node-a", SessionID: "session-a", Generation: 4, Backend: "claude",
	}); err != nil {
		t.Fatal(err)
	}

	const submissions = 128
	start := make(chan struct{})
	errs := make([]error, submissions)
	var submitters sync.WaitGroup
	for index := range submissions {
		submitters.Add(1)
		go func(index int) {
			defer submitters.Done()
			<-start
			request := testRequest(fmt.Sprintf("shutdown-race-%03d", index), ActionSendInput)
			request.Text = "bounded"
			_, errs[index] = executor.Submit(context.Background(), request)
		}(index)
	}
	shutdownDone := make(chan error, 1)
	go func() {
		<-start
		shutdownDone <- executor.Shutdown(context.Background())
	}()
	close(start)
	submitters.Wait()
	if err := <-shutdownDone; err != nil {
		t.Fatal(err)
	}

	for index, submitErr := range errs {
		operationID := fmt.Sprintf("shutdown-race-%03d", index)
		record, found, err := store.Lookup(operationID)
		if err != nil {
			t.Fatal(err)
		}
		if submitErr != nil {
			if (!errors.Is(submitErr, ErrRuntimeShuttingDown) &&
				!errors.Is(submitErr, ErrQueueFull)) || found {
				t.Fatalf("rejected %s found=%t submit_err=%v", operationID, found, submitErr)
			}
			continue
		}
		if !found || record.State != OperationCompleted ||
			record.Error != ErrRuntimeShuttingDown.Error() {
			t.Fatalf("accepted %s record=%+v found=%t", operationID, record, found)
		}
	}
	if len(executor.active) != 0 {
		t.Fatalf("active operations after shutdown=%d", len(executor.active))
	}
}
