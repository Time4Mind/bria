package supervisioncomposition

import (
	"context"

	"bria/internal/app"
	"bria/internal/domain"
	"bria/internal/sessionattachment"
	"bria/internal/sessionrecoverycontrol"
)

// RecoverSafeFallback is the startup path for runtimes without accepted-turn
// history. Crash-sensitive accepted work must remain fenced.
func RecoverSafeFallback(ctx context.Context, computer domain.ComputerID, store Store, starter app.SessionStarter) (app.SessionRecoveryResult, error) {
	if err := RequireSafeFallback(ctx, store); err != nil {
		return app.SessionRecoveryResult{}, err
	}
	sessions, err := store.List(ctx)
	if err != nil {
		return app.SessionRecoveryResult{}, err
	}
	if err := sessionattachment.RecordPersistedExits(computer, sessions, starter, sessionrecoverycontrol.StartupRecoverable); err != nil {
		return app.SessionRecoveryResult{}, err
	}
	return app.RecoverPersistedSessionsForComputer(ctx, computer, store, starter)
}
