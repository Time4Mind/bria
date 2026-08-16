package telegramapp_test

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Time4Mind/bria/internal/application"
	"github.com/Time4Mind/bria/internal/clusterstate"
	"github.com/Time4Mind/bria/internal/domain"
	"github.com/Time4Mind/bria/internal/nodecontrol"
	"github.com/Time4Mind/bria/internal/runtimehost"
	"github.com/Time4Mind/bria/internal/sessioncontrol"
	"github.com/Time4Mind/bria/internal/telegramapp"
	"github.com/Time4Mind/bria/internal/telegrambot"
	"github.com/Time4Mind/bria/internal/telegramui"
	"github.com/Time4Mind/bria/internal/transcript"
)

type blockingControls struct {
	mu        sync.RWMutex
	started   chan struct{}
	release   chan struct{}
	ref       domain.SessionRef
	events    []transcript.Event
	afterSend []transcript.Event
	external  *runtimehost.InputPayload
	pane      []byte
	key       runtimehost.InteractiveKey
	keyHash   string
	text      string
}

type closingControls struct {
	*blockingControls
	service *application.Service
}

type delayedTranscriptControls struct {
	*blockingControls
	started chan struct{}
	release chan struct{}
	once    sync.Once
}

type transcriptErrorControls struct {
	*blockingControls
	err error
}

func (c *transcriptErrorControls) Transcript(
	context.Context,
	application.Principal,
	domain.SessionRef,
) ([]transcript.Event, error) {
	return nil, c.err
}

