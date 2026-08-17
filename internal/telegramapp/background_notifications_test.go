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

func TestMutedBackgroundNotificationIsNotSentAndIsDurablyConsumed(t *testing.T) {
	fixture := newFixture(t)
	actor := application.Principal{UserID: 7}
	preferences, err := fixture.service.Preferences(actor)
	if err != nil {
		t.Fatal(err)
	}
	if err := preferences.SetBackgroundNotification(domain.BackgroundFinished, false); err != nil {
		t.Fatal(err)
	}
	if err := fixture.service.SetPreferences(context.Background(), actor, preferences); err != nil {
		t.Fatal(err)
	}
	created := time.Unix(300, 0).UTC()
	session := domain.Session{
		ID: "background-finished", NodeID: "allowed", OwnerID: 7, Name: "Finished",
		Backend: "codex", State: domain.SessionLive, RuntimePhase: domain.RuntimeRunning,
		CreatedAt: created, LiveSinceAt: created,
	}
	applyBackgroundCommand(t, fixture, "add-muted-background", clusterstate.CommandAddSession,
		created, session)
	stored := fixture.machine.State().Sessions[session.Ref().Key()]
	applyBackgroundCommand(t, fixture, "finish-muted-background",
		clusterstate.CommandPublishSessionRuntime, created.Add(time.Second),
		clusterstate.PublishSessionRuntime{
			Session: session.Ref(), Generation: stored.RuntimeGeneration,
			Phase: domain.RuntimeIdle,
		})
	handler, err := telegramapp.NewHandler(
		fixture.service, fixture.projector, fixture.codec, fixture.messenger,
	)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Millisecond)
	defer cancel()
	handler.RunBackgroundNotifications(ctx, 5*time.Millisecond)
	if len(fixture.messenger.sent) != 0 {
		t.Fatalf("muted notifications=%d", len(fixture.messenger.sent))
	}
	notice := fixture.machine.State().Navigation.BackgroundByUser[7][session.Ref().Key()]
	if notice.Kind != domain.BackgroundFinished || !notice.Notified {
		t.Fatalf("muted notice=%#v", notice)
	}
}

func TestReconciliationRefreshesActiveResponseCardWithRecoveredFinal(t *testing.T) {
	fixture := newFixture(t)
	// This test intentionally runs reconciliation and card refresh concurrently;
	// the shared ordering slice is for synchronous callback tests only.
	fixture.messenger.events = nil
	actor := application.Principal{UserID: 7}
	ref := domain.SessionRef{NodeID: "allowed", SessionID: "live"}
	session := fixture.machine.State().Sessions[ref.Key()]
	runningAt := time.Unix(200, 0).UTC()
	applyBackgroundCommand(t, fixture, "active-running",
		clusterstate.CommandPublishSessionRuntime, runningAt,
		clusterstate.PublishSessionRuntime{
			Session: ref, Generation: session.RuntimeGeneration,
			Phase: domain.RuntimeRunning,
		})
	running := fixture.machine.State().Sessions[ref.Key()]
	if err := fixture.service.RecordTelegramResponseCard(
		application.WithOperationScope(context.Background(), "active-card"), actor,
		domain.TelegramResponseCard{
			ChatID: 7, MessageID: 51, Rich: true, Session: ref,
			SessionRevision: running.Revision, SessionEventAt: running.LastEventAt,
		},
	); err != nil {
		t.Fatal(err)
	}
	controls := &blockingControls{ref: ref, events: []transcript.Event{
		{
			Kind: transcript.EventToolResult, Head: "Bash", Body: "ok",
			Timestamp: runningAt.Add(time.Second).Format(time.RFC3339Nano),
		},
		{
			Kind: transcript.EventAssistantFinal, Text: "RECOVERED FINAL",
			Timestamp: runningAt.Add(2 * time.Second).Format(time.RFC3339Nano),
		},
	}}
	handler, err := telegramapp.NewHandlerWithControls(
		fixture.service, fixture.projector, fixture.codec, fixture.messenger, controls,
	)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	handler.RunBackgroundNotifications(ctx, 5*time.Millisecond)
	if len(fixture.messenger.sent) == 0 {
		t.Fatal("active final did not create a new response card")
	}
	latest := fixture.messenger.sent[len(fixture.messenger.sent)-1].Text
	if !strings.Contains(latest, "RECOVERED FINAL") {
		t.Fatalf("recovered final missing from active card: %q", latest)
	}
	removedCarrier := false
	for _, message := range fixture.messenger.deleted {
		if message.MessageID == 51 {
			removedCarrier = true
			break
		}
	}
	if !removedCarrier {
		t.Fatalf("completed carrier was not removed: %#v", fixture.messenger.deleted)
	}
	settled := fixture.machine.State().Sessions[ref.Key()]
	if settled.RuntimePhase != domain.RuntimeIdle {
		t.Fatalf("runtime phase = %q", settled.RuntimePhase)
	}
	if active, activeErr := fixture.service.ActiveSession(actor); activeErr != nil || active.Ref() != ref {
		t.Fatalf("completed session stopped being active: %#v / %v", active, activeErr)
	}
	if _, background := fixture.machine.State().Navigation.BackgroundByUser[actor.UserID][ref.Key()]; background {
		t.Fatal("active completed session was incorrectly placed in the background panel")
	}
}

