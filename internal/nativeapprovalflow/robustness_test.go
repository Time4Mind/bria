package nativeapprovalflow

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"bria/internal/nativecontrolport"
	"bria/internal/sessionruntime"
)

type approvalFlowObserver struct{ events []Event }

func (o *approvalFlowObserver) ObserveNativeApproval(_ context.Context, event Event) {
	o.events = append(o.events, event)
}

func TestUnknownApprovalIsNotReplayedAfterDialogResize(t *testing.T) {
	f := &Flow{}
	d := &flowDriver{result: errors.New("receipt unknown")}
	full := flowFixture(t, "codex-command-approval.txt")
	s := flowScreenProvider{snapshot: sessionruntime.NativeSnapshot{FullText: collapseApproval(t, full), Hash: strings.Repeat("a", 64), ProviderSessionID: "same", Generation: 1, Interactive: true}}
	_ = f.Observe(context.Background(), "s", s, d, enabledAlways)
	s.snapshot.FullText = full
	s.snapshot.Hash = strings.Repeat("b", 64)
	_ = f.Observe(context.Background(), "s", s, d, enabledAlways)
	if d.count() != 1 {
		t.Fatal("resize replayed unknown active decision")
	}
}

func TestStaleApprovalRetriesOnlyAfterTransportHashChanges(t *testing.T) {
	f := &Flow{}
	d := &flowDriver{results: []error{nativecontrolport.ErrStale, nil}}
	fixture := flowFixture(t, "codex-command-approval.txt")
	s := flowScreenProvider{snapshot: sessionruntime.NativeSnapshot{FullText: fixture, Hash: strings.Repeat("a", 64), ProviderSessionID: "same", Generation: 1, Interactive: true}}
	if err := f.Observe(context.Background(), "s", s, d, enabledAlways); !errors.Is(err, nativecontrolport.ErrStale) {
		t.Fatalf("first observation error=%v, want stale", err)
	}
	if err := f.Observe(context.Background(), "s", s, d, enabledAlways); err != nil {
		t.Fatal(err)
	}
	if d.count() != 1 {
		t.Fatal("unchanged stale snapshot was retried")
	}
	s.snapshot.Hash = strings.Repeat("b", 64)
	if err := f.Observe(context.Background(), "s", s, d, enabledAlways); err != nil {
		t.Fatal(err)
	}
	if d.count() != 2 {
		t.Fatal("fresh snapshot did not retry stale approval exactly once")
	}
}

func TestUnknownApprovalBarrierSurvivesNoninteractiveScreenUntilNewGeneration(t *testing.T) {
	f := &Flow{}
	d := &flowDriver{results: []error{errors.New("receipt unknown"), nil}}
	first := flowFixture(t, "codex-command-approval-collapsed.txt")
	second := first
	s := flowScreenProvider{snapshot: sessionruntime.NativeSnapshot{FullText: first, Hash: strings.Repeat("a", 64), ProviderSessionID: "same", Generation: 1, Interactive: true}}
	_ = f.Observe(context.Background(), "s", s, d, enabledAlways)
	s.snapshot = sessionruntime.NativeSnapshot{FullText: "ordinary output", Hash: strings.Repeat("b", 64), ProviderSessionID: "same", Generation: 1}
	_ = f.Observe(context.Background(), "s", s, d, enabledAlways)
	s.snapshot = sessionruntime.NativeSnapshot{FullText: second, Hash: strings.Repeat("c", 64), ProviderSessionID: "same", Generation: 1, Interactive: true}
	_ = f.Observe(context.Background(), "s", s, d, enabledAlways)
	if d.count() != 1 {
		t.Fatal("unknown outcome barrier was cleared inside the same generation")
	}
	s.snapshot.Generation = 2
	if err := f.Observe(context.Background(), "s", s, d, enabledAlways); err != nil {
		t.Fatal(err)
	}
	if d.count() != 2 {
		t.Fatal("new generation did not clear unknown outcome barrier")
	}
}

