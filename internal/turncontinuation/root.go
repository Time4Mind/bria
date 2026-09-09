package turncontinuation

import (
	"bria/internal/turnadmission"
	"bria/internal/turnprocessing"
	"context"
	"errors"
	"sync"
)

// Root preserves the existing asynchronous acceptance boundary shared with
// continuation: caller cancellation is not a new provider submission.
func Root(ctx, root context.Context, input turnprocessing.DurableLeasedInput, callbacks turnprocessing.DurableInputCallbacks, work *sync.WaitGroup, run func(context.Context, *turnadmission.Admission, func(context.Context) error) (turnprocessing.DurableInputCompletion, bool), publish func(context.Context, bool), complete func(turnprocessing.DurableInputCompletion) error) (turnprocessing.DurableInputProcessReceipt, error) {
	receipt := turnprocessing.DurableInputProcessReceipt{SessionID: input.SessionID, MessageID: input.MessageID, Sequence: input.Sequence}
	acceptedSignal := make(chan struct{}, 1)
	resultSignal := make(chan turnprocessing.DurableInputProcessReceipt, 1)
	var acceptanceErr error // Published through resultSignal, never read at early ACK.
	admission := turnadmission.NewAdmission()
	turnContext, cancel := context.WithCancel(context.WithoutCancel(ctx))
	stop := context.AfterFunc(root, cancel)
	work.Add(1)
	go func() {
		defer work.Done()
		defer stop()
		defer cancel()
		acceptedOnce := false
		outcome, accepted := run(turnContext, admission, func(cbctx context.Context) error {
			if acceptedOnce {
				return errors.New("provider repeated durable acceptance")
			}
			acceptedOnce = true
			acceptanceErr = callbacks.OnAccepted(cbctx, turnprocessing.DurableInputAcceptance{SessionID: input.SessionID, MessageID: input.MessageID, Sequence: input.Sequence})
			publish(context.WithoutCancel(cbctx), true)
			if acceptanceErr != nil {
				return acceptanceErr
			}
			acceptedSignal <- struct{}{}
			return nil
		})
		var commitErr error
		if accepted {
			commitErr = complete(outcome)
		}
		admission.FinishRoot(commitErr)
		resultSignal <- turnprocessing.DurableInputProcessReceipt{SessionID: input.SessionID, MessageID: input.MessageID, Sequence: input.Sequence, Completion: outcome, Accepted: accepted}
	}()
	select {
	case <-acceptedSignal:
		receipt.Accepted = true
		receipt.Completion = turnprocessing.DurableInputPending
		return receipt, nil
	case receipt := <-resultSignal:
		if !receipt.Accepted {
			publish(context.WithoutCancel(ctx), false)
			return receipt, errors.New("provider did not durably accept input")
		}
		return receipt, acceptanceErr
	case <-ctx.Done():
		cancel()
		publish(context.WithoutCancel(ctx), false)
		return receipt, ctx.Err()
	}
}
