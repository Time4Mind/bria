package turncompletion_test

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"bria/internal/turncompletion"
)

func TestResolvedSignalPublishesTerminalStateToEveryReader(t *testing.T) {
	signal := turncompletion.New()
	select {
	case <-signal.Done():
		t.Fatal("unresolved signal is already done")
	default:
	}
	signal.Resolve("failed")
	select {
	case <-signal.Done():
	default:
		t.Fatal("resolved signal is not done")
	}
	signal.Resolve("succeeded")
	for i := 0; i < 3; i++ {
		state, err := signal.Wait(context.Background())
		if err != nil || state != "failed" {
			t.Fatalf("terminal state=%q err=%v, want first result failed", state, err)
		}
	}
}

func TestWaitCancellationDoesNotResolveOrConsumeSignal(t *testing.T) {
	signal := turncompletion.New()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if state, err := signal.Wait(ctx); !errors.Is(err, context.Canceled) || state != "" {
		t.Fatalf("cancelled wait=%q, %v", state, err)
	}
	select {
	case <-signal.Done():
		t.Fatal("wait cancellation resolved signal")
	default:
	}
	signal.Resolve("succeeded")
	if state, err := signal.Wait(context.Background()); err != nil || state != "succeeded" {
		t.Fatalf("later wait=%q, %v", state, err)
	}
	if state, err := signal.Wait(ctx); err != nil || state != "succeeded" {
		t.Fatalf("already-published completion lost to cancellation: %q, %v", state, err)
	}
}

func TestWaitingReadersAllObserveOneConcurrentResolution(t *testing.T) {
	signal := turncompletion.New()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	const count = 32
	results := make(chan string, count)
	var readers, writers sync.WaitGroup
	for i := 0; i < count; i++ {
		readers.Add(1)
		go func() {
			defer readers.Done()
			state, err := signal.Wait(ctx)
			if err != nil {
				t.Error(err)
			}
			results <- state
		}()
	}
	for i := 0; i < count; i++ {
		writers.Add(1)
		go func(i int) { defer writers.Done(); signal.Resolve(fmt.Sprintf("terminal-%d", i)) }(i)
	}
	writers.Wait()
	readers.Wait()
	close(results)
	winner, err := signal.Wait(ctx)
	if err != nil || winner == "" {
		t.Fatal("terminal result was not published")
	}
	for state := range results {
		if state != winner {
			t.Fatal("readers observed different terminal results")
		}
	}
}

func TestEmptyTerminalAndDeadlineRemainDistinct(t *testing.T) {
	signal := turncompletion.New()
	if _, err := signal.Wait(nil); err == nil {
		t.Fatal("nil context was accepted")
	}
	ctx, cancel := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
	defer cancel()
	if _, err := signal.Wait(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("expired wait error=%v", err)
	}
	signal.Resolve("")
	if state, err := signal.Wait(context.Background()); state != "" || err != nil {
		t.Fatalf("empty terminal value=%q, %v", state, err)
	}
}
