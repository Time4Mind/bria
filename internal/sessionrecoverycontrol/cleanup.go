package sessionrecoverycontrol

import (
	"bria/internal/domain"
	"context"
)

func (control *Control) DeleteUnrecoverableRecovery(ctx context.Context, session domain.Session) bool {
	store, ok := control.store.(interface {
		DeleteUnrecoverableAwaitingRecovery(context.Context, domain.Session) (bool, error)
	})
	if !ok {
		return false
	}
	deleted, err := store.DeleteUnrecoverableAwaitingRecovery(ctx, session)
	return err == nil && deleted
}

func (control *Control) DeleteEmptyRecovery(ctx context.Context, session domain.Session) bool {
	if session.ID() == "" || session.Status() != domain.SessionAwaitingRecovery {
		return false
	}
	store, ok := control.store.(interface {
		DeleteEmptyAwaitingRecovery(context.Context, domain.Session) (bool, error)
	})
	if !ok {
		return false
	}
	deleted, err := store.DeleteEmptyAwaitingRecovery(ctx, session)
	return err == nil && deleted
}
