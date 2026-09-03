package telegramcontroller_test

import (
	"context"
	"errors"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"bria/internal/app"
	"bria/internal/coordinator"
	"bria/internal/domain"
	"bria/internal/sessionruntime"
	"bria/internal/settingsport"
	"bria/internal/telegramcontroller"
)

const (
	ownerID = int64(42)
	chatID  = int64(42)
)

func TestRoutesOnlyOwnerPrivateMessagesAndUnknownSlashIsAPrompt(t *testing.T) {
	controller := newController(t, creatorFunc(nil), &memorySessions{}, submitterFunc(nil), notifierFunc(nil), telegramcontroller.Options{})
	t.Cleanup(func() { _ = controller.Close(context.Background()) })

	unauthorized := message(1, "/status")
	unauthorized.ActorID++
	decision, err := controller.Handle(context.Background(), unauthorized)
	if err != nil || decision.Kind != coordinator.DecisionSkip {
		t.Fatalf("unauthorized Handle() = (%#v, %v), want skip", decision, err)
	}

	decision, err = controller.Handle(context.Background(), message(2, "/status"))
	if err != nil || decision.Kind != coordinator.DecisionStatus || !strings.Contains(decision.Status.Text, "Bria") {
		t.Fatalf("status Handle() = (%#v, %v)", decision, err)
	}

	decision, err = controller.Handle(context.Background(), message(3, "/unknown-command"))
	if err != nil || decision.Kind != coordinator.DecisionStatus || !strings.Contains(decision.Status.Text, "активной сессии") {
		t.Fatalf("unknown slash Handle() = (%#v, %v), want no-active prompt result", decision, err)
	}
}

func TestEveryCallbackIsSkippedWithoutProductEffect(t *testing.T) {
	var creates, submits, notifications int
	controller := newController(t,
		creatorFunc(func(context.Context, app.ConfirmedSessionIntent) (app.CreateSessionResult, error) {
			creates++
			return app.CreateSessionResult{}, errors.New("must not create")
		}),
		&memorySessions{},
		submitterFunc(func(context.Context, domain.SessionID, string) (sessionruntime.TurnResult, error) {
			submits++
			return sessionruntime.TurnResult{}, errors.New("must not submit")
		}),
		notifierFunc(func(context.Context, telegramcontroller.Notification) error {
			notifications++
			return errors.New("must not notify")
		}),
		telegramcontroller.Options{},
	)
	t.Cleanup(func() { _ = controller.Close(context.Background()) })

	callbacks := []coordinator.Update{
		{
			ID: 4, Kind: coordinator.UpdateCallback, ActorID: ownerID,
			ConversationID: chatID, ConversationKind: "private", Text: "/new codex /tmp",
		},
		{
			ID: 5, Kind: coordinator.UpdateCallback, ActorID: ownerID + 1,
			ConversationID: chatID + 1, ConversationKind: "group", Text: "legacy-invalid-token",
		},
	}
	for _, update := range callbacks {
		decision, err := controller.Handle(context.Background(), update)
		if err != nil || decision.Kind != coordinator.DecisionSkip {
			t.Fatalf("callback Handle() = (%#v, %v), want skip", decision, err)
		}
	}
	if creates != 0 || submits != 0 || notifications != 0 {
		t.Fatalf("callback side effects: creates=%d submits=%d notifications=%d", creates, submits, notifications)
	}
}

func TestStatusProvidesBriaMainMenuKeyboard(t *testing.T) {
	controller := newController(t, creatorFunc(nil), &memorySessions{}, submitterFunc(nil), notifierFunc(nil), telegramcontroller.Options{})
	decision, err := controller.Handle(context.Background(), coordinator.Update{
		ID: 1, Kind: coordinator.UpdateMessage, ActorID: ownerID,
		ConversationID: chatID, ConversationKind: "private", Text: "/status",
	})
	if err != nil {
		t.Fatal(err)
	}
	if decision.Keyboard == nil || len(*decision.Keyboard) != 3 || (*decision.Keyboard)[0][0].CallbackData != "menu:sessions" {
		t.Fatalf("keyboard = %#v", decision.Keyboard)
	}
}

func TestKnownMenuCallbackReturnsMenuDecision(t *testing.T) {
	controller := newController(t, creatorFunc(nil), &memorySessions{}, submitterFunc(nil), notifierFunc(nil), telegramcontroller.Options{})
	decision, err := controller.Handle(context.Background(), coordinator.Update{
		ID: 1, Kind: coordinator.UpdateCallback, ActorID: ownerID,
		ConversationID: chatID, ConversationKind: "private", CallbackQueryID: "q", SourceMessageID: 9, Text: "menu:status",
	})
	if err != nil || decision.Kind != coordinator.DecisionStatus || decision.Keyboard == nil || len(*decision.Keyboard) != 3 {
		t.Fatalf("decision = %#v, err=%v", decision, err)
	}
}

func TestCallbackCreationUsesUpdateIDAsIdempotencyIdentity(t *testing.T) {
	ready := readySession(t, "11111111-1111-4111-9111-111111111111", domain.ProviderCodex, "/tmp", "provider-1", 1)
	var got app.ConfirmedSessionIntent
	creator := creatorFunc(func(_ context.Context, intent app.ConfirmedSessionIntent) (app.CreateSessionResult, error) {
		got = intent
		return app.CreateSessionResult{Session: sessionWithIntent(t, ready, intent.IntentID)}, nil
	})
	controller := newController(t, creator, &memorySessions{byID: map[domain.SessionID]domain.Session{ready.ID(): ready}}, submitterFunc(nil), notifierFunc(nil), telegramcontroller.Options{
		CreateDrafts: createDraftSelectorFunc{workdir: "/tmp", computerID: "local"},
	})
	t.Cleanup(func() { _ = controller.Close(context.Background()) })

	decision, err := controller.Handle(context.Background(), coordinator.Update{
		ID: 99, Kind: coordinator.UpdateCallback, ActorID: ownerID,
		ConversationID: chatID, ConversationKind: "private",
		CallbackQueryID: "q", SourceMessageID: 9, Text: "new:codex",
	})
	if err != nil || decision.Kind != coordinator.DecisionStatus {
		t.Fatalf("Handle() = (%#v, %v)", decision, err)
	}
	if got.IntentID != "telegram-update:99" {
		t.Fatalf("intent id = %q, want telegram-update:99", got.IntentID)
	}
}

func TestSessionCardNeverOffersClear(t *testing.T) {
	workdir := t.TempDir()
	ready := readySession(t, "11111111-1111-4111-9111-111111111111", domain.ProviderCodex, workdir, "provider-1", 1)
	creator := creatorFunc(func(_ context.Context, intent app.ConfirmedSessionIntent) (app.CreateSessionResult, error) {
		return app.CreateSessionResult{Session: sessionWithIntent(t, ready, intent.IntentID)}, nil
	})
	controller := newController(
		t,
		creator,
		&memorySessions{byID: map[domain.SessionID]domain.Session{ready.ID(): ready}},
		submitterFunc(nil),
		notifierFunc(nil),
		telegramcontroller.Options{},
	)
	t.Cleanup(func() { _ = controller.Close(context.Background()) })

	mustStatus(t, controller, message(10, "/new codex "+workdir))
	decision := mustStatus(t, controller, message(11, "/status"))
	if decision.Keyboard == nil {
		t.Fatal("session card has no keyboard")
	}
	for _, row := range *decision.Keyboard {
		for _, button := range row {
			if button.Text == "Очистить" || button.CallbackData == "ft:clear" {
				t.Fatalf("forbidden clear action is exposed: %#v", button)
			}
		}
	}
}

func TestUnavailableSessionCannotBecomePersistedActive(t *testing.T) {
	workdir := t.TempDir()
	ready := readySession(t, "11111111-1111-4111-9111-111111111111", domain.ProviderCodex, workdir, "provider-1", 1)
	snapshot := ready.Snapshot()
	snapshot.Status = domain.SessionAwaitingRecovery
	snapshot.Binding = nil
	unavailable, err := domain.RestoreSession(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	uiState := &recordingActiveStore{}
	controller := newController(
		t,
		creatorFunc(nil),
		&memorySessions{byID: map[domain.SessionID]domain.Session{unavailable.ID(): unavailable}},
		submitterFunc(nil),
		notifierFunc(nil),
		telegramcontroller.Options{UIState: uiState},
	)
	t.Cleanup(func() { _ = controller.Close(context.Background()) })

	decision := mustStatus(t, controller, message(12, "/use "+string(unavailable.ID())))
	if !strings.Contains(decision.Status.Text, "недоступна") {
		t.Fatalf("status = %q, want unavailable", decision.Status.Text)
	}
	if len(uiState.saved) != 0 {
		t.Fatalf("persisted active sessions = %v, want none", uiState.saved)
	}
}

func TestReplyRoutesToOriginSessionWithoutChangingActiveSession(t *testing.T) {
	workdirOne := t.TempDir()
	workdirTwo := t.TempDir()
	one := readySession(t, "11111111-1111-4111-9111-111111111111", domain.ProviderCodex, workdirOne, "provider-1", 1)
	two := readySession(t, "22222222-2222-4222-9222-222222222222", domain.ProviderClaude, workdirTwo, "provider-2", 1)
	submitted := make(chan domain.SessionID, 1)
	submitter := submitterFunc(func(_ context.Context, id domain.SessionID, text string) (sessionruntime.TurnResult, error) {
		if text != "reply to background" {
			t.Fatalf("submitted text = %q", text)
		}
		submitted <- id
		return sessionruntime.TurnResult{Final: "done", TerminalStatus: sessionruntime.StatusCompleted}, nil
	})
	routes := replyRouteStoreFunc(func(_ context.Context, messageID int64) (domain.SessionID, bool, error) {
		if messageID != 500 {
			t.Fatalf("ResolveReply(%d), want 500", messageID)
		}
		return two.ID(), true, nil
	})
	sessions := &memorySessions{byID: map[domain.SessionID]domain.Session{one.ID(): one, two.ID(): two}, listed: []domain.Session{one, two}}
	controller := newController(t, creatorFunc(nil), sessions, submitter, notifierFunc(nil), telegramcontroller.Options{
		Recovered:   []domain.Session{one, two},
		ReplyRoutes: routes,
	})
	t.Cleanup(func() { _ = controller.Close(context.Background()) })

	mustStatus(t, controller, message(20, "/use "+string(one.ID())))
	reply := message(21, "reply to background")
	reply.SourceMessageID = 501
	reply.ReplyToMessageID = 500
	mustStatus(t, controller, reply)
	select {
	case got := <-submitted:
		if got != two.ID() {
			t.Fatalf("reply submitted to %q, want origin %q", got, two.ID())
		}
	case <-time.After(time.Second):
		t.Fatal("reply was not submitted")
	}
}

func TestSettingsCallbacksPersistAndTogglePreferences(t *testing.T) {
	preferences := &testPreferences{}
	controller := newController(t, creatorFunc(nil), &memorySessions{}, submitterFunc(nil), notifierFunc(nil), telegramcontroller.Options{Settings: preferences})
	decision, err := controller.Handle(context.Background(), coordinator.Update{ID: 1, Kind: coordinator.UpdateCallback, ActorID: ownerID, ConversationID: chatID, ConversationKind: "private", CallbackQueryID: "q", SourceMessageID: 9, Text: "menu:settings"})
	if err != nil || decision.Kind != coordinator.DecisionStatus || !strings.Contains(decision.Status.Text, "Screen: false") {
		t.Fatalf("settings = %#v, err=%v", decision, err)
	}
	if _, err := controller.Handle(context.Background(), coordinator.Update{ID: 2, Kind: coordinator.UpdateCallback, ActorID: ownerID, ConversationID: chatID, ConversationKind: "private", CallbackQueryID: "q2", SourceMessageID: 9, Text: "settings:screen"}); err != nil {
		t.Fatal(err)
	}
	if !preferences.settings.ScreenEnabled {
		t.Fatalf("settings after toggle screen = false")
	}
}

func TestSemanticProviderSettingsUseTypedPortWithoutCredentials(t *testing.T) {
	providers := &testProviderPreferences{values: map[domain.Provider]telegramcontroller.ProviderPreference{
		domain.ProviderCodex:  {Provider: domain.ProviderCodex, Enabled: true, Configured: true},
		domain.ProviderClaude: {Provider: domain.ProviderClaude, Enabled: false, Configured: true},
	}}
	controller := newController(t, creatorFunc(nil), &memorySessions{}, submitterFunc(nil), notifierFunc(nil), telegramcontroller.Options{
		Settings: &testPreferences{}, Providers: providers,
	})
	t.Cleanup(func() { _ = controller.Close(context.Background()) })

	initial, err := controller.HandleSemanticAction(context.Background(), telegramcontroller.SemanticAction{Kind: telegramcontroller.SemanticMenuSettings})
	if err != nil || initial.Surface == nil || !strings.Contains(initial.Surface.Text, "codex: включен, настроен") || !strings.Contains(initial.Surface.Text, "claude: выключен, настроен") {
		t.Fatalf("provider surface = (%#v, %v)", initial, err)
	}
	if _, err := controller.HandleSemanticAction(context.Background(), telegramcontroller.SemanticAction{Kind: telegramcontroller.SemanticSettingsProviderCodex}); err != nil {
		t.Fatal(err)
	}
	if providers.values[domain.ProviderCodex].Enabled {
		t.Fatal("Codex remained enabled after typed provider action")
	}
}

type testPreferences struct {
	settings settingsport.Snapshot
}

type testProviderPreferences struct {
	values map[domain.Provider]telegramcontroller.ProviderPreference
}

func (p *testProviderPreferences) Snapshot(context.Context) ([]telegramcontroller.ProviderPreference, error) {
	return []telegramcontroller.ProviderPreference{p.values[domain.ProviderCodex], p.values[domain.ProviderClaude]}, nil
}
func (p *testProviderPreferences) ToggleProvider(_ context.Context, provider domain.Provider) error {
	current := p.values[provider]
	current.Enabled = !current.Enabled
	p.values[provider] = current
	return nil
}

type recordingActiveStore struct {
	saved []domain.SessionID
}

type projectionUIState struct {
	saved   []domain.SessionID
	pages   []string
	history map[domain.SessionID][]string
}

type projectionUISnapshot struct {
	Saved []domain.SessionID
	Pages []string
}

func (s *projectionUIState) SetActiveSession(_ context.Context, id domain.SessionID) error {
	s.saved = append(s.saved, id)
	return nil
}

func (s *projectionUIState) SetCardPage(_ context.Context, id domain.SessionID, page, pages int, anchor string, latest bool) error {
	s.pages = append(s.pages, string(id)+":"+strconv.Itoa(page)+":"+strconv.Itoa(pages)+":"+anchor+":"+strconv.FormatBool(latest))
	return nil
}

func (s *projectionUIState) LoadCardHistory(_ context.Context, id domain.SessionID) ([]string, error) {
	return append([]string(nil), s.history[id]...), nil
}

func (s *projectionUIState) snapshot() projectionUISnapshot {
	return projectionUISnapshot{Saved: append([]domain.SessionID(nil), s.saved...), Pages: append([]string(nil), s.pages...)}
}

type recordingDraftSelector struct {
	computerID   domain.ComputerID
	workdir      string
	previewed    []domain.Provider
	confirms     int
	afterConfirm func()
}

func (s *recordingDraftSelector) PreviewCreateDraft(_ context.Context, provider domain.Provider) (telegramcontroller.CreateDraft, error) {
	s.previewed = append(s.previewed, provider)
	return telegramcontroller.CreateDraft{ComputerID: s.computerID, Provider: provider, Workdir: s.workdir}, nil
}

func (s *recordingDraftSelector) ConfirmCreateDraft(_ context.Context, provider domain.Provider, _ int64) (telegramcontroller.CreateDraft, error) {
	s.confirms++
	if s.afterConfirm != nil {
		s.afterConfirm()
	}
	return telegramcontroller.CreateDraft{ComputerID: s.computerID, Provider: provider, Workdir: s.workdir, Confirmed: true}, nil
}

type replyRouteStoreFunc func(context.Context, int64) (domain.SessionID, bool, error)

func (f replyRouteStoreFunc) ResolveReply(ctx context.Context, messageID int64) (domain.SessionID, bool, error) {
	return f(ctx, messageID)
}

func (s *recordingActiveStore) SetActiveSession(_ context.Context, id domain.SessionID) error {
	s.saved = append(s.saved, id)
	return nil
}

func (p *testPreferences) Snapshot(context.Context) (telegramcontroller.PreferenceSnapshot, error) {
	if p.settings.CardDetail == "" {
		p.settings = settingsport.Snapshot{ContinueExisting: true, CardDetail: "standard", ShowTechnicalActions: true, NotifyBackgroundQuestions: true, NotifyBackgroundErrors: true, SessionLifetime: "never", QueueLimit: 32, VoiceRecognition: "parakeet"}
	}
	return p.settings, nil
}
func (p *testPreferences) ToggleContinueExisting(context.Context) error {
	_, _ = p.Snapshot(context.Background())
	p.settings.ContinueExisting = !p.settings.ContinueExisting
	return nil
}
func (p *testPreferences) ToggleScreen(context.Context) error {
	_, _ = p.Snapshot(context.Background())
	p.settings.ScreenEnabled = !p.settings.ScreenEnabled
	return nil
}
func (p *testPreferences) ToggleCardDetail(context.Context) error {
	_, _ = p.Snapshot(context.Background())
	if p.settings.CardDetail == "standard" {
		p.settings.CardDetail = "compact"
	} else {
		p.settings.CardDetail = "standard"
	}
	return nil
}
func (p *testPreferences) ToggleTechnicalActions(context.Context) error {
	_, _ = p.Snapshot(context.Background())
	p.settings.ShowTechnicalActions = !p.settings.ShowTechnicalActions
	return nil
}
func (p *testPreferences) ToggleBackgroundQuestions(context.Context) error {
	_, _ = p.Snapshot(context.Background())
	p.settings.NotifyBackgroundQuestions = !p.settings.NotifyBackgroundQuestions
	return nil
}
func (p *testPreferences) ToggleBackgroundErrors(context.Context) error {
	_, _ = p.Snapshot(context.Background())
	p.settings.NotifyBackgroundErrors = !p.settings.NotifyBackgroundErrors
	return nil
}
func (p *testPreferences) SetSessionLifetime(_ context.Context, lifetime string) error {
	_, _ = p.Snapshot(context.Background())
	p.settings.SessionLifetime = lifetime
	return nil
}

func TestNewSetsActiveAndWorkerEmitsOrderedTaggedNotifications(t *testing.T) {
	workdir := t.TempDir()
	ready := readySession(t, "11111111-1111-4111-9111-111111111111", domain.ProviderCodex, workdir, "provider-1", 1)
	var gotIntent app.ConfirmedSessionIntent
	creator := creatorFunc(func(_ context.Context, intent app.ConfirmedSessionIntent) (app.CreateSessionResult, error) {
		gotIntent = intent
		return app.CreateSessionResult{Session: sessionWithIntent(t, ready, intent.IntentID)}, nil
	})
	notifications := make(chan telegramcontroller.Notification, 4)
	notifier := notifierFunc(func(_ context.Context, notification telegramcontroller.Notification) error {
		notifications <- notification
		return nil
	})
	submitter := submitterFunc(func(_ context.Context, id domain.SessionID, text string) (sessionruntime.TurnResult, error) {
		if id != ready.ID() || text != "/unknown-is-prompt" {
			t.Fatalf("Submit(%q, %q)", id, text)
		}
		return sessionruntime.TurnResult{
			Events: []sessionruntime.TurnEvent{
				{Kind: sessionruntime.EventCommentary, Text: "working"},
				{Kind: sessionruntime.EventQuestion, Text: "question"},
			},
			Final:          "done",
			TerminalStatus: sessionruntime.StatusCompleted,
		}, nil
	})
	controller := newController(t, creator, &memorySessions{byID: map[domain.SessionID]domain.Session{ready.ID(): ready}}, submitter, notifier, telegramcontroller.Options{})
	t.Cleanup(func() { _ = controller.Close(context.Background()) })

	decision, err := controller.Handle(context.Background(), message(10, "/new codex "+workdir))
	if err != nil || decision.Kind != coordinator.DecisionStatus || !strings.Contains(decision.Status.Text, string(ready.ID())) || !strings.Contains(decision.Status.Text, "готова") {
		t.Fatalf("new Handle() = (%#v, %v)", decision, err)
	}
	if gotIntent.IntentID != "telegram-update:10" || gotIntent.ComputerID != "local" || gotIntent.Provider != domain.ProviderCodex || gotIntent.Workdir != workdir {
		t.Fatalf("Create intent = %#v", gotIntent)
	}

	decision, err = controller.Handle(context.Background(), message(11, "/unknown-is-prompt"))
	if err != nil || decision.Kind != coordinator.DecisionStatus || !strings.Contains(decision.Status.Text, "принят") {
		t.Fatalf("prompt Handle() = (%#v, %v), want immediate acceptance", decision, err)
	}
	wantKinds := []telegramcontroller.NotificationKind{
		telegramcontroller.NotificationCommentary,
		telegramcontroller.NotificationQuestion,
		telegramcontroller.NotificationFinal,
	}
	for index, wantKind := range wantKinds {
		select {
		case got := <-notifications:
			if got.Kind != wantKind || got.SessionID != ready.ID() || got.ConversationID != chatID {
				t.Fatalf("notification %d = %#v", index, got)
			}
		case <-time.After(time.Second):
			t.Fatalf("notification %d not emitted", index)
		}
	}
}

func TestPerSessionFIFOAndCrossSessionConcurrency(t *testing.T) {
	workdirOne := t.TempDir()
	workdirTwo := t.TempDir()
	one := readySession(t, "11111111-1111-4111-9111-111111111111", domain.ProviderCodex, workdirOne, "provider-1", 1)
	two := readySession(t, "22222222-2222-4222-9222-222222222222", domain.ProviderClaude, workdirTwo, "provider-2", 1)
	creator := creatorFunc(func(_ context.Context, intent app.ConfirmedSessionIntent) (app.CreateSessionResult, error) {
		if intent.Provider == domain.ProviderCodex {
			return app.CreateSessionResult{Session: sessionWithIntent(t, one, intent.IntentID)}, nil
		}
		return app.CreateSessionResult{Session: sessionWithIntent(t, two, intent.IntentID)}, nil
	})
	started := make(chan string, 4)
	releases := map[string]chan struct{}{
		"one-first":  make(chan struct{}),
		"one-second": make(chan struct{}),
		"two-first":  make(chan struct{}),
	}
	submitter := submitterFunc(func(ctx context.Context, _ domain.SessionID, text string) (sessionruntime.TurnResult, error) {
		started <- text
		select {
		case <-releases[text]:
			return sessionruntime.TurnResult{Final: text, TerminalStatus: sessionruntime.StatusCompleted}, nil
		case <-ctx.Done():
			return sessionruntime.TurnResult{}, ctx.Err()
		}
	})
	store := &memorySessions{byID: map[domain.SessionID]domain.Session{one.ID(): one, two.ID(): two}}
	controller := newController(t, creator, store, submitter, notifierFunc(func(context.Context, telegramcontroller.Notification) error { return nil }), telegramcontroller.Options{QueueLimit: 2})
	t.Cleanup(func() { _ = controller.Close(context.Background()) })

	mustStatus(t, controller, message(20, "/new codex "+workdirOne))
	mustStatus(t, controller, message(21, "one-first"))
	if got := waitString(t, started); got != "one-first" {
		t.Fatalf("first started = %q", got)
	}
	mustStatus(t, controller, message(22, "one-second"))
	select {
	case got := <-started:
		t.Fatalf("same-session second started before first completed: %q", got)
	case <-time.After(40 * time.Millisecond):
	}

	mustStatus(t, controller, message(23, "/new claude "+workdirTwo))
	mustStatus(t, controller, message(24, "two-first"))
	if got := waitString(t, started); got != "two-first" {
		t.Fatalf("cross-session started = %q, want two-first", got)
	}
	close(releases["two-first"])
	close(releases["one-first"])
	if got := waitString(t, started); got != "one-second" {
		t.Fatalf("FIFO next started = %q, want one-second", got)
	}
	close(releases["one-second"])
}

func TestStopCancelsOnlyCurrentSessionTurn(t *testing.T) {
	workdir := t.TempDir()
	ready := readySession(t, "11111111-1111-4111-9111-111111111111", domain.ProviderCodex, workdir, "provider-1", 1)
	creator := creatorFunc(func(_ context.Context, intent app.ConfirmedSessionIntent) (app.CreateSessionResult, error) {
		return app.CreateSessionResult{Session: sessionWithIntent(t, ready, intent.IntentID)}, nil
	})
	started := make(chan struct{}, 1)
	stopped := make(chan struct{})
	submitter := submitterFunc(func(ctx context.Context, _ domain.SessionID, _ string) (sessionruntime.TurnResult, error) {
		close(started)
		<-ctx.Done()
		close(stopped)
		return sessionruntime.TurnResult{}, ctx.Err()
	})
	controller := newController(t, creator, &memorySessions{byID: map[domain.SessionID]domain.Session{ready.ID(): ready}}, submitter, notifierFunc(func(context.Context, telegramcontroller.Notification) error { return nil }), telegramcontroller.Options{})
	t.Cleanup(func() { _ = controller.Close(context.Background()) })
	mustStatus(t, controller, message(30, "/new codex "+workdir))
	mustStatus(t, controller, message(31, "long turn"))
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("turn did not start")
	}
	decision := mustStatus(t, controller, message(32, "/stop"))
	if !strings.Contains(decision.Status.Text, "Остановка") {
		t.Fatalf("stop status = %q", decision.Status.Text)
	}
	select {
	case <-stopped:
	case <-time.After(time.Second):
		t.Fatal("current turn was not cancelled")
	}
}

