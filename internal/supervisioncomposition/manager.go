// Package supervisioncomposition owns startup and live supervision for exact local provider bindings.
package supervisioncomposition

import (
	"context"
	"errors"
	"sync"
	"time"

	"bria/internal/app"
	"bria/internal/controllertelemetry"
	"bria/internal/domain"
	"bria/internal/recoverybackoff"
	"bria/internal/sessionattachment"
	"bria/internal/sessionrecoverycontrol"
	"bria/internal/sessionsupervisor"
)

var ErrInvalidOptions = errors.New("session supervision composition is unavailable")

type Store interface {
	sessionsupervisor.Store
	List(context.Context) ([]domain.Session, error)
}

type Options struct {
	LocalComputerID             domain.ComputerID
	Store                       Store
	Waiter                      sessionsupervisor.ProcessWaiter
	Restarter                   sessionsupervisor.Restarter
	InitialRestarter            sessionsupervisor.Restarter
	AcceptedTurns               sessionsupervisor.AcceptedTurnReconciler
	ShouldContinueAcceptedTurns func(context.Context, domain.Session, domain.ProviderBinding, sessionsupervisor.AcceptedTurnReconciliation) (bool, error)
	ContinueAcceptedTurns       func(context.Context, domain.Session, domain.ProviderBinding, sessionsupervisor.AcceptedTurnReconciliation) error
	MaxRestartAttempts          int
	SweepInterval               time.Duration
	WaitBeforeRetry             sessionsupervisor.RetryWaiter
	Now                         func() time.Time
	Report                      func(error)
	ReportSession               func(domain.SessionID, error)
}

type watchedBinding struct {
	identity recoverybackoff.Identity
	cancel   context.CancelFunc
}

type Manager struct {
	computer         domain.ComputerID
	store            Store
	restarter        sessionsupervisor.Restarter
	control          *sessionrecoverycontrol.Control
	startup          *sessionsupervisor.Supervisor
	interval         time.Duration
	recoveryBackoff  *recoverybackoff.Tracker
	report           func(error)
	reportSession    func(domain.SessionID, error)
	mu               sync.Mutex
	observer         controllertelemetry.Observer
	recoveryNotifier func(context.Context, domain.SessionID)
	now              func() time.Time
	workers          map[domain.SessionID]watchedBinding
	wait             sync.WaitGroup
}

func New(options Options) (*Manager, error) {
	if options.LocalComputerID == "" || options.Store == nil || options.Waiter == nil || options.Restarter == nil ||
		options.AcceptedTurns == nil || options.SweepInterval <= 0 || options.Report == nil {
		return nil, ErrInvalidOptions
	}
	supervisorOptions := sessionsupervisor.Options{
		MaxRestartAttempts: options.MaxRestartAttempts, WaitBeforeRetry: options.WaitBeforeRetry,
		Now: options.Now, AcceptedTurns: options.AcceptedTurns,
		ShouldContinueAcceptedTurns: options.ShouldContinueAcceptedTurns,
		ContinueAcceptedTurns:       options.ContinueAcceptedTurns,
	}
	control, err := sessionrecoverycontrol.New(options.Store, options.Waiter, options.Restarter, options.InitialRestarter, supervisorOptions)
	if err != nil {
		return nil, ErrInvalidOptions
	}
	startup, err := sessionsupervisor.New(options.Store, exitedWaiter{}, options.Restarter, supervisorOptions)
	if err != nil {
		return nil, ErrInvalidOptions
	}
	reportSession := options.ReportSession
	if reportSession == nil {
		reportSession = func(_ domain.SessionID, err error) { options.Report(err) }
	}
	return &Manager{
		computer: options.LocalComputerID, store: options.Store, restarter: options.Restarter, control: control, startup: startup,
		interval: options.SweepInterval, recoveryBackoff: recoverybackoff.Default(), now: options.Now, report: options.Report,
		reportSession: reportSession,
		workers:       make(map[domain.SessionID]watchedBinding),
	}, nil
}

