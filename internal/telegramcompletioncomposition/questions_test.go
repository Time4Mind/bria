package telegramcompletioncomposition

import (
	"bria/internal/callbacktoken"
	"bria/internal/domain"
	"bria/internal/settings"
	"bria/internal/settingscomposition"
	"bria/internal/telegrambridge"
	"bria/internal/telegramcontroller"
	"bria/internal/telegramnotify"
	"bria/internal/telegramstate"
	"bytes"
	"context"
	"testing"
	"time"
)

type visibleQuestionController struct {
	completionControllerStub
	visible bool
}

func (c visibleQuestionController) NativeScreenVisible(domain.SessionID) bool { return c.visible }

type questionDeliverySpy struct {
	calls     int
	operation string
}

func (d *questionDeliverySpy) Deliver(_ context.Context, _ telegramcontroller.Notification, operation string) (telegramnotify.DeliveryReceipt, error) {
	d.calls++
	d.operation = operation
	return telegramnotify.DeliveryReceipt{OperationID: operation, State: telegramnotify.DeliveryConfirmed}, nil
}

func TestRouterQuestionsCannotBypassPolicyThroughRawNotifier(t *testing.T) {
	fallback, policy := &questionDeliverySpy{}, &questionDeliverySpy{}
	router, err := NewRouter(fallback)
	if err != nil {
		t.Fatal(err)
	}
	if err := router.BindFinals(policy); err != nil {
		t.Fatal(err)
	}
	if _, err := router.Deliver(context.Background(), telegramcontroller.Notification{Kind: telegramcontroller.NotificationQuestion}, "question:1"); err != nil {
		t.Fatal(err)
	}
	if fallback.calls != 0 || policy.calls != 1 || policy.operation != "question:1" {
		t.Fatalf("fallback=%#v policy=%#v", fallback, policy)
	}
}

func TestQuestionPolicyOffOptInAndActiveVisibility(t *testing.T) {
	const id domain.SessionID = "00000000-0000-4000-8000-000000000001"
	for _, test := range []struct {
		name                                                         string
		enabled, selected, active, visible, wantSuppressed, wantEdit bool
	}{
		{"background default off", false, false, false, false, true, false},
		{"background opt in", true, false, false, false, false, false},
		{"active visible", false, true, true, true, false, true},
		{"active menu hidden", true, true, true, false, true, false},
		{"native overlay", true, true, false, true, true, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			ctx := context.Background()
			now := time.Now()
			codec, err := callbacktoken.New(bytes.Repeat([]byte{7}, 32), nil, func() time.Time { return now })
			if err != nil {
				t.Fatal(err)
			}
			presenter, err := telegrambridge.NewPresenter(codec, func() time.Time { return now }, time.Minute)
			if err != nil {
				t.Fatal(err)
			}
			preferences := settings.NewMemoryStore()
			if err := preferences.Update(ctx, func(s *settings.Settings) error { s.NotifyBackgroundQuestions = test.enabled; return nil }); err != nil {
				t.Fatal(err)
			}
			store := telegramstate.NewMemoryStore()
			if err := store.Update(ctx, func(s *telegramstate.State) error {
				if test.selected {
					s.ActiveSession = id
				}
				return s.SetCard(telegramstate.Card{SessionID: id, Carrier: telegramstate.Carrier{ChatID: 42, MessageID: 55}, Page: telegramstate.Page{Current: 1, Total: 1, FollowLatest: true}})
			}); err != nil {
				t.Fatal(err)
			}
			card := telegramcontroller.SemanticCard{SessionID: id, Header: "Session", Pages: []telegramcontroller.SemanticContentPage{{Content: "existing context and question", Anchors: []string{"latest"}}}, View: telegramcontroller.SemanticPageView{Page: 1, Pages: 1, Anchor: "latest", FollowLatest: true}, SelectableSessionIDs: []domain.SessionID{id}, SessionRowSizes: []int{1}}
			sender := &completionSenderStub{}
			deliverer := CompletionDeliverer{Controller: visibleQuestionController{completionControllerStub: completionControllerStub{card: card, active: test.active}, visible: test.visible}, Presenter: presenter, Sender: sender, Cards: store, Preferences: settingscomposition.Preferences{Store: preferences}, ConversationID: 42}
			receipt, err := deliverer.Deliver(ctx, telegramcontroller.Notification{Kind: telegramcontroller.NotificationQuestion, SessionID: id, Text: "RAW PRIVATE QUESTION"}, "question:1")
			if err != nil || receipt.State != telegramnotify.DeliveryConfirmed || receipt.Suppressed != test.wantSuppressed || sender.edited != test.wantEdit {
				t.Fatalf("receipt=%#v sender=%#v err=%v", receipt, sender, err)
			}
			if test.wantSuppressed {
				if sender.status.Text != "" {
					t.Fatal("suppressed question reached transport")
				}
				return
			}
			if sender.prepared.OperationID != "question:1" {
				t.Fatal("lost durable deduplication identity")
			}
			if !test.active {
				if sender.status.Text != "Фоновая сессия ждёт ответа." || sender.prepared.Card.MakeActive || sender.prepared.Presentation.SessionID != string(id) {
					t.Fatalf("background overwrote context: %#v", sender.prepared)
				}
				if len(*sender.prepared.Keyboard) != 1 || len((*sender.prepared.Keyboard)[0]) != 1 {
					t.Fatal("question must have only open-session control")
				}
				decoded, err := presenter.DecodeCallback((*sender.prepared.Keyboard)[0][0].CallbackData)
				if err != nil || decoded.SessionID != string(id) {
					t.Fatalf("wrong question target %#v err=%v", decoded, err)
				}
			} else if sender.status.SourceMessageID != 55 {
				t.Fatal("active question created new card")
			}
		})
	}
}

func TestDisabledQuestionSkipsProjection(t *testing.T) {
	controller := &failingCompletionController{}
	deliverer := CompletionDeliverer{Controller: controller, Presenter: &telegrambridge.Presenter{}, Sender: &completionSenderStub{}, Cards: telegramstate.NewMemoryStore(), ConversationID: 42}
	receipt, err := deliverer.Deliver(context.Background(), telegramcontroller.Notification{Kind: telegramcontroller.NotificationQuestion, SessionID: "background"}, "q")
	if err != nil || !receipt.Suppressed || controller.calls != 0 {
		t.Fatalf("receipt=%#v calls=%d err=%v", receipt, controller.calls, err)
	}
}
