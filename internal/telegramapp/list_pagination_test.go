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

func TestMultibyteRichSessionPageReplacesLegacyCarrier(t *testing.T) {
	fixture := newFixture(t)
	fixture.messenger.discardEventTrace()
	ref := domain.SessionRef{NodeID: "allowed", SessionID: "live"}
	controls := &blockingControls{ref: ref, events: []transcript.Event{{
		Kind: transcript.EventAssistantFinal,
		Text: strings.Repeat("длинный русский ответ ", 400),
	}}}
	handler, err := telegramapp.NewHandlerWithControls(
		fixture.service, fixture.projector, fixture.codec, fixture.messenger, controls,
	)
	if err != nil {
		t.Fatal(err)
	}
	token, err := fixture.codec.Page(7, telegramui.ActionPagePrevious, ref, 1)
	if err != nil {
		t.Fatal(err)
	}
	data, err := (telegramui.Callback{
		Action: telegramui.ActionPagePrevious, Token: token,
	}).Encode()
	if err != nil {
		t.Fatal(err)
	}
	if err := handler.HandleTelegramUpdate(context.Background(), telegrambot.IncomingUpdate{
		UpdateID: 91, Kind: telegrambot.IncomingCallback, ChatID: 7, UserID: 7,
		CallbackID: "previous", CallbackData: data,
		CallbackOrigin: telegrambot.Message{ChatID: 7, MessageID: 50},
	}); err != nil {
		t.Fatal(err)
	}
	sent, edited, deleted := fixture.messenger.screensSnapshot()
	if len(edited) != 0 || len(sent) != 1 {
		t.Fatalf("edits=%d sends=%d", len(edited), len(sent))
	}
	if !sent[0].RichMarkdown {
		t.Fatalf("replacement is not rich: %#v", sent[0])
	}
	if len(deleted) != 1 || deleted[0].MessageID != 50 {
		t.Fatalf("obsolete carrier=%#v", deleted)
	}
	if size := len(sent[0].Text); size > telegrambot.MaxMessageTextBytes {
		t.Fatalf("session page has %d encoded bytes", size)
	}
}

func TestRunningCardRefreshKeepsTheExplicitlySelectedPage(t *testing.T) {
	fixture := newFixture(t)
	fixture.messenger.discardEventTrace()
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
	events := make([]transcript.Event, 0, 16)
	for index := 0; index < 8; index++ {
		events = append(events, transcript.Event{
			Kind: transcript.EventUserText,
			Text: fmt.Sprintf("request %d %s", index, strings.Repeat("question ", 300)),
		})
		events = append(events, transcript.Event{
			Kind: transcript.EventAssistantText,
			Text: fmt.Sprintf("answer %d %s", index, strings.Repeat("content ", 300)),
		})
	}
	controls := &blockingControls{ref: ref, events: events}
	handler, err := telegramapp.NewHandlerWithControls(
		fixture.service, fixture.projector, fixture.codec, fixture.messenger, controls,
	)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := handler.HandleTelegramUpdate(ctx, telegrambot.IncomingUpdate{
		UpdateID: 92, Kind: telegrambot.IncomingMessage,
		ChatID: 7, UserID: 7, Text: "keep my page",
	}); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(2 * time.Second)
	var sent []telegramui.Screen
	for len(sent) < 1 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
		sent, _, _ = fixture.messenger.screensSnapshot()
	}
	if len(sent) < 1 {
		t.Fatalf("running cards=%d", len(sent))
	}
	initial := sent[len(sent)-1]
	if len(initial.Grid) == 0 || len(initial.Grid[0]) < 2 ||
		initial.Grid[0][1].Label == "1/1" {
		t.Fatalf("test transcript did not paginate: %#v", initial.Grid)
	}
	previous := callbackForAction(t, initial, telegramui.ActionPagePrevious)
	origin := stubMessageForScreen(len(sent), initial)
	if err := handler.HandleTelegramUpdate(ctx, telegrambot.IncomingUpdate{
		UpdateID: 93, Kind: telegrambot.IncomingCallback,
		ChatID: 7, UserID: 7, CallbackID: "previous-running",
		CallbackData: previous, CallbackOrigin: origin,
	}); err != nil {
		t.Fatal(err)
	}
	_, edited, _ := fixture.messenger.screensSnapshot()
	if len(edited) == 0 {
		t.Fatal("manual page was not rendered")
	}
	manual := edited[len(edited)-1]
	want := manual.Grid[0][1].Label
	if want == initial.Grid[0][1].Label {
		t.Fatalf("previous page did not move: %s", want)
	}
	workerStart := len(edited)
	deadline = time.Now().Add(2 * time.Second)
	for len(edited) <= workerStart && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
		_, edited, _ = fixture.messenger.screensSnapshot()
	}
	if len(edited) <= workerStart {
		t.Fatal("running card worker did not refresh the selected page")
	}
	cancel()
	time.Sleep(50 * time.Millisecond)
	_, edited, _ = fixture.messenger.screensSnapshot()
	for index, screen := range edited[workerStart:] {
		if got := screen.Grid[0][1].Label; got != want {
			t.Fatalf("worker edit %d reset page to %s, want %s", index, got, want)
		}
	}
}

