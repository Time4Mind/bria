package telegramflow_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"bria/internal/app"
	"bria/internal/coordinator"
	"bria/internal/domain"
	"bria/internal/observability"
	"bria/internal/safelog"
	"bria/internal/sessionruntime"
	"bria/internal/storage"
	"bria/internal/telegramcontroller"
	"bria/internal/telegramflow"
	"bria/internal/telegrampipeline"
	"bria/internal/telegrampromptcomposition"
	"bria/internal/telegramruntimecomposition"
)

// A23 consumer acceptance: real lifecycle, disk state, controller, signed
// callback adapter, prompt delivery and flow Sender. Only provider termination
// and the Telegram transport are fake. The barriers choose a delivery order;
// they do not manufacture a lifecycle result or a fallback projection.
func TestArchiveSwitchIntegration(t *testing.T) {
	for _, scenario := range []struct {
		name                    string
		remaining               bool
		fallbackCarrier         int64
		lateClose               bool
		archiveBeforeProjection bool
	}{
		{"same_carrier", true, 99, false, false},
		{"different_carrier", true, 77, false, false},
		{"no_fallback_carrier", true, 0, false, false},
		{"no_remaining_session", false, 0, false, false},
		{"prepared_close_after_fallback_refresh", true, 99, true, false},
		{"archive_before_close_projection", true, 77, false, true},
		{"archive_before_close_projection_no_remaining", false, 0, false, true},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			ctx := context.Background()
			path := filepath.Join(t.TempDir(), "state.json")
			store, err := storage.OpenSessionStore(path)
			if err != nil {
				t.Fatal(err)
			}
			ids := []domain.SessionID{"11111111-1111-4111-9111-111111111111"}
			if scenario.remaining {
				ids = append(ids, "22222222-2222-4222-9222-222222222222")
			}
			var sessions []domain.Session
			for i, id := range ids {
				starting, err := domain.NewStartingSession(id, domain.IntentID("intent-"+string(id)), "local", domain.ProviderCodex, "/synthetic")
				if err != nil {
					t.Fatal(err)
				}
				if _, _, err := store.PutStartingIfAbsent(ctx, starting); err != nil {
					t.Fatal(err)
				}
				ready, err := starting.Ready(domain.ProviderBinding{Provider: domain.ProviderCodex, SessionID: "provider-" + string(id), Generation: 1})
				if err != nil {
					t.Fatal(err)
				}
				if err := store.CompareAndSwap(ctx, starting, ready); err != nil {
					t.Fatal(err)
				}
				marker := "CLOSED_SESSION_CONTENT"
				if i == 1 {
					marker = "REMAINING_SESSION_CONTENT"
				}
				if err := store.SetCardPrompt(ctx, id, "prompt-"+string(id), marker); err != nil {
					t.Fatal(err)
				}
				carrier := int64(99)
				if i == 1 {
					carrier = scenario.fallbackCarrier
				}
				if carrier > 0 {
					if err := store.SetCardCarrier(ctx, id, 42, carrier); err != nil {
						t.Fatal(err)
					}
				}
				sessions = append(sessions, ready)
			}
			release := make(chan struct{})
			var releaseOnce sync.Once
			unblock := func() { releaseOnce.Do(func() { close(release) }) }
			defer unblock()
			if scenario.archiveBeforeProjection {
				unblock()
			}
			closer, err := app.NewSessionCloser(store, archiveSwitchProvider{release}, time.Now)
			if err != nil {
				t.Fatal(err)
			}
			completed := make(chan struct{})
			var delivery telegrampromptcomposition.Deliverer
			deliveryErrors := make(chan error, 8)
			options := telegramcontroller.Options{Recovered: sessions, UIState: store, SessionCloser: archiveSwitchCompletion{closer, completed, scenario.archiveBeforeProjection}}
			var observer *observability.TelegramFlowObserver
			logDir := filepath.Join(filepath.Dir(path), "logs")
			if scenario.name == "same_carrier" {
				logger, err := safelog.Open(safelog.Options{Directory: logDir})
				if err != nil {
					t.Fatal(err)
				}
				observer, err = observability.NewTelegramFlowObserver(logger)
				if err != nil {
					t.Fatal(err)
				}
				defer observer.Close()
				options.ControllerObserver = observer
			}
			controller, err := telegramcontroller.New(7, 42, "local", archiveSwitchUnused{}, store, archiveSwitchUnused{},
				archiveSwitchNotify(func(ctx context.Context, notice telegramcontroller.Notification) error {
					_, err := delivery.Deliver(ctx, notice, notice.OperationID)
					deliveryErrors <- err
					return err
				}), options)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { unblock(); _ = controller.Close(ctx) }()
			if _, err := controller.HandleSemanticAction(ctx, telegramcontroller.SemanticAction{Kind: telegramcontroller.SemanticSelect, SessionID: ids[0]}); err != nil {
				t.Fatal(err)
			}
			now := time.Unix(1_800_000_000, 0).UTC()
			presenter := newPresenter(t, now)
			wire := &archiveSwitchWire{cards: make(map[int64]coordinator.Status), keyboards: make(map[int64]*coordinator.KeyboardMarkup)}
			adapter := telegramruntimecomposition.ControllerFlowAdapter{Controller: controller}
			cards := telegramruntimecomposition.SessionTelegramUIStore{State: store}
			flowConfig := telegramflow.Config{
				OwnerUserID: 7, OwnerPrivateChatID: 42, Presenter: presenter,
				CallbackRegistry: telegrampipeline.NewMemoryCallbackRegistry(func() time.Time { return now }),
				UIState:          cards, MessageUI: adapter, Callbacks: adapter,
				Operations: telegramflow.NewMemoryCallbackOperationStore(), Sender: wire,
			}
			if observer != nil {
				flowConfig.Observer = observer
			}
			handler, outbound, err := telegramflow.New(flowConfig)
			if err != nil {
				t.Fatal(err)
			}
			delivery = telegrampromptcomposition.Deliverer{Controller: controller, Cards: cards, Presenter: presenter, Sender: outbound}
			// Obtain the close confirmation and confirmed action through the
			// real signed Handler, including callback persistence and receipts.
			if _, err := delivery.Deliver(ctx, telegramcontroller.Notification{Kind: telegramcontroller.NotificationPromptStatus, SessionID: ids[0]}, "initial-card"); err != nil {
				t.Fatal(err)
			}
			first := wire.keyboard(99)
			token := archiveSwitchButton(t, first, "Закрыть")
			decision, err := handler.Handle(ctx, archiveSwitchUpdate(1, token))
			if err != nil {
				t.Fatal(err)
			}
			if _, err := outbound.EditStatusWithKeyboard(ctx, "status:1", decision.Status, decision.Keyboard); err != nil {
				t.Fatal(err)
			}
			confirm := archiveSwitchButton(t, decision.Keyboard, "Архивировать")
			callbackCtx, cancel := context.WithCancel(ctx)
			closeDecision, err := handler.Handle(callbackCtx, archiveSwitchUpdate(2, confirm))
			cancel()
			if err != nil {
				t.Fatal(err)
			}
			// A ready session has durably entered Closing while Abort is still
			// held. Its callback must already project the selectable fallback or
			// menu; an eventual background refresh cannot repair a late A edit.
			closing, err := store.Load(ctx, ids[0])
			wantStatus := domain.SessionClosing
			if scenario.archiveBeforeProjection {
				wantStatus = domain.SessionArchived
			}
			if err != nil || closing.Status() != wantStatus {
				t.Fatalf("before provider exit status=%s err=%v", closing.Status(), err)
			}
			want := domain.SessionID("")
			if scenario.remaining {
				want = ids[1]
				if !strings.Contains(closeDecision.Status.Text, "REMAINING_SESSION_CONTENT") || strings.Contains(closeDecision.Status.Text, "CLOSED_SESSION_CONTENT") {
					t.Errorf("close callback prepared old card after durable Closing: %q", closeDecision.Status.Text)
				}
			} else if closeDecision.Status.Text != "Сессии" {
				t.Errorf("close callback did not prepare empty session menu: %q", closeDecision.Status.Text)
			}
			selected, err := store.LoadActiveSession(ctx)
			if err != nil || selected != want {
				t.Errorf("selection before provider exit=%q err=%v want=%q", selected, err, want)
			}
			sendClose := func() {
				if _, err := outbound.EditStatusWithKeyboard(ctx, "status:2", closeDecision.Status, closeDecision.Keyboard); err != nil {
					t.Errorf("close delivery: %v", err)
				}
			}
			if !scenario.lateClose {
				sendClose()
			}
			unblock()
			select {
			case <-completed:
			case <-time.After(3 * time.Second):
				t.Fatal("async archive/controller completion did not finish")
			}
			if scenario.lateClose {
				sendClose()
			}
			for len(deliveryErrors) > 0 {
				if err := <-deliveryErrors; err != nil {
					t.Errorf("fallback delivery: %v", err)
				}
			}
			// Reopen disk state, not just the controller's cached selection.
			reread, err := storage.OpenSessionStore(path)
			if err != nil {
				t.Fatal(err)
			}
			archived, err := reread.Load(ctx, ids[0])
			if err != nil || archived.Status() != domain.SessionArchived {
				t.Fatalf("durable archive=%s err=%v", archived.Status(), err)
			}
			active, err := reread.LoadActiveSession(ctx)
			if err != nil || active != want {
				t.Errorf("durable active=%q err=%v want=%q", active, err, want)
			}
			visible := wire.card(99).Text
			if scenario.remaining {
				if !strings.Contains(visible, "REMAINING_SESSION_CONTENT") || strings.Contains(visible, "CLOSED_SESSION_CONTENT") {
					t.Errorf("owner's visible close carrier did not switch to remaining session: %q", visible)
				}
			} else if visible != "Сессии" {
				t.Errorf("owner's visible close carrier did not switch to empty session menu: %q", visible)
			}
			if observer != nil {
				observer.Close() // Flush the actual shared queue before disk inspection.
				archiveSwitchLogChain(t, logDir, []string{string(ids[0]), string(ids[1]), token, confirm,
					"CLOSED_SESSION_CONTENT", "REMAINING_SESSION_CONTENT", "/synthetic", "provider-", "status:2", "query-2"})
			}
		})
	}
}

