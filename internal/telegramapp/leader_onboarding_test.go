package telegramapp_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/Time4Mind/bria/internal/application"
	"github.com/Time4Mind/bria/internal/callbacktoken"
	"github.com/Time4Mind/bria/internal/clusterstate"
	"github.com/Time4Mind/bria/internal/domain"
	"github.com/Time4Mind/bria/internal/telegramapp"
	"github.com/Time4Mind/bria/internal/telegrambot"
	"github.com/Time4Mind/bria/internal/telegramui"
)

func newFixture(t *testing.T) fixture {
	return newFixtureWithLeader(t, true)
}

func newFixtureWithLeader(t *testing.T, assigned bool) fixture {
	t.Helper()
	state := domain.NewState()
	for _, node := range []domain.Node{
		{ID: "allowed", Name: "Allowed", Status: domain.NodeOnline},
		{ID: "hidden", Name: "Hidden", Status: domain.NodeOnline},
	} {
		if err := state.AddNode(node); err != nil {
			t.Fatal(err)
		}
	}
	if err := state.SetNodeAccess(7, domain.RoleOwner, "allowed"); err != nil {
		t.Fatal(err)
	}
	if assigned {
		if err := state.SetPreferredLeader("allowed"); err != nil {
			t.Fatal(err)
		}
	}
	created := time.Unix(100, 0).UTC()
	if err := state.AddSession(domain.Session{
		ID: "live", NodeID: "allowed", OwnerID: 7, Name: "Live", Backend: "codex",
		State: domain.SessionActive, CreatedAt: created, LiveSinceAt: created,
	}); err != nil {
		t.Fatal(err)
	}
	machine := clusterstate.NewMachine(state)
	events := make([]string, 0)
	port := machinePort{machine: machine, events: &events}
	service, err := application.NewService(port, port)
	if err != nil {
		t.Fatal(err)
	}
	codec, err := callbacktoken.New([]byte(strings.Repeat("k", callbacktoken.KeyBytes)))
	if err != nil {
		t.Fatal(err)
	}
	projector, err := telegramapp.NewTelegramProjector(port, codec)
	if err != nil {
		t.Fatal(err)
	}
	messenger := &messengerStub{events: &events}
	handler, err := telegramapp.NewHandler(service, projector, codec, messenger)
	if err != nil {
		t.Fatal(err)
	}
	return fixture{
		handler: handler, service: service, projector: projector,
		machine: machine, codec: codec, messenger: messenger,
		events: &events,
	}
}

func TestManualModeRequiresInitialLeaderSelection(t *testing.T) {
	fixture := newFixtureWithLeader(t, false)
	if err := fixture.handler.HandleTelegramUpdate(context.Background(), telegrambot.IncomingUpdate{
		UpdateID: 1, Kind: telegrambot.IncomingMessage, ChatID: 7, UserID: 7,
		Text: "/menu", LanguageCode: "ru",
	}); err != nil {
		t.Fatal(err)
	}
	if len(fixture.messenger.sent) != 1 {
		t.Fatalf("sent screens=%d", len(fixture.messenger.sent))
	}
	screen := fixture.messenger.sent[0]
	if screen.Name != telegramui.ScreenSettings ||
		!strings.Contains(screen.Text, "Выбор лидера") {
		t.Fatalf("unexpected setup screen: %#v", screen)
	}
}
