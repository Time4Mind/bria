package runtimehost

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type fixedInputQueueLimit int

func (limit fixedInputQueueLimit) InputQueueLimit(int64) int { return int(limit) }

func TestLocalExecutorBoundsPreparedInputFIFOAndPreservesControlReserve(t *testing.T) {
	store := NewMemoryOperationStore()
	executor, err := NewLocalExecutor("node-a", &fakeRuntimeDriver{}, store)
	if err != nil {
		t.Fatal(err)
	}
	executor.SetInputQueueLimitResolver(fixedInputQueueLimit(5))
	if err := executor.Prepare(RuntimeBinding{
		NodeID: "node-a", SessionID: "session-a", Generation: 4, Backend: "claude",
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

	var first Request
	for index := 0; index < 5; index++ {
		request := testRequest(fmt.Sprintf("input-%d", index), ActionSendInput)
		request.Text = "queued"
		if index == 0 {
			first = request
		}
		if _, err := executor.Submit(context.Background(), request); err != nil {
			t.Fatalf("input %d: %v", index, err)
		}
	}
	duplicate, err := executor.Submit(context.Background(), first)
	if err != nil || !duplicate.Duplicate {
		t.Fatalf("full-queue duplicate=%+v err=%v", duplicate, err)
	}
	rejectedInput := testRequest("input-overflow", ActionSendInput)
	rejectedInput.Text = "must be rejected"
	if _, err := executor.Submit(context.Background(), rejectedInput); !errors.Is(err, ErrQueueFull) {
		t.Fatalf("overflow input error=%v", err)
	}
	if _, found, err := store.Lookup(rejectedInput.OperationID); err != nil || found {
		t.Fatalf("rejected input persisted found=%t err=%v", found, err)
	}

	for index := 0; index < controlQueueReserve; index++ {
		request := testRequest(fmt.Sprintf("control-%d", index), ActionStop)
		if _, err := executor.Submit(context.Background(), request); err != nil {
			t.Fatalf("control %d: %v", index, err)
		}
	}
	rejectedControl := testRequest("control-overflow", ActionStop)
	if _, err := executor.Submit(context.Background(), rejectedControl); !errors.Is(err, ErrQueueFull) {
		t.Fatalf("overflow control error=%v", err)
	}
	if _, found, err := store.Lookup(rejectedControl.OperationID); err != nil || found {
		t.Fatalf("rejected control persisted found=%t err=%v", found, err)
	}
}

func TestLocalExecutorInputLimitIsAtomicUnderConcurrentAdmission(t *testing.T) {
	executor, err := NewLocalExecutor("node-a", &fakeRuntimeDriver{}, NewMemoryOperationStore())
	if err != nil {
		t.Fatal(err)
	}
	executor.SetInputQueueLimitResolver(fixedInputQueueLimit(5))
	if err := executor.Prepare(RuntimeBinding{
		NodeID: "node-a", SessionID: "session-a", Generation: 4, Backend: "claude",
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

	var accepted atomic.Int32
	var rejected atomic.Int32
	var workers sync.WaitGroup
	for index := 0; index < 20; index++ {
		workers.Add(1)
		go func(index int) {
			defer workers.Done()
			request := testRequest(fmt.Sprintf("concurrent-%d", index), ActionSendInput)
			request.Text = "queued"
			_, err := executor.Submit(context.Background(), request)
			switch {
			case err == nil:
				accepted.Add(1)
			case errors.Is(err, ErrQueueFull):
				rejected.Add(1)
			default:
				t.Errorf("submit %d: %v", index, err)
			}
		}(index)
	}
	workers.Wait()
	if accepted.Load() != 5 || rejected.Load() != 15 {
		t.Fatalf("accepted/rejected=%d/%d, want 5/15", accepted.Load(), rejected.Load())
	}
}

func TestInputQueueLimitFallsBackToSafeDefault(t *testing.T) {
	for _, value := range []int{-1, 0, 6, 1000} {
		if got := normalizeInputQueueLimit(value); got != 5 {
			t.Fatalf("normalize(%d)=%d, want 5", value, got)
		}
	}
	for _, value := range []int{5, 10, 20} {
		if got := normalizeInputQueueLimit(value); got != value {
			t.Fatalf("normalize(%d)=%d", value, got)
		}
	}
}
