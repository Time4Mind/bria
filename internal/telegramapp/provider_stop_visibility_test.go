package telegramapp_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/Time4Mind/bria/internal/application"
	"github.com/Time4Mind/bria/internal/clusterstate"
	"github.com/Time4Mind/bria/internal/domain"
	"github.com/Time4Mind/bria/internal/providerstop"
	"github.com/Time4Mind/bria/internal/telegramapp"
	"github.com/Time4Mind/bria/internal/telegrambot"
	"github.com/Time4Mind/bria/internal/telegramui"
	"github.com/Time4Mind/bria/internal/transcript"
)

type blockingSendMessenger struct {
	*messengerStub
	started chan struct{}
	release chan struct{}
}

type blockingFinalMessenger struct {
	*messengerStub
	marker  string
	started chan struct{}
	release chan struct{}
}

func (m *blockingFinalMessenger) SendScreen(
	ctx context.Context,
	chatID int64,
	screen telegramui.Screen,
) (telegrambot.Message, error) {
	if !strings.Contains(screen.Text, m.marker) {
		return m.messengerStub.SendScreen(ctx, chatID, screen)
	}
	select {
	case m.started <- struct{}{}:
	default:
	}
	select {
	case <-m.release:
		return m.messengerStub.SendScreen(ctx, chatID, screen)
	case <-ctx.Done():
		return telegrambot.Message{}, ctx.Err()
	}
}

func (m *blockingSendMessenger) SendScreen(
	ctx context.Context,
	chatID int64,
	screen telegramui.Screen,
) (telegrambot.Message, error) {
	select {
	case m.started <- struct{}{}:
	default:
	}
	select {
	case <-m.release:
		return m.messengerStub.SendScreen(ctx, chatID, screen)
	case <-ctx.Done():
		return telegrambot.Message{}, ctx.Err()
	}
}

func prepareProviderStopTurn(
	t *testing.T,
	fixture fixture,
	ref domain.SessionRef,
	providerID string,
	promptAt time.Time,
) domain.Session {
	t.Helper()
	session := fixture.machine.State().Sessions[ref.Key()]
	applyBackgroundCommand(t, fixture, "bind-"+string(ref.SessionID),
		clusterstate.CommandBindProviderSession, promptAt.Add(-time.Second),
		clusterstate.BindProviderSession{
			ActorID: 7, Session: ref, ExpectedRevision: session.Revision,
			ProviderID: providerID,
		})
	bound := fixture.machine.State().Sessions[ref.Key()]
	applyBackgroundCommand(t, fixture, "run-"+string(ref.SessionID),
		clusterstate.CommandPublishSessionRuntime, promptAt,
		clusterstate.PublishSessionRuntime{
			Session: ref, Generation: bound.RuntimeGeneration,
			Phase: domain.RuntimeRunning, Result: &domain.SessionOperationResult{
				OperationID: "input-" + string(ref.SessionID), Action: domain.ActionSendInput,
				Status: domain.OperationQueued,
			},
		})
	return fixture.machine.State().Sessions[ref.Key()]
}

func providerStopEvents(promptAt time.Time, finalText string) ([]transcript.Event, time.Time) {
	finalAt := promptAt.Add(500 * time.Millisecond)
	return []transcript.Event{
		{Kind: transcript.EventUserText, Text: "prompt", Timestamp: promptAt.Format(time.RFC3339Nano)},
		{Kind: transcript.EventAssistantFinal, Text: finalText,
			Timestamp: finalAt.Format(time.RFC3339Nano)},
		{Kind: transcript.EventTurnComplete,
			Timestamp: finalAt.Add(time.Millisecond).Format(time.RFC3339Nano)},
	}, finalAt
}

func notifyProviderStop(
	t *testing.T,
	handler *telegramapp.Handler,
	signal providerstop.Signal,
) context.CancelFunc {
	t.Helper()
	bus := providerstop.NewBus(4)
	ctx, cancel := context.WithCancel(context.Background())
	go handler.RunProviderStopNotifications(ctx, bus.Events())
	if err := bus.Notify(ctx, signal); err != nil {
		cancel()
		t.Fatal(err)
	}
	return cancel
}

