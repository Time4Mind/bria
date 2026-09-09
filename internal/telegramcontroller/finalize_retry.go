package telegramcontroller

import (
	"bria/internal/controllertelemetry"
	"bria/internal/domain"
	"bria/internal/turnfinalization"
	"context"
)

func (c *Controller) retryFinalization(ctx context.Context, id domain.SessionID, binding domain.ProviderBinding, message string, save func(context.Context) error) error {
	return turnfinalization.Retry(ctx, id, binding, c.sessions, save, c.finalizationFailure(ctx, id, message))
}

func (c *Controller) finalizationFailure(ctx context.Context, id domain.SessionID, message string) func(uint64, error) {
	return func(uint64, error) {
		c.closeFlow.Observe(context.WithoutCancel(ctx), controllertelemetry.Event{Stage: controllertelemetry.FinalSave, OperationID: message + ":finalize", ParentOperationID: message, SessionID: string(id), Outcome: controllertelemetry.Failed, Reason: controllertelemetry.PersistFailed})
	}
}
