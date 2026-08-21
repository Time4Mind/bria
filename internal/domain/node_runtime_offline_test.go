package domain_test

import (
	"testing"
	"time"

	"github.com/Time4Mind/bria/internal/domain"
)

func TestOfflineActiveNodePromotesNewestEligibleBackground(t *testing.T) {
	state := domain.NewState()
	seenAt := time.Unix(100, 0).UTC()
	for _, node := range []domain.Node{
		{ID: "first", Name: "First", Status: domain.NodeOnline, LastSeenAt: seenAt},
		{ID: "second", Name: "Second", Status: domain.NodeOnline, LastSeenAt: seenAt},
	} {
		if err := state.AddNode(node); err != nil {
			t.Fatal(err)
		}
	}
	if err := state.SetNodeAccess(7, domain.RoleOwner, "first", "second"); err != nil {
		t.Fatal(err)
	}
	for index, nodeID := range []domain.NodeID{"first", "second"} {
		at := time.Unix(int64(index+1), 0).UTC()
		if err := state.AddSession(domain.Session{
			ID: domain.SessionID("session-" + string(nodeID)), NodeID: nodeID,
			OwnerID: 7, Name: string(nodeID), Backend: "claude",
			State: domain.SessionLive, CreatedAt: at, LiveSinceAt: at,
		}); err != nil {
			t.Fatal(err)
		}
	}
	first := domain.SessionRef{NodeID: "first", SessionID: "session-first"}
	if err := state.SelectSession(7, first, time.Unix(10, 0).UTC()); err != nil {
		t.Fatal(err)
	}
	if err := state.MarkNodeOffline("first", seenAt); err != nil {
		t.Fatal(err)
	}
	if got := state.Navigation.ActiveNodeByUser[7]; got != "first" {
		t.Fatalf("transient outage changed selected node=%q", got)
	}
	if err := state.MarkNodeOffline("second", seenAt); err != nil {
		t.Fatal(err)
	}
	if got := state.Navigation.ActiveNodeByUser[7]; got != "first" {
		t.Fatalf("selected offline node was discarded=%q", got)
	}
	if got := state.Navigation.ActiveSessionByUserNode[7]["first"]; got != "session-first" {
		t.Fatalf("selected offline session was discarded=%q", got)
	}
}