func TestStopperConfirmsInterruptedTerminalWithoutCancellingSubmitOrReleasingQueue(t *testing.T) {
	workdir := t.TempDir()
	ready := readySession(t, "11111111-1111-4111-9111-111111111111", domain.ProviderCodex, workdir, "provider-1", 1)
	creator := creatorFunc(func(_ context.Context, intent app.ConfirmedSessionIntent) (app.CreateSessionResult, error) {
		return app.CreateSessionResult{Session: sessionWithIntent(t, ready, intent.IntentID)}, nil
	})
	started := make(chan string, 2)
	terminal := make(chan struct{})
	contextCancelled := make(chan struct{}, 1)
	submitter := submitterFunc(func(ctx context.Context, _ domain.SessionID, text string) (sessionruntime.TurnResult, error) {
		started <- text
		if text != "first" {
			return sessionruntime.TurnResult{Final: "done", TerminalStatus: sessionruntime.StatusCompleted}, nil
		}
		select {
		case <-terminal:
			return sessionruntime.TurnResult{TerminalStatus: sessionruntime.StatusInterrupted}, sessionruntime.ErrTurnFailed
		case <-ctx.Done():
			contextCancelled <- struct{}{}
			return sessionruntime.TurnResult{}, ctx.Err()
		}
	})
	stopEntered := make(chan domain.SessionID, 1)
	releaseStop := make(chan struct{})
	stopper := stopperFunc(func(_ context.Context, id domain.SessionID) error {
		stopEntered <- id
		<-releaseStop
		close(terminal)
		return nil
	})
	controller := newController(t, creator, &memorySessions{byID: map[domain.SessionID]domain.Session{ready.ID(): ready}}, submitter, notifierFunc(nil), telegramcontroller.Options{Stopper: stopper, QueueLimit: 1})
	t.Cleanup(func() { _ = controller.Close(context.Background()) })

	mustStatus(t, controller, message(33, "/new codex "+workdir))
	mustStatus(t, controller, message(34, "first"))
	if got := waitString(t, started); got != "first" {
		t.Fatalf("started = %q, want first", got)
	}
	mustStatus(t, controller, message(35, "second"))

	decision := make(chan coordinator.Decision, 1)
	go func() { decision <- mustStatus(t, controller, message(36, "/stop")) }()
	if got := <-stopEntered; got != ready.ID() {
		t.Fatalf("StopCurrent session = %q, want %q", got, ready.ID())
	}
	select {
	case got := <-decision:
		t.Fatalf("stop returned before provider terminal: %#v", got)
	case <-time.After(40 * time.Millisecond):
	}
	select {
	case <-contextCancelled:
		t.Fatal("confirmed stop also cancelled Submit context")
	default:
	}
	select {
	case got := <-started:
		t.Fatalf("queued turn started before interrupted terminal: %q", got)
	default:
	}

	close(releaseStop)
	select {
	case got := <-decision:
		if !strings.Contains(got.Status.Text, "остановлен") {
			t.Fatalf("confirmed stop status = %q", got.Status.Text)
		}
	case <-time.After(time.Second):
		t.Fatal("stop did not return after interrupted terminal")
	}
	if got := waitString(t, started); got != "second" {
		t.Fatalf("post-stop turn = %q, want second", got)
	}
}

func TestConcurrentConfirmedStopStartsOnlyOneInterrupt(t *testing.T) {
	workdir := t.TempDir()
	ready := readySession(t, "11111111-1111-4111-9111-111111111111", domain.ProviderCodex, workdir, "provider-1", 1)
	creator := creatorFunc(func(_ context.Context, intent app.ConfirmedSessionIntent) (app.CreateSessionResult, error) {
		return app.CreateSessionResult{Session: sessionWithIntent(t, ready, intent.IntentID)}, nil
	})
	started := make(chan struct{})
	terminal := make(chan struct{})
	submitter := submitterFunc(func(context.Context, domain.SessionID, string) (sessionruntime.TurnResult, error) {
		close(started)
		<-terminal
		return sessionruntime.TurnResult{TerminalStatus: sessionruntime.StatusInterrupted}, sessionruntime.ErrTurnFailed
	})
	stopEntered := make(chan struct{})
	releaseStop := make(chan struct{})
	var once sync.Once
	stopper := stopperFunc(func(context.Context, domain.SessionID) error {
		once.Do(func() { close(stopEntered) })
		<-releaseStop
		close(terminal)
		return nil
	})
	controller := newController(t, creator, &memorySessions{byID: map[domain.SessionID]domain.Session{ready.ID(): ready}}, submitter, notifierFunc(nil), telegramcontroller.Options{Stopper: stopper})
	t.Cleanup(func() { _ = controller.Close(context.Background()) })
	mustStatus(t, controller, message(37, "/new codex "+workdir))
	mustStatus(t, controller, message(38, "first"))
	<-started

	first := make(chan coordinator.Decision, 1)
	go func() { first <- mustStatus(t, controller, message(39, "/stop")) }()
	<-stopEntered
	second := mustStatus(t, controller, message(40, "/stop"))
	if !strings.Contains(second.Status.Text, "уже") {
		t.Fatalf("concurrent stop status = %q, want already stopping", second.Status.Text)
	}
	close(releaseStop)
	select {
	case <-first:
	case <-time.After(time.Second):
		t.Fatal("first stop did not finish")
	}
}

func TestCaptionOnlyMessageIsSubmittedAsPrompt(t *testing.T) {
	workdir := t.TempDir()
	ready := readySession(t, "11111111-1111-4111-9111-111111111111", domain.ProviderCodex, workdir, "provider-1", 1)
	creator := creatorFunc(func(_ context.Context, intent app.ConfirmedSessionIntent) (app.CreateSessionResult, error) {
		return app.CreateSessionResult{Session: sessionWithIntent(t, ready, intent.IntentID)}, nil
	})
	submitted := make(chan string, 1)
	submitter := submitterFunc(func(_ context.Context, _ domain.SessionID, text string) (sessionruntime.TurnResult, error) {
		submitted <- text
		return sessionruntime.TurnResult{TerminalStatus: sessionruntime.StatusCompleted}, nil
	})
	controller := newController(t, creator, &memorySessions{byID: map[domain.SessionID]domain.Session{ready.ID(): ready}}, submitter, notifierFunc(nil), telegramcontroller.Options{})
	t.Cleanup(func() { _ = controller.Close(context.Background()) })
	mustStatus(t, controller, message(41, "/new codex "+workdir))
	update := message(42, "")
	update.Caption = "  inspect this image  "
	mustStatus(t, controller, update)
	if got := waitString(t, submitted); got != "inspect this image" {
		t.Fatalf("caption prompt = %q, want normalized caption", got)
	}
}

func TestVoiceAndPhotoUseInjectedPreparerWhileVideoNeverDoes(t *testing.T) {
	workdir := t.TempDir()
	ready := readySession(t, "11111111-1111-4111-9111-111111111111", domain.ProviderCodex, workdir, "provider-1", 1)
	creator := creatorFunc(func(_ context.Context, intent app.ConfirmedSessionIntent) (app.CreateSessionResult, error) {
		return app.CreateSessionResult{Session: sessionWithIntent(t, ready, intent.IntentID)}, nil
	})
	submitted := make(chan string, 3)
	submitter := submitterFunc(func(_ context.Context, _ domain.SessionID, text string) (sessionruntime.TurnResult, error) {
		submitted <- text
		return sessionruntime.TurnResult{TerminalStatus: sessionruntime.StatusCompleted}, nil
	})
	prepared := make(chan telegramcontroller.IncomingInput, 2)
	preparer := inputPreparerFunc(func(_ context.Context, input telegramcontroller.IncomingInput) (string, error) {
		prepared <- input
		switch input.Kind {
		case "voice":
			return "voice transcript", nil
		case "photo":
			return "prepared photo", nil
		default:
			return "", errors.New("video or unsupported input reached preparer")
		}
	})
	controller := newController(t, creator, &memorySessions{byID: map[domain.SessionID]domain.Session{ready.ID(): ready}}, submitter, notifierFunc(nil), telegramcontroller.Options{InputPreparer: preparer})
	t.Cleanup(func() { _ = controller.Close(context.Background()) })
	mustStatus(t, controller, message(43, "/new codex "+workdir))

	voice := message(44, "")
	voice.Caption = "voice caption"
	voice.MediaKind = "voice"
	voice.MediaFileID = "voice-file"
	voice.MediaFileUniqueID = "voice-unique"
	voice.MediaFileSize = 4096
	voice.MediaMIMEType = "audio/ogg"
	voice.MediaDurationSeconds = 7
	voice.MediaDownloadAllowed = true
	mustStatus(t, controller, voice)
	if got := waitString(t, submitted); got != "voice caption\n\nvoice transcript" {
		t.Fatalf("voice prompt = %q", got)
	}
	if got := <-prepared; got.Kind != "voice" || got.FileID != "voice-file" || !got.DownloadPermitted {
		t.Fatalf("voice preparer input = %#v", got)
	}

	photo := message(45, "")
	photo.MediaKind = "photo"
	photo.MediaFileID = "photo-file"
	photo.MediaFileUniqueID = "photo-unique"
	photo.MediaWidth = 1280
	photo.MediaHeight = 720
	photo.MediaDownloadAllowed = true
	mustStatus(t, controller, photo)
	if got := waitString(t, submitted); got != "prepared photo" {
		t.Fatalf("photo prompt = %q", got)
	}
	if got := <-prepared; got.Kind != "photo" || got.Width != 1280 || got.Height != 720 {
		t.Fatalf("photo preparer input = %#v", got)
	}

	video := message(46, "")
	video.Caption = "video caption"
	video.MediaKind = "video"
	video.MediaFileID = "video-file"
	video.MediaFileUniqueID = "video-unique"
	video.MediaDurationSeconds = 12
	video.MediaWidth = 1920
	video.MediaHeight = 1080
	video.MediaDownloadAllowed = false
	mustStatus(t, controller, video)
	videoPrompt := waitString(t, submitted)
	if !strings.Contains(videoPrompt, "video caption") || !strings.Contains(videoPrompt, "Видео") || !strings.Contains(videoPrompt, "12") {
		t.Fatalf("video prompt = %q, want caption and metadata", videoPrompt)
	}
	select {
	case got := <-prepared:
		t.Fatalf("video unexpectedly reached preparer: %#v", got)
	default:
	}
}

