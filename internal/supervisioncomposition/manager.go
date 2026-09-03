// Package supervisioncomposition owns startup and live process supervision for
// exact local provider bindings.
package supervisioncomposition

import (
	"context"
	"errors"
	"sync"
	"time"

	"bria/internal/app"
	"bria/internal/domain"
	"bria/internal/sessionsupervisor"
)

var ErrInvalidOptions = errors.New("session supervision composition is unavailable")

type Store interface {
	sessionsupervisor.Store
	List(context.Context) ([]domain.Session, error)
}

type Options struct {
	LocalComputerID    domain.ComputerID
	Store              Store
	Waiter             sessionsupervisor.ProcessWaiter
	Restarter          sessionsupervisor.Restarter
	AcceptedTurns      sessionsupervisor.AcceptedTurnReconciler
	MaxRestartAttempts int
	SweepInterval      time.Duration
	WaitBeforeRetry    sessionsupervisor.RetryWaiter
	Now                func() time.Time
	Report             func(error)
}

type watchedBinding struct {
	binding domain.ProviderBinding
	cancel  context.CancelFunc
}

type Manager struct {
	computer  domain.ComputerID
	store     Store
	restarter sessionsupervisor.Restarter
	live      *sessionsupervisor.Supervisor
	startup   *sessionsupervisor.Supervisor
	interval  time.Duration
	report    func(error)

	mu      sync.Mutex
	workers map[domain.SessionID]watchedBinding
	wait    sync.WaitGroup
}

func New(options Options) (*Manager, error) {
	if options.LocalComputerID == "" || options.Store == nil || options.Waiter == nil || options.Restarter == nil ||
		options.AcceptedTurns == nil || options.SweepInterval <= 0 || options.Report == nil {
		return nil, ErrInvalidOptions
	}
	supervisorOptions := sessionsupervisor.Options{
		MaxRestartAttempts: options.MaxRestartAttempts, WaitBeforeRetry: options.WaitBeforeRetry,
		Now: options.Now, AcceptedTurns: options.AcceptedTurns,
	}
	live, err := sessionsupervisor.New(options.Store, options.Waiter, options.Restarter, supervisorOptions)
	if err != nil {
		return nil, ErrInvalidOptions
	}
	startup, err := sessionsupervisor.New(options.Store, exitedWaiter{}, options.Restarter, supervisorOptions)
	if err != nil {
		return nil, ErrInvalidOptions
	}
	return &Manager{
		computer: options.LocalComputerID, store: options.Store, restarter: options.Restarter, live: live, startup: startup,
		interval: options.SweepInterval, report: options.Report, workers: make(map[domain.SessionID]watchedBinding),
	}, nil
}

// RequireSafeFallback permits a runtime without supervision seams only when no
// persisted session can contain an accepted in-flight turn.
func RequireSafeFallback(ctx context.Context, store Store) error {
	if ctx == nil || store == nil {
		return ErrInvalidOptions
	}
	sessions, err := store.List(ctx)
	if err != nil {
		return err
	}
	for _, session := range sessions {
		if hazardousRecovery(session) {
			return sessionsupervisor.ErrReconciliationRequired
		}
	}
	return nil
}

// RecoverStartup reconciles crash-sensitive sessions first, then delegates all
// remaining safe states to the ordinary exact-resume recovery path.
func (manager *Manager) RecoverStartup(ctx context.Context) (app.SessionRecoveryResult, error) {
	if manager == nil || ctx == nil {
		return app.SessionRecoveryResult{}, ErrInvalidOptions
	}
	sessions, err := manager.store.List(ctx)
	if err != nil {
		return app.SessionRecoveryResult{}, err
	}
	handled := make(map[domain.SessionID]struct{})
	var result app.SessionRecoveryResult
	for _, session := range sessions {
		if session.ComputerID() != manager.computer || !hazardousRecovery(session) {
			continue
		}
		binding, bound := session.Binding()
		if !bound {
			return result, ErrInvalidOptions
		}
		handled[session.ID()] = struct{}{}
		var recovered sessionsupervisor.Result
		if session.Status() == domain.SessionAwaitingRecovery {
			recovered, err = manager.startup.RecoverPersisted(ctx, session.ID(), binding)
		} else {
			recovered, err = manager.startup.Watch(ctx, session.ID(), binding)
		}
		if err != nil {
			if errors.Is(err, sessionsupervisor.ErrReconciliationRequired) || errors.Is(err, sessionsupervisor.ErrRecoveryExhausted) {
				manager.report(err)
				result.Awaiting++
				continue
			}
			return result, err
		}
		switch {
		case recovered.Recovered:
			result.Recovered++
			result.Sessions = append(result.Sessions, recovered.Session)
		case recovered.Archived:
			result.FinalizedClosing++
		case recovered.AwaitingRecovery:
			result.Awaiting++
		}
	}
	ordinary, err := app.RecoverPersistedSessionsForComputer(ctx, manager.computer, filteredStore{Store: manager.store, excluded: handled}, manager.restarter)
	result.Recovered += ordinary.Recovered
	result.Awaiting += ordinary.Awaiting
	result.FinalizedClosing += ordinary.FinalizedClosing
	result.SkippedRemote += ordinary.SkippedRemote
	result.Sessions = append(result.Sessions, ordinary.Sessions...)
	return result, err
}

