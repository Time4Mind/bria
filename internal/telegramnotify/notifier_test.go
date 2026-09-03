package telegramnotify_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"reflect"
	"strings"
	"testing"
	"unicode/utf8"

	"bria/internal/domain"
	"bria/internal/telegram"
	"bria/internal/telegramcontroller"
	"bria/internal/telegramnotify"
)

const (
	testBotToken  = "123456:test-only-token"
	testSessionID = domain.SessionID("11111111-2222-4333-8444-555555555555")
)

type receiptRecorder struct {
	receipts []telegramnotify.OutboundReceipt
	err      error
}

func (recorder *receiptRecorder) RecordOutboundReceipt(
	_ context.Context,
	receipt telegramnotify.OutboundReceipt,
) error {
	recorder.receipts = append(recorder.receipts, receipt)
	return recorder.err
}

func TestNotifierSendsCommentaryQuestionAndFinalAsNewRussianPrefixedMessages(t *testing.T) {
	t.Parallel()

	wantText := []string{
		"Сессия 11111111 - комментарий\nПроверяю файлы.",
		"Сессия 11111111 - вопрос\nКакой вариант выбрать?",
		"Сессия 11111111 - итог\nГотово.",
		"Сессия 11111111 - ошибка\nНе удалось завершить запрос.",
	}
	call := 0
	client := mustNotifyClient(t, func(request *http.Request) (*http.Response, error) {
		if request.URL.Path != "/bot"+testBotToken+"/sendMessage" {
			t.Fatalf("request path = %q, want a new sendMessage", request.URL.Path)
		}
		var body telegram.SendMessageRequest
		decodeNotifyRequest(t, request, &body)
		if body.ChatID != 42 || body.Text != wantText[call] || body.ReplyMarkup != nil {
			t.Fatalf("send %d body = %#v, want chat 42 text %q without keyboard", call, body, wantText[call])
		}
		call++
		return notifyResponse(http.StatusOK, `{"ok":true,"result":{"message_id":`+string(rune('0'+call))+`,"from":{"id":600,"is_bot":true},"chat":{"id":42,"type":"private"}}}`), nil
	})
	notifier, err := telegramnotify.New(client)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	for _, notification := range []telegramcontroller.Notification{
		{ConversationID: 42, SessionID: testSessionID, Kind: telegramcontroller.NotificationCommentary, Text: "Проверяю файлы."},
		{ConversationID: 42, SessionID: testSessionID, Kind: telegramcontroller.NotificationQuestion, Text: "Какой вариант выбрать?"},
		{ConversationID: 42, SessionID: testSessionID, Kind: telegramcontroller.NotificationFinal, Text: "Готово."},
		{ConversationID: 42, SessionID: testSessionID, Kind: telegramcontroller.NotificationError, Text: "Не удалось завершить запрос."},
	} {
		if err := notifier.Notify(context.Background(), notification); err != nil {
			t.Fatalf("Notify(%q) error = %v", notification.Kind, err)
		}
	}
	if call != 4 {
		t.Fatalf("sendMessage calls = %d, want 4", call)
	}
}

func TestNotifierRecordsConfirmedTelegramReceiptForReplyRouting(t *testing.T) {
	t.Parallel()

	recorder := &receiptRecorder{}
	client := mustNotifyClient(t, func(request *http.Request) (*http.Response, error) {
		return notifyResponse(http.StatusOK, `{"ok":true,"result":{"message_id":731,"from":{"id":600,"is_bot":true},"chat":{"id":42,"type":"private"}}}`), nil
	})
	notifier, err := telegramnotify.NewWithOptions(client, telegramnotify.Options{
		ReceiptRecorder: recorder,
	})
	if err != nil {
		t.Fatalf("NewWithOptions() error = %v", err)
	}
	if err := notifier.Notify(context.Background(), telegramcontroller.Notification{
		ConversationID: 42,
		SessionID:      testSessionID,
		Kind:           telegramcontroller.NotificationFinal,
		Text:           "Готово.",
	}); err != nil {
		t.Fatalf("Notify() error = %v", err)
	}

	want := []telegramnotify.OutboundReceipt{{MessageID: 731, SessionID: testSessionID}}
	if !reflect.DeepEqual(recorder.receipts, want) {
		t.Fatalf("recorded receipts = %#v, want %#v", recorder.receipts, want)
	}
}

