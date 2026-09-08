package telegrambridge_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"

	"bria/internal/coordinator"
	"bria/internal/telegram"
	"bria/internal/telegrambridge"
)

func TestTableRenderingSelectsRichWithoutScreenOrExplicitFlag(t *testing.T) {
	const table = "| Column | Value |\n|---|---|\n| one | two |"
	for _, action := range []string{"send", "send-keyboard", "edit"} {
		t.Run(action, func(t *testing.T) {
			calls := 0
			client := mustTelegramClient(t, func(request *http.Request) (*http.Response, error) {
				calls++
				var body struct {
					Text      string                     `json:"text"`
					Rich      *telegram.InputRichMessage `json:"rich_message"`
					MessageID int64                      `json:"message_id"`
				}
				if err := json.NewDecoder(request.Body).Decode(&body); err != nil {
					t.Fatal(err)
				}
				method := "sendRichMessage"
				if action == "edit" {
					method = "editMessageText"
				}
				if !strings.HasSuffix(request.URL.Path, "/"+method) || body.Rich == nil || !strings.Contains(body.Rich.Markdown, "| <sub>Column</sub> | <sub>Value</sub> |") || body.Text != "" {
					t.Errorf("table took wrong transport: method=%s body=%+v", request.URL.Path, body)
				}
				if action == "edit" && body.MessageID != 55 {
					t.Errorf("changed carrier: %d", body.MessageID)
				}
				if body.Rich != nil && len(body.Rich.Media) != 0 {
					t.Error("table rendering forced Screen")
				}
				return response(http.StatusOK, `{"ok":true,"result":{"message_id":55,"from":{"id":600,"is_bot":true},"chat":{"id":42,"type":"private"}}}`), nil
			})
			sender, err := telegrambridge.NewSender(client)
			if err != nil {
				t.Fatal(err)
			}
			defer sender.Close(context.Background())
			status := coordinator.Status{ConversationID: 42, SourceMessageID: 55, Text: "Card\n\n─────  \n" + table}
			keyboard := coordinator.KeyboardMarkup{{{Text: "1/1", CallbackData: "signed"}}}
			switch action {
			case "send":
				_, err = sender.SendStatus(context.Background(), "table", status)
			case "send-keyboard":
				_, err = sender.SendStatusWithKeyboard(context.Background(), "table", status, &keyboard)
			case "edit":
				_, err = sender.EditStatusWithKeyboard(context.Background(), "table", status, &keyboard)
			}
			if err != nil || calls != 1 {
				t.Fatalf("delivery err=%v calls=%d", err, calls)
			}
		})
	}
}

func TestTableRichUnknownSendDoesNotFallBackOrRetry(t *testing.T) {
	calls := 0
	client := mustTelegramClient(t, func(request *http.Request) (*http.Response, error) {
		calls++
		var body map[string]json.RawMessage
		decodeJSON(t, request, &body)
		if !strings.HasSuffix(request.URL.Path, "/sendRichMessage") || body["rich_message"] == nil {
			t.Error("not rich table attempt")
		}
		return nil, errors.New("ambiguous send outcome")
	})
	sender, err := telegrambridge.NewSender(client)
	if err != nil {
		t.Fatal(err)
	}
	_, err = sender.SendStatus(context.Background(), "table-unknown", coordinator.Status{ConversationID: 42, Text: "| A | B |\n|---|---|\n| one | two |"})
	if err == nil || calls != 1 {
		t.Fatalf("unknown delivery err=%v attempts=%d", err, calls)
	}
}

func TestAllTextMessagesUseRichRegardlessOfContentOrFlag(t *testing.T) {
	const markdown = "Menu\n\nChoose **action**"
	for _, action := range []string{"send", "send-keyboard", "edit"} {
		t.Run(action, func(t *testing.T) {
			client := mustTelegramClient(t, func(request *http.Request) (*http.Response, error) {
				var body struct {
					Text      string                     `json:"text"`
					ParseMode string                     `json:"parse_mode"`
					Rich      *telegram.InputRichMessage `json:"rich_message"`
				}
				if err := json.NewDecoder(request.Body).Decode(&body); err != nil {
					t.Fatal(err)
				}
				method := "sendRichMessage"
				if action == "edit" {
					method = "editMessageText"
				}
				if !strings.HasSuffix(request.URL.Path, "/"+method) || body.Rich == nil || body.Rich.Markdown != markdown || len(body.Rich.Media) != 0 || body.Text != "" || body.ParseMode != "" {
					t.Errorf("non-Rich ordinary message: %s %+v", request.URL.Path, body)
				}
				return response(http.StatusOK, `{"ok":true,"result":{"message_id":55,"from":{"id":600,"is_bot":true},"chat":{"id":42,"type":"private"}}}`), nil
			})
			sender, err := telegrambridge.NewSender(client)
			if err != nil {
				t.Fatal(err)
			}
			defer sender.Close(context.Background())
			status := coordinator.Status{ConversationID: 42, SourceMessageID: 55, Text: markdown, RichMarkdown: false}
			keyboard := coordinator.KeyboardMarkup{{{Text: "Menu", CallbackData: "signed"}}}
			switch action {
			case "send":
				_, err = sender.SendStatus(context.Background(), "ordinary", status)
			case "send-keyboard":
				_, err = sender.SendStatusWithKeyboard(context.Background(), "ordinary", status, &keyboard)
			case "edit":
				_, err = sender.EditStatusWithKeyboard(context.Background(), "ordinary", status, &keyboard)
			}
			if err != nil {
				t.Fatal(err)
			}
		})
	}
}
