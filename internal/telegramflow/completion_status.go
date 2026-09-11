package telegramflow

import (
	"context"
	"errors"

	"bria/internal/coordinator"
)

// EnqueueCompletionPrepared persists the first exact completion projection.
// Retries reuse it instead of recapturing whatever card became current later.
func (sender *Sender) EnqueueCompletionPrepared(ctx context.Context, sequence uint64, prepared Prepared) (coordinator.DurableOutboundReceipt, error) {
	if sender == nil || sequence == 0 || prepared.OperationID == "" || prepared.Card.SessionID == "" {
		return coordinator.DurableOutboundReceipt{}, errors.New("durable completion Telegram status is invalid")
	}
	if existing, found, err := sender.operations.LoadStatus(ctx, prepared.OperationID); err != nil {
		return coordinator.DurableOutboundReceipt{}, err
	} else if found {
		if existing.Sequence != sequence || existing.Status.ConversationID != prepared.Status.ConversationID ||
			existing.Prepared != nil && (existing.Prepared.Card.SessionID != prepared.Card.SessionID || existing.Prepared.Card.FinalOperationID != prepared.Card.FinalOperationID) {
			return coordinator.DurableOutboundReceipt{}, errors.New("durable completion Telegram status identity collision")
		}
		if existing.Phase == StatusQueued {
			_, _ = sender.deliverStatusOperation(ctx, existing)
		}
		return coordinator.DurableOutboundReceipt{OperationID: existing.ID, Sequence: existing.Sequence}, nil
	}
	operation := StatusOperation{ID: prepared.OperationID, Sequence: sequence, Status: prepared.Status,
		Keyboard: cloneCoordinatorKeyboard(prepared.Keyboard), Prepared: &prepared, Edit: prepared.Edit, Phase: StatusQueued}
	persisted, _, err := sender.operations.EnqueueStatus(ctx, operation)
	if err != nil {
		return coordinator.DurableOutboundReceipt{}, err
	}
	if persisted.Phase == StatusQueued {
		_, _ = sender.deliverStatusOperation(ctx, persisted)
	}
	return coordinator.DurableOutboundReceipt{OperationID: persisted.ID, Sequence: persisted.Sequence}, nil
}

func (sender *Sender) DeliverCompletionPrepared(ctx context.Context, sequence uint64, prepared Prepared) (coordinator.Receipt, error) {
	if _, err := sender.EnqueueCompletionPrepared(ctx, sequence, prepared); err != nil {
		return coordinator.Receipt{}, err
	}
	receipt, confirmed, err := sender.ResolveStatusReceipt(ctx, prepared.OperationID)
	if err != nil || !confirmed || receipt.MessageID <= 0 {
		return coordinator.Receipt{}, errors.Join(err, errors.New("Telegram completion delivery is unconfirmed"))
	}
	return receipt, nil
}
