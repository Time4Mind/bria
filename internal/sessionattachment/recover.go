package sessionattachment

import (
	"bria/internal/domain"
	"bria/internal/providerattachport"
	"context"
	"errors"
	"fmt"
	"time"
)

func Recover(ctx context.Context, awaiting domain.Session, prior domain.ProviderBinding, options Options) (_ Result, returnedErr error) {
	if ctx == nil || options.Store == nil || options.Attacher == nil || options.Now == nil || options.Abort == nil || options.Conflict == nil || options.Archive == nil {
		return Result{}, errors.New("attach recovery dependencies are required")
	}
	result := Result{Session: awaiting, AwaitingRecovery: true}
	request := providerattachport.StartSessionRequest{SessionID: awaiting.ID(), ComputerID: awaiting.ComputerID(), Provider: awaiting.Provider(),
		Workdir: awaiting.Workdir(), Mode: providerattachport.SessionStartResume, PriorBinding: &prior}
	binding, err := options.Attacher.Attach(ctx, request)
	if err != nil {
		if errors.Is(err, providerattachport.ErrTerminalUnavailable) {
			if options.CommitInputRecovery != nil {
				if commitErr := options.CommitInputRecovery(context.WithoutCancel(ctx)); commitErr != nil {
					return result, fmt.Errorf("commit unavailable input recovery: %w", commitErr)
				}
			}
			at := options.Now().UTC()
			closing, closeErr := awaiting.BeginClose(at)
			if closeErr != nil {
				return result, errors.Join(ErrReconciliationRequired, closeErr)
			}
			archived, archiveErr := options.Archive(closing, at)
			if archiveErr != nil {
				return result, archiveErr
			}
			if replaceErr := options.Store.Replace(ctx, awaiting, archived); replaceErr != nil {
				return options.Conflict(ctx, awaiting, replaceErr)
			}
			result.Session, result.AwaitingRecovery, result.Archived = archived, false, true
			return result, nil
		}
		return result, errors.Join(ErrReconciliationRequired, fmt.Errorf("attach existing terminal: %w", err))
	}
	keep := false
	defer func() {
		if !keep {
			// This cleans only the observer; Abort would destroy accepted work.
			cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
			defer cancel()
			if err := options.Attacher.Detach(cleanup, request, binding); err != nil {
				returnedErr = errors.Join(returnedErr, fmt.Errorf("detach uncommitted observer: %w", err))
			}
		}
	}()
	if err := ctx.Err(); err != nil {
		return result, err
	}
	recovered, err := awaiting.Recovered(binding, options.Now().UTC())
	if err != nil {
		return result, errors.Join(ErrReconciliationRequired, err)
	}
	if options.AcceptedTurns == nil {
		return result, ErrReconciliationRequired
	}
	result.Reconciliation, err = options.AcceptedTurns.ReconcileAcceptedTurns(ctx, awaiting.ID(), prior)
	var stableBarrier interface{ StableRecoveryBarrierRevision() string }
	capturedUnknown := options.CommitInputRecovery != nil && errors.As(err, &stableBarrier) && stableBarrier.StableRecoveryBarrierRevision() != ""
	if err != nil && !capturedUnknown {
		return result, errors.Join(ErrReconciliationRequired, err)
	}
	if err = ValidateReconciliation(result.Reconciliation); err != nil {
		return result, errors.Join(ErrReconciliationRequired, err)
	}
	active := false
	for _, turn := range result.Reconciliation.Turns {
		active = active || turn.Outcome == AcceptedTurnUnknown
	}
	if active && options.CommitInputRecovery != nil {
		// A durable recovery cutoff means the user has chosen to abandon every
		// unresolved request captured by the proven total failure, regardless
		// of its former acceptance phase. Do not resurrect or observe it.
		active = false
	}
	if active && options.ShouldContinueAcceptedTurns != nil {
		active, err = options.ShouldContinueAcceptedTurns(ctx, recovered, prior, result.Reconciliation)
		if err != nil {
			return result, errors.Join(ErrReconciliationRequired, fmt.Errorf("select accepted continuation: %w", err))
		}
	}
	if active && options.ContinueAcceptedTurns == nil && options.ContinueAcceptedTurnsWithRecovery == nil {
		return result, ErrReconciliationRequired
	}
	if active && options.CommitInputRecovery != nil && options.ContinueAcceptedTurnsWithRecovery == nil {
		return result, ErrReconciliationRequired
	}
	if active && recovered.Status() == domain.SessionClosing {
		snapshot := recovered.Snapshot()
		snapshot.Status = domain.SessionClosingAfterWork
		recovered, err = domain.RestoreSession(snapshot)
		if err != nil {
			return result, err
		}
	}
	if active && recovered.Status() == domain.SessionReady {
		recovered, err = recovered.StartWork(recovered.StateChangedAt())
		if target, _ := awaiting.RecoveryTarget(); err == nil && target == domain.SessionStopping {
			recovered, err = recovered.BeginStop(recovered.StateChangedAt())
		}
		if err != nil {
			return result, err
		}
	}
	if err := ctx.Err(); err != nil {
		return result, err
	}
	if !active && options.CommitInputRecovery != nil {
		if err = options.CommitInputRecovery(context.WithoutCancel(ctx)); err != nil {
			return result, fmt.Errorf("commit attached input recovery: %w", err)
		}
	}
	if err = options.Store.Replace(ctx, awaiting, recovered); err != nil {
		return options.Conflict(ctx, awaiting, err)
	}
	if !active && (recovered.Status() == domain.SessionClosing || recovered.Status() == domain.SessionClosingAfterWork) {
		if err = options.Abort(ctx, request, binding); err != nil {
			return blockAttached(ctx, options, recovered, err)
		}
		keep = true // The saved close intent has now physically closed this binding.
		archived, archiveErr := options.Archive(recovered, options.Now().UTC())
		if archiveErr != nil {
			return Result{Session: recovered}, archiveErr
		}
		if err = options.Store.Replace(ctx, recovered, archived); err != nil {
			return options.Conflict(ctx, recovered, err)
		}
		result.Session, result.AwaitingRecovery, result.Archived = archived, false, true
		return result, nil
	}
	if active {
		if options.ContinueAcceptedTurnsWithRecovery != nil {
			err = options.ContinueAcceptedTurnsWithRecovery(ctx, recovered, prior, result.Reconciliation, options.CommitInputRecovery)
		} else {
			err = options.ContinueAcceptedTurns(ctx, recovered, prior, result.Reconciliation)
		}
		if err != nil {
			blocked, blockErr := blockAttached(ctx, options, recovered, err)
			blocked.Reconciliation = result.Reconciliation
			return blocked, blockErr
		}
	}
	keep = true
	result.Session, result.AwaitingRecovery, result.Recovered = recovered, false, true
	return result, nil
}

func blockAttached(ctx context.Context, options Options, recovered domain.Session, cause error) (Result, error) {
	custodyCtx := context.WithoutCancel(ctx)
	blocked, err := recovered.AwaitRecoveryAt(options.Now().UTC())
	if err == nil {
		err = options.Store.Replace(custodyCtx, recovered, blocked)
	}
	if err != nil {
		return options.Conflict(custodyCtx, recovered, errors.Join(cause, err))
	}
	return Result{Session: blocked, AwaitingRecovery: true}, errors.Join(ErrReconciliationRequired, cause)
}