type filteredStore struct {
	Store
	excluded map[domain.SessionID]struct{}
}

func (store filteredStore) List(ctx context.Context) ([]domain.Session, error) {
	sessions, err := store.Store.List(ctx)
	if err != nil {
		return nil, err
	}
	filtered := make([]domain.Session, 0, len(sessions))
	for _, session := range sessions {
		if _, excluded := store.excluded[session.ID()]; !excluded {
			filtered = append(filtered, session)
		}
	}
	return filtered, nil
}

func (manager *Manager) Run(ctx context.Context) error {
	if manager == nil || ctx == nil {
		return ErrInvalidOptions
	}
	sweep := func() {
		if err := manager.sweep(ctx); err != nil && !errors.Is(err, context.Canceled) {
			manager.report(err)
		}
	}
	sweep()
	ticker := time.NewTicker(manager.interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			manager.stopWorkers()
			return ctx.Err()
		case <-ticker.C:
			sweep()
		}
	}
}

func (manager *Manager) sweep(ctx context.Context) error {
	sessions, err := manager.store.List(ctx)
	if err != nil {
		return err
	}
	desired := make(map[domain.SessionID]domain.ProviderBinding)
	for _, session := range sessions {
		if session.ComputerID() != manager.computer || !liveSupervisable(session.Status()) {
			continue
		}
		if binding, bound := session.Binding(); bound {
			desired[session.ID()] = binding
		}
	}
	manager.mu.Lock()
	defer manager.mu.Unlock()
	for id, watched := range manager.workers {
		if binding, ok := desired[id]; !ok || binding != watched.binding {
			watched.cancel()
			delete(manager.workers, id)
		}
	}
	for id, binding := range desired {
		if _, exists := manager.workers[id]; exists {
			continue
		}
		workerCtx, cancel := context.WithCancel(ctx)
		manager.workers[id] = watchedBinding{binding: binding, cancel: cancel}
		manager.wait.Add(1)
		go manager.watch(workerCtx, id, binding)
	}
	return nil
}

func (manager *Manager) watch(ctx context.Context, id domain.SessionID, binding domain.ProviderBinding) {
	defer manager.wait.Done()
	_, err := manager.live.Watch(ctx, id, binding)
	if err != nil && !errors.Is(err, context.Canceled) {
		manager.report(err)
	}
	manager.mu.Lock()
	if current, exists := manager.workers[id]; exists && current.binding == binding {
		delete(manager.workers, id)
	}
	manager.mu.Unlock()
}

func (manager *Manager) stopWorkers() {
	manager.mu.Lock()
	for id, worker := range manager.workers {
		worker.cancel()
		delete(manager.workers, id)
	}
	manager.mu.Unlock()
	manager.wait.Wait()
}

func hazardousRecovery(session domain.Session) bool {
	status := session.Status()
	if status == domain.SessionRunning || status == domain.SessionStopping || status == domain.SessionClosingAfterWork {
		return true
	}
	if status == domain.SessionAwaitingRecovery {
		target, ok := session.RecoveryTarget()
		return ok && (target == domain.SessionRunning || target == domain.SessionStopping || target == domain.SessionClosingAfterWork)
	}
	return false
}

func liveSupervisable(status domain.SessionStatus) bool {
	switch status {
	case domain.SessionResuming, domain.SessionReady, domain.SessionRunning, domain.SessionStopping,
		domain.SessionClosingAfterWork, domain.SessionClosing:
		return true
	default:
		return false
	}
}

type exitedWaiter struct{}

func (exitedWaiter) Wait(context.Context, domain.SessionID, domain.ProviderBinding) error { return nil }
