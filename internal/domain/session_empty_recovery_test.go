package domain_test

import (
	"testing"
	"time"

	"github.com/Time4Mind/bria/internal/domain"
)

func TestNodeRebootDiscardsTrackedSessionWithoutUserRequest(t *testing.T) {
	state := fixtureState(t)
	ref := addSession(t, state, "empty", "alpha", 1, time.Unix(100, 0).UTC())
	session := state.Sessions[ref.Key()]
	session.UserRequestTracked = true
	session.UserRequestSeen = false
	state.Sessions[ref.Key()] = session

	plan, err := state.ObserveNodeBoot(
		"alpha", "boot-next", time.Unix(100, 0).UTC().Add(6*time.Hour),
	)
	if err != nil || len(plan.Discarded) != 1 || plan.Discarded[0] != ref ||
		len(plan.Archived) != 0 || len(plan.Recover) != 0 {
		t.Fatalf("plan = (%#v, %v)", plan, err)
	}
	got := state.Sessions[ref.Key()]
	if got.State != domain.SessionDiscarding || got.ArchiveReason != "" ||
		!got.ArchivedAt.IsZero() || got.DiscardedAt.IsZero() {
		t.Fatalf("empty session after reboot = %#v", got)
	}
	if archived := state.VisibleSessions(1, false); len(archived) != 0 {
		t.Fatalf("empty session leaked into archive: %#v", archived)
	}
}
