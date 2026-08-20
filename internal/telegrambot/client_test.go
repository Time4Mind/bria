package telegrambot

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Time4Mind/bria/internal/i18n"
	"github.com/Time4Mind/bria/internal/telegramui"
)

func TestClientGetUpdatesUsesBoundedLongPollPayload(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/bot"+testToken+"/getUpdates" {
			t.Errorf("unexpected path %q", request.URL.Path)
		}
		if !request.Close {
			t.Error("getUpdates must not reuse a proxy connection")
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
	editedScreen := screen
	editedScreen.Text += "\nchanged"
	if _, err := client.EditScreen(context.Background(), message, editedScreen); err != nil {
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

func TestClientSkipsUnchangedScreenEdit(t *testing.T) {
	var mu sync.Mutex
	methods := make([]string, 0, 2)
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		method := request.URL.Path[strings.LastIndex(request.URL.Path, "/")+1:]
		mu.Lock()
		methods = append(methods, method)
		mu.Unlock()
		writeJSON(t, writer, map[string]any{
			"ok": true, "result": map[string]any{"message_id": 9, "chat": map[string]any{"id": 42}},
		})
	}))
	defer server.Close()

	client := newFakeServerClient(t, server, time.Second)
	screen := telegramui.RenderMainMenu(i18n.For("en"), "backend")
	message, err := client.SendScreen(context.Background(), 42, screen)
	if err != nil {
		t.Fatalf("SendScreen: %v", err)
	}
	if message.ScreenHash == "" {
		t.Fatal("sent screen has no fingerprint")
	}
	unchanged, err := client.EditScreen(context.Background(), message, screen)
	if err != nil {
		t.Fatalf("unchanged EditScreen: %v", err)
	}
	if unchanged != message {
		t.Fatalf("unchanged message = %#v want %#v", unchanged, message)
	}

	changed := screen
	changed.Text += "\nchanged"
	updated, err := client.EditScreen(context.Background(), unchanged, changed)
	if err != nil {
		t.Fatalf("changed EditScreen: %v", err)
	}
	if updated.ScreenHash == "" || updated.ScreenHash == message.ScreenHash {
		t.Fatalf("changed screen fingerprint = %q, previous %q", updated.ScreenHash, message.ScreenHash)
	}
	mu.Lock()
	defer mu.Unlock()
	if got := strings.Join(methods, ","); got != "sendMessage,editMessageText" {
		t.Fatalf("methods = %q", got)
	}
}
