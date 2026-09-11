// Package sessionsupervisor turns one observed provider-process exit into a
// durable, exact-session recovery decision.
package sessionsupervisor

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"sync"
	"time"

	"bria/internal/app"
	"bria/internal/domain"
	"bria/internal/sessionattachment"
)

var (
	ErrRecoveryExhausted      = errors.New("session recovery attempts exhausted")
	ErrReconciliationRequired = sessionattachment.ErrReconciliationRequired
	ErrInvalidReconciliation  = sessionattachment.ErrInvalidReconciliation
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

type AcceptedTurnOutcome = sessionattachment.AcceptedTurnOutcome

const (
	AcceptedTurnCompleted = sessionattachment.AcceptedTurnCompleted
	AcceptedTurnFailed    = sessionattachment.AcceptedTurnFailed
	AcceptedTurnUnknown   = sessionattachment.AcceptedTurnUnknown
)

type ReconciledAcceptedTurn = sessionattachment.ReconciledAcceptedTurn
type AcceptedTurnReconciliation = sessionattachment.AcceptedTurnReconciliation
type AcceptedTurnReconciler = sessionattachment.AcceptedTurnReconciler

type InputRecovery interface {
	BeginInputRecovery(context.Context, string, string, bool) (bool, error)
	CommitInputRecoverySkip(context.Context, string, string) error
}

type Options struct {
	MaxRestartAttempts                int
	WaitBeforeRetry                   RetryWaiter
	Now                               func() time.Time
	AcceptedTurns                     AcceptedTurnReconciler
	ShouldContinueAcceptedTurns       func(context.Context, domain.Session, domain.ProviderBinding, AcceptedTurnReconciliation) (bool, error)
	ContinueAcceptedTurns             func(context.Context, domain.Session, domain.ProviderBinding, AcceptedTurnReconciliation) error
	ContinueAcceptedTurnsWithRecovery func(context.Context, domain.Session, domain.ProviderBinding, AcceptedTurnReconciliation, func(context.Context) error) error
	InputRecovery                     InputRecovery
	LegacyBlockedRecovery             bool
}

type Result = sessionattachment.Result

type Supervisor struct {
	store                    Store
	waiter                   ProcessWaiter
	restarter                Restarter
	maxAttempts              int
	retry                    RetryWaiter
	now                      func() time.Time
	reconciler               AcceptedTurnReconciler
	shouldContinue           func(context.Context, domain.Session, domain.ProviderBinding, AcceptedTurnReconciliation) (bool, error)
	continuation             func(context.Context, domain.Session, domain.ProviderBinding, AcceptedTurnReconciliation) error
	continuationWithRecovery func(context.Context, domain.Session, domain.ProviderBinding, AcceptedTurnReconciliation, func(context.Context) error) error
	inputRecovery            InputRecovery
	legacyBlockedRecovery    bool
	recoveryMu               sync.Mutex
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
		reconciler:               options.AcceptedTurns,
		shouldContinue:           options.ShouldContinueAcceptedTurns,
		continuation:             options.ContinueAcceptedTurns,
		continuationWithRecovery: options.ContinueAcceptedTurnsWithRecovery,
		inputRecovery:            options.InputRecovery,
		legacyBlockedRecovery:    options.LegacyBlockedRecovery,
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
			if receipt, ok := supervisor.store.(interface {
				WasEmptySessionDeleted(context.Context, domain.SessionID, domain.ProviderBinding) (bool, error)
			}); ok {
				deleted, receiptErr := receipt.WasEmptySessionDeleted(ctx, sessionID, observed)
				if receiptErr != nil {
					return Result{}, receiptErr
				}
				if deleted {
					return Result{Deleted: true, Stale: true}, nil
				}
			}
			return Result{}, errors.Join(fmt.Errorf("wait for provider process: %w", err), fmt.Errorf("reread session after wait failure: %w", loadErr))
		}
		if attacher, ok := supervisor.restarter.(app.SessionAttacher); !ok || !attacher.SupportsAttach(current.Provider()) {
			return Result{Session: current}, fmt.Errorf("wait for provider process: %w", err)
		}
	}

	current, err := supervisor.store.Load(ctx, sessionID)
	if err != nil {
		if receipt, ok := supervisor.store.(interface {
			WasEmptySessionDeleted(context.Context, domain.SessionID, domain.ProviderBinding) (bool, error)
		}); ok {
			deleted, receiptErr := receipt.WasEmptySessionDeleted(ctx, sessionID, observed)
			if receiptErr != nil {
				return Result{}, receiptErr
			}
			if deleted {
				return Result{Deleted: true, Stale: true}, nil
			}
		}
		return Result{}, fmt.Errorf("load exited session: %w", err)
	}
	if !hasBinding(current, observed) || !supervisable(current.Status()) {
		return Result{Stale: true, Session: current}, nil
	}
	if current.Status() == domain.SessionAwaitingRecovery {
		return Result{Stale: true, AwaitingRecovery: true, Session: current}, nil
	}
	attacher, canAttach := supervisor.restarter.(app.SessionAttacher)
	canAttach = canAttach && attacher.SupportsAttach(current.Provider())
	if current.Status() == domain.SessionClosing && !canAttach {
		if store, ok := supervisor.store.(interface {
			DeleteEmptyClosing(context.Context, domain.Session) (bool, error)
		}); ok {
			deleted, err := store.DeleteEmptyClosing(ctx, current)
			if err != nil {
				return supervisor.staleAfterConflict(ctx, current, err)
			}
			if deleted {
				return Result{Deleted: true, Session: current}, nil
			}
		}
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
	recoveryToken, recoveryActive, recoveryErr := supervisor.beginInputRecovery(ctx, awaiting, observed, supervisor.legacyBlockedRecovery)
	if recoveryErr != nil {
		return result, fmt.Errorf("begin input recovery: %w", recoveryErr)
	}
	if canAttach {
		return supervisor.attach(ctx, awaiting, observed, attacher, recoveryToken, recoveryActive)
	}
	if needsAcceptedTurnReconciliation(current.Status()) || supervisor.reconciler != nil && current.Status() == domain.SessionReady {
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
			if commitErr := supervisor.commitInputRecovery(ctx, current.ID(), recoveryToken, recoveryActive); commitErr != nil {
				return result, fmt.Errorf("commit closing input recovery: %w", commitErr)
			}
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
	restarted, restartErr := supervisor.restart(ctx, awaiting, observed, recoveryToken, recoveryActive)
	restarted.Reconciliation = result.Reconciliation
	return restarted, restartErr
}

func (supervisor *Supervisor) restart(ctx context.Context, awaiting domain.Session, prior domain.ProviderBinding, recoveryToken string, recoveryActive bool) (Result, error) {
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
				if commitErr := supervisor.commitInputRecovery(ctx, awaiting.ID(), recoveryToken, recoveryActive); commitErr != nil {
					abortErr := supervisor.restarter.Abort(ctx, request, binding)
					return result, errors.Join(fmt.Errorf("commit recovered input boundary: %w", commitErr), abortErr)
				}
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

func (supervisor *Supervisor) beginInputRecovery(ctx context.Context, awaiting domain.Session, prior domain.ProviderBinding, legacyBlockedOnly bool) (string, bool, error) {
	if supervisor.inputRecovery == nil {
		return "", false, nil
	}
	token := inputRecoveryToken(awaiting, prior)
	active, err := supervisor.inputRecovery.BeginInputRecovery(ctx, string(awaiting.ID()), token, legacyBlockedOnly)
	return token, active, err
}

func (supervisor *Supervisor) commitInputRecovery(ctx context.Context, sessionID domain.SessionID, token string, active bool) error {
	if !active {
		return nil
	}
	if supervisor.inputRecovery == nil || token == "" {
		return errors.New("input recovery boundary is unavailable")
	}
	return supervisor.inputRecovery.CommitInputRecoverySkip(context.WithoutCancel(ctx), string(sessionID), token)
}

func inputRecoveryToken(awaiting domain.Session, prior domain.ProviderBinding) string {
	material := fmt.Sprintf("%s\x00%s\x00%s\x00%d\x00%d", awaiting.ID(), prior.Provider, prior.SessionID, prior.Generation, awaiting.StateChangedAt().UnixNano())
	return fmt.Sprintf("%x", sha256.Sum256([]byte(material)))
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

var validateReconciliation = sessionattachment.ValidateReconciliation
