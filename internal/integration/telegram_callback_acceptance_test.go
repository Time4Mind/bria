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
	releaseAcknowledgements := make(chan struct{})
	var releaseOnce sync.Once
	release := func() { releaseOnce.Do(func() { close(releaseAcknowledgements) }) }
	httpClient := &callbackHTTPClient{
		acknowledgementStarted:  make(chan string, 2),
		releaseAcknowledgements: releaseAcknowledgements,
	}
	client, err := telegram.NewClient("123:test-token", httpClient, telegram.Options{})
	if err != nil {
		t.Fatal(err)
	}
	sender, err := telegrambridge.NewSender(client)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		release()
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		if err := sender.Close(ctx); err != nil {
			t.Errorf("join callback acknowledgements: %v", err)
		}
	})
	keyboard := coordinator.KeyboardMarkup{{
		{Text: "‹", CallbackData: "pg:prev"},
		{Text: "1/1", CallbackData: "pg:jump"},
		{Text: "›", CallbackData: "pg:next"},
	}, {
		{Text: "Стоп", CallbackData: "ft:stop"},
		{Text: "≡ Меню", CallbackData: "ft:more"},
	}}

	operationContext, cancelOperation := context.WithTimeout(context.Background(), time.Second)
	defer cancelOperation()
	_, err = sender.SendStatusWithKeyboard(operationContext, "", coordinator.Status{
		ConversationID: 42, Text: "card", CallbackQueryID: "q1", SourceMessageID: 77,
	}, &keyboard)
	if err != nil {
		t.Fatalf("send card: %v", err)
	}
	// Both visible operations must return while the HTTP acknowledgements are
	// still blocked. Callback completion must not gate a card transition.
	httpClient.waitForAcknowledgement(t, "q1")
	afterSend := httpClient.snapshot()
	if afterSend.answerCalls != 1 {
		t.Fatalf("callback answers after send = %d, want 1", afterSend.answerCalls)
	}
	if afterSend.sendCalls != 1 || afterSend.lastSend.ChatID != 42 {
		t.Fatalf("send calls/body = %d/%#v, want one message to chat 42", afterSend.sendCalls, afterSend.lastSend)
	}
	if got := afterSend.lastSend.ReplyMarkup.InlineKeyboard[0][2].CallbackData; got != "pg:next" {
		t.Fatalf("next callback = %q, want pg:next", got)
	}

	_, err = sender.EditStatusWithKeyboard(operationContext, "", coordinator.Status{
		ConversationID: 42, Text: "card page 2", CallbackQueryID: "q2", SourceMessageID: 77,
	}, &keyboard)
	if err != nil {
		t.Fatalf("edit card: %v", err)
	}
	httpClient.waitForAcknowledgement(t, "q2")
	release()
	completionContext, cancelCompletion := context.WithTimeout(context.Background(), time.Second)
	defer cancelCompletion()
	if err := sender.Close(completionContext); err != nil {
		t.Fatalf("complete callback acknowledgements: %v", err)
	}
	afterEdit := httpClient.snapshot()
	if afterEdit.answerCalls != 2 {
		t.Fatalf("callback answers after edit = %d, want one per callback", afterEdit.answerCalls)
	}
	if afterEdit.sendCalls != 1 || afterEdit.editCalls != 1 || afterEdit.lastEdit.MessageID != 77 || afterEdit.lastEdit.RichMessage == nil || afterEdit.lastEdit.RichMessage.Markdown != "card page 2" {
		t.Fatalf("send/edit calls/body = %d/%d/%#v, want one in-place edit of message 77", afterEdit.sendCalls, afterEdit.editCalls, afterEdit.lastEdit)
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
	if err != nil || accepted.Kind != coordinator.DecisionSkip {
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
	mu sync.Mutex
	callbackHTTPObservation
	acknowledgementStarted  chan string
	releaseAcknowledgements <-chan struct{}
}

type callbackHTTPObservation struct {
	answerCalls int
	sendCalls   int
	editCalls   int
	lastSend    telegram.SendRichMessageRequest
	lastEdit    telegram.EditMessageTextRequest
}

func (c *callbackHTTPClient) Do(request *http.Request) (*http.Response, error) {
	defer request.Body.Close()
	callbackID, err := c.record(request)
	if err != nil {
		return nil, err
	}
	body := `{"ok":true,"result":{"message_id":77,"from":{"id":9,"is_bot":true,"first_name":"Bria"},"chat":{"id":42,"type":"private"},"text":"ok"}}`
	if callbackID != "" {
		select {
		case c.acknowledgementStarted <- callbackID:
		case <-request.Context().Done():
			return nil, request.Context().Err()
		}
		select {
		case <-c.releaseAcknowledgements:
		case <-request.Context().Done():
			return nil, request.Context().Err()
		}
		body = `{"ok":true,"result":true}`
	}
	return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body))}, nil
}

func (c *callbackHTTPClient) record(request *http.Request) (string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	switch {
	case strings.HasSuffix(request.URL.Path, "/answerCallbackQuery"):
		var answer telegram.AnswerCallbackQueryRequest
		if err := json.NewDecoder(request.Body).Decode(&answer); err != nil {
			return "", err
		}
		c.answerCalls++
		return string(answer.CallbackQueryID), nil
	case strings.HasSuffix(request.URL.Path, "/sendRichMessage"):
		c.sendCalls++
		if err := json.NewDecoder(request.Body).Decode(&c.lastSend); err != nil {
			return "", err
		}
	case strings.HasSuffix(request.URL.Path, "/editMessageText"):
		c.editCalls++
		if err := json.NewDecoder(request.Body).Decode(&c.lastEdit); err != nil {
			return "", err
		}
	default:
		return "", io.ErrUnexpectedEOF
	}
	return "", nil
}

func (c *callbackHTTPClient) snapshot() callbackHTTPObservation {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.callbackHTTPObservation
}

func (c *callbackHTTPClient) waitForAcknowledgement(t *testing.T, want string) {
	t.Helper()
	select {
	case got := <-c.acknowledgementStarted:
		if got != want {
			t.Fatalf("callback acknowledgement = %q, want %q", got, want)
		}
	case <-time.After(time.Second):
		t.Fatalf("callback acknowledgement %q did not start", want)
	}
}
