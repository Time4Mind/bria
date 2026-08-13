package telegramapp_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/Time4Mind/bria/internal/clusterstate"
	"github.com/Time4Mind/bria/internal/domain"
	"github.com/Time4Mind/bria/internal/telegrambot"
	"github.com/Time4Mind/bria/internal/telegramui"
)

func TestNodeWithoutLiveSessionKeepsSessionListFallback(t *testing.T) {
	fixture := newFixture(t)
	command, err := clusterstate.NewCommand(
		"allow-empty-node", clusterstate.CommandSetNodeAccess, time.Unix(200, 0),
		clusterstate.SetNodeAccess{
			UserID: 7, Role: domain.RoleOwner, NodeIDs: []domain.NodeID{"allowed", "hidden"},
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	if result := fixture.machine.Apply(command); result.Err() != nil {
		t.Fatal(result.Err())
	}
	token, err := fixture.codec.Node(7, telegramui.ActionSelectNode, "hidden")
	if err != nil {
		t.Fatal(err)
	}
	data, err := (telegramui.Callback{
		Action: telegramui.ActionSelectNode, Token: token,
	}).Encode()
	if err != nil {
		t.Fatal(err)
	}
	if err := fixture.handler.HandleTelegramUpdate(context.Background(), telegrambot.IncomingUpdate{
		UpdateID: 30, Kind: telegrambot.IncomingCallback,
		ChatID: 7, UserID: 7, CallbackID: "empty-node", CallbackData: data,
		CallbackOrigin: telegrambot.Message{ChatID: 7, MessageID: 10},
	}); err != nil {
		t.Fatal(err)
	}
	if len(fixture.messenger.edited) != 1 ||
		fixture.messenger.edited[0].Name != telegramui.ScreenSessions ||
		!strings.Contains(fixture.messenger.edited[0].Text, "Hidden") {
		t.Fatalf("fallback=%#v", fixture.messenger.edited)
	}
}

func TestLegacyNodeSessionButtonsPreserveTheirNavigationContext(t *testing.T) {
	fixture := newFixture(t)
	origin := telegrambot.Message{
		ChatID: 7, MessageID: 11, Text: "Allowed · active sessions",
	}
	servers := encodeCallback(t, telegramui.ActionSessions, "")
	if err := fixture.handler.HandleTelegramUpdate(context.Background(), telegrambot.IncomingUpdate{
		UpdateID: 31, Kind: telegrambot.IncomingCallback, ChatID: 7, UserID: 7,
		CallbackID: "legacy-servers", CallbackData: servers, CallbackOrigin: origin,
	}); err != nil {
		t.Fatal(err)
	}
	if got := fixture.messenger.edited[len(fixture.messenger.edited)-1]; got.Name != telegramui.ScreenNodes {
		t.Fatalf("legacy servers callback opened %#v", got)
	}

	settingsToken, err := fixture.codec.Node(
		7, telegramui.ActionStatusSettingsNode, "allowed",
	)
	if err != nil {
		t.Fatal(err)
	}
	settings := encodeCallback(t, telegramui.ActionStatusSettingsNode, settingsToken)
	if err := fixture.handler.HandleTelegramUpdate(context.Background(), telegrambot.IncomingUpdate{
		UpdateID: 32, Kind: telegrambot.IncomingCallback, ChatID: 7, UserID: 7,
		CallbackID: "legacy-settings", CallbackData: settings, CallbackOrigin: origin,
	}); err != nil {
		t.Fatal(err)
	}
	got := fixture.messenger.edited[len(fixture.messenger.edited)-1]
	back := got.Grid[len(got.Grid)-1][0].Callback
	if back.Action != telegramui.ActionSelectNode || back.Token == "" {
		t.Fatalf("legacy settings back route=%#v", back)
	}
}

func TestServersBackReturnsToSessionsInsteadOfMenu(t *testing.T) {
	fixture := newFixture(t)
	servers := encodeCallback(t, telegramui.ActionSessions, "servers")
	if err := fixture.handler.HandleTelegramUpdate(context.Background(), telegrambot.IncomingUpdate{
		UpdateID: 33, Kind: telegrambot.IncomingCallback, ChatID: 7, UserID: 7,
		CallbackID: "servers", CallbackData: servers,
		CallbackOrigin: telegrambot.Message{ChatID: 7, MessageID: 12},
	}); err != nil {
		t.Fatal(err)
	}
	selector := fixture.messenger.edited[len(fixture.messenger.edited)-1]
	back := selector.Grid[len(selector.Grid)-1][0].Callback
	if back.Action != telegramui.ActionSessions || back.Token != "" {
		t.Fatalf("servers back route=%#v", back)
	}

	backData, err := back.Encode()
	if err != nil {
		t.Fatal(err)
	}
	if err := fixture.handler.HandleTelegramUpdate(context.Background(), telegrambot.IncomingUpdate{
		UpdateID: 34, Kind: telegrambot.IncomingCallback, ChatID: 7, UserID: 7,
		CallbackID: "back-sessions", CallbackData: backData,
		CallbackOrigin: telegrambot.Message{ChatID: 7, MessageID: 12},
	}); err != nil {
		t.Fatal(err)
	}
	got := fixture.messenger.edited[len(fixture.messenger.edited)-1]
	if got.Name == telegramui.ScreenMenu {
		t.Fatalf("servers back opened menu: %#v", got)
	}
}

func TestArchivedSessionSelectionIsAcknowledgedWithoutRetry(t *testing.T) {
	fixture := newFixture(t)
	actor := domain.UserID(7)
	archived := domain.Session{
		ID: "archived", NodeID: "allowed", OwnerID: actor, Name: "Archived",
		Backend: "codex", State: domain.SessionActive,
		CreatedAt: time.Unix(100, 0).UTC(), LiveSinceAt: time.Unix(100, 0).UTC(),
	}
	command, err := clusterstate.NewCommand(
		"add-archived-callback-target", clusterstate.CommandAddSession,
		time.Unix(100, 0).UTC(), archived,
	)
	if err != nil {
		t.Fatal(err)
	}
	if result := fixture.machine.Apply(command); result.Err() != nil {
		t.Fatal(result.Err())
	}
	archive, err := clusterstate.NewCommand(
		"archive-callback-target", clusterstate.CommandArchiveSession,
		time.Unix(200, 0).UTC(), clusterstate.ArchiveSession{
			Session: archived.Ref(), ExpectedRevision: 1, Reason: "resume_failed",
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	if result := fixture.machine.Apply(archive); result.Err() != nil {
		t.Fatal(result.Err())
	}
	token, err := fixture.codec.Session(
		actor, telegramui.ActionSelectSession, archived.Ref(),
	)
	if err != nil {
		t.Fatal(err)
	}
	data := encodeCallback(t, telegramui.ActionSelectSession, token)
	if err := fixture.handler.HandleTelegramUpdate(context.Background(), telegrambot.IncomingUpdate{
		UpdateID: 35, Kind: telegrambot.IncomingCallback, ChatID: 7, UserID: 7,
		CallbackID: "stale-session", CallbackData: data,
		CallbackOrigin: telegrambot.Message{ChatID: 7, MessageID: 13},
	}); err != nil {
		t.Fatalf("stale selection must be terminal, got %v", err)
	}
	if len(fixture.messenger.edited) != 0 {
		t.Fatalf("stale selection edited card: %#v", fixture.messenger.edited)
	}
	if len(fixture.messenger.answers) != 1 ||
		!strings.HasSuffix(fixture.messenger.answers[0], ":Unavailable") {
		t.Fatalf("answers=%#v", fixture.messenger.answers)
	}
}
