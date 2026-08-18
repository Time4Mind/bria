package telegramapp_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/Time4Mind/bria/internal/application"
	"github.com/Time4Mind/bria/internal/domain"
	"github.com/Time4Mind/bria/internal/telegramapp"
	"github.com/Time4Mind/bria/internal/telegrambot"
	"github.com/Time4Mind/bria/internal/transcript"
)

func TestTextInputPreservesHistoryWithoutInventingProviderOrder(t *testing.T) {
	fixture := newFixture(t)
	ref := domain.SessionRef{NodeID: "allowed", SessionID: "live"}
	controls := &blockingControls{ref: ref, events: []transcript.Event{
		{Kind: transcript.EventUserText, Text: "previous prompt",
			Timestamp: time.Now().Add(-time.Minute).Format(time.RFC3339Nano)},
		{Kind: transcript.EventAssistantFinal, Text: "previous answer",
			Timestamp: time.Now().Add(-30 * time.Second).Format(time.RFC3339Nano)},
	}}
	handler, err := telegramapp.NewHandlerWithControls(
		fixture.service, fixture.projector, fixture.codec, fixture.messenger, controls,
	)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := handler.HandleTelegramUpdate(ctx, telegrambot.IncomingUpdate{
		UpdateID: 451, Kind: telegrambot.IncomingMessage, ChatID: 7, UserID: 7,
		Text: "current prompt",
	}); err != nil {
		t.Fatal(err)
	}
	if len(fixture.messenger.sent) != 1 {
		t.Fatalf("sent screens=%#v", fixture.messenger.sent)
	}
	text := fixture.messenger.sent[0].Text
	if !strings.Contains(text, "previous answer") {
		t.Fatalf("initial card lost history:\n%s", text)
	}
	if strings.Contains(text, "current prompt") {
		t.Fatalf("initial card invented a provider event:\n%s", text)
	}
}

func TestTextInputUsesProviderTranscriptOrderAfterFlush(t *testing.T) {
	fixture := newFixture(t)
	actor := application.Principal{UserID: 7}
	ref := domain.SessionRef{NodeID: "allowed", SessionID: "live"}
	session, err := fixture.service.Session(actor, ref)
	if err != nil {
		t.Fatal(err)
	}
	if err := fixture.service.PublishSessionRuntime(
		context.Background(), session, domain.RuntimeRunning, nil,
	); err != nil {
		t.Fatal(err)
	}
	controls := &blockingControls{ref: ref, events: []transcript.Event{{
		Kind: transcript.EventAssistantFinal, Text: "previous answer",
		Timestamp: time.Now().Add(-time.Minute).Format(time.RFC3339Nano),
	}}}
	handler, err := telegramapp.NewHandlerWithControls(
		fixture.service, fixture.projector, fixture.codec, fixture.messenger, controls,
	)
	if err != nil {
		t.Fatal(err)
	}
	fixture.messenger.editNotify = make(chan struct{}, 1)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := handler.HandleTelegramUpdate(ctx, telegrambot.IncomingUpdate{
		UpdateID: 452, Kind: telegrambot.IncomingMessage, ChatID: 7, UserID: 7,
		Text: "current prompt",
	}); err != nil {
		t.Fatal(err)
	}
	waitTestNotification(t, fixture.messenger.editNotify, "lagging transcript was not refreshed")
	edited := fixture.messenger.edited[len(fixture.messenger.edited)-1].Text
	if !strings.Contains(edited, "previous answer") || strings.Contains(edited, "current prompt") {
		t.Fatalf("card did not preserve provider-only history during flush lag:\n%s", edited)
	}
	now := time.Now()
	controls.mu.Lock()
	controls.events = append(controls.events,
		transcript.Event{
			Kind: transcript.EventUserText, Text: "current prompt",
			Timestamp: now.Format(time.RFC3339Nano),
		},
		transcript.Event{
			Kind: transcript.EventToolCall, Head: "ORDERED TOOL", Body: "work",
			Timestamp: now.Add(time.Millisecond).Format(time.RFC3339Nano),
		},
		transcript.Event{
			Kind: transcript.EventAssistantText, Text: "ordered answer",
			Timestamp: now.Add(2 * time.Millisecond).Format(time.RFC3339Nano),
		},
	)
	controls.mu.Unlock()
	waitTestNotification(t, fixture.messenger.editNotify, "flushed transcript was not refreshed")
	edited = fixture.messenger.edited[len(fixture.messenger.edited)-1].Text
	if count := strings.Count(edited, "👤 current prompt"); count != 1 {
		t.Fatalf("provider prompt count after transcript flush = %d:\n%s", count, edited)
	}
	promptAt := strings.Index(edited, "👤 current prompt")
	toolAt := strings.Index(edited, "ORDERED TOOL")
	answerAt := strings.Index(edited, "ordered answer")
	if promptAt < 0 || toolAt < 0 || answerAt < 0 || promptAt > toolAt || toolAt > answerAt {
		t.Fatalf("provider order was not preserved:\n%s", edited)
	}
}

func TestLiveCardReplacesOnceWhenTelegramRateLimitsEdits(t *testing.T) {
	fixture := newFixture(t)
	actor := application.Principal{UserID: 7}
	ref := domain.SessionRef{NodeID: "allowed", SessionID: "live"}
	session, err := fixture.service.Session(actor, ref)
	if err != nil {
		t.Fatal(err)
	}
	if err := fixture.service.PublishSessionRuntime(
		context.Background(), session, domain.RuntimeRunning, nil,
	); err != nil {
		t.Fatal(err)
	}
	controls := &blockingControls{ref: ref, events: []transcript.Event{{
		Kind: transcript.EventAssistantText, Text: "working",
		Timestamp: time.Now().Format(time.RFC3339Nano),
	}}}
	handler, err := telegramapp.NewHandlerWithControls(
		fixture.service, fixture.projector, fixture.codec, fixture.messenger, controls,
	)
	if err != nil {
		t.Fatal(err)
	}
	fixture.messenger.editErr = &telegrambot.APIError{
		Method: "editMessageText", Code: 429, RetryAfter: time.Minute,
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := handler.HandleTelegramUpdate(ctx, telegrambot.IncomingUpdate{
		UpdateID: 453, Kind: telegrambot.IncomingMessage, ChatID: 7, UserID: 7,
		Text: "trigger flood recovery",
	}); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(2 * time.Second)
	for {
		sent, _, _ := fixture.messenger.screensSnapshot()
		if len(sent) >= 2 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("live card was not replaced after edit flood wait: sent=%d", len(sent))
		}
		time.Sleep(10 * time.Millisecond)
	}
	time.Sleep(1400 * time.Millisecond)
	sent, _, _ := fixture.messenger.screensSnapshot()
	if len(sent) != 2 {
		t.Fatalf("live card replacements=%d want=2 total sends", len(sent))
	}
}
