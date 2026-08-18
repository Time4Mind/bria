package telegrambot

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Time4Mind/bria/internal/telegramui"
)

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