func (c *delayedTranscriptControls) Transcript(
	ctx context.Context,
	actor application.Principal,
	ref domain.SessionRef,
) ([]transcript.Event, error) {
	if ref != c.ref {
		return nil, nil
	}
	c.once.Do(func() { close(c.started) })
	select {
	case <-c.release:
		return c.blockingControls.Transcript(ctx, actor, ref)
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func (c *blockingControls) SendInput(
	_ context.Context,
	_ application.Principal,
	_ string,
	text string,
) (sessioncontrol.Accepted, error) {
	c.mu.Lock()
	c.text = text
	if c.afterSend != nil {
		c.events = append([]transcript.Event(nil), c.afterSend...)
	}
	c.mu.Unlock()
	return sessioncontrol.Accepted{Session: c.ref}, nil
}

func (c *blockingControls) SendExternalInput(
	_ context.Context,
	_ application.Principal,
	_ string,
	input runtimehost.InputPayload,
) (sessioncontrol.Accepted, error) {
	c.external = &input
	return sessioncontrol.Accepted{Session: c.ref}, nil
}

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
		{Kind: transcript.EventAssistantFinal, Text: "FINAL ANSWER",
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
	// The first signal is the initial plain card; the second is the completed
	// Rich replacement. Receiving it synchronizes the assertion with the worker.
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
	latest := fixture.messenger.sent[len(fixture.messenger.sent)-1].Text
	if !strings.Contains(latest, "FINAL ANSWER") {
		t.Fatalf("final answer missing from settled card: %q", latest)
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

func (c *blockingControls) Stop(
	context.Context,
	application.Principal,
	string,
	domain.SessionRef,
) (sessioncontrol.Accepted, error) {
	close(c.started)
	<-c.release
	return sessioncontrol.Accepted{
		Session: c.ref,
		Receipt: runtimehost.Receipt{OperationID: "stop", Accepted: true},
	}, nil
}

func (c *blockingControls) Clear(
	context.Context,
	application.Principal,
	string,
	domain.SessionRef,
) (sessioncontrol.Accepted, error) {
	return sessioncontrol.Accepted{}, nil
}

func (c *blockingControls) Close(
	context.Context,
	application.Principal,
	string,
	domain.SessionRef,
) (sessioncontrol.Accepted, error) {
	return sessioncontrol.Accepted{}, nil
}

func (c *blockingControls) Restore(
	context.Context,
	application.Principal,
	string,
	domain.SessionRef,
) (sessioncontrol.Accepted, error) {
	return sessioncontrol.Accepted{}, nil
}

func (c *blockingControls) OpenTerminal(
	context.Context,
	application.Principal,
	string,
	domain.SessionRef,
) (sessioncontrol.Accepted, error) {
	return sessioncontrol.Accepted{}, nil
}

func (c *blockingControls) CapturePane(
	context.Context,
	application.Principal,
	string,
	domain.SessionRef,
) ([]byte, error) {
	return append([]byte(nil), c.pane...), nil
}

func (c *blockingControls) Transcript(
	context.Context,
	application.Principal,
	domain.SessionRef,
) ([]transcript.Event, error) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return append([]transcript.Event(nil), c.events...), nil
}

func (c *blockingControls) OpenSessionFile(
	context.Context, application.Principal, domain.SessionRef, string,
) (nodecontrol.SessionFile, error) {
	return nodecontrol.SessionFile{}, domain.ErrNotFound
}

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
	data, err := (telegramui.Callback{
		Action: telegramui.ActionSelectSession, Token: token,
	}).Encode()
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

func TestTranscriptPageButtonsResolveOpaqueTargetPage(t *testing.T) {
	fixture := newFixture(t)
	ref := domain.SessionRef{NodeID: "allowed", SessionID: "live"}
	controls := &blockingControls{events: []transcript.Event{
		{Kind: transcript.EventAssistantFinal, Text: "First answer"},
		{Kind: transcript.EventAssistantFinal, Text: "Second answer"},
	}}
	handler, err := telegramapp.NewHandlerWithControls(
		fixture.service, fixture.projector, fixture.codec, fixture.messenger, controls,
	)
	if err != nil {
		t.Fatal(err)
	}
	selectToken, err := fixture.codec.Session(7, telegramui.ActionSelectSession, ref)
	if err != nil {
		t.Fatal(err)
	}
	selectData, err := (telegramui.Callback{
		Action: telegramui.ActionSelectSession, Token: selectToken,
	}).Encode()
	if err != nil {
		t.Fatal(err)
	}
	if err := handler.HandleTelegramUpdate(context.Background(), telegrambot.IncomingUpdate{
		UpdateID: 90, Kind: telegrambot.IncomingCallback, ChatID: 7, UserID: 7,
		CallbackID: "select", CallbackData: selectData,
		CallbackOrigin: telegrambot.Message{ChatID: 7, MessageID: 10},
	}); err != nil {
		t.Fatal(err)
	}
	latest := fixture.messenger.sent[len(fixture.messenger.sent)-1]
	if !strings.Contains(latest.Text, "Second answer") || strings.Contains(latest.Text, "First answer") ||
		!strings.Contains(telegramui.CanonicalGrid(latest.Grid), "2/2") {
		t.Fatalf("latest page=%#v", latest)
	}
	previous := latest.Grid[0][0].Callback
	previousData, err := previous.Encode()
	if err != nil {
		t.Fatal(err)
	}
	if err := handler.HandleTelegramUpdate(context.Background(), telegrambot.IncomingUpdate{
		UpdateID: 91, Kind: telegrambot.IncomingCallback, ChatID: 7, UserID: 7,
		CallbackID: "previous", CallbackData: previousData,
		CallbackOrigin: telegrambot.Message{ChatID: 7, MessageID: 10, Rich: true},
	}); err != nil {
		t.Fatal(err)
	}
	first := fixture.messenger.edited[len(fixture.messenger.edited)-1]
	if !strings.Contains(first.Text, "First answer") || strings.Contains(first.Text, "Second answer") ||
		!strings.Contains(telegramui.CanonicalGrid(first.Grid), "1/2") {
		t.Fatalf("first page=%#v", first)
	}
	wrapped := first.Grid[0][0].Callback
	wrappedData, err := wrapped.Encode()
	if err != nil {
		t.Fatal(err)
	}
	if err := handler.HandleTelegramUpdate(context.Background(), telegrambot.IncomingUpdate{
		UpdateID: 92, Kind: telegrambot.IncomingCallback, ChatID: 7, UserID: 7,
		CallbackID: "wrapped-previous", CallbackData: wrappedData,
		CallbackOrigin: telegrambot.Message{ChatID: 7, MessageID: 10, Rich: true},
	}); err != nil {
		t.Fatal(err)
	}
	last := fixture.messenger.edited[len(fixture.messenger.edited)-1]
	if !strings.Contains(last.Text, "Second answer") ||
		!strings.Contains(telegramui.CanonicalGrid(last.Grid), "2/2") {
		t.Fatalf("wrapped last page=%#v", last)
	}
}

func TestRepeatedStalePreviousCallbackAccumulatesPageMoves(t *testing.T) {
	fixture := newFixture(t)
	ref := domain.SessionRef{NodeID: "allowed", SessionID: "live"}
	events := make([]transcript.Event, 0, 9)
	for page := 1; page <= 9; page++ {
		events = append(events, transcript.Event{
			Kind: transcript.EventAssistantFinal, Text: fmt.Sprintf("Answer %d", page),
		})
	}
	controls := &blockingControls{events: events}
	handler, err := telegramapp.NewHandlerWithControls(
		fixture.service, fixture.projector, fixture.codec, fixture.messenger, controls,
	)
	if err != nil {
		t.Fatal(err)
	}
	selectToken, err := fixture.codec.Session(7, telegramui.ActionSelectSession, ref)
	if err != nil {
		t.Fatal(err)
	}
	selectData, err := (telegramui.Callback{
		Action: telegramui.ActionSelectSession, Token: selectToken,
	}).Encode()
	if err != nil {
		t.Fatal(err)
	}
	origin := telegrambot.Message{ChatID: 7, MessageID: 10, Rich: true}
	if err := handler.HandleTelegramUpdate(context.Background(), telegrambot.IncomingUpdate{
		UpdateID: 100, Kind: telegrambot.IncomingCallback, ChatID: 7, UserID: 7,
		CallbackID: "select", CallbackData: selectData, CallbackOrigin: origin,
	}); err != nil {
		t.Fatal(err)
	}
	latest := fixture.messenger.edited[len(fixture.messenger.edited)-1]
	if !strings.Contains(telegramui.CanonicalGrid(latest.Grid), "9/9") {
		t.Fatalf("latest=%#v", latest)
	}
	stalePrevious, err := latest.Grid[0][0].Callback.Encode()
	if err != nil {
		t.Fatal(err)
	}
	for updateID := int64(101); updateID <= 102; updateID++ {
		if err := handler.HandleTelegramUpdate(context.Background(), telegrambot.IncomingUpdate{
			UpdateID: updateID, Kind: telegrambot.IncomingCallback, ChatID: 7, UserID: 7,
			CallbackID: fmt.Sprintf("previous-%d", updateID), CallbackData: stalePrevious,
			CallbackOrigin: origin,
		}); err != nil {
			t.Fatal(err)
		}
	}
	pageEight := fixture.messenger.edited[len(fixture.messenger.edited)-2]
	pageSeven := fixture.messenger.edited[len(fixture.messenger.edited)-1]
	if !strings.Contains(telegramui.CanonicalGrid(pageEight.Grid), "8/9") ||
		!strings.Contains(telegramui.CanonicalGrid(pageSeven.Grid), "7/9") {
		t.Fatalf("repeated stale callback pages=%q then %q",
			telegramui.CanonicalGrid(pageEight.Grid), telegramui.CanonicalGrid(pageSeven.Grid))
	}
}
