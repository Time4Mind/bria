package domain_test

import (
	"testing"
	"time"

	"github.com/Time4Mind/bria/internal/domain"
)

func TestBackgroundNoticeRequiresConfiguredSwitchesIntoSession(t *testing.T) {
	state, first, second := backgroundState(t)
	preferences := state.Preferences[1]
	preferences.BackgroundDismissSwitches = 3
	if err := state.SetPreferences(1, preferences); err != nil {
		t.Fatal(err)
	}
	firstSession := state.Sessions[first.Key()]
	if err := state.PublishSessionRuntime(
		first, firstSession.RuntimeGeneration, domain.RuntimeRunning, nil,
		time.Unix(3, 0).UTC(),
	); err != nil {
		t.Fatal(err)
	}
	if err := state.SelectSession(1, second, time.Unix(4, 0).UTC()); err != nil {
		t.Fatal(err)
	}
	notice := state.Navigation.BackgroundByUser[1][first.Key()]
	if notice.Kind != domain.BackgroundWorking || notice.Acknowledgements != 0 {
		t.Fatalf("working notice=%#v", notice)
	}
	firstSession = state.Sessions[first.Key()]
	if err := state.PublishSessionRuntime(
		first, firstSession.RuntimeGeneration, domain.RuntimeIdle, nil,
		time.Unix(5, 0).UTC(),
	); err != nil {
		t.Fatal(err)
	}
	notice = state.Navigation.BackgroundByUser[1][first.Key()]
	if notice.Kind != domain.BackgroundFinished || notice.Acknowledgements != 0 || notice.Notified {
		t.Fatalf("finished notice=%#v", notice)
	}
	for visit := 1; visit <= 3; visit++ {
		if err := state.SelectSession(1, first, time.Unix(int64(6+visit*2), 0).UTC()); err != nil {
			t.Fatal(err)
		}
		notice = state.Navigation.BackgroundByUser[1][first.Key()]
		if notice.Dismissed != (visit == 3) {
			t.Fatalf("visit %d dismissed=%v", visit, notice.Dismissed)
		}
		if visit < 3 {
			if err := state.SelectSession(1, second, time.Unix(int64(7+visit*2), 0).UTC()); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := state.SelectSession(1, second, time.Unix(20, 0).UTC()); err != nil {
		t.Fatal(err)
	}
	notice = state.Navigation.BackgroundByUser[1][first.Key()]
	if !notice.Dismissed {
		t.Fatal("dismissed notice reappeared after switching away")
	}
}

func TestBackgroundPreferencesDefaultToNotificationsAndOneSwitch(t *testing.T) {
	preferences := domain.UserPreferences{}
	if preferences.EffectiveBackgroundDismissSwitches() != 1 {
		t.Fatalf("legacy dismissal=%d", preferences.EffectiveBackgroundDismissSwitches())
	}
	for _, kind := range []domain.BackgroundNoticeKind{
		domain.BackgroundFinished, domain.BackgroundError, domain.BackgroundNeedsAction,
	} {
		if !preferences.SendsBackgroundNotification(kind) {
			t.Fatalf("%q notification was not enabled by default", kind)
		}
	}
}

func TestDismissedWorkingNoticeRestartsWhenSessionBecomesBackgroundAgain(t *testing.T) {
	state, first, second := backgroundState(t)
	session := state.Sessions[first.Key()]
	if err := state.PublishSessionRuntime(
		first, session.RuntimeGeneration, domain.RuntimeRunning, nil,
		time.Unix(3, 0).UTC(),
	); err != nil {
		t.Fatal(err)
	}
	if err := state.SelectSession(1, second, time.Unix(4, 0).UTC()); err != nil {
		t.Fatal(err)
	}
	if err := state.SelectSession(1, first, time.Unix(5, 0).UTC()); err != nil {
		t.Fatal(err)
	}
	if err := state.SelectSession(1, second, time.Unix(6, 0).UTC()); err != nil {
		t.Fatal(err)
	}
	notice := state.Navigation.BackgroundByUser[1][first.Key()]
	if notice.Dismissed || notice.Acknowledgements != 0 || notice.Kind != domain.BackgroundWorking {
		t.Fatalf("working notice did not restart: %#v", notice)
	}
	session = state.Sessions[first.Key()]
	if err := state.PublishSessionRuntime(
		first, session.RuntimeGeneration, domain.RuntimeWaitingInput, nil,
		time.Unix(7, 0).UTC(),
	); err != nil {
		t.Fatal(err)
	}
	notice = state.Navigation.BackgroundByUser[1][first.Key()]
	if notice.Dismissed || notice.Kind != domain.BackgroundNeedsAction {
		t.Fatalf("new event notice=%#v", notice)
	}
	if err := state.SelectSession(1, first, time.Unix(8, 0).UTC()); err != nil {
		t.Fatal(err)
	}
	if err := state.SelectSession(1, second, time.Unix(9, 0).UTC()); err != nil {
		t.Fatal(err)
	}
	notice = state.Navigation.BackgroundByUser[1][first.Key()]
	if !notice.Dismissed || notice.Kind != domain.BackgroundNeedsAction {
		t.Fatalf("action notice restarted without a new event: %#v", notice)
	}
}

func backgroundState(t *testing.T) (*domain.State, domain.SessionRef, domain.SessionRef) {
	t.Helper()
	state := domain.NewState()
	if err := state.AddNode(domain.Node{ID: "node", Name: "Node", Status: domain.NodeOnline}); err != nil {
		t.Fatal(err)
	}
	if err := state.SetNodeAccess(1, domain.RoleOwner, "node"); err != nil {
		t.Fatal(err)
	}
	add := func(id string, at int64) domain.SessionRef {
		timestamp := time.Unix(at, 0).UTC()
		session := domain.Session{
			ID: domain.SessionID(id), NodeID: "node", OwnerID: 1, Name: id,
			Backend: "codex", State: domain.SessionLive, RuntimePhase: domain.RuntimeIdle,
			CreatedAt: timestamp, LiveSinceAt: timestamp, LastEventAt: timestamp,
		}
		if err := state.AddSession(session); err != nil {
			t.Fatal(err)
		}
		return session.Ref()
	}
	return state, add("first", 1), add("second", 2)
}
