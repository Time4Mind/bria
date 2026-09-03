package interactionflow

import (
	"context"
	"errors"
	"sync"

	"bria/internal/coordinator"
	"bria/internal/telegrambridge"
	"bria/internal/telegramflow"
)

var (
	ErrDeliveryUnbound      = errors.New("provider interaction delivery is not bound")
	ErrDeliveryAlreadyBound = errors.New("provider interaction delivery is already bound")
)

// PreparedSender is implemented by telegramflow.Sender. Register keeps the
// signed manifest private until SendStatusWithKeyboard obtains a positive
// Telegram receipt and durably binds it to the exact carrier.
type PreparedSender interface {
	Register(telegramflow.Prepared) error
	SendStatusWithKeyboard(context.Context, string, coordinator.Status, *coordinator.KeyboardMarkup) (coordinator.Receipt, error)
}

// DeliveryRelay breaks the intentional composition cycle: Flow is the
// telegramflow callback executor, while its initial delivery uses the Sender
// returned by telegramflow.New. Bind is a one-time local composition action.
type DeliveryRelay struct {
	mu     sync.RWMutex
	sender DeliverySender
}

func NewDeliveryRelay() *DeliveryRelay { return &DeliveryRelay{} }

func (relay *DeliveryRelay) Bind(sender DeliverySender) error {
	if relay == nil || sender == nil {
		return ErrInvalidConfiguration
	}
	relay.mu.Lock()
	defer relay.mu.Unlock()
	if relay.sender != nil {
		return ErrDeliveryAlreadyBound
	}
	relay.sender = sender
	return nil
}

func (relay *DeliveryRelay) Deliver(ctx context.Context, delivery Delivery) (DeliveryReceipt, error) {
	if relay == nil {
		return DeliveryReceipt{}, ErrDeliveryUnbound
	}
	relay.mu.RLock()
	sender := relay.sender
	relay.mu.RUnlock()
	if sender == nil {
		return DeliveryReceipt{}, ErrDeliveryUnbound
	}
	return sender.Deliver(ctx, delivery)
}

// TelegramDeliverySender is the concrete initial-delivery adapter used by
// Flow. Callback continuation is handled by Flow.HandleCallback through the
// ordinary signed telegramflow callback pipeline.
type TelegramDeliverySender struct {
	sender    PreparedSender
	presenter *telegrambridge.Presenter
}

func NewTelegramDeliverySender(sender PreparedSender, presenter *telegrambridge.Presenter) (*TelegramDeliverySender, error) {
	if sender == nil || presenter == nil {
		return nil, ErrInvalidConfiguration
	}
	return &TelegramDeliverySender{sender: sender, presenter: presenter}, nil
}

func (sender *TelegramDeliverySender) Deliver(ctx context.Context, delivery Delivery) (DeliveryReceipt, error) {
	if sender == nil || sender.sender == nil || sender.presenter == nil {
		return DeliveryReceipt{}, ErrInvalidConfiguration
	}
	prepared, err := telegramflow.PrepareInteraction(
		delivery.OperationID,
		delivery.SessionID,
		delivery.ConversationID,
		delivery.OperationID,
		delivery.Surface,
		sender.presenter,
	)
	if err != nil {
		return DeliveryReceipt{}, errors.New("prepare signed provider interaction failed")
	}
	if err := sender.sender.Register(prepared); err != nil {
		return DeliveryReceipt{}, errors.New("register signed provider interaction failed")
	}
	receipt, err := sender.sender.SendStatusWithKeyboard(ctx, delivery.OperationID, prepared.Status, prepared.Keyboard)
	if err != nil || receipt.MessageID <= 0 {
		return DeliveryReceipt{}, errors.New("provider interaction Telegram delivery is unconfirmed")
	}
	return DeliveryReceipt{OperationID: delivery.OperationID, CarrierMessageID: receipt.MessageID}, nil
}

var _ DeliverySender = (*TelegramDeliverySender)(nil)
var _ DeliverySender = (*DeliveryRelay)(nil)
