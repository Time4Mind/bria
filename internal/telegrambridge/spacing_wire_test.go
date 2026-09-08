package telegrambridge_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"

	"bria/internal/coordinator"
	"bria/internal/telegram"
	"bria/internal/telegrambridge"
)

type spacingHTTPClient func(*http.Request) (*http.Response, error)

func (client spacingHTTPClient) Do(request *http.Request) (*http.Response, error) {
	return client(request)
}

func TestCardDividerHardBreakOnUniformRichWire(t *testing.T) {
	// The semantic card header owns the explicit Rich Markdown hard break.
	const header = "📋 Session\n\n─────  \n"
	for _, body := range []struct {
		name, markdown string
	}{
		{"ordinary", "**Answer**\n\nNext"},
		{"code", "```go\nanswer()\n```"},
		{"list", "- **First**\n- Second"},
	} {
		for _, mode := range []string{"legacy-off", "legacy-on"} {
			for _, action := range []string{"send", "send-keyboard", "edit"} {
				t.Run(body.name+"/"+mode+"/"+action, func(t *testing.T) {
					var wire struct {
						Text     string                     `json:"text"`
						Entities []telegram.MessageEntity   `json:"entities"`
						Rich     *telegram.InputRichMessage `json:"rich_message"`
					}
					var method string
					client, err := telegram.NewClient("123:spacing-test", spacingHTTPClient(func(request *http.Request) (*http.Response, error) {
						method = request.URL.Path
						if err := json.NewDecoder(request.Body).Decode(&wire); err != nil {
							t.Fatal(err)
						}
						return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(`{"ok":true,"result":{"message_id":55,"from":{"id":600,"is_bot":true},"chat":{"id":42,"type":"private"}}}`))}, nil
					}), telegram.Options{})
					if err != nil {
						t.Fatal(err)
					}
					sender, err := telegrambridge.NewSender(client)
					if err != nil {
						t.Fatal(err)
					}
					status := coordinator.Status{ConversationID: 42, SourceMessageID: 55, Text: header + body.markdown, RichMarkdown: mode == "legacy-on"}
					keyboard := coordinator.KeyboardMarkup{{{Text: "Menu", CallbackData: "signed"}}}
					switch action {
					case "send":
						_, err = sender.SendStatus(context.Background(), "spacing", status)
					case "send-keyboard":
						_, err = sender.SendStatusWithKeyboard(context.Background(), "spacing", status, &keyboard)
					case "edit":
						_, err = sender.EditStatusWithKeyboard(context.Background(), "spacing", status, &keyboard)
					}
					if err != nil {
						t.Fatal(err)
					}
					wantMethod := "/sendRichMessage"
					if action == "edit" {
						wantMethod = "/editMessageText"
					}
					if !strings.HasSuffix(method, wantMethod) {
						t.Fatalf("wire method = %q, want %q", method, wantMethod)
					}
					if wire.Rich == nil || wire.Rich.Markdown != header+body.markdown || wire.Text != "" || len(wire.Entities) != 0 {
						t.Fatalf("wire = %+v, want uniform Rich hardbreak regardless of legacy flag", wire)
					}
				})
			}
		}
	}
}
