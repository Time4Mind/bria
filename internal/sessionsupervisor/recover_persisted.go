package sessionsupervisor

import (
	"context"
	"errors"
	"fmt"
	"time"

	"bria/internal/domain"
)

// RecoverPersisted continues a recovery whose durable awaiting_recovery
// transition survived the prior Bria process. The caller must first confirm
// that the exact retained provider binding has exited.
func (supervisor *Supervisor) RecoverPersisted(ctx context.Context, sessionID domain.SessionID, prior domain.ProviderBinding) (Result, error) {
	if ctx == nil {
		return Result{}, errors.New("session supervisor context is required")
	}
	if err := ctx.Err(); err != nil {
		return Result{}, err
	}
	supervisor.recoveryMu.Lock()
	defer supervisor.recoveryMu.Unlock()

	current, err := supervisor.store.Load(ctx, sessionID)
	if err != nil {
		return Result{}, fmt.Errorf("load persisted recovery: %w", err)
	}
	result := Result{AwaitingRecovery: current.Status() == domain.SessionAwaitingRecovery, Session: current}
	if current.Status() != domain.SessionAwaitingRecovery || !hasBinding(current, prior) {
		result.Stale = true
		return result, nil
	}
	target, ok := current.RecoveryTarget()
	if !ok || target == domain.SessionStarting {
		return result, errors.New("persisted recovery target is unavailable")
	}
	if needsAcceptedTurnReconciliation(target) {
		if supervisor.reconciler == nil {
			return result, ErrReconciliationRequired
		}
		reconciliation, reconcileErr := supervisor.reconciler.ReconcileAcceptedTurns(ctx, current.ID(), prior)
		if reconcileErr != nil {
			return result, errors.Join(ErrReconciliationRequired, fmt.Errorf("reconcile persisted accepted turns: %w", reconcileErr))
		}
		if validateErr := validateReconciliation(reconciliation); validateErr != nil {
			return result, errors.Join(ErrReconciliationRequired, validateErr)
		}
		result.Reconciliation = reconciliation
		for _, turn := range reconciliation.Turns {
			if turn.Outcome == AcceptedTurnUnknown {
				return result, ErrReconciliationRequired
			}
		}
	}
	if target == domain.SessionClosingAfterWork || target == domain.SessionClosing {
		archived, archiveErr := archivePersistedRecovery(current, supervisor.now().UTC())
		if archiveErr != nil {
			return result, fmt.Errorf("archive persisted closing recovery: %w", archiveErr)
		}
		if replaceErr := supervisor.store.Replace(ctx, current, archived); replaceErr != nil {
			return supervisor.staleAfterConflict(ctx, current, replaceErr)
		}
		result.AwaitingRecovery = false
		result.Archived = true
		result.Session = archived
		return result, nil
	}
	restarted, restartErr := supervisor.restart(ctx, current, prior)
	restarted.Reconciliation = result.Reconciliation
	return restarted, restartErr
}

func archivePersistedRecovery(awaiting domain.Session, at time.Time) (domain.Session, error) {
	target, ok := awaiting.RecoveryTarget()
	if !ok || target != domain.SessionClosing && target != domain.SessionClosingAfterWork {
		return domain.Session{}, errors.New("persisted recovery is not closing")
	}
	if at.Before(awaiting.StateChangedAt()) {
		at = awaiting.StateChangedAt()
	}
	snapshot := awaiting.Snapshot()
	snapshot.Status = domain.SessionClosing
	snapshot.RecoveryTarget = nil
	snapshot.StateChangedAt = at
	closing, err := domain.RestoreSession(snapshot)
	if err != nil {
		return domain.Session{}, err
	}
	return closing.Archive(at)
}
