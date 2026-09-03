package telegramnotify_test

import (
	"context"
	"errors"
	"net/http"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"testing"

	"bria/internal/telegram"
	"bria/internal/telegramcontroller"
	"bria/internal/telegramnotify"
)

type partReceiptStore struct {
	mu        sync.Mutex
	confirmed map[string][]telegramnotify.PartReceipt
	unknown   []string
}

func (store *partReceiptStore) ConfirmedParts(_ context.Context, operationID string) ([]telegramnotify.PartReceipt, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	return append([]telegramnotify.PartReceipt(nil), store.confirmed[operationID]...), nil
}

func (store *partReceiptStore) ConfirmPart(_ context.Context, operationID string, receipt telegramnotify.PartReceipt) error {
	store.mu.Lock()
	defer store.mu.Unlock()
	store.confirmed[operationID] = append(store.confirmed[operationID], receipt)
	return nil
}

func (store *partReceiptStore) MarkPartUnknown(_ context.Context, operationID, partID string) error {
	store.mu.Lock()
	defer store.mu.Unlock()
	store.unknown = append(store.unknown, operationID+"/"+partID)
	return nil
}

func TestDeliverSkipsConfirmedPartsAndReturnsCompleteConfirmationSet(t *testing.T) {
	t.Parallel()

	const operationID = "turn:42:final"
	store := &partReceiptStore{confirmed: map[string][]telegramnotify.PartReceipt{
		operationID: {{PartID: operationID + ":part:1-of-3", MessageID: 701}},
	}}
	var sent []string
	client := mustNotifyClient(t, func(request *http.Request) (*http.Response, error) {
		var body telegram.SendMessageRequest
		decodeNotifyRequest(t, request, &body)
		sent = append(sent, body.Text)
		messageID := 701 + len(sent)
		return notifyResponse(http.StatusOK, `{"ok":true,"result":{"message_id":`+strconv.Itoa(messageID)+`,"from":{"id":600,"is_bot":true},"chat":{"id":42,"type":"private"}}}`), nil
	})
	notifier, err := telegramnotify.NewWithOptions(client, telegramnotify.Options{PartReceipts: store})
	if err != nil {
		t.Fatal(err)
	}
	receipt, err := notifier.Deliver(context.Background(), telegramcontroller.Notification{
		ConversationID: 42, SessionID: testSessionID, Kind: telegramcontroller.NotificationFinal,
		Text: strings.Repeat("a", 9000), OperationID: operationID,
	}, operationID)
	if err != nil {
		t.Fatalf("Deliver() error = %v", err)
	}
	if receipt.State != telegramnotify.DeliveryConfirmed || len(receipt.Parts) != 3 {
		t.Fatalf("Deliver() receipt = %#v, want three confirmed parts", receipt)
	}
	if len(sent) != 2 {
		t.Fatalf("send calls = %d, want only two unconfirmed pages", len(sent))
	}
	wantIDs := []string{
		operationID + ":part:1-of-3",
		operationID + ":part:2-of-3",
		operationID + ":part:3-of-3",
	}
	gotIDs := make([]string, 0, len(receipt.Parts))
	for _, part := range receipt.Parts {
		gotIDs = append(gotIDs, part.PartID)
	}
	if !reflect.DeepEqual(gotIDs, wantIDs) {
		t.Fatalf("part ids = %#v, want %#v", gotIDs, wantIDs)
	}
}

func TestDeliverMarksAmbiguousPartAndNeverRetriesItInCall(t *testing.T) {
	t.Parallel()

	const operationID = "turn:84:final"
	store := &partReceiptStore{confirmed: make(map[string][]telegramnotify.PartReceipt)}
	calls := 0
	client := mustNotifyClient(t, func(*http.Request) (*http.Response, error) {
		calls++
		if calls == 2 {
			return nil, errors.New("connection closed after write")
		}
		return notifyResponse(http.StatusOK, `{"ok":true,"result":{"message_id":801,"from":{"id":600,"is_bot":true},"chat":{"id":42,"type":"private"}}}`), nil
	})
	notifier, err := telegramnotify.NewWithOptions(client, telegramnotify.Options{PartReceipts: store})
	if err != nil {
		t.Fatal(err)
	}
	receipt, err := notifier.Deliver(context.Background(), telegramcontroller.Notification{
		ConversationID: 42, SessionID: testSessionID, Kind: telegramcontroller.NotificationFinal,
		Text: strings.Repeat("b", 9000), OperationID: operationID,
	}, operationID)
	if err == nil {
		t.Fatal("Deliver() error = nil, want ambiguous outcome")
	}
	if receipt.State != telegramnotify.DeliveryUnknown || calls != 2 {
		t.Fatalf("Deliver() receipt/calls = %#v/%d, want unknown after two calls", receipt, calls)
	}
	if len(store.confirmed[operationID]) != 1 || len(store.unknown) != 1 {
		t.Fatalf("confirmation state = confirmed %#v unknown %#v", store.confirmed, store.unknown)
	}
}

func TestDeliverRejectsMultipartWithoutDurableConfirmationStore(t *testing.T) {
	t.Parallel()

	calls := 0
	client := mustNotifyClient(t, func(*http.Request) (*http.Response, error) {
		calls++
		return nil, errors.New("must not send")
	})
	notifier, err := telegramnotify.New(client)
	if err != nil {
		t.Fatal(err)
	}
	_, err = notifier.Deliver(context.Background(), telegramcontroller.Notification{
		ConversationID: 42, SessionID: testSessionID, Kind: telegramcontroller.NotificationFinal,
		Text: strings.Repeat("c", 9000), OperationID: "turn:126:final",
	}, "turn:126:final")
	if err == nil || calls != 0 {
		t.Fatalf("Deliver() error/calls = %v/%d, want preflight rejection", err, calls)
	}
}
