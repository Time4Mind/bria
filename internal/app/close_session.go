package app

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"bria/internal/domain"
)

type SessionCloseStore interface {
	Load(context.Context, domain.SessionID) (domain.Session, error)
	Replace(context.Context, domain.Session, domain.Session) error
}

type CloseSessionResult struct {
	Session   domain.Session
	Scheduled bool
	Deleted   bool
}

// BeginClose makes the durable close transition without waiting for the
// provider process to acknowledge termination. The remainder of the close is
// completed asynchronously by the closer. It is intended for interactive UI
// callbacks, where waiting on a provider shutdown would block the bot update
// loop.
func (closer *SessionCloser) BeginClose(ctx context.Context, id domain.SessionID) (CloseSessionResult, error) {
	return closer.BeginCloseWithCompletion(ctx, id, nil)
}

// BeginCloseWithCompletion reports asynchronous results after the durable
// lifecycle transition. Synchronous results are returned only, not also reported.
// Completion retains operation values but is independent of callback cancellation.
// It runs outside the closer lock and must not panic.
func (closer *SessionCloser) BeginCloseWithCompletion(ctx context.Context, id domain.SessionID, completed func(context.Context, CloseSessionResult, error)) (CloseSessionResult, error) {
	if ctx == nil || id == "" {
		return CloseSessionResult{}, errors.New("session close context and id are required")
	}
	closer.mu.Lock()
	defer closer.mu.Unlock()
	current, err := closer.store.Load(ctx, id)
	if err != nil {
		return CloseSessionResult{}, fmt.Errorf("load session to close: %w", err)
	}
	_, bound := current.Binding()
	if current.Status() == domain.SessionClosingAfterWork {
		return CloseSessionResult{Session: current, Scheduled: true}, nil
	}
	if !bound || (current.Status() != domain.SessionReady && current.Status() != domain.SessionClosing) {
		return closer.closeCurrent(ctx, current)
	}
	closing := current
	if current.Status() == domain.SessionReady {
		closing, err = current.BeginClose(closer.now().UTC())
		if err == nil {
			err = closer.store.Replace(ctx, current, closing)
		}
	}
	if err != nil {
		return CloseSessionResult{}, fmt.Errorf("begin session close: %w", err)
	}
	closer.completeAsync(ctx, id, completed)
	return CloseSessionResult{Session: closing, Scheduled: true}, nil
}

func (closer *SessionCloser) completeAsync(ctx context.Context, id domain.SessionID, completed func(context.Context, CloseSessionResult, error)) {
	go func() {
		completionContext := context.WithoutCancel(ctx)
		result, err := closer.Close(completionContext, id)
		if completed != nil {
			completed(completionContext, result, err)
		}
	}()
}

type emptyClosingSessionStore interface {
	DeleteEmptyClosing(context.Context, domain.Session) (bool, error)
}

// SessionCloser durably schedules a busy session or confirms the exact
// provider process has exited before it makes the session archival state
// visible.
type SessionCloser struct {
	store   SessionCloseStore
	starter SessionStarter
	now     func() time.Time
	mu      sync.Mutex
}

func NewSessionCloser(store SessionCloseStore, starter SessionStarter, now func() time.Time) (*SessionCloser, error) {
	if store == nil || starter == nil {
		return nil, errors.New("session close dependencies are required")
	}
	if now == nil {
		return nil, errors.New("session close clock is required")
	}
	return &SessionCloser{store: store, starter: starter, now: now}, nil
}

func (closer *SessionCloser) Close(ctx context.Context, id domain.SessionID) (CloseSessionResult, error) {
	if ctx == nil || id == "" {
		return CloseSessionResult{}, errors.New("session close context and id are required")
	}
	closer.mu.Lock()
	defer closer.mu.Unlock()

	current, err := closer.store.Load(ctx, id)
	if err != nil {
		return CloseSessionResult{}, fmt.Errorf("load session to close: %w", err)
	}
	return closer.closeCurrent(ctx, current)
}

// closeCurrent requires mu: deciding and scheduling a busy close is atomic.
func (closer *SessionCloser) closeCurrent(ctx context.Context, current domain.Session) (CloseSessionResult, error) {
	at := closer.now().UTC()
	switch current.Status() {
	case domain.SessionArchived:
		return CloseSessionResult{Session: current}, nil
	case domain.SessionRunning, domain.SessionStopping:
		scheduled, buildErr := current.CloseAfterWork(at)
		if buildErr != nil {
			return CloseSessionResult{}, buildErr
		}
		if err := closer.store.Replace(ctx, current, scheduled); err != nil {
			return CloseSessionResult{}, fmt.Errorf("persist close after work: %w", err)
		}
		return CloseSessionResult{Session: scheduled, Scheduled: true}, nil
	case domain.SessionReady, domain.SessionClosingAfterWork, domain.SessionAwaitingRecovery, domain.SessionClosing:
		return closer.closeNow(ctx, current, at)
	default:
		return CloseSessionResult{}, fmt.Errorf("session %q cannot close from %q", current.ID(), current.Status())
	}
}

