// Package initialstartretry owns bounded initial provider startup retries and
// the durable failure transition for a newly created session.
package initialstartretry

import (
	"context"
	"errors"
	"fmt"
	"time"

	"bria/internal/domain"
	"bria/internal/providerattachport"
)

var ErrOutcomeUnknown = errors.New("create session durable outcome is unknown")

var ErrInitialStartExhausted = errors.New("initial provider start attempts exhausted")

const MaxAttempts = 5

const recoveryFinalizationTimeout = 5 * time.Second

type Policy struct {
	MaxAttempts int
	Delay       time.Duration
	OnFailure   func(AttemptFailure)
}

type AttemptFailure struct {
	SessionID  domain.SessionID
	ComputerID domain.ComputerID
	Provider   domain.Provider
	Attempt    int
	Err        error
}

func ValidatePolicy(policy Policy) error {
	if policy.MaxAttempts < 1 {
		return errors.New("initial start retry max attempts must be positive")
	}
	if policy.MaxAttempts > MaxAttempts {
		return fmt.Errorf("initial start retry max attempts must not exceed %d", MaxAttempts)
	}
	if policy.Delay < 0 {
		return errors.New("initial start retry delay must not be negative")
	}
	return nil
}

type CreationStore interface {
	Load(context.Context, domain.SessionID) (domain.Session, error)
	CompareAndSwap(context.Context, domain.Session, domain.Session) error
}

type RecoveryStore interface {
	Load(context.Context, domain.SessionID) (domain.Session, error)
	Replace(context.Context, domain.Session, domain.Session) error
}

type Starter interface {
	Start(context.Context, providerattachport.StartSessionRequest) (domain.ProviderBinding, error)
}

type RecoveryStarter interface {
	Starter
	Abort(context.Context, providerattachport.StartSessionRequest, domain.ProviderBinding) error
}

type CreateSessionResult struct {
	Session       domain.Session
	Replayed      bool
	StartError    error
	StartAttempts int
}

type ExhaustedError struct {
	Result   CreateSessionResult
	Attempts int
}

func (err *ExhaustedError) Error() string {
	return fmt.Sprintf("%s after %d attempts; empty failed-start session deleted", ErrInitialStartExhausted, err.Attempts)
}

func (err *ExhaustedError) Unwrap() error {
	return err.Result.StartError
}

func (err *ExhaustedError) Is(target error) bool {
	return target == ErrInitialStartExhausted
}

type Result struct {
	CreateSessionResult
	Binding   domain.ProviderBinding
	Exhausted bool
}

func Run(
	ctx context.Context,
	starting domain.Session,
	request providerattachport.StartSessionRequest,
	store CreationStore,
	starter Starter,
	policy Policy,
	cleanupEnabled bool,
) (Result, error) {
	var startErrors []error
	for attempt := 1; attempt <= policy.MaxAttempts; attempt++ {
		binding, startErr := starter.Start(ctx, request)
		if startErr == nil {
			return Result{
				CreateSessionResult: CreateSessionResult{Session: starting, StartAttempts: attempt},
				Binding:             binding,
			}, nil
		}
		startErrors = append(startErrors, startErr)
		if policy.OnFailure != nil {
			policy.OnFailure(AttemptFailure{
				SessionID: starting.ID(), ComputerID: starting.ComputerID(), Provider: starting.Provider(), Attempt: attempt, Err: startErr,
			})
		}
		if attempt == policy.MaxAttempts {
			return finishFailure(ctx, starting, store, startErrors, attempt, cleanupEnabled)
		}
		if waitErr := wait(ctx, policy.Delay); waitErr != nil {
			failureCtx, cancelFailure := context.WithTimeout(context.WithoutCancel(ctx), recoveryFinalizationTimeout)
			defer cancelFailure()
			result, persistErr := PersistFailure(
				failureCtx,
				starting,
				joinErrors(append(startErrors, waitErr)),
				store,
			)
			result.StartAttempts = attempt
			if persistErr != nil || !cleanupEnabled {
				return result, errors.Join(waitErr, persistErr)
			}
			_, deleteErr := deleteEmptyFailedStart(failureCtx, result, store)
			return result, errors.Join(waitErr, deleteErr)
		}
		current, loadErr := store.Load(ctx, starting.ID())
		if loadErr != nil {
			return Result{CreateSessionResult: CreateSessionResult{
					Session: starting, StartError: joinErrors(startErrors), StartAttempts: attempt,
				}}, errors.Join(
					ErrOutcomeUnknown,
					fmt.Errorf("reread starting session before retry: %w", loadErr),
				)
		}
		if !current.Equal(starting) {
			return Result{CreateSessionResult: CreateSessionResult{
					Session: current, StartError: joinErrors(startErrors), StartAttempts: attempt,
				}}, errors.Join(
					ErrOutcomeUnknown,
					errors.New("starting session changed before provider retry"),
				)
		}
	}
	panic("validated initial start retry policy made no attempt")
}