func TestDocumentInputRequiresExplicitPolicyAndPreparer(t *testing.T) {
	workdir := t.TempDir()
	ready := readySession(t, "11111111-1111-4111-9111-111111111111", domain.ProviderCodex, workdir, "provider-1", 1)
	creator := creatorFunc(func(_ context.Context, intent app.ConfirmedSessionIntent) (app.CreateSessionResult, error) {
		return app.CreateSessionResult{Session: sessionWithIntent(t, ready, intent.IntentID)}, nil
	})
	submitted := make(chan string, 1)
	submitter := submitterFunc(func(_ context.Context, _ domain.SessionID, text string) (sessionruntime.TurnResult, error) {
		submitted <- text
		return sessionruntime.TurnResult{TerminalStatus: sessionruntime.StatusCompleted}, nil
	})
	preparer := inputPreparerFunc(func(context.Context, telegramcontroller.IncomingInput) (string, error) {
		return "verified staged document", nil
	})
	controller := newController(t, creator, &memorySessions{byID: map[domain.SessionID]domain.Session{ready.ID(): ready}}, submitter, notifierFunc(nil), telegramcontroller.Options{InputPreparer: preparer})
	t.Cleanup(func() { _ = controller.Close(context.Background()) })
	mustStatus(t, controller, message(47, "/new codex "+workdir))
	document := message(48, "")
	document.Caption = "document caption"
	document.MediaKind = "document"
	document.MediaFileID = "document-file"
	document.MediaFileUniqueID = "document-unique"
	decision := mustStatus(t, controller, document)
	if !strings.Contains(decision.Status.Text, "документ") {
		t.Fatalf("default document status = %q", decision.Status.Text)
	}
	select {
	case got := <-submitted:
		t.Fatalf("document submitted without explicit policy: %q", got)
	default:
	}

	allowed := newController(t, creator, &memorySessions{byID: map[domain.SessionID]domain.Session{ready.ID(): ready}}, submitter, notifierFunc(nil), telegramcontroller.Options{InputPreparer: preparer, AllowDocumentInput: true})
	t.Cleanup(func() { _ = allowed.Close(context.Background()) })
	mustStatus(t, allowed, message(49, "/new codex "+workdir))
	mustStatus(t, allowed, document)
	if got := waitString(t, submitted); got != "document caption\n\nverified staged document" {
		t.Fatalf("allowed document prompt = %q", got)
	}
}

func TestArchiveListsOnlyArchivedSessions(t *testing.T) {
	archived := archivedSession(t, "11111111-1111-4111-9111-111111111111", domain.ProviderCodex, "/archived", "provider-archived", 1)
	awaiting := readySession(t, "22222222-2222-4222-9222-222222222222", domain.ProviderClaude, "/recovering", "provider-recovering", 1)
	awaitingSnapshot := awaiting.Snapshot()
	awaitingSnapshot.Status = domain.SessionAwaitingRecovery
	recovery := domain.SessionReady
	awaitingSnapshot.RecoveryTarget = &recovery
	awaiting, err := domain.RestoreSession(awaitingSnapshot)
	if err != nil {
		t.Fatalf("RestoreSession(awaiting) error = %v", err)
	}
	store := &memorySessions{listed: []domain.Session{awaiting, archived}}
	controller := newController(t, creatorFunc(nil), store, submitterFunc(nil), notifierFunc(nil), telegramcontroller.Options{})
	t.Cleanup(func() { _ = controller.Close(context.Background()) })
	decision := mustStatus(t, controller, coordinator.Update{
		ID: 50, Kind: coordinator.UpdateCallback, ActorID: ownerID,
		ConversationID: chatID, ConversationKind: "private", CallbackQueryID: "q", SourceMessageID: 10, Text: "mm:arch",
	})
	if !strings.Contains(decision.Status.Text, "11111111") || strings.Contains(decision.Status.Text, "22222222") {
		t.Fatalf("archive = %q, want archived only", decision.Status.Text)
	}
}

