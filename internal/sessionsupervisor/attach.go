package sessionsupervisor

import (
	"bria/internal/app"
	"bria/internal/domain"
	"bria/internal/sessionattachment"
	"context"
)

func (s *Supervisor) attach(ctx context.Context, awaiting domain.Session, prior domain.ProviderBinding, attacher app.SessionAttacher) (Result, error) {
	return sessionattachment.Recover(ctx, awaiting, prior, sessionattachment.Options{
		Store: s.store, Attacher: attacher, Abort: s.restarter.Abort, Now: s.now,
		AcceptedTurns: s.reconciler, ShouldContinueAcceptedTurns: s.shouldContinue, ContinueAcceptedTurns: s.continuation,
		Conflict: s.staleAfterConflict, Archive: archiveExited,
	})
}
