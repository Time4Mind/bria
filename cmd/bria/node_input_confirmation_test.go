package main

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/Time4Mind/bria/internal/domain"
	"github.com/Time4Mind/bria/internal/promptidentity"
	"github.com/Time4Mind/bria/internal/providerbinding"
	"github.com/Time4Mind/bria/internal/runtimehost"
	"github.com/Time4Mind/bria/internal/transcript"
)

type inputBindingLookupStub struct {
	record providerbinding.Record
	found  bool
	err    error
}

func (s *inputBindingLookupStub) LookupRef(domain.SessionRef) (providerbinding.Record, bool, error) {
	return s.record, s.found, s.err
}

type inputTranscriptReaderStub struct {
	mu     sync.Mutex
	events []transcript.Event
	err    error
}

func (s *inputTranscriptReaderStub) Read(
	context.Context,
	transcript.Request,
) ([]transcript.Event, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]transcript.Event(nil), s.events...), s.err
}

func (s *inputTranscriptReaderStub) set(events []transcript.Event, err error) {
	s.mu.Lock()
	s.events, s.err = append([]transcript.Event(nil), events...), err
	s.mu.Unlock()
}

func TestProviderTranscriptInputConfirmationRequiresNewMatchingUserEvent(t *testing.T) {
	base := time.Date(2026, 8, 22, 12, 0, 0, 0, time.UTC)
	old := transcript.Event{Kind: transcript.EventUserText, Text: "same prompt", Timestamp: base.Format(time.RFC3339Nano)}
	reader := &inputTranscriptReaderStub{events: []transcript.Event{old}}
	lookup := &inputBindingLookupStub{found: true, record: providerbinding.Record{
		NodeID: "node", SessionID: "session", ProviderSessionID: "provider-session",
		RuntimeGeneration: 4, Workdir: "/work",
	}}
	confirmer, err := newProviderTranscriptInputConfirmer(lookup, reader)
	if err != nil {
		t.Fatal(err)
	}
	confirmer.now = func() time.Time { return base.Add(time.Second) }
	binding := runtimehost.RuntimeBinding{
		NodeID: "node", SessionID: "session", Generation: 4, Backend: "codex", Workdir: "/work",
	}
	baseline, err := confirmer.BaselineInput(context.Background(), binding)
	if err != nil || len(baseline.UserTail) != 1 {
		t.Fatalf("baseline=%+v err=%v", baseline, err)
	}
	reader.set([]transcript.Event{
		old,
		{Kind: transcript.EventAssistantText, Text: "old answer", Timestamp: base.Add(time.Second).Format(time.RFC3339Nano)},
		{Kind: transcript.EventUserText, Text: "different", Timestamp: base.Add(2 * time.Second).Format(time.RFC3339Nano)},
		{Kind: transcript.EventUserText, Text: "same prompt", Timestamp: base.Add(3 * time.Second).Format(time.RFC3339Nano)},
	}, nil)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := confirmer.ConfirmInput(ctx, binding, baseline, promptidentity.Digest("same prompt")); err != nil {
		t.Fatal(err)
	}
}

func TestProviderTranscriptInputConfirmationRejectsOldUnrelatedAndStale(t *testing.T) {
	base := time.Date(2026, 8, 22, 12, 0, 0, 0, time.UTC)
	reader := &inputTranscriptReaderStub{events: []transcript.Event{{
		Kind: transcript.EventUserText, Text: "old", Timestamp: base.Format(time.RFC3339Nano),
	}}}
	lookup := &inputBindingLookupStub{found: true, record: providerbinding.Record{
		NodeID: "node", SessionID: "session", ProviderSessionID: "provider-session",
		RuntimeGeneration: 4, Workdir: "/work",
	}}
	confirmer, err := newProviderTranscriptInputConfirmer(lookup, reader)
	if err != nil {
		t.Fatal(err)
	}
	confirmer.now = func() time.Time { return base.Add(time.Second) }
	binding := runtimehost.RuntimeBinding{NodeID: "node", SessionID: "session", Generation: 4, Backend: "claude"}
	baseline, err := confirmer.BaselineInput(context.Background(), binding)
	if err != nil {
		t.Fatal(err)
	}
	reader.set([]transcript.Event{{
		Kind: transcript.EventUserText, Text: "unrelated", Timestamp: base.Add(2 * time.Second).Format(time.RFC3339Nano),
	}}, nil)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	err = confirmer.ConfirmInput(ctx, binding, baseline, promptidentity.Digest("expected"))
	cancel()
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("unrelated confirmation err=%v", err)
	}
	lookup.record.RuntimeGeneration = 5
	if _, err := confirmer.BaselineInput(context.Background(), binding); !errors.Is(err, runtimehost.ErrStaleRuntime) {
		t.Fatalf("stale baseline err=%v", err)
	}
}

func TestProviderTranscriptInputConfirmationAllowsMissingFirstTranscript(t *testing.T) {
	base := time.Date(2026, 8, 22, 12, 0, 0, 0, time.UTC)
	reader := &inputTranscriptReaderStub{err: transcript.ErrTranscriptNotFound}
	lookup := &inputBindingLookupStub{found: true, record: providerbinding.Record{
		NodeID: "node", SessionID: "session", ProviderSessionID: "provider-session",
		RuntimeGeneration: 4, Workdir: "/work",
	}}
	confirmer, err := newProviderTranscriptInputConfirmer(lookup, reader)
	if err != nil {
		t.Fatal(err)
	}
	confirmer.now = func() time.Time { return base }
	binding := runtimehost.RuntimeBinding{NodeID: "node", SessionID: "session", Generation: 4, Backend: "claude"}
	baseline, err := confirmer.BaselineInput(context.Background(), binding)
	if err != nil || baseline.ProviderSessionID == "" || len(baseline.UserTail) != 0 {
		t.Fatalf("baseline=%+v err=%v", baseline, err)
	}
	reader.set([]transcript.Event{{
		Kind: transcript.EventUserText, Text: "first prompt", Timestamp: base.Add(time.Second).Format(time.RFC3339Nano),
	}}, nil)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := confirmer.ConfirmInput(ctx, binding, baseline, promptidentity.Digest("first prompt")); err != nil {
		t.Fatal(err)
	}
}

