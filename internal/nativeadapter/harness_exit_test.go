package nativeadapter

import (
	"context"
	"errors"
	"testing"
	"time"

	"bria/internal/runtimeprotocol"
)

func TestHarnessExitWaitsForScannerAndDrainsFinalFrame(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	h := &adapterHarness{ctx: ctx, lines: make(chan []byte, 1), done: make(chan error), readerDone: make(chan struct{})}
	returned := make(chan struct{})
	var got runtimeprotocol.AdapterMessage
	go func() {
		defer close(returned)
		got = h.receiveRaw(t)
	}()
	// Force receiveRaw to select process exit while the scanner has not yet
	// published its last frame. The second handshake restores the exit result
	// for cleanup; neither handshake depends on scheduler timing.
	select {
	case h.done <- nil:
	case <-ctx.Done():
		t.Fatal("receiver did not observe process exit")
	}
	select {
	case err := <-h.done:
		if err != nil {
			t.Fatal(err)
		}
	case <-ctx.Done():
		t.Fatal("receiver did not preserve exit result")
	}
	h.lines <- []byte(`{"protocol":1,"type":"closed","provider_session_id":"` + fixtureSession + `"}`)
	close(h.readerDone)
	select {
	case <-returned:
	case <-ctx.Done():
		t.Fatal("receiver did not drain scanner frame")
	}
	if got.Type != runtimeprotocol.TypeClosed || got.ProviderSessionID != fixtureSession {
		t.Fatalf("final frame lost after process exit: %+v", got)
	}
}

func TestHarnessDrainsBufferedFramesAndPreservesExit(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	h := &adapterHarness{ctx: ctx, lines: make(chan []byte, 2), done: make(chan error, 1), readerDone: make(chan struct{})}
	h.lines <- []byte(`{"protocol":1,"type":"ready","provider_session_id":"` + fixtureSession + `","readiness":"protocol","authentication":"unknown"}`)
	h.lines <- []byte(`{"protocol":1,"type":"closed","provider_session_id":"` + fixtureSession + `"}`)
	exitErr := errors.New("synthetic adapter exit")
	h.done <- exitErr
	close(h.readerDone)
	for _, want := range []runtimeprotocol.MessageType{runtimeprotocol.TypeReady, runtimeprotocol.TypeClosed} {
		if got := h.receiveRaw(t); got.Type != want {
			t.Fatalf("frame order=%s want=%s", got.Type, want)
		}
	}
	select {
	case err := <-h.done:
		if !errors.Is(err, exitErr) {
			t.Fatalf("exit error lost: %v", err)
		}
	default:
		t.Fatal("cleanup exit result consumed")
	}
}

type harnessFailedReader struct{ err error }

func (r harnessFailedReader) Read([]byte) (int, error) { return 0, r.err }

func TestHarnessScannerPreservesReadFailure(t *testing.T) {
	fault := errors.New("synthetic scanner failure")
	h := &adapterHarness{ctx: context.Background(), lines: make(chan []byte, 1), readerDone: make(chan struct{})}
	go h.scanOutput(harnessFailedReader{err: fault})
	<-h.readerDone
	if !errors.Is(h.readerErr, fault) {
		t.Fatalf("scanner failure lost: %v", h.readerErr)
	}
}
