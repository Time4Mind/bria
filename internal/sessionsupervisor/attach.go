package sessionsupervisor

import (
	"bria/internal/app"
	"bria/internal/domain"
	"bria/internal/sessionattachment"
	"context"
)

func (s *Supervisor) attach(ctx context.Context, awaiting domain.Session, prior domain.ProviderBinding, attacher app.SessionAttacher, recoveryToken string, recoveryActive bool) (Result, error) {
	var skip, attach func(context.Context) error
	if recoveryActive {
		skip = func(commitContext context.Context) error {
			return s.commitInputRecovery(commitContext, awaiting.ID(), recoveryToken, true)
		}
		attach = func(commitContext context.Context) error {
			return s.commitAttachedInputRecovery(commitContext, awaiting.ID(), recoveryToken, true)
		}
	}
	return sessionattachment.Recover(ctx, awaiting, prior, sessionattachment.Options{
		Store: s.store, Attacher: attacher, Abort: s.restarter.Abort, Now: s.now,
		AcceptedTurns: s.reconciler, ShouldContinueAcceptedTurns: s.shouldContinue, ContinueAcceptedTurns: s.continuation,
		ContinueAcceptedTurnsWithRecovery: s.continuationWithRecovery, CommitInputRecoverySkip: skip, CommitInputRecoveryAttach: attach,
		Conflict: s.staleAfterConflict, Archive: archiveExited,
	})
}