func TestReconciliationRepublishesFinalSettledByHeartbeat(t *testing.T) {
	fixture := newFixture(t)
	actor := application.Principal{UserID: 7}
	ref := domain.SessionRef{NodeID: "allowed", SessionID: "live"}
	session := fixture.machine.State().Sessions[ref.Key()]
	finalAt := time.Now().Add(-time.Second).UTC()
	applyBackgroundCommand(t, fixture, "heartbeat-settled-final",
		clusterstate.CommandPublishSessionRuntime, finalAt,
		clusterstate.PublishSessionRuntime{
			Session: ref, Generation: session.RuntimeGeneration,
			Phase: domain.RuntimeIdle,
		})
	settledBeforeCard := fixture.machine.State().Sessions[ref.Key()]
	// This is the production failure mode: a stale render after heartbeat
	// settlement already checkpointed the settled session revision and event
	// time, even though the provider final was not visible in that render.
	if err := fixture.service.RecordTelegramResponseCard(
		application.WithOperationScope(context.Background(), "stale-active-card"), actor,
		domain.TelegramResponseCard{
			ChatID: 7, MessageID: 81, Rich: true, Session: ref,
			SessionRevision: settledBeforeCard.Revision,
			SessionEventAt:  settledBeforeCard.LastEventAt,
		},
	); err != nil {
		t.Fatal(err)
	}
	controls := &blockingControls{ref: ref, events: []transcript.Event{{
		Kind: transcript.EventAssistantFinal, Text: "HEARTBEAT FINAL",
		Timestamp: finalAt.Format(time.RFC3339Nano),
	}}}
	handler, err := telegramapp.NewHandlerWithControls(
		fixture.service, fixture.projector, fixture.codec, fixture.messenger, controls,
	)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	handler.RunBackgroundNotifications(ctx, 5*time.Millisecond)
	if len(fixture.messenger.sent) != 1 {
		t.Fatalf("replacement cards = %d", len(fixture.messenger.sent))
	}
	if !strings.Contains(fixture.messenger.sent[0].Text, "HEARTBEAT FINAL") {
		t.Fatalf("final missing from replacement: %q", fixture.messenger.sent[0].Text)
	}
	card, ok, cardErr := fixture.service.TelegramResponseCard(actor)
	if cardErr != nil || !ok {
		t.Fatalf("response card missing: %#v / %v", card, cardErr)
	}
	settled := fixture.machine.State().Sessions[ref.Key()]
	if card.Session != ref || card.SessionRevision != settled.Revision ||
		card.SessionEventAt.Before(settled.LastEventAt) ||
		!card.RenderedFinalAt.Equal(finalAt) {
		t.Fatalf("card checkpoint = %#v, session revision = %d", card, settled.Revision)
	}
}

