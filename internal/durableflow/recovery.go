package durableflow

import (
	"context"

	"bria/internal/acceptedinput"
	"bria/internal/messagejournal"
)

// RootInputReady checks fresh-root admission without changing live steering leases.
func (flow *Flow) RootInputReady(ctx context.Context, sessionID, messageID string, sequence uint64) (bool, error) {
	if flow == nil {
		return false, ErrInvalidHandoff
	}
	return acceptedinput.RootReady(ctx, flow.journal, sessionID, messageID, sequence)
}

func (flow *Flow) Inputs(ctx context.Context, sessionID string) ([]messagejournal.Input, error) {
	return flow.journal.Inputs(ctx, sessionID)
}
