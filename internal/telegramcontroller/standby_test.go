package telegramcontroller_test

import (
	"bria/internal/app"
	"bria/internal/domain"
	"bria/internal/sessionruntime"
	"bria/internal/settingsport"
	"bria/internal/telegramcontroller"
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"
)

type standbySessions struct {
	*lockedSessions
	emptyMu sync.Mutex
	empty   map[domain.SessionID]bool
}

func (s *standbySessions) HasEmptyCloseEligibility(_ context.Context, id domain.SessionID) (bool, error) {
	s.emptyMu.Lock()
	defer s.emptyMu.Unlock()
	return s.empty[id], nil
}
func (s *standbySessions) setEmpty(id domain.SessionID, empty bool) {
	s.emptyMu.Lock()
	defer s.emptyMu.Unlock()
	s.empty[id] = empty
}
func (s *standbySessions) DeleteEmptyAwaitingRecovery(_ context.Context, expected domain.Session) (bool, error) {
	s.emptyMu.Lock()
	defer s.emptyMu.Unlock()
	if expected.Status() != domain.SessionAwaitingRecovery || !s.empty[expected.ID()] {
		return false, nil
	}
	s.lockedSessions.mu.Lock()
	defer s.lockedSessions.mu.Unlock()
	current, ok := s.lockedSessions.byID[expected.ID()]
	if !ok || !current.Equal(expected) {
		return false, nil
	}
	delete(s.lockedSessions.byID, expected.ID())
	delete(s.empty, expected.ID())
	return true, nil
}

func (s *standbySessions) SetActiveSession(context.Context, domain.SessionID) error { return nil }
func (s *standbySessions) SetCardPrompt(_ context.Context, id domain.SessionID, _, _ string) error {
	s.setEmpty(id, false)
	return nil
}

type standbyPreferences struct {
	settingsport.Preferences
	mu    sync.Mutex
	value settingsport.Snapshot
}

func (p *standbyPreferences) Snapshot(context.Context) (settingsport.Snapshot, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.value, nil
}
func (p *standbyPreferences) ToggleStandby(context.Context) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.value.StandbyEnabled = !p.value.StandbyEnabled
	return nil
}

