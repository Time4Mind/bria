package supervisioncomposition

import (
	"context"
	"errors"

	"bria/internal/controllertelemetry"
	"bria/internal/domain"
	"bria/internal/sessionrecoverycontrol"
)

// RecoverSession reuses exact-binding reconciliation for an explicit retry.
// It never submits a prompt. Unknown accepted outcomes remain in recovery.
func (manager *Manager) RecoverSession(ctx context.Context, id domain.SessionID) (domain.Session, error) {
	if manager == nil || ctx == nil || id == "" {
		return domain.Session{}, ErrInvalidOptions
	}
	result, err := manager.control.RecoverSession(ctx, manager.computer, id)
	if errors.Is(err, sessionrecoverycontrol.ErrInvalidRequest) {
		err = ErrInvalidOptions
	}
	manager.observeRecovery(ctx, id, controllertelemetry.ManualRecovery, result, err)
	return result.Session, err
}

// SetRecoveryNotifier refreshes only automatic live outcomes after commit.
// It runs outside locks, must honor context, and owns delivery error handling.
func (manager *Manager) SetRecoveryNotifier(notify func(context.Context, domain.SessionID)) {
	manager.mu.Lock()
	manager.recoveryNotifier = notify
	manager.mu.Unlock()
}
