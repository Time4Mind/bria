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
)

func TestCloseLastNodeSessionKeepsThatNodesEmptySessionScreen(t *testing.T) {
	fixture := newFixture(t)
	actor := application.Principal{UserID: 7}
	if result := fixture.machine.Apply(commandForTest(
		t,
		"allow-second-node",
		clusterstate.CommandSetNodeAccess,
		clusterstate.SetNodeAccess{
			UserID: 7, Role: domain.RoleOwner,
			NodeIDs: []domain.NodeID{"allowed", "hidden"},
		},
	)); result.Err() != nil {
		t.Fatal(result.Err())
	}
	other := domain.Session{
		ID: "other", NodeID: "hidden", OwnerID: 7, Name: "Other", Backend: "codex",
		State: domain.SessionActive, RuntimePhase: domain.RuntimeIdle,
		CreatedAt: time.Unix(200, 0).UTC(), LiveSinceAt: time.Unix(200, 0).UTC(),
	}
	if result := fixture.machine.Apply(commandForTest(
		t, "add-other-node-session", clusterstate.CommandAddSession, other,
	)); result.Err() != nil {
		t.Fatal(result.Err())
	}
	closing, err := fixture.service.ActiveSession(actor)
	if err != nil || closing.NodeID != "allowed" {
		t.Fatalf("closing session=%#v/%v", closing, err)
	}
	controls := &closingControls{
		blockingControls: &blockingControls{ref: closing.Ref()}, service: fixture.service,
	}
	handler, err := telegramapp.NewHandlerWithControls(
		fixture.service, fixture.projector, fixture.codec, fixture.messenger, controls,
	)
	if err != nil {
		t.Fatal(err)
	}
	token, err := fixture.codec.Session(7, telegramui.ActionConfirmClose, closing.Ref())
	if err != nil {
		t.Fatal(err)
	}
	data, err := (telegramui.Callback{
		Action: telegramui.ActionConfirmClose, Token: token,
	}).Encode()
	if err != nil {
		t.Fatal(err)
	}
	if err := handler.HandleTelegramUpdate(context.Background(), telegrambot.IncomingUpdate{
		UpdateID: 72, Kind: telegrambot.IncomingCallback, ChatID: 7, UserID: 7,
		CallbackID: "close-last", CallbackData: data,
		CallbackOrigin: telegrambot.Message{ChatID: 7, MessageID: 10},
	}); err != nil {
		t.Fatal(err)
	}
	if len(fixture.messenger.edited) != 1 {
		t.Fatalf("edits=%#v", fixture.messenger.edited)
	}
	screen := fixture.messenger.edited[0]
	if screen.Name != telegramui.ScreenSessions || !strings.Contains(screen.Text, "Allowed") ||
		strings.Contains(screen.Text, "Other") {
		t.Fatalf("close target=%#v", screen)
	}
	lastRow := screen.Grid[len(screen.Grid)-1]
	if len(lastRow) != 3 || lastRow[0].Callback.Action != telegramui.ActionNewSession ||
		lastRow[1].Callback.Action != telegramui.ActionSessions ||
		lastRow[2].Callback.Action != telegramui.ActionMenu {
		t.Fatalf("empty node actions=%#v", lastRow)
	}
	state := fixture.machine.State()
	if state.Navigation.ActiveNodeByUser[7] != "allowed" ||
		state.Navigation.ActiveSessionByUserNode[7]["allowed"] != "" {
		t.Fatalf("navigation=%#v", state.Navigation)
	}
}

func TestCloseLastNodeSessionReturnsToAllHostsGridWhenConfigured(t *testing.T) {
	fixture := newFixture(t)
	actor := application.Principal{UserID: 7}
	if result := fixture.machine.Apply(commandForTest(
		t,
		"allow-second-node",
		clusterstate.CommandSetNodeAccess,
		clusterstate.SetNodeAccess{
			UserID: 7, Role: domain.RoleOwner,
			NodeIDs: []domain.NodeID{"allowed", "hidden"},
		},
	)); result.Err() != nil {
		t.Fatal(result.Err())
	}
	preferences := fixture.machine.State().Preferences[7]
	preferences.SessionView = domain.ViewAllHosts
	if result := fixture.machine.Apply(commandForTest(
		t, "all-hosts", clusterstate.CommandSetPreferences,
		clusterstate.SetPreferences{UserID: 7, Preferences: preferences},
	)); result.Err() != nil {
		t.Fatal(result.Err())
	}
	other := domain.Session{
		ID: "other", NodeID: "hidden", OwnerID: 7, Name: "Other", Backend: "codex",
		State: domain.SessionActive, RuntimePhase: domain.RuntimeIdle,
		CreatedAt: time.Unix(200, 0).UTC(), LiveSinceAt: time.Unix(200, 0).UTC(),
	}
	if result := fixture.machine.Apply(commandForTest(
		t, "add-other-node-session", clusterstate.CommandAddSession, other,
	)); result.Err() != nil {
		t.Fatal(result.Err())
	}
	closing, err := fixture.service.ActiveSession(actor)
	if err != nil || closing.NodeID != "allowed" {
		t.Fatalf("closing session=%#v/%v", closing, err)
	}
	controls := &closingControls{
		blockingControls: &blockingControls{ref: closing.Ref()}, service: fixture.service,
	}
	handler, err := telegramapp.NewHandlerWithControls(
		fixture.service, fixture.projector, fixture.codec, fixture.messenger, controls,
	)
	if err != nil {
		t.Fatal(err)
	}
	token, err := fixture.codec.Session(7, telegramui.ActionConfirmClose, closing.Ref())
	if err != nil {
		t.Fatal(err)
	}
	data, err := (telegramui.Callback{
		Action: telegramui.ActionConfirmClose, Token: token,
	}).Encode()
	if err != nil {
		t.Fatal(err)
	}
	if err := handler.HandleTelegramUpdate(context.Background(), telegrambot.IncomingUpdate{
		UpdateID: 73, Kind: telegrambot.IncomingCallback, ChatID: 7, UserID: 7,
		CallbackID: "close-last", CallbackData: data,
		CallbackOrigin: telegrambot.Message{ChatID: 7, MessageID: 10},
	}); err != nil {
		t.Fatal(err)
	}
	if len(fixture.messenger.edited) != 1 {
		t.Fatalf("edits=%#v", fixture.messenger.edited)
	}
	screen := fixture.messenger.edited[0]
	if screen.Name != telegramui.ScreenSessions || strings.Contains(screen.Text, "Allowed") ||
		len(screen.Grid) < 2 || len(screen.Grid[0]) != 1 ||
		screen.Grid[0][0].Callback.Action != telegramui.ActionSelectSession ||
		!strings.Contains(screen.Grid[0][0].Label, "Other") ||
		!strings.Contains(screen.Grid[0][0].Label, "Hidden") {
		t.Fatalf("close target=%#v", screen)
	}
}
