package runtimehost

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"
)

func TestSuccessfulCloseRetiresLocalRuntimeWorker(t *testing.T) {
	executor := newTestExecutor(t, &fakeRuntimeDriver{})
	executor.SetArchiveWriter(&fakeArchiveWriter{})
	request := testRequest("close-retire", ActionClose)
	request.ArchiveCommitID = "archive-close-retire"
	request.Archive = &ArchivePayload{
		ArchiveID: request.ArchiveCommitID, OwnerID: 42, Workdir: "/work",
		ProviderSessionID: "provider", CreatedAt: time.Unix(1, 0).UTC(),
		ArchivedAt: time.Unix(2, 0).UTC(),
	}
	result := waitSubmittedResult(t, executor, request)
	if !result.Delivered || !result.ArchiveCommitted {
		t.Fatalf("close result=%+v", result)
	}

	deadline := time.Now().Add(time.Second)
	for {
		executor.mu.RLock()
		_, exists := executor.sessions[runtimeKey("node-a", "session-a")]
		executor.mu.RUnlock()
		if !exists {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("closed runtime remained registered")
		}
		time.Sleep(time.Millisecond)
	}
	if _, err := executor.Submit(context.Background(), testRequest("after-close", ActionStop)); !errors.Is(err, ErrRuntimeUnavailable) {
		t.Fatalf("operation after close error=%v", err)
	}
	waitLocalExecutorWorkers(t, executor)

	receipt, err := executor.Submit(context.Background(), request)
	if err != nil || !receipt.Duplicate || !receipt.Accepted {
		t.Fatalf("completed close duplicate: receipt=%+v err=%v", receipt, err)
	}
}

func TestConcurrentCloseSubmissionsAreIdempotent(t *testing.T) {
	driver := &fakeRuntimeDriver{
		closeStarted: make(chan struct{}, 1),
		releaseClose: make(chan struct{}),
	}
	executor := newTestExecutor(t, driver)
	executor.SetArchiveWriter(&fakeArchiveWriter{})
	request := testRequest("close-concurrent", ActionClose)
	request.ArchiveCommitID = "archive-close-concurrent"
	request.Archive = &ArchivePayload{
		ArchiveID: request.ArchiveCommitID, OwnerID: 42, Workdir: "/work",
		ProviderSessionID: "provider", CreatedAt: time.Unix(1, 0).UTC(),
		ArchivedAt: time.Unix(2, 0).UTC(),
	}

	const attempts = 16
	start := make(chan struct{})
	receipts := make([]Receipt, attempts)
	errs := make([]error, attempts)
	var wg sync.WaitGroup
	for i := 0; i < attempts; i++ {
		wg.Add(1)
		go func(index int) {
			defer wg.Done()
			<-start
			receipts[index], errs[index] = executor.Submit(context.Background(), request)
		}(i)
	}
	close(start)
	select {
	case <-driver.closeStarted:
	case <-time.After(time.Second):
		t.Fatal("close did not start")
	}
	wg.Wait()
	close(driver.releaseClose)
	result := waitResult(t, executor, request.OperationID)
	if !result.Delivered || !result.ArchiveCommitted {
		t.Fatalf("close result=%+v", result)
	}
	for i := range errs {
		if errs[i] != nil || !receipts[i].Accepted {
			t.Fatalf("submission %d: receipt=%+v err=%v", i, receipts[i], errs[i])
		}
	}
	if got := len(driver.snapshot()); got != 1 {
		t.Fatalf("close driver calls=%d, want 1", got)
	}
	waitLocalExecutorWorkers(t, executor)
}

func waitLocalExecutorWorkers(t *testing.T, executor *LocalExecutor) {
	t.Helper()
	done := make(chan struct{})
	go func() {
		executor.workers.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("local executor worker did not exit")
	}
}

func TestSuccessfulDiscardClosesWithoutArchiveAndRetiresRuntime(t *testing.T) {
	driver := &fakeRuntimeDriver{}
	executor := newTestExecutor(t, driver)
	result := waitSubmittedResult(t, executor, testRequest("discard-empty", ActionDiscard))
	if !result.Delivered || result.ArchiveCommitted {
		t.Fatalf("discard result=%+v", result)
	}
	calls := driver.snapshot()
	if len(calls) != 1 || calls[0].action != "close" {
		t.Fatalf("discard calls=%#v", calls)
	}
	executor.mu.RLock()
	_, exists := executor.sessions[runtimeKey("node-a", "session-a")]
	executor.mu.RUnlock()
	if exists {
		t.Fatal("discarded runtime remained registered")
	}
}