func TestReconciliationRepublishesFinalAfterInterruptedTelegramSettlement(t *testing.T) {
	fixture := newFixture(t)
	actor := application.Principal{UserID: 7}
	ref := domain.SessionRef{NodeID: "allowed", SessionID: "live"}
	session := fixture.machine.State().Sessions[ref.Key()]
	promptAt := time.Now().Add(-5 * time.Second).UTC()
	applyBackgroundCommand(t, fixture, "queued-prompt",
		clusterstate.CommandPublishSessionRuntime, promptAt,
		clusterstate.PublishSessionRuntime{
			Session: ref, Generation: session.RuntimeGeneration,
			Phase: domain.RuntimeRunning, Result: &domain.SessionOperationResult{
				OperationID: "input-before-restart", Action: domain.ActionSendInput,
				Status: domain.OperationQueued,
			},
		})
	running := fixture.machine.State().Sessions[ref.Key()]
	finalAt := promptAt.Add(3 * time.Second)
	settledAt := finalAt.Add(500 * time.Millisecond)
	applyBackgroundCommand(t, fixture, "transcript-settled-before-repost",
		clusterstate.CommandPublishSessionRuntime, settledAt,
		clusterstate.PublishSessionRuntime{
			Session: ref, Generation: running.RuntimeGeneration,
			Phase: domain.RuntimeIdle,
		})
	settled := fixture.machine.State().Sessions[ref.Key()]
	if settled.LastOperation == nil ||
		settled.LastOperation.Status != domain.OperationQueued ||
		!settled.LastEventAt.Equal(settledAt) {
		t.Fatalf("settlement precondition = %#v", settled)
	}
	if err := fixture.service.RecordTelegramResponseCard(
		application.WithOperationScope(context.Background(), "interrupted-final-card"), actor,
		domain.TelegramResponseCard{
			ChatID: 7, MessageID: 82, Rich: true, Session: ref,
			SessionRevision: settled.Revision, SessionEventAt: settled.LastEventAt,
		},
	); err != nil {
		t.Fatal(err)
	}
	controls := &blockingControls{ref: ref, events: []transcript.Event{
		{
			Kind: transcript.EventUserText, Text: "prompt before restart",
			Timestamp: promptAt.Format(time.RFC3339Nano),
		},
		{
			Kind: transcript.EventAssistantFinal, Text: "FINAL AFTER RESTART",
			Timestamp: finalAt.Format(time.RFC3339Nano),
		},
	}}
	handler, err := telegramapp.NewHandlerWithControls(
		fixture.service, fixture.projector, fixture.codec, fixture.messenger, controls,
	)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	handler.RunBackgroundNotifications(ctx, 5*time.Millisecond)
	if len(fixture.messenger.sent) != 1 ||
		!strings.Contains(fixture.messenger.sent[0].Text, "FINAL AFTER RESTART") {
		t.Fatalf("replacement cards = %#v", fixture.messenger.sent)
	}
	card, ok, cardErr := fixture.service.TelegramResponseCard(actor)
	if cardErr != nil || !ok || !card.RenderedFinalAt.Equal(finalAt) {
		t.Fatalf("response card checkpoint = %#v / %v / %v", card, ok, cardErr)
	}
}

