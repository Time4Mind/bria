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

func TestReplaceResponseCardsDeletesThePreviousReplicatedCard(t *testing.T) {
	fixture := newFixture(t)
	actor := application.Principal{UserID: 7}
	preferences, err := fixture.service.Preferences(actor)
	if err != nil {
		t.Fatal(err)
	}
	preferences.Language = domain.LanguageEnglish
	preferences.ResponseCards = domain.ResponseCardsReplace
	if err := fixture.service.SetPreferences(context.Background(), actor, preferences); err != nil {
		t.Fatal(err)
	}
	ref := domain.SessionRef{NodeID: "allowed", SessionID: "live"}
	controls := &blockingControls{ref: ref}
	handler, err := telegramapp.NewHandlerWithControls(
		fixture.service, fixture.projector, fixture.codec, fixture.messenger, controls,
	)
	if err != nil {
		t.Fatal(err)
	}
	for updateID := int64(1); updateID <= 2; updateID++ {
		ctx, cancel := context.WithCancel(context.Background())
		err = handler.HandleTelegramUpdate(ctx, telegrambot.IncomingUpdate{
			UpdateID: updateID, Kind: telegrambot.IncomingMessage,
			ChatID: 7, UserID: 7, Text: "prompt",
		})
		cancel()
		if err != nil {
			t.Fatal(err)
		}
	}
	if len(fixture.messenger.deleted) != 1 || fixture.messenger.deleted[0].MessageID != 1 {
		t.Fatalf("deleted=%#v", fixture.messenger.deleted)
	}
	card, ok, err := fixture.service.TelegramResponseCard(actor)
	if err != nil || !ok || card.MessageID != 2 {
		t.Fatalf("replicated latest card=%#v/%v/%v", card, ok, err)
	}
}

func TestPaneRefreshRendersFinalAnswerWhenRuntimeAlreadySettledIdle(t *testing.T) {
	fixture := newFixture(t)
	fixture.messenger.sendNotify = make(chan struct{}, 2)
	fixture.messenger.deleteNotify = make(chan struct{}, 1)
	ref := domain.SessionRef{NodeID: "allowed", SessionID: "live"}
	actor := application.Principal{UserID: 7}
	session, err := fixture.service.Session(actor, ref)
	if err != nil {
		t.Fatal(err)
	}
	if err := fixture.service.PublishSessionRuntime(
		context.Background(), session, domain.RuntimeRunning, nil,
	); err != nil {
		t.Fatal(err)
	}
	controls := &blockingControls{ref: ref, afterSend: []transcript.Event{
		{Kind: transcript.EventToolResult, Head: "Bash", Body: "tool completed"},
		{Kind: transcript.EventAssistantFinal,
			Text:      "FINAL ANSWER START " + strings.Repeat("middle ", 1000) + "FINAL ANSWER END",
			Timestamp: time.Now().Add(time.Millisecond).Format(time.RFC3339Nano)},
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
		UpdateID: 3, Kind: telegrambot.IncomingMessage,
		ChatID: 7, UserID: 7, Text: "prompt",
	}); err != nil {
		t.Fatal(err)
	}
	<-fixture.messenger.sendNotify
	select {
	case <-fixture.messenger.sendNotify:
	case <-time.After(2 * time.Second):
		t.Fatal("settled idle card was not promoted to a rich final card")
	}
	waitTestNotification(t, fixture.messenger.deleteNotify, "old active carrier was not deleted")
	if len(fixture.messenger.sent) < 2 {
		t.Fatal("settled idle card was not promoted to a rich final card")
	}
	latest := fixture.messenger.sent[len(fixture.messenger.sent)-1]
	if !strings.Contains(latest.Text, "FINAL ANSWER START") ||
		strings.Contains(latest.Text, "FINAL ANSWER END") {
		t.Fatalf("final response did not open at its beginning: %q", latest.Text)
	}
	label := latest.Grid[0][1].Label
	parts := strings.SplitN(label, "/", 2)
	if len(parts) != 2 || parts[0] == parts[1] {
		t.Fatalf("final response start page = %s", label)
	}
	if len(fixture.messenger.deleted) == 0 || fixture.messenger.deleted[0].MessageID != 1 {
		t.Fatalf("old active carrier was not deleted: %#v", fixture.messenger.deleted)
	}
}

func TestPaneRefreshSettlesClaudeProviderErrorAsDegraded(t *testing.T) {
	fixture := newFixture(t)
	fixture.messenger.sendNotify = make(chan struct{}, 2)
	fixture.messenger.deleteNotify = make(chan struct{}, 1)
	ref := domain.SessionRef{NodeID: "allowed", SessionID: "live"}
	session, err := fixture.service.Session(application.Principal{UserID: 7}, ref)
	if err != nil {
		t.Fatal(err)
	}
	if err := fixture.service.PublishSessionRuntime(
		context.Background(), session, domain.RuntimeRunning, nil,
	); err != nil {
		t.Fatal(err)
	}
	controls := &blockingControls{ref: ref, events: []transcript.Event{{
		Kind: transcript.EventAssistantFinal, Text: "subscription access is disabled",
		Error: true, Timestamp: time.Now().Add(time.Second).UTC().Format(time.RFC3339Nano),
	}}}
	handler, err := telegramapp.NewHandlerWithControls(
		fixture.service, fixture.projector, fixture.codec, fixture.messenger, controls,
	)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := handler.HandleTelegramUpdate(ctx, telegrambot.IncomingUpdate{
		UpdateID: 4, Kind: telegrambot.IncomingMessage,
		ChatID: 7, UserID: 7, Text: "prompt",
	}); err != nil {
		t.Fatal(err)
	}
	waitTestNotification(t, fixture.messenger.sendNotify, "initial Claude card was not sent")
	waitTestNotification(t, fixture.messenger.sendNotify, "Claude error card was not reposted")
	waitTestNotification(t, fixture.messenger.deleteNotify, "initial Claude carrier was not retired")
	session, err = fixture.service.Session(application.Principal{UserID: 7}, ref)
	if err != nil {
		t.Fatal(err)
	}
	if session.RuntimePhase != domain.RuntimeDegraded || session.LastOperation == nil ||
		session.LastOperation.Status != domain.OperationFailed ||
		session.LastOperation.Detail != "subscription access is disabled" {
		t.Fatalf("provider failure was not settled: %#v", session)
	}
}