func TestNodePaginationCallbackEditsSameCarrier(t *testing.T) {
	fixture := newFixture(t)
	closed := fixture.machine.Apply(commandForTest(t, "close-live", clusterstate.CommandCloseSession,
		clusterstate.SessionRevision{ActorID: 7,
			Session:          domain.SessionRef{NodeID: "allowed", SessionID: "live"},
			ExpectedRevision: 1, ArchiveCommitID: "test"}))
	if closed.Err() != nil {
		t.Fatal(closed.Err())
	}
	for index := 0; index < 8; index++ {
		nodeID := domain.NodeID(fmt.Sprintf("page-node-%02d", index))
		result := fixture.machine.Apply(commandForTest(t, "add-"+string(nodeID),
			clusterstate.CommandAddNode, domain.Node{
				ID: nodeID, Name: string(nodeID), Status: domain.NodeOnline,
				CreatedAt: time.Unix(int64(600+index), 0),
			}))
		if result.Err() != nil {
			t.Fatal(result.Err())
		}
	}
	// The dedicated server selector remains independently pageable even though
	// the Sessions -> Servers route now opens the full status screen.
	origin := telegrambot.Message{ChatID: 7, MessageID: 77}
	first, err := fixture.projector.OpenNodeSelector(application.Principal{UserID: 7})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(telegramui.CanonicalGrid(first.Grid), "nodes_next") {
		t.Fatalf("first page=%#v", first)
	}
	next := callbackForAction(t, first, telegramui.ActionNodesNext)
	invokeListCallback(t, fixture, 301, next, origin)
	second := fixture.messenger.edited[len(fixture.messenger.edited)-1]
	grid := telegramui.CanonicalGrid(second.Grid)
	if !strings.Contains(grid, "nodes_prev") || !strings.Contains(grid, "nodes_next") {
		t.Fatalf("second page=%s", grid)
	}
	if len(fixture.messenger.sent) != 0 || len(fixture.messenger.edited) != 1 {
		t.Fatalf("sent=%d edited=%d", len(fixture.messenger.sent), len(fixture.messenger.edited))
	}
}

func TestListPaginationRejectsTokenFromAnotherAction(t *testing.T) {
	fixture := newFixture(t)
	token, err := fixture.codec.Choice(7, telegramui.ActionNodesNext, "nodes-page", "2")
	if err != nil {
		t.Fatal(err)
	}
	data := encodeCallback(t, telegramui.ActionSessionsNext, token)
	invokeListCallback(t, fixture, 303, data, telegrambot.Message{ChatID: 7, MessageID: 77})
	if len(fixture.messenger.edited) != 0 {
		t.Fatalf("cross-action token edited screen=%#v", fixture.messenger.edited)
	}
}

func invokeListCallback(
	t *testing.T,
	fixture fixture,
	updateID int64,
	data string,
	origin telegrambot.Message,
) {
	t.Helper()
	if err := fixture.handler.HandleTelegramUpdate(context.Background(), telegrambot.IncomingUpdate{
		UpdateID: updateID, Kind: telegrambot.IncomingCallback,
		UserID: 7, ChatID: 7, CallbackID: fmt.Sprintf("page-%d", updateID),
		CallbackData: data, CallbackOrigin: origin,
	}); err != nil {
		t.Fatal(err)
	}
}
