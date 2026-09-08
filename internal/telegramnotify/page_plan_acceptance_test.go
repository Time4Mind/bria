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

	"bria/internal/telegramcontroller"
	"bria/internal/telegramnotify"
)

func TestSavedNotificationPlanPrecedesSendAndResumesExactPages(t *testing.T) {
	const operation = "saved-pages:final"
	path := filepath.Join(t.TempDir(), "parts.json")
	store, err := telegramnotify.OpenFilePartReceiptStore(path)
	if err != nil {
		t.Fatal(err)
	}
	notification := telegramcontroller.Notification{OperationID: operation, ConversationID: 42, SessionID: testSessionID, Kind: telegramcontroller.NotificationFinal, Text: strings.Repeat("x", 9000)}
	var attempts []string
	rejectSecond := true
	client := mustNotifyClient(t, func(request *http.Request) (*http.Response, error) {
		body := decodeRichNotifyRequest(t, request)
		attempts = append(attempts, body.RichMessage.Markdown)
		pages := readFrozenPages(t, path, operation)
		if len(pages) < 3 {
			t.Errorf("complete plan absent before send: %d pages", len(pages))
		}
		unknown, err := store.UnknownParts(context.Background(), operation)
		if err != nil || len(unknown) != 1 {
			t.Errorf("send not durably claimed: %v / %v", unknown, err)
		}
		if rejectSecond && len(attempts) == 2 {
			return notifyResponse(http.StatusBadRequest, `{"ok":false,"error_code":400,"description":"definitive rejection"}`), nil
		}
		return notifyResponse(http.StatusOK, fmt.Sprintf(`{"ok":true,"result":{"message_id":%d,"from":{"id":600,"is_bot":true},"chat":{"id":42,"type":"private"}}}`, 700+len(attempts))), nil
	})
	notifier, err := telegramnotify.NewWithOptions(client, telegramnotify.Options{PartReceipts: store})
	if err != nil {
		t.Fatal(err)
	}
	receipt, err := notifier.Deliver(context.Background(), notification, operation)
	if err == nil || receipt.State != telegramnotify.DeliveryFailed || len(receipt.Parts) != 1 {
		t.Fatalf("first delivery = %+v / %v", receipt, err)
	}
	wantPages := readFrozenPages(t, path, operation)
	if len(wantPages) < 3 {
		t.Fatal("no frozen plan available for resume")
	}
	store, err = telegramnotify.OpenFilePartReceiptStore(path)
	if err != nil {
		t.Fatal(err)
	}
	notifier, err = telegramnotify.NewWithOptions(client, telegramnotify.Options{PartReceipts: store})
	if err != nil {
		t.Fatal(err)
	}
	rejectSecond = false
	start := len(attempts)
	if err := notifier.Notify(context.Background(), notification); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(attempts[start:], wantPages[1:]) {
		t.Fatal("resume changed frozen pages or resent confirmed first part")
	}
	if err := notifier.Notify(context.Background(), notification); err != nil {
		t.Fatal(err)
	}
	if len(attempts) != start+len(wantPages)-1 {
		t.Fatal("completed plan replayed")
	}
	for _, changed := range []telegramcontroller.Notification{
		{OperationID: operation, ConversationID: 43, SessionID: testSessionID, Kind: notification.Kind, Text: notification.Text},
		{OperationID: operation, ConversationID: 42, SessionID: "22222222-2222-4333-8444-555555555555", Kind: notification.Kind, Text: notification.Text},
		{OperationID: operation, ConversationID: 42, SessionID: testSessionID, Kind: telegramcontroller.NotificationError, Text: notification.Text},
		{OperationID: operation, ConversationID: 42, SessionID: testSessionID, Kind: notification.Kind, Text: notification.Text + "changed"},
	} {
		if _, err := notifier.Deliver(context.Background(), changed, operation); err == nil {
			t.Error("changed delivery input accepted")
		}
	}
	if len(attempts) != start+len(wantPages)-1 {
		t.Fatal("identity conflict caused a send")
	}
}

func readFrozenPages(t *testing.T, path, operation string) []string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Errorf("read page plan: %v", err)
		return nil
	}
	var state struct {
		Plans map[string]struct {
			Pages []string `json:"pages"`
		} `json:"plans"`
	}
	if err := json.Unmarshal(data, &state); err != nil {
		t.Errorf("decode page plan: %v", err)
		return nil
	}
	return state.Plans[operation].Pages
}
