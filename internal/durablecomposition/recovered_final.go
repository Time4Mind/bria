package durablecomposition

import (
	"context"
	"errors"

	"bria/internal/domain"
	"bria/internal/telegramcontroller"
)

// RecoveredFinalRestorer joins proven recovery to the normal output journal.
// Returning success permits reconciliation to complete the accepted input, so
// output custody must follow history persistence and precede that return.
type RecoveredFinalRestorer struct {
	History AcceptedFinalRestorer
	Output  OutputCustody
}

func (r RecoveredFinalRestorer) RestoreAcceptedFinal(ctx context.Context, id domain.SessionID, messageID, final string) error {
	if ctx == nil || r.History == nil || r.Output.Flow == nil || r.Output.OwnerPrivateChatID <= 0 {
		return errors.New("recovered final output dependencies are required")
	}
	if err := r.History.RestoreAcceptedFinal(ctx, id, messageID, final); err != nil {
		return err
	}
	// Use exactly the normal producer's operation, kind and unmodified payload.
	// Repeated recovery after a crash reuses pending/confirmed/unknown custody;
	// it never resets an existing output into a dispatchable state.
	_, err := r.Output.AcceptOutput(ctx, telegramcontroller.OutgoingNotification{
		OperationID: messageID + ":final", ConversationID: r.Output.OwnerPrivateChatID,
		SessionID: id, Kind: telegramcontroller.NotificationFinal, Payload: []byte(final),
	})
	return err
}

var _ AcceptedFinalRestorer = RecoveredFinalRestorer{}
