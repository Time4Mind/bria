package durableflow

import (
	"context"

	"bria/internal/acceptedinput"
)

// RootInputReady checks fresh-root admission without changing live steering leases.
func (flow *Flow) RootInputReady(ctx context.Context, sessionID, messageID string, sequence uint64) (bool, error) {
	if flow == nil {
		return false, ErrInvalidHandoff
	}
	return acceptedinput.RootReady(ctx, flow.journal, sessionID, messageID, sequence)
}
