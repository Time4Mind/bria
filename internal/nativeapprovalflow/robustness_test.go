package nativeapprovalflow

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"bria/internal/sessionruntime"
)

func TestUnknownApprovalIsNotReplayedAfterDialogResize(t *testing.T) {
	f := &Flow{}
	d := &flowDriver{result: errors.New("receipt unknown")}
	s := flowScreenProvider{snapshot: sessionruntime.NativeSnapshot{FullText: flowFixture(t, "codex-command-approval-collapsed.txt"), Hash: strings.Repeat("a", 64), ProviderSessionID: "same", Generation: 1, Interactive: true}}
	_ = f.Observe(context.Background(), "s", s, d, enabledAlways)
	s.snapshot.FullText = flowFixture(t, "codex-command-approval.txt")
	s.snapshot.Hash = strings.Repeat("b", 64)
	_ = f.Observe(context.Background(), "s", s, d, enabledAlways)
	if d.count() != 1 {
		t.Fatal("resize replayed unknown active decision")
	}
}

func TestCancelledToggleDoesNotWaitForProvider(t *testing.T) {
	f := &Flow{}
	d := &flowDriver{entered: make(chan struct{}), release: make(chan struct{})}
	s := flowScreenProvider{snapshot: sessionruntime.NativeSnapshot{FullText: flowFixture(t, "codex-command-approval.txt"), Hash: strings.Repeat("a", 64), ProviderSessionID: "same", Generation: 1, Interactive: true}}
	done := make(chan error, 1)
	go func() { done <- f.Observe(context.Background(), "s", s, d, enabledAlways) }()
	<-d.entered
	defer func() { close(d.release); <-done }()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	toggled := make(chan error, 1)
	go func() { toggled <- f.Toggle(ctx, func(context.Context) error { return errors.New("must not mutate") }) }()
	select {
	case err := <-toggled:
		if !errors.Is(err, context.Canceled) {
			t.Fatal("cancelled toggle mutated setting", err)
		}
	case <-time.After(100 * time.Millisecond):
		t.Fatal("cancelled toggle blocked behind provider")
	}
}
