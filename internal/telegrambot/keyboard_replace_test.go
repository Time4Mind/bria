package telegrambot

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/Time4Mind/bria/internal/telegramui"
)

func TestReplaceKeyboardSendsSafeHistoricalAction(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/bot"+testToken+"/editMessageReplyMarkup" {
			t.Fatalf("path=%q", request.URL.Path)
		}
		var payload editReplyMarkupPayload
		if err := json.NewDecoder(request.Body).Decode(&payload); err != nil {
			t.Fatal(err)
		}
		if payload.ChatID != 42 || payload.MessageID != 9 ||
			len(payload.ReplyMarkup.InlineKeyboard) != 1 ||
			len(payload.ReplyMarkup.InlineKeyboard[0]) != 1 ||
			payload.ReplyMarkup.InlineKeyboard[0][0].CallbackData != "session:token" {
			t.Fatalf("replace keyboard payload=%#v", payload)
		}
		writeJSON(t, writer, map[string]any{"ok": true, "result": true})
	}))
	defer server.Close()
	client := newFakeServerClient(t, server, time.Second)
	if err := client.ReplaceKeyboard(
		context.Background(), Message{ChatID: 42, MessageID: 9},
		telegramui.Grid{telegramui.Row{{
			Label: "Open current", Callback: telegramui.Callback{
				Action: telegramui.ActionSelectSession, Token: "token",
			},
		}}},
	); err != nil {
		t.Fatal(err)
	}
}
