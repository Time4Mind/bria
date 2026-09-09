package main

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"bria/internal/callbacktoken"
	"bria/internal/coordinator"
	"bria/internal/domain"
	"bria/internal/sessionruntime"
	"bria/internal/telegram"
	"bria/internal/telegrambridge"
	"bria/internal/telegramcompletioncomposition"
	"bria/internal/telegramcontroller"
	"bria/internal/telegramflow"
	"bria/internal/telegramnotify"
	"bria/internal/telegrampromptcomposition"
	"bria/internal/telegramstate"
)

// Navigation and projections use the actual controller; only provider ports,
// time and HTTP are fixtures. No provider operation is invoked by these tests.
type navigationPorts struct {
	telegramcontroller.SessionCreator
	sessionruntime.Submitter
	sessions []domain.Session
}

func (p navigationPorts) List(context.Context) ([]domain.Session, error) { return p.sessions, nil }
func (p navigationPorts) Load(_ context.Context, id domain.SessionID) (domain.Session, error) {
	for _, s := range p.sessions {
		if s.ID() == id {
			return s, nil
		}
	}
	return domain.Session{}, errors.New("fixture session absent")
}
func (navigationPorts) Notify(context.Context, telegramcontroller.Notification) error { return nil }
func (navigationPorts) SetActiveSession(context.Context, domain.SessionID) error      { return nil }
func (navigationPorts) LoadCardHistory(context.Context, domain.SessionID) ([]string, error) {
	return []string{"BACKGROUND CONTENT"}, nil
}

type navigationRequestKey struct{}

type navigationWire struct {
	mu       sync.Mutex
	text     string
	stage    string
	entered  chan struct{}
	left     chan struct{}
	release  chan struct{}
	waitOnce sync.Once
}

func (w *navigationWire) barrier(ctx context.Context) error {
	w.waitOnce.Do(func() { close(w.entered) })
	select {
	case <-ctx.Done():
		close(w.left)
		return ctx.Err()
	case <-w.release:
		return nil
	}
}
func (w *navigationWire) Do(req *http.Request) (*http.Response, error) {
	if w.stage == "failure" {
		return &http.Response{StatusCode: 400, Body: io.NopCloser(strings.NewReader(`{"ok":false,"error_code":400,"description":"synthetic invalid request"}`)), Header: make(http.Header)}, nil
	}
	if req.Context().Value(navigationRequestKey{}) != nil && w.stage == "http" {
		if err := w.barrier(req.Context()); err != nil {
			return nil, err
		}
	}
	if err := req.Context().Err(); err != nil {
		return nil, err
	}
	body, err := io.ReadAll(req.Body)
	if err != nil {
		return nil, err
	}
	w.mu.Lock()
	w.text = string(body)
	w.mu.Unlock()
	return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(`{"ok":true,"result":{"message_id":77,"from":{"id":600,"is_bot":true},"chat":{"id":42,"type":"private"}}}`)), Header: make(http.Header)}, nil
}
func (w *navigationWire) current() string { w.mu.Lock(); defer w.mu.Unlock(); return w.text }

type navigationSender struct {
	*telegrambridge.Sender
	wire *navigationWire
}

// The prepared payload is delivered through the real Rich bridge and HTTP.
// Durable callback commits are covered by the integration owner's Flow seam.
func (s navigationSender) Register(telegramflow.Prepared) error {
	if s.wire.stage == "prepared" {
		close(s.wire.entered)
		<-s.wire.release
	}
	return nil
}

type navigationFixture struct {
	c         *telegramcontroller.Controller
	wire      *navigationWire
	sender    navigationSender
	prompts   telegrampromptcomposition.Deliverer
	outputs   telegramcompletioncomposition.CompletionDeliverer
	id, other domain.SessionID
}

type navigationDeliveryResult struct {
	receipt telegramnotify.DeliveryReceipt
	err     error
}

