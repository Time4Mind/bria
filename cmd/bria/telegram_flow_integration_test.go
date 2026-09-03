package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"bria/internal/telegram"
	"bria/internal/telegramflow"
)

func TestRunExecutesSignedMenuCallbackThroughDurableTypedFlow(t *testing.T) {
	temporary := t.TempDir()
	configPath, statePath := writeStatusConfig(t, temporary, "123:signed-flow-secret")
	disableAllProviders(t, configPath)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	transport := &signedCallbackTransport{t: t, cancel: cancel}
	dependencies := testCommandDependencies(t, &http.Client{Transport: transport})

	var stdout, stderr strings.Builder
	if code := runContextWithDependencies(ctx, []string{"run", "--config", configPath}, &stdout, &stderr, dependencies); code != 0 {
		t.Fatalf("run exit code = %d, stderr = %q", code, stderr.String())
	}
	if transport.callbackData == "" || transport.rawCallbackSeen {
		t.Fatalf("callback_data = %q raw=%t, want opaque signed data only", transport.callbackData, transport.rawCallbackSeen)
	}
	operations, err := telegramflow.OpenFileCallbackOperationStore(statePath + ".callback-operations.json")
	if err != nil {
		t.Fatal(err)
	}
	operation, found, err := operations.Load(context.Background(), "status:102")
	if err != nil || !found || operation.Phase != telegramflow.CallbackCommitted || operation.Receipt != 70 {
		t.Fatalf("callback operation = %#v found=%t err=%v", operation, found, err)
	}
}

type signedCallbackTransport struct {
	t               *testing.T
	cancel          context.CancelFunc
	calls           int
	callbackData    string
	rawCallbackSeen bool
}

func (transport *signedCallbackTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	transport.t.Helper()
	transport.calls++
	switch transport.calls {
	case 1:
		return telegramResponse(`{"ok":true,"result":{"id":600,"is_bot":true,"first_name":"Bria","username":"my_bria_bot"}}`), nil
	case 2:
		return telegramResponse(`{"ok":true,"result":[]}`), nil
	case 3:
		return telegramResponse(`{"ok":true,"result":[{"update_id":101,"message":{"message_id":60,"from":{"id":42},"chat":{"id":42,"type":"private"},"text":"/menu"}}]}`), nil
	case 4:
		if request.URL.Path != "/bot123:signed-flow-secret/sendMessage" {
			transport.t.Fatalf("menu request path = %q", request.URL.Path)
		}
		var payload struct {
			ReplyMarkup struct {
				Rows [][]struct {
					CallbackData string `json:"callback_data"`
				} `json:"inline_keyboard"`
			} `json:"reply_markup"`
		}
		if err := json.Unmarshal([]byte(requestBody(transport.t, request)), &payload); err != nil {
			transport.t.Fatal(err)
		}
		if len(payload.ReplyMarkup.Rows) == 0 || len(payload.ReplyMarkup.Rows[0]) == 0 {
			transport.t.Fatal("menu has no signed buttons")
		}
		transport.callbackData = payload.ReplyMarkup.Rows[0][0].CallbackData
		transport.rawCallbackSeen = strings.Contains(transport.callbackData, "menu:") || strings.Contains(transport.callbackData, "mm:")
		return telegramResponse(`{"ok":true,"result":{"message_id":70,"from":{"id":600,"is_bot":true},"chat":{"id":42,"type":"private"},"text":"menu"}}`), nil
	case 5:
		body := fmt.Sprintf(`{"ok":true,"result":[{"update_id":102,"callback_query":{"id":"callback-102","from":{"id":42,"is_bot":false,"first_name":"A"},"message":{"message_id":70,"from":{"id":600,"is_bot":true,"first_name":"Bria"},"chat":{"id":42,"type":"private"}},"data":%q}}]}`, transport.callbackData)
		return telegramResponse(body), nil
	case 6:
		if request.URL.Path != "/bot123:signed-flow-secret/answerCallbackQuery" {
			transport.t.Fatalf("callback acknowledgement path = %q", request.URL.Path)
		}
		return telegramResponse(`{"ok":true,"result":true}`), nil
	case 7:
		if request.URL.Path != "/bot123:signed-flow-secret/editMessageText" {
			transport.t.Fatalf("typed callback mutation path = %q", request.URL.Path)
		}
		body := requestBody(transport.t, request)
		if !strings.Contains(body, `"message_id":70`) || !strings.Contains(body, "reply_markup") || strings.Contains(body, "mm:") {
			transport.t.Fatalf("typed callback edit body = %q", body)
		}
		return telegramResponse(`{"ok":true,"result":{"message_id":70,"from":{"id":600,"is_bot":true},"chat":{"id":42,"type":"private"},"text":"sessions"}}`), nil
	case 8:
		transport.cancel()
		return nil, errors.New("stop signed callback test poll")
	default:
		transport.t.Fatalf("unexpected Telegram call %d to %s", transport.calls, request.URL.Redacted())
		return nil, nil
	}
}

var _ telegram.HTTPClient = (*http.Client)(nil)