func TestHistoricalPageNavigationDoesNotRepublishDeliveredFinal(t *testing.T) {
	fixture := newFixture(t)
	actor := application.Principal{UserID: 7}
	ref := domain.SessionRef{NodeID: "allowed", SessionID: "live"}
	session, err := fixture.service.Session(actor, ref)
	if err != nil {
		t.Fatal(err)
	}
	finalAt := time.Now().Add(-time.Second).UTC()
	if err := fixture.service.RecordTelegramResponseCard(
		application.WithOperationScope(context.Background(), "delivered-final-card"), actor,
		domain.TelegramResponseCard{
			ChatID: 7, MessageID: 117, Rich: true, Session: ref,
			SessionRevision: session.Revision, SessionEventAt: session.LastEventAt,
			RenderedFinalAt: finalAt,
		},
	); err != nil {
		t.Fatal(err)
	}
	controls := &blockingControls{ref: ref, events: []transcript.Event{
		{Kind: transcript.EventAssistantFinal, Text: "Older answer",
			Timestamp: finalAt.Add(-time.Second).Format(time.RFC3339Nano)},
		{Kind: transcript.EventAssistantFinal, Text: "Delivered final",
			Timestamp: finalAt.Format(time.RFC3339Nano)},
	}}
	handler, err := telegramapp.NewHandlerWithControls(
		fixture.service, fixture.projector, fixture.codec, fixture.messenger, controls,
	)
	if err != nil {
		t.Fatal(err)
	}
	initial, err := fixture.projector.SessionCardPage(
		actor, ref, []application.CardEvent{
			{Kind: application.CardEventAssistantText, Text: "Older answer", PageBreak: true},
			{Kind: application.CardEventAssistantText, Text: "Delivered final", PageBreak: true},
		}, 0,
	)
	if err != nil {
		t.Fatal(err)
	}
	previousData, err := initial.Grid[0][0].Callback.Encode()
	if err != nil {
		t.Fatal(err)
	}
	if err := handler.HandleTelegramUpdate(context.Background(), telegrambot.IncomingUpdate{
		UpdateID: 172, Kind: telegrambot.IncomingCallback, ChatID: 7, UserID: 7,
		CallbackID: "historical-page", CallbackData: previousData,
		CallbackOrigin: telegrambot.Message{ChatID: 7, MessageID: 117, Rich: true},
	}); err != nil {
		t.Fatal(err)
	}
	card, ok, cardErr := fixture.service.TelegramResponseCard(actor)
	if cardErr != nil || !ok || card.MessageID != 117 || !card.RenderedFinalAt.Equal(finalAt) {
		t.Fatalf("historical navigation erased final watermark: %#v / %v / %v", card, ok, cardErr)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	handler.RunBackgroundNotifications(ctx, 5*time.Millisecond)
	if len(fixture.messenger.sent) != 0 {
		t.Fatalf("historical page triggered duplicate final carrier: %#v", fixture.messenger.sent)
	}
}

func TestReconciliationAcceptsFastFinalBeforeLegacyDeliveryAck(t *testing.T) {
	fixture := newFixture(t)
	actor := application.Principal{UserID: 7}
	ref := domain.SessionRef{NodeID: "allowed", SessionID: "live"}
	session := fixture.machine.State().Sessions[ref.Key()]
	finalAt := time.Now().Add(-20 * time.Second).UTC()
	ackAt := finalAt.Add(10 * time.Second)
	applyBackgroundCommand(t, fixture, "legacy-running-ack",
		clusterstate.CommandPublishSessionRuntime, ackAt,
		clusterstate.PublishSessionRuntime{
			Session: ref, Generation: session.RuntimeGeneration,
			Phase: domain.RuntimeRunning, Result: &domain.SessionOperationResult{
				OperationID: "legacy-input", Action: domain.ActionSendInput,
				Status: domain.OperationSucceeded,
			},
		})
	running := fixture.machine.State().Sessions[ref.Key()]
	if err := fixture.service.RecordTelegramResponseCard(
		application.WithOperationScope(context.Background(), "legacy-card"), actor,
		domain.TelegramResponseCard{
			ChatID: 7, MessageID: 61, Rich: true, Session: ref,
			SessionRevision: running.Revision, SessionEventAt: running.LastEventAt,
		},
	); err != nil {
		t.Fatal(err)
	}
	controls := &blockingControls{ref: ref, events: []transcript.Event{{
		Kind: transcript.EventAssistantFinal, Text: "FAST FINAL",
		Timestamp: finalAt.Format(time.RFC3339Nano),
	}}}
	handler, err := telegramapp.NewHandlerWithControls(
		fixture.service, fixture.projector, fixture.codec, fixture.messenger, controls,
	)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	handler.RunBackgroundNotifications(ctx, 5*time.Millisecond)
	if got := fixture.machine.State().Sessions[ref.Key()].RuntimePhase; got != domain.RuntimeIdle {
		t.Fatalf("runtime phase = %q", got)
	}
}

func TestBackgroundReconciliationRestoresLiveCardWorkerAfterRestart(t *testing.T) {
	fixture := newFixture(t)
	fixture.messenger.sendNotify = make(chan struct{}, 1)
	fixture.messenger.editNotify = make(chan struct{}, 1)
	actor := application.Principal{UserID: 7}
	ref := domain.SessionRef{NodeID: "allowed", SessionID: "live"}
	session := fixture.machine.State().Sessions[ref.Key()]
	runningAt := time.Now().Add(-time.Second).UTC()
	applyBackgroundCommand(t, fixture, "restart-running",
		clusterstate.CommandPublishSessionRuntime, runningAt,
		clusterstate.PublishSessionRuntime{
			Session: ref, Generation: session.RuntimeGeneration,
			Phase: domain.RuntimeRunning,
		})
	running := fixture.machine.State().Sessions[ref.Key()]
	if err := fixture.service.RecordTelegramResponseCard(
		application.WithOperationScope(context.Background(), "legacy-live-card"), actor,
		domain.TelegramResponseCard{
			ChatID: 7, MessageID: 71, Rich: false, Session: ref,
			SessionRevision: running.Revision, SessionEventAt: running.LastEventAt,
		},
	); err != nil {
		t.Fatal(err)
	}
	controls := &blockingControls{
		ref: ref, pane: []byte("provider is working\n"), events: []transcript.Event{{
			Kind: transcript.EventToolCall, Head: "Bash", Body: "echo working",
			Timestamp: runningAt.Format(time.RFC3339Nano),
		}},
	}
	handler, err := telegramapp.NewHandlerWithControls(
		fixture.service, fixture.projector, fixture.codec, fixture.messenger, controls,
	)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		handler.RunBackgroundNotifications(ctx, 5*time.Millisecond)
	}()
	select {
	case <-fixture.messenger.sendNotify:
	case <-time.After(2 * time.Second):
		cancel()
		<-done
		t.Fatal("restart reconciliation did not promote and refresh the live card")
	}
	select {
	case <-fixture.messenger.editNotify:
	case <-time.After(2 * time.Second):
		cancel()
		<-done
		t.Fatal("restored worker did not refresh the terminal snapshot")
	}
	cancel()
	<-done
	if len(fixture.messenger.sent) == 0 || !fixture.messenger.sent[0].RichMarkdown {
		t.Fatalf("replacement screen = %#v", fixture.messenger.sent)
	}
	if len(fixture.messenger.edited) == 0 || fixture.messenger.edited[len(fixture.messenger.edited)-1].Pane == nil {
		t.Fatalf("refreshed screen = %#v", fixture.messenger.edited)
	}
	if len(fixture.messenger.deleted) == 0 || fixture.messenger.deleted[0].MessageID != 71 {
		t.Fatalf("legacy carrier was not replaced: %#v", fixture.messenger.deleted)
	}
}

