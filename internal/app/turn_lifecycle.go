package app

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"bria/internal/domain"
)

type SessionTurnStore interface {
	Load(context.Context, domain.SessionID) (domain.Session, error)
	Replace(context.Context, domain.Session, domain.Session) error
}

// SessionTurnLifecycle makes the provider turn state durable instead of
// treating an in-memory worker goroutine as lifecycle truth.
type SessionTurnLifecycle struct {
	store SessionTurnStore
	now   func() time.Time
	mu    sync.Mutex
}

func NewSessionTurnLifecycle(store SessionTurnStore, now func() time.Time) (*SessionTurnLifecycle, error) {
	if store == nil {
		return nil, errors.New("session turn store is required")
	}
	if now == nil {
		return nil, errors.New("session turn clock is required")
	}
	return &SessionTurnLifecycle{store: store, now: now}, nil
}

func (tracker *SessionTurnLifecycle) Start(ctx context.Context, id domain.SessionID) (domain.Session, error) {
	return tracker.transition(ctx, id, func(current domain.Session, at time.Time) (domain.Session, error) {
		return current.StartWork(at)
	})
}

func (tracker *SessionTurnLifecycle) BeginStop(ctx context.Context, id domain.SessionID) (domain.Session, error) {
	return tracker.transition(ctx, id, func(current domain.Session, at time.Time) (domain.Session, error) {
		return current.BeginStop(at)
	})
}

// Finish returns closeAfter=true without changing a scheduled-close session;
// the caller must immediately invoke SessionCloser after the provider turn has
// reached its terminal state.
func (tracker *SessionTurnLifecycle) Finish(ctx context.Context, id domain.SessionID) (session domain.Session, closeAfter bool, err error) {
	if ctx == nil {
		return domain.Session{}, false, errors.New("session turn context is required")
	}
	tracker.mu.Lock()
	defer tracker.mu.Unlock()
	current, err := tracker.store.Load(ctx, id)
	if err != nil {
		return domain.Session{}, false, fmt.Errorf("load session turn: %w", err)
	}
	if current.Status() == domain.SessionClosingAfterWork {
		return current, true, nil
	}
	next, err := current.FinishWork(tracker.now().UTC())
	if err != nil {
		return domain.Session{}, false, err
	}
	if err := tracker.store.Replace(ctx, current, next); err != nil {
		return domain.Session{}, false, fmt.Errorf("persist finished session turn: %w", err)
	}
	return next, false, nil
}

func (tracker *SessionTurnLifecycle) transition(
	ctx context.Context,
	id domain.SessionID,
	build func(domain.Session, time.Time) (domain.Session, error),
) (domain.Session, error) {
	if ctx == nil {
		return domain.Session{}, errors.New("session turn context is required")
	}
	if id == "" {
		return domain.Session{}, errors.New("session id is required")
	}
	tracker.mu.Lock()
	defer tracker.mu.Unlock()
	current, err := tracker.store.Load(ctx, id)
	if err != nil {
		return domain.Session{}, fmt.Errorf("load session turn: %w", err)
	}
	next, err := build(current, tracker.now().UTC())
	if err != nil {
		return domain.Session{}, err
	}
	if err := tracker.store.Replace(ctx, current, next); err != nil {
		return domain.Session{}, fmt.Errorf("persist session turn: %w", err)
	}
	return next, nil
}
