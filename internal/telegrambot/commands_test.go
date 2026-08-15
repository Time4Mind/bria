package telegrambot

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestClientSetMyCommandsPublishesLocalizedMenu(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/bot"+testToken+"/setMyCommands" {
			t.Errorf("unexpected path %q", request.URL.Path)
		}
		var payload setMyCommandsPayload
		if err := json.NewDecoder(request.Body).Decode(&payload); err != nil {
			t.Fatalf("decode payload: %v", err)
		}
		if payload.LanguageCode != "ru" || len(payload.Commands) != 1 ||
			payload.Commands[0].Command != "menu" {
			t.Errorf("unexpected commands payload: %#v", payload)
		}
		writeJSON(t, writer, map[string]any{"ok": true, "result": true})
	}))
	defer server.Close()
	client := newFakeServerClient(t, server, time.Second)
	if err := client.SetMyCommands(context.Background(), []BotCommand{{
		Command: "menu", Description: "Открыть меню",
	}}, "ru"); err != nil {
		t.Fatalf("SetMyCommands: %v", err)
	}
}

func TestClientSetMyCommandsRejectsInvalidCommand(t *testing.T) {
	client, err := NewClient(ClientConfig{Token: testToken})
	if err != nil {
		t.Fatal(err)
	}
	if err := client.SetMyCommands(context.Background(), []BotCommand{{
		Command: "/Menu", Description: "invalid",
	}}, ""); err == nil {
		t.Fatal("SetMyCommands accepted an invalid command")
	}
}