func newNavigationFixture(t *testing.T, stage string) *navigationFixture {
	t.Helper()
	ctx := context.Background()
	p := navigationPorts{}
	for _, id := range []domain.SessionID{"11111111-1111-4111-9111-111111111111", "22222222-2222-4222-9222-222222222222"} {
		starting, err := domain.NewStartingSession(id, domain.IntentID(id), "local", domain.ProviderCodex, "/fixture")
		if err != nil {
			t.Fatal(err)
		}
		ready, err := starting.Ready(domain.ProviderBinding{Provider: domain.ProviderCodex, SessionID: "native-" + string(id), Generation: 1})
		if err != nil {
			t.Fatal(err)
		}
		p.sessions = append(p.sessions, ready)
	}
	c, err := telegramcontroller.New(7, 42, "local", p, p, p, p, telegramcontroller.Options{Recovered: p.sessions, UIState: p})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = c.Close(context.Background()) })
	id := p.sessions[0].ID()
	if _, err := c.HandleSemanticAction(ctx, telegramcontroller.SemanticAction{Kind: telegramcontroller.SemanticSelect, SessionID: id}); err != nil {
		t.Fatal(err)
	}
	if !c.NativeScreenVisible(id) {
		t.Fatal("real selected card is not visible")
	}
	cards := telegramstate.NewMemoryStore()
	if err := cards.Update(ctx, func(s *telegramstate.State) error {
		s.ActiveSession = id
		return s.SetCard(telegramstate.Card{SessionID: id, Carrier: telegramstate.Carrier{ChatID: 42, MessageID: 77}, Page: telegramstate.Page{Current: 1, Total: 1, FollowLatest: true}})
	}); err != nil {
		t.Fatal(err)
	}
	wire := &navigationWire{stage: stage, entered: make(chan struct{}), left: make(chan struct{}), release: make(chan struct{})}
	var clockMu sync.Mutex
	now := time.Unix(1800000000, 0)
	clockNow := func() time.Time { clockMu.Lock(); defer clockMu.Unlock(); return now }
	scheduler, err := telegram.OpenMutationScheduler(telegram.SchedulerOptions{Now: clockNow, Wait: func(ctx context.Context, d time.Duration) error {
		if ctx.Value(navigationRequestKey{}) != nil && stage == "scheduler" {
			if err := wire.barrier(ctx); err != nil {
				return err
			}
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		clockMu.Lock()
		now = now.Add(d)
		clockMu.Unlock()
		return nil
	}})
	if err != nil {
		t.Fatal(err)
	}
	lease, err := scheduler.Acquire(ctx, telegram.Mutation{Method: "editMessageText", ChatID: 42, CardID: 77})
	if err != nil {
		t.Fatal(err)
	}
	if err := lease.Complete(telegram.MutationOutcome{}); err != nil {
		t.Fatal(err)
	}
	client, err := telegram.NewClient("123:navigation-fixture", wire, telegram.Options{Scheduler: scheduler})
	if err != nil {
		t.Fatal(err)
	}
	base, err := telegrambridge.NewSender(client)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = base.Close(context.Background()) })
	sender := navigationSender{base, wire}
	codec, err := callbacktoken.New(bytes.Repeat([]byte{7}, 32), nil, clockNow)
	if err != nil {
		t.Fatal(err)
	}
	presenter, err := telegrambridge.NewPresenter(codec, clockNow, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	return &navigationFixture{c: c, wire: wire, sender: sender, id: id, other: p.sessions[1].ID(),
		prompts: telegrampromptcomposition.Deliverer{Controller: c, Cards: cards, Presenter: presenter, Sender: sender},
		outputs: telegramcompletioncomposition.CompletionDeliverer{Controller: c, Cards: cards, Presenter: presenter, Sender: sender, ConversationID: 42}}
}

func (f *navigationFixture) navigate(t *testing.T, ctx context.Context, target string) {
	t.Helper()
	var result telegramcontroller.SemanticActionResult
	var err error
	switch target {
	case "menu":
		result, err = f.c.HandleSemanticMessage(ctx, coordinator.Update{ID: 101, ActorID: 7, ConversationID: 42, ConversationKind: "private", Kind: coordinator.UpdateMessage, Text: "/menu"})
	case "nodes":
		result, err = f.c.HandleSemanticAction(ctx, telegramcontroller.SemanticAction{Kind: telegramcontroller.SemanticMenuNodes, SessionID: f.id})
	case "other-session":
		result, err = f.c.HandleSemanticAction(ctx, telegramcontroller.SemanticAction{Kind: telegramcontroller.SemanticSelect, SessionID: f.other})
	}
	if err != nil {
		t.Fatal(err)
	}
	if f.c.NativeScreenVisible(f.id) {
		t.Fatal("navigation left old session visible")
	}
	text := ""
	if result.Surface != nil {
		text = result.Surface.Text
	}
	if result.Card != nil {
		text = result.Card.Header
	}
	if text == "" {
		t.Fatal("navigation produced no screen")
	}
	_, err = f.sender.EditStatusWithKeyboard(ctx, "navigation", coordinator.Status{ConversationID: 42, SourceMessageID: 77, Text: text}, nil)
	if err != nil {
		t.Fatal(err)
	}
}