func archiveSwitchLogChain(t *testing.T, dir string, forbidden []string) {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(dir, "detailed.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	for _, private := range forbidden {
		if private != "" && strings.Contains(string(raw), private) {
			t.Fatalf("safe JSONL contains fixture content or raw identity %q", private)
		}
	}
	var rows []safelog.Event
	for _, line := range strings.Split(strings.TrimSpace(string(raw)), "\n") {
		var event safelog.Event
		if err := json.Unmarshal([]byte(line), &event); err != nil {
			t.Fatal(err)
		}
		rows = append(rows, event)
	}
	var closeRef, targetRef, runRef string
	for i, row := range rows {
		if row.Fields["sequence"] != strconv.Itoa(i+1) {
			t.Fatalf("non-contiguous physical log sequence at %d", i)
		}
		if runRef == "" {
			runRef = row.Fields["run_ref"]
		}
		if runRef == "" || row.Fields["run_ref"] != runRef {
			t.Fatal("controller/flow do not share one log run")
		}
		if row.Fields["stage"] == "selection.fallback" && row.Result == "selected" {
			closeRef, targetRef = row.EntityID, row.Fields["target_session_ref"]
		}
	}
	if closeRef == "" || targetRef == "" {
		t.Fatal("actual controller did not log correlated fallback choice")
	}
	wantStages := []string{"callback.plan", "session.archive_outcome", "selection.fallback", "selection.persist", "selection.project", "card.edit_started", "transport.edit", "state.commit", "session.archive_outcome"}
	next := 0
	var actual []string
	var commitCardRef string
	for _, row := range rows {
		if row.EntityID != closeRef {
			continue
		}
		stage := row.Fields["stage"]
		actual = append(actual, stage+":"+row.Result)
		if stage == "selection.persist" || stage == "selection.project" {
			if row.Fields["target_session_ref"] != targetRef {
				t.Errorf("%s changed fallback target reference", stage)
			}
			wantOutcome := "persisted"
			if stage == "selection.project" {
				wantOutcome = "projected"
			}
			if row.Result != wantOutcome || row.ErrorCategory != "" {
				t.Errorf("unsuccessful %s in actual close chain: %+v", stage, row)
			}
		}
		if stage == "callback.plan" && row.Fields["action"] != "close" {
			t.Error("controller close chain is linked to another callback action")
		}
		if stage == "card.edit_started" || stage == "transport.edit" || stage == "state.commit" {
			if row.Fields["session_ref"] != targetRef {
				t.Errorf("%s card HMAC does not match actual controller fallback", stage)
			}
			if row.Result != "completed" || row.ErrorCategory != "" {
				t.Errorf("unconfirmed %s in real close chain: %+v", stage, row)
			}
			if commitCardRef == "" {
				commitCardRef = row.Fields["card_ref"]
			}
			if commitCardRef == "" || row.Fields["card_ref"] != commitCardRef {
				t.Errorf("%s changed physical carrier reference", stage)
			}
		}
		if next < len(wantStages) && stage == wantStages[next] {
			if stage == "session.archive_outcome" {
				want := "scheduled"
				if next == len(wantStages)-1 {
					want = "archived"
				}
				if row.Result != want {
					continue
				}
			}
			next++
		}
	}
	if next != len(wantStages) {
		t.Fatalf("incomplete physical close chain: matched %d/%d; actual=%v", next, len(wantStages), actual)
	}
}

