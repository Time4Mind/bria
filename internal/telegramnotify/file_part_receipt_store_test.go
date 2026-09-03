package telegramnotify_test

import (
	"context"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"bria/internal/telegramcontroller"
	"bria/internal/telegramnotify"
)

func TestFilePartReceiptStoreReopensConfirmedAndUnknownState(t *testing.T) {
	path := filepath.Join(t.TempDir(), "telegram-parts.json")
	store, err := telegramnotify.OpenFilePartReceiptStore(path)
	if err != nil {
		t.Fatal(err)
	}
	const operationID = "turn:1:final"
	confirmed := telegramnotify.PartReceipt{PartID: operationID + ":part:1-of-2", MessageID: 701}
	unknown := operationID + ":part:2-of-2"
	if err := store.ConfirmPart(context.Background(), operationID, confirmed); err != nil {
		t.Fatal(err)
	}
	if err := store.MarkPartUnknown(context.Background(), operationID, unknown); err != nil {
		t.Fatal(err)
	}
	reopened, err := telegramnotify.OpenFilePartReceiptStore(path)
	if err != nil {
		t.Fatal(err)
	}
	gotConfirmed, err := reopened.ConfirmedParts(context.Background(), operationID)
	if err != nil || !reflect.DeepEqual(gotConfirmed, []telegramnotify.PartReceipt{confirmed}) {
		t.Fatalf("confirmed after reopen = (%#v, %v)", gotConfirmed, err)
	}
	gotUnknown, err := reopened.UnknownParts(context.Background(), operationID)
	if err != nil || !reflect.DeepEqual(gotUnknown, []string{unknown}) {
		t.Fatalf("unknown after reopen = (%#v, %v)", gotUnknown, err)
	}
	if err := reopened.ResolveUnknownForRetry(context.Background(), operationID, unknown); err != nil {
		t.Fatal(err)
	}
	gotUnknown, err = reopened.UnknownParts(context.Background(), operationID)
	if err != nil || len(gotUnknown) != 0 {
		t.Fatalf("unknown after explicit retry resolution = (%#v, %v)", gotUnknown, err)
	}
}

func TestFilePartReceiptStoreConfirmedReceiptIsImmutable(t *testing.T) {
	store, err := telegramnotify.OpenFilePartReceiptStore(filepath.Join(t.TempDir(), "telegram-parts.json"))
	if err != nil {
		t.Fatal(err)
	}
	const operationID = "turn:2:final"
	part := telegramnotify.PartReceipt{PartID: operationID + ":part:1-of-1", MessageID: 801}
	if err := store.ConfirmPart(context.Background(), operationID, part); err != nil {
		t.Fatal(err)
	}
	part.MessageID = 802
	if err := store.ConfirmPart(context.Background(), operationID, part); err == nil {
		t.Fatal("conflicting confirmed receipt was accepted")
	}
}

func TestFilePartReceiptStoreRejectsCorruptionAndUnknownFields(t *testing.T) {
	for _, body := range []string{
		`{"version":1,"operations":`,
		`{"version":2,"operations":{}}`,
		`{"version":1,"operations":{},"secret":"forbidden"}`,
	} {
		path := filepath.Join(t.TempDir(), "telegram-parts.json")
		if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := telegramnotify.OpenFilePartReceiptStore(path); err == nil {
			t.Fatalf("corrupt state accepted: %s", body)
		}
	}
}

func TestDeliverDoesNotResendDurablyUnknownPartBeforeExplicitResolution(t *testing.T) {
	const operationID = "turn:3:final"
	store, err := telegramnotify.OpenFilePartReceiptStore(filepath.Join(t.TempDir(), "telegram-parts.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := store.MarkPartUnknown(context.Background(), operationID, operationID+":part:1-of-3"); err != nil {
		t.Fatal(err)
	}
	calls := 0
	client := mustNotifyClient(t, func(*http.Request) (*http.Response, error) {
		calls++
		return nil, nil
	})
	notifier, err := telegramnotify.NewWithOptions(client, telegramnotify.Options{PartReceipts: store})
	if err != nil {
		t.Fatal(err)
	}
	receipt, err := notifier.Deliver(context.Background(), telegramcontroller.Notification{
		OperationID: operationID, ConversationID: 42, SessionID: testSessionID,
		Kind: telegramcontroller.NotificationFinal, Text: strings.Repeat("x", 9000),
	}, operationID)
	if err == nil || receipt.State != telegramnotify.DeliveryUnknown || calls != 0 {
		t.Fatalf("Deliver unresolved unknown = (%#v, %v), calls=%d", receipt, err, calls)
	}
}