func (f *navigationFixture) deliver(ctx context.Context, kind telegramcontroller.NotificationKind) (telegramnotify.DeliveryReceipt, error) {
	ctx = context.WithValue(ctx, navigationRequestKey{}, true)
	n := telegramcontroller.Notification{Kind: kind, SessionID: f.id, OperationID: "background", ConversationID: 42}
	if kind == telegramcontroller.NotificationPromptStatus {
		return f.prompts.Deliver(ctx, n, n.OperationID)
	}
	return f.outputs.Deliver(ctx, n, n.OperationID)
}

func TestNavigationSuppressesRoutineEditsOfStillActiveSession(t *testing.T) {
	for _, kind := range []telegramcontroller.NotificationKind{telegramcontroller.NotificationPromptStatus, telegramcontroller.NotificationCommentary, telegramcontroller.NotificationQuestion} {
		for _, target := range []string{"menu", "nodes", "other-session"} {
			t.Run(string(kind)+"/"+target, func(t *testing.T) {
				ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
				defer cancel()
				f := newNavigationFixture(t, "")
				f.navigate(t, ctx, target)
				before := f.wire.current()
				receipt, err := f.deliver(ctx, kind)
				if err != nil || !receipt.Suppressed || receipt.State != telegramnotify.DeliveryConfirmed || f.wire.current() != before {
					t.Fatalf("hidden session overwrote chosen screen: suppressed=%t state=%s err=%v changed=%t", receipt.Suppressed, receipt.State, err, f.wire.current() != before)
				}
			})
		}
	}
}

func TestNavigationCancelsRoutineEditInSchedulerAndHTTP(t *testing.T) {
	for _, kind := range []telegramcontroller.NotificationKind{telegramcontroller.NotificationPromptStatus, telegramcontroller.NotificationCommentary, telegramcontroller.NotificationQuestion} {
		for _, stage := range []string{"prepared", "scheduler", "http"} {
			t.Run(string(kind)+"/"+stage, func(t *testing.T) {
				ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
				defer cancel()
				f := newNavigationFixture(t, stage)
				done := make(chan navigationDeliveryResult, 1)
				go func() { receipt, err := f.deliver(ctx, kind); done <- navigationDeliveryResult{receipt, err} }()
				select {
				case <-f.wire.entered:
				case <-ctx.Done():
					t.Fatal("edit did not reach barrier")
				}
				f.navigate(t, ctx, "menu")
				menu := f.wire.current()
				if stage != "prepared" {
					select {
					case <-f.wire.left:
					case <-time.After(time.Second):
						t.Error("navigation did not cancel old edit context")
					}
				}
				close(f.wire.release)
				select {
				case result := <-done:
					// Scheduler supersession is an existing successful no-op.
					// Navigation cancellation is confirmed suppression, not retry.
					if result.err != nil || result.receipt.State != telegramnotify.DeliveryConfirmed {
						t.Errorf("navigation treated as delivery failure: %+v", result)
					}
					if stage == "http" && (!result.receipt.Suppressed || len(result.receipt.Parts) != 0) {
						t.Error("canceled HTTP edit claimed a physical receipt")
					}
				case <-ctx.Done():
					t.Fatal("edit failed to drain within bounded context")
				}
				if f.wire.current() != menu {
					t.Error("late edit overwrote newer menu on HTTP wire")
				}
			})
		}
	}
}

func TestNavigationGuardDoesNotSuppressParentCancellation(t *testing.T) {
	for _, kind := range []telegramcontroller.NotificationKind{telegramcontroller.NotificationPromptStatus, telegramcontroller.NotificationCommentary, telegramcontroller.NotificationQuestion} {
		t.Run(string(kind), func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			f := newNavigationFixture(t, "http")
			done := make(chan navigationDeliveryResult, 1)
			go func() { receipt, err := f.deliver(ctx, kind); done <- navigationDeliveryResult{receipt, err} }()
			select {
			case <-f.wire.entered:
			case <-time.After(time.Second):
				t.Fatal("HTTP request missing")
			}
			cancel()
			select {
			case result := <-done:
				if !errors.Is(result.err, context.Canceled) || result.receipt.Suppressed || result.receipt.State != telegramnotify.DeliveryUnknown {
					t.Fatalf("parent cancellation hidden: %+v", result)
				}
			case <-time.After(time.Second):
				t.Fatal("parent cancellation did not drain")
			}
		})
	}
}