func PersistFailure(
	ctx context.Context,
	starting domain.Session,
	startErr error,
	store CreationStore,
) (Result, error) {
	awaitingRecovery, err := starting.AwaitRecovery()
	if err != nil {
		return Result{CreateSessionResult: CreateSessionResult{Session: starting}}, fmt.Errorf("prepare awaiting-recovery session: %w", err)
	}
	casErr := store.CompareAndSwap(ctx, starting, awaitingRecovery)
	persisted, loadErr := store.Load(ctx, starting.ID())
	if loadErr == nil && persisted.Equal(awaitingRecovery) {
		return Result{CreateSessionResult: CreateSessionResult{Session: persisted, StartError: startErr}}, nil
	}
	if loadErr != nil {
		return Result{CreateSessionResult: CreateSessionResult{Session: starting}}, errors.Join(
			ErrOutcomeUnknown,
			fmt.Errorf("reread awaiting-recovery session: %w", loadErr),
			casErr,
		)
	}
	if casErr == nil {
		casErr = errors.New("awaiting-recovery transition was not visible after successful compare-and-swap")
	}
	return Result{CreateSessionResult: CreateSessionResult{Session: persisted}}, errors.Join(
		fmt.Errorf("persist awaiting-recovery session: %w", casErr),
		fmt.Errorf("reread session status %q, want %q", persisted.Status(), domain.SessionAwaitingRecovery),
	)
}

func finishFailure(
	ctx context.Context,
	starting domain.Session,
	store CreationStore,
	startErrors []error,
	attempts int,
	cleanupEnabled bool,
) (Result, error) {
	failureCtx, cancelFailure := context.WithTimeout(context.WithoutCancel(ctx), recoveryFinalizationTimeout)
	defer cancelFailure()
	result, persistErr := PersistFailure(failureCtx, starting, joinErrors(startErrors), store)
	result.StartAttempts = attempts
	if persistErr != nil || !cleanupEnabled {
		return result, persistErr
	}
	deleted, deleteErr := deleteEmptyFailedStart(failureCtx, result, store)
	if deleteErr != nil || !deleted {
		return result, deleteErr
	}
	result.Exhausted = true
	return result, nil
}

func deleteEmptyFailedStart(ctx context.Context, failed Result, store CreationStore) (bool, error) {
	deleter, supported := store.(interface {
		DeleteEmptyFailedStart(context.Context, domain.Session) (bool, error)
	})
	if !supported {
		return false, nil
	}
	deleted, err := deleter.DeleteEmptyFailedStart(ctx, failed.Session)
	if err != nil {
		return false, fmt.Errorf("delete empty failed-start session: %w", err)
	}
	return deleted, nil
}

func joinErrors(errs []error) error {
	if len(errs) == 1 {
		return errs[0]
	}
	return errors.Join(errs...)
}

func wait(ctx context.Context, delay time.Duration) error {
	if delay == 0 {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
			return nil
		}
	}
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

// RecoverUnbound retries an initial start retained because the session already
// has durable user input. It never deletes that session and persists Ready
// before returning the new binding to the caller.
func RecoverUnbound(
	ctx context.Context,
	awaiting domain.Session,
	store RecoveryStore,
	starter RecoveryStarter,
	now func() time.Time,
) (Result, error) {
	target, recovering := awaiting.RecoveryTarget()
	_, bound := awaiting.Binding()
	if awaiting.Status() != domain.SessionAwaitingRecovery || !recovering || target != domain.SessionStarting || bound {
		return recoveryResult(awaiting, nil, 0), errors.New("unbound initial recovery requires awaiting starting session")
	}
	request := providerattachport.StartSessionRequest{
		SessionID: awaiting.ID(), ComputerID: awaiting.ComputerID(), Provider: awaiting.Provider(),
		Workdir: awaiting.Workdir(), Mode: providerattachport.SessionStartNew,
	}
	binding, startErr := starter.Start(ctx, request)
	if startErr != nil {
		return recoveryResult(awaiting, startErr, 1), startErr
	}
	persistenceCtx, cancelPersistence := context.WithTimeout(context.WithoutCancel(ctx), recoveryFinalizationTimeout)
	defer cancelPersistence()
	at := time.Now().UTC()
	if now != nil {
		at = now().UTC()
	}
	if at.Before(awaiting.StateChangedAt()) {
		at = awaiting.StateChangedAt()
	}
	ready, transitionErr := awaiting.Recovered(binding, at)
	if transitionErr != nil {
		return abortUnboundRecovery(persistenceCtx, starter, request, binding, awaiting, transitionErr)
	}
	casErr := store.Replace(persistenceCtx, awaiting, ready)
	persisted, loadErr := store.Load(persistenceCtx, awaiting.ID())
	if loadErr == nil && persisted.Equal(ready) {
		result := recoveryResult(persisted, nil, 1)
		result.Binding = binding
		return result, nil
	}
	failure := errors.Join(ErrOutcomeUnknown, casErr, loadErr)
	if loadErr == nil {
		failure = errors.Join(failure, errors.New("unbound recovery reread a conflicting session state"))
	}
	return abortUnboundRecovery(persistenceCtx, starter, request, binding, awaiting, failure)
}

func abortUnboundRecovery(
	ctx context.Context,
	starter RecoveryStarter,
	request providerattachport.StartSessionRequest,
	binding domain.ProviderBinding,
	awaiting domain.Session,
	failure error,
) (Result, error) {
	abortErr := starter.Abort(ctx, request, binding)
	return recoveryResult(awaiting, failure, 1), errors.Join(failure, abortErr)
}

func recoveryResult(session domain.Session, startErr error, attempts int) Result {
	return Result{CreateSessionResult: CreateSessionResult{Session: session, StartError: startErr, StartAttempts: attempts}}
}
