package telegrambot

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Time4Mind/bria/internal/i18n"
	"github.com/Time4Mind/bria/internal/telegramui"
)

const testToken = "123:test_token"

func TestClientGetUpdatesUsesBoundedLongPollPayload(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/bot"+testToken+"/getUpdates" {
			t.Errorf("unexpected path %q", request.URL.Path)
		}
		var payload getUpdatesPayload
		if err := json.NewDecoder(request.Body).Decode(&payload); err != nil {
			t.Fatalf("decode payload: %v", err)
		}
		if payload.Offset != 17 || payload.Limit != 25 || payload.Timeout != 1 {
			t.Errorf("unexpected getUpdates payload: %#v", payload)
		}
		if len(payload.AllowedUpdates) != 2 {
			t.Errorf("allowed_updates = %#v", payload.AllowedUpdates)
		}
		writeJSON(t, writer, map[string]any{
			"ok": true,
			"result": []any{map[string]any{
				"update_id": 17,
				"message": map[string]any{
					"message_id": 8, "from": map[string]any{"id": 42},
					"chat": map[string]any{"id": 42, "type": "private"}, "text": "hello",
				},
			}},
		})
	}))
	defer server.Close()
	client := newFakeServerClient(t, server, time.Second)
	updates, err := client.GetUpdates(context.Background(), GetUpdatesRequest{
		Offset: 17, Limit: 25, Timeout: 1,
	})
	if err != nil {
		t.Fatalf("GetUpdates: %v", err)
	}
	if len(updates) != 1 || updates[0].Message == nil || updates[0].Message.Text != "hello" {
		t.Fatalf("unexpected updates: %#v", updates)
	}
}

func TestClientConvertsScreenForSendAndEdit(t *testing.T) {
	var mu sync.Mutex
	methods := make([]string, 0, 4)
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		method := request.URL.Path[strings.LastIndex(request.URL.Path, "/")+1:]
		mu.Lock()
		methods = append(methods, method)
		mu.Unlock()
		switch method {
		case "sendMessage":
			var payload sendMessagePayload
			if err := json.NewDecoder(request.Body).Decode(&payload); err != nil {
				t.Fatalf("decode send: %v", err)
			}
			if payload.ChatID != 42 || len(payload.ReplyMarkup.InlineKeyboard) != 3 ||
				payload.ReplyMarkup.InlineKeyboard[0][0].CallbackData != "sessions" {
				t.Errorf("unexpected send payload: %#v", payload)
			}
			writeJSON(t, writer, map[string]any{
				"ok": true, "result": map[string]any{"message_id": 9, "chat": map[string]any{"id": 42}},
			})
		case "editMessageText":
			var payload editMessagePayload
			if err := json.NewDecoder(request.Body).Decode(&payload); err != nil {
				t.Fatalf("decode edit: %v", err)
			}
			if payload.ChatID != 42 || payload.MessageID != 9 {
				t.Errorf("unexpected edit payload: %#v", payload)
			}
			writeJSON(t, writer, map[string]any{
				"ok": true, "result": map[string]any{"message_id": 9, "chat": map[string]any{"id": 42}},
			})
		case "answerCallbackQuery":
			var payload answerCallbackPayload
			if err := json.NewDecoder(request.Body).Decode(&payload); err != nil {
				t.Fatalf("decode callback answer: %v", err)
			}
			if payload.CallbackQueryID != "callback-1" || payload.Text != "Done" {
				t.Errorf("unexpected callback payload: %#v", payload)
			}
			writeJSON(t, writer, map[string]any{"ok": true, "result": true})
		case "sendChatAction":
			var payload sendChatActionPayload
			if err := json.NewDecoder(request.Body).Decode(&payload); err != nil {
				t.Fatalf("decode chat action: %v", err)
			}
			if payload.ChatID != 42 || payload.Action != "typing" {
				t.Errorf("unexpected chat action payload: %#v", payload)
			}
			writeJSON(t, writer, map[string]any{"ok": true, "result": true})
		case "deleteMessage":
			var payload deleteMessagePayload
			if err := json.NewDecoder(request.Body).Decode(&payload); err != nil {
				t.Fatalf("decode delete: %v", err)
			}
			if payload.ChatID != 42 || payload.MessageID != 9 {
				t.Errorf("unexpected delete payload: %#v", payload)
			}
			writeJSON(t, writer, map[string]any{"ok": true, "result": true})
		default:
			http.NotFound(writer, request)
		}
	}))
	defer server.Close()
	client := newFakeServerClient(t, server, time.Second)
	screen := telegramui.RenderMainMenu(i18n.For("en"), "backend")
	message, err := client.SendScreen(context.Background(), 42, screen)
	if err != nil {
		t.Fatalf("SendScreen: %v", err)
	}
	if _, err := client.EditScreen(context.Background(), message, screen); err != nil {
		t.Fatalf("EditScreen: %v", err)
	}
	if err := client.AnswerCallbackQuery(context.Background(), "callback-1", "Done"); err != nil {
		t.Fatalf("AnswerCallbackQuery: %v", err)
	}
	if err := client.SendTyping(context.Background(), 42); err != nil {
		t.Fatalf("SendTyping: %v", err)
	}
	if err := client.DeleteMessage(context.Background(), message); err != nil {
		t.Fatalf("DeleteMessage: %v", err)
	}
	mu.Lock()
	defer mu.Unlock()
	if got := strings.Join(methods, ","); got != "sendMessage,editMessageText,answerCallbackQuery,sendChatAction,deleteMessage" {
		t.Fatalf("methods = %q", got)
	}
}

