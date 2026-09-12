package telegramflow

import (
	"context"
	"errors"
	"fmt"

	"bria/internal/coordinator"
	"bria/internal/telegramcardretirement"
	"bria/internal/telegramstate"
)

func CapturePreviousCarrier(ctx context.Context, store telegramcardretirement.Store, prepared Prepared, prior error) (Prepared, error) {
	if prior != nil {
		return prepared, prior
	}
	var err error
	prepared.CardRetirementCaptured = true
	prepared.CardRetirement, err = telegramcardretirement.Capture(ctx, store, prepared.Card.SessionID, retirementEligible(prepared))
	return prepared, err
}

func retirementEligible(prepared Prepared) bool {
	return prepared.Card.MakeActive && !prepared.Edit && prepared.Surface == nil && !prepared.Terminal
}

func (sender *Sender) captureLegacyRetirement(ctx context.Context, operation StatusOperation) (StatusOperation, bool, error) {
	if operation.Prepared == nil || operation.Prepared.CardRetirementCaptured {
		return operation, false, nil
	}
	prepared := *operation.Prepared
	prepared.CardRetirementCaptured = true
	if retirementEligible(prepared) {
		state, err := sender.uiState.Load(ctx)
		if err != nil {
			return StatusOperation{}, false, err
		}
		current, exists := state.Card(operation.Prepared.Card.SessionID)
		if exists && current.Carrier.ChatID > 0 && current.Carrier.MessageID > 0 {
			revision := operation.Prepared.Card.ExpectedCarrierRevision
			if revision == nil || *revision != current.CarrierRevision {
				settled, settleErr := sender.supersedeRetirement(ctx, operation, current.Carrier.MessageID)
				return settled, true, settleErr
			}
			prepared.CardRetirement = &telegramcardretirement.Plan{
				SessionID: operation.Prepared.Card.SessionID, Carrier: current.Carrier, CarrierRevision: current.CarrierRevision,
			}
		}
	}
	updated := operation
	updated.Prepared = &prepared
	changed, err := sender.operations.CompareAndSwapStatus(ctx, operation.ID, StatusQueued, updated)
	if err != nil || !changed {
		return StatusOperation{}, false, errors.Join(err, errors.New("legacy Telegram card retirement plan changed"))
	}
	return updated, false, nil
}

func (sender *Sender) supersedeStaleRetirement(ctx context.Context, operation StatusOperation) (coordinator.Receipt, error) {
	if operation.Prepared == nil || operation.Prepared.CardRetirement == nil {
		return coordinator.Receipt{}, errors.New("stale Telegram card retirement plan is missing")
	}
	settled, err := sender.supersedeRetirement(ctx, operation, operation.Prepared.CardRetirement.Carrier.MessageID)
	return coordinator.Receipt{MessageID: settled.Receipt}, err
}

func (sender *Sender) supersedeRetirement(ctx context.Context, operation StatusOperation, receiptID int64) (StatusOperation, error) {
	if receiptID <= 0 {
		return StatusOperation{}, errors.New("superseded Telegram card receipt is invalid")
	}
	if err := sender.releaseSupersededFinal(context.WithoutCancel(ctx), operation); err != nil {
		return StatusOperation{}, err
	}
	settled := operation
	settled.Phase, settled.Receipt, settled.Prepared = StatusSuperseded, receiptID, nil
	changed, err := sender.operations.CompareAndSwapStatus(context.WithoutCancel(ctx), operation.ID, StatusQueued, settled)
	if err != nil || !changed {
		return StatusOperation{}, errors.Join(err, fmt.Errorf("settle stale Telegram card retirement %s", operation.ID))
	}
	return settled, nil
}

func (sender *Sender) releaseSupersededFinal(ctx context.Context, operation StatusOperation) error {
	if operation.Prepared == nil || operation.Prepared.Card.FinalOperationID == "" {
		return nil
	}
	if operation.Prepared.Card.FinalOperationID != operation.ID || operation.Prepared.Card.SessionID == "" {
		return errors.New("superseded final custody identity is invalid")
	}
	return sender.uiState.Update(ctx, func(state *telegramstate.State) error {
		card, exists := state.Card(operation.Prepared.Card.SessionID)
		if !exists {
			return nil
		}
		card.PendingFinalOperations = card.PendingFinalsAfter(operation.ID)
		return state.SetCard(card)
	})
}
