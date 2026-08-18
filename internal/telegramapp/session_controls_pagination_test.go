package telegramapp_test

import (
	"context"
	"fmt"
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

func TestSelectingSettledStaleRunningSessionDoesNotRepostFinal(t *testing.T) {
	fixture := newFixture(t)
	actor := application.Principal{UserID: 7}
	finalAt := time.Now().Add(-time.Second).UTC()
	stale := domain.Session{
		ID: "stale-final", NodeID: "allowed", OwnerID: 7, Name: "Stale Final",
		Backend: "codex", State: domain.SessionActive, RuntimePhase: domain.RuntimeRunning,
		CreatedAt: finalAt.Add(-time.Second), LiveSinceAt: finalAt.Add(-time.Second), LastEventAt: finalAt.Add(-time.Second),
	}
	if result := fixture.machine.Apply(commandForTest(t, "add-stale-final", clusterstate.CommandAddSession, stale)); result.Err() != nil {
		t.Fatal(result.Err())
	}
	controls := &blockingControls{events: []transcript.Event{{Kind: transcript.EventAssistantFinal, Text: "Already delivered", Timestamp: finalAt.Format(time.RFC3339Nano)}}}
	handler, err := telegramapp.NewHandlerWithControls(fixture.service, fixture.projector, fixture.codec, fixture.messenger, controls)
	if err != nil {
		t.Fatal(err)
	}
	token, err := fixture.codec.Session(7, telegramui.ActionSelectSession, stale.Ref())
	if err != nil {
		t.Fatal(err)
	}
	data, err := (telegramui.Callback{Action: telegramui.ActionSelectSession, Token: token}).Encode()
	if err != nil {
		t.Fatal(err)
	}
	if err := handler.HandleTelegramUpdate(context.Background(), telegrambot.IncomingUpdate{
		UpdateID: 401, Kind: telegrambot.IncomingCallback, ChatID: 7, UserID: 7,
		CallbackID: "select-stale-final", CallbackData: data,
		CallbackOrigin: telegrambot.Message{ChatID: 7, MessageID: 150, Rich: true},
	}); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		session, sessionErr := fixture.service.Session(actor, stale.Ref())
		if sessionErr != nil {
			t.Fatal(sessionErr)
		}
		if session.RuntimePhase == domain.RuntimeIdle {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	time.Sleep(50 * time.Millisecond)
	if len(fixture.messenger.sent) != 0 {
		t.Fatalf("selecting delivered stale final reposted carrier: %#v", fixture.messenger.sent)
	}
	card, ok, cardErr := fixture.service.TelegramResponseCard(actor)
	if cardErr != nil || !ok || card.MessageID != 150 || card.Session != stale.Ref() || !card.RenderedFinalAt.Equal(finalAt) {
		t.Fatalf("stable selected carrier = %#v / %v / %v", card, ok, cardErr)
	}
}

func TestTranscriptPageButtonsResolveOpaqueTargetPage(t *testing.T) {
	fixture := newFixture(t)
	ref := domain.SessionRef{NodeID: "allowed", SessionID: "live"}
	controls := &blockingControls{events: []transcript.Event{{Kind: transcript.EventAssistantFinal, Text: "First answer"}, {Kind: transcript.EventAssistantFinal, Text: "Second answer"}}}
	handler, err := telegramapp.NewHandlerWithControls(fixture.service, fixture.projector, fixture.codec, fixture.messenger, controls)
	if err != nil {
		t.Fatal(err)
	}
	selectToken, err := fixture.codec.Session(7, telegramui.ActionSelectSession, ref)
	if err != nil {
		t.Fatal(err)
	}
	selectData, err := (telegramui.Callback{Action: telegramui.ActionSelectSession, Token: selectToken}).Encode()
	if err != nil {
		t.Fatal(err)
	}
	if err := handler.HandleTelegramUpdate(context.Background(), telegrambot.IncomingUpdate{
		UpdateID: 90, Kind: telegrambot.IncomingCallback, ChatID: 7, UserID: 7,
		CallbackID: "select", CallbackData: selectData, CallbackOrigin: telegrambot.Message{ChatID: 7, MessageID: 10},
	}); err != nil {
		t.Fatal(err)
	}
	latest := fixture.messenger.sent[len(fixture.messenger.sent)-1]
	if !strings.Contains(latest.Text, "Second answer") || strings.Contains(latest.Text, "First answer") || !strings.Contains(telegramui.CanonicalGrid(latest.Grid), "2/2") {
		t.Fatalf("latest page=%#v", latest)
	}
	previousData, err := latest.Grid[0][0].Callback.Encode()
	if err != nil {
		t.Fatal(err)
	}
	if err := handler.HandleTelegramUpdate(context.Background(), telegrambot.IncomingUpdate{
		UpdateID: 91, Kind: telegrambot.IncomingCallback, ChatID: 7, UserID: 7,
		CallbackID: "previous", CallbackData: previousData, CallbackOrigin: telegrambot.Message{ChatID: 7, MessageID: 10, Rich: true},
	}); err != nil {
		t.Fatal(err)
	}
	first := fixture.messenger.edited[len(fixture.messenger.edited)-1]
	if !strings.Contains(first.Text, "First answer") || strings.Contains(first.Text, "Second answer") || !strings.Contains(telegramui.CanonicalGrid(first.Grid), "1/2") {
		t.Fatalf("first page=%#v", first)
	}
	wrappedData, err := first.Grid[0][0].Callback.Encode()
	if err != nil {
		t.Fatal(err)
	}
	if err := handler.HandleTelegramUpdate(context.Background(), telegrambot.IncomingUpdate{
		UpdateID: 92, Kind: telegrambot.IncomingCallback, ChatID: 7, UserID: 7,
		CallbackID: "wrapped-previous", CallbackData: wrappedData, CallbackOrigin: telegrambot.Message{ChatID: 7, MessageID: 10, Rich: true},
	}); err != nil {
		t.Fatal(err)
	}
	last := fixture.messenger.edited[len(fixture.messenger.edited)-1]
	if !strings.Contains(last.Text, "Second answer") || !strings.Contains(telegramui.CanonicalGrid(last.Grid), "2/2") {
		t.Fatalf("wrapped last page=%#v", last)
	}
}

func TestStaleLatestButtonRestoresFollowAtCurrentLatestPage(t *testing.T) {
	fixture := newFixture(t)
	ref := domain.SessionRef{NodeID: "allowed", SessionID: "live"}
	controls := &blockingControls{events: []transcript.Event{{Kind: transcript.EventAssistantFinal, Text: "First answer"}, {Kind: transcript.EventAssistantFinal, Text: "Second answer"}}}
	handler, err := telegramapp.NewHandlerWithControls(fixture.service, fixture.projector, fixture.codec, fixture.messenger, controls)
	if err != nil {
		t.Fatal(err)
	}
	selectToken, err := fixture.codec.Session(7, telegramui.ActionSelectSession, ref)
	if err != nil {
		t.Fatal(err)
	}
	selectData, err := (telegramui.Callback{Action: telegramui.ActionSelectSession, Token: selectToken}).Encode()
	if err != nil {
		t.Fatal(err)
	}
	origin := telegrambot.Message{ChatID: 7, MessageID: 10, Rich: true}
	if err := handler.HandleTelegramUpdate(context.Background(), telegrambot.IncomingUpdate{
		UpdateID: 93, Kind: telegrambot.IncomingCallback, ChatID: 7, UserID: 7,
		CallbackID: "select", CallbackData: selectData, CallbackOrigin: origin,
	}); err != nil {
		t.Fatal(err)
	}
	current := fixture.messenger.edited[len(fixture.messenger.edited)-1]
	staleLatest, err := current.Grid[0][1].Callback.Encode()
	if err != nil {
		t.Fatal(err)
	}
	controls.appendTranscriptEvent(transcript.Event{Kind: transcript.EventAssistantFinal, Text: "Third answer"})
	if err := handler.HandleTelegramUpdate(context.Background(), telegrambot.IncomingUpdate{
		UpdateID: 94, Kind: telegrambot.IncomingCallback, ChatID: 7, UserID: 7,
		CallbackID: "latest", CallbackData: staleLatest, CallbackOrigin: origin,
	}); err != nil {
		t.Fatal(err)
	}
	latest := fixture.messenger.edited[len(fixture.messenger.edited)-1]
	if !strings.Contains(latest.Text, "Third answer") || latest.Grid[0][1].Label != "3/3" {
		t.Fatalf("stale latest button did not reach current latest: %#v", latest)
	}
}

func TestRepeatedStalePreviousCallbackAccumulatesPageMoves(t *testing.T) {
	fixture := newFixture(t)
	ref := domain.SessionRef{NodeID: "allowed", SessionID: "live"}
	events := make([]transcript.Event, 0, 9)
	for page := 1; page <= 9; page++ {
		events = append(events, transcript.Event{Kind: transcript.EventAssistantFinal, Text: fmt.Sprintf("Answer %d", page)})
	}
	controls := &blockingControls{events: events}
	handler, err := telegramapp.NewHandlerWithControls(fixture.service, fixture.projector, fixture.codec, fixture.messenger, controls)
	if err != nil {
		t.Fatal(err)
	}
	selectToken, err := fixture.codec.Session(7, telegramui.ActionSelectSession, ref)
	if err != nil {
		t.Fatal(err)
	}
	selectData, err := (telegramui.Callback{Action: telegramui.ActionSelectSession, Token: selectToken}).Encode()
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
			CallbackID: fmt.Sprintf("previous-%d", updateID), CallbackData: stalePrevious, CallbackOrigin: origin,
		}); err != nil {
			t.Fatal(err)
		}
	}
	pageEight := fixture.messenger.edited[len(fixture.messenger.edited)-2]
	pageSeven := fixture.messenger.edited[len(fixture.messenger.edited)-1]
	if !strings.Contains(telegramui.CanonicalGrid(pageEight.Grid), "8/9") || !strings.Contains(telegramui.CanonicalGrid(pageSeven.Grid), "7/9") {
		t.Fatalf("repeated stale callback pages=%q then %q", telegramui.CanonicalGrid(pageEight.Grid), telegramui.CanonicalGrid(pageSeven.Grid))
	}
}
