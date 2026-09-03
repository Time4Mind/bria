package interactionflow_test

import (
	"bytes"
	"context"
	"testing"
	"time"

	"bria/internal/callbacktoken"
	"bria/internal/coordinator"
	"bria/internal/interactionflow"
	"bria/internal/telegrambridge"
	"bria/internal/telegramflow"
	"bria/internal/telegramui"
)

func TestTelegramDeliverySenderSignsAndBindsInitialInteractionBeforeCarrierSend(t *testing.T) {
	t.Parallel()
	now := time.Unix(1_800_000_000, 0).UTC()
	codec, err := callbacktoken.New(bytes.Repeat([]byte{7}, 32), bytes.NewReader(bytes.Repeat([]byte{9}, 64)), func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	presenter, err := telegrambridge.NewPresenter(codec, func() time.Time { return now }, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	prepared := &recordingPreparedSender{}
	sender, err := interactionflow.NewTelegramDeliverySender(prepared, presenter)
	if err != nil {
		t.Fatal(err)
	}
	delivery := interactionflow.Delivery{
		OperationID: "interaction:opaque", SessionID: testSessionID,
		MessageID: "telegram-update:7", ProviderRequestID: "provider-request-1", ConversationID: 42,
		Surface: telegramflow.SurfaceOutput{
			Text: "Choose", Keyboard: telegramui.CardKeyboard{Rows: []telegramui.ButtonRow{{
				{Action: telegramui.ActionInteractionChoice, Target: telegramui.ButtonTarget{InteractionChoice: 1}},
			}}},
		},
	}
	receipt, err := sender.Deliver(context.Background(), delivery)
	if err != nil {
		t.Fatal(err)
	}
	if receipt.OperationID != delivery.OperationID || receipt.CarrierMessageID != 91 {
		t.Fatalf("receipt = %#v", receipt)
	}
	if prepared.registered.OperationID != delivery.OperationID ||
		prepared.registered.Presentation.InteractionRequestID != delivery.OperationID ||
		prepared.registered.Presentation.SessionID != string(testSessionID) ||
		len(prepared.registered.Presentation.TokenIDs) != 1 {
		t.Fatalf("prepared binding = %#v", prepared.registered)
	}
	if prepared.sentOperationID != delivery.OperationID {
		t.Fatalf("sent operation = %q", prepared.sentOperationID)
	}
}

func TestDeliveryRelayBindsExactlyOnceForAcyclicComposition(t *testing.T) {
	t.Parallel()
	relay := interactionflow.NewDeliveryRelay()
	if _, err := relay.Deliver(context.Background(), interactionflow.Delivery{}); err != interactionflow.ErrDeliveryUnbound {
		t.Fatalf("unbound Deliver error = %v", err)
	}
	target := &relayTarget{}
	if err := relay.Bind(target); err != nil {
		t.Fatal(err)
	}
	if err := relay.Bind(&relayTarget{}); err != interactionflow.ErrDeliveryAlreadyBound {
		t.Fatalf("second Bind error = %v", err)
	}
	receipt, err := relay.Deliver(context.Background(), interactionflow.Delivery{OperationID: "op"})
	if err != nil || receipt.OperationID != "op" || target.calls != 1 {
		t.Fatalf("relayed delivery = %#v, err=%v calls=%d", receipt, err, target.calls)
	}
}

type relayTarget struct{ calls int }

func (target *relayTarget) Deliver(_ context.Context, delivery interactionflow.Delivery) (interactionflow.DeliveryReceipt, error) {
	target.calls++
	return interactionflow.DeliveryReceipt{OperationID: delivery.OperationID, CarrierMessageID: 1}, nil
}

type recordingPreparedSender struct {
	registered      telegramflow.Prepared
	sentOperationID string
}

func (sender *recordingPreparedSender) Register(prepared telegramflow.Prepared) error {
	sender.registered = prepared
	return nil
}

func (sender *recordingPreparedSender) SendStatusWithKeyboard(_ context.Context, operationID string, status coordinator.Status, keyboard *coordinator.KeyboardMarkup) (coordinator.Receipt, error) {
	if status != sender.registered.Status || keyboard != sender.registered.Keyboard {
		return coordinator.Receipt{}, interactionflow.ErrInvalidConfiguration
	}
	sender.sentOperationID = operationID
	return coordinator.Receipt{MessageID: 91}, nil
}
