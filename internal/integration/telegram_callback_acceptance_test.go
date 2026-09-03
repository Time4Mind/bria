package integration_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"bria/internal/app"
	"bria/internal/coordinator"
	"bria/internal/domain"
	"bria/internal/sessionruntime"
	"bria/internal/telegram"
	"bria/internal/telegrambridge"
	"bria/internal/telegramcontroller"
)

// TestTelegramCallbackEditIsOneInPlaceOperation exercises the transport seam
// used by the coordinator for a live card. Telegram callback acknowledgement
// is deliberately counted: acknowledging twice makes the real Bot API return
// an error and used to turn an otherwise successful click into a crash-loop.
func TestTelegramCallbackEditIsOneInPlaceOperation(t *testing.T) {
	httpClient := &callbackHTTPClient{}
	client, err := telegram.NewClient("123:test-token", httpClient, telegram.Options{})
	if err != nil {
		t.Fatal(err)
	}
	sender, err := telegrambridge.NewSender(client)
	if err != nil {
		t.Fatal(err)
	}
	keyboard := coordinator.KeyboardMarkup{{
		{Text: "‹", CallbackData: "pg:prev"},
		{Text: "1/1", CallbackData: "pg:jump"},
		{Text: "›", CallbackData: "pg:next"},
	}, {
		{Text: "Стоп", CallbackData: "ft:stop"},
		{Text: "≡ Меню", CallbackData: "ft:more"},
	}}

	_, err = sender.SendStatusWithKeyboard(context.Background(), "", coordinator.Status{
		ConversationID: 42, Text: "card", CallbackQueryID: "q1", SourceMessageID: 77,
	}, &keyboard)
	if err != nil {
		t.Fatalf("send card: %v", err)
	}
	if httpClient.answerCalls != 1 {
		t.Fatalf("callback answers after send = %d, want 1", httpClient.answerCalls)
	}
	if httpClient.sendCalls != 1 || httpClient.lastSend.ChatID != 42 {
		t.Fatalf("send calls/body = %d/%#v, want one message to chat 42", httpClient.sendCalls, httpClient.lastSend)
	}
	if got := httpClient.lastSend.ReplyMarkup.InlineKeyboard[0][2].CallbackData; got != "pg:next" {
		t.Fatalf("next callback = %q, want pg:next", got)
	}

	_, err = sender.EditStatusWithKeyboard(context.Background(), "", coordinator.Status{
		ConversationID: 42, Text: "card page 2", CallbackQueryID: "q2", SourceMessageID: 77,
	}, &keyboard)
	if err != nil {
		t.Fatalf("edit card: %v", err)
	}
	if httpClient.answerCalls != 2 {
		t.Fatalf("callback answers after edit = %d, want one per callback", httpClient.answerCalls)
	}
	if httpClient.editCalls != 1 || httpClient.lastEdit.MessageID != 77 || httpClient.lastEdit.Text != "card page 2" {
		t.Fatalf("edit calls/body = %d/%#v, want one in-place edit of message 77", httpClient.editCalls, httpClient.lastEdit)
	}
}

