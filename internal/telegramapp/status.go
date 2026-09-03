package telegramapp

import (
	"context"
	"errors"
	"strings"

	"bria/internal/coordinator"
)

const (
	privateConversationKind = "private"
	statusCommand           = "/status"
	statusReadinessText     = "Bria работает и готова принимать команды."
	unsupportedUpdateReason = "authorized update is not supported"
)

// StatusHandler admits the minimal provider-neutral readiness command. Any
// current authorized update it does not understand, including a non-text
// update represented by an empty Text field, is blocked so the coordinator
// does not consume it before a future handler can process it.
type StatusHandler struct {
	ownerUserID        int64
	ownerPrivateChatID int64
}

var _ coordinator.Handler = (*StatusHandler)(nil)

func NewStatusHandler(ownerUserID, ownerPrivateChatID int64) (*StatusHandler, error) {
	if ownerUserID <= 0 || ownerPrivateChatID <= 0 {
		return nil, errors.New("owner user id and owner private chat id must be positive")
	}
	return &StatusHandler{
		ownerUserID:        ownerUserID,
		ownerPrivateChatID: ownerPrivateChatID,
	}, nil
}

func (handler *StatusHandler) Handle(
	_ context.Context,
	update coordinator.Update,
) (coordinator.Decision, error) {
	if update.ActorID != handler.ownerUserID ||
		update.ConversationID != handler.ownerPrivateChatID ||
		update.ConversationKind != privateConversationKind {
		return coordinator.Decision{Kind: coordinator.DecisionSkip}, nil
	}

	if update.Kind == coordinator.UpdateMessage && strings.TrimSpace(update.Text) == statusCommand {
		return coordinator.Decision{
			Kind: coordinator.DecisionStatus,
			Status: coordinator.Status{
				ConversationID: update.ConversationID,
				Text:           statusReadinessText,
			},
		}, nil
	}

	return coordinator.Decision{
		Kind:        coordinator.DecisionBlock,
		BlockReason: unsupportedUpdateReason,
	}, nil
}