func waitForSessionPhase(
	t *testing.T,
	fixture fixture,
	ref domain.SessionRef,
	phase domain.RuntimePhase,
) domain.Session {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		session := fixture.machine.State().Sessions[ref.Key()]
		if session.RuntimePhase == phase {
			return session
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("session %s did not reach %s", ref.Key(), phase)
	return domain.Session{}
}

func waitForSentScreens(t *testing.T, messenger *messengerStub, want int) []telegramui.Screen {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		sent, _, _ := messenger.screensSnapshot()
		if len(sent) >= want {
			return sent
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("sent screens did not reach %d", want)
	return nil
}

func TestProviderStopRecoversMissingCardForActiveSessionOnce(t *testing.T) {
	fixture := newFixture(t)
	fixture.messenger.events = nil
	actor := application.Principal{UserID: 7}
	ref := domain.SessionRef{NodeID: "allowed", SessionID: "live"}
	providerID := "019fffe8-02ee-7aa1-b6cf-eed13a005490"
	promptAt := time.Now().Add(-time.Second).UTC()
	prepareProviderStopTurn(t, fixture, ref, providerID, promptAt)
	events, finalAt := providerStopEvents(promptAt, "RECOVERED WITHOUT REGISTRY")
	controls := &blockingControls{ref: ref, events: events}
	handler, err := telegramapp.NewHandlerWithControls(
		fixture.service, fixture.projector, fixture.codec, fixture.messenger, controls,
	)
	if err != nil {
		t.Fatal(err)
	}
	signal := providerstop.Signal{
		NodeID: string(ref.NodeID), SessionID: string(ref.SessionID),
		ProviderSessionID: providerID,
		RuntimeGeneration: fixture.machine.State().Sessions[ref.Key()].RuntimeGeneration,
	}
	cancel := notifyProviderStop(t, handler, signal)
	defer cancel()
	sent := waitForSentScreens(t, fixture.messenger, 1)
	if len(sent) != 1 || !strings.Contains(sent[0].Text, "RECOVERED WITHOUT REGISTRY") {
		t.Fatalf("recovered screens=%#v", sent)
	}
	var card domain.TelegramResponseCard
	var ok bool
	var cardErr error
	cardDeadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(cardDeadline) {
		card, ok, cardErr = fixture.service.TelegramResponseCard(actor)
		if cardErr != nil || ok {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	if cardErr != nil || !ok || card.Session != ref || !card.RenderedFinalAt.Equal(finalAt) {
		t.Fatalf("recovered card=%#v present=%v err=%v", card, ok, cardErr)
	}
	bus := providerstop.NewBus(1)
	duplicateCtx, duplicateCancel := context.WithCancel(context.Background())
	defer duplicateCancel()
	go handler.RunProviderStopNotifications(duplicateCtx, bus.Events())
	if err := bus.Notify(duplicateCtx, signal); err != nil {
		t.Fatal(err)
	}
	time.Sleep(100 * time.Millisecond)
	sent, edited, _ := fixture.messenger.screensSnapshot()
	if len(sent) != 1 || len(edited) != 0 {
		t.Fatalf("duplicate signal mutated transport: sent=%d edited=%d", len(sent), len(edited))
	}
}

func TestProviderStopRejectsStaleRuntimeGeneration(t *testing.T) {
	fixture := newFixture(t)
	fixture.messenger.events = nil
	ref := domain.SessionRef{NodeID: "allowed", SessionID: "live"}
	providerID := "019fffe8-02ee-7aa1-b6cf-eed13a005496"
	promptAt := time.Now().Add(-time.Second).UTC()
	running := prepareProviderStopTurn(t, fixture, ref, providerID, promptAt)
	events, _ := providerStopEvents(promptAt, "STALE GENERATION FINAL")
	controls := &blockingControls{ref: ref, events: events}
	handler, err := telegramapp.NewHandlerWithControls(
		fixture.service, fixture.projector, fixture.codec, fixture.messenger, controls,
	)
	if err != nil {
		t.Fatal(err)
	}
	cancel := notifyProviderStop(t, handler, providerstop.Signal{
		NodeID: string(ref.NodeID), SessionID: string(ref.SessionID),
		ProviderSessionID: providerID, RuntimeGeneration: running.RuntimeGeneration + 1,
	})
	defer cancel()
	time.Sleep(100 * time.Millisecond)
	session := fixture.machine.State().Sessions[ref.Key()]
	if session.RuntimePhase != domain.RuntimeRunning {
		t.Fatalf("stale signal changed runtime phase=%s", session.RuntimePhase)
	}
	sent, edited, deleted := fixture.messenger.screensSnapshot()
	if len(sent) != 0 || len(edited) != 0 || len(deleted) != 0 {
		t.Fatalf("stale signal mutated transport: sent=%d edited=%d deleted=%d",
			len(sent), len(edited), len(deleted))
	}
}

func TestProviderStopFindsHeartbeatSettledActiveSessionWithoutRegistry(t *testing.T) {
	fixture := newFixture(t)
	fixture.messenger.events = nil
	actor := application.Principal{UserID: 7}
	ref := domain.SessionRef{NodeID: "allowed", SessionID: "live"}
	providerID := "019fffe8-02ee-7aa1-b6cf-eed13a005494"
	promptAt := time.Now().Add(-time.Second).UTC()
	running := prepareProviderStopTurn(t, fixture, ref, providerID, promptAt)
	events, finalAt := providerStopEvents(promptAt, "FINAL AFTER HEARTBEAT")
	applyBackgroundCommand(t, fixture, "heartbeat-before-stop",
		clusterstate.CommandPublishSessionRuntime, finalAt.Add(100*time.Millisecond),
		clusterstate.PublishSessionRuntime{
			Session: ref, Generation: running.RuntimeGeneration, Phase: domain.RuntimeIdle,
		})
	controls := &blockingControls{ref: ref, events: events}
	handler, err := telegramapp.NewHandlerWithControls(
		fixture.service, fixture.projector, fixture.codec, fixture.messenger, controls,
	)
	if err != nil {
		t.Fatal(err)
	}
	cancel := notifyProviderStop(t, handler, providerstop.Signal{
		NodeID: string(ref.NodeID), SessionID: string(ref.SessionID),
		ProviderSessionID: providerID,
	})
	defer cancel()
	sent := waitForSentScreens(t, fixture.messenger, 1)
	if len(sent) != 1 || !strings.Contains(sent[0].Text, "FINAL AFTER HEARTBEAT") {
		t.Fatalf("heartbeat recovery screens=%#v", sent)
	}
	card, ok, cardErr := fixture.service.TelegramResponseCard(actor)
	if cardErr != nil || !ok || card.Session != ref || !card.RenderedFinalAt.Equal(finalAt) {
		t.Fatalf("heartbeat recovery card=%#v present=%v err=%v", card, ok, cardErr)
	}
}

func TestReconciliationRecoversSettledActiveFinalWithoutRegistry(t *testing.T) {
	fixture := newFixture(t)
	fixture.messenger.events = nil
	actor := application.Principal{UserID: 7}
	ref := domain.SessionRef{NodeID: "allowed", SessionID: "live"}
	providerID := "019fffe8-02ee-7aa1-b6cf-eed13a005495"
	promptAt := time.Now().Add(-time.Second).UTC()
	running := prepareProviderStopTurn(t, fixture, ref, providerID, promptAt)
	events, finalAt := providerStopEvents(promptAt, "FINAL AFTER RESTART WITHOUT CARD")
	applyBackgroundCommand(t, fixture, "settled-before-restart-without-card",
		clusterstate.CommandPublishSessionRuntime, finalAt.Add(100*time.Millisecond),
		clusterstate.PublishSessionRuntime{
			Session: ref, Generation: running.RuntimeGeneration, Phase: domain.RuntimeIdle,
		})
	controls := &blockingControls{ref: ref, events: events}
	handler, err := telegramapp.NewHandlerWithControls(
		fixture.service, fixture.projector, fixture.codec, fixture.messenger, controls,
	)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	handler.RunBackgroundNotifications(ctx, 5*time.Millisecond)
	sent, _, _ := fixture.messenger.screensSnapshot()
	if len(sent) != 1 || !strings.Contains(sent[0].Text, "FINAL AFTER RESTART WITHOUT CARD") {
		t.Fatalf("restart recovery screens=%#v", sent)
	}
	card, ok, cardErr := fixture.service.TelegramResponseCard(actor)
	if cardErr != nil || !ok || card.Session != ref || !card.RenderedFinalAt.Equal(finalAt) {
		t.Fatalf("restart recovery card=%#v present=%v err=%v", card, ok, cardErr)
	}
}

func TestProviderStopKeepsBackgroundFinalSilentUntilSelection(t *testing.T) {
	fixture := newFixture(t)
	fixture.messenger.events = nil
	actor := application.Principal{UserID: 7}
	background := domain.Session{
		ID: "background-stop", NodeID: "allowed", OwnerID: 7, Name: "Background",
		Backend: "codex", State: domain.SessionActive, RuntimePhase: domain.RuntimeIdle,
		CreatedAt: time.Now().Add(-time.Minute), LiveSinceAt: time.Now().Add(-time.Minute),
	}
	applyBackgroundCommand(t, fixture, "add-background-stop",
		clusterstate.CommandAddSession, background.CreatedAt, background)
	providerID := "019fffe8-02ee-7aa1-b6cf-eed13a005491"
	promptAt := time.Now().Add(-time.Second).UTC()
	prepareProviderStopTurn(t, fixture, background.Ref(), providerID, promptAt)
	events, finalAt := providerStopEvents(promptAt, "BACKGROUND FINAL ON SELECTION")
	controls := &blockingControls{ref: background.Ref(), events: events}
	handler, err := telegramapp.NewHandlerWithControls(
		fixture.service, fixture.projector, fixture.codec, fixture.messenger, controls,
	)
	if err != nil {
		t.Fatal(err)
	}
	cancel := notifyProviderStop(t, handler, providerstop.Signal{
		NodeID: string(background.NodeID), SessionID: string(background.ID),
		ProviderSessionID: providerID,
	})
	defer cancel()
	waitForSessionPhase(t, fixture, background.Ref(), domain.RuntimeIdle)
	time.Sleep(50 * time.Millisecond)
	sent, edited, deleted := fixture.messenger.screensSnapshot()
	if len(sent) != 0 || len(edited) != 0 || len(deleted) != 0 {
		t.Fatalf("background final surfaced: sent=%d edited=%d deleted=%d",
			len(sent), len(edited), len(deleted))
	}
	card, ok, cardErr := fixture.service.TelegramResponseCard(actor)
	if cardErr != nil || ok {
		t.Fatalf("background final manufactured carrier=%#v present=%v err=%v", card, ok, cardErr)
	}
	token, err := fixture.codec.Session(7, telegramui.ActionSelectSession, background.Ref())
	if err != nil {
		t.Fatal(err)
	}
	data, err := (telegramui.Callback{
		Action: telegramui.ActionSelectSession, Token: token,
	}).Encode()
	if err != nil {
		t.Fatal(err)
	}
	viewCtx, viewCancel := context.WithCancel(context.Background())
	defer viewCancel()
	if err := handler.HandleTelegramUpdate(viewCtx, telegrambot.IncomingUpdate{
		UpdateID: 990, Kind: telegrambot.IncomingCallback, ChatID: 7, UserID: 7,
		CallbackID: "select-background-final", CallbackData: data,
		CallbackOrigin: telegrambot.Message{ChatID: 7, MessageID: 70, Rich: true},
	}); err != nil {
		t.Fatal(err)
	}
	_, edited, _ = fixture.messenger.screensSnapshot()
	if len(edited) != 1 || !strings.Contains(edited[0].Text, "BACKGROUND FINAL ON SELECTION") {
		t.Fatalf("selected background card=%#v", edited)
	}
	card, ok, cardErr = fixture.service.TelegramResponseCard(actor)
	if cardErr != nil || !ok || card.Session != background.Ref() ||
		!card.RenderedFinalAt.Equal(finalAt) {
		t.Fatalf("selected card=%#v present=%v err=%v", card, ok, cardErr)
	}
}

func TestMenuCheckpointSurvivesRestartAndBlocksFinalRecovery(t *testing.T) {
	fixture := newFixture(t)
	fixture.messenger.events = nil
	actor := application.Principal{UserID: 7}
	ref := domain.SessionRef{NodeID: "allowed", SessionID: "live"}
	providerID := "019fffe8-02ee-7aa1-b6cf-eed13a005498"
	promptAt := time.Now().Add(-time.Second).UTC()
	running := prepareProviderStopTurn(t, fixture, ref, providerID, promptAt)
	events, finalAt := providerStopEvents(promptAt, "FINAL MUST STAY BEHIND MENU")
	applyBackgroundCommand(t, fixture, "settled-behind-real-menu",
		clusterstate.CommandPublishSessionRuntime, finalAt.Add(100*time.Millisecond),
		clusterstate.PublishSessionRuntime{
			Session: ref, Generation: running.RuntimeGeneration, Phase: domain.RuntimeIdle,
		})
	controls := &blockingControls{ref: ref, events: events}
	handler, err := telegramapp.NewHandlerWithControls(
		fixture.service, fixture.projector, fixture.codec, fixture.messenger, controls,
	)
	if err != nil {
		t.Fatal(err)
	}
	if err := handler.HandleTelegramUpdate(context.Background(), telegrambot.IncomingUpdate{
		UpdateID: 992, Kind: telegrambot.IncomingMessage, ChatID: 7, UserID: 7, Text: "/menu",
	}); err != nil {
		t.Fatal(err)
	}
	card, ok, cardErr := fixture.service.TelegramResponseCard(actor)
	if cardErr != nil || !ok || card.Session != (domain.SessionRef{}) || card.MessageID == 0 {
		t.Fatalf("menu checkpoint=%#v present=%v err=%v", card, ok, cardErr)
	}

	// A new handler has no in-memory visible-view state. The durable non-session
	// checkpoint must still prevent reconciliation from surfacing the final.
	restarted, err := telegramapp.NewHandlerWithControls(
		fixture.service, fixture.projector, fixture.codec, fixture.messenger, controls,
	)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Millisecond)
	defer cancel()
	restarted.RunBackgroundNotifications(ctx, 5*time.Millisecond)
	sent, edited, deleted := fixture.messenger.screensSnapshot()
	if len(sent) != 1 || len(edited) != 0 || len(deleted) != 0 {
		t.Fatalf("restart replaced menu: sent=%d edited=%d deleted=%d",
			len(sent), len(edited), len(deleted))
	}
	card, ok, cardErr = fixture.service.TelegramResponseCard(actor)
	if cardErr != nil || !ok || card.Session != (domain.SessionRef{}) {
		t.Fatalf("menu checkpoint changed=%#v present=%v err=%v", card, ok, cardErr)
	}
}

func TestProviderStopDoesNotReplaceVisibleNonSessionFlow(t *testing.T) {
	fixture := newFixture(t)
	fixture.messenger.events = nil
	actor := application.Principal{UserID: 7}
	ref := domain.SessionRef{NodeID: "allowed", SessionID: "live"}
	if err := fixture.service.RecordTelegramResponseCard(
		application.WithOperationScope(context.Background(), "visible-menu-card"), actor,
		domain.TelegramResponseCard{ChatID: 7, MessageID: 71, Rich: true},
	); err != nil {
		t.Fatal(err)
	}
	providerID := "019fffe8-02ee-7aa1-b6cf-eed13a005492"
	promptAt := time.Now().Add(-time.Second).UTC()
	prepareProviderStopTurn(t, fixture, ref, providerID, promptAt)
	events, _ := providerStopEvents(promptAt, "FINAL BEHIND MENU")
	controls := &blockingControls{ref: ref, events: events}
	handler, err := telegramapp.NewHandlerWithControls(
		fixture.service, fixture.projector, fixture.codec, fixture.messenger, controls,
	)
	if err != nil {
		t.Fatal(err)
	}
	cancel := notifyProviderStop(t, handler, providerstop.Signal{
		NodeID: string(ref.NodeID), SessionID: string(ref.SessionID),
		ProviderSessionID: providerID,
	})
	defer cancel()
	waitForSessionPhase(t, fixture, ref, domain.RuntimeIdle)
	time.Sleep(50 * time.Millisecond)
	sent, edited, deleted := fixture.messenger.screensSnapshot()
	if len(sent) != 0 || len(edited) != 0 || len(deleted) != 0 {
		t.Fatalf("non-session flow was replaced: sent=%d edited=%d deleted=%d",
			len(sent), len(edited), len(deleted))
	}
	card, ok, cardErr := fixture.service.TelegramResponseCard(actor)
	if cardErr != nil || !ok || card.Session != (domain.SessionRef{}) || card.MessageID != 71 {
		t.Fatalf("menu carrier changed=%#v present=%v err=%v", card, ok, cardErr)
	}
}

func TestProviderStopRepairsStaleRegistryWhenSessionViewIsKnown(t *testing.T) {
	fixture := newFixture(t)
	fixture.messenger.events = nil
	actor := application.Principal{UserID: 7}
	ref := domain.SessionRef{NodeID: "allowed", SessionID: "live"}
	providerID := "019fffe8-02ee-7aa1-b6cf-eed13a005497"
	promptAt := time.Now().Add(-time.Second).UTC()
	running := prepareProviderStopTurn(t, fixture, ref, providerID, promptAt)
	controls := &blockingControls{ref: ref, events: []transcript.Event{{
		Kind: transcript.EventUserText, Text: "prompt",
		Timestamp: promptAt.Format(time.RFC3339Nano),
	}}}
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
	viewCtx, viewCancel := context.WithCancel(context.Background())
	defer viewCancel()
	if err := handler.HandleTelegramUpdate(viewCtx, telegrambot.IncomingUpdate{
		UpdateID: 991, Kind: telegrambot.IncomingCallback, ChatID: 7, UserID: 7,
		CallbackID: "select-visible-session", CallbackData: data,
		CallbackOrigin: telegrambot.Message{ChatID: 7, MessageID: 72, Rich: true},
	}); err != nil {
		t.Fatal(err)
	}
	if err := fixture.service.RecordTelegramResponseCard(
		application.WithOperationScope(context.Background(), "late-non-session-checkpoint"), actor,
		domain.TelegramResponseCard{ChatID: 7, MessageID: 73, Rich: true},
	); err != nil {
		t.Fatal(err)
	}
	finalAt := promptAt.Add(500 * time.Millisecond)
	controls.appendTranscriptEvent(transcript.Event{
		Kind: transcript.EventAssistantFinal, Text: "REPAIRED STALE REGISTRY",
		Timestamp: finalAt.Format(time.RFC3339Nano),
	})
	controls.appendTranscriptEvent(transcript.Event{
		Kind:      transcript.EventTurnComplete,
		Timestamp: finalAt.Add(time.Millisecond).Format(time.RFC3339Nano),
	})
	cancel := notifyProviderStop(t, handler, providerstop.Signal{
		NodeID: string(ref.NodeID), SessionID: string(ref.SessionID),
		ProviderSessionID: providerID, RuntimeGeneration: running.RuntimeGeneration,
	})
	defer cancel()
	sent := waitForSentScreens(t, fixture.messenger, 1)
	if len(sent) != 1 || !strings.Contains(sent[0].Text, "REPAIRED STALE REGISTRY") {
		t.Fatalf("registry repair screens=%#v", sent)
	}
	card, ok, cardErr := fixture.service.TelegramResponseCard(actor)
	if cardErr != nil || !ok || card.Session != ref || !card.RenderedFinalAt.Equal(finalAt) {
		t.Fatalf("registry repair card=%#v present=%v err=%v", card, ok, cardErr)
	}
}

func TestRecoveredFinalIsDiscardedWhenSessionSwitchesDuringSend(t *testing.T) {
	fixture := newFixture(t)
	fixture.messenger.events = nil
	actor := application.Principal{UserID: 7}
	ref := domain.SessionRef{NodeID: "allowed", SessionID: "live"}
	second := domain.Session{
		ID: "switch-target", NodeID: "allowed", OwnerID: 7, Name: "Other",
		Backend: "codex", State: domain.SessionActive, RuntimePhase: domain.RuntimeIdle,
		CreatedAt: time.Now(), LiveSinceAt: time.Now(),
	}
	applyBackgroundCommand(t, fixture, "add-switch-target",
		clusterstate.CommandAddSession, second.CreatedAt, second)
	providerID := "019fffe8-02ee-7aa1-b6cf-eed13a005493"
	promptAt := time.Now().Add(-time.Second).UTC()
	prepareProviderStopTurn(t, fixture, ref, providerID, promptAt)
	events, _ := providerStopEvents(promptAt, "STALE SWITCH FINAL")
	controls := &blockingControls{ref: ref, events: events}
	blocking := &blockingSendMessenger{
		messengerStub: fixture.messenger,
		started:       make(chan struct{}, 1), release: make(chan struct{}),
	}
	handler, err := telegramapp.NewHandlerWithControls(
		fixture.service, fixture.projector, fixture.codec, blocking, controls,
	)
	if err != nil {
		t.Fatal(err)
	}
	cancel := notifyProviderStop(t, handler, providerstop.Signal{
		NodeID: string(ref.NodeID), SessionID: string(ref.SessionID),
		ProviderSessionID: providerID,
	})
	defer cancel()
	select {
	case <-blocking.started:
	case <-time.After(2 * time.Second):
		t.Fatal("recovered final send did not start")
	}
	if err := fixture.service.SelectSession(context.Background(), actor, second.Ref()); err != nil {
		t.Fatal(err)
	}
	close(blocking.release)
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		_, _, deleted := fixture.messenger.screensSnapshot()
		if len(deleted) == 1 {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	_, _, deleted := fixture.messenger.screensSnapshot()
	if len(deleted) != 1 {
		t.Fatalf("stale replacement was not deleted: %#v", deleted)
	}
	if card, ok, cardErr := fixture.service.TelegramResponseCard(actor); cardErr != nil || ok {
		t.Fatalf("stale replacement committed=%#v present=%v err=%v", card, ok, cardErr)
	}
	active, activeErr := fixture.service.ActiveSession(actor)
	if activeErr != nil || active.Ref() != second.Ref() {
		t.Fatalf("active session=%#v err=%v", active, activeErr)
	}
}

func TestRecoveredFinalIsDiscardedWhenMenuOpensDuringSend(t *testing.T) {
	fixture := newFixture(t)
	fixture.messenger.events = nil
	actor := application.Principal{UserID: 7}
	ref := domain.SessionRef{NodeID: "allowed", SessionID: "live"}
	providerID := "019fffe8-02ee-7aa1-b6cf-eed13a005499"
	promptAt := time.Now().Add(-time.Second).UTC()
	prepareProviderStopTurn(t, fixture, ref, providerID, promptAt)
	events, _ := providerStopEvents(promptAt, "BLOCKED FINAL BEHIND MENU")
	controls := &blockingControls{ref: ref, events: events}
	blocking := &blockingFinalMessenger{
		messengerStub: fixture.messenger, marker: "BLOCKED FINAL BEHIND MENU",
		started: make(chan struct{}, 1), release: make(chan struct{}),
	}
	handler, err := telegramapp.NewHandlerWithControls(
		fixture.service, fixture.projector, fixture.codec, blocking, controls,
	)
	if err != nil {
		t.Fatal(err)
	}
	cancel := notifyProviderStop(t, handler, providerstop.Signal{
		NodeID: string(ref.NodeID), SessionID: string(ref.SessionID),
		ProviderSessionID: providerID,
	})
	defer cancel()
	select {
	case <-blocking.started:
	case <-time.After(2 * time.Second):
		t.Fatal("recovered final send did not start")
	}
	menuDone := make(chan error, 1)
	menuStarted := make(chan struct{})
	go func() {
		close(menuStarted)
		menuDone <- handler.HandleTelegramUpdate(context.Background(), telegrambot.IncomingUpdate{
			UpdateID: 993, Kind: telegrambot.IncomingMessage,
			ChatID: 7, UserID: 7, Text: "/menu",
		})
	}()
	<-menuStarted
	// The Telegram activity queue is intentionally blocked by the final send.
	// Give the menu handler a scheduler turn to publish its visible intent before
	// releasing transport; the race assertion below proves that intent won.
	time.Sleep(20 * time.Millisecond)
	close(blocking.release)
	select {
	case err := <-menuDone:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("menu did not finish after final send was released")
	}
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		_, _, deleted := fixture.messenger.screensSnapshot()
		if len(deleted) == 1 {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	sent, _, deleted := fixture.messenger.screensSnapshot()
	if len(sent) != 2 || sent[0].Name != telegramui.ScreenSessionCard ||
		sent[1].Name != telegramui.ScreenMenu || len(deleted) != 1 {
		t.Fatalf("menu race transport: sent=%#v deleted=%#v", sent, deleted)
	}
	card, ok, cardErr := fixture.service.TelegramResponseCard(actor)
	if cardErr != nil || !ok || card.Session != (domain.SessionRef{}) ||
		card.MessageID != 2 {
		t.Fatalf("menu did not remain current=%#v present=%v err=%v", card, ok, cardErr)
	}
}
