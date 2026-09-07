package supervisioncomposition

import (
	"context"

	"bria/internal/app"
	"bria/internal/domain"
)

// recordPersistedExits shares the established startup boundary with both
// supervised recovery and the safe no-history fallback. It records only exact
// local bindings; it never turns a later unknown Abort into a blanket success.
func recordPersistedExits(computer domain.ComputerID, sessions []domain.Session, starter app.SessionStarter) error {
	recorder, ok := starter.(persistedExitRecorder)
	if !ok {
		return nil
	}
	for _, session := range sessions {
		binding, bound := session.Binding()
		if session.ComputerID() != computer || !bound || !startupRecoverable(session.Status()) {
			continue
		}
		request := app.StartSessionRequest{
			SessionID: session.ID(), ComputerID: session.ComputerID(), Provider: session.Provider(), Workdir: session.Workdir(),
			Mode: app.SessionStartResume, PriorBinding: &binding,
		}
		if err := recorder.ConfirmPersistedExit(request, binding); err != nil {
			return err
		}
	}
	return nil
}

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
	if err := recordPersistedExits(computer, sessions, starter); err != nil {
		return app.SessionRecoveryResult{}, err
	}
	return app.RecoverPersistedSessionsForComputer(ctx, computer, store, starter)
}