func archiveSwitchUpdate(id int64, token string) coordinator.Update {
	return coordinator.Update{ID: id, Kind: coordinator.UpdateCallback, ActorID: 7, ConversationID: 42, ConversationKind: "private", Text: token, CallbackQueryID: fmt.Sprintf("query-%d", id), SourceMessageID: 99}
}

func archiveSwitchButton(t *testing.T, keyboard *coordinator.KeyboardMarkup, label string) string {
	t.Helper()
	if keyboard != nil {
		for _, row := range *keyboard {
			for _, button := range row {
				if strings.Contains(button.Text, label) {
					return button.CallbackData
				}
			}
		}
	}
	t.Fatalf("button %q not present in %+v", label, keyboard)
	return ""
}

type archiveSwitchUnused struct{}

func (archiveSwitchUnused) Create(context.Context, app.ConfirmedSessionIntent) (app.CreateSessionResult, error) {
	return app.CreateSessionResult{}, errors.New("unexpected create")
}
func (archiveSwitchUnused) Submit(context.Context, domain.SessionID, string) (sessionruntime.TurnResult, error) {
	return sessionruntime.TurnResult{}, errors.New("unexpected submit")
}

type archiveSwitchProvider struct{ release <-chan struct{} }

func (archiveSwitchProvider) Start(context.Context, app.StartSessionRequest) (domain.ProviderBinding, error) {
	return domain.ProviderBinding{}, errors.New("unexpected provider start")
}
func (p archiveSwitchProvider) Abort(context.Context, app.StartSessionRequest, domain.ProviderBinding) error {
	<-p.release
	return nil
}

