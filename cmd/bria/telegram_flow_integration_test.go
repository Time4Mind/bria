package main

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"bria/internal/telegram"
	"bria/internal/telegramflow"
)

func TestRunExecutesSignedMenuCallbackThroughDurableTypedFlow(t *testing.T) {
	temporary := t.TempDir()
	configPath, statePath := writeStatusConfig(t, temporary, "123:signed-flow-secret")
	disableAllProviders(t, configPath)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
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
	defer cancelFlowOnFailure(transport.t, transport.cancel)
	transport.calls++
	switch transport.calls {
	case 1:
		return telegramResponse(`{"ok":true,"result":{"id":600,"is_bot":true,"first_name":"Bria","username":"my_bria_bot"}}`), nil
	case 2:
		return telegramResponse(`{"ok":true,"result":[]}`), nil
	case 3:
		return telegramResponse(`{"ok":true,"result":[{"update_id":101,"message":{"message_id":60,"from":{"id":42},"chat":{"id":42,"type":"private"},"text":"/menu"}}]}`), nil
	case 4:
		if request.URL.Path != "/bot123:signed-flow-secret/sendRichMessage" {
			return nil, flowFixtureError(transport.t, "menu request path = %q", request.URL.Path)
		}
		payload := decodeRichFlowBody(transport.t, requestBody(transport.t, request))
		if payload.RichMessage.Markdown != "Меню" {
			return nil, flowFixtureError(transport.t, "menu Markdown = %q", payload.RichMessage.Markdown)
		}
		if payload.ReplyMarkup == nil || len(payload.ReplyMarkup.InlineKeyboard) == 0 || len(payload.ReplyMarkup.InlineKeyboard[0]) == 0 {
			return nil, flowFixtureError(transport.t, "menu has no signed buttons")
		}
		transport.callbackData = payload.ReplyMarkup.InlineKeyboard[0][0].CallbackData
		transport.rawCallbackSeen = strings.Contains(transport.callbackData, "menu:") || strings.Contains(transport.callbackData, "mm:")
		return telegramResponse(`{"ok":true,"result":{"message_id":70,"from":{"id":600,"is_bot":true},"chat":{"id":42,"type":"private"},"text":"menu"}}`), nil
	case 5:
		body := fmt.Sprintf(`{"ok":true,"result":[{"update_id":102,"callback_query":{"id":"callback-102","from":{"id":42,"is_bot":false,"first_name":"A"},"message":{"message_id":70,"from":{"id":600,"is_bot":true,"first_name":"Bria"},"chat":{"id":42,"type":"private"}},"data":%q}}]}`, transport.callbackData)
		return telegramResponse(body), nil
	case 6:
		if request.URL.Path != "/bot123:signed-flow-secret/answerCallbackQuery" {
			return nil, flowFixtureError(transport.t, "callback acknowledgement path = %q", request.URL.Path)
		}
		return telegramResponse(`{"ok":true,"result":true}`), nil
	case 7:
		if request.URL.Path != "/bot123:signed-flow-secret/editMessageText" {
			return nil, flowFixtureError(transport.t, "typed callback mutation path = %q", request.URL.Path)
		}
		body := requestBody(transport.t, request)
		payload := decodeRichFlowBody(transport.t, body)
		if payload.MessageID != 70 || payload.ReplyMarkup == nil || !strings.HasPrefix(payload.RichMessage.Markdown, "Сессии") || strings.Contains(body, "mm:") {
			return nil, flowFixtureError(transport.t, "typed callback edit body = %q", body)
		}
		return telegramResponse(`{"ok":true,"result":{"message_id":70,"from":{"id":600,"is_bot":true},"chat":{"id":42,"type":"private"},"text":"sessions"}}`), nil
	case 8:
		transport.cancel()
		return nil, errors.New("stop signed callback test poll")
	default:
		return nil, flowFixtureError(transport.t, "unexpected Telegram call %d to %s", transport.calls, request.URL.Redacted())
	}
}

var _ telegram.HTTPClient = (*http.Client)(nil)
