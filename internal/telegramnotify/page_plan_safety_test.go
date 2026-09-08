package telegramnotify_test

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"bria/internal/telegramcontroller"
	"bria/internal/telegramnotify"
)

func planNotification(operation string) telegramcontroller.Notification {
	return telegramcontroller.Notification{OperationID: operation, ConversationID: 42, SessionID: testSessionID, Kind: telegramcontroller.NotificationFinal, Text: "**Original source**"}
}

func TestConcurrentDeliveryHandlesCannotDuplicateClaimedPage(t *testing.T) {
	path := filepath.Join(t.TempDir(), "parts.json")
	firstStore, err := telegramnotify.OpenFilePartReceiptStore(path)
	if err != nil {
		t.Fatal(err)
	}
	secondStore, err := telegramnotify.OpenFilePartReceiptStore(path)
	if err != nil {
		t.Fatal(err)
	}
	entered, release := make(chan struct{}), make(chan struct{})
	var calls atomic.Int32
	client := mustNotifyClient(t, func(request *http.Request) (*http.Response, error) {
		if calls.Add(1) == 1 {
			close(entered)
		}
		select {
		case <-release:
		case <-request.Context().Done():
			return nil, request.Context().Err()
		}
		return notifyResponse(http.StatusOK, `{"ok":true,"result":{"message_id":710,"from":{"id":600,"is_bot":true},"chat":{"id":42,"type":"private"}}}`), nil
	})
	first, err := telegramnotify.NewWithOptions(client, telegramnotify.Options{PartReceipts: firstStore})
	if err != nil {
		t.Fatal(err)
	}
	second, err := telegramnotify.NewWithOptions(client, telegramnotify.Options{PartReceipts: secondStore})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
	defer cancel()
	notification := planNotification("concurrent-plan")
	finished := make(chan error, 1)
	go func() { _, err := first.Deliver(ctx, notification, notification.OperationID); finished <- err }()
	select {
	case <-entered:
	case <-ctx.Done():
		t.Fatal("first send did not start")
	}
	competing, stop := context.WithTimeout(ctx, time.Second)
	receipt, err := second.Deliver(competing, notification, notification.OperationID)
	stop()
	if err == nil || receipt.State != telegramnotify.DeliveryUnknown || calls.Load() != 1 {
		t.Errorf("concurrent delivery = %+v / %v, sends=%d", receipt, err, calls.Load())
	}
	close(release)
	select {
	case err := <-finished:
		if err != nil {
			t.Fatal(err)
		}
	case <-ctx.Done():
		t.Fatal("first delivery failed to finish")
	}
	receipt, err = second.Deliver(ctx, notification, notification.OperationID)
	if err != nil || receipt.State != telegramnotify.DeliveryConfirmed || calls.Load() != 1 {
		t.Fatalf("completed plan replayed: %+v / %v sends=%d", receipt, err, calls.Load())
	}
}

type failedConfirmationStore struct {
	*telegramnotify.FilePartReceiptStore
}

func (failedConfirmationStore) ConfirmPart(context.Context, string, telegramnotify.PartReceipt) error {
	return errors.New("injected confirmation persistence failure")
}

func TestSuccessfulWireWithLostConfirmationRemainsUnknownAfterReopen(t *testing.T) {
	path := filepath.Join(t.TempDir(), "parts.json")
	store, err := telegramnotify.OpenFilePartReceiptStore(path)
	if err != nil {
		t.Fatal(err)
	}
	calls := 0
	client := mustNotifyClient(t, func(*http.Request) (*http.Response, error) {
		calls++
		return notifyResponse(http.StatusOK, `{"ok":true,"result":{"message_id":711,"from":{"id":600,"is_bot":true},"chat":{"id":42,"type":"private"}}}`), nil
	})
	notifier, err := telegramnotify.NewWithOptions(client, telegramnotify.Options{PartReceipts: failedConfirmationStore{store}})
	if err != nil {
		t.Fatal(err)
	}
	notification := planNotification("lost-confirmation")
	receipt, err := notifier.Deliver(context.Background(), notification, notification.OperationID)
	if err == nil || receipt.State != telegramnotify.DeliveryUnknown || calls != 1 {
		t.Fatalf("first outcome = %+v / %v", receipt, err)
	}
	store, err = telegramnotify.OpenFilePartReceiptStore(path)
	if err != nil {
		t.Fatal(err)
	}
	notifier, err = telegramnotify.NewWithOptions(client, telegramnotify.Options{PartReceipts: store})
	if err != nil {
		t.Fatal(err)
	}
	receipt, err = notifier.Deliver(context.Background(), notification, notification.OperationID)
	if err == nil || receipt.State != telegramnotify.DeliveryUnknown || calls != 1 {
		t.Fatalf("unconfirmed success resent: %+v / %v sends=%d", receipt, err, calls)
	}
}

