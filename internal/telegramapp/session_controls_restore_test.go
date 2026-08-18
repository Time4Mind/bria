package telegramapp_test

import (
	"context"
	"fmt"
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

func TestCompletedSessionSwitchKeepsReplicatedRichCarrier(t *testing.T) {
	fixture := newFixture(t)
	actor := application.Principal{UserID: 7}
	firstRef := domain.SessionRef{NodeID: "allowed", SessionID: "live"}
	second := domain.Session{
		ID: "completed-second", NodeID: "allowed", OwnerID: 7, Name: "Completed Second",
		Backend: "codex", State: domain.SessionActive, RuntimePhase: domain.RuntimeIdle,
		CreatedAt: time.Unix(300, 0).UTC(), LiveSinceAt: time.Unix(300, 0).UTC(),
	}
	if result := fixture.machine.Apply(commandForTest(
		t, "add-completed-second", clusterstate.CommandAddSession, second,
	)); result.Err() != nil {
		t.Fatal(result.Err())
	}
	first, err := fixture.service.Session(actor, firstRef)
	if err != nil {
		t.Fatal(err)
	}
	if err := fixture.service.RecordTelegramResponseCard(
		application.WithOperationScope(context.Background(), "completed-rich-card"), actor,
		domain.TelegramResponseCard{
			ChatID: 7, MessageID: 90, Rich: true, Session: firstRef,
			SessionRevision: first.Revision, SessionEventAt: first.LastEventAt,
		},
	); err != nil {
		t.Fatal(err)
	}
	controls := &blockingControls{events: []transcript.Event{{
		Kind: transcript.EventAssistantFinal, Text: "completed answer",
		Timestamp: time.Now().Add(-time.Minute).Format(time.RFC3339Nano),
	}}}
	handler, err := telegramapp.NewHandlerWithControls(
		fixture.service, fixture.projector, fixture.codec, fixture.messenger, controls,
	)
	if err != nil {
		t.Fatal(err)
	}
	for index, ref := range []domain.SessionRef{second.Ref(), firstRef, second.Ref(), firstRef} {
		token, tokenErr := fixture.codec.Session(7, telegramui.ActionSelectSession, ref)
		if tokenErr != nil {
			t.Fatal(tokenErr)
		}
		data, encodeErr := (telegramui.Callback{Action: telegramui.ActionSelectSession, Token: token}).Encode()
		if encodeErr != nil {
			t.Fatal(encodeErr)
		}
		if handleErr := handler.HandleTelegramUpdate(context.Background(), telegrambot.IncomingUpdate{
			UpdateID: int64(200 + index), Kind: telegrambot.IncomingCallback,
			ChatID: 7, UserID: 7, CallbackID: fmt.Sprintf("switch-%d", index),
			CallbackData:   data,
			CallbackOrigin: telegrambot.Message{ChatID: 7, MessageID: 90},
		}); handleErr != nil {
			t.Fatal(handleErr)
		}
	}
	if len(fixture.messenger.sent) != 0 || len(fixture.messenger.deleted) != 0 {
		t.Fatalf("completed switches recreated carrier: sent=%#v deleted=%#v", fixture.messenger.sent, fixture.messenger.deleted)
	}
	if len(fixture.messenger.edited) != 4 {
		t.Fatalf("completed switches edits = %d", len(fixture.messenger.edited))
	}
	card, ok, cardErr := fixture.service.TelegramResponseCard(actor)
	if cardErr != nil || !ok || card.MessageID != 90 || !card.Rich || card.Session != firstRef {
		t.Fatalf("stable rich carrier = %#v / %v / %v", card, ok, cardErr)
	}
}

func TestSessionSwitchRestoresEachSessionsPageAndFollowMode(t *testing.T) {
	fixture := newFixture(t)
	actor := application.Principal{UserID: 7}
	firstRef := domain.SessionRef{NodeID: "allowed", SessionID: "live"}
	second := domain.Session{
		ID: "page-second", NodeID: "allowed", OwnerID: 7, Name: "Page Second",
		Backend: "codex", State: domain.SessionActive, RuntimePhase: domain.RuntimeIdle,
		CreatedAt: time.Unix(320, 0).UTC(), LiveSinceAt: time.Unix(320, 0).UTC(),
	}
	if result := fixture.machine.Apply(commandForTest(
		t, "add-page-second", clusterstate.CommandAddSession, second,
	)); result.Err() != nil {
		t.Fatal(result.Err())
	}
	finalAt := time.Now().Add(-time.Second).UTC()
	controls := &blockingControls{events: []transcript.Event{
		{Kind: transcript.EventAssistantFinal, Text: "Answer one", Timestamp: finalAt.Add(-2 * time.Second).Format(time.RFC3339Nano)},
		{Kind: transcript.EventAssistantFinal, Text: "Answer two", Timestamp: finalAt.Add(-time.Second).Format(time.RFC3339Nano)},
		{Kind: transcript.EventAssistantFinal, Text: "Answer three", Timestamp: finalAt.Format(time.RFC3339Nano)},
	}}
	handler, err := telegramapp.NewHandlerWithControls(
		fixture.service, fixture.projector, fixture.codec, fixture.messenger, controls,
	)
	if err != nil {
		t.Fatal(err)
	}
	origin := telegrambot.Message{ChatID: 7, MessageID: 90, Rich: true}
	updateID := int64(300)
	selectSession := func(ref domain.SessionRef) telegramui.Screen {
		t.Helper()
		token, tokenErr := fixture.codec.Session(7, telegramui.ActionSelectSession, ref)
		if tokenErr != nil {
			t.Fatal(tokenErr)
		}
		data, encodeErr := (telegramui.Callback{Action: telegramui.ActionSelectSession, Token: token}).Encode()
		if encodeErr != nil {
			t.Fatal(encodeErr)
		}
		updateID++
		if handleErr := handler.HandleTelegramUpdate(context.Background(), telegrambot.IncomingUpdate{
			UpdateID: updateID, Kind: telegrambot.IncomingCallback, ChatID: 7, UserID: 7,
			CallbackID: fmt.Sprintf("select-%d", updateID), CallbackData: data, CallbackOrigin: origin,
		}); handleErr != nil {
			t.Fatal(handleErr)
		}
		return fixture.messenger.edited[len(fixture.messenger.edited)-1]
	}
	pagePrevious := func(screen telegramui.Screen) telegramui.Screen {
		t.Helper()
		data, encodeErr := screen.Grid[0][0].Callback.Encode()
		if encodeErr != nil {
			t.Fatal(encodeErr)
		}
		updateID++
		if handleErr := handler.HandleTelegramUpdate(context.Background(), telegrambot.IncomingUpdate{
			UpdateID: updateID, Kind: telegrambot.IncomingCallback, ChatID: 7, UserID: 7,
			CallbackID: fmt.Sprintf("previous-%d", updateID), CallbackData: data, CallbackOrigin: origin,
		}); handleErr != nil {
			t.Fatal(handleErr)
		}
		return fixture.messenger.edited[len(fixture.messenger.edited)-1]
	}

	first := selectSession(firstRef)
	if first.Grid[0][1].Label != "3/3" {
		t.Fatalf("first initial page = %s", first.Grid[0][1].Label)
	}
	first = pagePrevious(first)
	if first.Grid[0][1].Label != "2/3" {
		t.Fatalf("first pinned page = %s", first.Grid[0][1].Label)
	}
	// A process restart clears Handler's in-memory page map. The replicated
	// response-card metadata must restore the historical page before rendering,
	// otherwise a background refresh silently jumps back to the latest page.
	handler, err = telegramapp.NewHandlerWithControls(
		fixture.service, fixture.projector, fixture.codec, fixture.messenger, controls,
	)
	if err != nil {
		t.Fatal(err)
	}
	if restored := selectSession(firstRef); restored.Grid[0][1].Label != "2/3" {
		t.Fatalf("first page after handler restart = %s", restored.Grid[0][1].Label)
	}
	secondScreen := selectSession(second.Ref())
	secondScreen = pagePrevious(secondScreen)
	secondScreen = pagePrevious(secondScreen)
	if secondScreen.Grid[0][1].Label != "1/3" {
		t.Fatalf("second pinned page = %s", secondScreen.Grid[0][1].Label)
	}
	if restored := selectSession(firstRef); restored.Grid[0][1].Label != "2/3" {
		t.Fatalf("first restored page = %s", restored.Grid[0][1].Label)
	}
	if restored := selectSession(second.Ref()); restored.Grid[0][1].Label != "1/3" {
		t.Fatalf("second restored page = %s", restored.Grid[0][1].Label)
	}
	card, ok, cardErr := fixture.service.TelegramResponseCard(actor)
	if cardErr != nil || !ok || card.Session != second.Ref() || !card.RenderedFinalAt.Equal(finalAt) {
		t.Fatalf("restored completed session lost final delivery: %#v / %v / %v", card, ok, cardErr)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	handler.RunBackgroundNotifications(ctx, 5*time.Millisecond)
	if len(fixture.messenger.sent) != 0 {
		t.Fatalf("restored historical page reposted final: %#v", fixture.messenger.sent)
	}
	latestData, err := secondScreen.Grid[0][1].Callback.Encode()
	if err != nil {
		t.Fatal(err)
	}
	updateID++
	if err := handler.HandleTelegramUpdate(context.Background(), telegrambot.IncomingUpdate{
		UpdateID: updateID, Kind: telegrambot.IncomingCallback, ChatID: 7, UserID: 7,
		CallbackID: "latest-second", CallbackData: latestData, CallbackOrigin: origin,
	}); err != nil {
		t.Fatal(err)
	}
	controls.appendTranscriptEvent(transcript.Event{Kind: transcript.EventAssistantFinal, Text: "Answer four"})
	selectSession(firstRef)
	if followed := selectSession(second.Ref()); followed.Grid[0][1].Label != "4/4" {
		t.Fatalf("second follow page = %s", followed.Grid[0][1].Label)
	}
}
