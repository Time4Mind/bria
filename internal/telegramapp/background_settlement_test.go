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
	"github.com/Time4Mind/bria/internal/transcript"
)

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
