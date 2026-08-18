package telegramapp_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/Time4Mind/bria/internal/application"
	"github.com/Time4Mind/bria/internal/clusterstate"
	"github.com/Time4Mind/bria/internal/domain"
	"github.com/Time4Mind/bria/internal/telegramapp"
	"github.com/Time4Mind/bria/internal/telegrambot"
	"github.com/Time4Mind/bria/internal/telegramui"
	"github.com/Time4Mind/bria/internal/transcript"
)

func TestStopAnswersTelegramBeforeNodeAcknowledgement(t *testing.T) {
	fixture := newFixture(t)
	ref := domain.SessionRef{NodeID: "allowed", SessionID: "live"}
	controls := &blockingControls{
		started: make(chan struct{}), release: make(chan struct{}), ref: ref,
	}
	handler, err := telegramapp.NewHandlerWithControls(
		fixture.service, fixture.projector, fixture.codec, fixture.messenger, controls,
	)
	if err != nil {
		t.Fatal(err)
	}
	token, err := fixture.codec.Session(7, telegramui.ActionStop, ref)
	if err != nil {
		t.Fatal(err)
	}
	data, err := (telegramui.Callback{Action: telegramui.ActionStop, Token: token}).Encode()
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() {
		done <- handler.HandleTelegramUpdate(ctx, telegrambot.IncomingUpdate{
			UpdateID: 70, Kind: telegrambot.IncomingCallback, ChatID: 7, UserID: 7,
			CallbackID: "stop", CallbackData: data,
			CallbackOrigin: telegrambot.Message{ChatID: 7, MessageID: 10},
		})
	}()
	select {
	case <-controls.started:
	case <-time.After(time.Second):
		t.Fatal("node control was not invoked")
	}
	if len(fixture.messenger.answers) != 1 || fixture.messenger.answers[0] != "stop:" {
		t.Fatalf("callback spinner was not answered first: %#v", fixture.messenger.answers)
	}
	close(controls.release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func TestCloseReturnsToMostRecentLiveSessionWithoutServerList(t *testing.T) {
	fixture := newFixture(t)
	actor := application.Principal{UserID: 7}
	second := domain.Session{
		ID: "second", NodeID: "allowed", OwnerID: 7, Name: "Second", Backend: "codex",
		State: domain.SessionActive, RuntimePhase: domain.RuntimeIdle,
		CreatedAt: time.Unix(200, 0).UTC(), LiveSinceAt: time.Unix(200, 0).UTC(),
	}
	if result := fixture.machine.Apply(commandForTest(t, "add-second", clusterstate.CommandAddSession, second)); result.Err() != nil {
		t.Fatal(result.Err())
	}
	if err := fixture.service.SelectSession(context.Background(), actor, second.Ref()); err != nil {
		t.Fatal(err)
	}
	controls := &closingControls{
		blockingControls: &blockingControls{ref: second.Ref()}, service: fixture.service,
	}
	handler, err := telegramapp.NewHandlerWithControls(
		fixture.service, fixture.projector, fixture.codec, fixture.messenger, controls,
	)
	if err != nil {
		t.Fatal(err)
	}
	token, err := fixture.codec.Session(7, telegramui.ActionConfirmClose, second.Ref())
	if err != nil {
		t.Fatal(err)
	}
	data, err := (telegramui.Callback{Action: telegramui.ActionConfirmClose, Token: token}).Encode()
	if err != nil {
		t.Fatal(err)
	}
	if err := handler.HandleTelegramUpdate(context.Background(), telegrambot.IncomingUpdate{
		UpdateID: 71, Kind: telegrambot.IncomingCallback, ChatID: 7, UserID: 7,
		CallbackID: "close", CallbackData: data,
		CallbackOrigin: telegrambot.Message{ChatID: 7, MessageID: 10},
	}); err != nil {
		t.Fatal(err)
	}
	if len(fixture.messenger.edited) != 1 {
		t.Fatalf("edits=%#v", fixture.messenger.edited)
	}
	got := fixture.messenger.edited[0]
	if got.Name != telegramui.ScreenSessionCard || !strings.Contains(got.Text, "Live") {
		t.Fatalf("close fallback=%#v", got)
	}
}

func TestHiddenTechnicalEventsLeaveOnlyAssistantNarrative(t *testing.T) {
	fixture := newFixture(t)
	preferences, err := fixture.service.Preferences(application.Principal{UserID: 7})
	if err != nil {
		t.Fatal(err)
	}
	for _, eventType := range []domain.CardEventType{
		domain.CardEventToolCall, domain.CardEventToolResult, domain.CardEventThinking,
	} {
		if err := preferences.SetCardEventVisibility(eventType, false); err != nil {
			t.Fatal(err)
		}
	}
	if err := fixture.service.SetPreferences(
		application.WithOperationScope(context.Background(), "hide-technical"),
		application.Principal{UserID: 7}, preferences,
	); err != nil {
		t.Fatal(err)
	}
	ref := domain.SessionRef{NodeID: "allowed", SessionID: "live"}
	controls := &blockingControls{events: []transcript.Event{
		{Kind: transcript.EventThinking, Body: "private chain"},
		{Kind: transcript.EventToolCall, ToolUseID: "tool", Head: "Bash", Body: "secret command"},
		{Kind: transcript.EventToolResult, ToolUseID: "tool", Body: "secret output"},
		{Kind: transcript.EventAssistantFinal, Text: "Visible answer"},
	}}
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
	data, err := (telegramui.Callback{Action: telegramui.ActionSelectSession, Token: token}).Encode()
	if err != nil {
		t.Fatal(err)
	}
	if err := handler.HandleTelegramUpdate(context.Background(), telegrambot.IncomingUpdate{
		UpdateID: 80, Kind: telegrambot.IncomingCallback, ChatID: 7, UserID: 7,
		CallbackID: "select", CallbackData: data,
		CallbackOrigin: telegrambot.Message{ChatID: 7, MessageID: 10},
	}); err != nil {
		t.Fatal(err)
	}
	if len(fixture.messenger.sent) != 1 {
		t.Fatalf("sent=%#v", fixture.messenger.sent)
	}
	screen := fixture.messenger.sent[0]
	if !strings.Contains(screen.Text, "Visible answer") {
		t.Fatalf("assistant answer missing: %q", screen.Text)
	}
	for _, hidden := range []string{"private chain", "Bash", "secret command", "secret output"} {
		if strings.Contains(screen.Text, hidden) {
			t.Fatalf("hidden technical content %q leaked in %q", hidden, screen.Text)
		}
	}
	if screen.Pane != nil {
		t.Fatal("terminal pane remained visible with hidden technical content")
	}
}
