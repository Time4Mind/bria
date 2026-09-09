package telegramcontroller

import (
	"bria/internal/domain"
	"bria/internal/turnfinalization"
	"context"
)

func (w *sessionWorker) finishLifecycle(ctx context.Context, message string, bindings ...domain.ProviderBinding) (bool, error) {
	c := w.controller
	lifecycle := turnfinalization.Lifecycle{Turns: c.turnLifecycle, Closer: c.sessionCloser, CloseFlow: &c.closeFlow, Replace: c.replaceLive, Apply: c.applyClosedSession, Notify: w.notifyTurnError}
	if len(bindings) == 1 {
		return lifecycle.Retry(ctx, w.sessionID, bindings[0], message, c.sessions, c.finalizationFailure(ctx, w.sessionID, message))
	}
	return lifecycle.Finish(ctx, w.sessionID, message)
}
