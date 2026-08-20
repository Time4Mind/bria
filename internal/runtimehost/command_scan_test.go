package runtimehost

import (
	"strings"
	"testing"
	"time"
)

func TestJSONRPCScannerStopsWhenConsumerLeaves(t *testing.T) {
	lines := make(chan []byte, 1)
	errorsOut := make(chan error, 1)
	stop := make(chan struct{})
	done := make(chan struct{})
	input := strings.Repeat("{\"id\":1}\n", 32)
	go func() {
		scanJSONRPCLines(strings.NewReader(input), lines, errorsOut, stop)
		close(done)
	}()
	select {
	case <-lines:
	case <-time.After(time.Second):
		t.Fatal("scanner did not publish its first line")
	}
	close(stop)
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("scanner remained blocked after consumer cancellation")
	}
}
