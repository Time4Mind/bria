// Package sessionrecoverycontrol serializes exact-binding recovery decisions
// after process waiting, without serializing the waits themselves.
package sessionrecoverycontrol

import (
	"context"
	"errors"

	"bria/internal/app"
	"bria/internal/domain"
	"bria/internal/sessionsupervisor"
)

var ErrInvalidRequest = errors.New("manual session recovery request is invalid")

type Store interface {
	sessionsupervisor.Store
	List(context.Context) ([]domain.Session, error)
}

// The shared lock also fences startup's batch recovery against live/manual work.
type Control struct {
	gate      chan struct{}
	store     Store
	waiter    sessionsupervisor.ProcessWaiter
	restarter sessionsupervisor.Restarter
	options   sessionsupervisor.Options
	manual    *sessionsupervisor.Supervisor
}

func New(store Store, waiter sessionsupervisor.ProcessWaiter, restarter sessionsupervisor.Restarter, options sessionsupervisor.Options) (*Control, error) {
	manual, err := sessionsupervisor.New(store, waiter, restarter, options)
	if err != nil {
		return nil, err
	}
	return &Control{gate: make(chan struct{}, 1), store: store, waiter: waiter, restarter: restarter, options: options, manual: manual}, nil
}

func (c *Control) Lock()   { c.gate <- struct{}{} }
func (c *Control) Unlock() { <-c.gate }

func (c *Control) lock(ctx context.Context) error {
	if ctx == nil {
		return ErrInvalidRequest
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	select {
	case c.gate <- struct{}{}:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (c *Control) Watch(ctx context.Context, id domain.SessionID, binding domain.ProviderBinding) (sessionsupervisor.Result, error) {
	if ctx == nil {
		return sessionsupervisor.Result{}, ErrInvalidRequest
	}
	current, loadErr := c.store.Load(ctx, id)
	if attacher, ok := c.restarter.(app.SessionAttacher); loadErr == nil && ok && attacher.SupportsAttach(current.Provider()) && current.Status() == domain.SessionAwaitingRecovery {
		if err := c.lock(ctx); err != nil {
			return sessionsupervisor.Result{}, err
		}
		defer c.Unlock()
		return c.manual.RecoverPersisted(ctx, id, binding)
	}
	// Retain the actual wait outcome, including uncertain exits. Never substitute
	// a successful exit for a timeout or an unavailable process handle.
	waitErr := c.waiter.Wait(ctx, id, binding)
	if err := c.lock(ctx); err != nil {
		return sessionsupervisor.Result{}, err
	}
	defer c.Unlock()
	if err := ctx.Err(); err != nil {
		return sessionsupervisor.Result{}, err
	}
	supervisor, err := sessionsupervisor.New(c.store, observedExit{waitErr}, c.restarter, c.options)
	if err != nil {
		return sessionsupervisor.Result{}, err
	}
	return supervisor.Watch(ctx, id, binding)
}

func (c *Control) RecoverSession(ctx context.Context, computer domain.ComputerID, id domain.SessionID) (sessionsupervisor.Result, error) {
	if computer == "" || id == "" {
		return sessionsupervisor.Result{}, ErrInvalidRequest
	}
	if err := c.lock(ctx); err != nil {
		return sessionsupervisor.Result{}, err
	}
	defer c.Unlock()
	if err := ctx.Err(); err != nil {
		return sessionsupervisor.Result{}, err
	}
	current, err := c.store.Load(ctx, id)
	if err != nil {
		return sessionsupervisor.Result{}, err
	}
	result := sessionsupervisor.Result{Session: current, AwaitingRecovery: current.Status() == domain.SessionAwaitingRecovery}
	if current.ID() != id || current.ComputerID() != computer {
		return result, ErrInvalidRequest
	}
	if current.Status() == domain.SessionReady {
		result.Stale = true
		return result, nil
	}
	binding, bound := current.Binding()
	if current.Status() != domain.SessionAwaitingRecovery || !bound {
		return result, ErrInvalidRequest
	}
	return c.manual.RecoverPersisted(ctx, id, binding)
}

type observedExit struct{ err error }

func (w observedExit) Wait(context.Context, domain.SessionID, domain.ProviderBinding) error {
	return w.err
}