func TestNotifierDoesNotRecordUnconfirmedTelegramReceipt(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		response *http.Response
		sendErr  error
	}{
		{name: "transport failure", sendErr: errors.New("connection closed")},
		{name: "missing positive receipt", response: notifyResponse(http.StatusOK, `{"ok":true,"result":{"message_id":0,"from":{"id":600,"is_bot":true},"chat":{"id":42,"type":"private"}}}`)},
	}
	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			recorder := &receiptRecorder{}
			client := mustNotifyClient(t, func(request *http.Request) (*http.Response, error) {
				return test.response, test.sendErr
			})
			notifier, err := telegramnotify.NewWithOptions(client, telegramnotify.Options{ReceiptRecorder: recorder})
			if err != nil {
				t.Fatal(err)
			}
			err = notifier.Notify(context.Background(), telegramcontroller.Notification{
				ConversationID: 42,
				SessionID:      testSessionID,
				Kind:           telegramcontroller.NotificationFinal,
				Text:           "Готово.",
			})
			if err == nil {
				t.Fatal("Notify() error = nil, want unconfirmed delivery failure")
			}
			if len(recorder.receipts) != 0 {
				t.Fatalf("recorded receipts = %#v, want none", recorder.receipts)
			}
		})
	}
}

func TestNotifierSurfacesReceiptPersistenceFailureAfterConfirmedSend(t *testing.T) {
	t.Parallel()

	wantErr := errors.New("receipt storage unavailable")
	recorder := &receiptRecorder{err: wantErr}
	client := mustNotifyClient(t, func(request *http.Request) (*http.Response, error) {
		return notifyResponse(http.StatusOK, `{"ok":true,"result":{"message_id":912,"from":{"id":600,"is_bot":true},"chat":{"id":42,"type":"private"}}}`), nil
	})
	notifier, err := telegramnotify.NewWithOptions(client, telegramnotify.Options{ReceiptRecorder: recorder})
	if err != nil {
		t.Fatal(err)
	}
	err = notifier.Notify(context.Background(), telegramcontroller.Notification{
		ConversationID: 42,
		SessionID:      testSessionID,
		Kind:           telegramcontroller.NotificationCommentary,
		Text:           "Промежуточный результат.",
	})
	if !errors.Is(err, wantErr) {
		t.Fatalf("Notify() error = %v, want receipt persistence error", err)
	}
	want := []telegramnotify.OutboundReceipt{{MessageID: 912, SessionID: testSessionID}}
	if !reflect.DeepEqual(recorder.receipts, want) {
		t.Fatalf("attempted receipts = %#v, want %#v", recorder.receipts, want)
	}
}

func TestNotifierSplitsLongUnicodeLosslesslyWithinTelegramLimits(t *testing.T) {
	t.Parallel()

	const prefix = "Сессия 11111111 - итог\n"
	content := strings.Repeat("я🙂", 1200)
	var sent []string
	client := mustNotifyClient(t, func(request *http.Request) (*http.Response, error) {
		var body telegram.SendMessageRequest
		decodeNotifyRequest(t, request, &body)
		sent = append(sent, body.Text)
		messageID := len(sent)
		return notifyResponse(http.StatusOK, `{"ok":true,"result":{"message_id":`+string(rune('0'+messageID))+`,"from":{"id":600,"is_bot":true},"chat":{"id":42,"type":"private"}}}`), nil
	})
	notifier, err := telegramnotify.New(client)
	if err != nil {
		t.Fatal(err)
	}
	if err := notifier.Notify(context.Background(), telegramcontroller.Notification{
		ConversationID: 42,
		SessionID:      testSessionID,
		Kind:           telegramcontroller.NotificationFinal,
		Text:           content,
	}); err != nil {
		t.Fatalf("Notify() error = %v", err)
	}
	if len(sent) < 2 {
		t.Fatalf("send count = %d, want multiple Unicode pages", len(sent))
	}
	var joined strings.Builder
	for index, text := range sent {
		if len(text) > 4096 || utf8.RuneCountInString(text) > 4096 {
			t.Errorf("page %d is over Telegram limit: bytes=%d runes=%d", index, len(text), utf8.RuneCountInString(text))
		}
		if !strings.HasPrefix(text, prefix) {
			t.Fatalf("page %d prefix = %q, want %q", index, text[:min(len(text), len(prefix))], prefix)
		}
		joined.WriteString(strings.TrimPrefix(text, prefix))
	}
	if got := joined.String(); got != content {
		t.Fatalf("joined content changed: bytes=%d, want %d", len(got), len(content))
	}
}

