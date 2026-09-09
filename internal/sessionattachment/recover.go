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
	if err != nil {
		return result, errors.Join(ErrReconciliationRequired, err)
	}
	if err = ValidateReconciliation(result.Reconciliation); err != nil {
		return result, errors.Join(ErrReconciliationRequired, err)
	}
	active := false
	for _, turn := range result.Reconciliation.Turns {
		active = active || turn.Outcome == AcceptedTurnUnknown
	}
	if active && options.ContinueAcceptedTurns == nil {
		return result, ErrReconciliationRequired
	}
	if active && recovered.Status() == domain.SessionClosing {
		// A detached close did not prove idle. Retain its intent while the exact
		// accepted turn is observed; only terminal completion may close the CLI.
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
		if err = options.ContinueAcceptedTurns(ctx, recovered, prior, result.Reconciliation); err != nil {
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
	blocked, err := recovered.AwaitRecoveryAt(options.Now().UTC())
	if err == nil {
		err = options.Store.Replace(ctx, recovered, blocked)
	}
	if err != nil {
		return options.Conflict(ctx, recovered, errors.Join(cause, err))
	}
	return Result{Session: blocked, AwaitingRecovery: true}, errors.Join(ErrReconciliationRequired, cause)
}
