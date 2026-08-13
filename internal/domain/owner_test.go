package domain_test

import (
	"testing"
	"time"

	"github.com/Time4Mind/bria/internal/domain"
)

func TestSetSoleOwnerMigratesPrivateStateAndDropsLegacyAccess(t *testing.T) {
	state := domain.NewState()
	if err := state.AddNode(domain.Node{ID: "node", Name: "Node", Status: domain.NodeOnline}); err != nil {
		t.Fatal(err)
	}
	if err := state.SetNodeAccess(1, domain.RoleOwner, "node"); err != nil {
		t.Fatal(err)
	}
	if err := state.SetNodeAccess(2, domain.RoleMember, "node"); err != nil {
		t.Fatal(err)
	}
	created := time.Unix(10, 0)
	if err := state.AddSession(domain.Session{
		ID: "session", NodeID: "node", OwnerID: 1, Name: "Session",
		State: domain.SessionActive, CreatedAt: created, LiveSinceAt: created,
	}); err != nil {
		t.Fatal(err)
	}
	state.Navigation.ActiveNodeByUser[1] = "node"
	if err := state.SetSoleOwner(9); err != nil {
		t.Fatal(err)
	}
	if state.OwnerID() != 9 || len(state.Users) != 1 || !state.CanAccessNode(9, "node") {
		t.Fatalf("users=%#v", state.Users)
	}
	if state.Sessions["node/session"].OwnerID != 1 || state.Navigation.ActiveNodeByUser[9] != "node" ||
		!state.CanPerformSessionAction(9, domain.SessionRef{NodeID: "node", SessionID: "session"}, domain.ActionClose) {
		t.Fatalf("session/navigation not migrated: %#v %#v", state.Sessions, state.Navigation)
	}
	if state.CanAccessNode(1, "node") || state.CanAccessNode(2, "node") {
		t.Fatal("legacy users retained access")
	}
}