// TestControllerLiveCardFlowCoversCreateSubmitAndCallback verifies the
// public user-flow boundary before Telegram transport: create a real logical
// session, submit text to its provider worker, then navigate the same card by
// a CCBot-compatible callback. It intentionally uses no command-only shortcut.
func TestControllerLiveCardFlowCoversCreateSubmitAndCallback(t *testing.T) {
	const (
		owner = int64(42)
		chat  = int64(42)
	)
	workdir := t.TempDir()
	ready, err := domain.NewStartingSession("11111111-1111-4111-9111-111111111111", "intent", "local", domain.ProviderClaude, workdir)
	if err != nil {
		t.Fatal(err)
	}
	ready, err = ready.Ready(domain.ProviderBinding{Provider: domain.ProviderClaude, SessionID: "provider-1", Generation: 1})
	if err != nil {
		t.Fatal(err)
	}
	store := &flowSessions{session: ready}
	final := make(chan telegramcontroller.Notification, 2)
	controller, err := telegramcontroller.New(owner, chat, "local", flowCreator{session: ready}, store,
		flowSubmitter{}, flowNotifier{ch: final}, telegramcontroller.Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer controller.Close(context.Background())

	created, err := controller.Handle(context.Background(), coordinator.Update{
		ID: 1, Kind: coordinator.UpdateMessage, ActorID: owner, ConversationID: chat,
		ConversationKind: "private", Text: "/new claude " + workdir,
	})
	if err != nil || created.Kind != coordinator.DecisionStatus || created.Keyboard == nil {
		t.Fatalf("create decision = %#v, err=%v", created, err)
	}
	if !strings.Contains(created.Status.Text, "готова") {
		t.Fatalf("create card = %q, want ready state", created.Status.Text)
	}

	accepted, err := controller.Handle(context.Background(), coordinator.Update{
		ID: 2, Kind: coordinator.UpdateMessage, ActorID: owner, ConversationID: chat,
		ConversationKind: "private", Text: "Ответь OK",
	})
	if err != nil || accepted.Kind != coordinator.DecisionStatus || !strings.Contains(accepted.Status.Text, "принят") {
		t.Fatalf("submit decision = %#v, err=%v", accepted, err)
	}
	select {
	case notification := <-final:
		if notification.Kind != telegramcontroller.NotificationFinal || notification.Text != "OK" {
			t.Fatalf("provider notification = %#v, want final OK", notification)
		}
	case <-time.After(time.Second):
		t.Fatal("provider final notification was not emitted")
	}

	navigated, err := controller.Handle(context.Background(), coordinator.Update{
		ID: 3, Kind: coordinator.UpdateCallback, ActorID: owner, ConversationID: chat,
		ConversationKind: "private", CallbackQueryID: "callback-1", SourceMessageID: 77, Text: "pg:next",
	})
	if err != nil || navigated.Kind != coordinator.DecisionStatus {
		t.Fatalf("pagination decision = %#v, err=%v", navigated, err)
	}
	if navigated.Status.CallbackQueryID != "callback-1" || navigated.Status.SourceMessageID != 77 {
		t.Fatalf("pagination receipt = %#v, want callback callback-1 on carrier 77", navigated.Status)
	}
	if !hasCallback(*navigated.Keyboard, "ft:more") || !hasCallback(*navigated.Keyboard, "sw:"+string(ready.ID())) {
		t.Fatalf("live card keyboard = %#v, want menu and session switcher", navigated.Keyboard)
	}
}

func hasCallback(markup coordinator.KeyboardMarkup, want string) bool {
	for _, row := range markup {
		for _, button := range row {
			if button.CallbackData == want {
				return true
			}
		}
	}
	return false
}

type flowCreator struct{ session domain.Session }

func (c flowCreator) Create(_ context.Context, intent app.ConfirmedSessionIntent) (app.CreateSessionResult, error) {
	snapshot := c.session.Snapshot()
	snapshot.IntentID = intent.IntentID
	session, err := domain.RestoreSession(snapshot)
	if err != nil {
		return app.CreateSessionResult{}, err
	}
	return app.CreateSessionResult{Session: session}, nil
}

type flowSubmitter struct{}

func (flowSubmitter) Submit(context.Context, domain.SessionID, string) (sessionruntime.TurnResult, error) {
	return sessionruntime.TurnResult{Final: "OK", TerminalStatus: sessionruntime.StatusCompleted}, nil
}

type flowNotifier struct {
	ch chan<- telegramcontroller.Notification
}

func (n flowNotifier) Notify(_ context.Context, notification telegramcontroller.Notification) error {
	n.ch <- notification
	return nil
}

type flowSessions struct {
	mu      sync.Mutex
	session domain.Session
}

func (s *flowSessions) List(context.Context) ([]domain.Session, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return []domain.Session{s.session}, nil
}

func (s *flowSessions) Load(context.Context, domain.SessionID) (domain.Session, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.session, nil
}

type callbackHTTPClient struct {
	answerCalls int
	sendCalls   int
	editCalls   int
	lastSend    telegram.SendMessageRequest
	lastEdit    telegram.EditMessageTextRequest
}

func (c *callbackHTTPClient) Do(request *http.Request) (*http.Response, error) {
	defer request.Body.Close()
	switch {
	case strings.HasSuffix(request.URL.Path, "/answerCallbackQuery"):
		c.answerCalls++
	case strings.HasSuffix(request.URL.Path, "/sendMessage"):
		c.sendCalls++
		if err := json.NewDecoder(request.Body).Decode(&c.lastSend); err != nil {
			return nil, err
		}
	case strings.HasSuffix(request.URL.Path, "/editMessageText"):
		c.editCalls++
		if err := json.NewDecoder(request.Body).Decode(&c.lastEdit); err != nil {
			return nil, err
		}
	default:
		return nil, io.ErrUnexpectedEOF
	}
	body := `{"ok":true,"result":{"message_id":77,"from":{"id":9,"is_bot":true,"first_name":"Bria"},"chat":{"id":42,"type":"private"},"text":"ok"}}`
	if strings.HasSuffix(request.URL.Path, "/answerCallbackQuery") {
		body = `{"ok":true,"result":true}`
	}
	return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body))}, nil
}
