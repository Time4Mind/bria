package acceptedrecovery

import (
	"context"

	"bria/internal/domain"
	"bria/internal/sessionsupervisor"
)

type SessionStore interface {
	Load(context.Context, domain.SessionID) (domain.Session, error)
}

type GuardedArchivedResumer struct {
	Base interface {
		Resume(context.Context, domain.SessionID) (domain.Session, error)
	}
	Sessions   SessionStore
	Reconciler AcceptedTurnReconciler
}

func (resumer GuardedArchivedResumer) Resume(ctx context.Context, id domain.SessionID) (domain.Session, error) {
	if ctx == nil || id == "" || resumer.Base == nil || resumer.Sessions == nil {
		return domain.Session{}, sessionsupervisor.ErrReconciliationRequired
	}
	archived, err := resumer.Sessions.Load(ctx, id)
	if err != nil {
		return domain.Session{}, err
	}
	if archived.ID() != id || archived.Status() != domain.SessionArchived {
		return domain.Session{}, sessionsupervisor.ErrReconciliationRequired
	}
	// Explicit reopening preserves pending acceptance but does not require a
	// terminal receipt. Reconcile any available final before starting; the
	// journal, not opening the CLI, fences replay of previously accepted input.
	// Actual history/persistence errors still prevent an inconsistent resume.
	binding, bound := archived.Binding()
	if !bound {
		return domain.Session{}, sessionsupervisor.ErrReconciliationRequired
	}
	if _, err := resumer.Reconciler.ReconcileAcceptedTurns(ctx, id, binding); err != nil {
		return domain.Session{}, err
	}
	current, err := resumer.Sessions.Load(ctx, id)
	if err != nil {
		return domain.Session{}, err
	}
	if !current.Equal(archived) {
		return domain.Session{}, sessionsupervisor.ErrReconciliationRequired
	}
	return resumer.Base.Resume(ctx, id)
}
