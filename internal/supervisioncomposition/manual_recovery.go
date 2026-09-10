package supervisioncomposition

import (
	"context"
	"errors"

	"bria/internal/controllertelemetry"
	"bria/internal/domain"
	"bria/internal/sessionrecoverycontrol"
)

// RecoverSession explicitly retries exact binding without submitting a prompt.
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

// SetRecoveryNotifier refreshes committed automatic outcomes outside locks.
func (manager *Manager) SetRecoveryNotifier(notify func(context.Context, domain.SessionID)) {
	manager.mu.Lock()
	manager.recoveryNotifier = notify
	manager.mu.Unlock()
}
