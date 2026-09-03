package integration_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"bria/internal/coordinator"
	"bria/internal/storage"
	"bria/internal/telegram"
	"bria/internal/telegramapp"
	"bria/internal/telegrambridge"
)

func TestTelegramStatusAcceptanceQuarantinesPersistsAndDoesNotReplay(t *testing.T) {
	statePath := filepath.Join(t.TempDir(), "state.json")
	transport := &statusAcceptanceHTTPClient{t: t, statePath: statePath}
	client, err := telegram.NewClient("123:acceptance-secret", transport, telegram.Options{})
	if err != nil {
		t.Fatalf("telegram.NewClient() error = %v", err)
	}

	run := func() error {
		checkpoints, err := storage.OpenCoordinatorCheckpointStore(statePath)
		if err != nil {
			return err
		}
		source, err := telegrambridge.NewSource(client)
		if err != nil {
			return err
		}
		sender, err := telegrambridge.NewSender(client)
		if err != nil {
			return err
		}
		readiness, err := telegrambridge.NewReadiness(client, "my_bria_bot")
		if err != nil {
			return err
		}
		handler, err := telegramapp.NewStatusHandler(42, 42)
		if err != nil {
			return err
		}
		loop, err := coordinator.NewLoop(source, checkpoints, handler, sender, readiness)
		if err != nil {
			return err
		}
		return loop.Run(context.Background())
	}

	if err := run(); !errors.Is(err, coordinator.ErrUpdateBlocked) {
		t.Fatalf("first Run() error = %v, want ErrUpdateBlocked", err)
	}
	if transport.sendCalls != 1 {
		t.Fatalf("sendMessage calls = %d, want 1", transport.sendCalls)
	}

	checkpoints, err := storage.OpenCoordinatorCheckpointStore(statePath)
	if err != nil {
		t.Fatalf("OpenCoordinatorCheckpointStore() error = %v", err)
	}
	stored, found, err := checkpoints.Load(context.Background())
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if !found || stored.Checkpoint.NextUpdateID != 80 || stored.Checkpoint.Blocked == nil ||
		stored.Checkpoint.Blocked.UpdateID != 80 {
		t.Fatalf("stored checkpoint = %#v, want offset 80 blocked at update 80", stored)
	}
	if stored.Checkpoint.Outbound == nil || stored.Checkpoint.Outbound.Receipt == nil ||
		stored.Checkpoint.Outbound.Receipt.MessageID != 901 {
		t.Fatalf("stored checkpoint = %#v, want confirmed receipt 901", stored)
	}

	transport.restart = true
	if err := run(); !errors.Is(err, coordinator.ErrUpdateBlocked) {
		t.Fatalf("restart Run() error = %v, want ErrUpdateBlocked", err)
	}
	if transport.sendCalls != 1 {
		t.Fatalf("sendMessage calls after restart = %d, want no duplicate", transport.sendCalls)
	}
	if transport.restartCalls != 1 {
		t.Fatalf("restart HTTP calls = %d, want bot identity check only", transport.restartCalls)
	}
}

type statusAcceptanceHTTPClient struct {
	t            *testing.T
	statePath    string
	calls        int
	sendCalls    int
	restart      bool
	restartCalls int
}

func (client *statusAcceptanceHTTPClient) Do(request *http.Request) (*http.Response, error) {
	client.t.Helper()
	if request.URL.Scheme != "https" || request.URL.Host != "api.telegram.org" {
		client.t.Fatalf("destination = %s, want official Telegram TLS endpoint", request.URL.Redacted())
	}
	if client.restart {
		client.restartCalls++
		if request.URL.Path != "/bot123:acceptance-secret/getMe" {
			client.t.Fatalf("restart path = %q, want getMe", request.URL.Path)
		}
		return statusAcceptanceResponse(`{"ok":true,"result":{"id":600,"is_bot":true,"first_name":"Bria","username":"my_bria_bot"}}`), nil
	}

	client.calls++
	switch client.calls {
	case 1:
		if request.URL.Path != "/bot123:acceptance-secret/getMe" {
			client.t.Fatalf("request 1 path = %q, want getMe", request.URL.Path)
		}
		if _, err := os.Stat(client.statePath); !os.IsNotExist(err) {
			client.t.Fatalf("state exists before identity check: %v", err)
		}
		return statusAcceptanceResponse(`{"ok":true,"result":{"id":600,"is_bot":true,"first_name":"Bria","username":"my_bria_bot"}}`), nil
	case 2:
		var body telegram.GetUpdatesRequest
		statusAcceptanceDecode(client.t, request, &body)
		if body.Offset != -1 || body.Limit != 1 || body.TimeoutSeconds != 0 {
			client.t.Fatalf("bootstrap request = %#v", body)
		}
		return statusAcceptanceResponse(`{"ok":true,"result":[{"update_id":77,"message":{"message_id":1,"from":{"id":42},"chat":{"id":42,"type":"private"},"text":"stale must not run"}}]}`), nil
	case 3:
		var body telegram.GetUpdatesRequest
		statusAcceptanceDecode(client.t, request, &body)
		if body.Offset != 78 {
			client.t.Fatalf("live offset = %d, want 78", body.Offset)
		}
		state, err := os.ReadFile(client.statePath)
		if err != nil || !strings.Contains(string(state), `"next_update_id": 78`) {
			client.t.Fatalf("bootstrap checkpoint not durable before live poll: err=%v state=%s", err, state)
		}
		return statusAcceptanceResponse(`{"ok":true,"result":[
  {"update_id":78,"message":{"message_id":2,"from":{"id":7},"chat":{"id":7,"type":"private"},"text":"/status"}},
  {"update_id":79,"message":{"message_id":3,"from":{"id":42},"chat":{"id":42,"type":"private"},"text":"/status"}},
  {"update_id":80,"message":{"message_id":4,"from":{"id":42},"chat":{"id":42,"type":"private"},"text":"/future"}}
]}`), nil
	case 4:
		client.sendCalls++
		var body telegram.SendMessageRequest
		statusAcceptanceDecode(client.t, request, &body)
		if body.ChatID != 42 || body.Text == "" || strings.Contains(body.Text, "stale") {
			client.t.Fatalf("sendMessage body = %#v", body)
		}
		return statusAcceptanceResponse(`{"ok":true,"result":{"message_id":901,"from":{"id":600,"is_bot":true},"chat":{"id":42,"type":"private"},"text":"ready"}}`), nil
	default:
		client.t.Fatalf("unexpected Telegram request %d: %s", client.calls, request.URL.Redacted())
		return nil, nil
	}
}

func statusAcceptanceDecode(t *testing.T, request *http.Request, destination any) {
	t.Helper()
	decoder := json.NewDecoder(request.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(destination); err != nil {
		t.Fatalf("decode request body: %v", err)
	}
}

func statusAcceptanceResponse(body string) *http.Response {
	return &http.Response{
		StatusCode: http.StatusOK,
		Header:     make(http.Header),
		Body:       io.NopCloser(strings.NewReader(body)),
	}
}