func TestProviderTranscriptInputConfirmationWaitsForFirstBinding(t *testing.T) {
	base := time.Date(2026, 8, 22, 12, 0, 0, 0, time.UTC)
	reader := &inputTranscriptReaderStub{}
	lookup := &inputBindingLookupStub{}
	confirmer, err := newProviderTranscriptInputConfirmer(lookup, reader)
	if err != nil {
		t.Fatal(err)
	}
	confirmer.now = func() time.Time { return base }
	binding := runtimehost.RuntimeBinding{NodeID: "node", SessionID: "session", Generation: 4, Backend: "claude"}
	baseline, err := confirmer.BaselineInput(context.Background(), binding)
	if err != nil || baseline.ProviderSessionID != "" || baseline.CapturedAt != base {
		t.Fatalf("baseline=%+v err=%v", baseline, err)
	}
	lookup.found = true
	lookup.record = providerbinding.Record{
		NodeID: "node", SessionID: "session", ProviderSessionID: "first-provider",
		RuntimeGeneration: 4, Workdir: "/work",
	}
	reader.set([]transcript.Event{{
		Kind: transcript.EventUserText, Text: "first prompt", Timestamp: base.Add(time.Second).Format(time.RFC3339Nano),
	}}, nil)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := confirmer.ConfirmInput(ctx, binding, baseline, promptidentity.Digest("first prompt")); err != nil {
		t.Fatal(err)
	}
}

func TestTranscriptPromptMatcherFailsClosedOnDuplicateAndFirstPromptHistory(t *testing.T) {
	base := time.Date(2026, 8, 22, 12, 0, 0, 0, time.UTC)
	old := transcript.Event{
		Kind: transcript.EventUserText, Text: "same", Timestamp: base.Format(time.RFC3339Nano),
	}
	baseline := runtimehost.InputConfirmationBaseline{
		UserTail:   []string{inputUserFingerprint(old)},
		CapturedAt: base.Add(time.Second),
	}
	if transcriptContainsNewPrompt([]transcript.Event{old, old}, baseline, promptidentity.Digest("same")) {
		t.Fatal("duplicated old event confirmed a new prompt")
	}
	firstBaseline := runtimehost.InputConfirmationBaseline{CapturedAt: base.Add(time.Second)}
	tooOld := transcript.Event{
		Kind: transcript.EventUserText, Text: "first", Timestamp: base.Add(500 * time.Millisecond).Format(time.RFC3339Nano),
	}
	if transcriptContainsNewPrompt([]transcript.Event{tooOld}, firstBaseline, promptidentity.Digest("first")) {
		t.Fatal("pre-baseline first prompt history was accepted")
	}
	equalTime := tooOld
	equalTime.Timestamp = firstBaseline.CapturedAt.Format(time.RFC3339Nano)
	if transcriptContainsNewPrompt([]transcript.Event{equalTime}, firstBaseline, promptidentity.Digest("first")) {
		t.Fatal("first prompt at the baseline boundary was accepted")
	}
	newAfterTruncation := transcript.Event{
		Kind: transcript.EventUserText, Text: "same", Timestamp: base.Add(2 * time.Second).Format(time.RFC3339Nano),
	}
	if transcriptContainsNewPrompt([]transcript.Event{newAfterTruncation}, baseline, promptidentity.Digest("same")) {
		t.Fatal("matching event without the append boundary was accepted")
	}
}

func TestProviderTranscriptInputConfirmationAcceptsLegacyGenerationZero(t *testing.T) {
	base := time.Date(2026, 8, 22, 12, 0, 0, 0, time.UTC)
	reader := &inputTranscriptReaderStub{events: []transcript.Event{{
		Kind: transcript.EventUserText, Text: "old", Timestamp: base.Format(time.RFC3339Nano),
	}}}
	lookup := &inputBindingLookupStub{found: true, record: providerbinding.Record{
		NodeID: "node", SessionID: "session", ProviderSessionID: "legacy-provider",
		RuntimeGeneration: 0, Workdir: "/work",
	}}
	confirmer, err := newProviderTranscriptInputConfirmer(lookup, reader)
	if err != nil {
		t.Fatal(err)
	}
	confirmer.now = func() time.Time { return base.Add(time.Second) }
	binding := runtimehost.RuntimeBinding{NodeID: "node", SessionID: "session", Generation: 4, Backend: "codex"}
	baseline, err := confirmer.BaselineInput(context.Background(), binding)
	if err != nil || !baseline.LegacyGeneration {
		t.Fatalf("baseline=%+v err=%v", baseline, err)
	}
	reader.set([]transcript.Event{
		{Kind: transcript.EventUserText, Text: "old", Timestamp: base.Format(time.RFC3339Nano)},
		{Kind: transcript.EventUserText, Text: "new", Timestamp: base.Add(2 * time.Second).Format(time.RFC3339Nano)},
	}, nil)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := confirmer.ConfirmInput(ctx, binding, baseline, promptidentity.Digest("new")); err != nil {
		t.Fatal(err)
	}
}
