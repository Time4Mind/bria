package telegramnotify_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"unicode/utf8"

	"bria/internal/telegram"
	"bria/internal/telegramcontroller"
	"bria/internal/telegramnotify"
)

func TestSavedTablePagesKeepHeadersRowsAndFinalWireBudget(t *testing.T) {
	path := filepath.Join(t.TempDir(), "parts.json")
	store, err := telegramnotify.OpenFilePartReceiptStore(path)
	if err != nil {
		t.Fatal(err)
	}
	const header = "| key | value |\n| --- | --- |\n"
	var source strings.Builder
	source.WriteString(header)
	for i := 0; i < 160; i++ {
		fmt.Fprintf(&source, "| row-%03d | %s |\n", i, strings.Repeat("данные東京🙂", 5))
	}
	var sent []string
	client := mustNotifyClient(t, func(request *http.Request) (*http.Response, error) {
		page := decodeRichNotifyRequest(t, request).RichMessage.Markdown
		if len(page) > 4096 || !utf8.ValidString(page) {
			t.Errorf("invalid final wire page: %d bytes", len(page))
		}
		if !strings.Contains(page, "| <sub>key</sub> | <sub>value</sub> |\n| --- | --- |\n") {
			t.Error("continuation lacks complete table header")
		}
		sent = append(sent, page)
		return notifyResponse(http.StatusOK, fmt.Sprintf(`{"ok":true,"result":{"message_id":%d,"from":{"id":600,"is_bot":true},"chat":{"id":42,"type":"private"}}}`, 800+len(sent))), nil
	})
	notifier, err := telegramnotify.NewWithOptions(client, telegramnotify.Options{PartReceipts: store})
	if err != nil {
		t.Fatal(err)
	}
	receipt, err := notifier.Deliver(context.Background(), telegramcontroller.Notification{OperationID: "table-plan", ConversationID: 42, SessionID: testSessionID, Kind: telegramcontroller.NotificationFinal, Text: source.String()}, "table-plan")
	if err != nil || receipt.State != telegramnotify.DeliveryConfirmed {
		t.Fatalf("table delivery = %+v / %v", receipt, err)
	}
	if len(sent) < 2 || !reflect.DeepEqual(sent, readFrozenPages(t, path, "table-plan")) {
		t.Fatal("wire does not match complete frozen plan")
	}
	all := strings.Join(sent, "")
	for i := 0; i < 160; i++ {
		if strings.Count(all, fmt.Sprintf("row-%03d", i)) != 1 {
			t.Fatalf("row %d lost or repeated", i)
		}
	}
	if strings.Count(all, "данные東京🙂") != 800 {
		t.Fatal("cell body lost or duplicated")
	}
}

func TestV1NotificationResumeKeepsLegacyPartsAndUnknownFence(t *testing.T) {
	const operation = "legacy-table"
	const prefix = "Сессия 11111111 - итог\n"
	text := "| key | value |\n| --- | --- |\n" + strings.Repeat("| original | unchanged legacy table row |\n", 260)
	var oldPages []string
	// Frozen v1 rule: exact byte slices before Rich normalization, no row-aware
	// packing. ASCII fixture permits an independent explicit legacy boundary.
	for remaining := text; remaining != ""; {
		end := min(len(remaining), 4096-len(prefix))
		oldPages = append(oldPages, telegram.NormalizeRichMarkdown(prefix+remaining[:end]))
		remaining = remaining[end:]
	}
	firstID := fmt.Sprintf("%s:part:1-of-%d", operation, len(oldPages))
	unknownID := fmt.Sprintf("%s:part:2-of-%d", operation, len(oldPages))
	path := filepath.Join(t.TempDir(), "parts.json")
	seed := map[string]any{"version": 1, "operations": map[string]any{operation: map[string]any{firstID: map[string]any{"state": "confirmed", "message_id": 701}, unknownID: map[string]any{"state": "unknown"}}}}
	data, err := json.Marshal(seed)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	store, err := telegramnotify.OpenFilePartReceiptStore(path)
	if err != nil {
		t.Fatal(err)
	}
	var sent []string
	client := mustNotifyClient(t, func(request *http.Request) (*http.Response, error) {
		sent = append(sent, decodeRichNotifyRequest(t, request).RichMessage.Markdown)
		return notifyResponse(http.StatusOK, fmt.Sprintf(`{"ok":true,"result":{"message_id":%d,"from":{"id":600,"is_bot":true},"chat":{"id":42,"type":"private"}}}`, 900+len(sent))), nil
	})
	notification := telegramcontroller.Notification{OperationID: operation, ConversationID: 42, SessionID: testSessionID, Kind: telegramcontroller.NotificationFinal, Text: text}
	notifier, err := telegramnotify.NewWithOptions(client, telegramnotify.Options{PartReceipts: store})
	if err != nil {
		t.Fatal(err)
	}
	receipt, err := notifier.Deliver(context.Background(), notification, operation)
	if err == nil || receipt.State != telegramnotify.DeliveryUnknown || len(sent) != 0 {
		t.Fatalf("legacy unknown auto-sent: %+v / %v", receipt, err)
	}
	if !reflect.DeepEqual(readFrozenPages(t, path, operation), oldPages) {
		t.Fatal("old parts were silently repaginated")
	}
	if err := store.ResolveUnknownForRetry(context.Background(), operation, unknownID); err != nil {
		t.Fatal(err)
	}
	store, err = telegramnotify.OpenFilePartReceiptStore(path)
	if err != nil {
		t.Fatal(err)
	}
	notifier, err = telegramnotify.NewWithOptions(client, telegramnotify.Options{PartReceipts: store})
	if err != nil {
		t.Fatal(err)
	}
	receipt, err = notifier.Deliver(context.Background(), notification, operation)
	if err != nil || receipt.State != telegramnotify.DeliveryConfirmed {
		t.Fatalf("explicit legacy resume = %+v / %v", receipt, err)
	}
	if !reflect.DeepEqual(sent, oldPages[1:]) || receipt.Parts[0].MessageID != 701 {
		t.Fatal("legacy resume changed pages, ids or confirmed receipt")
	}
}
