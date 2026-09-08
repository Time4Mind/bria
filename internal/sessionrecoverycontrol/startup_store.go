package sessionrecoverycontrol

import (
	"context"

	"bria/internal/domain"
	"bria/internal/sessionsupervisor"
)

type FilteredStore struct {
	Store
	Excluded map[domain.SessionID]struct{}
}

func RequireSafeFallback(ctx context.Context, store Store) error {
	sessions, err := store.List(ctx)
	if err != nil {
		return err
	}
	for _, session := range sessions {
		if HazardousRecovery(session) {
			return sessionsupervisor.ErrReconciliationRequired
		}
	}
	return nil
}

func (store FilteredStore) DeleteEmptyClosing(ctx context.Context, session domain.Session) (bool, error) {
	if empty, ok := store.Store.(interface {
		DeleteEmptyClosing(context.Context, domain.Session) (bool, error)
	}); ok {
		return empty.DeleteEmptyClosing(ctx, session)
	}
	return false, nil
}

func (store FilteredStore) DeleteEmptyAwaitingRecovery(ctx context.Context, session domain.Session) (bool, error) {
	if empty, ok := store.Store.(interface {
		DeleteEmptyAwaitingRecovery(context.Context, domain.Session) (bool, error)
	}); ok {
		return empty.DeleteEmptyAwaitingRecovery(ctx, session)
	}
	return false, nil
}

func (store FilteredStore) DeleteUnrecoverableAwaitingRecovery(ctx context.Context, session domain.Session) (bool, error) {
	if unrecoverable, ok := store.Store.(interface {
		DeleteUnrecoverableAwaitingRecovery(context.Context, domain.Session) (bool, error)
	}); ok {
		return unrecoverable.DeleteUnrecoverableAwaitingRecovery(ctx, session)
	}
	return false, nil
}

func (store FilteredStore) List(ctx context.Context) ([]domain.Session, error) {
	sessions, err := store.Store.List(ctx)
	if err != nil {
		return nil, err
	}
	filtered := make([]domain.Session, 0, len(sessions))
	for _, session := range sessions {
		if _, excluded := store.Excluded[session.ID()]; !excluded {
			filtered = append(filtered, session)
		}
	}
	return filtered, nil
}

func StartupRecoverable(status domain.SessionStatus) bool {
	switch status {
	case domain.SessionResuming, domain.SessionReady, domain.SessionRunning, domain.SessionStopping,
		domain.SessionClosingAfterWork, domain.SessionAwaitingRecovery, domain.SessionClosing:
		return true
	default:
		return false
	}
}

func HazardousRecovery(session domain.Session) bool {
	status := session.Status()
	if status == domain.SessionRunning || status == domain.SessionStopping || status == domain.SessionClosingAfterWork {
		return true
	}
	if status == domain.SessionAwaitingRecovery {
		target, ok := session.RecoveryTarget()
		return ok && (target == domain.SessionRunning || target == domain.SessionStopping || target == domain.SessionClosingAfterWork)
	}
	return false
}

func LiveSupervisable(status domain.SessionStatus) bool {
	switch status {
	case domain.SessionResuming, domain.SessionReady, domain.SessionRunning, domain.SessionStopping,
		domain.SessionClosingAfterWork, domain.SessionClosing:
		return true
	default:
		return false
	}
}