func (closer *SessionCloser) closeNow(ctx context.Context, current domain.Session, at time.Time) (CloseSessionResult, error) {
	if _, bound := current.Binding(); !bound {
		target, recovering := current.RecoveryTarget()
		if current.Status() == domain.SessionAwaitingRecovery && recovering && target == domain.SessionStarting {
			// Start errors guarantee adapter exit. Keep the binding-required
			// closing/archive invariants: delete only atomic known-empty evidence.
			if store, ok := closer.store.(interface {
				DeleteEmptyFailedStart(context.Context, domain.Session) (bool, error)
			}); ok {
				deleted, err := store.DeleteEmptyFailedStart(ctx, current)
				if err != nil || deleted {
					return CloseSessionResult{Session: current, Deleted: deleted}, err
				}
			}
		}
		return CloseSessionResult{Session: current}, errors.New("unbound session requires proven empty failed-start evidence for deletion")
	}
	closing := current
	if current.Status() != domain.SessionClosing {
		var err error
		closing, err = current.BeginClose(at)
		if err != nil {
			return CloseSessionResult{}, err
		}
		if err := closer.store.Replace(ctx, current, closing); err != nil {
			return CloseSessionResult{}, fmt.Errorf("persist session closing: %w", err)
		}
	}
	binding, ok := closing.Binding()
	if !ok {
		return CloseSessionResult{Session: closing}, errors.New("closing session has no provider binding")
	}
	request := StartSessionRequest{
		SessionID: closing.ID(), ComputerID: closing.ComputerID(), Provider: closing.Provider(),
		Workdir: closing.Workdir(), Mode: SessionStartResume, PriorBinding: &binding,
	}
	if err := closer.starter.Abort(ctx, request, binding); err != nil {
		// Awaiting-recovery means the prior process crossed the durable exit
		// boundary. After a restart the adapter is normally not tracked locally;
		// treat that exact missing-process result as a confirmed exit so the UI
		// can always close the stale session.
		if current.Status() == domain.SessionAwaitingRecovery && strings.Contains(err.Error(), "not tracked") {
			return closer.finishClose(ctx, closing, at)
		}
		if receipt, ok := closer.store.(interface {
			WasEmptySessionDeleted(context.Context, domain.SessionID, domain.ProviderBinding) (bool, error)
		}); ok {
			deleted, receiptErr := receipt.WasEmptySessionDeleted(ctx, closing.ID(), binding)
			if receiptErr != nil {
				return CloseSessionResult{Session: closing}, errors.Join(err, receiptErr)
			}
			if deleted {
				return CloseSessionResult{Session: closing, Deleted: true}, nil
			}
		}
		awaiting, buildErr := closing.AwaitRecoveryAt(at)
		if buildErr != nil {
			return CloseSessionResult{Session: closing}, errors.Join(err, buildErr)
		}
		if persistErr := closer.store.Replace(ctx, closing, awaiting); persistErr != nil {
			return CloseSessionResult{Session: closing}, errors.Join(
				fmt.Errorf("confirm provider exit: %w", err),
				fmt.Errorf("persist uncertain close: %w", persistErr),
			)
		}
		return CloseSessionResult{Session: awaiting}, fmt.Errorf("confirm provider exit: %w", err)
	}
	return closer.finishClose(ctx, closing, at)
}

func (closer *SessionCloser) finishClose(ctx context.Context, closing domain.Session, at time.Time) (CloseSessionResult, error) {
	if store, ok := closer.store.(emptyClosingSessionStore); ok {
		deleted, err := store.DeleteEmptyClosing(ctx, closing)
		if err != nil {
			return CloseSessionResult{Session: closing}, fmt.Errorf("delete proven empty session after confirmed exit: %w", err)
		}
		if deleted {
			return CloseSessionResult{Session: closing, Deleted: true}, nil
		}
	}
	archived, err := closing.Archive(at)
	if err != nil {
		return CloseSessionResult{Session: closing}, err
	}
	if err := closer.store.Replace(ctx, closing, archived); err != nil {
		return CloseSessionResult{Session: closing}, fmt.Errorf("persist archived session after confirmed exit: %w", err)
	}
	return CloseSessionResult{Session: archived}, nil
}