// RequireSafeFallback rejects fallback when persisted accepted work may exist.
func RequireSafeFallback(ctx context.Context, store Store) error {
	if ctx == nil || store == nil {
		return ErrInvalidOptions
	}
	return sessionrecoverycontrol.RequireSafeFallback(ctx, store)
}

// RecoverStartup reconciles crash-sensitive sessions before ordinary recovery.
func (manager *Manager) RecoverStartup(ctx context.Context) (app.SessionRecoveryResult, error) {
	if manager == nil || ctx == nil {
		return app.SessionRecoveryResult{}, ErrInvalidOptions
	}
	manager.control.Lock()
	defer manager.control.Unlock()
	sessions, err := manager.store.List(ctx)
	if err != nil {
		return app.SessionRecoveryResult{}, err
	}
	if err := sessionattachment.RecordPersistedExits(manager.computer, sessions, manager.restarter, sessionrecoverycontrol.StartupRecoverable); err != nil {
		return app.SessionRecoveryResult{}, err
	}
	handled := make(map[domain.SessionID]struct{})
	var result app.SessionRecoveryResult
	for _, session := range sessions {
		deferred, initial, deferErr := sessionrecoverycontrol.DeferUnboundInitial(ctx, manager.store, session, manager.computer, manager.now)
		if deferErr != nil {
			if ctx.Err() == nil {
				manager.reportSession(session.ID(), deferErr)
			}
			return result, deferErr
		}
		if session.ComputerID() == manager.computer && initial {
			session = deferred
			handled[session.ID()] = struct{}{}
			result.Awaiting++
			manager.observeRecovery(ctx, session.ID(), controllertelemetry.StartupRecovery, sessionsupervisor.Result{Session: session, AwaitingRecovery: true}, nil)
			continue
		}
		target, _ := session.RecoveryTarget()
		_, bound := session.Binding()
		ready := session.Status() == domain.SessionReady || session.Status() == domain.SessionAwaitingRecovery && target == domain.SessionReady
		attacher, canAttach := manager.restarter.(app.SessionAttacher)
		attach := canAttach && attacher.SupportsAttach(session.Provider()) && bound && sessionrecoverycontrol.StartupRecoverable(session.Status())
		if session.ComputerID() != manager.computer || !sessionrecoverycontrol.HazardousRecovery(session) && !ready && !attach {
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
		manager.observeRecovery(ctx, session.ID(), controllertelemetry.StartupRecovery, recovered, err)
		if err != nil {
			if ctx.Err() == nil {
				manager.reportSession(session.ID(), err)
			}
			if errors.Is(err, sessionsupervisor.ErrReconciliationRequired) || errors.Is(err, sessionsupervisor.ErrRecoveryExhausted) {
				// Empty card history does not prove absent input custody.
				if !errors.Is(err, sessionsupervisor.ErrReconciliationRequired) && (manager.control.DeleteUnrecoverableRecovery(ctx, session) || manager.control.DeleteEmptyRecovery(ctx, session)) {
					result.FinalizedClosing++
				} else {
					result.Awaiting++
				}
				continue
			}
			return result, err
		}
		switch {
		case recovered.Recovered:
			result.Recovered++
			result.Sessions = append(result.Sessions, recovered.Session)
		case recovered.Archived || recovered.Deleted:
			result.FinalizedClosing++
		case recovered.AwaitingRecovery:
			if manager.control.DeleteEmptyRecovery(ctx, recovered.Session) {
				result.FinalizedClosing++
			} else {
				result.Awaiting++
			}
		}
	}
	ordinary, err := app.RecoverPersistedSessionsForComputer(ctx, manager.computer, sessionrecoverycontrol.FilteredStore{Store: manager.store, Excluded: handled}, manager.restarter)
	for _, session := range ordinary.Sessions {
		manager.observeRecovery(ctx, session.ID(), controllertelemetry.StartupRecovery, sessionsupervisor.Result{Session: session, Recovered: true}, nil)
	}
	if current, readErr := manager.store.List(ctx); readErr == nil {
		for _, session := range current {
			_, alreadyObserved := handled[session.ID()]
			if !alreadyObserved && session.ComputerID() == manager.computer && session.Status() == domain.SessionAwaitingRecovery {
				manager.observeRecovery(ctx, session.ID(), controllertelemetry.StartupRecovery, sessionsupervisor.Result{Session: session, AwaitingRecovery: true}, nil)
			}
		}
	}
	result.Recovered += ordinary.Recovered
	result.Awaiting += ordinary.Awaiting
	result.FinalizedClosing += ordinary.FinalizedClosing
	result.SkippedRemote += ordinary.SkippedRemote
	result.Sessions = append(result.Sessions, ordinary.Sessions...)
	return result, err
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
	desired := make(map[domain.SessionID]recoverybackoff.Identity)
	for _, session := range sessions {
		attacher, canAttach := manager.restarter.(app.SessionAttacher)
		retryAttach := canAttach && attacher.SupportsAttach(session.Provider()) && session.Status() == domain.SessionAwaitingRecovery
		target, recovering := session.RecoveryTarget()
		_, bound := session.Binding()
		retryInitial := session.Status() == domain.SessionAwaitingRecovery && recovering && target == domain.SessionStarting && !bound
		if session.ComputerID() != manager.computer || !sessionrecoverycontrol.LiveSupervisable(session.Status()) && !retryAttach && !retryInitial {
			continue
		}
		if binding, bound := session.Binding(); bound {
			desired[session.ID()] = recoverybackoff.Identity{Binding: binding, Lifecycle: session.StateChangedAt()}
		} else if retryInitial {
			desired[session.ID()] = recoverybackoff.Identity{Lifecycle: session.StateChangedAt(), Initial: true}
		}
	}
	manager.mu.Lock()
	defer manager.mu.Unlock()
	now := manager.now()
	for id, watched := range manager.workers {
		if identity, ok := desired[id]; !ok || identity != watched.identity {
			watched.cancel()
			delete(manager.workers, id)
		}
	}
	manager.recoveryBackoff.Retain(desired)
	for id, identity := range desired {
		if _, exists := manager.workers[id]; exists {
			continue
		}
		if !manager.recoveryBackoff.Ready(id, identity, now) {
			continue
		}
		workerCtx, cancel := context.WithCancel(ctx)
		manager.workers[id] = watchedBinding{identity: identity, cancel: cancel}
		manager.wait.Add(1)
		go manager.watch(workerCtx, id, identity)
	}
	return nil
}

func (manager *Manager) watch(ctx context.Context, id domain.SessionID, identity recoverybackoff.Identity) {
	defer manager.wait.Done()
	var result sessionsupervisor.Result
	var err error
	if identity.Initial {
		result, err = manager.control.RecoverUnboundInitial(ctx, manager.computer, id)
	} else {
		result, err = manager.control.Watch(ctx, id, identity.Binding)
	}
	manager.observeRecovery(ctx, id, controllertelemetry.LiveRecovery, result, err)
	shouldReport := err != nil && !errors.Is(err, context.Canceled)
	manager.mu.Lock()
	if current, exists := manager.workers[id]; exists && current.identity == identity {
		delete(manager.workers, id)
	}
	if err != nil && ctx.Err() == nil {
		if result.Session.ID() == id {
			identity.Lifecycle = result.Session.StateChangedAt()
		}
		manager.recoveryBackoff.Failed(id, identity, manager.now())
	} else if result.Recovered || result.Archived || result.Deleted || result.Stale {
		manager.recoveryBackoff.Succeeded(id)
	}
	notify := manager.recoveryNotifier
	manager.mu.Unlock()
	if shouldReport {
		manager.reportSession(id, err)
	}
	if notify != nil && !result.Stale && (result.AwaitingRecovery || result.Recovered || result.Archived || result.Deleted) {
		notifyCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		notify(notifyCtx, id)
	}
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

type exitedWaiter struct{}

func (exitedWaiter) Wait(context.Context, domain.SessionID, domain.ProviderBinding) error { return nil }
