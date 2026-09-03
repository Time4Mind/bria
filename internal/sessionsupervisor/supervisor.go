// Package sessionsupervisor turns one observed provider-process exit into a
// durable, exact-session recovery decision.
package sessionsupervisor

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"bria/internal/app"
	"bria/internal/domain"
)

var (
	ErrRecoveryExhausted      = errors.New("session recovery attempts exhausted")
	ErrReconciliationRequired = errors.New("accepted turn reconciliation required")
	ErrInvalidReconciliation  = errors.New("invalid accepted turn reconciliation")
)

type Store interface {
	Load(context.Context, domain.SessionID) (domain.Session, error)
	Replace(context.Context, domain.Session, domain.Session) error
}

type ProcessWaiter interface {
	Wait(context.Context, domain.SessionID, domain.ProviderBinding) error
}

type Restarter interface {
	Start(context.Context, app.StartSessionRequest) (domain.ProviderBinding, error)
	Abort(context.Context, app.StartSessionRequest, domain.ProviderBinding) error
}

type RetryWaiter func(context.Context, int) error

// AcceptedTurnOutcome is the durable terminal disposition of one input that
// the provider had accepted before its exact process generation exited.
type AcceptedTurnOutcome string

const (
	AcceptedTurnCompleted AcceptedTurnOutcome = "completed"
	AcceptedTurnFailed    AcceptedTurnOutcome = "failed"
	AcceptedTurnUnknown   AcceptedTurnOutcome = "unknown"
)

// ReconciledAcceptedTurn is a durable receipt. Unknown deliberately remains a
// blocking terminal journal state; the supervisor never submits it again.
type ReconciledAcceptedTurn struct {
	MessageID string
	Outcome   AcceptedTurnOutcome
}

type AcceptedTurnReconciliation struct {
	Turns []ReconciledAcceptedTurn
}

// AcceptedTurnReconciler inspects provider-native history for the exact prior
// binding and returns only after every accepted input it found has been
// durably moved to completed, failed, or blocking unknown state. An empty
// receipt proves there were no accepted inputs for this logical session.
type AcceptedTurnReconciler interface {
	ReconcileAcceptedTurns(context.Context, domain.SessionID, domain.ProviderBinding) (AcceptedTurnReconciliation, error)
}

type Options struct {
	MaxRestartAttempts int
	WaitBeforeRetry    RetryWaiter
	Now                func() time.Time
	AcceptedTurns      AcceptedTurnReconciler
}

type Result struct {
	Stale            bool
	AwaitingRecovery bool
	Recovered        bool
	Archived         bool
	RestartAttempts  int
	Session          domain.Session
	Reconciliation   AcceptedTurnReconciliation
}

type Supervisor struct {
	store       Store
	waiter      ProcessWaiter
	restarter   Restarter
	maxAttempts int
	retry       RetryWaiter
	now         func() time.Time
	reconciler  AcceptedTurnReconciler
	recoveryMu  sync.Mutex
}

func New(store Store, waiter ProcessWaiter, restarter Restarter, options Options) (*Supervisor, error) {
	if store == nil || waiter == nil || restarter == nil || options.WaitBeforeRetry == nil || options.Now == nil {
		return nil, errors.New("session supervisor dependencies are required")
	}
	if options.MaxRestartAttempts <= 0 || options.MaxRestartAttempts > 64 {
		return nil, errors.New("max restart attempts must be between 1 and 64")
	}
	return &Supervisor{
		store: store, waiter: waiter, restarter: restarter,
		maxAttempts: options.MaxRestartAttempts, retry: options.WaitBeforeRetry, now: options.Now,
		reconciler: options.AcceptedTurns,
	}, nil
}

