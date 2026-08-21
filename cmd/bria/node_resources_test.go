package main

import (
	"context"
	"testing"
	"time"

	"github.com/Time4Mind/bria/internal/processmetrics"
)

func TestNodeResourceMonitorSamplesImmediatelyOnTicksAndStops(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	ticks := make(chan time.Time)
	captured := make(chan struct{}, 3)
	emitted := make(chan processmetrics.Snapshot, 3)
	done := make(chan struct{})
	go func() {
		defer close(done)
		runNodeResourceMonitor(
			ctx, ticks,
			func(context.Context) processmetrics.Snapshot {
				captured <- struct{}{}
				return processmetrics.Snapshot{Goroutines: 7}
			},
			func(snapshot processmetrics.Snapshot) { emitted <- snapshot },
		)
	}()
	assertResourceSample(t, captured, emitted)
	ticks <- time.Now()
	assertResourceSample(t, captured, emitted)
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("resource monitor did not stop")
	}
	select {
	case ticks <- time.Now():
		t.Fatal("resource monitor consumed a tick after cancellation")
	default:
	}
}

func assertResourceSample(
	t *testing.T,
	captured <-chan struct{},
	emitted <-chan processmetrics.Snapshot,
) {
	t.Helper()
	select {
	case <-captured:
	case <-time.After(time.Second):
		t.Fatal("resource capture did not run")
	}
	select {
	case snapshot := <-emitted:
		if snapshot.Goroutines != 7 {
			t.Fatalf("snapshot=%+v", snapshot)
		}
	case <-time.After(time.Second):
		t.Fatal("resource snapshot was not emitted")
	}
}
