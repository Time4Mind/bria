package telegramflow

import (
	"context"
	"time"

	"bria/internal/coordinator"
	"bria/internal/telegrampipeline"
	"bria/internal/telegramtrace"
)

func (sender *Sender) cardTrace(ctx context.Context, stage, operationID string, prepared Prepared, plan telegrampipeline.CallbackPlan, carrierID int64, started time.Time, err error) {
	if carrierID == 0 {
		carrierID = prepared.Status.SourceMessageID
	}
	sessionID := prepared.Card.SessionID
	if prepared.Surface != nil && prepared.Surface.NativeSessionID != "" {
		sessionID = prepared.Surface.NativeSessionID
	}
	event := telegramtrace.Card(stage, operationID, string(sessionID), prepared.Status.ConversationID, carrierID,
		prepared.Presentation, prepared.Card.Projection.Card.View, prepared.Card.SessionID != "", started, err)
	event.Action, event.Effect, event.UpdateID, event.UpdateKind = plan.Action, plan.Effect, plan.UpdateID, coordinator.UpdateCallback
	sender.trace(ctx, event)
}
