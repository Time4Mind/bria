package durableflow

import (
	"context"

	"bria/internal/messagejournal"
)

func (flow *Flow) commitRecoveredInput(ctx context.Context, input messagejournal.Input, resolution AcceptedResolution) error {
	_, err := flow.journal.ResolveAcceptedInput(ctx, input.SessionID, input.MessageID, input.Sequence, messagejournal.InputPhase(resolution))
	return err
}