func TestNavigationGuardDoesNotSuppressControllerShutdown(t *testing.T) {
	for _, kind := range []telegramcontroller.NotificationKind{telegramcontroller.NotificationPromptStatus, telegramcontroller.NotificationCommentary, telegramcontroller.NotificationQuestion} {
		t.Run(string(kind), func(t *testing.T) {
			parent, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			f := newNavigationFixture(t, "http")
			done := make(chan navigationDeliveryResult, 1)
			go func() { receipt, err := f.deliver(parent, kind); done <- navigationDeliveryResult{receipt, err} }()
			select {
			case <-f.wire.entered:
			case <-parent.Done():
				t.Fatal("HTTP request missing")
			}
			closing, cancelClose := context.WithTimeout(context.Background(), time.Second)
			defer cancelClose()
			if err := f.c.Close(closing); err != nil {
				t.Fatal(err)
			}
			select {
			case result := <-done:
				if parent.Err() != nil {
					t.Fatal("delivery parent must remain alive during controller shutdown")
				}
				if !errors.Is(result.err, context.Canceled) || result.receipt.Suppressed || result.receipt.State != telegramnotify.DeliveryUnknown || len(result.receipt.Parts) != 0 {
					t.Fatalf("controller shutdown hidden as navigation: %+v", result)
				}
			case <-parent.Done():
				t.Fatal("controller shutdown did not drain HTTP delivery")
			}
			if f.wire.current() != "" {
				t.Fatal("canceled HTTP edit published content")
			}
		})
	}
}

func TestNavigationGuardDoesNotSuppressDeliveryAfterControllerClose(t *testing.T) {
	for _, kind := range []telegramcontroller.NotificationKind{telegramcontroller.NotificationPromptStatus, telegramcontroller.NotificationCommentary, telegramcontroller.NotificationQuestion} {
		t.Run(string(kind), func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			f := newNavigationFixture(t, "")
			if err := f.c.Close(ctx); err != nil {
				t.Fatal(err)
			}
			delivery, release, visible := f.c.NativeDeliveryContext(ctx, f.id)
			defer release()
			if visible || delivery.Err() == nil || context.Cause(delivery) == context.Canceled {
				t.Errorf("closed controller must be invisible with distinct cancellation cause")
			}
			receipt, err := f.deliver(ctx, kind)
			if ctx.Err() != nil {
				t.Fatal("delivery parent must remain alive")
			}
			if !errors.Is(err, context.Canceled) || receipt.State != telegramnotify.DeliveryUnknown || receipt.Suppressed || len(receipt.Parts) != 0 {
				t.Fatalf("closed controller delivery suppressed: receipt=%+v err=%v", receipt, err)
			}
			if f.wire.current() != "" {
				t.Fatal("closed controller published an HTTP edit")
			}
		})
	}
}

func TestNavigationAfterAcceptedRoutineHTTPKeepsNewScreen(t *testing.T) {
	for _, kind := range []telegramcontroller.NotificationKind{telegramcontroller.NotificationPromptStatus, telegramcontroller.NotificationCommentary, telegramcontroller.NotificationQuestion} {
		t.Run(string(kind), func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			f := newNavigationFixture(t, "")
			if receipt, err := f.deliver(ctx, kind); err != nil || receipt.State != telegramnotify.DeliveryConfirmed {
				t.Fatalf("visible edit=%+v err=%v", receipt, err)
			}
			old := f.wire.current()
			f.navigate(t, ctx, "menu")
			if f.wire.current() == old || !strings.Contains(f.wire.current(), "Меню") {
				t.Fatal("menu did not replace already accepted old edit")
			}
		})
	}
}

func TestNavigationGuardKeepsTransportErrorsVisible(t *testing.T) {
	for _, kind := range []telegramcontroller.NotificationKind{telegramcontroller.NotificationPromptStatus, telegramcontroller.NotificationCommentary, telegramcontroller.NotificationQuestion} {
		t.Run(string(kind), func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			f := newNavigationFixture(t, "failure")
			receipt, err := f.deliver(ctx, kind)
			if err == nil || receipt.Suppressed || receipt.State != telegramnotify.DeliveryUnknown || len(receipt.Parts) != 0 {
				t.Fatalf("real HTTP failure was suppressed: receipt=%+v err=%v", receipt, err)
			}
		})
	}
}