func TestClientTimeoutAndErrorsNeverExposeToken(t *testing.T) {
	releaseHandler := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		<-releaseHandler
	}))
	defer server.Close()
	defer close(releaseHandler)
	client := newFakeServerClient(t, server, 20*time.Millisecond)
	_, err := client.SendMessage(context.Background(), MessageRequest{ChatID: 42, Text: "hello"})
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("SendMessage error = %v, want deadline exceeded", err)
	}
	if strings.Contains(err.Error(), testToken) {
		t.Fatal("transport error exposed the bot token")
	}
}

func TestClientRejectsLimitsBeforeHTTP(t *testing.T) {
	client, err := NewClient(ClientConfig{Token: testToken})
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	if _, err := client.GetUpdates(context.Background(), GetUpdatesRequest{Limit: 101}); err == nil {
		t.Fatal("GetUpdates accepted an oversized limit")
	}
	if _, err := client.SendMessage(context.Background(), MessageRequest{
		ChatID: 42, Text: strings.Repeat("x", MaxMessageTextBytes+1),
	}); err == nil {
		t.Fatal("SendMessage accepted oversized text")
	}
	if _, err := client.SendMessage(context.Background(), MessageRequest{
		ChatID: 42, Text: "hello", ParseMode: telegramui.ParseMode("Markdown"),
	}); err == nil {
		t.Fatal("SendMessage accepted an unsupported parse mode")
	}
	if err := client.AnswerCallbackQuery(
		context.Background(), "id", strings.Repeat("x", MaxCallbackTextBytes+1),
	); err == nil {
		t.Fatal("AnswerCallbackQuery accepted oversized text")
	}
}

func TestClientRedactsTokenFromAPIErrors(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.WriteHeader(http.StatusUnauthorized)
		writeJSON(t, writer, map[string]any{
			"ok": false, "error_code": 401, "description": "rejected " + testToken,
		})
	}))
	defer server.Close()
	client := newFakeServerClient(t, server, time.Second)
	_, err := client.SendMessage(context.Background(), MessageRequest{ChatID: 42, Text: "hello"})
	if err == nil {
		t.Fatal("SendMessage unexpectedly succeeded")
	}
	if strings.Contains(err.Error(), testToken) {
		t.Fatal("API error exposed the bot token")
	}
}

