package coordinator

import (
	"bria/internal/coordinator/recoverycontrol"
	"context"
	"fmt"
)

type RecoveryControl = recoverycontrol.Control

// UnknownRecoveryHandler may project an exact persisted unknown as a signed
// recovery prompt. It must not retry or confirm the unknown operation.
type UnknownRecoveryHandler interface {
	PrepareUnknownRecovery(context.Context, Update) (RecoveryControl, Decision, error)
}

func (loop *Loop) enqueueRecoveryPrompt(ctx context.Context, stored StoredCheckpoint, update Update, control RecoveryControl, prompt Decision) (StoredCheckpoint, error) {
	if stored.Checkpoint.Recovery != nil {
		return StoredCheckpoint{}, fmt.Errorf("%w for recovery operation %q", ErrDeliveryUnknown, stored.Checkpoint.Recovery.OriginalOperationID)
	}
	durable, ok := loop.sender.(DurableStatusSender)
	if !ok {
		return StoredCheckpoint{}, fmt.Errorf("%w for recovery operation %q", ErrDeliveryUnknown, control.OriginalOperationID)
	}
	next := cloneCheckpoint(stored.Checkpoint)
	next.Recovery = &control
	next.Outbound = &Outbound{OperationID: control.PromptOperationID, UpdateID: update.ID, Status: prompt.Status, Keyboard: cloneKeyboard(prompt.Keyboard), Phase: OutboundPrepared}
	prepared, err := loop.saveVerified(ctx, stored, next)
	if err != nil {
		return StoredCheckpoint{}, err
	}
	receipt, enqueueErr := durable.EnqueueStatus(ctx, control.PromptOperationID, prompt.Status, prompt.Keyboard)
	if enqueueErr != nil || receipt.OperationID != control.PromptOperationID || receipt.Sequence == 0 {
		unknown := cloneCheckpoint(prepared.Checkpoint)
		unknown.Outbound.Phase = OutboundUnknown
		if _, saveErr := loop.saveVerified(context.WithoutCancel(ctx), prepared, unknown); saveErr != nil {
			return StoredCheckpoint{}, saveErr
		}
		return StoredCheckpoint{}, fmt.Errorf("%w for durable recovery operation %q", ErrDeliveryUnknown, control.PromptOperationID)
	}
	enqueued := cloneCheckpoint(prepared.Checkpoint)
	enqueued.NextUpdateID = update.ID + 1
	enqueued.Outbound.Phase = OutboundEnqueued
	enqueued.Outbound.Durable = &DurableOutboundReceipt{OperationID: receipt.OperationID, Sequence: receipt.Sequence}
	return loop.saveVerified(context.WithoutCancel(ctx), prepared, enqueued)
}
