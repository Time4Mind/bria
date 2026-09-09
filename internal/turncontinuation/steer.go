package turncontinuation

import (
	"context"
	"errors"

	"bria/internal/sessionruntime"
	"bria/internal/turnadmission"
	"bria/internal/turnprocessing"
)

// Steer retains the existing exact-ACK and admission-ticket protocol.
// The controller owns selection of the currently active completion signal.
func Steer(ctx context.Context, submitter sessionruntime.Submitter, input turnprocessing.DurableLeasedInput, text string, callbacks turnprocessing.DurableInputCallbacks, ticket *turnadmission.Ticket, same func() bool, publish func(context.Context, bool), await func()) (turnprocessing.DurableInputProcessReceipt, error) {
	receipt := turnprocessing.DurableInputProcessReceipt{SessionID: input.SessionID, MessageID: input.MessageID, Sequence: input.Sequence}
	steerer, ok := submitter.(sessionruntime.CurrentTurnSubmitter)
	if !ok {
		ticket.Finish(nil)
		publish(ctx, false)
		return receipt, errors.New("provider does not support current-turn input")
	}
	if len(input.Attachments) != 0 {
		ticket.Finish(nil)
		publish(ctx, false)
		return receipt, errors.New("current-turn attachments require turn-scoped custody")
	}
	accepted := false
	err := steerer.SubmitCurrentWithCallbacks(ctx, input.SessionID, sessionruntime.StructuredInput{Text: text}, sessionruntime.TurnCallbacks{
		MessageID: input.MessageID,
		OnAccepted: func(message string) error {
			if !same() {
				return errors.New("current-turn acceptance crossed a turn boundary")
			}
			if accepted || message != input.MessageID {
				return errors.New("provider returned invalid current-turn acceptance")
			}
			accepted = true
			err := callbacks.OnAccepted(ctx, turnprocessing.DurableInputAcceptance{SessionID: input.SessionID, MessageID: input.MessageID, Sequence: input.Sequence})
			publish(context.WithoutCancel(ctx), true)
			return err
		},
	})
	receipt.Accepted = accepted
	if accepted && err != nil {
		receipt.Completion = turnprocessing.DurableInputAwaitingRecovery
		ticket.Finish(err)
		return receipt, err
	}
	if err != nil || !accepted {
		publish(context.WithoutCancel(ctx), false)
		err = errors.Join(err, errors.New("provider did not accept current-turn input"))
		ticket.Finish(err)
		return receipt, err
	}
	receipt.Completion = turnprocessing.DurableInputPending
	await()
	return receipt, nil
}
