package telegrampromptcomposition

import (
	"bytes"
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"bria/internal/callbacktoken"
	"bria/internal/coordinator"
	"bria/internal/domain"
	"bria/internal/telegrambridge"
	"bria/internal/telegramcontroller"
	"bria/internal/telegramflow"
	"bria/internal/telegramnotify"
	"bria/internal/telegramstate"
)

type controllerStub struct {
	card    telegramcontroller.SemanticCard
	surface *telegramcontroller.SemanticSurface
}

type visibleNativeController struct {
	controllerStub
	visible bool
}

type cancelableNativeController struct {
	visibleNativeController
	view context.Context
}

func (c cancelableNativeController) NativeDeliveryContext(parent context.Context, _ domain.SessionID) (context.Context, context.CancelFunc, bool) {
	ctx, cancel := context.WithCancel(parent)
	stop := context.AfterFunc(c.view, cancel)
	return ctx, func() { stop(); cancel() }, true
}

type queuedNativeSender struct {
	senderStub
	queued chan struct{}
}

func (s *queuedNativeSender) EditStatusWithKeyboard(ctx context.Context, _ string, _ coordinator.Status, _ *coordinator.KeyboardMarkup) (coordinator.Receipt, error) {
	close(s.queued)
	<-ctx.Done()
	return coordinator.Receipt{}, ctx.Err()
}

func (c visibleNativeController) NativeScreenVisible(domain.SessionID) bool { return c.visible }

func (stub controllerStub) ProjectCurrent(context.Context, domain.SessionID) (telegramcontroller.SemanticActionResult, error) {
	if stub.surface != nil {
		return telegramcontroller.SemanticActionResult{Surface: stub.surface}, nil
	}
	return telegramcontroller.SemanticActionResult{Card: &stub.card}, nil
}

type senderStub struct {
	prepared telegramflow.Prepared
	status   coordinator.Status
}

type observedSender struct {
	edits chan coordinator.Status
}

func (stub *observedSender) Register(telegramflow.Prepared) error { return nil }

func (stub *observedSender) EditStatusWithKeyboard(_ context.Context, _ string, status coordinator.Status, _ *coordinator.KeyboardMarkup) (coordinator.Receipt, error) {
	stub.edits <- status
	return coordinator.Receipt{MessageID: status.SourceMessageID}, nil
}

func (stub *senderStub) Register(prepared telegramflow.Prepared) error {
	stub.prepared = prepared
	return nil
}

func (stub *senderStub) EditStatusWithKeyboard(_ context.Context, _ string, status coordinator.Status, _ *coordinator.KeyboardMarkup) (coordinator.Receipt, error) {
	stub.status = status
	return coordinator.Receipt{MessageID: status.SourceMessageID}, nil
}