func TestStandbySemanticEnableFirstPromptAndDisable(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	original := readySession(t, "dddddddd-dddd-4ddd-9ddd-dddddddddddd", domain.ProviderCodex, dir, "original", 1)
	store := &standbySessions{lockedSessions: newLockedSessions(original), empty: map[domain.SessionID]bool{}}
	prefs := &standbyPreferences{value: settingsport.Snapshot{CardDetail: "standard", CardPageLimit: 64, DefaultProviders: map[domain.ComputerID]domain.Provider{"local": domain.ProviderCodex}}}
	created := make(chan domain.Session, 4)
	count := 0
	creator := creatorFunc(func(_ context.Context, intent app.ConfirmedSessionIntent) (app.CreateSessionResult, error) {
		count++
		id := domain.SessionID(fmt.Sprintf("eeeeeeee-eeee-4eee-9eee-%012d", count))
		session, err := domain.NewStartingSession(id, intent.IntentID, intent.ComputerID, intent.Provider, intent.Workdir)
		if err != nil {
			return app.CreateSessionResult{}, err
		}
		session, err = session.Rename(intent.Name, domain.SessionNameDirectory)
		if err != nil {
			return app.CreateSessionResult{}, err
		}
		session, err = session.Ready(domain.ProviderBinding{Provider: intent.Provider, SessionID: "test", Generation: 1})
		if err != nil {
			return app.CreateSessionResult{}, err
		}
		store.Set(session)
		store.setEmpty(id, true)
		created <- session
		return app.CreateSessionResult{Session: session}, nil
	})
	controller := newController(t, creator, store, submitterFunc(func(context.Context, domain.SessionID, string) (sessionruntime.TurnResult, error) {
		return sessionruntime.TurnResult{Final: "done", TerminalStatus: sessionruntime.StatusCompleted}, nil
	}), nil, telegramcontroller.Options{Settings: prefs, UIState: store, Recovered: []domain.Session{original}})
	t.Cleanup(func() { _ = controller.Close(ctx) })
	mustStatus(t, controller, message(90, "/use "+string(original.ID())))
	if _, err := controller.HandleSemanticAction(ctx, telegramcontroller.SemanticAction{Kind: telegramcontroller.SemanticSettingsStandby}); err != nil {
		t.Fatal(err)
	}
	var first domain.Session
	select {
	case first = <-created:
	case <-time.After(time.Second):
		t.Fatal("enable did not create standby")
	}
	if first.Name() != "default" {
		t.Fatalf("standby name = %q, want default", first.Name())
	}
	// Join the in-flight worker through the public ensure boundary.
	if err := controller.EnsureStandby(ctx, "local"); err != nil {
		t.Fatal(err)
	}
	result, err := controller.HandleSemanticAction(ctx, telegramcontroller.SemanticAction{Kind: telegramcontroller.SemanticMenuSessions})
	if err != nil || result.Card == nil || result.Card.SessionID != original.ID() {
		t.Fatalf("enable switched active: %#v err=%v", result, err)
	}
	if _, err := controller.HandleSemanticAction(ctx, telegramcontroller.SemanticAction{Kind: telegramcontroller.SemanticSelect, SessionID: first.ID()}); err != nil {
		t.Fatal(err)
	}
	if _, err := controller.Handle(ctx, message(91, "test user context")); err != nil {
		t.Fatal(err)
	}
	var second domain.Session
	select {
	case second = <-created:
	case <-time.After(time.Second):
		t.Fatal("first user prompt did not replenish standby")
	}
	if err := controller.EnsureStandby(ctx, "local"); err != nil {
		t.Fatal(err)
	}
	result, err = controller.HandleSemanticAction(ctx, telegramcontroller.SemanticAction{Kind: telegramcontroller.SemanticMenuSessions})
	if err != nil || result.Card == nil || result.Card.SessionID != first.ID() {
		t.Fatalf("replacement switched active: %#v err=%v", result, err)
	}
	if _, err := controller.HandleSemanticAction(ctx, telegramcontroller.SemanticAction{Kind: telegramcontroller.SemanticSettingsStandby}); err != nil {
		t.Fatal(err)
	}
	if _, err := controller.HandleSemanticAction(ctx, telegramcontroller.SemanticAction{Kind: telegramcontroller.SemanticSelect, SessionID: second.ID()}); err != nil {
		t.Fatal(err)
	}
	if _, err := controller.Handle(ctx, message(92, "second user context")); err != nil {
		t.Fatal(err)
	}
	if err := controller.EnsureStandby(ctx, "local"); err != nil {
		t.Fatal(err)
	}
	select {
	case unexpected := <-created:
		t.Fatalf("disabled created %s", unexpected.ID())
	default:
	}
}

