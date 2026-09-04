package telegrampromptcomposition

import (
	"bytes"
	"context"
	"strings"
	"testing"
	"time"

	"bria/internal/callbacktoken"
	"bria/internal/coordinator"
	"bria/internal/domain"
	"bria/internal/telegrambridge"
	"bria/internal/telegramcontroller"
	"bria/internal/telegramflow"
	"bria/internal/telegramstate"
)

type controllerStub struct {
	card telegramcontroller.SemanticCard
}

func (stub controllerStub) ProjectCurrent(context.Context, domain.SessionID) (telegramcontroller.SemanticActionResult, error) {
	return telegramcontroller.SemanticActionResult{Card: &stub.card}, nil
}

type senderStub struct {
	prepared telegramflow.Prepared
	status   coordinator.Status
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
}
