package domain_test

import (
	"reflect"
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
	if err := state.RecordTelegramResponseCard(1, domain.TelegramResponseCard{
		ChatID: 1, MessageID: 11, Session: domain.SessionRef{NodeID: "node", SessionID: "session"},
		SessionRevision: 1,
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
	if len(state.TelegramResponseCards) != 0 {
		t.Fatalf("legacy response cards retained: %#v", state.TelegramResponseCards)
	}
}

func TestSetSoleOwnerPreservesPrivateStateForTheCanonicalOwner(t *testing.T) {
	state := domain.NewState()
	for _, nodeID := range []domain.NodeID{"first", "second"} {
		if err := state.AddNode(domain.Node{ID: nodeID, Name: string(nodeID), Status: domain.NodeOnline}); err != nil {
			t.Fatal(err)
		}
	}
	// A stale bootstrap access list makes startup issue SetSoleOwner even though
	// the private actor did not change.
	if err := state.SetNodeAccess(7, domain.RoleOwner, "first"); err != nil {
		t.Fatal(err)
	}
	created := time.Unix(20, 0).UTC()
	if err := state.AddSession(domain.Session{
		ID: "session", NodeID: "first", OwnerID: 7, Name: "Session", Backend: "codex",
		State: domain.SessionLive, RuntimePhase: domain.RuntimeIdle,
		CreatedAt: created, LiveSinceAt: created,
	}); err != nil {
		t.Fatal(err)
	}
	ref := domain.SessionRef{NodeID: "first", SessionID: "session"}
	card := domain.TelegramResponseCard{
		ChatID: 7, MessageID: 23, Session: ref, SessionRevision: 4,
		SessionEventAt: created.Add(time.Minute), RenderedFinalAt: created.Add(2 * time.Minute),
	}
	if err := state.RecordTelegramResponseCard(7, card); err != nil {
		t.Fatal(err)
	}
	state.Navigation.ActiveNodeByUser[7] = "first"
	state.Navigation.ActiveSessionByUserNode[7] = map[domain.NodeID]domain.SessionID{"first": "session"}
	beforeCard := state.TelegramResponseCards[7]
	beforeNavigation := state.Navigation
	beforePreferences := state.Preferences[7]

	if err := state.SetSoleOwner(7); err != nil {
		t.Fatal(err)
	}

	if !state.CanAccessNode(7, "first") || !state.CanAccessNode(7, "second") {
		t.Fatalf("owner access was not reconciled: %#v", state.Users[7])
	}
	if got := state.TelegramResponseCards[7]; !reflect.DeepEqual(got, beforeCard) {
		t.Fatalf("response card changed:\n got: %#v\nwant: %#v", got, beforeCard)
	}
	if !reflect.DeepEqual(state.Navigation, beforeNavigation) {
		t.Fatalf("navigation changed:\n got: %#v\nwant: %#v", state.Navigation, beforeNavigation)
	}
	if !reflect.DeepEqual(state.Preferences[7], beforePreferences) {
		t.Fatalf("preferences changed:\n got: %#v\nwant: %#v", state.Preferences[7], beforePreferences)
	}
}
