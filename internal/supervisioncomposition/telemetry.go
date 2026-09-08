package supervisioncomposition

import (
	"context"
	"errors"

	"bria/internal/controllertelemetry"
	"bria/internal/domain"
	"bria/internal/sessionsupervisor"
)

// SetObserver is optional and safe before or during supervision. The observer
// contract is non-blocking; callbacks run outside the manager's worker lock.
func (manager *Manager) SetObserver(observer controllertelemetry.Observer) {
	manager.mu.Lock()
	manager.observer = observer
	manager.mu.Unlock()
}

func (manager *Manager) observeRecovery(ctx context.Context, id domain.SessionID, source controllertelemetry.Reason, result sessionsupervisor.Result, err error) {
	manager.mu.Lock()
	observer := manager.observer
	manager.mu.Unlock()
	if observer == nil {
		return
	}
	outcome := controllertelemetry.Failed
	switch {
	case err == nil && result.Recovered:
		outcome = controllertelemetry.Recovered
	case err == nil && result.Archived:
		outcome = controllertelemetry.Archived
	case err == nil && result.Deleted:
		outcome = controllertelemetry.Deleted
	case err == nil && result.Stale:
		outcome = controllertelemetry.Preserved
	case errors.Is(err, sessionsupervisor.ErrRecoveryExhausted):
		outcome = controllertelemetry.RecoveryExhausted
	case result.AwaitingRecovery:
		outcome = controllertelemetry.AwaitingRecovery
	case errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded):
		outcome = controllertelemetry.Skipped
	}
	for _, turn := range result.Reconciliation.Turns {
		if turn.Outcome == sessionsupervisor.AcceptedTurnUnknown {
			outcome = controllertelemetry.RecoveryUnknown
			break
		}
	}
	observer.ObserveControllerEvent(ctx, controllertelemetry.Event{Time: manager.now(), Stage: controllertelemetry.RecoveryOutcome, Reason: source, Outcome: outcome,
		OperationID: controllertelemetry.Operation(ctx), SessionID: string(id), NodeID: string(manager.computer)})
}