func TestResumeArchivedActivatesOnlyExactOriginalProviderSession(t *testing.T) {
	archived := archivedSession(t, "11111111-1111-4111-9111-111111111111", domain.ProviderCodex, "/archived", "provider-original", 3)
	resuming, err := archived.BeginResume(archived.StateChangedAt().Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	resumed, err := resuming.ResumeReady(domain.ProviderBinding{Provider: domain.ProviderCodex, SessionID: "provider-original", Generation: 4}, archived.StateChangedAt().Add(2*time.Minute), domain.SessionLifetimeNever)
	if err != nil {
		t.Fatal(err)
	}
	store := &memorySessions{byID: map[domain.SessionID]domain.Session{archived.ID(): archived}, listed: []domain.Session{archived}}
	resumer := archivedResumerFunc(func(_ context.Context, id domain.SessionID) (domain.Session, error) {
		if id != archived.ID() {
			t.Fatalf("resume id = %q", id)
		}
		store.byID[id] = resumed
		store.listed = []domain.Session{resumed}
		return resumed, nil
	})
	submitted := make(chan string, 1)
	controller := newController(t, creatorFunc(nil), store, submitterFunc(func(_ context.Context, id domain.SessionID, text string) (sessionruntime.TurnResult, error) {
		if id != resumed.ID() {
			t.Fatalf("submit id = %q", id)
		}
		submitted <- text
		return sessionruntime.TurnResult{TerminalStatus: sessionruntime.StatusCompleted}, nil
	}), notifierFunc(nil), telegramcontroller.Options{ArchivedResumer: resumer})
	t.Cleanup(func() { _ = controller.Close(context.Background()) })
	decision, err := controller.ResumeArchived(context.Background(), archived.ID())
	if err != nil || decision.Kind != coordinator.DecisionStatus || !strings.Contains(decision.Status.Text, string(archived.ID())) {
		t.Fatalf("ResumeArchived() = (%#v, %v)", decision, err)
	}
	mustStatus(t, controller, message(51, "continued work"))
	if got := waitString(t, submitted); got != "continued work" {
		t.Fatalf("resumed prompt = %q", got)
	}
}

func TestCloseSessionArchivesAndRemovesActiveSession(t *testing.T) {
	ready := readySession(t, "11111111-1111-4111-9111-111111111111", domain.ProviderClaude, "/work", "provider-original", 1)
	archived := archivedSession(t, string(ready.ID()), ready.Provider(), ready.Workdir(), "provider-original", 1)
	store := &memorySessions{byID: map[domain.SessionID]domain.Session{ready.ID(): ready}, listed: []domain.Session{ready}}
	closer := sessionCloserFunc(func(_ context.Context, id domain.SessionID) (app.CloseSessionResult, error) {
		store.byID[id] = archived
		store.listed = []domain.Session{archived}
		return app.CloseSessionResult{Session: archived}, nil
	})
	controller := newController(t, creatorFunc(nil), store, submitterFunc(nil), notifierFunc(nil), telegramcontroller.Options{Recovered: []domain.Session{ready}, SessionCloser: closer})
	t.Cleanup(func() { _ = controller.Close(context.Background()) })
	mustStatus(t, controller, message(52, "/use "+string(ready.ID())))
	decision, err := controller.CloseSession(context.Background(), ready.ID())
	if err != nil || !strings.Contains(decision.Status.Text, "11111111") {
		t.Fatalf("CloseSession() = (%#v, %v)", decision, err)
	}
	decision = mustStatus(t, controller, message(53, "must not run"))
	if !strings.Contains(decision.Status.Text, "Нет активной") {
		t.Fatalf("post-close prompt status = %q", decision.Status.Text)
	}
}

func TestStopDoesNotInterruptWhenDurableStoppingTransitionFails(t *testing.T) {
	workdir := t.TempDir()
	ready := readySession(t, "11111111-1111-4111-9111-111111111111", domain.ProviderCodex, workdir, "provider-1", 1)
	running, _ := ready.StartWork(ready.StateChangedAt().Add(time.Minute))
	creator := creatorFunc(func(_ context.Context, intent app.ConfirmedSessionIntent) (app.CreateSessionResult, error) {
		return app.CreateSessionResult{Session: sessionWithIntent(t, ready, intent.IntentID)}, nil
	})
	started := make(chan struct{})
	release := make(chan struct{})
	submitter := submitterFunc(func(context.Context, domain.SessionID, string) (sessionruntime.TurnResult, error) {
		close(started)
		<-release
		return sessionruntime.TurnResult{TerminalStatus: sessionruntime.StatusCompleted}, nil
	})
	interrupts := 0
	stopper := stopperFunc(func(context.Context, domain.SessionID) error { interrupts++; return nil })
	lifecycle := turnLifecycleFunc{
		start: func(context.Context, domain.SessionID) (domain.Session, error) { return running, nil },
		stop: func(context.Context, domain.SessionID) (domain.Session, error) {
			return domain.Session{}, errors.New("persist failed")
		},
		finish: func(context.Context, domain.SessionID) (domain.Session, bool, error) {
			return ready, false, nil
		},
	}
	controller := newController(t, creator, &memorySessions{byID: map[domain.SessionID]domain.Session{ready.ID(): ready}}, submitter, notifierFunc(nil), telegramcontroller.Options{Stopper: stopper, TurnLifecycle: lifecycle})
	t.Cleanup(func() { _ = controller.Close(context.Background()) })
	mustStatus(t, controller, message(54, "/new codex "+workdir))
	mustStatus(t, controller, message(55, "work"))
	<-started
	decision := mustStatus(t, controller, message(56, "/stop"))
	if interrupts != 0 || !strings.Contains(decision.Status.Text, "сохранить") {
		t.Fatalf("failed durable stop = (%d interrupts, %q)", interrupts, decision.Status.Text)
	}
	close(release)
}

func TestTurnLifecycleDurablyBracketsProviderSubmit(t *testing.T) {
	workdir := t.TempDir()
	ready := readySession(t, "11111111-1111-4111-9111-111111111111", domain.ProviderCodex, workdir, "provider-1", 1)
	creator := creatorFunc(func(_ context.Context, intent app.ConfirmedSessionIntent) (app.CreateSessionResult, error) {
		return app.CreateSessionResult{Session: sessionWithIntent(t, ready, intent.IntentID)}, nil
	})
	store := &memorySessions{byID: map[domain.SessionID]domain.Session{ready.ID(): ready}}
	events := make(chan string, 6)
	var stateMu sync.Mutex
	current := ready
	lifecycle := turnLifecycleFunc{
		start: func(context.Context, domain.SessionID) (domain.Session, error) {
			stateMu.Lock()
			defer stateMu.Unlock()
			next, err := current.StartWork(time.Now().UTC())
			if err == nil {
				current = next
				store.byID[next.ID()] = next
			}
			events <- "start"
			return next, err
		},
		stop: func(context.Context, domain.SessionID) (domain.Session, error) {
			return domain.Session{}, errors.New("unexpected stop")
		},
		finish: func(context.Context, domain.SessionID) (domain.Session, bool, error) {
			stateMu.Lock()
			defer stateMu.Unlock()
			next, err := current.FinishWork(time.Now().UTC())
			if err == nil {
				current = next
				store.byID[next.ID()] = next
			}
			events <- "finish"
			return next, false, err
		},
	}
	submitter := submitterFunc(func(_ context.Context, _ domain.SessionID, text string) (sessionruntime.TurnResult, error) {
		events <- "submit:" + text
		return sessionruntime.TurnResult{TerminalStatus: sessionruntime.StatusCompleted}, nil
	})
	controller := newController(t, creator, store, submitter, notifierFunc(nil), telegramcontroller.Options{TurnLifecycle: lifecycle})
	t.Cleanup(func() { _ = controller.Close(context.Background()) })
	mustStatus(t, controller, message(57, "/new codex "+workdir))
	mustStatus(t, controller, message(58, "one"))
	want := []string{"start", "submit:one", "finish"}
	for _, expected := range want {
		select {
		case got := <-events:
			if got != expected {
				t.Fatalf("lifecycle event = %q, want %q", got, expected)
			}
		case <-time.After(time.Second):
			t.Fatalf("missing lifecycle event %q", expected)
		}
	}
	mustStatus(t, controller, message(59, "two"))
	for _, expected := range []string{"start", "submit:two", "finish"} {
		select {
		case got := <-events:
			if got != expected {
				t.Fatalf("second lifecycle event = %q, want %q", got, expected)
			}
		case <-time.After(time.Second):
			t.Fatalf("missing second lifecycle event %q", expected)
		}
	}
}

func TestInteractiveSubmitStreamsEventOnceAndResolvesCorrelatedInteraction(t *testing.T) {
	workdir := t.TempDir()
	ready := readySession(t, "11111111-1111-4111-9111-111111111111", domain.ProviderCodex, workdir, "provider-1", 1)
	creator := creatorFunc(func(_ context.Context, intent app.ConfirmedSessionIntent) (app.CreateSessionResult, error) {
		return app.CreateSessionResult{Session: sessionWithIntent(t, ready, intent.IntentID)}, nil
	})
	streamed := make(chan telegramcontroller.Notification, 3)
	resolved := make(chan sessionruntime.InteractionRequest, 1)
	interactive := &interactiveSubmitter{
		submitWithCallbacks: func(ctx context.Context, id domain.SessionID, text string, callbacks sessionruntime.TurnCallbacks) (sessionruntime.TurnResult, error) {
			if id != ready.ID() || text != "interactive" || callbacks.MessageID != "telegram-update:111" {
				t.Fatalf("interactive submit identity = (%q, %q, %q)", id, text, callbacks.MessageID)
			}
			event := sessionruntime.TurnEvent{Kind: sessionruntime.EventCommentary, Text: "streamed once"}
			if err := callbacks.OnEvent(event); err != nil {
				return sessionruntime.TurnResult{}, err
			}
			request := sessionruntime.InteractionRequest{ID: "interaction-1", Kind: "question", ItemID: "item-1", Blocking: true}
			response, err := callbacks.OnInteraction(ctx, request)
			if err != nil || response.ID != request.ID || response.Outcome != "answered" || response.Answers["choice"][0] != "yes" {
				t.Fatalf("interaction response = (%#v, %v)", response, err)
			}
			return sessionruntime.TurnResult{Events: []sessionruntime.TurnEvent{event}, Final: "done", TerminalStatus: sessionruntime.StatusCompleted}, nil
		},
	}
	controller := newController(t, creator, &memorySessions{byID: map[domain.SessionID]domain.Session{ready.ID(): ready}}, interactive, notifierFunc(func(_ context.Context, notification telegramcontroller.Notification) error {
		streamed <- notification
		return nil
	}), telegramcontroller.Options{Interactions: interactionHandlerFunc(func(_ context.Context, envelope telegramcontroller.InteractionEnvelope) (sessionruntime.InteractionResponse, error) {
		if envelope.SessionID != ready.ID() || envelope.MessageID != "telegram-update:111" {
			t.Fatalf("interaction envelope = %#v", envelope)
		}
		request := envelope.Request
		resolved <- request
		return sessionruntime.InteractionResponse{ID: request.ID, Outcome: "answered", Answers: map[string][]string{"choice": {"yes"}}}, nil
	})})
	t.Cleanup(func() { _ = controller.Close(context.Background()) })
	mustStatus(t, controller, message(110, "/new codex "+workdir))
	mustStatus(t, controller, message(111, "interactive"))
	select {
	case request := <-resolved:
		if request.ID != "interaction-1" || request.ItemID != "item-1" {
			t.Fatalf("resolved request = %#v", request)
		}
	case <-time.After(time.Second):
		t.Fatal("interaction was not resolved")
	}
	first := <-streamed
	second := <-streamed
	if first.Kind != telegramcontroller.NotificationCommentary || first.Text != "streamed once" || first.OperationID != "telegram-update:111:event:1" {
		t.Fatalf("streamed notification = %#v", first)
	}
	if second.Kind != telegramcontroller.NotificationFinal || second.OperationID != "telegram-update:111:final" {
		t.Fatalf("final notification = %#v", second)
	}
	select {
	case duplicate := <-streamed:
		t.Fatalf("interactive event replayed from TurnResult: %#v", duplicate)
	case <-time.After(20 * time.Millisecond):
	}
	if interactive.plainCalls != 0 {
		t.Fatalf("plain Submit calls = %d", interactive.plainCalls)
	}
}

func TestProcessDurableInputCommitsExactAcceptanceBeforeEventsAndCompletion(t *testing.T) {
	ready := readySession(t, "33333333-3333-4333-9333-333333333333", domain.ProviderCodex, t.TempDir(), "provider-3", 1)
	order := make([]string, 0, 3)
	interactive := &interactiveSubmitter{submitWithCallbacks: func(_ context.Context, id domain.SessionID, text string, callbacks sessionruntime.TurnCallbacks) (sessionruntime.TurnResult, error) {
		if id != ready.ID() || text != "durable text" || callbacks.MessageID != "telegram-update:301" {
			t.Fatalf("provider input = %q/%q/%q", id, text, callbacks.MessageID)
		}
		if err := callbacks.OnAccepted(callbacks.MessageID); err != nil {
			return sessionruntime.TurnResult{}, err
		}
		order = append(order, "provider-after-accepted")
		if err := callbacks.OnEvent(sessionruntime.TurnEvent{Kind: sessionruntime.EventCommentary, Text: "working"}); err != nil {
			return sessionruntime.TurnResult{}, err
		}
		return sessionruntime.TurnResult{TerminalStatus: sessionruntime.StatusCompleted, Final: "done"}, nil
	}}
	controller := newController(t, creatorFunc(nil), &memorySessions{byID: map[domain.SessionID]domain.Session{ready.ID(): ready}}, interactive, notifierFunc(func(context.Context, telegramcontroller.Notification) error { return nil }), telegramcontroller.Options{Recovered: []domain.Session{ready}})
	t.Cleanup(func() { _ = controller.Close(context.Background()) })
	receipt, err := controller.ProcessDurableInput(context.Background(), telegramcontroller.DurableLeasedInput{
		SessionID: ready.ID(), MessageID: "telegram-update:301", Sequence: 7, Payload: []byte("durable text"),
	}, telegramcontroller.DurableInputCallbacks{OnAccepted: func(_ context.Context, acceptance telegramcontroller.DurableInputAcceptance) error {
		order = append(order, "custody-accepted")
		if acceptance.SessionID != ready.ID() || acceptance.MessageID != "telegram-update:301" || acceptance.Sequence != 7 {
			t.Fatalf("acceptance = %#v", acceptance)
		}
		return nil
	}})
	if err != nil || !receipt.Accepted || receipt.Completion != telegramcontroller.DurableInputSucceeded {
		t.Fatalf("ProcessDurableInput() = (%#v, %v)", receipt, err)
	}
	want := []string{"custody-accepted", "provider-after-accepted"}
	if !reflect.DeepEqual(order, want) {
		t.Fatalf("effect order = %#v, want %#v", order, want)
	}
}

func TestProcessDurableInputConfirmsExactProviderInteractionAcceptance(t *testing.T) {
	workdir := t.TempDir()
	ready := readySession(t, "11111111-1111-4111-9111-111111111112", domain.ProviderCodex, workdir, "provider-1", 1)
	accepted := make(chan telegramcontroller.InteractionResponseAcceptance, 1)
	interactions := acceptingInteractionHandler{
		resolve: func(_ context.Context, envelope telegramcontroller.InteractionEnvelope) (sessionruntime.InteractionResponse, error) {
			return sessionruntime.InteractionResponse{ID: envelope.Request.ID, Outcome: "answered", Answers: map[string][]string{"choice": {"yes"}}}, nil
		},
		confirm: func(_ context.Context, acceptance telegramcontroller.InteractionResponseAcceptance) error {
			accepted <- acceptance
			return nil
		},
	}
	interactive := &interactiveSubmitter{submitWithCallbacks: func(ctx context.Context, id domain.SessionID, text string, callbacks sessionruntime.TurnCallbacks) (sessionruntime.TurnResult, error) {
		if err := callbacks.OnAccepted(callbacks.MessageID); err != nil {
			return sessionruntime.TurnResult{}, err
		}
		request := sessionruntime.InteractionRequest{ID: "interaction-1", Kind: "question", ItemID: "item-1", Blocking: true}
		if _, err := callbacks.OnInteraction(ctx, request); err != nil {
			return sessionruntime.TurnResult{}, err
		}
		if callbacks.OnInteractionResponseAccepted == nil {
			t.Fatal("interaction acceptance callback is nil")
		}
		if err := callbacks.OnInteractionResponseAccepted(sessionruntime.InteractionResponseAcceptance{
			ProviderSessionID: "provider-1", MessageID: callbacks.MessageID, InteractionID: request.ID,
		}); err != nil {
			return sessionruntime.TurnResult{}, err
		}
		return sessionruntime.TurnResult{Final: "done", TerminalStatus: sessionruntime.StatusCompleted}, nil
	}}
	controller := newController(t, nil, &memorySessions{byID: map[domain.SessionID]domain.Session{ready.ID(): ready}}, interactive, nil,
		telegramcontroller.Options{Interactions: interactions, Recovered: []domain.Session{ready}})
	t.Cleanup(func() { _ = controller.Close(context.Background()) })
	input := telegramcontroller.DurableLeasedInput{SessionID: ready.ID(), MessageID: "telegram-update:112", Sequence: 1, Payload: []byte("answer")}
	receipt, err := controller.ProcessDurableInput(context.Background(), input, telegramcontroller.DurableInputCallbacks{
		OnAccepted: func(context.Context, telegramcontroller.DurableInputAcceptance) error { return nil },
	})
	if err != nil || !receipt.Accepted || receipt.Completion != telegramcontroller.DurableInputSucceeded {
		t.Fatalf("ProcessDurableInput() = (%#v, %v)", receipt, err)
	}
	select {
	case got := <-accepted:
		want := telegramcontroller.InteractionResponseAcceptance{
			SessionID: ready.ID(), MessageID: input.MessageID, ProviderRequestID: "interaction-1",
		}
		if got != want {
			t.Fatalf("interaction acceptance = %#v, want %#v", got, want)
		}
	case <-time.After(time.Second):
		t.Fatal("interaction acceptance was not confirmed")
	}
}

func TestDurableTurnObserversUseStableTypedIdentityAndCannotCorruptCompletion(t *testing.T) {
	ready := readySession(t, "11111111-1111-4111-9111-111111111113", domain.ProviderCodex, t.TempDir(), "provider-1", 1)
	var observed telegramcontroller.RuntimeEventObservation
	var final telegramcontroller.FinalObservation
	var outputs []telegramcontroller.OutgoingNotification
	interactive := &interactiveSubmitter{submitWithCallbacks: func(_ context.Context, _ domain.SessionID, _ string, callbacks sessionruntime.TurnCallbacks) (sessionruntime.TurnResult, error) {
		if err := callbacks.OnAccepted(callbacks.MessageID); err != nil {
			return sessionruntime.TurnResult{}, err
		}
		if err := callbacks.OnEvent(sessionruntime.TurnEvent{Kind: sessionruntime.EventCommentary, Text: "working"}); err != nil {
			return sessionruntime.TurnResult{}, err
		}
		return sessionruntime.TurnResult{Final: "done", TerminalStatus: sessionruntime.StatusCompleted}, nil
	}}
	controller := newController(t, nil, &memorySessions{byID: map[domain.SessionID]domain.Session{ready.ID(): ready}}, interactive, nil,
		telegramcontroller.Options{
			Recovered: []domain.Session{ready},
			RuntimeEvents: runtimeEventObserverFunc(func(_ context.Context, event telegramcontroller.RuntimeEventObservation) error {
				observed = event
				return errors.New("screen unavailable")
			}),
			Finals: finalProcessorFunc(func(_ context.Context, value telegramcontroller.FinalObservation) error {
				final = value
				return errors.New("artifact unavailable")
			}),
			DurableOutput: durableOutputFunc(func(_ context.Context, output telegramcontroller.OutgoingNotification) (telegramcontroller.OutputReceipt, error) {
				outputs = append(outputs, output)
				return telegramcontroller.OutputReceipt{Inserted: true, SessionID: output.SessionID, OperationID: output.OperationID, Sequence: uint64(len(outputs))}, nil
			}),
		})
	t.Cleanup(func() { _ = controller.Close(context.Background()) })
	input := telegramcontroller.DurableLeasedInput{SessionID: ready.ID(), MessageID: "telegram-update:113", Sequence: 1, Payload: []byte("go")}
	receipt, err := controller.ProcessDurableInput(context.Background(), input, telegramcontroller.DurableInputCallbacks{
		OnAccepted: func(context.Context, telegramcontroller.DurableInputAcceptance) error { return nil },
	})
	if err != nil || receipt.Completion != telegramcontroller.DurableInputSucceeded {
		t.Fatalf("ProcessDurableInput() = (%#v, %v)", receipt, err)
	}
	if observed.OperationID != "telegram-update:113:event:1" || observed.SessionID != ready.ID() || observed.MessageID != input.MessageID ||
		observed.Event.Kind != sessionruntime.EventCommentary || observed.Event.Text != "working" {
		t.Fatalf("runtime observation = %#v", observed)
	}
	if final.OperationID != "telegram-update:113:final" || final.SessionID != ready.ID() || final.MessageID != input.MessageID || final.Text != "done" {
		t.Fatalf("final observation = %#v", final)
	}
	var operationIDs []string
	for _, output := range outputs {
		operationIDs = append(operationIDs, output.OperationID)
	}
	for _, want := range []string{
		"telegram-update:113:event:1:observer-error",
		"telegram-update:113:final-processor-error",
		"telegram-update:113:final",
	} {
		found := false
		for _, got := range operationIDs {
			found = found || got == want
		}
		if !found {
			t.Fatalf("durable outputs = %v, missing %q", operationIDs, want)
		}
	}
}

func TestQueueLimitRejectsOnlyExcessPendingTurn(t *testing.T) {
	workdir := t.TempDir()
	ready := readySession(t, "11111111-1111-4111-9111-111111111111", domain.ProviderCodex, workdir, "provider-1", 1)
	creator := creatorFunc(func(_ context.Context, intent app.ConfirmedSessionIntent) (app.CreateSessionResult, error) {
		return app.CreateSessionResult{Session: sessionWithIntent(t, ready, intent.IntentID)}, nil
	})
	started := make(chan struct{}, 1)
	release := make(chan struct{})
	submitter := submitterFunc(func(ctx context.Context, _ domain.SessionID, _ string) (sessionruntime.TurnResult, error) {
		select {
		case started <- struct{}{}:
		default:
		}
		select {
		case <-release:
			return sessionruntime.TurnResult{Final: "ok", TerminalStatus: sessionruntime.StatusCompleted}, nil
		case <-ctx.Done():
			return sessionruntime.TurnResult{}, ctx.Err()
		}
	})
	controller := newController(t, creator, &memorySessions{byID: map[domain.SessionID]domain.Session{ready.ID(): ready}}, submitter, notifierFunc(func(context.Context, telegramcontroller.Notification) error { return nil }), telegramcontroller.Options{QueueLimit: 1})
	t.Cleanup(func() { _ = controller.Close(context.Background()) })
	mustStatus(t, controller, message(40, "/new codex "+workdir))
	mustStatus(t, controller, message(41, "active"))
	select {
	case <-started:
	case <-time.After(3 * time.Second):
		t.Fatal("active turn did not start")
	}
	mustStatus(t, controller, message(42, "one pending"))
	decision := mustStatus(t, controller, message(43, "excess"))
	if !strings.Contains(decision.Status.Text, "переполнена") {
		t.Fatalf("queue-limit status = %q", decision.Status.Text)
	}
	close(release)
}

func TestDurableInputIsAcceptedBeforeAcknowledgementAndNotDuplicatedInMemory(t *testing.T) {
	ready := readySession(t, "11111111-1111-4111-9111-111111111111", domain.ProviderCodex, t.TempDir(), "provider-1", 1)
	accepted := make(chan telegramcontroller.SessionInput, 1)
	submits := make(chan string, 1)
	custody := durableInputFunc(func(_ context.Context, input telegramcontroller.SessionInput) (telegramcontroller.InputReceipt, error) {
		accepted <- input
		return telegramcontroller.InputReceipt{
			Inserted: true, SessionID: input.SessionID, MessageID: input.MessageID, Sequence: 1,
		}, nil
	})
	controller := newController(t,
		creatorFunc(nil),
		&memorySessions{byID: map[domain.SessionID]domain.Session{ready.ID(): ready}},
		submitterFunc(func(_ context.Context, _ domain.SessionID, text string) (sessionruntime.TurnResult, error) {
			submits <- text
			return sessionruntime.TurnResult{TerminalStatus: sessionruntime.StatusCompleted}, nil
		}),
		notifierFunc(nil),
		telegramcontroller.Options{Recovered: []domain.Session{ready}, DurableInput: custody},
	)
	t.Cleanup(func() { _ = controller.Close(context.Background()) })
	mustStatus(t, controller, message(70, "/use "+string(ready.ID())))
	decision := mustStatus(t, controller, message(71, "durable prompt"))
	if !strings.Contains(decision.Status.Text, "принят") {
		t.Fatalf("ack = %q", decision.Status.Text)
	}
	select {
	case input := <-accepted:
		if input.SessionID != ready.ID() || input.MessageID != "telegram-update:71" || string(input.Payload) != "durable prompt" {
			t.Fatalf("accepted input = %#v", input)
		}
	default:
		t.Fatal("ack returned before durable custody accepted the input")
	}
	select {
	case duplicate := <-submits:
		t.Fatalf("durably accepted input was also submitted in-memory: %q", duplicate)
	case <-time.After(20 * time.Millisecond):
	}
}

func TestDurableInputFailureIsNotAcknowledgedOrSubmitted(t *testing.T) {
	ready := readySession(t, "11111111-1111-4111-9111-111111111111", domain.ProviderCodex, t.TempDir(), "provider-1", 1)
	submits := make(chan string, 1)
	controller := newController(t,
		creatorFunc(nil),
		&memorySessions{byID: map[domain.SessionID]domain.Session{ready.ID(): ready}},
		submitterFunc(func(_ context.Context, _ domain.SessionID, text string) (sessionruntime.TurnResult, error) {
			submits <- text
			return sessionruntime.TurnResult{}, nil
		}),
		notifierFunc(nil),
		telegramcontroller.Options{
			Recovered: []domain.Session{ready},
			DurableInput: durableInputFunc(func(context.Context, telegramcontroller.SessionInput) (telegramcontroller.InputReceipt, error) {
				return telegramcontroller.InputReceipt{}, errors.New("disk unavailable with token-secret")
			}),
		},
	)
	t.Cleanup(func() { _ = controller.Close(context.Background()) })
	mustStatus(t, controller, message(72, "/use "+string(ready.ID())))
	decision := mustStatus(t, controller, message(73, "must remain unaccepted"))
	if strings.Contains(decision.Status.Text, "Запрос принят для") || strings.Contains(decision.Status.Text, "token-secret") || !strings.Contains(decision.Status.Text, "сохранить") {
		t.Fatalf("failed custody status = %q", decision.Status.Text)
	}
	select {
	case duplicate := <-submits:
		t.Fatalf("failed durable input was submitted: %q", duplicate)
	default:
	}
}

func TestMessageDuringAsyncStartingEntersDurableCustodyBeforeProviderReady(t *testing.T) {
	workdir := t.TempDir()
	starting, err := domain.NewStartingSession(
		"11111111-1111-4111-9111-111111111111",
		"telegram-update:80",
		"local",
		domain.ProviderCodex,
		workdir,
	)
	if err != nil {
		t.Fatal(err)
	}
	ready, err := starting.Ready(domain.ProviderBinding{Provider: domain.ProviderCodex, SessionID: "provider-original", Generation: 1})
	if err != nil {
		t.Fatal(err)
	}
	store := newLockedSessions(starting)
	outcomes := make(chan telegramcontroller.SessionStartOutcome, 1)
	begin := asyncCreatorFunc(func(_ context.Context, intent app.ConfirmedSessionIntent) (telegramcontroller.PendingSessionStart, error) {
		if intent.IntentID != starting.IntentID() {
			t.Fatalf("intent id = %q", intent.IntentID)
		}
		return telegramcontroller.PendingSessionStart{Session: starting, Outcome: outcomes}, nil
	})
	accepted := make(chan telegramcontroller.SessionInput, 1)
	controller := newController(t,
		creatorFunc(nil), store, submitterFunc(nil), notifierFunc(nil),
		telegramcontroller.Options{
			AsyncCreator: begin,
			DurableInput: durableInputFunc(func(_ context.Context, input telegramcontroller.SessionInput) (telegramcontroller.InputReceipt, error) {
				accepted <- input
				return telegramcontroller.InputReceipt{Inserted: true, SessionID: input.SessionID, MessageID: input.MessageID, Sequence: 1}, nil
			}),
		},
	)
	t.Cleanup(func() { _ = controller.Close(context.Background()) })
	decision := mustStatus(t, controller, message(80, "/new codex "+workdir))
	if !strings.Contains(decision.Status.Text, "starting") {
		t.Fatalf("starting decision = %q", decision.Status.Text)
	}
	decision = mustStatus(t, controller, message(81, "queued while starting"))
	if !strings.Contains(decision.Status.Text, "принят") {
		t.Fatalf("starting input decision = %q", decision.Status.Text)
	}
	select {
	case input := <-accepted:
		if input.SessionID != starting.ID() || input.MessageID != "telegram-update:81" {
			t.Fatalf("starting custody input = %#v", input)
		}
	default:
		t.Fatal("starting input did not enter durable custody")
	}
	store.Set(ready)
	outcomes <- telegramcontroller.SessionStartOutcome{Session: ready}
	close(outcomes)
	eventuallyStatusContains(t, controller, message(82, "/status"), "готова")
}

func TestMessageDuringAsyncResumeEntersDurableCustodyAndExactReadyWins(t *testing.T) {
	archived := archivedSession(t, "11111111-1111-4111-9111-111111111111", domain.ProviderClaude, t.TempDir(), "provider-original", 3)
	resuming, err := archived.BeginResume(archived.StateChangedAt().Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	ready, err := resuming.ResumeReady(
		domain.ProviderBinding{Provider: domain.ProviderClaude, SessionID: "provider-original", Generation: 4},
		archived.StateChangedAt().Add(2*time.Minute),
		domain.SessionLifetimeNever,
	)
	if err != nil {
		t.Fatal(err)
	}
	store := newLockedSessions(archived)
	outcomes := make(chan telegramcontroller.SessionStartOutcome, 1)
	accepted := make(chan telegramcontroller.SessionInput, 1)
	controller := newController(t,
		creatorFunc(nil), store, submitterFunc(nil), notifierFunc(nil),
		telegramcontroller.Options{
			AsyncResumer: asyncResumerFunc(func(_ context.Context, id domain.SessionID) (telegramcontroller.PendingSessionStart, error) {
				if id != archived.ID() {
					t.Fatalf("resume id = %q", id)
				}
				store.Set(resuming)
				return telegramcontroller.PendingSessionStart{Session: resuming, Outcome: outcomes}, nil
			}),
			DurableInput: durableInputFunc(func(_ context.Context, input telegramcontroller.SessionInput) (telegramcontroller.InputReceipt, error) {
				accepted <- input
				return telegramcontroller.InputReceipt{Inserted: true, SessionID: input.SessionID, MessageID: input.MessageID, Sequence: 1}, nil
			}),
		},
	)
	t.Cleanup(func() { _ = controller.Close(context.Background()) })
	decision, err := controller.ResumeArchived(context.Background(), archived.ID())
	if err != nil || !strings.Contains(decision.Status.Text, "resuming") {
		t.Fatalf("ResumeArchived() = (%#v, %v)", decision, err)
	}
	mustStatus(t, controller, message(83, "queued while resuming"))
	select {
	case input := <-accepted:
		if input.SessionID != archived.ID() || input.MessageID != "telegram-update:83" {
			t.Fatalf("resuming custody input = %#v", input)
		}
	default:
		t.Fatal("resuming input did not enter durable custody")
	}
	store.Set(ready)
	outcomes <- telegramcontroller.SessionStartOutcome{Session: ready}
	close(outcomes)
	eventuallyStatusContains(t, controller, message(84, "/status"), "готова")
}

func TestSemanticCardActionsAlwaysUseExplicitSessionTarget(t *testing.T) {
	first := readySession(t, "11111111-1111-4111-9111-111111111111", domain.ProviderCodex, t.TempDir(), "provider-1", 1)
	second := readySession(t, "22222222-2222-4222-9222-222222222222", domain.ProviderClaude, t.TempDir(), "provider-2", 1)
	store := newLockedSessions(first, second)
	activeStore := &recordingActiveStore{}
	preferences := &testPreferences{}
	controller := newController(t, creatorFunc(nil), store, submitterFunc(nil), notifierFunc(nil), telegramcontroller.Options{
		Recovered: []domain.Session{first, second}, UIState: activeStore, Settings: preferences,
	})
	t.Cleanup(func() { _ = controller.Close(context.Background()) })
	mustStatus(t, controller, message(90, "/use "+string(first.ID())))

	for _, action := range []telegramcontroller.SemanticAction{
		{Kind: telegramcontroller.SemanticPagePrevious, SessionID: second.ID(), Page: 1},
		{Kind: telegramcontroller.SemanticPageLatest, SessionID: second.ID(), FollowLatest: true},
		{Kind: telegramcontroller.SemanticPageNext, SessionID: second.ID(), Page: 1},
		{Kind: telegramcontroller.SemanticOptions, SessionID: second.ID()},
		{Kind: telegramcontroller.SemanticScreen, SessionID: second.ID()},
	} {
		result, err := controller.HandleSemanticAction(context.Background(), action)
		if err != nil || result.Decision.Kind != coordinator.DecisionStatus || !strings.Contains(result.Decision.Status.Text, string(second.ID())) || result.Card == nil || result.Card.SessionID != second.ID() {
			t.Fatalf("HandleSemanticAction(%q) = (%#v, %v), want typed second session card", action.Kind, result, err)
		}
	}
	if !preferences.settings.ScreenEnabled {
		t.Fatal("semantic screen action did not toggle global screen setting")
	}
	result, err := controller.HandleSemanticAction(context.Background(), telegramcontroller.SemanticAction{Kind: telegramcontroller.SemanticSelect, SessionID: second.ID()})
	if err != nil || !strings.Contains(result.Decision.Status.Text, string(second.ID())) || result.Card == nil || !result.Card.MakeActive || activeStore.saved[len(activeStore.saved)-1] != second.ID() {
		t.Fatalf("semantic select = (%#v, %v), saved=%#v", result, err, activeStore.saved)
	}
}

func TestSemanticStopTargetsBusySessionEvenWhenAnotherSessionIsActive(t *testing.T) {
	first := readySession(t, "11111111-1111-4111-9111-111111111111", domain.ProviderCodex, t.TempDir(), "provider-1", 1)
	second := readySession(t, "22222222-2222-4222-9222-222222222222", domain.ProviderClaude, t.TempDir(), "provider-2", 1)
	started := make(chan struct{})
	release := make(chan struct{})
	stopped := make(chan domain.SessionID, 1)
	controller := newController(t,
		creatorFunc(nil), newLockedSessions(first, second),
		submitterFunc(func(_ context.Context, id domain.SessionID, _ string) (sessionruntime.TurnResult, error) {
			if id == second.ID() {
				close(started)
				<-release
			}
			return sessionruntime.TurnResult{TerminalStatus: sessionruntime.StatusInterrupted}, nil
		}), notifierFunc(nil),
		telegramcontroller.Options{Recovered: []domain.Session{first, second}, Stopper: stopperFunc(func(_ context.Context, id domain.SessionID) error {
			stopped <- id
			return nil
		})},
	)
	t.Cleanup(func() { _ = controller.Close(context.Background()) })
	mustStatus(t, controller, message(91, "/use "+string(second.ID())))
	mustStatus(t, controller, message(92, "busy"))
	<-started
	mustStatus(t, controller, message(93, "/use "+string(first.ID())))
	result, err := controller.HandleSemanticAction(context.Background(), telegramcontroller.SemanticAction{Kind: telegramcontroller.SemanticStop, SessionID: second.ID()})
	if err != nil || !strings.Contains(result.Decision.Status.Text, "остановлен") || result.Card == nil || result.Card.SessionID != second.ID() {
		t.Fatalf("semantic stop = (%#v, %v)", result, err)
	}
	if got := <-stopped; got != second.ID() {
		t.Fatalf("stopped session = %q, want explicit %q", got, second.ID())
	}
	close(release)
}

func TestSemanticCloseAndResumeTargetExplicitSessions(t *testing.T) {
	ready := readySession(t, "11111111-1111-4111-9111-111111111111", domain.ProviderCodex, t.TempDir(), "provider-1", 1)
	archivedReady := readySession(t, "22222222-2222-4222-9222-222222222222", domain.ProviderClaude, t.TempDir(), "provider-original", 3)
	archived := archivedSession(t, string(archivedReady.ID()), archivedReady.Provider(), archivedReady.Workdir(), "provider-original", 3)
	resuming, _ := archived.BeginResume(archived.StateChangedAt().Add(time.Minute))
	resumed, _ := resuming.ResumeReady(domain.ProviderBinding{Provider: archived.Provider(), SessionID: "provider-original", Generation: 4}, archived.StateChangedAt().Add(2*time.Minute), domain.SessionLifetimeNever)
	store := newLockedSessions(ready, archived)
	closed := make(chan domain.SessionID, 1)
	resumedIDs := make(chan domain.SessionID, 1)
	controller := newController(t, creatorFunc(nil), store, submitterFunc(nil), notifierFunc(nil), telegramcontroller.Options{
		Recovered: []domain.Session{ready},
		SessionCloser: sessionCloserFunc(func(_ context.Context, id domain.SessionID) (app.CloseSessionResult, error) {
			closed <- id
			result := archivedSession(t, string(ready.ID()), ready.Provider(), ready.Workdir(), "provider-1", 1)
			store.Set(result)
			return app.CloseSessionResult{Session: result}, nil
		}),
		ArchivedResumer: archivedResumerFunc(func(_ context.Context, id domain.SessionID) (domain.Session, error) {
			resumedIDs <- id
			store.Set(resumed)
			return resumed, nil
		}),
	})
	t.Cleanup(func() { _ = controller.Close(context.Background()) })
	if _, err := controller.HandleSemanticAction(context.Background(), telegramcontroller.SemanticAction{Kind: telegramcontroller.SemanticClose, SessionID: ready.ID()}); err != nil {
		t.Fatal(err)
	}
	if got := <-closed; got != ready.ID() {
		t.Fatalf("closed session = %q", got)
	}
	if _, err := controller.HandleSemanticAction(context.Background(), telegramcontroller.SemanticAction{Kind: telegramcontroller.SemanticResume, SessionID: archived.ID()}); err != nil {
		t.Fatal(err)
	}
	if got := <-resumedIDs; got != archived.ID() {
		t.Fatalf("resumed session = %q", got)
	}
}

func TestSemanticActionRejectsMissingOrUnexpectedTargetFields(t *testing.T) {
	controller := newController(t, creatorFunc(nil), &memorySessions{}, submitterFunc(nil), notifierFunc(nil), telegramcontroller.Options{})
	t.Cleanup(func() { _ = controller.Close(context.Background()) })
	for _, action := range []telegramcontroller.SemanticAction{
		{Kind: telegramcontroller.SemanticStop},
		{Kind: telegramcontroller.SemanticClose, SessionID: "session", Page: 1},
		{Kind: "unknown", SessionID: "session"},
	} {
		if _, err := controller.HandleSemanticAction(context.Background(), action); err == nil {
			t.Fatalf("HandleSemanticAction(%#v) accepted invalid action", action)
		}
	}
}

func TestGlobalSemanticActionsExposeOnlyTypedSurfacesAndStableCreateIdentity(t *testing.T) {
	ready := readySession(t, "11111111-1111-4111-9111-111111111111", domain.ProviderCodex, "/tmp", "provider-1", 1)
	var intent app.ConfirmedSessionIntent
	controller := newController(t,
		creatorFunc(func(_ context.Context, got app.ConfirmedSessionIntent) (app.CreateSessionResult, error) {
			intent = got
			return app.CreateSessionResult{Session: sessionWithIntent(t, ready, got.IntentID)}, nil
		}),
		newLockedSessions(ready), submitterFunc(nil), notifierFunc(nil),
		telegramcontroller.Options{Settings: &testPreferences{}, CreateDrafts: createDraftSelectorFunc{workdir: "/tmp", computerID: "local"}},
	)
	t.Cleanup(func() { _ = controller.Close(context.Background()) })
	for _, kind := range []telegramcontroller.SemanticActionKind{
		telegramcontroller.SemanticMenuSessions,
		telegramcontroller.SemanticMenuNew,
		telegramcontroller.SemanticMenuArchive,
		telegramcontroller.SemanticMenuStatus,
		telegramcontroller.SemanticMenuSettings,
		telegramcontroller.SemanticMenuBack,
		telegramcontroller.SemanticSettingsScreen,
		telegramcontroller.SemanticSettingsDetail,
	} {
		result, err := controller.HandleSemanticAction(context.Background(), telegramcontroller.SemanticAction{Kind: kind})
		if err != nil || result.Surface == nil || result.Surface.Text == "" {
			t.Fatalf("global action %q = (%#v, %v), want typed surface", kind, result, err)
		}
	}
	result, err := controller.HandleSemanticAction(context.Background(), telegramcontroller.SemanticAction{Kind: telegramcontroller.SemanticCreateCodex, UpdateID: 777})
	if err != nil || result.Card == nil || intent.IntentID != "telegram-update:777" {
		t.Fatalf("semantic create = (%#v, %v), intent=%#v", result, err, intent)
	}
	if _, err := controller.HandleSemanticAction(context.Background(), telegramcontroller.SemanticAction{Kind: telegramcontroller.SemanticCreateClaude}); err == nil {
		t.Fatal("semantic create accepted missing claimed update id")
	}
}

func TestSemanticCreateUsesOnlyExplicitConfirmedAbsoluteDraft(t *testing.T) {
	workdir := t.TempDir()
	ready := readySession(t, "44444444-4444-4444-9444-444444444444", domain.ProviderCodex, workdir, "provider-4", 1)
	var intent app.ConfirmedSessionIntent
	controller := newController(t, creatorFunc(func(_ context.Context, got app.ConfirmedSessionIntent) (app.CreateSessionResult, error) {
		intent = got
		return app.CreateSessionResult{Session: sessionWithIntent(t, ready, got.IntentID)}, nil
	}), newLockedSessions(ready), submitterFunc(nil), notifierFunc(nil), telegramcontroller.Options{
		CreateDrafts: createDraftSelectorFunc{computerID: "local", workdir: workdir},
	})
	t.Cleanup(func() { _ = controller.Close(context.Background()) })
	preview, err := controller.HandleSemanticAction(context.Background(), telegramcontroller.SemanticAction{Kind: telegramcontroller.SemanticMenuNew})
	if err != nil || preview.Surface == nil || !strings.Contains(preview.Surface.Text, workdir) || strings.Contains(preview.Surface.Text, "по умолчанию: /tmp") {
		t.Fatalf("draft preview = (%#v, %v)", preview, err)
	}
	if _, err := controller.HandleSemanticAction(context.Background(), telegramcontroller.SemanticAction{Kind: telegramcontroller.SemanticCreateCodex, UpdateID: 401}); err != nil {
		t.Fatal(err)
	}
	if intent.Workdir != workdir || intent.ComputerID != "local" || intent.Provider != domain.ProviderCodex {
		t.Fatalf("confirmed intent = %#v", intent)
	}

	invalid := newController(t, creatorFunc(nil), &memorySessions{}, submitterFunc(nil), notifierFunc(nil), telegramcontroller.Options{
		CreateDrafts: createDraftSelectorFunc{computerID: "local", workdir: "relative/path"},
	})
	t.Cleanup(func() { _ = invalid.Close(context.Background()) })
	if _, err := invalid.HandleSemanticAction(context.Background(), telegramcontroller.SemanticAction{Kind: telegramcontroller.SemanticCreateCodex, UpdateID: 402}); err == nil {
		t.Fatal("semantic create accepted relative workdir")
	}
}

func TestProjectCurrentReadsExactOrActiveSurfaceWithoutChangingDurableState(t *testing.T) {
	first := readySession(t, "11111111-1111-4111-9111-111111111111", domain.ProviderCodex, t.TempDir(), "provider-1", 1)
	second := readySession(t, "22222222-2222-4222-9222-222222222222", domain.ProviderClaude, t.TempDir(), "provider-2", 1)
	ui := &projectionUIState{history: map[domain.SessionID][]string{first.ID(): {"first"}, second.ID(): {"second"}}}
	controller := newController(t, creatorFunc(nil), newLockedSessions(first, second), submitterFunc(nil), notifierFunc(nil), telegramcontroller.Options{
		Recovered: []domain.Session{first, second}, UIState: ui,
	})
	t.Cleanup(func() { _ = controller.Close(context.Background()) })
	mustStatus(t, controller, message(300, "/use "+string(first.ID())))
	before := ui.snapshot()

	exact, err := controller.ProjectCurrent(context.Background(), second.ID())
	if err != nil || exact.Card == nil || exact.Card.SessionID != second.ID() || exact.Card.MakeActive {
		t.Fatalf("exact ProjectCurrent() = (%#v, %v), want second read-only card", exact, err)
	}
	global, err := controller.ProjectCurrent(context.Background(), "")
	if err != nil || global.Card == nil || global.Card.SessionID != first.ID() || global.Card.MakeActive {
		t.Fatalf("global ProjectCurrent() = (%#v, %v), want active first read-only card", global, err)
	}
	again, err := controller.ProjectCurrent(context.Background(), second.ID())
	if err != nil || !reflect.DeepEqual(exact, again) {
		t.Fatalf("repeated exact ProjectCurrent() = (%#v, %v), want %#v", again, err, exact)
	}
	if after := ui.snapshot(); !reflect.DeepEqual(before, after) {
		t.Fatalf("ProjectCurrent changed durable UI bytes/counters: before=%#v after=%#v", before, after)
	}
}

func TestNewSessionAvailabilityFollowsLiveProviderSnapshot(t *testing.T) {
	providers := &testProviderPreferences{values: map[domain.Provider]telegramcontroller.ProviderPreference{
		domain.ProviderCodex:  {Provider: domain.ProviderCodex, Configured: true, Enabled: true},
		domain.ProviderClaude: {Provider: domain.ProviderClaude, Configured: true, Enabled: false},
	}}
	drafts := &recordingDraftSelector{computerID: "local", workdir: t.TempDir()}
	controller := newController(t, creatorFunc(nil), &memorySessions{}, submitterFunc(nil), notifierFunc(nil), telegramcontroller.Options{Providers: providers, CreateDrafts: drafts})
	t.Cleanup(func() { _ = controller.Close(context.Background()) })

	first, err := controller.HandleSemanticAction(context.Background(), telegramcontroller.SemanticAction{Kind: telegramcontroller.SemanticMenuNew})
	if err != nil || first.Surface == nil || !hasSemanticAction(first.Surface.Rows, telegramcontroller.SemanticCreateCodex) || hasSemanticAction(first.Surface.Rows, telegramcontroller.SemanticCreateClaude) || !reflect.DeepEqual(drafts.previewed, []domain.Provider{domain.ProviderCodex}) {
		t.Fatalf("one-enabled new surface = (%#v, %v), previews=%v", first, err, drafts.previewed)
	}
	legacy, err := controller.Handle(context.Background(), coordinator.Update{ID: 301, Kind: coordinator.UpdateCallback, ActorID: ownerID, ConversationID: chatID, ConversationKind: "private", CallbackQueryID: "new-menu", SourceMessageID: 1, Text: "menu:new"})
	if err != nil || legacy.Keyboard == nil || len(*legacy.Keyboard) != 2 || len((*legacy.Keyboard)[0]) != 1 || (*legacy.Keyboard)[0][0].CallbackData != "new:codex" {
		t.Fatalf("one-enabled legacy new menu = (%#v, %v)", legacy, err)
	}

	providers.values[domain.ProviderCodex] = telegramcontroller.ProviderPreference{Provider: domain.ProviderCodex, Configured: true, Enabled: false}
	none, err := controller.HandleSemanticAction(context.Background(), telegramcontroller.SemanticAction{Kind: telegramcontroller.SemanticMenuNew})
	if err != nil || none.Surface == nil || hasSemanticAction(none.Surface.Rows, telegramcontroller.SemanticCreateCodex) || hasSemanticAction(none.Surface.Rows, telegramcontroller.SemanticCreateClaude) || !strings.Contains(none.Surface.Text, "недоступно") {
		t.Fatalf("zero-enabled new surface = (%#v, %v)", none, err)
	}

	providers.values[domain.ProviderClaude] = telegramcontroller.ProviderPreference{Provider: domain.ProviderClaude, Configured: true, Enabled: true}
	reenabled, err := controller.HandleSemanticAction(context.Background(), telegramcontroller.SemanticAction{Kind: telegramcontroller.SemanticMenuNew})
	if err != nil || reenabled.Surface == nil || hasSemanticAction(reenabled.Surface.Rows, telegramcontroller.SemanticCreateCodex) || !hasSemanticAction(reenabled.Surface.Rows, telegramcontroller.SemanticCreateClaude) || !reflect.DeepEqual(drafts.previewed, []domain.Provider{domain.ProviderCodex, domain.ProviderClaude}) {
		t.Fatalf("re-enabled new surface = (%#v, %v), previews=%v", reenabled, err, drafts.previewed)
	}
}

func TestDisabledProviderBlocksStaleNewConfirmAndTextCreate(t *testing.T) {
	providers := &testProviderPreferences{values: map[domain.Provider]telegramcontroller.ProviderPreference{
		domain.ProviderCodex:  {Provider: domain.ProviderCodex, Configured: true, Enabled: true},
		domain.ProviderClaude: {Provider: domain.ProviderClaude, Configured: true, Enabled: false},
	}}
	drafts := &recordingDraftSelector{computerID: "local", workdir: t.TempDir()}
	var creates int
	ready := readySession(t, "33333333-3333-4333-9333-333333333333", domain.ProviderCodex, drafts.workdir, "provider-3", 1)
	controller := newController(t, creatorFunc(func(_ context.Context, intent app.ConfirmedSessionIntent) (app.CreateSessionResult, error) {
		creates++
		return app.CreateSessionResult{Session: sessionWithIntent(t, ready, intent.IntentID)}, nil
	}), newLockedSessions(ready), submitterFunc(nil), notifierFunc(nil), telegramcontroller.Options{Providers: providers, CreateDrafts: drafts})
	t.Cleanup(func() { _ = controller.Close(context.Background()) })

	if _, err := controller.HandleSemanticAction(context.Background(), telegramcontroller.SemanticAction{Kind: telegramcontroller.SemanticMenuNew}); err != nil {
		t.Fatal(err)
	}
	providers.values[domain.ProviderCodex] = telegramcontroller.ProviderPreference{Provider: domain.ProviderCodex, Configured: true, Enabled: false}
	stale, err := controller.HandleSemanticAction(context.Background(), telegramcontroller.SemanticAction{Kind: telegramcontroller.SemanticCreateCodex, UpdateID: 310})
	if err != nil || stale.Surface == nil || !strings.Contains(stale.Surface.Text, "недоступно") || drafts.confirms != 0 || creates != 0 {
		t.Fatalf("stale semantic create = (%#v, %v), confirms=%d creates=%d", stale, err, drafts.confirms, creates)
	}
	if decision := mustStatus(t, controller, message(311, "/new codex "+drafts.workdir)); !strings.Contains(decision.Status.Text, "недоступно") || creates != 0 {
		t.Fatalf("disabled /new = %#v, creates=%d", decision, creates)
	}
	callback, err := controller.Handle(context.Background(), coordinator.Update{ID: 312, Kind: coordinator.UpdateCallback, ActorID: ownerID, ConversationID: chatID, ConversationKind: "private", CallbackQueryID: "stale-new", SourceMessageID: 1, Text: "new:codex"})
	if err != nil || !strings.Contains(callback.Status.Text, "недоступно") || drafts.confirms != 0 || creates != 0 {
		t.Fatalf("disabled callback create = (%#v, %v), confirms=%d creates=%d", callback, err, drafts.confirms, creates)
	}

	providers.values[domain.ProviderCodex] = telegramcontroller.ProviderPreference{Provider: domain.ProviderCodex, Configured: true, Enabled: true}
	drafts.afterConfirm = func() {
		providers.values[domain.ProviderCodex] = telegramcontroller.ProviderPreference{Provider: domain.ProviderCodex, Configured: true, Enabled: false}
	}
	confirmed, err := controller.HandleSemanticAction(context.Background(), telegramcontroller.SemanticAction{Kind: telegramcontroller.SemanticCreateCodex, UpdateID: 313})
	if err != nil || confirmed.Surface == nil || !strings.Contains(confirmed.Surface.Text, "недоступно") || creates != 0 || drafts.confirms != 1 {
		t.Fatalf("disabled-after-confirm create = (%#v, %v), confirms=%d creates=%d", confirmed, err, drafts.confirms, creates)
	}
}

func hasSemanticAction(rows [][]telegramcontroller.SemanticButton, want telegramcontroller.SemanticActionKind) bool {
	for _, row := range rows {
		for _, button := range row {
			if button.Action == want {
				return true
			}
		}
	}
	return false
}

func TestSemanticMessageReturnsTypedMenuWithoutRawCallbackData(t *testing.T) {
	controller := newController(t, creatorFunc(nil), &memorySessions{}, submitterFunc(nil), notifierFunc(nil), telegramcontroller.Options{})
	t.Cleanup(func() { _ = controller.Close(context.Background()) })
	result, err := controller.HandleSemanticMessage(context.Background(), message(120, "/menu"))
	if err != nil || result.Surface == nil || len(result.Surface.Rows) != 3 {
		t.Fatalf("HandleSemanticMessage() = (%#v, %v)", result, err)
	}
	for _, row := range result.Surface.Rows {
		for _, button := range row {
			if button.Label == "" || button.Action == "" {
				t.Fatalf("untyped semantic button = %#v", button)
			}
		}
	}
}

func TestAuthorizationSecretBypassesHistoryAndJournalAndUsesSourceMessageIdentity(t *testing.T) {
	var started telegramcontroller.AuthorizationStart
	var submitted telegramcontroller.AuthorizationSecret
	auth := authorizationFlowFunc{
		start: func(_ context.Context, request telegramcontroller.AuthorizationStart) (telegramcontroller.AuthorizationChallenge, error) {
			started = request
			return telegramcontroller.AuthorizationChallenge{
				OperationID: request.OperationID, ComputerID: request.ComputerID, Provider: request.Provider,
				ChallengeReference: "opaque-challenge", Instruction: "Введите одноразовый код следующим сообщением.",
			}, nil
		},
		submit: func(_ context.Context, request telegramcontroller.AuthorizationSecret) (telegramcontroller.AuthorizationResult, error) {
			submitted = request
			if string(request.Secret) != "very-secret-code" {
				t.Fatalf("secret at authorization boundary = %q", request.Secret)
			}
			return telegramcontroller.AuthorizationResult{Authenticated: true, DeletionKnown: true}, nil
		},
		pending: func(context.Context, telegramcontroller.AuthorizationPendingLookup) ([]telegramcontroller.PendingAuthorization, error) {
			return []telegramcontroller.PendingAuthorization{{AuthorizationChallenge: telegramcontroller.AuthorizationChallenge{
				OperationID: started.OperationID, ComputerID: started.ComputerID, Provider: started.Provider,
				ChallengeReference: "opaque-challenge",
			}, AcceptsSecret: true}}, nil
		},
	}
	durableInputs := 0
	controller := newController(t, creatorFunc(nil), &memorySessions{}, submitterFunc(nil), notifierFunc(nil), telegramcontroller.Options{
		Authorization: auth,
		DurableInput: durableInputFunc(func(context.Context, telegramcontroller.SessionInput) (telegramcontroller.InputReceipt, error) {
			durableInputs++
			return telegramcontroller.InputReceipt{}, errors.New("secret reached journal")
		}),
	})
	t.Cleanup(func() { _ = controller.Close(context.Background()) })
	result, err := controller.HandleSemanticAction(context.Background(), telegramcontroller.SemanticAction{Kind: telegramcontroller.SemanticAuthorizeCodex, UpdateID: 130})
	if err != nil || result.Surface == nil || !strings.Contains(result.Surface.Text, "одноразовый") {
		t.Fatalf("start authorization = (%#v, %v)", result, err)
	}
	if started.OperationID != "telegram-update:130:authorization" || started.ActorID != ownerID || started.PrivateChatID != chatID || started.Provider != domain.ProviderCodex {
		t.Fatalf("authorization start = %#v", started)
	}
	secretUpdate := message(131, "very-secret-code")
	secretUpdate.SourceMessageID = 9001
	decision := mustStatus(t, controller, secretUpdate)
	if !strings.Contains(decision.Status.Text, "авторизован") || strings.Contains(decision.Status.Text, "very-secret-code") {
		t.Fatalf("authorization result = %q", decision.Status.Text)
	}
	if submitted.OperationID != started.OperationID || submitted.SubmissionOperationID != "telegram-message:9001:authorization" || submitted.SourceMessageID != 9001 || submitted.ChallengeReference != "opaque-challenge" {
		t.Fatalf("authorization submit = %#v", submitted)
	}
	if durableInputs != 0 {
		t.Fatalf("secret journal calls = %d", durableInputs)
	}
	for _, value := range submitted.Secret {
		if value != 0 {
			t.Fatal("controller retained non-zero authorization secret bytes after Submit")
		}
	}
}

func TestClaudeAuthorizationAvailabilityComesOnlyFromInjectedCapability(t *testing.T) {
	starts := 0
	auth := authorizationFlowFunc{
		supports: func(domain.Provider) bool { return false },
		start: func(context.Context, telegramcontroller.AuthorizationStart) (telegramcontroller.AuthorizationChallenge, error) {
			starts++
			return telegramcontroller.AuthorizationChallenge{}, errors.New("must not start")
		},
	}
	controller := newController(t, creatorFunc(nil), &memorySessions{}, submitterFunc(nil), notifierFunc(nil), telegramcontroller.Options{Authorization: auth})
	t.Cleanup(func() { _ = controller.Close(context.Background()) })
	result, err := controller.HandleSemanticAction(context.Background(), telegramcontroller.SemanticAction{Kind: telegramcontroller.SemanticAuthorizeClaude, UpdateID: 140})
	if err != nil || result.Surface == nil || !strings.Contains(result.Surface.Text, "недоступна") || starts != 0 {
		t.Fatalf("unsupported Claude authorization = (%#v, %v), starts=%d", result, err, starts)
	}

	auth.supports = func(provider domain.Provider) bool { return provider == domain.ProviderClaude }
	auth.start = func(_ context.Context, request telegramcontroller.AuthorizationStart) (telegramcontroller.AuthorizationChallenge, error) {
		starts++
		return telegramcontroller.AuthorizationChallenge{
			OperationID: request.OperationID, ComputerID: request.ComputerID, Provider: request.Provider,
			ChallengeReference: "claude-challenge", Instruction: "Введите код Claude.",
		}, nil
	}
	capable := newController(t, creatorFunc(nil), &memorySessions{}, submitterFunc(nil), notifierFunc(nil), telegramcontroller.Options{Authorization: auth})
	t.Cleanup(func() { _ = capable.Close(context.Background()) })
	result, err = capable.HandleSemanticAction(context.Background(), telegramcontroller.SemanticAction{Kind: telegramcontroller.SemanticAuthorizeClaude, UpdateID: 141})
	if err != nil || result.Surface == nil || !strings.Contains(result.Surface.Text, "Claude") || starts != 1 {
		t.Fatalf("supported Claude authorization = (%#v, %v), starts=%d", result, err, starts)
	}
}

func TestAuthorizationPendingStateRestoresAfterControllerRestartBeforeOrdinaryRouting(t *testing.T) {
	submits := 0
	durableInputs := 0
	auth := authorizationFlowFunc{
		pending: func(_ context.Context, request telegramcontroller.AuthorizationPendingLookup) ([]telegramcontroller.PendingAuthorization, error) {
			if request.ActorID != ownerID || request.PrivateChatID != chatID || request.ConversationKind != "private" {
				t.Fatalf("pending lookup = %#v", request)
			}
			return []telegramcontroller.PendingAuthorization{{AuthorizationChallenge: telegramcontroller.AuthorizationChallenge{
				OperationID: "telegram-update:150:authorization", ComputerID: domain.ComputerID("local"),
				Provider: domain.ProviderClaude, ChallengeReference: "restart-challenge",
			}, AcceptsSecret: true}}, nil
		},
		supports: func(provider domain.Provider) bool { return provider == domain.ProviderClaude },
		submit: func(_ context.Context, secret telegramcontroller.AuthorizationSecret) (telegramcontroller.AuthorizationResult, error) {
			submits++
			if secret.OperationID != "telegram-update:150:authorization" || string(secret.Secret) != "restart-secret" {
				t.Fatalf("restored submit = %#v", secret)
			}
			return telegramcontroller.AuthorizationResult{Authenticated: true, DeletionKnown: true}, nil
		},
	}
	controller := newController(t, creatorFunc(nil), &memorySessions{}, submitterFunc(nil), notifierFunc(nil), telegramcontroller.Options{
		Authorization: auth,
		DurableInput: durableInputFunc(func(context.Context, telegramcontroller.SessionInput) (telegramcontroller.InputReceipt, error) {
			durableInputs++
			return telegramcontroller.InputReceipt{}, errors.New("secret reached ordinary journal")
		}),
	})
	t.Cleanup(func() { _ = controller.Close(context.Background()) })
	update := message(151, "restart-secret")
	update.SourceMessageID = 9151
	decision := mustStatus(t, controller, update)
	if submits != 1 || durableInputs != 0 || !strings.Contains(decision.Status.Text, "Claude авторизован") {
		t.Fatalf("restored auth result = submits %d journal %d status %q", submits, durableInputs, decision.Status.Text)
	}
}

func TestTerminalAuthorizationMessageRedeliveryIsConsumedBeforePendingOrOrdinaryInput(t *testing.T) {
	consumed := 0
	auth := authorizationFlowFunc{
		consume: func(_ context.Context, lookup telegramcontroller.AuthorizationMessageLookup) (telegramcontroller.AuthorizationMessageBinding, error) {
			consumed++
			if lookup.ActorID != ownerID || lookup.PrivateChatID != chatID || lookup.SourceMessageID != 9010 {
				t.Fatalf("message lookup = %#v", lookup)
			}
			return telegramcontroller.AuthorizationMessageBinding{
				Bound: true, Provider: domain.ProviderCodex, Authenticated: true, DeletionKnown: true,
			}, nil
		},
		pending: func(context.Context, telegramcontroller.AuthorizationPendingLookup) ([]telegramcontroller.PendingAuthorization, error) {
			t.Fatal("terminal redelivery reached pending authorization")
			return nil, nil
		},
	}
	controller := newController(t, creatorFunc(nil), &memorySessions{}, submitterFunc(nil), notifierFunc(nil), telegramcontroller.Options{
		Authorization: auth,
		DurableInput: durableInputFunc(func(context.Context, telegramcontroller.SessionInput) (telegramcontroller.InputReceipt, error) {
			t.Fatal("terminal authorization redelivery reached ordinary input")
			return telegramcontroller.InputReceipt{}, nil
		}),
	})
	t.Cleanup(func() { _ = controller.Close(context.Background()) })
	redelivery := message(151, "same-secret-must-not-be-read")
	redelivery.SourceMessageID = 9010
	decision := mustStatus(t, controller, redelivery)
	if decision.Status.Text != "Codex авторизован. Сообщение с секретом удалено." || consumed != 1 {
		t.Fatalf("redelivery decision/calls = (%q, %d)", decision.Status.Text, consumed)
	}
}

func TestTerminalAuthorizationMessageWithUnconfirmedDeletionBlocksBeforePending(t *testing.T) {
	auth := authorizationFlowFunc{consume: func(context.Context, telegramcontroller.AuthorizationMessageLookup) (telegramcontroller.AuthorizationMessageBinding, error) {
		return telegramcontroller.AuthorizationMessageBinding{Bound: true, DeletionKnown: false}, nil
	}}
	controller := newController(t, creatorFunc(nil), &memorySessions{}, submitterFunc(nil), notifierFunc(nil), telegramcontroller.Options{Authorization: auth})
	t.Cleanup(func() { _ = controller.Close(context.Background()) })
	redelivery := message(152, "secret")
	redelivery.SourceMessageID = 9011
	if _, err := controller.Handle(context.Background(), redelivery); err == nil {
		t.Fatal("unconfirmed deletion redelivery advanced")
	}
}

func TestInteractionTombstoneIsConsumedBeforeAuthorizationPending(t *testing.T) {
	auth := authorizationFlowFunc{pending: func(context.Context, telegramcontroller.AuthorizationPendingLookup) ([]telegramcontroller.PendingAuthorization, error) {
		t.Fatal("interaction tombstone reached authorization pending")
		return nil, nil
	}}
	interactions := interactionTextFlow{
		consume: func(_ context.Context, input telegramcontroller.InteractionTextInput) (telegramcontroller.InteractionTextResult, error) {
			if input.SourceMessageID != 9012 {
				t.Fatalf("interaction tombstone lookup = %#v", input)
			}
			return telegramcontroller.InteractionTextResult{Handled: true, Secret: true, DeletionKnown: true, Status: "Ответ уже принят."}, nil
		},
		resolve: func(context.Context, telegramcontroller.InteractionTextInput) (telegramcontroller.InteractionTextResult, error) {
			t.Fatal("interaction tombstone reached pending resolver")
			return telegramcontroller.InteractionTextResult{}, nil
		},
	}
	controller := newController(t, creatorFunc(nil), &memorySessions{}, submitterFunc(nil), notifierFunc(nil), telegramcontroller.Options{
		Authorization: auth, InteractionText: interactions,
	})
	t.Cleanup(func() { _ = controller.Close(context.Background()) })
	redelivery := message(153, "secret-other")
	redelivery.SourceMessageID = 9012
	decision := mustStatus(t, controller, redelivery)
	if decision.Status.Text != "Ответ уже принят." {
		t.Fatalf("interaction tombstone status = %q", decision.Status.Text)
	}
}

func TestAmbiguousPendingAuthorizationDeletesMessageAndFailsClosed(t *testing.T) {
	discarded := int64(0)
	auth := authorizationFlowFunc{
		pending: func(context.Context, telegramcontroller.AuthorizationPendingLookup) ([]telegramcontroller.PendingAuthorization, error) {
			return []telegramcontroller.PendingAuthorization{{}, {}}, nil
		},
		discard: func(_ context.Context, request telegramcontroller.AuthorizationDiscard) (telegramcontroller.AuthorizationResult, error) {
			discarded = request.SourceMessageID
			return telegramcontroller.AuthorizationResult{DeletionKnown: true}, nil
		},
	}
	controller := newController(t, creatorFunc(nil), &memorySessions{}, submitterFunc(nil), notifierFunc(nil), telegramcontroller.Options{Authorization: auth})
	t.Cleanup(func() { _ = controller.Close(context.Background()) })
	update := message(152, "possible-secret")
	update.SourceMessageID = 9152
	decision := mustStatus(t, controller, update)
	if discarded != 9152 || !strings.Contains(decision.Status.Text, "Нельзя однозначно") {
		t.Fatalf("ambiguous auth result = discard %d status %q", discarded, decision.Status.Text)
	}
}

func TestPendingAuthorizationMediaDeletionFailureDoesNotAdvanceToNormalInput(t *testing.T) {
	durableInputs := 0
	var discard telegramcontroller.AuthorizationDiscard
	auth := authorizationFlowFunc{
		pending: func(context.Context, telegramcontroller.AuthorizationPendingLookup) ([]telegramcontroller.PendingAuthorization, error) {
			return []telegramcontroller.PendingAuthorization{{AuthorizationChallenge: telegramcontroller.AuthorizationChallenge{
				OperationID: "auth-op", ComputerID: "local", Provider: domain.ProviderCodex, ChallengeReference: "challenge",
			}, AcceptsSecret: true}}, nil
		},
		discard: func(_ context.Context, request telegramcontroller.AuthorizationDiscard) (telegramcontroller.AuthorizationResult, error) {
			discard = request
			return telegramcontroller.AuthorizationResult{DeletionKnown: false}, errors.New("delete outcome unknown")
		},
	}
	controller := newController(t, creatorFunc(nil), &memorySessions{}, submitterFunc(nil), notifierFunc(nil), telegramcontroller.Options{
		Authorization: auth,
		DurableInput: durableInputFunc(func(context.Context, telegramcontroller.SessionInput) (telegramcontroller.InputReceipt, error) {
			durableInputs++
			return telegramcontroller.InputReceipt{}, nil
		}),
	})
	t.Cleanup(func() { _ = controller.Close(context.Background()) })
	update := message(153, "")
	update.SourceMessageID = 9153
	update.Caption = "possible-secret"
	update.MediaKind = "photo"
	update.MediaFileID = "opaque-file"
	update.MediaDownloadAllowed = true
	if _, err := controller.Handle(context.Background(), update); err == nil {
		t.Fatal("Handle() error = nil, want unconfirmed deletion to block checkpoint")
	}
	if discard.OperationID != "telegram-message:9153:delete" || discard.SourceMessageID != 9153 || durableInputs != 0 {
		t.Fatalf("discard/journal = %#v/%d", discard, durableInputs)
	}
}

func TestStructuredAttachmentEntersDurableCustodyWithoutPathEncoding(t *testing.T) {
	ready := readySession(t, "55555555-5555-4555-9555-555555555555", domain.ProviderCodex, t.TempDir(), "provider-5", 1)
	wantAttachment := telegramcontroller.AttachmentRef{
		Reference: "photo-custody-55", Size: 2048,
		SHA256: "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef",
	}
	var accepted telegramcontroller.SessionInput
	controller := newController(t, creatorFunc(nil), &memorySessions{byID: map[domain.SessionID]domain.Session{ready.ID(): ready}}, submitterFunc(nil), notifierFunc(nil), telegramcontroller.Options{
		Recovered:     []domain.Session{ready},
		InputPreparer: structuredPreparer{prepared: telegramcontroller.PreparedInput{Text: "inspect", Attachments: []telegramcontroller.AttachmentRef{wantAttachment}}},
		DurableInput: durableInputFunc(func(_ context.Context, input telegramcontroller.SessionInput) (telegramcontroller.InputReceipt, error) {
			accepted = input
			return telegramcontroller.InputReceipt{Inserted: true, SessionID: input.SessionID, MessageID: input.MessageID, Sequence: 1}, nil
		}),
	})
	t.Cleanup(func() { _ = controller.Close(context.Background()) })
	mustStatus(t, controller, message(160, "/use "+string(ready.ID())))
	update := message(161, "look")
	update.MediaKind = "photo"
	update.MediaFileID = "telegram-opaque"
	update.MediaDownloadAllowed = true
	mustStatus(t, controller, update)
	if string(accepted.Payload) != "look\n\ninspect" || !reflect.DeepEqual(accepted.Attachments, []telegramcontroller.AttachmentRef{wantAttachment}) {
		t.Fatalf("durable structured input = %#v", accepted)
	}
	if strings.Contains(string(accepted.Payload), "/") || strings.Contains(accepted.Attachments[0].Reference, "/") {
		t.Fatalf("structured input leaked a path = %#v", accepted)
	}
}

func TestPersistedReadySessionIsNotUsableAfterControllerRestart(t *testing.T) {
	ready := readySession(t, "11111111-1111-4111-9111-111111111111", domain.ProviderCodex, t.TempDir(), "provider-1", 1)
	store := &memorySessions{byID: map[domain.SessionID]domain.Session{ready.ID(): ready}, listed: []domain.Session{ready}}
	submits := 0
	controller := newController(t, creatorFunc(nil), store, submitterFunc(func(context.Context, domain.SessionID, string) (sessionruntime.TurnResult, error) {
		submits++
		return sessionruntime.TurnResult{}, nil
	}), notifierFunc(func(context.Context, telegramcontroller.Notification) error { return nil }), telegramcontroller.Options{})
	t.Cleanup(func() { _ = controller.Close(context.Background()) })

	decision := mustStatus(t, controller, message(50, "/use "+string(ready.ID())))
	if !strings.Contains(decision.Status.Text, "процесс не запущен") {
		t.Fatalf("use persisted-ready status = %q", decision.Status.Text)
	}
	decision = mustStatus(t, controller, message(51, "do not submit"))
	if !strings.Contains(decision.Status.Text, "активной сессии") || submits != 0 {
		t.Fatalf("prompt after unusable /use = %#v, submits=%d", decision, submits)
	}
	decision = mustStatus(t, controller, message(52, "/sessions"))
	if !strings.Contains(decision.Status.Text, "процесс не запущен") {
		t.Fatalf("sessions status = %q", decision.Status.Text)
	}
}

func TestNotifierFailureDoesNotSuppressLaterEventsOrFinal(t *testing.T) {
	workdir := t.TempDir()
	ready := readySession(t, "11111111-1111-4111-9111-111111111111", domain.ProviderCodex, workdir, "provider-1", 1)
	creator := creatorFunc(func(_ context.Context, intent app.ConfirmedSessionIntent) (app.CreateSessionResult, error) {
		return app.CreateSessionResult{Session: sessionWithIntent(t, ready, intent.IntentID)}, nil
	})
	var mu sync.Mutex
	var kinds []telegramcontroller.NotificationKind
	done := make(chan struct{})
	notifier := notifierFunc(func(_ context.Context, notification telegramcontroller.Notification) error {
		mu.Lock()
		kinds = append(kinds, notification.Kind)
		count := len(kinds)
		mu.Unlock()
		if count == 3 {
			close(done)
		}
		if count == 1 {
			return errors.New("notification failed")
		}
		return nil
	})
	submitter := submitterFunc(func(context.Context, domain.SessionID, string) (sessionruntime.TurnResult, error) {
		return sessionruntime.TurnResult{
			Events: []sessionruntime.TurnEvent{
				{Kind: sessionruntime.EventCommentary, Text: "one"},
				{Kind: sessionruntime.EventQuestion, Text: "two"},
			},
			Final: "three", TerminalStatus: sessionruntime.StatusCompleted,
		}, nil
	})
	controller := newController(t, creator, &memorySessions{byID: map[domain.SessionID]domain.Session{ready.ID(): ready}}, submitter, notifier, telegramcontroller.Options{})
	t.Cleanup(func() { _ = controller.Close(context.Background()) })
	mustStatus(t, controller, message(60, "/new codex "+workdir))
	mustStatus(t, controller, message(61, "run"))
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("later notifications were suppressed")
	}
	mu.Lock()
	defer mu.Unlock()
	want := []telegramcontroller.NotificationKind{telegramcontroller.NotificationCommentary, telegramcontroller.NotificationQuestion, telegramcontroller.NotificationFinal}
	if len(kinds) != len(want) {
		t.Fatalf("notification kinds = %#v", kinds)
	}
	for index := range want {
		if kinds[index] != want[index] {
			t.Fatalf("notification kinds = %#v, want %#v", kinds, want)
		}
	}
}

func TestNotifierFailureIsRecordedUnknownWithoutAutomaticRetry(t *testing.T) {
	workdir := t.TempDir()
	ready := readySession(t, "11111111-1111-4111-9111-111111111111", domain.ProviderCodex, workdir, "provider-1", 1)
	creator := creatorFunc(func(_ context.Context, intent app.ConfirmedSessionIntent) (app.CreateSessionResult, error) {
		return app.CreateSessionResult{Session: sessionWithIntent(t, ready, intent.IntentID)}, nil
	})
	notifyCalls := make(chan telegramcontroller.Notification, 2)
	recorded := make(chan telegramcontroller.NotificationFailure, 1)
	controller := newController(t,
		creator,
		&memorySessions{byID: map[domain.SessionID]domain.Session{ready.ID(): ready}},
		submitterFunc(func(context.Context, domain.SessionID, string) (sessionruntime.TurnResult, error) {
			return sessionruntime.TurnResult{Final: "done", TerminalStatus: sessionruntime.StatusCompleted}, nil
		}),
		notifierFunc(func(_ context.Context, notification telegramcontroller.Notification) error {
			notifyCalls <- notification
			return errors.New("receipt unavailable")
		}),
		telegramcontroller.Options{OutputFailures: outputFailureRecorderFunc(func(_ context.Context, failure telegramcontroller.NotificationFailure) error {
			recorded <- failure
			return nil
		})},
	)
	t.Cleanup(func() { _ = controller.Close(context.Background()) })
	mustStatus(t, controller, message(60, "/new codex "+workdir))
	mustStatus(t, controller, message(61, "run"))

	select {
	case notification := <-notifyCalls:
		if notification.Kind != telegramcontroller.NotificationFinal {
			t.Fatalf("notification kind = %q, want final", notification.Kind)
		}
	case <-time.After(time.Second):
		t.Fatal("notification attempt missing")
	}
	select {
	case failure := <-recorded:
		if failure.SessionID != ready.ID() || failure.Kind != telegramcontroller.NotificationFinal || failure.State != telegramcontroller.DeliveryUnknown {
			t.Fatalf("recorded failure = %#v", failure)
		}
	case <-time.After(time.Second):
		t.Fatal("notification failure was not recorded")
	}
	failure, ok := controller.DeliveryFailure(ready.ID())
	if !ok || !failure.DurablyRecorded || failure.State != telegramcontroller.DeliveryUnknown {
		t.Fatalf("DeliveryFailure() = (%#v, %t), want durable unknown", failure, ok)
	}
	select {
	case duplicate := <-notifyCalls:
		t.Fatalf("notification was retried automatically: %#v", duplicate)
	case <-time.After(20 * time.Millisecond):
	}
}

func TestDurableOutputIsAcceptedBeforeDeliveryAndDirectNotifierIsBypassed(t *testing.T) {
	workdir := t.TempDir()
	ready := readySession(t, "11111111-1111-4111-9111-111111111111", domain.ProviderCodex, workdir, "provider-1", 1)
	creator := creatorFunc(func(_ context.Context, intent app.ConfirmedSessionIntent) (app.CreateSessionResult, error) {
		return app.CreateSessionResult{Session: sessionWithIntent(t, ready, intent.IntentID)}, nil
	})
	accepted := make(chan telegramcontroller.OutgoingNotification, 1)
	direct := make(chan telegramcontroller.Notification, 1)
	controller := newController(t,
		creator,
		&memorySessions{byID: map[domain.SessionID]domain.Session{ready.ID(): ready}},
		submitterFunc(func(context.Context, domain.SessionID, string) (sessionruntime.TurnResult, error) {
			return sessionruntime.TurnResult{Final: "durable final", TerminalStatus: sessionruntime.StatusCompleted}, nil
		}),
		notifierFunc(func(_ context.Context, notification telegramcontroller.Notification) error {
			direct <- notification
			return nil
		}),
		telegramcontroller.Options{DurableOutput: durableOutputFunc(func(_ context.Context, output telegramcontroller.OutgoingNotification) (telegramcontroller.OutputReceipt, error) {
			accepted <- output
			return telegramcontroller.OutputReceipt{Inserted: true, SessionID: output.SessionID, OperationID: output.OperationID, Sequence: 1}, nil
		})},
	)
	t.Cleanup(func() { _ = controller.Close(context.Background()) })
	mustStatus(t, controller, message(100, "/new codex "+workdir))
	mustStatus(t, controller, message(101, "run"))
	select {
	case output := <-accepted:
		if output.SessionID != ready.ID() || output.OperationID != "telegram-update:101:final" || output.Kind != telegramcontroller.NotificationFinal || string(output.Payload) != "durable final" {
			t.Fatalf("durable output = %#v", output)
		}
	case <-time.After(time.Second):
		t.Fatal("final did not enter durable output custody")
	}
	select {
	case notification := <-direct:
		t.Fatalf("durable output was also delivered directly: %#v", notification)
	case <-time.After(20 * time.Millisecond):
	}
}

func TestFailedTurnsEmitOneSafeErrorEachAndWorkerContinuesFIFO(t *testing.T) {
	workdir := t.TempDir()
	ready := readySession(t, "11111111-1111-4111-9111-111111111111", domain.ProviderClaude, workdir, "provider-secret-id", 1)
	creator := creatorFunc(func(_ context.Context, intent app.ConfirmedSessionIntent) (app.CreateSessionResult, error) {
		return app.CreateSessionResult{Session: sessionWithIntent(t, ready, intent.IntentID)}, nil
	})

	var submitMu sync.Mutex
	var submitted []string
	submitter := submitterFunc(func(_ context.Context, _ domain.SessionID, text string) (sessionruntime.TurnResult, error) {
		submitMu.Lock()
		submitted = append(submitted, text)
		submitMu.Unlock()
		switch text {
		case "transport":
			return sessionruntime.TurnResult{}, errors.New("provider-secret-id auth token-secret")
		case "failed":
			return sessionruntime.TurnResult{TerminalStatus: sessionruntime.StatusFailed, ErrorCode: sessionruntime.ErrorAuthenticationFailed}, nil
		case "interrupted":
			return sessionruntime.TurnResult{TerminalStatus: sessionruntime.StatusInterrupted, ErrorCode: sessionruntime.ErrorInterrupted}, nil
		default:
			return sessionruntime.TurnResult{Final: "recovered", TerminalStatus: sessionruntime.StatusCompleted}, nil
		}
	})
	notifications := make(chan telegramcontroller.Notification, 4)
	controller := newController(t,
		creator,
		&memorySessions{byID: map[domain.SessionID]domain.Session{ready.ID(): ready}},
		submitter,
		notifierFunc(func(_ context.Context, notification telegramcontroller.Notification) error {
			notifications <- notification
			return nil
		}),
		telegramcontroller.Options{QueueLimit: 4},
	)
	t.Cleanup(func() { _ = controller.Close(context.Background()) })

	mustStatus(t, controller, message(62, "/new claude "+workdir))
	for index, prompt := range []string{"transport", "failed", "interrupted", "success"} {
		mustStatus(t, controller, message(int64(63+index), prompt))
	}
	for index, wantKind := range []telegramcontroller.NotificationKind{
		telegramcontroller.NotificationError,
		telegramcontroller.NotificationError,
		telegramcontroller.NotificationError,
		telegramcontroller.NotificationFinal,
	} {
		select {
		case notification := <-notifications:
			if notification.Kind != wantKind {
				t.Fatalf("notification %d kind = %q, want %q", index, notification.Kind, wantKind)
			}
			if strings.Contains(notification.Text, "provider-secret-id") || strings.Contains(notification.Text, "token-secret") {
				t.Fatalf("notification %d leaked provider detail: %q", index, notification.Text)
			}
		case <-time.After(time.Second):
			t.Fatalf("notification %d not emitted", index)
		}
	}
	submitMu.Lock()
	defer submitMu.Unlock()
	wantSubmitted := []string{"transport", "failed", "interrupted", "success"}
	if len(submitted) != len(wantSubmitted) {
		t.Fatalf("submitted = %#v, want %#v", submitted, wantSubmitted)
	}
	for index := range wantSubmitted {
		if submitted[index] != wantSubmitted[index] {
			t.Fatalf("submitted = %#v, want FIFO %#v", submitted, wantSubmitted)
		}
	}
}

func TestCloseCancelsTurnsAndAbortsOnlySessionsCreatedByController(t *testing.T) {
	workdir := t.TempDir()
	ready := readySession(t, "11111111-1111-4111-9111-111111111111", domain.ProviderCodex, workdir, "provider-1", 1)
	creator := creatorFunc(func(_ context.Context, intent app.ConfirmedSessionIntent) (app.CreateSessionResult, error) {
		return app.CreateSessionResult{Session: sessionWithIntent(t, ready, intent.IntentID)}, nil
	})
	started := make(chan struct{})
	submitter := submitterFunc(func(ctx context.Context, _ domain.SessionID, _ string) (sessionruntime.TurnResult, error) {
		close(started)
		<-ctx.Done()
		return sessionruntime.TurnResult{}, ctx.Err()
	})
	var aborted []app.StartSessionRequest
	lifecycle := lifecycleFunc(func(_ context.Context, request app.StartSessionRequest, binding domain.ProviderBinding) error {
		if binding.SessionID != "provider-1" {
			t.Fatalf("Abort binding = %#v", binding)
		}
		aborted = append(aborted, request)
		return nil
	})
	controller := newController(t, creator, &memorySessions{byID: map[domain.SessionID]domain.Session{ready.ID(): ready}}, submitter, notifierFunc(func(context.Context, telegramcontroller.Notification) error { return nil }), telegramcontroller.Options{Lifecycle: lifecycle})
	mustStatus(t, controller, message(70, "/new codex "+workdir))
	mustStatus(t, controller, message(71, "running"))
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("turn did not start")
	}
	if err := controller.Close(context.Background()); err != nil {
		t.Fatalf("Close() error = %v", err)
	}
	if len(aborted) != 1 || aborted[0].SessionID != ready.ID() {
		t.Fatalf("Abort requests = %#v, want created session only", aborted)
	}
}

