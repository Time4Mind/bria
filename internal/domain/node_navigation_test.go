package domain_test

import (
	"testing"
	"time"

	"github.com/Time4Mind/bria/internal/domain"
)

func TestOnlineHeartbeatDoesNotReplaceExplicitlySelectedEmptyNode(t *testing.T) {
	state := domain.NewState()
	for _, node := range []domain.Node{
		{ID: "empty", Name: "Empty", Status: domain.NodeOnline},
		{ID: "busy", Name: "Busy", Status: domain.NodeOnline},
	} {
		if err := state.AddNode(node); err != nil {
			t.Fatal(err)
		}
	}
	if err := state.SetNodeAccess(7, domain.RoleOwner, "empty", "busy"); err != nil {
		t.Fatal(err)
	}
	created := time.Unix(100, 0).UTC()
	if err := state.AddSession(domain.Session{ID: "live", NodeID: "busy", OwnerID: 7,
		Backend: "codex", State: domain.SessionActive, RuntimePhase: domain.RuntimeIdle,
		CreatedAt: created, LiveSinceAt: created}); err != nil {
		t.Fatal(err)
	}
	if err := state.SelectNode(7, "empty", created.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	if err := state.UpdateNodeInventory("busy", domain.NodeOnline, "v", nil, created.Add(2*time.Second)); err != nil {
		t.Fatal(err)
	}
	if got := state.Navigation.ActiveNodeByUser[7]; got != "empty" {
		t.Fatalf("selected node changed to %q", got)
	}
}