func TestServersNavigationIsNotReplacedByRunningSessionRefresh(t *testing.T) {
	fixture := newFixture(t)
	actor := application.Principal{UserID: 7}
	ref := domain.SessionRef{NodeID: "allowed", SessionID: "live"}
	session := fixture.machine.State().Sessions[ref.Key()]
	runningAt := time.Now().UTC()
	applyBackgroundCommand(t, fixture, "servers-navigation-running",
		clusterstate.CommandPublishSessionRuntime, runningAt,
		clusterstate.PublishSessionRuntime{
			Session: ref, Generation: session.RuntimeGeneration,
			Phase: domain.RuntimeRunning,
		})
	running := fixture.machine.State().Sessions[ref.Key()]
	if err := fixture.service.RecordTelegramResponseCard(
		application.WithOperationScope(context.Background(), "servers-navigation-card"), actor,
		domain.TelegramResponseCard{
			ChatID: 7, MessageID: 91, Rich: true, Session: ref,
			SessionRevision: running.Revision, SessionEventAt: running.LastEventAt,
		},
	); err != nil {
		t.Fatal(err)
	}
	controls := &blockingControls{ref: ref, events: []transcript.Event{{
		Kind: transcript.EventToolCall, Head: "Bash", Body: "still working",
		Timestamp: runningAt.Format(time.RFC3339Nano),
	}}}
	handler, err := telegramapp.NewHandlerWithControls(
		fixture.service, fixture.projector, fixture.codec, fixture.messenger, controls,
	)
	if err != nil {
		t.Fatal(err)
	}
	origin := telegrambot.Message{ChatID: 7, MessageID: 91, Rich: true}
	invokeCarrierAction(t, handler, 601, origin, telegramui.ActionSessions, "servers")
	if got := fixture.messenger.edited[len(fixture.messenger.edited)-1].Name; got != telegramui.ScreenStatus {
		t.Fatalf("navigation screen = %q", got)
	}
	card, ok, cardErr := fixture.service.TelegramResponseCard(actor)
	if cardErr != nil || !ok || card.Session != (domain.SessionRef{}) {
		t.Fatalf("navigation card checkpoint = %#v / %v / %v", card, ok, cardErr)
	}
	// Return to the live card, then open the byte-for-byte identical Servers
	// screen again. Each click is a distinct causal mutation even though the
	// resulting empty checkpoint has the same fingerprint.
	if err := fixture.service.RecordTelegramResponseCard(
		application.WithOperationScope(context.Background(), "returned-session-card"), actor,
		domain.TelegramResponseCard{
			ChatID: 7, MessageID: 91, Rich: true, Session: ref,
			SessionRevision: running.Revision, SessionEventAt: running.LastEventAt,
		},
	); err != nil {
		t.Fatal(err)
	}
	invokeCarrierAction(t, handler, 602, origin, telegramui.ActionSessions, "servers")
	card, ok, cardErr = fixture.service.TelegramResponseCard(actor)
	if cardErr != nil || !ok || card.Session != (domain.SessionRef{}) {
		t.Fatalf("repeated navigation card checkpoint = %#v / %v / %v", card, ok, cardErr)
	}
	edits := len(fixture.messenger.edited)
	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Millisecond)
	defer cancel()
	handler.RunBackgroundNotifications(ctx, 5*time.Millisecond)
	if len(fixture.messenger.edited) != edits {
		t.Fatalf("servers screen was replaced: %#v", fixture.messenger.edited[edits:])
	}
}

func applyBackgroundCommand(
	t *testing.T,
	fixture fixture,
	id string,
	kind clusterstate.CommandKind,
	at time.Time,
	payload any,
) {
	t.Helper()
	command, err := clusterstate.NewCommand(id, kind, at, payload)
	if err != nil {
		t.Fatal(err)
	}
	if result := fixture.machine.Apply(command); result.Err() != nil {
		t.Fatal(result.Err())
	}
}