func TestCloseStartsEveryAbortConcurrentlyAndJoinsThemWithoutRepeating(t *testing.T) {
	workdirOne := t.TempDir()
	workdirTwo := t.TempDir()
	one := readySession(t, "11111111-1111-4111-9111-111111111111", domain.ProviderCodex, workdirOne, "provider-1", 1)
	two := readySession(t, "22222222-2222-4222-9222-222222222222", domain.ProviderClaude, workdirTwo, "provider-2", 1)
	creator := creatorFunc(func(_ context.Context, intent app.ConfirmedSessionIntent) (app.CreateSessionResult, error) {
		if intent.Provider == domain.ProviderCodex {
			return app.CreateSessionResult{Session: sessionWithIntent(t, one, intent.IntentID)}, nil
		}
		return app.CreateSessionResult{Session: sessionWithIntent(t, two, intent.IntentID)}, nil
	})

	entered := make(chan domain.SessionID, 2)
	exited := make(chan domain.SessionID, 2)
	release := make(chan struct{})
	lifecycle := lifecycleFunc(func(ctx context.Context, request app.StartSessionRequest, _ domain.ProviderBinding) error {
		entered <- request.SessionID
		select {
		case <-release:
		case <-ctx.Done():
			return ctx.Err()
		}
		exited <- request.SessionID
		return nil
	})
	controller := newController(t,
		creator,
		&memorySessions{},
		submitterFunc(nil),
		notifierFunc(nil),
		telegramcontroller.Options{Lifecycle: lifecycle},
	)
	mustStatus(t, controller, message(80, "/new codex "+workdirOne))
	mustStatus(t, controller, message(81, "/new claude "+workdirTwo))

	closed := make(chan error, 1)
	go func() { closed <- controller.Close(context.Background()) }()
	enteredIDs := map[domain.SessionID]bool{}
	for len(enteredIDs) < 2 {
		select {
		case id := <-entered:
			enteredIDs[id] = true
		case <-time.After(time.Second):
			t.Fatalf("only %d abort call(s) entered before release; aborts are not concurrent", len(enteredIDs))
		}
	}
	close(release)
	for index := 0; index < 2; index++ {
		select {
		case <-exited:
		case <-time.After(time.Second):
			t.Fatal("abort goroutine did not exit")
		}
	}
	select {
	case err := <-closed:
		if err != nil {
			t.Fatalf("Close() error = %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("Close() did not join concurrent aborts")
	}
	if err := controller.Close(context.Background()); err != nil {
		t.Fatalf("second Close() error = %v", err)
	}
	select {
	case id := <-entered:
		t.Fatalf("second Close() repeated abort for %q", id)
	default:
	}
}

func message(id int64, text string) coordinator.Update {
	return coordinator.Update{
		ID: id, Kind: coordinator.UpdateMessage, ActorID: ownerID,
		ConversationID: chatID, ConversationKind: "private", Text: text,
	}
}

func newController(
	t *testing.T,
	creator telegramcontroller.SessionCreator,
	sessions telegramcontroller.SessionStore,
	submitter sessionruntime.Submitter,
	notifier telegramcontroller.Notifier,
	options telegramcontroller.Options,
) *telegramcontroller.Controller {
	t.Helper()
	if creator == nil {
		creator = creatorFunc(func(context.Context, app.ConfirmedSessionIntent) (app.CreateSessionResult, error) {
			return app.CreateSessionResult{}, errors.New("unexpected create")
		})
	}
	if submitter == nil {
		submitter = submitterFunc(func(context.Context, domain.SessionID, string) (sessionruntime.TurnResult, error) {
			return sessionruntime.TurnResult{}, errors.New("unexpected submit")
		})
	}
	if notifier == nil {
		notifier = notifierFunc(func(context.Context, telegramcontroller.Notification) error { return nil })
	}
	controller, err := telegramcontroller.New(ownerID, chatID, "local", creator, sessions, submitter, notifier, options)
	if err != nil {
		t.Fatalf("telegramcontroller.New() error = %v", err)
	}
	return controller
}

func mustStatus(t *testing.T, controller *telegramcontroller.Controller, update coordinator.Update) coordinator.Decision {
	t.Helper()
	decision, err := controller.Handle(context.Background(), update)
	if err != nil || decision.Kind != coordinator.DecisionStatus || decision.Status.ConversationID != chatID {
		t.Fatalf("Handle(%q) = (%#v, %v), want status", update.Text, decision, err)
	}
	return decision
}

func waitString(t *testing.T, values <-chan string) string {
	t.Helper()
	select {
	case value := <-values:
		return value
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for submission")
		return ""
	}
}

func eventuallyStatusContains(t *testing.T, controller *telegramcontroller.Controller, update coordinator.Update, want string) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		decision, err := controller.Handle(context.Background(), update)
		if err == nil && strings.Contains(decision.Status.Text, want) {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("status never contained %q", want)
}

func readySession(t *testing.T, id string, provider domain.Provider, workdir, providerID string, generation uint64) domain.Session {
	t.Helper()
	starting, err := domain.NewStartingSession(domain.SessionID(id), domain.IntentID("intent-"+id), "local", provider, workdir)
	if err != nil {
		t.Fatalf("NewStartingSession() error = %v", err)
	}
	ready, err := starting.Ready(domain.ProviderBinding{Provider: provider, SessionID: providerID, Generation: generation})
	if err != nil {
		t.Fatalf("Ready() error = %v", err)
	}
	return ready
}

func archivedSession(t *testing.T, id string, provider domain.Provider, workdir, providerID string, generation uint64) domain.Session {
	t.Helper()
	session := readySession(t, id, provider, workdir, providerID, generation)
	now := session.StateChangedAt().Add(time.Minute)
	closing, err := session.BeginClose(now)
	if err != nil {
		t.Fatalf("BeginClose() error = %v", err)
	}
	archived, err := closing.Archive(now.Add(time.Minute))
	if err != nil {
		t.Fatalf("Archive() error = %v", err)
	}
	return archived
}

func sessionWithIntent(t *testing.T, session domain.Session, intentID domain.IntentID) domain.Session {
	t.Helper()
	snapshot := session.Snapshot()
	snapshot.IntentID = intentID
	updated, err := domain.RestoreSession(snapshot)
	if err != nil {
		t.Fatalf("RestoreSession() error = %v", err)
	}
	return updated
}

type creatorFunc func(context.Context, app.ConfirmedSessionIntent) (app.CreateSessionResult, error)

func (function creatorFunc) Create(ctx context.Context, intent app.ConfirmedSessionIntent) (app.CreateSessionResult, error) {
	if function == nil {
		return app.CreateSessionResult{}, errors.New("unexpected create")
	}
	return function(ctx, intent)
}

type asyncCreatorFunc func(context.Context, app.ConfirmedSessionIntent) (telegramcontroller.PendingSessionStart, error)

func (function asyncCreatorFunc) BeginCreate(ctx context.Context, intent app.ConfirmedSessionIntent) (telegramcontroller.PendingSessionStart, error) {
	return function(ctx, intent)
}

type asyncResumerFunc func(context.Context, domain.SessionID) (telegramcontroller.PendingSessionStart, error)

func (function asyncResumerFunc) BeginResume(ctx context.Context, id domain.SessionID) (telegramcontroller.PendingSessionStart, error) {
	return function(ctx, id)
}

type submitterFunc func(context.Context, domain.SessionID, string) (sessionruntime.TurnResult, error)

func (function submitterFunc) Submit(ctx context.Context, id domain.SessionID, text string) (sessionruntime.TurnResult, error) {
	if function == nil {
		return sessionruntime.TurnResult{}, errors.New("unexpected submit")
	}
	return function(ctx, id, text)
}

type interactiveSubmitter struct {
	plainCalls          int
	submitWithCallbacks func(context.Context, domain.SessionID, string, sessionruntime.TurnCallbacks) (sessionruntime.TurnResult, error)
}

func (submitter *interactiveSubmitter) Submit(context.Context, domain.SessionID, string) (sessionruntime.TurnResult, error) {
	submitter.plainCalls++
	return sessionruntime.TurnResult{}, errors.New("plain submit must not be used")
}

func (submitter *interactiveSubmitter) SubmitWithCallbacks(ctx context.Context, id domain.SessionID, text string, callbacks sessionruntime.TurnCallbacks) (sessionruntime.TurnResult, error) {
	return submitter.submitWithCallbacks(ctx, id, text, callbacks)
}

type interactionHandlerFunc func(context.Context, telegramcontroller.InteractionEnvelope) (sessionruntime.InteractionResponse, error)

func (function interactionHandlerFunc) ResolveInteraction(ctx context.Context, envelope telegramcontroller.InteractionEnvelope) (sessionruntime.InteractionResponse, error) {
	return function(ctx, envelope)
}

type acceptingInteractionHandler struct {
	resolve func(context.Context, telegramcontroller.InteractionEnvelope) (sessionruntime.InteractionResponse, error)
	confirm func(context.Context, telegramcontroller.InteractionResponseAcceptance) error
}

type interactionTextFlow struct {
	consume func(context.Context, telegramcontroller.InteractionTextInput) (telegramcontroller.InteractionTextResult, error)
	resolve func(context.Context, telegramcontroller.InteractionTextInput) (telegramcontroller.InteractionTextResult, error)
}

type runtimeEventObserverFunc func(context.Context, telegramcontroller.RuntimeEventObservation) error

func (function runtimeEventObserverFunc) ObserveRuntimeEvent(ctx context.Context, event telegramcontroller.RuntimeEventObservation) error {
	return function(ctx, event)
}

type finalProcessorFunc func(context.Context, telegramcontroller.FinalObservation) error

func (function finalProcessorFunc) ProcessFinal(ctx context.Context, final telegramcontroller.FinalObservation) error {
	return function(ctx, final)
}

func (flow interactionTextFlow) ConsumeBoundSourceMessage(ctx context.Context, input telegramcontroller.InteractionTextInput) (telegramcontroller.InteractionTextResult, error) {
	return flow.consume(ctx, input)
}

func (flow interactionTextFlow) ResolvePendingText(ctx context.Context, input telegramcontroller.InteractionTextInput) (telegramcontroller.InteractionTextResult, error) {
	return flow.resolve(ctx, input)
}

func (handler acceptingInteractionHandler) ResolveInteraction(ctx context.Context, envelope telegramcontroller.InteractionEnvelope) (sessionruntime.InteractionResponse, error) {
	return handler.resolve(ctx, envelope)
}

func (handler acceptingInteractionHandler) ConfirmInteractionResponse(ctx context.Context, acceptance telegramcontroller.InteractionResponseAcceptance) error {
	return handler.confirm(ctx, acceptance)
}

type authorizationFlowFunc struct {
	supports func(domain.Provider) bool
	start    func(context.Context, telegramcontroller.AuthorizationStart) (telegramcontroller.AuthorizationChallenge, error)
	consume  func(context.Context, telegramcontroller.AuthorizationMessageLookup) (telegramcontroller.AuthorizationMessageBinding, error)
	pending  func(context.Context, telegramcontroller.AuthorizationPendingLookup) ([]telegramcontroller.PendingAuthorization, error)
	submit   func(context.Context, telegramcontroller.AuthorizationSecret) (telegramcontroller.AuthorizationResult, error)
	discard  func(context.Context, telegramcontroller.AuthorizationDiscard) (telegramcontroller.AuthorizationResult, error)
}

func (flow authorizationFlowFunc) ConsumeAuthorizationMessage(ctx context.Context, request telegramcontroller.AuthorizationMessageLookup) (telegramcontroller.AuthorizationMessageBinding, error) {
	if flow.consume == nil {
		return telegramcontroller.AuthorizationMessageBinding{}, nil
	}
	return flow.consume(ctx, request)
}

type createDraftSelectorFunc struct {
	computerID domain.ComputerID
	workdir    string
}

func (selector createDraftSelectorFunc) PreviewCreateDraft(_ context.Context, provider domain.Provider) (telegramcontroller.CreateDraft, error) {
	return telegramcontroller.CreateDraft{ComputerID: selector.computerID, Provider: provider, Workdir: selector.workdir}, nil
}

func (selector createDraftSelectorFunc) ConfirmCreateDraft(_ context.Context, provider domain.Provider, _ int64) (telegramcontroller.CreateDraft, error) {
	return telegramcontroller.CreateDraft{ComputerID: selector.computerID, Provider: provider, Workdir: selector.workdir, Confirmed: true}, nil
}

func (flow authorizationFlowFunc) PendingAuthorizations(ctx context.Context, request telegramcontroller.AuthorizationPendingLookup) ([]telegramcontroller.PendingAuthorization, error) {
	if flow.pending == nil {
		return nil, nil
	}
	return flow.pending(ctx, request)
}

func (flow authorizationFlowFunc) SupportsAuthorization(provider domain.Provider) bool {
	if flow.supports != nil {
		return flow.supports(provider)
	}
	return provider == domain.ProviderCodex
}

func (flow authorizationFlowFunc) StartAuthorization(ctx context.Context, request telegramcontroller.AuthorizationStart) (telegramcontroller.AuthorizationChallenge, error) {
	return flow.start(ctx, request)
}

func (flow authorizationFlowFunc) SubmitAuthorization(ctx context.Context, request telegramcontroller.AuthorizationSecret) (telegramcontroller.AuthorizationResult, error) {
	return flow.submit(ctx, request)
}

func (flow authorizationFlowFunc) DiscardAuthorizationMessage(ctx context.Context, request telegramcontroller.AuthorizationDiscard) (telegramcontroller.AuthorizationResult, error) {
	if flow.discard == nil {
		return telegramcontroller.AuthorizationResult{DeletionKnown: true}, nil
	}
	return flow.discard(ctx, request)
}

type stopperFunc func(context.Context, domain.SessionID) error

func (function stopperFunc) StopCurrent(ctx context.Context, id domain.SessionID) error {
	return function(ctx, id)
}

type inputPreparerFunc func(context.Context, telegramcontroller.IncomingInput) (string, error)

func (function inputPreparerFunc) Prepare(ctx context.Context, input telegramcontroller.IncomingInput) (string, error) {
	return function(ctx, input)
}

type structuredPreparer struct {
	prepared telegramcontroller.PreparedInput
}

func (preparer structuredPreparer) Prepare(context.Context, telegramcontroller.IncomingInput) (string, error) {
	return "", errors.New("legacy string preparer must not be used")
}

func (preparer structuredPreparer) PrepareStructured(context.Context, telegramcontroller.IncomingInput) (telegramcontroller.PreparedInput, error) {
	return preparer.prepared, nil
}

type durableInputFunc func(context.Context, telegramcontroller.SessionInput) (telegramcontroller.InputReceipt, error)

func (function durableInputFunc) Accept(ctx context.Context, input telegramcontroller.SessionInput) (telegramcontroller.InputReceipt, error) {
	return function(ctx, input)
}

type archivedResumerFunc func(context.Context, domain.SessionID) (domain.Session, error)

func (function archivedResumerFunc) Resume(ctx context.Context, id domain.SessionID) (domain.Session, error) {
	return function(ctx, id)
}

type sessionCloserFunc func(context.Context, domain.SessionID) (app.CloseSessionResult, error)

func (function sessionCloserFunc) Close(ctx context.Context, id domain.SessionID) (app.CloseSessionResult, error) {
	return function(ctx, id)
}

type turnLifecycleFunc struct {
	start  func(context.Context, domain.SessionID) (domain.Session, error)
	stop   func(context.Context, domain.SessionID) (domain.Session, error)
	finish func(context.Context, domain.SessionID) (domain.Session, bool, error)
}

func (function turnLifecycleFunc) Start(ctx context.Context, id domain.SessionID) (domain.Session, error) {
	return function.start(ctx, id)
}

func (function turnLifecycleFunc) BeginStop(ctx context.Context, id domain.SessionID) (domain.Session, error) {
	return function.stop(ctx, id)
}

func (function turnLifecycleFunc) Finish(ctx context.Context, id domain.SessionID) (domain.Session, bool, error) {
	return function.finish(ctx, id)
}

type notifierFunc func(context.Context, telegramcontroller.Notification) error

func (function notifierFunc) Notify(ctx context.Context, notification telegramcontroller.Notification) error {
	if function == nil {
		return nil
	}
	return function(ctx, notification)
}

type outputFailureRecorderFunc func(context.Context, telegramcontroller.NotificationFailure) error

func (function outputFailureRecorderFunc) RecordNotificationFailure(ctx context.Context, failure telegramcontroller.NotificationFailure) error {
	return function(ctx, failure)
}

type durableOutputFunc func(context.Context, telegramcontroller.OutgoingNotification) (telegramcontroller.OutputReceipt, error)

func (function durableOutputFunc) AcceptOutput(ctx context.Context, output telegramcontroller.OutgoingNotification) (telegramcontroller.OutputReceipt, error) {
	return function(ctx, output)
}

type lifecycleFunc func(context.Context, app.StartSessionRequest, domain.ProviderBinding) error

func (function lifecycleFunc) Abort(ctx context.Context, request app.StartSessionRequest, binding domain.ProviderBinding) error {
	return function(ctx, request, binding)
}

type memorySessions struct {
	byID   map[domain.SessionID]domain.Session
	listed []domain.Session
}

type lockedSessions struct {
	mu   sync.RWMutex
	byID map[domain.SessionID]domain.Session
}

func newLockedSessions(sessions ...domain.Session) *lockedSessions {
	store := &lockedSessions{byID: make(map[domain.SessionID]domain.Session, len(sessions))}
	for _, session := range sessions {
		store.byID[session.ID()] = session
	}
	return store
}

func (sessions *lockedSessions) Set(session domain.Session) {
	sessions.mu.Lock()
	sessions.byID[session.ID()] = session
	sessions.mu.Unlock()
}

func (sessions *lockedSessions) List(context.Context) ([]domain.Session, error) {
	sessions.mu.RLock()
	defer sessions.mu.RUnlock()
	result := make([]domain.Session, 0, len(sessions.byID))
	for _, session := range sessions.byID {
		result = append(result, session)
	}
	return result, nil
}

func (sessions *lockedSessions) Load(_ context.Context, id domain.SessionID) (domain.Session, error) {
	sessions.mu.RLock()
	defer sessions.mu.RUnlock()
	if session, ok := sessions.byID[id]; ok {
		return session, nil
	}
	return domain.Session{}, errors.New("session not found")
}

func (sessions *memorySessions) List(context.Context) ([]domain.Session, error) {
	return append([]domain.Session(nil), sessions.listed...), nil
}

func (sessions *memorySessions) Load(_ context.Context, id domain.SessionID) (domain.Session, error) {
	if session, ok := sessions.byID[id]; ok {
		return session, nil
	}
	return domain.Session{}, errors.New("session not found")
}
