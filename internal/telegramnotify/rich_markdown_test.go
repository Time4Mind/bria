package telegramnotify_test

import (
	"context"
	"errors"
	"net/http"
	"path/filepath"
	"reflect"
	"testing"

	"bria/internal/telegramcontroller"
	"bria/internal/telegramnotify"
)

func TestNotifierRichMarkdownContent(t *testing.T) {
	t.Parallel()
	for _, seam := range []string{"Notify", "Deliver"} {
		for _, fixture := range []struct {
			name, text, want string
		}{
			{"plain", "Фоновая сессия завершена.", "Фоновая сессия завершена."},
			{"formatting", "**Готово**\n\n`auth_status` 🙂\n[отчёт](https://example.test/report)", "**Готово**\n\n`auth_status` 🙂\n[отчёт](https://example.test/report)"},
			{"table", "| Статус | Значение |\n| --- | --- |\n| Готово | 42 |", "\n| <sub>Статус</sub> | <sub>Значение</sub> |\n| --- | --- |\n| <sub>Готово</sub> | <sub>42</sub> |"},
			{"literal table", "```markdown\n| A | B |\n| --- | --- |\n| x | y |\n```", `<pre><code class="language-markdown">| A | B |
| --- | --- |
| x | y |</code></pre>`},
		} {
			t.Run(seam+"/"+fixture.name, func(t *testing.T) {
				t.Parallel()
				recorder := &receiptRecorder{}
				var sent []string
				client := mustNotifyClient(t, func(request *http.Request) (*http.Response, error) {
					body := decodeRichNotifyRequest(t, request)
					sent = append(sent, body.RichMessage.Markdown)
					return notifyResponse(http.StatusOK, `{"ok":true,"result":{"message_id":731,"from":{"id":600,"is_bot":true},"chat":{"id":42,"type":"private"}}}`), nil
				})
				notifier, err := telegramnotify.NewWithOptions(client, telegramnotify.Options{ReceiptRecorder: recorder})
				if err != nil {
					t.Fatal(err)
				}
				notification := telegramcontroller.Notification{
					ConversationID: 42, SessionID: testSessionID,
					Kind: telegramcontroller.NotificationFinal, Text: fixture.text,
				}
				if seam == "Notify" {
					err = notifier.Notify(context.Background(), notification)
				} else {
					var receipt telegramnotify.DeliveryReceipt
					receipt, err = notifier.Deliver(context.Background(), notification, "turn:42:final")
					want := telegramnotify.DeliveryReceipt{
						OperationID: "turn:42:final", State: telegramnotify.DeliveryConfirmed,
						Parts: []telegramnotify.PartReceipt{{PartID: "turn:42:final:part:1-of-1", MessageID: 731}},
					}
					if err == nil && !reflect.DeepEqual(receipt, want) {
						t.Fatalf("receipt = %#v, want %#v", receipt, want)
					}
				}
				if err != nil {
					t.Fatal(err)
				}
				if want := []string{"Сессия 11111111 - итог\n" + fixture.want}; !reflect.DeepEqual(sent, want) {
					t.Fatalf("sent markdown = %#v, want %#v", sent, want)
				}
				if want := []telegramnotify.OutboundReceipt{{MessageID: 731, SessionID: testSessionID}}; !reflect.DeepEqual(recorder.receipts, want) {
					t.Fatalf("reply receipts = %#v, want %#v", recorder.receipts, want)
				}
			})
		}
	}
}

func TestRichDeliveryUnknownSurvivesReopenWithoutFallbackOrRetry(t *testing.T) {
	t.Parallel()
	for _, outcome := range []string{"connection lost", "server error", "request timeout", "missing receipt"} {
		t.Run(outcome, func(t *testing.T) {
			t.Parallel()
			path := filepath.Join(t.TempDir(), "parts.json")
			store, err := telegramnotify.OpenFilePartReceiptStore(path)
			if err != nil {
				t.Fatal(err)
			}
			var sent []string
			client := mustNotifyClient(t, func(request *http.Request) (*http.Response, error) {
				body := decodeRichNotifyRequest(t, request)
				sent = append(sent, body.RichMessage.Markdown)
				switch outcome {
				case "connection lost":
					return nil, errors.New("connection closed after write")
				case "server error":
					return notifyResponse(http.StatusInternalServerError, `{"ok":false,"error_code":500,"description":"unknown"}`), nil
				case "request timeout":
					return notifyResponse(http.StatusRequestTimeout, `{"ok":false,"error_code":408,"description":"request timed out"}`), nil
				default:
					return notifyResponse(http.StatusOK, `{"ok":true,"result":{"message_id":0,"chat":{"id":42,"type":"private"}}}`), nil
				}
			})
			notification := telegramcontroller.Notification{
				ConversationID: 42, SessionID: testSessionID,
				Kind: telegramcontroller.NotificationFinal, Text: "**Готово.**",
			}
			const operationID = "turn:42:final"
			for attempt := 0; attempt < 2; attempt++ {
				recorder := &receiptRecorder{}
				notifier, err := telegramnotify.NewWithOptions(client, telegramnotify.Options{
					PartReceipts: store, ReceiptRecorder: recorder,
				})
				if err != nil {
					t.Fatal(err)
				}
				receipt, err := notifier.Deliver(context.Background(), notification, operationID)
				if err == nil || receipt.State != telegramnotify.DeliveryUnknown || len(receipt.Parts) != 0 || len(recorder.receipts) != 0 {
					t.Fatalf("attempt %d = %#v / %v, reply receipts %#v", attempt, receipt, err, recorder.receipts)
				}
				store, err = telegramnotify.OpenFilePartReceiptStore(path)
				if err != nil {
					t.Fatal(err)
				}
				unknown, err := store.UnknownParts(context.Background(), operationID)
				if err != nil || !reflect.DeepEqual(unknown, []string{operationID + ":part:1-of-1"}) {
					t.Fatalf("durable unknown = %#v / %v", unknown, err)
				}
			}
			if !reflect.DeepEqual(sent, []string{"Сессия 11111111 - итог\n**Готово.**"}) {
				t.Fatalf("Rich attempts = %#v, want exactly one full message across reopen", sent)
			}
		})
	}
}