type publicationRaceController struct {
	*telegramcontroller.Controller
	after func()
}

type publicationRaceSender struct {
	navigationSender
	after func()
}

func (s publicationRaceSender) Register(p telegramflow.Prepared) error {
	if err := s.navigationSender.Register(p); err != nil {
		return err
	}
	s.after()
	return nil
}

func (c publicationRaceController) ProjectCurrent(ctx context.Context, id domain.SessionID) (telegramcontroller.SemanticActionResult, error) {
	result, err := c.Controller.ProjectCurrent(ctx, id)
	c.after()
	return result, err
}

func (c publicationRaceController) ProjectCompletion(ctx context.Context, id domain.SessionID) (telegramcontroller.SemanticCard, bool, error) {
	card, active, err := c.Controller.ProjectCompletion(ctx, id)
	c.after()
	return card, active, err
}

func setNavigationPendingFinals(t *testing.T, card *telegramstate.Card, operations ...string) {
	t.Helper()
	card.PendingFinalOperations = append([]string(nil), operations...)
}

func TestRoutineRefreshSuppressesReopenedPendingFinal(t *testing.T) {
	for _, kind := range []telegramcontroller.NotificationKind{telegramcontroller.NotificationPromptStatus, telegramcontroller.NotificationCommentary, telegramcontroller.NotificationQuestion} {
		t.Run(string(kind), func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			f := newNavigationFixture(t, "")
			path := filepath.Join(t.TempDir(), "cards.json")
			file, err := telegramstate.OpenFileStore(path)
			if err != nil {
				t.Fatal(err)
			}
			initial, err := f.prompts.Cards.Load(ctx)
			if err != nil {
				t.Fatal(err)
			}
			if err := file.Update(ctx, func(state *telegramstate.State) error {
				*state = initial
				card, _ := state.Card(f.id)
				setNavigationPendingFinals(t, &card, "request-a:final", "request-b:final")
				return state.SetCard(card)
			}); err != nil {
				t.Fatal(err)
			}
			reopened, err := telegramstate.OpenFileStore(path)
			if err != nil {
				t.Fatal(err)
			}
			f.prompts.Cards, f.outputs.Cards = reopened, reopened
			receipt, err := f.deliver(ctx, kind)
			if err != nil || !receipt.Suppressed || receipt.State != telegramnotify.DeliveryConfirmed || len(receipt.Parts) != 0 || f.wire.current() != "" {
				t.Fatalf("reopened pending final reached old carrier: receipt=%+v err=%v", receipt, err)
			}
		})
	}
}

func TestRoutineRefreshRechecksCarrierAndFinalAfterProjectionAndRegistration(t *testing.T) {
	for _, kind := range []telegramcontroller.NotificationKind{telegramcontroller.NotificationPromptStatus, telegramcontroller.NotificationCommentary, telegramcontroller.NotificationQuestion} {
		for _, point := range []string{"projection", "registration"} {
			for _, change := range []string{"new-carrier", "pending-final", "other-active"} {
				t.Run(string(kind)+"/"+point+"/"+change, func(t *testing.T) {
					ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
					defer cancel()
					f := newNavigationFixture(t, "")
					store := f.prompts.Cards.(*telegramstate.MemoryStore)
					changeState := func() {
						if err := store.Update(ctx, func(state *telegramstate.State) error {
							card, _ := state.Card(f.id)
							switch change {
							case "new-carrier":
								card.Carrier.MessageID = 88
							case "pending-final":
								setNavigationPendingFinals(t, &card, "request:final")
							case "other-active":
								state.ActiveSession = ""
							}
							return state.SetCard(card)
						}); err != nil {
							t.Fatal(err)
						}
					}
					if point == "projection" {
						controller := publicationRaceController{Controller: f.c, after: changeState}
						f.prompts.Controller, f.outputs.Controller = controller, controller
					} else {
						sender := publicationRaceSender{navigationSender: f.sender, after: changeState}
						f.prompts.Sender, f.outputs.Sender = sender, sender
					}
					receipt, err := f.deliver(ctx, kind)
					if err != nil || !receipt.Suppressed || receipt.State != telegramnotify.DeliveryConfirmed || f.wire.current() != "" {
						t.Fatalf("projection dispatched stale carrier/state: receipt=%+v err=%v", receipt, err)
					}
				})
			}
		}
	}
}
