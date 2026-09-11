package runtimediagnostic

import (
	"io"
	"reflect"
	"testing"
	"time"
)

type closerFunc func() error

func (close closerFunc) Close() error { return close() }

func TestDrainBeforeWaitClosesStuckPipeOnlyAfterStop(t *testing.T) {
	done := make(chan struct{})
	var events []string
	DrainBeforeWait(done, closerFunc(func() error {
		events = append(events, "close")
		close(done)
		return nil
	}), func() { events = append(events, "stop") }, time.Millisecond)
	if !reflect.DeepEqual(events, []string{"stop", "close"}) {
		t.Fatalf("events = %v, want stop then close", events)
	}
}

func TestDrainBeforeWaitPreservesNaturallyDrainedPipe(t *testing.T) {
	done := make(chan struct{})
	close(done)
	closed := false
	DrainBeforeWait(done, closerFunc(func() error { closed = true; return io.ErrClosedPipe }), func() {}, time.Second)
	if closed {
		t.Fatal("naturally drained pipe was closed again")
	}
}

func TestDrainBeforeWaitAcceptsDrainCompletedByStop(t *testing.T) {
	done := make(chan struct{})
	closed := false
	DrainBeforeWait(done, closerFunc(func() error { closed = true; return nil }), func() { close(done) }, time.Millisecond)
	if closed {
		t.Fatal("pipe completed by process stop was force-closed")
	}
}
