package turncontinuation

import (
	"bria/internal/turnprocessing"
	"context"
	"errors"
	"sync"
)

// Callbacks preserves acceptance on observation loss and publishes settlement
// only after the exact terminal receipt was durably committed.
func Callbacks(input turnprocessing.DurableLeasedInput, commit func(context.Context, turnprocessing.DurableInputCompletion) error, settled func()) turnprocessing.DurableInputCallbacks {
	var once sync.Once
	return turnprocessing.DurableInputCallbacks{OnCompleted: func(ctx context.Context, r turnprocessing.DurableInputProcessReceipt) error {
		if !r.Accepted || r.SessionID != input.SessionID || r.MessageID != input.MessageID || r.Sequence != input.Sequence {
			return errors.New("accepted continuation completion tuple changed")
		}
		switch r.Completion {
		case turnprocessing.DurableInputAwaitingRecovery, turnprocessing.DurableInputUnknown:
			return nil
		case turnprocessing.DurableInputSucceeded, turnprocessing.DurableInputTerminalFailed, turnprocessing.DurableInputFailed:
		default:
			return errors.New("accepted continuation completion is invalid")
		}
		if err := commit(context.WithoutCancel(ctx), r.Completion); err != nil {
			return err
		}
		if r.Completion != turnprocessing.DurableInputFailed && settled != nil {
			once.Do(settled)
		}
		return nil
	}}
}
