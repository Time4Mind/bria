package acceptedrecovery

import (
	"context"
	"errors"

	"bria/internal/domain"
	"bria/internal/sessionsupervisor"
)

func (reconciler AcceptedTurnReconciler) CheckResume(ctx context.Context, session domain.Session) error {
	binding, bound := session.Binding()
	if ctx == nil || session.ID() == "" || !bound {
		return sessionsupervisor.ErrReconciliationRequired
	}
	result, err := reconciler.ReconcileAcceptedTurns(ctx, session.ID(), binding)
	if err != nil {
		return errors.Join(sessionsupervisor.ErrReconciliationRequired, err)
	}
	for _, turn := range result.Turns {
		if turn.Outcome != sessionsupervisor.AcceptedTurnCompleted && turn.Outcome != sessionsupervisor.AcceptedTurnFailed {
			return sessionsupervisor.ErrReconciliationRequired
		}
	}
	return nil
}
