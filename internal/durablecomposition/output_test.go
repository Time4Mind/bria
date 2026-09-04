package durablecomposition_test

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"bria/internal/domain"
	"bria/internal/durablecomposition"
	"bria/internal/durableflow"
	"bria/internal/messagejournal"
	"bria/internal/telegramcontroller"
	"bria/internal/telegramnotify"
)

type deliveryFunc func(context.Context, telegramcontroller.Notification, string) (telegramnotify.DeliveryReceipt, error)

func (function deliveryFunc) Deliver(ctx context.Context, notification telegramcontroller.Notification, operationID string) (telegramnotify.DeliveryReceipt, error) {
	return function(ctx, notification, operationID)
}

func TestOutputCustodyCoalescesOnlyPendingIntermediateCardStates(t *testing.T) {
	journal, err := messagejournal.Open(filepath.Join(t.TempDir(), "journal.json"), messagejournal.DefaultLimits())
	if err != nil {
		t.Fatal(err)
	}
	flow, err := durableflow.New(journal, nil, nil, durableflow.Options{
		Owner: "local", LeaseDuration: time.Minute, Now: func() time.Time { return time.Unix(1, 0) },
	})
	if err != nil {
		t.Fatal(err)
	}
	custody := durablecomposition.OutputCustody{Flow: flow, OwnerPrivateChatID: 42}
	sessionID := domain.SessionID("00000000-0000-4000-8000-000000000001")
	for _, notification := range []telegramcontroller.OutgoingNotification{
		{OperationID: "event:1", ConversationID: 42, SessionID: sessionID, Kind: telegramcontroller.NotificationCommentary, Payload: []byte("one")},
		{OperationID: "event:2", ConversationID: 42, SessionID: sessionID, Kind: telegramcontroller.NotificationPromptStatus, Payload: []byte("two")},
		{OperationID: "question:1", ConversationID: 42, SessionID: sessionID, Kind: telegramcontroller.NotificationQuestion, Payload: []byte("question")},
		{OperationID: "final:1", ConversationID: 42, SessionID: sessionID, Kind: telegramcontroller.NotificationFinal, Payload: []byte("final")},
	} {
		if _, err := custody.AcceptOutput(context.Background(), notification); err != nil {
			t.Fatal(err)
		}
	}
	outputs, err := journal.Outputs(context.Background(), string(sessionID))
	if err != nil {
		t.Fatal(err)
	}
	want := []messagejournal.OutputPhase{
		messagejournal.OutputSuperseded, messagejournal.OutputSuperseded,
		messagejournal.OutputPending, messagejournal.OutputPending,
	}
	if len(outputs) != len(want) {
		t.Fatalf("outputs = %#v", outputs)
	}
	for index := range want {
		if outputs[index].Phase != want[index] {
			t.Fatalf("output phases = %#v, want %#v", outputs, want)
		}
	}
}

func TestTelegramOutputSenderAcceptsExplicitlySuppressedStateWithoutSyntheticMessageReceipt(t *testing.T) {
	sender := durablecomposition.TelegramOutputSender{OwnerPrivateChatID: 42, Deliverer: deliveryFunc(func(_ context.Context, _ telegramcontroller.Notification, operationID string) (telegramnotify.DeliveryReceipt, error) {
		return telegramnotify.DeliveryReceipt{OperationID: operationID, State: telegramnotify.DeliveryConfirmed, Suppressed: true}, nil
	})}
	result, err := sender.Deliver(context.Background(), durableflow.ProviderOutput{
		SessionID: "00000000-0000-4000-8000-000000000001", OperationID: "event:1", Sequence: 1,
		Kind: string(telegramcontroller.NotificationCommentary), Payload: []byte("hidden"),
	})
	if err != nil || result.State != durableflow.DeliveryConfirmed || result.Receipt != "telegram:event:1:suppressed" {
		t.Fatalf("Deliver() = (%#v, %v)", result, err)
	}
}
