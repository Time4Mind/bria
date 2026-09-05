package telegramcompletioncomposition

import (
	"bytes"
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"bria/internal/callbacktoken"
	"bria/internal/coordinator"
	"bria/internal/domain"
	"bria/internal/settings"
	"bria/internal/settingscomposition"
	"bria/internal/telegrambridge"
	"bria/internal/telegramcontroller"
	"bria/internal/telegramflow"
	"bria/internal/telegramstate"
)

type completionControllerStub struct {
	card   telegramcontroller.SemanticCard
	active bool
}

type failingCompletionController struct{ calls int }

func (stub *failingCompletionController) ProjectCompletion(context.Context, domain.SessionID) (telegramcontroller.SemanticCard, bool, error) {
	stub.calls++
	return telegramcontroller.SemanticCard{}, false, errors.New("projection must stay lazy")
}

func (stub completionControllerStub) ProjectCompletion(context.Context, domain.SessionID) (telegramcontroller.SemanticCard, bool, error) {
	return stub.card, stub.active, nil
}

type completionSenderStub struct {
	prepared telegramflow.Prepared
	status   coordinator.Status
	edited   bool
}

func (stub *completionSenderStub) EditStatusWithKeyboard(_ context.Context, _ string, status coordinator.Status, _ *coordinator.KeyboardMarkup) (coordinator.Receipt, error) {
	stub.status = status
	stub.edited = true
	return coordinator.Receipt{MessageID: status.SourceMessageID}, nil
}

func (stub *completionSenderStub) Register(prepared telegramflow.Prepared) error {
	stub.prepared = prepared
	return nil
}

func TestCompletionDelivererEditsActiveCommentaryAndSuppressesBackgroundCommentary(t *testing.T) {
	now := time.Date(2026, 9, 4, 12, 0, 0, 0, time.UTC)
	codec, err := callbacktoken.New(bytes.Repeat([]byte{8}, 32), bytes.NewReader(make([]byte, 4096)), func() time.Time { return now })
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
		Pages:                []telegramcontroller.SemanticContentPage{{Content: "PROMPT\nCOMMENT ONE\nTOOL TWO", Anchors: []string{"latest"}}},
		View:                 telegramcontroller.SemanticPageView{Page: 1, Pages: 1, Anchor: "latest", FollowLatest: true},
		SelectableSessionIDs: []domain.SessionID{sessionID}, SessionRowSizes: []int{1}, Working: true,
	}
	store := telegramstate.NewMemoryStore()
	if err := store.Update(context.Background(), func(state *telegramstate.State) error {
		state.ActiveSession = sessionID
		return state.SetCard(telegramstate.Card{SessionID: sessionID, Carrier: telegramstate.Carrier{ChatID: 42, MessageID: 55}, Page: telegramstate.Page{Current: 1, Total: 1, FollowLatest: true}})
	}); err != nil {
		t.Fatal(err)
	}

	activeSender := &completionSenderStub{}
	active := CompletionDeliverer{Controller: completionControllerStub{card: card, active: true}, Presenter: presenter, Sender: activeSender, Cards: store, ConversationID: 42}
	receipt, err := active.Deliver(context.Background(), telegramcontroller.Notification{OperationID: "event:2", ConversationID: 42, SessionID: sessionID, Kind: telegramcontroller.NotificationCommentary, Text: "TOOL TWO"}, "event:2")
	if err != nil || receipt.State != "confirmed" || !activeSender.edited || activeSender.status.SourceMessageID != 55 || !strings.Contains(activeSender.status.Text, "COMMENT ONE\nTOOL TWO") {
		t.Fatalf("active commentary = (%#v, %#v, %v)", receipt, activeSender, err)
	}

	backgroundSender := &completionSenderStub{}
	background := CompletionDeliverer{Controller: completionControllerStub{card: card, active: false}, Presenter: presenter, Sender: backgroundSender, Cards: store, ConversationID: 42}
	receipt, err = background.Deliver(context.Background(), telegramcontroller.Notification{OperationID: "event:3", ConversationID: 42, SessionID: sessionID, Kind: telegramcontroller.NotificationCommentary, Text: "hidden"}, "event:3")
	if err != nil || receipt.State != "confirmed" || !receipt.Suppressed || len(receipt.Parts) != 0 || backgroundSender.status.Text != "" {
		t.Fatalf("background commentary = (%#v, %#v, %v)", receipt, backgroundSender, err)
	}
}

func TestCompletionDelivererSuppressesDisabledBackgroundError(t *testing.T) {
	preferenceStore, err := settings.OpenFileStore(filepath.Join(t.TempDir(), "settings.json"))
	if err != nil {
		t.Fatal(err)
	}
	preferences := settingscomposition.Preferences{Store: preferenceStore}
	if err := preferences.ToggleBackgroundErrors(context.Background()); err != nil {
		t.Fatal(err)
	}
	sender := &completionSenderStub{}
	sessionID := domain.SessionID("00000000-0000-4000-8000-000000000001")
	deliverer := CompletionDeliverer{
		Controller: completionControllerStub{active: false, card: telegramcontroller.SemanticCard{SessionID: sessionID}},
		Presenter:  &telegrambridge.Presenter{}, Sender: sender, Preferences: preferences, ConversationID: 42,
	}
	receipt, err := deliverer.Deliver(context.Background(), telegramcontroller.Notification{
		OperationID: "error:1", ConversationID: 42, SessionID: sessionID,
		Kind: telegramcontroller.NotificationError, Text: "hidden",
	}, "error:1")
	if err != nil || !receipt.Suppressed || receipt.State != "confirmed" || sender.status.Text != "" {
		t.Fatalf("background error = (%#v, %#v, %v)", receipt, sender, err)
	}
}