func TestResumeUsesSavedPayloadWithoutRunningCurrentRenderer(t *testing.T) {
	path := filepath.Join(t.TempDir(), "parts.json")
	store, err := telegramnotify.OpenFilePartReceiptStore(path)
	if err != nil {
		t.Fatal(err)
	}
	notification := planNotification("previous-renderer")
	input, err := json.Marshal([]any{notification.ConversationID, notification.SessionID, notification.Kind, notification.Text})
	if err != nil {
		t.Fatal(err)
	}
	hash := fmt.Sprintf("%x", sha256.Sum256(input))
	const frozen = "Сессия 11111111 - итог\nA payload saved by an earlier renderer."
	if _, err := store.GetOrCreatePlan(context.Background(), notification.OperationID, hash, []string{frozen}, nil); err != nil {
		t.Fatal(err)
	}
	store, err = telegramnotify.OpenFilePartReceiptStore(path)
	if err != nil {
		t.Fatal(err)
	}
	client := mustNotifyClient(t, func(request *http.Request) (*http.Response, error) {
		if page := decodeRichNotifyRequest(t, request).RichMessage.Markdown; page != frozen {
			t.Errorf("frozen page was recalculated: %q", page)
		}
		return notifyResponse(http.StatusOK, `{"ok":true,"result":{"message_id":712,"from":{"id":600,"is_bot":true},"chat":{"id":42,"type":"private"}}}`), nil
	})
	notifier, err := telegramnotify.NewWithOptions(client, telegramnotify.Options{PartReceipts: store})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := notifier.Deliver(context.Background(), notification, notification.OperationID); err != nil {
		t.Fatal(err)
	}
}

func TestLostPlanEntryCannotRebindChangedInputOrMutateCorruptFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "parts.json")
	store, err := telegramnotify.OpenFilePartReceiptStore(path)
	if err != nil {
		t.Fatal(err)
	}
	notification := planNotification("lost-plan-entry")
	input, err := json.Marshal([]any{notification.ConversationID, notification.SessionID, notification.Kind, notification.Text})
	if err != nil {
		t.Fatal(err)
	}
	hash := fmt.Sprintf("%x", sha256.Sum256(input))
	if _, err := store.GetOrCreatePlan(context.Background(), notification.OperationID, hash, []string{"frozen"}, nil); err != nil {
		t.Fatal(err)
	}
	if claimed, err := store.ClaimPart(context.Background(), notification.OperationID, notification.OperationID+":part:1-of-1"); err != nil || !claimed {
		t.Fatalf("claim = %v / %v", claimed, err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var state map[string]json.RawMessage
	if err := json.Unmarshal(data, &state); err != nil {
		t.Fatal(err)
	}
	state["plans"] = json.RawMessage(`{}`)
	damaged, err := json.Marshal(state)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, damaged, 0600); err != nil {
		t.Fatal(err)
	}
	calls := 0
	client := mustNotifyClient(t, func(*http.Request) (*http.Response, error) { calls++; return nil, errors.New("must not send") })
	notifier, err := telegramnotify.NewWithOptions(client, telegramnotify.Options{PartReceipts: store})
	if err != nil {
		t.Fatal(err)
	}
	notification.Text += " changed after loss"
	if _, err := notifier.Deliver(context.Background(), notification, notification.OperationID); err == nil {
		t.Fatal("lost plan was rebound")
	}
	actual, err := os.ReadFile(path)
	if err != nil || string(actual) != string(damaged) || calls != 0 {
		t.Fatalf("corrupt state mutated or sent: same=%v calls=%d err=%v", string(actual) == string(damaged), calls, err)
	}
}
