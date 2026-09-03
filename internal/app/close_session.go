package app

import (
	"context"
	"errors"
	"fmt"
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
	if ctx == nil {
		return CloseSessionResult{}, errors.New("session close context is required")
	}
	if id == "" {
		return CloseSessionResult{}, errors.New("session id is required")
	}
	closer.mu.Lock()
	defer closer.mu.Unlock()

	current, err := closer.store.Load(ctx, id)
	if err != nil {
		return CloseSessionResult{}, fmt.Errorf("load session to close: %w", err)
	}
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
	case domain.SessionReady, domain.SessionClosingAfterWork, domain.SessionClosing:
		return closer.closeNow(ctx, current, at)
	default:
		return CloseSessionResult{}, fmt.Errorf("session %q cannot close from %q", id, current.Status())
	}
}

func (closer *SessionCloser) closeNow(ctx context.Context, current domain.Session, at time.Time) (CloseSessionResult, error) {
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
	archived, err := closing.Archive(at)
	if err != nil {
		return CloseSessionResult{Session: closing}, err
	}
	if err := closer.store.Replace(ctx, closing, archived); err != nil {
		return CloseSessionResult{Session: closing}, fmt.Errorf("persist archived session after confirmed exit: %w", err)
	}
	return CloseSessionResult{Session: archived}, nil
}