func TestCompletionDelivererDoesNotProjectBackgroundCommentary(t *testing.T) {
	activeID := domain.SessionID("00000000-0000-4000-8000-000000000001")
	backgroundID := domain.SessionID("00000000-0000-4000-8000-000000000002")
	store := telegramstate.NewMemoryStore()
	if err := store.Update(context.Background(), func(state *telegramstate.State) error {
		if err := state.SetCard(telegramstate.Card{SessionID: activeID, Page: telegramstate.Page{Current: 1, Total: 1, FollowLatest: true}}); err != nil {
			return err
		}
		if err := state.SetCard(telegramstate.Card{SessionID: backgroundID, Page: telegramstate.Page{Current: 1, Total: 1, FollowLatest: true}}); err != nil {
			return err
		}
		state.ActiveSession = activeID
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	controller := &failingCompletionController{}
	deliverer := CompletionDeliverer{
		Controller: controller, Presenter: &telegrambridge.Presenter{}, Sender: &completionSenderStub{},
		Cards: store, ConversationID: 42,
	}
	receipt, err := deliverer.Deliver(context.Background(), telegramcontroller.Notification{
		OperationID: "commentary:background", ConversationID: 42, SessionID: backgroundID,
		Kind: telegramcontroller.NotificationCommentary, Text: "hidden",
	}, "commentary:background")
	if err != nil || !receipt.Suppressed || receipt.State != "confirmed" || controller.calls != 0 {
		t.Fatalf("lazy background commentary = (%#v, %v), projection calls=%d", receipt, err, controller.calls)
	}
}

func (stub *completionSenderStub) SendStatusWithKeyboard(_ context.Context, _ string, status coordinator.Status, _ *coordinator.KeyboardMarkup) (coordinator.Receipt, error) {
	stub.status = status
	return coordinator.Receipt{MessageID: 77}, nil
}

func TestCompletionDelivererSeparatesActiveCardAndBackgroundNotice(t *testing.T) {
	now := time.Date(2026, 9, 4, 12, 0, 0, 0, time.UTC)
	codec, err := callbacktoken.New(bytes.Repeat([]byte{7}, 32), bytes.NewReader(make([]byte, 4096)), func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	presenter, err := telegrambridge.NewPresenter(codec, func() time.Time { return now }, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	card := telegramcontroller.SemanticCard{
		SessionID: "00000000-0000-4000-8000-000000000001", Effect: telegramcontroller.SemanticEditSameCarrier,
		Header: "⚠️ Архивировать сессию test?", Pages: []telegramcontroller.SemanticContentPage{{Content: "", Anchors: []string{"close-confirmation"}}},
		View:              telegramcontroller.SemanticPageView{Page: 1, Pages: 1, Anchor: "close-confirmation", FollowLatest: true},
		CloseConfirmation: true,
	}
	for _, test := range []struct {
		name   string
		active bool
	}{
		{name: "active", active: true},
		{name: "background", active: false},
	} {
		t.Run(test.name, func(t *testing.T) {
			sender := &completionSenderStub{}
			deliverer := CompletionDeliverer{Controller: completionControllerStub{card: card, active: test.active}, Presenter: presenter, Sender: sender, ConversationID: 42}
			receipt, err := deliverer.Deliver(context.Background(), telegramcontroller.Notification{
				OperationID: "completion:" + test.name, ConversationID: 42, SessionID: card.SessionID,
				Kind: telegramcontroller.NotificationFinal, Text: "FULL FINAL",
			}, "completion:"+test.name)
			if err != nil || receipt.State != "confirmed" || receipt.Parts[0].MessageID != 77 {
				t.Fatalf("Deliver() = (%#v, %v)", receipt, err)
			}
			if test.active {
				if strings.Contains(sender.status.Text, "FULL FINAL") || !strings.Contains(sender.status.Text, "Архивировать сессию") || !sender.prepared.Card.MakeActive {
					t.Fatalf("active completion = %#v / %#v", sender.status, sender.prepared.Card)
				}
				keyboard := sender.prepared.Card.Projection.Card.Keyboard
				if len(keyboard.Rows) != 1 || keyboard.Rows[0][0].Label != "Архивировать" || keyboard.Rows[0][1].Label != "Отмена" {
					t.Fatalf("active completion keyboard is not compact: %#v", keyboard)
				}
			} else if strings.Contains(sender.status.Text, "FULL FINAL") || sender.status.Text != "Фоновая сессия завершена." || sender.prepared.Card.MakeActive {
				t.Fatalf("background completion leaked final = %#v / %#v", sender.status, sender.prepared.Card)
			}
		})
	}
}
