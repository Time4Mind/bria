// Package durableinputbridge translates turn-processing receipts and callbacks
// into provider-neutral durable custody. It owns no journal or provider state.
package durableinputbridge

import (
	"context"
	"errors"
	"sync"

	"bria/internal/domain"
	"bria/internal/durableflow"
	"bria/internal/messagejournal"
	"bria/internal/turnprocessing"
)

type InputProcessor interface {
	ProcessDurableInput(context.Context, turnprocessing.DurableLeasedInput, turnprocessing.DurableInputCallbacks) (turnprocessing.DurableInputProcessReceipt, error)
}

type Processor struct {
	processor InputProcessor
	notify    []func(domain.SessionID)
}

// New accepts optional nonblocking post-commit completion notifiers.
func New(processor InputProcessor, notify ...func(domain.SessionID)) *Processor {
	return &Processor{processor: processor, notify: append([]func(domain.SessionID){}, notify...)}
}

func (processor *Processor) Process(ctx context.Context, input durableflow.ProviderInput, callbacks durableflow.InputProcessCallbacks) (durableflow.InputProcessResult, error) {
	result := durableflow.InputProcessResult{SessionID: input.SessionID, MessageID: input.MessageID, Sequence: input.Sequence, State: durableflow.InputProcessUnknown}
	if processor == nil || processor.processor == nil || callbacks.OnAccepted == nil {
		return result, durableflow.ErrInvalidHandoff
	}
	attachments := make([]turnprocessing.AttachmentRef, len(input.Attachments))
	for i, ref := range input.Attachments {
		attachments[i] = turnprocessing.AttachmentRef{Reference: ref.Reference, Size: ref.Size, SHA256: ref.SHA256}
	}
	var completedWake sync.Once
	receipt, err := processor.processor.ProcessDurableInput(ctx, turnprocessing.DurableLeasedInput{
		SessionID: domain.SessionID(input.SessionID), MessageID: input.MessageID, Sequence: input.Sequence,
		Payload: append([]byte(nil), input.Payload...), Attachments: attachments,
	}, turnprocessing.DurableInputCallbacks{OnPrepared: func(callbackCtx context.Context, preparation turnprocessing.DurableInputPreparation) error {
		if callbacks.OnPrepared == nil || preparation.SessionID != domain.SessionID(input.SessionID) || preparation.MessageID != input.MessageID || preparation.Sequence != input.Sequence {
			return durableflow.ErrInvalidHandoff
		}
		return callbacks.OnPrepared(callbackCtx, durableflow.ProviderInput{
			SessionID: input.SessionID, MessageID: input.MessageID, Sequence: input.Sequence,
			Payload: append([]byte(nil), preparation.Payload...), Attachments: append([]messagejournal.AttachmentRef(nil), input.Attachments...),
		})
	}, OnAccepted: func(callbackCtx context.Context, acceptance turnprocessing.DurableInputAcceptance) error {
		if acceptance.SessionID != domain.SessionID(input.SessionID) || acceptance.MessageID != input.MessageID || acceptance.Sequence != input.Sequence {
			return durableflow.ErrInvalidHandoff
		}
		return callbacks.OnAccepted(callbackCtx, durableflow.HandoffResult{SessionID: input.SessionID, MessageID: input.MessageID, Sequence: input.Sequence, State: durableflow.HandoffAccepted})
	}, OnCompleted: func(callbackCtx context.Context, receipt turnprocessing.DurableInputProcessReceipt) error {
		completed, err := translate(input, receipt, nil)
		if err != nil || callbacks.OnCompleted == nil || receipt.Completion == turnprocessing.DurableInputPending {
			return durableflow.ErrInvalidHandoff
		}
		if err := callbacks.OnCompleted(callbackCtx, completed); err != nil {
			return err
		}
		if completed.State == durableflow.InputProcessCompleted || completed.State == durableflow.InputProcessTerminalFailed {
			completedWake.Do(func() {
				for _, notify := range processor.notify {
					if notify != nil {
						notify(receipt.SessionID)
					}
				}
			})
		}
		return nil
	}})
	if errors.Is(err, turnprocessing.ErrInputDeferred) && !receipt.Accepted && input.SessionID != "" && input.MessageID != "" && input.Sequence != 0 && receipt.SessionID == domain.SessionID(input.SessionID) && receipt.MessageID == input.MessageID && receipt.Sequence == input.Sequence {
		return durableflow.InputProcessResult{SessionID: input.SessionID, MessageID: input.MessageID, Sequence: input.Sequence, State: durableflow.InputProcessDeferred}, nil
	}
	return translate(input, receipt, err)
}

func translate(input durableflow.ProviderInput, receipt turnprocessing.DurableInputProcessReceipt, processErr error) (durableflow.InputProcessResult, error) {
	result := durableflow.InputProcessResult{SessionID: input.SessionID, MessageID: input.MessageID, Sequence: input.Sequence, State: durableflow.InputProcessUnknown}
	if input.SessionID == "" || input.MessageID == "" || input.Sequence == 0 || receipt.SessionID != domain.SessionID(input.SessionID) || receipt.MessageID != input.MessageID || receipt.Sequence != input.Sequence || !receipt.Accepted {
		return result, errors.Join(processErr, durableflow.ErrInvalidHandoff)
	}
	if processErr != nil && receipt.Completion != turnprocessing.DurableInputAwaitingRecovery && receipt.Completion != turnprocessing.DurableInputPending {
		return result, processErr
	}
	switch receipt.Completion {
	case turnprocessing.DurableInputSucceeded:
		result.State = durableflow.InputProcessCompleted
	case turnprocessing.DurableInputFailed, turnprocessing.DurableInputTerminalFailed, turnprocessing.DurableInputUnknown:
		result.State = durableflow.InputProcessState(receipt.Completion)
	case turnprocessing.DurableInputPending, turnprocessing.DurableInputAwaitingRecovery:
		result.State = durableflow.InputProcessAccepted
	default:
		return result, durableflow.ErrInvalidHandoff
	}
	return result, processErr
}
