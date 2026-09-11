package telegramcontroller_test

import (
	"bria/internal/app"
	"bria/internal/domain"
	"bria/internal/settingsport"
	"bria/internal/telegramcontroller"
	"bria/internal/telegramsessions"
	"context"
	"strings"
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

func TestAsyncStandbyPublishesBackgroundStartingSessionBeforeProviderReady(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	active := readySession(t, "aaaaaaaa-aaaa-4aaa-9aaa-aaaaaaaaaaaa", domain.ProviderCodex, dir, "active-provider", 1)
	store := &standbySessions{lockedSessions: newLockedSessions(active), empty: map[domain.SessionID]bool{}}
	ui := &standbyActiveRecorder{standbySessions: store, selected: make(chan domain.SessionID, 8)}
	prefs := &standbyPreferences{value: settingsport.Snapshot{StandbyEnabled: true, CardDetail: "standard", CardPageLimit: 64, DefaultProviders: map[domain.ComputerID]domain.Provider{"local": domain.ProviderCodex}, DefaultWorkdirs: map[domain.ComputerID]string{"local": dir}}}
	outcome := make(chan telegramcontroller.SessionStartOutcome, 1)
	starting := make(chan domain.Session, 1)
	notifications := make(chan telegramcontroller.Notification, 8)
	async := asyncCreatorFunc(func(_ context.Context, intent app.ConfirmedSessionIntent) (telegramcontroller.PendingSessionStart, error) {
		session, err := domain.NewStartingSession("bbbbbbbb-bbbb-4bbb-9bbb-bbbbbbbbbbbb", intent.IntentID, intent.ComputerID, intent.Provider, intent.Workdir)
		if err != nil {
			return telegramcontroller.PendingSessionStart{}, err
		}
		store.Set(session)
		store.setEmpty(session.ID(), true)
		starting <- session
		return telegramcontroller.PendingSessionStart{Session: session, Outcome: outcome}, nil
	})
	controller := newController(t, nil, store, nil, notifierFunc(func(_ context.Context, notification telegramcontroller.Notification) error {
		notifications <- notification
		return nil
	}), telegramcontroller.Options{Settings: prefs, UIState: ui, AsyncCreator: async, Recovered: []domain.Session{active}})
	t.Cleanup(func() { _ = controller.Close(ctx) })
	if _, err := controller.HandleSemanticAction(ctx, telegramcontroller.SemanticAction{Kind: telegramcontroller.SemanticSelect, SessionID: active.ID()}); err != nil {
		t.Fatal(err)
	}
	select {
	case <-ui.selected:
	case <-time.After(time.Second):
		t.Fatal("active session selection was not persisted")
	}

	controller.ScheduleStandby()
	var pending domain.Session
	select {
	case pending = <-starting:
	case <-time.After(time.Second):
		t.Fatal("standby not started")
	}
	select {
	case notification := <-notifications:
		if notification.SessionID != active.ID() || notification.Kind != telegramcontroller.NotificationPromptStatus {
			t.Fatalf("notification=%#v", notification)
		}
	case <-time.After(200 * time.Millisecond):
		t.Fatal("background Starting standby did not request an immediate card refresh")
	}
	current, err := controller.ProjectCurrent(ctx, active.ID())
	if err != nil || current.Card == nil {
		t.Fatalf("ProjectCurrent()=(%#v, %v)", current, err)
	}
	found := false
	for _, id := range current.Card.SelectableSessionIDs {
		found = found || id == pending.ID()
	}
	if !found {
		t.Fatalf("Starting standby %s absent from buttons: %#v", pending.ID(), current.Card.SelectableSessionIDs)
	}
	if strings.Contains(current.Card.Footer, "default") || strings.Contains(current.Card.Footer, "фон") {
		t.Fatalf("empty Starting standby leaked into background footer: %q", current.Card.Footer)
	}
	ready, err := pending.Ready(domain.ProviderBinding{Provider: pending.Provider(), SessionID: "standby-provider", Generation: 1})
	if err != nil {
		t.Fatal(err)
	}
	store.Set(ready)
	outcome <- telegramcontroller.SessionStartOutcome{Session: ready}
	select {
	case notification := <-notifications:
		if notification.SessionID != active.ID() || notification.Kind != telegramcontroller.NotificationPromptStatus {
			t.Fatalf("ready notification=%#v", notification)
		}
	case <-time.After(200 * time.Millisecond):
		t.Fatal("ready standby did not request a card refresh")
	}
	current, err = controller.ProjectCurrent(ctx, active.ID())
	if err != nil || current.Card == nil {
		t.Fatalf("ProjectCurrent() for ready standby=(%#v, %v)", current, err)
	}
	if strings.Contains(current.Card.Footer, "default") || strings.Contains(current.Card.Footer, "фон") {
		t.Fatalf("empty Ready standby leaked into background footer: %q", current.Card.Footer)
	}
	store.setEmpty(pending.ID(), false)
	current, err = controller.ProjectCurrent(ctx, active.ID())
	if err != nil || current.Card == nil {
		t.Fatalf("ProjectCurrent() after context=(%#v, %v)", current, err)
	}
	pendingLabel := telegramsessions.Labels([]domain.Session{active, ready}, active.ComputerID())[ready.ID()]
	if !strings.Contains(current.Card.Footer, "фон") || !strings.Contains(current.Card.Footer, pendingLabel) {
		t.Fatalf("non-empty Starting standby absent from background footer: %q", current.Card.Footer)
	}
}

func TestAsyncStandbyStartingButtonSelectsFIFOWhileProviderStarts(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	active := readySession(t, "cccccccc-cccc-4ccc-9ccc-cccccccccccc", domain.ProviderCodex, dir, "active-provider", 1)
	store := &standbySessions{lockedSessions: newLockedSessions(active), empty: map[domain.SessionID]bool{}}
	ui := &standbyActiveRecorder{standbySessions: store, selected: make(chan domain.SessionID, 8)}
	prefs := &standbyPreferences{value: settingsport.Snapshot{StandbyEnabled: true, CardDetail: "standard", CardPageLimit: 64, DefaultProviders: map[domain.ComputerID]domain.Provider{"local": domain.ProviderCodex}, DefaultWorkdirs: map[domain.ComputerID]string{"local": dir}}}
	outcome := make(chan telegramcontroller.SessionStartOutcome, 1)
	starting := make(chan domain.Session, 1)
	accepted := make(chan telegramcontroller.SessionInput, 1)
	async := asyncCreatorFunc(func(_ context.Context, intent app.ConfirmedSessionIntent) (telegramcontroller.PendingSessionStart, error) {
		session, err := domain.NewStartingSession("dddddddd-dddd-4ddd-9ddd-dddddddddddd", intent.IntentID, intent.ComputerID, intent.Provider, intent.Workdir)
		if err != nil {
			return telegramcontroller.PendingSessionStart{}, err
		}
		store.Set(session)
		store.setEmpty(session.ID(), true)
		starting <- session
		return telegramcontroller.PendingSessionStart{Session: session, Outcome: outcome}, nil
	})
	custody := durableInputFunc(func(_ context.Context, input telegramcontroller.SessionInput) (telegramcontroller.InputReceipt, error) {
		accepted <- input
		return telegramcontroller.InputReceipt{SessionID: input.SessionID, MessageID: input.MessageID, Sequence: 1}, nil
	})
	controller := newController(t, nil, store, nil, nil, telegramcontroller.Options{Settings: prefs, UIState: ui, AsyncCreator: async, DurableInput: custody, Recovered: []domain.Session{active}})
	t.Cleanup(func() { _ = controller.Close(ctx) })
	if _, err := controller.HandleSemanticAction(ctx, telegramcontroller.SemanticAction{Kind: telegramcontroller.SemanticSelect, SessionID: active.ID()}); err != nil {
		t.Fatal(err)
	}
	select {
	case <-ui.selected:
	case <-time.After(time.Second):
		t.Fatal("active session selection was not persisted")
	}
	controller.ScheduleStandby()
	var pending domain.Session
	select {
	case pending = <-starting:
	case <-time.After(time.Second):
		t.Fatal("standby not started")
	}

	selectedCard, err := controller.HandleSemanticAction(ctx, telegramcontroller.SemanticAction{Kind: telegramcontroller.SemanticSelect, SessionID: pending.ID()})
	if err != nil || selectedCard.Card == nil || selectedCard.Card.SessionID != pending.ID() {
		t.Fatalf("select Starting standby=(%#v, %v)", selectedCard, err)
	}
	select {
	case selected := <-ui.selected:
		if selected != pending.ID() {
			t.Fatalf("selected=%s, want %s", selected, pending.ID())
		}
	case <-time.After(200 * time.Millisecond):
		t.Fatal("Starting standby button did not persist selection")
	}
	if _, err := controller.Handle(ctx, message(202, "early FIFO prompt")); err != nil {
		t.Fatal(err)
	}
	select {
	case input := <-accepted:
		if input.SessionID != pending.ID() || input.MessageID != "telegram-update:202" {
			t.Fatalf("accepted input=%#v", input)
		}
	case <-time.After(200 * time.Millisecond):
		t.Fatal("early prompt did not enter durable FIFO custody")
	}
}
