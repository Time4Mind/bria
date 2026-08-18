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
	wantPage := strings.SplitN(want, "/", 2)[0]
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
	turnAt := time.Now()
	controls.appendTranscriptEvent(transcript.Event{
		Kind: transcript.EventUserText, Text: "keep my page",
		Timestamp: turnAt.Format(time.RFC3339Nano),
	})
	controls.appendTranscriptEvent(transcript.Event{
		Kind:      transcript.EventAssistantText,
		Text:      "new page " + strings.Repeat("later content ", 400),
		Timestamp: turnAt.Add(time.Millisecond).Format(time.RFC3339Nano),
	})
	previousTotal := strings.SplitN(want, "/", 2)[1]
	grew := false
	deadline = time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		_, edited, _ = fixture.messenger.screensSnapshot()
		latestLabel := edited[len(edited)-1].Grid[0][1].Label
		parts := strings.SplitN(latestLabel, "/", 2)
		if len(parts) == 2 && parts[1] != previousTotal {
			grew = true
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if !grew {
		t.Fatalf("pinned card did not observe transcript growth: edits=%#v", edited)
	}
	sent, _, _ = fixture.messenger.screensSnapshot()
	if len(sent) != 1 {
		t.Fatalf("pinned running page created a carrier before final: sent=%d", len(sent))
	}
	controls.appendTranscriptEvent(transcript.Event{
		Kind: transcript.EventAssistantFinal, Text: "PINNED FINAL ANSWER",
		Timestamp: time.Now().Add(time.Millisecond).Format(time.RFC3339Nano),
	})
	deadline = time.Now().Add(3 * time.Second)
	for len(sent) < 2 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
		sent, _, _ = fixture.messenger.screensSnapshot()
	}
	cancel()
	time.Sleep(50 * time.Millisecond)
	_, edited, _ = fixture.messenger.screensSnapshot()
	if len(sent) < 2 || !strings.Contains(sent[len(sent)-1].Text, "PINNED FINAL ANSWER") {
		t.Fatalf("pinned page did not publish final carrier after completion: sent=%#v", sent)
	}
	for index, screen := range edited[workerStart:] {
		got := screen.Grid[0][1].Label
		if strings.SplitN(got, "/", 2)[0] != wantPage {
			t.Fatalf("worker edit %d reset page to %s, want page %s", index, got, wantPage)
		}
	}
}

func TestRunningCardLatestPageFollowsTranscriptGrowth(t *testing.T) {
	fixture := newFixture(t)
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
	controls := &blockingControls{ref: ref, events: []transcript.Event{{
		Kind: transcript.EventAssistantText,
		Text: "initial " + strings.Repeat("content ", 600),
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
		UpdateID: 94, Kind: telegrambot.IncomingMessage,
		ChatID: 7, UserID: 7, Text: "follow latest",
	}); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(2 * time.Second)
	var edited []telegramui.Screen
	for len(edited) == 0 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
		_, edited, _ = fixture.messenger.screensSnapshot()
	}
	if len(edited) == 0 {
		t.Fatal("latest page was not refreshed")
	}
	before := edited[len(edited)-1].Grid[0][1].Label
	beforeParts := strings.SplitN(before, "/", 2)
	if len(beforeParts) != 2 || beforeParts[0] != beforeParts[1] {
		t.Fatalf("card did not start in follow mode: %s", before)
	}
	controls.appendTranscriptEvent(transcript.Event{
		Kind: transcript.EventAssistantText,
		Text: "growth " + strings.Repeat("new content ", 600),
	})
	deadline = time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		_, edited, _ = fixture.messenger.screensSnapshot()
		label := edited[len(edited)-1].Grid[0][1].Label
		parts := strings.SplitN(label, "/", 2)
		if len(parts) == 2 && parts[1] != beforeParts[1] && parts[0] == parts[1] {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("latest card did not follow transcript growth: before=%s edits=%#v", before, edited)
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
