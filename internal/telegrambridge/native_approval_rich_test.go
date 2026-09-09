package telegrambridge_test

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"bria/internal/coordinator"
	"bria/internal/telegram"
	"bria/internal/telegrambridge"
)

func TestCommandApprovalReachesTelegramAsLiteralRichMarkdown(t *testing.T) {
	formatted := "**Command**\n\n```shell\nprintf '%s' '$HOME'\n```"
	client := mustTelegramClient(t, func(request *http.Request) (*http.Response, error) {
		var body struct {
			Text string                     `json:"text"`
			Rich *telegram.InputRichMessage `json:"rich_message"`
		}
		if err := json.NewDecoder(request.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		if !strings.HasSuffix(request.URL.Path, "/sendRichMessage") || body.Text != "" || body.Rich == nil || body.Rich.Markdown != formatted {
			t.Fatalf("approval took a non-literal Rich route: path=%s body=%+v", request.URL.Path, body)
		}
		return response(http.StatusOK, `{"ok":true,"result":{"message_id":55,"from":{"id":600,"is_bot":true},"chat":{"id":42,"type":"private"}}}`), nil
	})
	sender, err := telegrambridge.NewSender(client)
	if err != nil {
		t.Fatal(err)
	}
	defer sender.Close(context.Background())
	keyboard := coordinator.KeyboardMarkup{{{Text: "Enter", CallbackData: "signed"}}}
	if _, err := sender.SendStatusWithKeyboard(context.Background(), "approval-rich", coordinator.Status{ConversationID: 42, Text: formatted, RichMarkdown: true}, &keyboard); err != nil {
		t.Fatal(err)
	}
}
