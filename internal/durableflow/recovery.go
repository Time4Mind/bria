package durableflow

import (
	"context"
	"time"

	"bria/internal/acceptedinput"
	"bria/internal/messagejournal"
)

// RecoveryInput is the payload-free journal state used to detect whether a
// stable recovery barrier has new evidence.
type RecoveryInput struct {
	MessageID  string
	Sequence   uint64
	Phase      messagejournal.InputPhase
	LeaseOwner string
	LeaseUntil time.Time
}

// RootInputReady checks fresh-root admission without changing live steering leases.
func (flow *Flow) RootInputReady(ctx context.Context, sessionID, messageID string, sequence uint64) (bool, error) {
	if flow == nil {
		return false, ErrInvalidHandoff
	}
	return acceptedinput.RootReady(ctx, flow.journal, sessionID, messageID, sequence)
}

func (flow *Flow) RecoveryInputs(ctx context.Context, sessionID string) ([]RecoveryInput, error) {
	inputs, err := flow.journal.Inputs(ctx, sessionID)
	if err != nil {
		return nil, err
	}
	result := make([]RecoveryInput, len(inputs))
	for index, input := range inputs {
		result[index] = RecoveryInput{MessageID: input.MessageID, Sequence: input.Sequence, Phase: input.Phase, LeaseOwner: input.Lease.Owner, LeaseUntil: input.Lease.Until}
	}
	return result, nil
}