func TestFreshClaudeSessionRendersBeforeTranscriptExists(t *testing.T) {
	fixture := newFixture(t)
	fixture.messenger.editNotify = make(chan struct{}, 2)
	actor := application.Principal{UserID: 7}
	ref := domain.SessionRef{NodeID: "allowed", SessionID: "live"}
	session, err := fixture.service.Session(actor, ref)
	if err != nil {
		t.Fatal(err)
	}
	if err := fixture.service.BindProviderSession(
		context.Background(), actor, session, "fresh-claude-provider-id",
	); err != nil {
		t.Fatal(err)
	}
	controls := &transcriptErrorControls{
		blockingControls: &blockingControls{ref: ref},
		err:              transcript.ErrTranscriptNotFound,
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
	if err := handler.HandleTelegramUpdate(context.Background(), telegrambot.IncomingUpdate{
		UpdateID: 40, Kind: telegrambot.IncomingCallback, ChatID: 7, UserID: 7,
		CallbackID: "select-fresh-claude", CallbackData: data,
		CallbackOrigin: telegrambot.Message{ChatID: 7, MessageID: 1},
	}); err != nil {
		t.Fatal(err)
	}
	waitTestNotification(t, fixture.messenger.editNotify, "fresh Claude card was not rendered")
	waitTestNotification(t, fixture.messenger.editNotify, "fresh Claude card refresh did not settle")
	if len(fixture.messenger.edited) != 2 ||
		fixture.messenger.edited[1].Name != telegramui.ScreenSessionCard {
		t.Fatalf("fresh Claude card was not rendered: %#v", fixture.messenger.edited)
	}
}

func TestBackgroundedSessionWorkerCannotRepostOverSelectedSession(t *testing.T) {
	fixture := newFixture(t)
	actor := application.Principal{UserID: 7}
	created := time.Unix(200, 0).UTC()
	command, err := clusterstate.NewCommand(
		"add-second-live-session", clusterstate.CommandAddSession, created,
		domain.Session{
			ID: "second", NodeID: "allowed", OwnerID: 7, Name: "Second", Backend: "codex",
			State: domain.SessionActive, CreatedAt: created, LiveSinceAt: created,
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	if result := fixture.machine.Apply(command); result.Err() != nil {
		t.Fatal(result.Err())
	}
	oldRef := domain.SessionRef{NodeID: "allowed", SessionID: "live"}
	oldSession, err := fixture.service.Session(actor, oldRef)
	if err != nil {
		t.Fatal(err)
	}
	if err := fixture.service.PublishSessionRuntime(
		context.Background(), oldSession, domain.RuntimeRunning, nil,
	); err != nil {
		t.Fatal(err)
	}
	controls := &delayedTranscriptControls{
		blockingControls: &blockingControls{ref: oldRef, events: []transcript.Event{
			{Kind: transcript.EventAssistantFinal, Text: "OLD FINAL"},
		}},
		started: make(chan struct{}), release: make(chan struct{}),
	}
	handler, err := telegramapp.NewHandlerWithControls(
		fixture.service, fixture.projector, fixture.codec, fixture.messenger, controls,
	)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := handler.HandleTelegramUpdate(ctx, telegrambot.IncomingUpdate{
		UpdateID: 41, Kind: telegrambot.IncomingMessage,
		ChatID: 7, UserID: 7, Text: "prompt",
	}); err != nil {
		t.Fatal(err)
	}
	select {
	case <-controls.started:
	case <-time.After(2 * time.Second):
		t.Fatal("old session worker did not reach transcript capture")
	}
	secondRef := domain.SessionRef{NodeID: "allowed", SessionID: "second"}
	token, err := fixture.codec.Session(7, telegramui.ActionSelectSession, secondRef)
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
		UpdateID: 42, Kind: telegrambot.IncomingCallback, ChatID: 7, UserID: 7,
		CallbackID: "select-second", CallbackData: data,
		CallbackOrigin: telegrambot.Message{ChatID: 7, MessageID: 1},
	}); err != nil {
		t.Fatal(err)
	}
	close(controls.release)
	time.Sleep(100 * time.Millisecond)
	active, err := fixture.service.ActiveSession(actor)
	if err != nil || active.Ref() != secondRef {
		t.Fatalf("active=%#v err=%v, want second session", active.Ref(), err)
	}
	if len(fixture.messenger.sent) != 1 {
		t.Fatalf("old worker recreated a card: %#v", fixture.messenger.sent)
	}
	if len(fixture.messenger.deleted) != 0 {
		t.Fatalf("old worker deleted selected carrier: %#v", fixture.messenger.deleted)
	}
}