// The observer barrier waits until the actual controller callback has returned,
// including its notification. It leaves the real closer's result unchanged.
type archiveSwitchCompletion struct {
	*app.SessionCloser
	completed               chan struct{}
	archiveBeforeProjection bool
}

func (c archiveSwitchCompletion) BeginCloseWithCompletion(ctx context.Context, id domain.SessionID, callback func(context.Context, app.CloseSessionResult, error)) (app.CloseSessionResult, error) {
	durable := make(chan struct{})
	result, err := c.SessionCloser.BeginCloseWithCompletion(ctx, id, func(ctx context.Context, result app.CloseSessionResult, err error) {
		close(durable)
		callback(ctx, result, err)
		close(c.completed)
	})
	if err == nil && c.archiveBeforeProjection {
		select {
		case <-durable:
		case <-time.After(3 * time.Second):
			return result, errors.New("durable archive before projection did not finish")
		}
	}
	return result, err
}

type archiveSwitchNotify func(context.Context, telegramcontroller.Notification) error

func (n archiveSwitchNotify) Notify(ctx context.Context, notice telegramcontroller.Notification) error {
	return n(ctx, notice)
}

type archiveSwitchWire struct {
	mu        sync.Mutex
	cards     map[int64]coordinator.Status
	keyboards map[int64]*coordinator.KeyboardMarkup
}

func (s *archiveSwitchWire) SendStatus(context.Context, string, coordinator.Status) (coordinator.Receipt, error) {
	return coordinator.Receipt{}, errors.New("unexpected unsigned send")
}
func (s *archiveSwitchWire) SendStatusWithKeyboard(context.Context, string, coordinator.Status, *coordinator.KeyboardMarkup) (coordinator.Receipt, error) {
	return coordinator.Receipt{}, errors.New("archive switch must edit visible carrier")
}
func (s *archiveSwitchWire) EditStatusWithKeyboard(_ context.Context, _ string, status coordinator.Status, keyboard *coordinator.KeyboardMarkup) (coordinator.Receipt, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.cards[status.SourceMessageID], s.keyboards[status.SourceMessageID] = status, keyboard
	return coordinator.Receipt{MessageID: status.SourceMessageID}, nil
}
func (s *archiveSwitchWire) card(id int64) coordinator.Status {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.cards[id]
}
func (s *archiveSwitchWire) keyboard(id int64) *coordinator.KeyboardMarkup {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.keyboards[id]
}
