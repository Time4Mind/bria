package telegramapp_test

import (
	"context"
	"strings"
	"testing"

	"github.com/Time4Mind/bria/internal/application"
	"github.com/Time4Mind/bria/internal/clusterstate"
	"github.com/Time4Mind/bria/internal/domain"
	"github.com/Time4Mind/bria/internal/telegrambot"
	"github.com/Time4Mind/bria/internal/telegramui"
)

func TestNewSessionSelectorExcludesDisabledNodes(t *testing.T) {
	fixture := newFixture(t)
	result := fixture.machine.Apply(commandForTest(t, "disabled", clusterstate.CommandAddNode,
		domain.Node{ID: "disabled", Name: "Disabled", Status: domain.NodeOffline, Lifecycle: domain.NodeDisabled}))
	if result.Err() != nil {
		t.Fatal(result.Err())
	}
	preferences := fixture.machine.State().Preferences[7]
	preferences.SessionView = domain.ViewAllHosts
	result = fixture.machine.Apply(commandForTest(t, "all-hosts-disabled", clusterstate.CommandSetPreferences,
		clusterstate.SetPreferences{UserID: 7, Preferences: preferences}))
	if result.Err() != nil {
		t.Fatal(result.Err())
	}
	if err := fixture.handler.SetSessionStarter(createStarterStub{events: fixture.events}); err != nil {
		t.Fatal(err)
	}
	data, _ := (telegramui.Callback{Action: telegramui.ActionNewSession}).Encode()
	if err := fixture.handler.HandleTelegramUpdate(context.Background(), telegrambot.IncomingUpdate{
		UpdateID: 73, Kind: telegrambot.IncomingCallback, ChatID: 7, UserID: 7,
		CallbackID: "new", CallbackData: data, CallbackOrigin: telegrambot.Message{ChatID: 7, MessageID: 10},
	}); err != nil {
		t.Fatal(err)
	}
	screen := fixture.messenger.edited[len(fixture.messenger.edited)-1]
	if strings.Contains(telegramui.CanonicalGrid(screen.Grid), "Disabled") {
		t.Fatal("disabled node appeared in the new-session selector")
	}
}

func TestNewSessionDoesNotOfferInstalledButDisconnectedBackend(t *testing.T) {
	fixture := newFixture(t)
	result := fixture.machine.Apply(commandForTest(t, "installed-backend", clusterstate.CommandPublishNodeHeartbeat,
		clusterstate.PublishNodeHeartbeat{NodeID: "allowed", BootID: "boot-installed", Backends: []domain.BackendDescriptor{{Name: "codex", Capabilities: []string{"session.create"}}}}))
	if result.Err() != nil {
		t.Fatal(result.Err())
	}
	if err := fixture.handler.SetSessionStarter(createStarterStub{events: fixture.events}); err != nil {
		t.Fatal(err)
	}
	sessions, err := fixture.projector.NodeSessions(application.Principal{UserID: 7}, "allowed")
	if err != nil {
		t.Fatal(err)
	}
	var create telegramui.Callback
	for _, row := range sessions.Grid {
		for _, button := range row {
			if button.Callback.Action == telegramui.ActionNewSession {
				create = button.Callback
			}
		}
	}
	data, err := create.Encode()
	if err != nil {
		t.Fatal(err)
	}
	if err := fixture.handler.HandleTelegramUpdate(context.Background(), telegrambot.IncomingUpdate{
		UpdateID: 75, Kind: telegrambot.IncomingCallback, ChatID: 7, UserID: 7,
		CallbackID: "installed-new", CallbackData: data, CallbackOrigin: telegrambot.Message{ChatID: 7, MessageID: 10},
	}); err != nil {
		t.Fatal(err)
	}
	screen := fixture.messenger.edited[len(fixture.messenger.edited)-1]
	for _, row := range screen.Grid {
		for _, button := range row {
			if button.Callback.Action == telegramui.ActionNewBackend {
				t.Fatalf("disconnected backend appeared in creation: %#v", screen)
			}
		}
	}
	if got := fixture.machine.State().Nodes["allowed"].Backends; len(got) != 0 {
		t.Fatalf("creation connected backend implicitly: %#v", got)
	}
}

func TestNewSessionSkipsBackendChoiceWhenOnlyOneIsConnected(t *testing.T) {
	fixture := newFixture(t)
	result := fixture.machine.Apply(commandForTest(t, "installed-backends", clusterstate.CommandPublishNodeHeartbeat,
		clusterstate.PublishNodeHeartbeat{NodeID: "allowed", BootID: "boot-installed", Backends: []domain.BackendDescriptor{{Name: "claude", Capabilities: []string{"session.create"}}, {Name: "codex", Capabilities: []string{"session.create"}}}}))
	if result.Err() != nil {
		t.Fatal(result.Err())
	}
	result = fixture.machine.Apply(commandForTest(t, "connect-codex", clusterstate.CommandSetNodeBackend,
		clusterstate.SetNodeBackend{NodeID: "allowed", Backend: "codex", Connected: true}))
	if result.Err() != nil {
		t.Fatal(result.Err())
	}
	if err := fixture.handler.SetSessionStarter(createStarterStub{events: fixture.events}); err != nil {
		t.Fatal(err)
	}
	sessions, err := fixture.projector.NodeSessions(application.Principal{UserID: 7}, "allowed")
	if err != nil {
		t.Fatal(err)
	}
	data, err := (telegramui.Callback{Action: telegramui.ActionNewSession, Token: actionToken(t, sessions, telegramui.ActionNewSession)}).Encode()
	if err != nil {
		t.Fatal(err)
	}
	if err := fixture.handler.HandleTelegramUpdate(context.Background(), telegrambot.IncomingUpdate{
		UpdateID: 77, Kind: telegrambot.IncomingCallback, ChatID: 7, UserID: 7,
		CallbackID: "connected-new", CallbackData: data, CallbackOrigin: telegrambot.Message{ChatID: 7, MessageID: 10},
	}); err != nil {
		t.Fatal(err)
	}
	screen := lastEdited(t, fixture)
	if screen.Grid[0][0].Callback.Action != telegramui.ActionNewDirectory {
		t.Fatalf("single connected backend did not advance to directories: %#v", screen)
	}
	for _, row := range screen.Grid {
		for _, button := range row {
			if button.Callback.Action == telegramui.ActionNewBackend {
				t.Fatalf("single connected backend produced selector: %#v", screen)
			}
		}
	}
}