func TestClientExposesTelegramFloodWait(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.WriteHeader(http.StatusTooManyRequests)
		writeJSON(t, writer, map[string]any{
			"ok": false, "error_code": 429,
			"description": "Too Many Requests: retry after 1167",
			"parameters":  map[string]any{"retry_after": 1167},
		})
	}))
	defer server.Close()
	client := newFakeServerClient(t, server, time.Second)
	_, err := client.EditMessage(context.Background(), EditMessageRequest{
		ChatID: 42, MessageID: 9, Text: "limited",
	})
	retryAfter, limited := FloodWait(err)
	if !limited || retryAfter != 1167*time.Second {
		t.Fatalf("FloodWait(%v)=(%v,%t)", err, retryAfter, limited)
	}
}

func TestFloodWaitFallsBackToDescription(t *testing.T) {
	retryAfter, limited := FloodWait(&APIError{
		Method: "editMessageText", Code: 429,
		Description: "Too Many Requests: retry after 42",
	})
	if !limited || retryAfter != 42*time.Second {
		t.Fatalf("FloodWait=(%v,%t)", retryAfter, limited)
	}
}

func TestRemoteFloodWaitRejectsLocalCooldown(t *testing.T) {
	err := &APIError{
		Method: "editScreen", Code: 429, RetryAfter: time.Minute, Local: true,
	}
	if _, limited := RemoteFloodWait(err); limited {
		t.Fatal("local cooldown was classified as a remote Telegram rejection")
	}
}

func TestClientTreatsUnchangedEditAsSuccess(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if !strings.HasSuffix(request.URL.Path, "/editMessageText") {
			t.Errorf("unexpected path %q", request.URL.Path)
		}
		writer.WriteHeader(http.StatusBadRequest)
		writeJSON(t, writer, map[string]any{
			"ok": false, "error_code": 400,
			"description": "Bad Request: message is not modified: specified new message content and reply markup are exactly the same",
		})
	}))
	defer server.Close()
	client := newFakeServerClient(t, server, time.Second)
	message, err := client.EditMessage(context.Background(), EditMessageRequest{
		ChatID: 42, MessageID: 9, Text: "unchanged",
	})
	if err != nil {
		t.Fatalf("EditMessage: %v", err)
	}
	if message.ChatID != 42 || message.MessageID != 9 {
		t.Fatalf("message = %#v", message)
	}
}

func TestClientTreatsExpiredCallbackAsConsumed(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if !strings.HasSuffix(request.URL.Path, "/answerCallbackQuery") {
			t.Errorf("unexpected path %q", request.URL.Path)
		}
		writer.WriteHeader(http.StatusBadRequest)
		writeJSON(t, writer, map[string]any{
			"ok": false, "error_code": 400,
			"description": "Bad Request: query is too old and response timeout expired or query ID is invalid",
		})
	}))
	defer server.Close()
	client := newFakeServerClient(t, server, time.Second)
	if err := client.AnswerCallbackQuery(context.Background(), "expired", ""); err != nil {
		t.Fatalf("expired callback was not consumed: %v", err)
	}
}

func TestClientGetMeValidatesBotIdentity(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if !strings.HasSuffix(request.URL.Path, "/getMe") {
			t.Errorf("unexpected path %q", request.URL.Path)
		}
		writeJSON(t, writer, map[string]any{
			"ok":     true,
			"result": map[string]any{"id": 42, "is_bot": true, "username": "test_bot"},
		})
	}))
	defer server.Close()
	identity, err := newFakeServerClient(t, server, time.Second).GetMe(context.Background())
	if err != nil || identity.ID != 42 || identity.Username != "test_bot" {
		t.Fatalf("GetMe=(%#v, %v)", identity, err)
	}
}

func newFakeServerClient(t *testing.T, server *httptest.Server, timeout time.Duration) *Client {
	t.Helper()
	client, err := NewClient(ClientConfig{
		Token: testToken, BaseURL: server.URL, HTTPClient: server.Client(),
		RequestTimeout: timeout, LongPollSlack: timeout, AllowInsecureHTTP: true,
	})
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	return client
}

func writeJSON(t *testing.T, writer http.ResponseWriter, value any) {
	t.Helper()
	writer.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(writer).Encode(value); err != nil {
		t.Fatalf("encode response: %v", err)
	}
}
