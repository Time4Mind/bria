package telegramcontroller_test

import (
	"bria/internal/app"
	"bria/internal/domain"
	"bria/internal/settingsport"
	"bria/internal/telegramcontroller"
	"context"
	"testing"
	"time"
)

type standbyActiveRecorder struct {
	*standbySessions
	selected chan domain.SessionID
}

func (s *standbyActiveRecorder) SetActiveSession(_ context.Context, id domain.SessionID) error {
	s.selected <- id
	return nil
}

func TestAsyncStandbySelectsStartingSessionBeforeProviderReady(t *testing.T) {
	ctx := context.Background()
	store := &standbySessions{lockedSessions: newLockedSessions(), empty: map[domain.SessionID]bool{}}
	ui := &standbyActiveRecorder{standbySessions: store, selected: make(chan domain.SessionID, 8)}
	prefs := &standbyPreferences{value: settingsport.Snapshot{StandbyEnabled: true, CardDetail: "standard", CardPageLimit: 64, DefaultProviders: map[domain.ComputerID]domain.Provider{"local": domain.ProviderCodex}, DefaultWorkdirs: map[domain.ComputerID]string{"local": t.TempDir()}}}
	outcome := make(chan telegramcontroller.SessionStartOutcome, 1)
	starting := make(chan domain.Session, 1)
	async := asyncCreatorFunc(func(_ context.Context, intent app.ConfirmedSessionIntent) (telegramcontroller.PendingSessionStart, error) {
		session, err := domain.NewStartingSession("ffffffff-ffff-4fff-9fff-ffffffffffff", intent.IntentID, intent.ComputerID, intent.Provider, intent.Workdir)
		if err != nil {
			return telegramcontroller.PendingSessionStart{}, err
		}
		store.Set(session)
		store.setEmpty(session.ID(), true)
		starting <- session
		return telegramcontroller.PendingSessionStart{Session: session, Outcome: outcome}, nil
	})
	controller := newController(t, nil, store, nil, nil, telegramcontroller.Options{Settings: prefs, UIState: ui, AsyncCreator: async})
	t.Cleanup(func() { _ = controller.Close(ctx) })
	controller.ScheduleStandby()
	var session domain.Session
	select {
	case session = <-starting:
	case <-time.After(time.Second):
		t.Fatal("standby not started")
	}
	select {
	case selected := <-ui.selected:
		if selected != session.ID() {
			t.Fatalf("selected=%s", selected)
		}
	case <-time.After(200 * time.Millisecond):
		t.Fatal("Starting standby exists but no active selection before provider readiness")
	}
	// No provider outcome was supplied: the real creation operation is still
	// pending, but an early user prompt must already address this session.
	if _, err := controller.Handle(ctx, message(201, "early test prompt")); err != nil {
		t.Fatal(err)
	}
	if empty, _ := store.HasEmptyCloseEligibility(ctx, session.ID()); empty {
		t.Fatal("early prompt missed pending active session")
	}
}