func TestStandbyDisabledThenStableThenReplacedWithoutSwitch(t *testing.T) {
	ctx := context.Background()
	popular, changed := t.TempDir(), t.TempDir()
	original := readySession(t, "aaaaaaaa-aaaa-4aaa-9aaa-aaaaaaaaaaaa", domain.ProviderCodex, popular, "prior", 1)
	store := &standbySessions{lockedSessions: newLockedSessions(original), empty: map[domain.SessionID]bool{}}
	prefs := &testPreferences{settings: settingsport.Snapshot{CardDetail: "standard", CardPageLimit: 64, DefaultProviders: map[domain.ComputerID]domain.Provider{"local": domain.ProviderCodex}}}
	var created []domain.Session
	creator := creatorFunc(func(_ context.Context, intent app.ConfirmedSessionIntent) (app.CreateSessionResult, error) {
		id := domain.SessionID(fmt.Sprintf("bbbbbbbb-bbbb-4bbb-9bbb-%012d", len(created)+1))
		session, err := domain.NewStartingSession(id, intent.IntentID, intent.ComputerID, intent.Provider, intent.Workdir)
		if err != nil {
			return app.CreateSessionResult{}, err
		}
		session, err = session.Rename(intent.Name, domain.SessionNameDirectory)
		if err != nil {
			return app.CreateSessionResult{}, err
		}
		session, err = session.Ready(domain.ProviderBinding{Provider: intent.Provider, SessionID: "test-" + string(id), Generation: 1})
		if err != nil {
			return app.CreateSessionResult{}, err
		}
		store.Set(session)
		store.setEmpty(id, true)
		created = append(created, session)
		return app.CreateSessionResult{Session: session}, nil
	})
	controller := newController(t, creator, store, nil, nil, telegramcontroller.Options{Settings: prefs, Recovered: []domain.Session{original}})
	t.Cleanup(func() { _ = controller.Close(ctx) })
	mustStatus(t, controller, message(90, "/use "+string(original.ID())))
	if err := controller.EnsureStandby(ctx, "local"); err != nil || len(created) != 0 {
		t.Fatalf("disabled created=%d err=%v", len(created), err)
	}
	prefs.settings.StandbyEnabled = true
	if err := controller.EnsureStandby(ctx, "local"); err != nil || len(created) != 1 || created[0].Workdir() != popular || !strings.HasPrefix(string(created[0].IntentID()), "standby:") {
		t.Fatalf("create=%#v err=%v", created, err)
	}
	for i := 0; i < 3; i++ {
		store.Set(readySession(t, fmt.Sprintf("cccccccc-cccc-4ccc-9ccc-%012d", i), domain.ProviderCodex, changed, "history", 1))
	}
	if err := controller.EnsureStandby(ctx, "local"); err != nil || len(created) != 1 {
		t.Fatalf("stable count=%d err=%v", len(created), err)
	}
	store.setEmpty(created[0].ID(), false) // Same durable evidence cleared by first recorded user request.
	if err := controller.EnsureStandby(ctx, "local"); err != nil || len(created) != 2 || created[1].Workdir() != changed {
		t.Fatalf("replacement=%#v err=%v", created, err)
	}
	result, err := controller.HandleSemanticAction(ctx, telegramcontroller.SemanticAction{Kind: telegramcontroller.SemanticMenuSessions})
	if err != nil || result.Card == nil || result.Card.SessionID != original.ID() {
		t.Fatalf("active switched: %#v err=%v", result, err)
	}
	// A new controller has no in-memory standby state; the durable intent and
	// empty proof must still prevent a duplicate preparation after restart.
	restored := newController(t, nil, store, nil, nil, telegramcontroller.Options{Settings: prefs, Recovered: []domain.Session{original, created[0], created[1]}})
	t.Cleanup(func() { _ = restored.Close(ctx) })
	if err := restored.EnsureStandby(ctx, "local"); err != nil {
		t.Fatalf("restart failed to recognize persisted standby: %v", err)
	}
}

func TestStandbyReplacesEmptyAwaitingRecoveryInsteadOfLeavingNoDefault(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	stale := sessionWithIntent(t, readySession(t, "aaaaaaaa-aaaa-4aaa-9aaa-aaaaaaaaaaaa", domain.ProviderCodex, dir, "stale-provider", 1), "standby:stale")
	var err error
	stale, err = stale.AwaitRecoveryAt(stale.StateChangedAt().Add(time.Second))
	if err != nil {
		t.Fatal(err)
	}
	store := &standbySessions{lockedSessions: newLockedSessions(stale), empty: map[domain.SessionID]bool{stale.ID(): true}}
	prefs := &standbyPreferences{value: settingsport.Snapshot{StandbyEnabled: true, CardDetail: "standard", CardPageLimit: 64, DefaultProviders: map[domain.ComputerID]domain.Provider{"local": domain.ProviderCodex}, DefaultWorkdirs: map[domain.ComputerID]string{"local": dir}}}
	created := make(chan domain.Session, 1)
	creator := creatorFunc(func(_ context.Context, intent app.ConfirmedSessionIntent) (app.CreateSessionResult, error) {
		session := readySession(t, "bbbbbbbb-bbbb-4bbb-9bbb-bbbbbbbbbbbb", intent.Provider, intent.Workdir, "replacement-provider", 1)
		session = sessionWithIntent(t, session, intent.IntentID)
		session, err = session.Rename(intent.Name, domain.SessionNameDirectory)
		if err != nil {
			return app.CreateSessionResult{}, err
		}
		store.Set(session)
		store.setEmpty(session.ID(), true)
		created <- session
		return app.CreateSessionResult{Session: session}, nil
	})
	controller := newController(t, creator, store, nil, nil, telegramcontroller.Options{Settings: prefs})
	t.Cleanup(func() { _ = controller.Close(ctx) })

	if err := controller.EnsureStandby(ctx, "local"); err != nil {
		t.Fatal(err)
	}
	select {
	case replacement := <-created:
		if replacement.Name() != "default" {
			t.Fatalf("replacement name=%q", replacement.Name())
		}
	case <-time.After(time.Second):
		t.Fatal("empty awaiting-recovery standby was not replaced")
	}
	if _, err := store.Load(ctx, stale.ID()); err == nil {
		t.Fatal("stale empty standby still exists")
	}
}