func TestUnknownApprovalDoesNotBlockDistinctQueuedRequestInSameGeneration(t *testing.T) {
	observer := &approvalFlowObserver{}
	f := &Flow{Observer: observer}
	d := &flowDriver{results: []error{sessionruntime.ErrNativeUnavailable, nil}}
	first := flowFixture(t, "codex-command-approval.txt")
	second := flowFixture(t, "codex-command-approval-wiki.txt")
	s := flowScreenProvider{snapshot: sessionruntime.NativeSnapshot{
		FullText: first + "\n" + second, Hash: strings.Repeat("a", 64),
		ProviderSessionID: "same", Generation: 4, Interactive: true,
	}}
	if err := f.Observe(context.Background(), "s", s, d, enabledAlways); !errors.Is(err, sessionruntime.ErrNativeUnavailable) {
		t.Fatalf("first approval error=%v, want unavailable", err)
	}
	if err := f.Observe(context.Background(), "s", s, d, enabledAlways); err != nil {
		t.Fatal(err)
	}
	if d.count() != 1 {
		t.Fatalf("same uncertain approval replayed: calls=%d", d.count())
	}

	// Codex can leave the other parallel request pending without changing the
	// provider generation. It is a separate decision and must not inherit the
	// first request's uncertain one-shot barrier.
	s.snapshot.FullText = first
	s.snapshot.Hash = strings.Repeat("b", 64)
	if err := f.Observe(context.Background(), "s", s, d, enabledAlways); err != nil {
		t.Fatal(err)
	}
	if d.count() != 2 {
		t.Fatalf("distinct queued approval blocked: calls=%d, want 2", d.count())
	}
	if len(observer.events) != 2 ||
		observer.events[0].Outcome != Uncertain || observer.events[1].Outcome != Confirmed {
		t.Fatalf("approval lifecycle events=%+v", observer.events)
	}
	for _, event := range observer.events {
		if event.SessionID != "s" || event.ProviderSessionID != "same" || event.Generation != 4 || len(event.Fingerprint) != 64 {
			t.Fatalf("approval event lost safe identity: %+v", event)
		}
	}
	s.snapshot.FullText, s.snapshot.Hash = first+"\n"+second, strings.Repeat("c", 64)
	if err := f.Observe(context.Background(), "s", s, d, enabledAlways); err != nil {
		t.Fatal(err)
	}
	if d.count() != 2 {
		t.Fatalf("old uncertain approval replayed after distinct success: calls=%d", d.count())
	}
}

func TestUnknownApprovalDoesNotBlockDifferentCompleteCommandWithSameReason(t *testing.T) {
	f := &Flow{}
	d := &flowDriver{results: []error{sessionruntime.ErrNativeUnavailable, nil}}
	first := flowFixture(t, "codex-command-approval.txt")
	command := strings.Index(first, "$ ")
	if command < 0 {
		t.Fatal("fixture command missing")
	}
	second := first[:command] + strings.ReplaceAll(first[command:], "EXAMPLEPRJX-1001", "EXAMPLEPRJX-1002")
	s := flowScreenProvider{snapshot: sessionruntime.NativeSnapshot{FullText: first, Hash: strings.Repeat("a", 64), ProviderSessionID: "same", Generation: 4, Interactive: true}}
	_ = f.Observe(context.Background(), "s", s, d, enabledAlways)
	s.snapshot.FullText, s.snapshot.Hash = second, strings.Repeat("b", 64)
	if err := f.Observe(context.Background(), "s", s, d, enabledAlways); err != nil {
		t.Fatal(err)
	}
	if d.count() != 2 {
		t.Fatalf("different complete command with same reason blocked: calls=%d", d.count())
	}
}

func TestUnknownCollapsedApprovalDoesNotBlockDifferentCollapsedCommand(t *testing.T) {
	f := &Flow{}
	d := &flowDriver{results: []error{sessionruntime.ErrNativeUnavailable, nil}}
	first := collapseApproval(t, flowFixture(t, "codex-command-approval.txt"))
	option := strings.Index(first, "commands that start with `")
	if option < 0 {
		t.Fatal("fixture persistent option missing")
	}
	second := first[:option] + strings.Replace(first[option:], "EXAMPLEPRJX-1001", "EXAMPLEPRJX-1002", 1)
	s := flowScreenProvider{snapshot: sessionruntime.NativeSnapshot{FullText: first, Hash: strings.Repeat("a", 64), ProviderSessionID: "same", Generation: 4, Interactive: true}}
	_ = f.Observe(context.Background(), "s", s, d, enabledAlways)
	s.snapshot.FullText, s.snapshot.Hash = second, strings.Repeat("b", 64)
	if err := f.Observe(context.Background(), "s", s, d, enabledAlways); err != nil {
		t.Fatal(err)
	}
	if d.count() != 2 {
		t.Fatalf("different collapsed command blocked: calls=%d", d.count())
	}
}

func collapseApproval(t *testing.T, screen string) string {
	t.Helper()
	heading := strings.Index(screen, "Would you like to run the following command?")
	options := strings.Index(screen, "› 1. Yes, proceed (y)")
	if heading < 0 || options < 0 || options <= heading {
		t.Fatal("approval fixture anchors missing")
	}
	return screen[heading:heading+len("Would you like to run the following command?")] + "\n\n  [… 12 lines] ctrl + a view all\n\n" + screen[options:]
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
