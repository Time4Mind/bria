package application_test

import (
	"testing"
	"time"

	"github.com/Time4Mind/bria/internal/application"
	"github.com/Time4Mind/bria/internal/clusterstate"
	"github.com/Time4Mind/bria/internal/domain"
)

func TestProviderAndActiveSessionLookupDoNotDependOnTelegramCard(t *testing.T) {
	state := domain.NewState()
	if err := state.AddNode(domain.Node{ID: "node", Name: "Node", Status: domain.NodeOnline}); err != nil {
		t.Fatal(err)
	}
	if err := state.SetSoleOwner(7); err != nil {
		t.Fatal(err)
	}
	created := time.Unix(100, 0).UTC()
	session := domain.Session{
		ID: "session", NodeID: "node", OwnerID: 7, Name: "Session",
		Backend: "codex", ProviderSessionID: "provider", State: domain.SessionActive,
		RuntimePhase: domain.RuntimeIdle, RuntimeGeneration: 3,
		CreatedAt: created, LiveSinceAt: created,
	}
	if err := state.AddSession(session); err != nil {
		t.Fatal(err)
	}
	machine := clusterstate.NewMachine(state)
	port := localMachine{machine: machine}
	service, err := application.NewService(port, port)
	if err != nil {
		t.Fatal(err)
	}
	actor, resolved, ok := service.ProviderSession(session.Ref(), "provider", 3)
	if !ok || actor.UserID != 7 || resolved.Ref() != session.Ref() {
		t.Fatalf("provider session=%#v actor=%#v ok=%t", resolved, actor, ok)
	}
	if _, _, ok := service.ProviderSession(session.Ref(), "provider", 4); ok {
		t.Fatal("stale runtime generation resolved")
	}
	if _, _, ok := service.ProviderSession(session.Ref(), "other", 3); ok {
		t.Fatal("foreign provider session resolved")
	}
	active := service.ActiveSessions()
	if len(active) != 1 || active[0].Actor.UserID != 7 ||
		active[0].Session.Ref() != session.Ref() {
		t.Fatalf("active sessions=%#v", active)
	}
	if len(machine.State().TelegramResponseCards) != 0 {
		t.Fatalf("lookup manufactured Telegram state=%#v", machine.State().TelegramResponseCards)
	}
}