func TestNotifierStopsAfterFirstAmbiguousPageFailureWithoutRetryOrLeakage(t *testing.T) {
	t.Parallel()

	const providerSessionSecret = "provider-session-secret"
	calls := 0
	client := mustNotifyClient(t, func(request *http.Request) (*http.Response, error) {
		calls++
		if calls == 2 {
			return nil, errors.New(testBotToken + " " + providerSessionSecret)
		}
		return notifyResponse(http.StatusOK, `{"ok":true,"result":{"message_id":1,"from":{"id":600,"is_bot":true},"chat":{"id":42,"type":"private"}}}`), nil
	})
	notifier, err := telegramnotify.New(client)
	if err != nil {
		t.Fatal(err)
	}
	err = notifier.Notify(context.Background(), telegramcontroller.Notification{
		ConversationID: 42,
		SessionID:      testSessionID,
		Kind:           telegramcontroller.NotificationCommentary,
		Text:           strings.Repeat("a", 9000),
	})
	if err == nil {
		t.Fatal("Notify() error = nil, want ambiguous transport failure")
	}
	if calls != 2 {
		t.Fatalf("send calls = %d, want first success then one failed attempt without retry", calls)
	}
	for _, forbidden := range []string{testBotToken, providerSessionSecret, string(testSessionID), strings.Repeat("a", 100)} {
		if strings.Contains(err.Error(), forbidden) {
			t.Fatalf("Notify() error exposed forbidden value")
		}
	}
}

func TestNotifierRejectsInvalidNotificationBeforeHTTP(t *testing.T) {
	t.Parallel()

	calls := 0
	client := mustNotifyClient(t, func(*http.Request) (*http.Response, error) {
		calls++
		return notifyResponse(http.StatusOK, `{"ok":true,"result":{}}`), nil
	})
	notifier, err := telegramnotify.New(client)
	if err != nil {
		t.Fatal(err)
	}
	valid := telegramcontroller.Notification{
		ConversationID: 42,
		SessionID:      testSessionID,
		Kind:           telegramcontroller.NotificationFinal,
		Text:           "ok",
	}
	tests := []telegramcontroller.Notification{
		withConversation(valid, 0),
		withSession(valid, "provider-session-id"),
		withKind(valid, "future"),
		withText(valid, ""),
		withText(valid, " \n\t"),
		withText(valid, string([]byte{0xff})),
	}
	for _, notification := range tests {
		if err := notifier.Notify(context.Background(), notification); err == nil {
			t.Errorf("Notify(%#v) error = nil", notification)
		}
	}
	if calls != 0 {
		t.Fatalf("invalid notifications made %d HTTP calls, want 0", calls)
	}
	if _, err := telegramnotify.New(nil); err == nil {
		t.Fatal("New(nil) error = nil")
	}
}

type notifyHTTPClientFunc func(*http.Request) (*http.Response, error)

func (function notifyHTTPClientFunc) Do(request *http.Request) (*http.Response, error) {
	return function(request)
}

func mustNotifyClient(t *testing.T, do notifyHTTPClientFunc) *telegram.Client {
	t.Helper()
	client, err := telegram.NewClient(testBotToken, do, telegram.Options{})
	if err != nil {
		t.Fatalf("telegram.NewClient() error = %v", err)
	}
	return client
}

func decodeNotifyRequest(t *testing.T, request *http.Request, result any) {
	t.Helper()
	decoder := json.NewDecoder(request.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(result); err != nil {
		t.Fatalf("decode request: %v", err)
	}
}

func notifyResponse(status int, body string) *http.Response {
	return &http.Response{
		StatusCode: status,
		Body:       io.NopCloser(strings.NewReader(body)),
		Header:     make(http.Header),
	}
}

func withConversation(notification telegramcontroller.Notification, id int64) telegramcontroller.Notification {
	notification.ConversationID = id
	return notification
}

func withSession(notification telegramcontroller.Notification, id domain.SessionID) telegramcontroller.Notification {
	notification.SessionID = id
	return notification
}

func withKind(notification telegramcontroller.Notification, kind telegramcontroller.NotificationKind) telegramcontroller.Notification {
	notification.Kind = kind
	return notification
}

func withText(notification telegramcontroller.Notification, text string) telegramcontroller.Notification {
	notification.Text = text
	return notification
}
