package app

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"bria/internal/domain"
)

// SessionRecoveryStore is the durable seam needed to reconnect provider
// adapters after Bria itself has restarted.
type SessionRecoveryStore interface {
	List(context.Context) ([]domain.Session, error)
	Replace(context.Context, domain.Session, domain.Session) error
}

type SessionRecoveryResult struct {
	Recovered        int
	Awaiting         int
	FinalizedClosing int
	SkippedRemote    int
	Sessions         []domain.Session
}

// RecoverPersistedSessions is the single-computer compatibility entry point.
// The current composition root identifies its only computer as "local". New
// multi-computer composition must call RecoverPersistedSessionsForComputer.
func RecoverPersistedSessions(ctx context.Context, store SessionRecoveryStore, starter SessionStarter) (SessionRecoveryResult, error) {
	return RecoverPersistedSessionsForComputer(ctx, "local", store, starter)
}

// RecoverPersistedSessionsForComputer reconnects only sessions owned by the
// named local computer. A prior provider binding is always supplied back to the
// starter and only a handshake for the exact same provider session ID, with a
// newer process generation, is accepted.
func RecoverPersistedSessionsForComputer(
	ctx context.Context,
	localComputerID domain.ComputerID,
	store SessionRecoveryStore,
	starter SessionStarter,
) (SessionRecoveryResult, error) {
	if strings.TrimSpace(string(localComputerID)) == "" {
		return SessionRecoveryResult{}, errors.New("local computer id is required")
	}
	if store == nil || starter == nil {
		return SessionRecoveryResult{}, errors.New("recovery dependencies are required")
	}
	sessions, err := store.List(ctx)
	if err != nil {
		return SessionRecoveryResult{}, fmt.Errorf("list sessions for recovery: %w", err)
	}
	var result SessionRecoveryResult
	for _, session := range sessions {
		if session.ComputerID() != localComputerID {
			result.SkippedRemote++
			continue
		}
		if !recoverableStatus(session.Status()) {
			continue
		}
		if err := ctx.Err(); err != nil {
			return result, err
		}
		request := recoveryRequest(session)
		binding, startErr := starter.Start(ctx, request)
		if startErr != nil {
			next, buildErr := awaitingRecovery(session, lifecycleNow(session))
			if buildErr != nil {
				return result, buildErr
			}
			if !next.Equal(session) {
				if replaceErr := store.Replace(ctx, session, next); replaceErr != nil {
					return result, fmt.Errorf("persist recovery failure for %q: %w", session.ID(), replaceErr)
				}
			}
			result.Awaiting++
			continue
		}
		awaiting, buildErr := awaitingRecovery(session, lifecycleNow(session))
		if buildErr != nil {
			_ = starter.Abort(ctx, request, binding)
			return result, fmt.Errorf("prepare recovered session %q: %w", session.ID(), buildErr)
		}
		next, recoverErr := awaiting.Recovered(binding, lifecycleNow(awaiting))
		if recoverErr != nil {
			if abortErr := starter.Abort(ctx, request, binding); abortErr != nil {
				return result, errors.Join(
					fmt.Errorf("reject mismatched recovery binding for %q: %w", session.ID(), recoverErr),
					fmt.Errorf("abort mismatched recovery for %q: %w", session.ID(), abortErr),
				)
			}
			if !awaiting.Equal(session) {
				if replaceErr := store.Replace(ctx, session, awaiting); replaceErr != nil {
					return result, fmt.Errorf("persist mismatched recovery for %q: %w", session.ID(), replaceErr)
				}
			}
			result.Awaiting++
			continue
		}
		if next.Status() == domain.SessionClosingAfterWork {
			next, recoverErr = next.BeginClose(lifecycleNow(next))
			if recoverErr != nil {
				_ = starter.Abort(ctx, request, binding)
				return result, fmt.Errorf("prepare recovered session %q for closing: %w", session.ID(), recoverErr)
			}
		}
		if next.Status() == domain.SessionClosing {
			if abortErr := starter.Abort(ctx, request, binding); abortErr != nil {
				return result, fmt.Errorf("confirm recovered closing session %q exit: %w", session.ID(), abortErr)
			}
			archived, archiveErr := next.Archive(lifecycleNow(next))
			if archiveErr != nil {
				return result, fmt.Errorf("archive recovered closing session %q: %w", session.ID(), archiveErr)
			}
			if replaceErr := store.Replace(ctx, session, archived); replaceErr != nil {
				return result, fmt.Errorf("persist recovered closing session %q archive: %w", session.ID(), replaceErr)
			}
			result.FinalizedClosing++
			continue
		}
		if err := store.Replace(ctx, session, next); err != nil {
			_ = starter.Abort(ctx, request, binding)
			return result, fmt.Errorf("persist recovered session %q: %w", session.ID(), err)
		}
		result.Recovered++
		result.Sessions = append(result.Sessions, next)
	}
	return result, nil
}

func recoverableStatus(status domain.SessionStatus) bool {
	switch status {
	case domain.SessionStarting, domain.SessionResuming, domain.SessionReady,
		domain.SessionRunning, domain.SessionStopping, domain.SessionClosingAfterWork,
		domain.SessionAwaitingRecovery, domain.SessionClosing:
		return true
	default:
		return false
	}
}

func recoveryRequest(session domain.Session) StartSessionRequest {
	request := StartSessionRequest{
		SessionID: session.ID(), ComputerID: session.ComputerID(),
		Provider: session.Provider(), Workdir: session.Workdir(), Mode: SessionStartNew,
	}
	if binding, ok := session.Binding(); ok {
		request.Mode = SessionStartResume
		request.PriorBinding = &binding
	}
	return request
}

func awaitingRecovery(session domain.Session, at time.Time) (domain.Session, error) {
	if session.Status() == domain.SessionAwaitingRecovery {
		return session, nil
	}
	return session.AwaitRecoveryAt(at)
}

// lifecycleNow tolerates a wall-clock correction without making a persisted
// session permanently unrecoverable. Equal transition timestamps are valid;
// ordering, not wall-clock freshness, is the invariant.
func lifecycleNow(session domain.Session) time.Time {
	now := time.Now().UTC()
	if changed := session.StateChangedAt(); now.Before(changed) {
		return changed
	}
	return now
}