func (supervisor *Supervisor) Watch(ctx context.Context, sessionID domain.SessionID, observed domain.ProviderBinding) (Result, error) {
	if ctx == nil {
		return Result{}, errors.New("session supervisor context is required")
	}
	if err := supervisor.waiter.Wait(ctx, sessionID, observed); err != nil {
		if contextErr := ctx.Err(); contextErr != nil {
			return Result{}, contextErr
		}
		current, loadErr := supervisor.store.Load(ctx, sessionID)
		if loadErr == nil && (!hasBinding(current, observed) || !supervisable(current.Status()) || current.Status() == domain.SessionAwaitingRecovery) {
			return Result{Stale: true, AwaitingRecovery: current.Status() == domain.SessionAwaitingRecovery, Session: current}, nil
		}
		if loadErr != nil {
			return Result{}, errors.Join(fmt.Errorf("wait for provider process: %w", err), fmt.Errorf("reread session after wait failure: %w", loadErr))
		}
		return Result{Session: current}, fmt.Errorf("wait for provider process: %w", err)
	}

	current, err := supervisor.store.Load(ctx, sessionID)
	if err != nil {
		return Result{}, fmt.Errorf("load exited session: %w", err)
	}
	if !hasBinding(current, observed) || !supervisable(current.Status()) {
		return Result{Stale: true, Session: current}, nil
	}
	if current.Status() == domain.SessionAwaitingRecovery {
		return Result{Stale: true, AwaitingRecovery: true, Session: current}, nil
	}
	if current.Status() == domain.SessionClosing {
		archived, buildErr := archiveExited(current, supervisor.now().UTC())
		if buildErr != nil {
			return Result{Session: current}, fmt.Errorf("archive exited closing session: %w", buildErr)
		}
		if err := supervisor.store.Replace(ctx, current, archived); err != nil {
			return supervisor.staleAfterConflict(ctx, current, err)
		}
		return Result{Archived: true, Session: archived}, nil
	}

	awaiting, err := current.AwaitRecoveryAt(supervisor.now().UTC())
	if err != nil {
		return Result{Session: current}, fmt.Errorf("prepare session recovery: %w", err)
	}
	if err := supervisor.store.Replace(ctx, current, awaiting); err != nil {
		return supervisor.staleAfterConflict(ctx, current, err)
	}
	result := Result{AwaitingRecovery: true, Session: awaiting}
	if needsAcceptedTurnReconciliation(current.Status()) {
		if supervisor.reconciler == nil {
			return result, ErrReconciliationRequired
		}
		reconciliation, reconcileErr := supervisor.reconciler.ReconcileAcceptedTurns(ctx, current.ID(), observed)
		if reconcileErr != nil {
			return result, errors.Join(ErrReconciliationRequired, fmt.Errorf("reconcile accepted turns: %w", reconcileErr))
		}
		if validateErr := validateReconciliation(reconciliation); validateErr != nil {
			return result, errors.Join(ErrReconciliationRequired, validateErr)
		}
		result.Reconciliation = reconciliation
		for _, turn := range reconciliation.Turns {
			if turn.Outcome == AcceptedTurnUnknown {
				return result, ErrReconciliationRequired
			}
		}
		if current.Status() == domain.SessionClosingAfterWork {
			archived, buildErr := archiveExited(current, supervisor.now().UTC())
			if buildErr != nil {
				return result, fmt.Errorf("archive reconciled closing session: %w", buildErr)
			}
			if replaceErr := supervisor.store.Replace(ctx, awaiting, archived); replaceErr != nil {
				return supervisor.staleAfterConflict(ctx, awaiting, replaceErr)
			}
			result.AwaitingRecovery = false
			result.Archived = true
			result.Session = archived
			return result, nil
		}
	}
	restarted, restartErr := supervisor.restart(ctx, awaiting, observed)
	restarted.Reconciliation = result.Reconciliation
	return restarted, restartErr
}

