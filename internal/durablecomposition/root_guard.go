package durablecomposition

import (
	"context"

	"bria/internal/durableflow"
	"bria/internal/turnprocessing"
)

// CheckRootInput is deliberately separate from leasing: accepted main input
// permits live steering, but cannot authorize a fresh root after restart.
func (custody InputCustody) CheckRootInput(ctx context.Context, input turnprocessing.DurableLeasedInput) error {
	if custody.Flow == nil {
		return durableflow.ErrInvalidHandoff
	}
	ready, err := custody.Flow.RootInputReady(ctx, string(input.SessionID), input.MessageID, input.Sequence)
	if err != nil {
		return err
	}
	if !ready {
		return turnprocessing.ErrInputDeferred
	}
	return nil
}

var _ turnprocessing.DurableRootInputGuard = InputCustody{}