func TestDelivererEditsActiveCardWithInCardMarker(t *testing.T) {
	now := time.Date(2026, 9, 4, 12, 0, 0, 0, time.UTC)
	codec, err := callbacktoken.New(bytes.Repeat([]byte{9}, 32), bytes.NewReader(make([]byte, 4096)), func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	presenter, err := telegrambridge.NewPresenter(codec, func() time.Time { return now }, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	sessionID := domain.SessionID("00000000-0000-4000-8000-000000000001")
	card := telegramcontroller.SemanticCard{
		SessionID: sessionID, Effect: telegramcontroller.SemanticEditSameCarrier, Header: "Сессия test",
		Pages:                []telegramcontroller.SemanticContentPage{{Content: "👨‍💻 Проверь проект", Anchors: []string{"prompt"}}},
		View:                 telegramcontroller.SemanticPageView{Page: 1, Pages: 1, Anchor: "prompt", FollowLatest: true},
		SelectableSessionIDs: []domain.SessionID{sessionID}, SessionRowSizes: []int{1}, MakeActive: true,
		SelectableSessionLabels: []string{"✓ test"},
	}
	state := telegramstate.NewMemoryStore()
	if err := state.Update(context.Background(), func(current *telegramstate.State) error {
		current.ActiveSession = sessionID
		return current.SetCard(telegramstate.Card{
			SessionID: sessionID, Carrier: telegramstate.Carrier{ChatID: 42, MessageID: 77},
			Page: telegramstate.Page{Current: 1, Total: 1, FollowLatest: true},
		})
	}); err != nil {
		t.Fatal(err)
	}
	sender := &senderStub{}
	deliverer := Deliverer{Controller: controllerStub{card: card}, Cards: state, Presenter: presenter, Sender: sender}
	receipt, err := deliverer.Deliver(context.Background(), telegramcontroller.Notification{
		OperationID: "prompt-status:1", ConversationID: 42, SessionID: sessionID,
		Kind: telegramcontroller.NotificationPromptStatus, Text: "👨‍💻",
	}, "prompt-status:1")
	if err != nil || receipt.State != "confirmed" || receipt.Parts[0].MessageID != 77 {
		t.Fatalf("Deliver() = (%#v, %v)", receipt, err)
	}
	if sender.status.SourceMessageID != 77 || !strings.Contains(sender.status.Text, "👨‍💻 Проверь проект") {
		t.Fatalf("active prompt refresh = %#v", sender.status)
	}
	if sender.prepared.Keyboard == nil {
		t.Fatal("prompt keyboard missing")
	}
	if revision := sender.prepared.Card.ExpectedCarrierRevision; revision == nil || *revision != 1 {
		t.Fatalf("prompt refresh lost captured carrier revision: %v", revision)
	}
	for _, message := range []int64{78, 77} {
		if err := state.Update(context.Background(), func(current *telegramstate.State) error {
			card, _ := current.Card(sessionID)
			card.Carrier.MessageID = message
			return current.SetCard(card)
		}); err != nil {
			t.Fatal(err)
		}
	}
	if *sender.prepared.Card.ExpectedCarrierRevision != 1 {
		t.Fatal("prepared revision changed with later carrier state")
	}
	foundName := false
	for _, row := range *sender.prepared.Keyboard {
		for _, button := range row {
			if button.Text == "✓ test" {
				foundName = true
			}
		}
	}
	if !foundName {
		t.Fatalf("prompt refresh lost selected session name: %+v", sender.prepared.Keyboard)
	}
	// Voice recognition must refresh a visible transcript without requiring
	// a menu round-trip. Hidden views are covered by real navigation tests.
	sender = &senderStub{}
	deliverer.Controller = visibleNativeController{controllerStub: controllerStub{card: card}, visible: true}
	deliverer.Sender = sender
	receipt, err = deliverer.Deliver(context.Background(), telegramcontroller.Notification{
		OperationID: "prompt-status:recognized", ConversationID: 42, SessionID: sessionID,
		Kind: telegramcontroller.NotificationPromptStatus, Text: "🙋‍♂",
	}, "prompt-status:recognized")
	if err != nil || receipt.State != "confirmed" || receipt.Suppressed || sender.status.SourceMessageID != 77 {
		t.Fatalf("prompt status was suppressed while transcript visible: %#v %v", receipt, err)
	}
	for _, nativeID := range []domain.SessionID{sessionID, "wrong-session", ""} {
		sender := &senderStub{}
		deliverer.Controller = controllerStub{surface: &telegramcontroller.SemanticSurface{Text: "CLI menu", NativeSessionID: nativeID}}
		deliverer.Sender = sender
		receipt, err := deliverer.Deliver(context.Background(), telegramcontroller.Notification{Kind: telegramcontroller.NotificationPromptStatus, SessionID: sessionID}, "prompt-native")
		if nativeID == sessionID {
			if err != nil || receipt.State != "confirmed" || !receipt.Suppressed {
				t.Fatalf("native overlay prompt state must remain stored-confirmed: %#v %v", receipt, err)
			}
		} else if err == nil {
			t.Fatalf("wrong or absent native identity accepted: %q", nativeID)
		}
		if sender.status.Text != "" || sender.prepared.OperationID != "" {
			t.Fatal("prompt refresh overwrote native keyboard")
		}
	}
	native := telegramcontroller.SemanticSurface{Text: "Choose actual model", NativeSessionID: sessionID, Rows: [][]telegramcontroller.SemanticButton{
		{{Label: "↑", Action: telegramcontroller.SemanticNativeKey, SessionID: sessionID, Choice: 1}, {Label: "⏎ Enter", Action: telegramcontroller.SemanticNativeKey, SessionID: sessionID, Choice: 5}},
		{{Label: "К сессии", Action: telegramcontroller.SemanticSelect, SessionID: sessionID}, {Label: "Меню", Action: telegramcontroller.SemanticMenuBack}},
	}}
	for _, visible := range []bool{false, true} {
		sender := &senderStub{}
		deliverer.Controller = visibleNativeController{controllerStub: controllerStub{surface: &native}, visible: visible}
		deliverer.Sender = sender
		receipt, err := deliverer.Deliver(context.Background(), telegramcontroller.Notification{Kind: telegramcontroller.NotificationNativeScreen, SessionID: sessionID}, "native:observation")
		if err != nil || receipt.State != "confirmed" {
			t.Fatalf("native delivery: %+v %v", receipt, err)
		}
		if !visible {
			if !receipt.Suppressed || sender.status.Text != "" {
				t.Fatal("queued native observation overwrote menu")
			}
			continue
		}
		if sender.status.Text != native.Text || sender.status.SourceMessageID != 77 || sender.prepared.Keyboard == nil {
			t.Fatalf("native picker not delivered to exact card: %+v", sender.status)
		}
		if revision := sender.prepared.Surface.ExpectedCarrierRevision; revision == nil || *revision != 3 {
			t.Fatalf("native refresh lost captured carrier revision: %v", revision)
		}
		for _, row := range *sender.prepared.Keyboard {
			for _, b := range row {
				if b.CallbackData == "" {
					t.Fatal("unsigned native key")
				}
			}
		}
	}
	view, cancelView := context.WithCancel(context.Background())
	defer cancelView()
	queued := &queuedNativeSender{queued: make(chan struct{})}
	deliverer.Controller = cancelableNativeController{visibleNativeController: visibleNativeController{controllerStub: controllerStub{surface: &native}, visible: true}, view: view}
	deliverer.Sender = queued
	finished := make(chan error, 1)
	go func() {
		receipt, err := deliverer.Deliver(context.Background(), telegramcontroller.Notification{Kind: telegramcontroller.NotificationNativeScreen, SessionID: sessionID}, "native:queued")
		if err == nil && (!receipt.Suppressed || receipt.State != "confirmed" || len(receipt.Parts) != 0) {
			err = fmt.Errorf("navigation cancellation receipt=%+v", receipt)
		}
		finished <- err
	}()
	select {
	case <-queued.queued:
	case <-time.After(time.Second):
		t.Fatal("native edit did not reach queue")
	}
	cancelView() // user navigation replaced the view while its edit waited
	select {
	case err := <-finished:
		if err != nil {
			t.Fatalf("canceled queued edit was not suppressed: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("menu cancellation did not leave edit queue")
	}
	if queued.status.Text != "" {
		t.Fatal("canceled native edit mutated the menu")
	}
}

func TestPromptStatusWaitsForItsNewInputCarrierAndNeverEditsPreviousCard(t *testing.T) {
	now := time.Date(2026, 9, 11, 10, 0, 0, 0, time.UTC)
	codec, err := callbacktoken.New(bytes.Repeat([]byte{7}, 32), bytes.NewReader(make([]byte, 4096)), func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	presenter, err := telegrambridge.NewPresenter(codec, func() time.Time { return now }, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	const sessionID = domain.SessionID("00000000-0000-4000-8000-000000000071")
	card := telegramcontroller.SemanticCard{
		SessionID: sessionID, Effect: telegramcontroller.SemanticEditSameCarrier, Header: "Сессия test\n",
		Pages: []telegramcontroller.SemanticContentPage{{Content: "👨‍💻 Новый запрос", Anchors: []string{"prompt"}}},
		View:  telegramcontroller.SemanticPageView{Page: 1, Pages: 1, Anchor: "prompt", FollowLatest: true},
	}
	state := telegramstate.NewMemoryStore()
	if err := state.Update(context.Background(), func(current *telegramstate.State) error {
		current.ActiveSession = sessionID
		return current.SetCard(telegramstate.Card{
			SessionID: sessionID, Carrier: telegramstate.Carrier{ChatID: 42, MessageID: 77},
			CarrierRevision: 1, LastPresentationOperation: "status:70",
			Page: telegramstate.Page{Current: 1, Total: 1, FollowLatest: false},
		})
	}); err != nil {
		t.Fatal(err)
	}
	sender := &observedSender{edits: make(chan coordinator.Status, 1)}
	deliverer := Deliverer{Controller: controllerStub{card: card}, Cards: state, Presenter: presenter, Sender: sender}
	type result struct {
		receipt telegramnotify.DeliveryReceipt
		err     error
	}
	done := make(chan result, 1)
	go func() {
		receipt, deliverErr := deliverer.Deliver(context.Background(), telegramcontroller.Notification{
			OperationID: "telegram-update:71:prompt-status:accepted", ConversationID: 42,
			SessionID: sessionID, Kind: telegramcontroller.NotificationPromptStatus, Text: "👨‍💻",
		}, "telegram-update:71:prompt-status:accepted")
		done <- result{receipt: receipt, err: deliverErr}
	}()
	select {
	case edit := <-sender.edits:
		t.Fatalf("previous carrier was edited before new input card committed: %#v", edit)
	case <-time.After(50 * time.Millisecond):
	}
	if err := state.Update(context.Background(), func(current *telegramstate.State) error {
		stored, _ := current.Card(sessionID)
		stored.Carrier = telegramstate.Carrier{ChatID: 42, MessageID: 78}
		stored.LastPresentationOperation = "status:71"
		return current.SetCard(stored)
	}); err != nil {
		t.Fatal(err)
	}
	select {
	case got := <-done:
		if got.err != nil || got.receipt.State != telegramnotify.DeliveryConfirmed {
			t.Fatalf("Deliver() = (%#v, %v)", got.receipt, got.err)
		}
	case <-time.After(time.Second):
		t.Fatal("prompt status did not resume after new carrier commit")
	}
	select {
	case edit := <-sender.edits:
		if edit.SourceMessageID != 78 {
			t.Fatalf("prompt status edited carrier %d, want 78", edit.SourceMessageID)
		}
	default:
		t.Fatal("new input carrier was not refreshed")
	}
}
