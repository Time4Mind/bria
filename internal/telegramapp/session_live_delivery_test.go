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
	"github.com/Time4Mind/bria/internal/telegramui"
	"github.com/Time4Mind/bria/internal/transcript"
)

func TestRunningTranscriptEditDoesNotWaitForPaneCapture(t *testing.T) {
	fixture := newFixture(t)
	actor := application.Principal{UserID: 7}
	preferences, err := fixture.service.Preferences(actor)
	if err != nil {
		t.Fatal(err)
	}
	preferences.TerminalSnapshots = domain.TerminalSnapshotAlways
	if err := fixture.service.SetPreferences(context.Background(), actor, preferences); err != nil {
		t.Fatal(err)
	}
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
		Kind: transcript.EventToolCall, Head: "Read", Body: "first.go",
	}}}
	fixture.messenger.editNotify = make(chan struct{}, 2)
	handler, err := telegramapp.NewHandlerWithControls(
		fixture.service, fixture.projector, fixture.codec, fixture.messenger, controls,
	)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	token, err := fixture.codec.Session(7, telegramui.ActionSelectSession, ref)
	if err != nil {
		t.Fatal(err)
	}
	data, err := (telegramui.Callback{
		Action: telegramui.ActionSelectSession, Token: token,
	}).Encode()
	if err != nil {
		t.Fatal(err)
	}
	if err := handler.HandleTelegramUpdate(ctx, telegrambot.IncomingUpdate{
		UpdateID: 501, Kind: telegrambot.IncomingCallback,
		ChatID: 7, UserID: 7, CallbackID: "select-running", CallbackData: data,
		CallbackOrigin: telegrambot.Message{
			ChatID: 7, MessageID: 9, Rich: true,
			RichMediaFileID: "telegram-pane", PaneHash: "shown-pane",
		},
	}); err != nil {
		t.Fatal(err)
	}
	// Ignore the explicit selection edit; the assertion targets the worker's
	// later delivery of a newly appended tool event.
	select {
	case <-fixture.messenger.editNotify:
	default:
	}
	controls.appendTranscriptEvent(transcript.Event{
		Kind: transcript.EventToolCall, Head: "Bash", Body: "go test ./...",
	})
	waitTestNotification(t, fixture.messenger.editNotify, "tool call card was not refreshed")
	cancel()
	fixture.messenger.mu.Lock()
	latest := fixture.messenger.edited[len(fixture.messenger.edited)-1]
	fixture.messenger.mu.Unlock()
	controls.mu.RLock()
	paneCalls := controls.paneCalls
	controls.mu.RUnlock()
	if paneCalls != 0 {
		t.Fatalf("tool delivery waited for %d pane captures", paneCalls)
	}
	if latest.Pane == nil || latest.Pane.PNG != nil ||
		latest.Pane.FileID != "telegram-pane" || !strings.Contains(latest.Text, "Bash") {
		t.Fatalf("tool call edit=%#v", latest)
	}
}

func TestSessionSelectionDefersChangingPaneCapture(t *testing.T) {
	fixture := newFixture(t)
	actor := application.Principal{UserID: 7}
	preferences, err := fixture.service.Preferences(actor)
	if err != nil {
		t.Fatal(err)
	}
	preferences.TerminalSnapshots = domain.TerminalSnapshotAlways
	if err := fixture.service.SetPreferences(context.Background(), actor, preferences); err != nil {
		t.Fatal(err)
	}
	ref := domain.SessionRef{NodeID: "allowed", SessionID: "live"}
	controls := &blockingControls{
		ref: ref, pane: []byte("changing terminal"),
		events: []transcript.Event{{Kind: transcript.EventAssistantText, Text: "current"}},
	}
	handler, err := telegramapp.NewHandlerWithControls(
		fixture.service, fixture.projector, fixture.codec, fixture.messenger, controls,
	)
	if err != nil {
		t.Fatal(err)
	}
	token, err := fixture.codec.Session(7, telegramui.ActionSelectSession, ref)
	if err != nil {
		t.Fatal(err)
	}
	data, err := (telegramui.Callback{
		Action: telegramui.ActionSelectSession, Token: token,
	}).Encode()
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	started := time.Now()
	if err := handler.HandleTelegramUpdate(ctx, telegrambot.IncomingUpdate{
		UpdateID: 502, Kind: telegrambot.IncomingCallback,
		ChatID: 7, UserID: 7, CallbackID: "select-fast", CallbackData: data,
		CallbackOrigin: telegrambot.Message{ChatID: 7, MessageID: 9, Rich: true},
	}); err != nil {
		t.Fatal(err)
	}
	controls.mu.Lock()
	controls.transcriptCalls = 0
	controls.mu.Unlock()
	if err := handler.HandleTelegramUpdate(ctx, telegrambot.IncomingUpdate{
		UpdateID: 503, Kind: telegrambot.IncomingCallback,
		ChatID: 7, UserID: 7, CallbackID: "select-cached", CallbackData: data,
		CallbackOrigin: telegrambot.Message{ChatID: 7, MessageID: 9, Rich: true},
	}); err != nil {
		t.Fatal(err)
	}
	if time.Since(started) > time.Second {
		t.Fatal("selection callback unexpectedly blocked")
	}
	controls.mu.RLock()
	paneCalls := controls.paneCalls
	transcriptCalls := controls.transcriptCalls
	controls.mu.RUnlock()
	if paneCalls != 0 {
		t.Fatalf("selection performed %d synchronous pane captures", paneCalls)
	}
	if transcriptCalls != 0 {
		t.Fatalf("cached selection performed %d synchronous transcript reads", transcriptCalls)
	}
}
