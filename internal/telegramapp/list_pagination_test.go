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
	if len(fixture.messenger.edited) != 0 || len(fixture.messenger.sent) != 1 {
		t.Fatalf("edits=%d sends=%d", len(fixture.messenger.edited), len(fixture.messenger.sent))
	}
	if !fixture.messenger.sent[0].RichMarkdown {
		t.Fatalf("replacement is not rich: %#v", fixture.messenger.sent[0])
	}
	if len(fixture.messenger.deleted) != 1 || fixture.messenger.deleted[0].MessageID != 50 {
		t.Fatalf("obsolete carrier=%#v", fixture.messenger.deleted)
	}
	if size := len(fixture.messenger.sent[0].Text); size > telegrambot.MaxMessageTextBytes {
		t.Fatalf("session page has %d encoded bytes", size)
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