func (supervisor *Supervisor) restart(ctx context.Context, awaiting domain.Session, prior domain.ProviderBinding) (Result, error) {
	request := app.StartSessionRequest{
		SessionID: awaiting.ID(), ComputerID: awaiting.ComputerID(), Provider: awaiting.Provider(),
		Workdir: awaiting.Workdir(), Mode: app.SessionStartResume, PriorBinding: &prior,
	}
	result := Result{AwaitingRecovery: true, Session: awaiting}
	var failures []error
	for attempt := 1; attempt <= supervisor.maxAttempts; attempt++ {
		if err := ctx.Err(); err != nil {
			return result, err
		}
		result.RestartAttempts = attempt
		binding, startErr := supervisor.restarter.Start(ctx, request)
		if startErr == nil {
			recovered, recoverErr := awaiting.Recovered(binding, supervisor.now().UTC())
			if recoverErr == nil {
				if replaceErr := supervisor.store.Replace(ctx, awaiting, recovered); replaceErr == nil {
					result.AwaitingRecovery = false
					result.Recovered = true
					result.Session = recovered
					return result, nil
				} else {
					abortErr := supervisor.restarter.Abort(ctx, request, binding)
					if abortErr != nil {
						return result, errors.Join(fmt.Errorf("persist recovered session: %w", replaceErr), fmt.Errorf("abort unowned recovered process: %w", abortErr))
					}
					return supervisor.staleAfterConflict(ctx, awaiting, replaceErr)
				}
			}
			abortErr := supervisor.restarter.Abort(ctx, request, binding)
			failures = append(failures, fmt.Errorf("reject replacement recovery binding: %w", recoverErr))
			if abortErr != nil {
				return result, errors.Join(append(failures, fmt.Errorf("abort replacement process: %w", abortErr))...)
			}
		} else {
			failures = append(failures, fmt.Errorf("restart attempt %d: %w", attempt, startErr))
		}
		if attempt < supervisor.maxAttempts {
			if err := supervisor.retry(ctx, attempt); err != nil {
				return result, errors.Join(append(failures, fmt.Errorf("wait before recovery retry: %w", err))...)
			}
		}
	}
	return result, errors.Join(append([]error{ErrRecoveryExhausted}, failures...)...)
}

func (supervisor *Supervisor) staleAfterConflict(ctx context.Context, previous domain.Session, replaceErr error) (Result, error) {
	current, loadErr := supervisor.store.Load(ctx, previous.ID())
	if loadErr != nil {
		return Result{Session: previous}, errors.Join(fmt.Errorf("persist supervised session: %w", replaceErr), fmt.Errorf("reread supervised session: %w", loadErr))
	}
	if current.Equal(previous) {
		return Result{Session: current}, fmt.Errorf("persist supervised session: %w", replaceErr)
	}
	return Result{Stale: true, AwaitingRecovery: current.Status() == domain.SessionAwaitingRecovery, Session: current}, nil
}

func hasBinding(session domain.Session, observed domain.ProviderBinding) bool {
	current, ok := session.Binding()
	return ok && current == observed
}

func supervisable(status domain.SessionStatus) bool {
	switch status {
	case domain.SessionResuming, domain.SessionReady, domain.SessionRunning, domain.SessionStopping,
		domain.SessionClosingAfterWork, domain.SessionAwaitingRecovery, domain.SessionClosing:
		return true
	default:
		return false
	}
}

func needsAcceptedTurnReconciliation(status domain.SessionStatus) bool {
	return status == domain.SessionRunning || status == domain.SessionStopping || status == domain.SessionClosingAfterWork
}

func archiveExited(session domain.Session, at time.Time) (domain.Session, error) {
	var err error
	if session.Status() == domain.SessionClosingAfterWork {
		session, err = session.BeginClose(at)
		if err != nil {
			return domain.Session{}, err
		}
	}
	return session.Archive(at)
}

func validateReconciliation(reconciliation AcceptedTurnReconciliation) error {
	seen := make(map[string]struct{}, len(reconciliation.Turns))
	for _, turn := range reconciliation.Turns {
		if strings.TrimSpace(turn.MessageID) == "" || strings.TrimSpace(turn.MessageID) != turn.MessageID {
			return fmt.Errorf("%w: message id is invalid", ErrInvalidReconciliation)
		}
		if _, exists := seen[turn.MessageID]; exists {
			return fmt.Errorf("%w: duplicate message id %q", ErrInvalidReconciliation, turn.MessageID)
		}
		seen[turn.MessageID] = struct{}{}
		switch turn.Outcome {
		case AcceptedTurnCompleted, AcceptedTurnFailed, AcceptedTurnUnknown:
		default:
			return fmt.Errorf("%w: unsupported outcome %q", ErrInvalidReconciliation, turn.Outcome)
		}
	}
	return nil
}
