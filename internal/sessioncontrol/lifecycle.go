package sessioncontrol

import (
	"context"
	"time"

	"github.com/Time4Mind/bria/internal/application"
	"github.com/Time4Mind/bria/internal/domain"
	"github.com/Time4Mind/bria/internal/processlog"
	"github.com/Time4Mind/bria/internal/runtimehost"
)

func (c *Controller) Close(
	ctx context.Context,
	actor application.Principal,
	operationID string,
	ref domain.SessionRef,
) (Accepted, error) {
	session, err := c.service.Session(actor, ref)
	if err != nil {
		return Accepted{}, err
	}
	if err := c.service.RequireSessionAction(actor, ref, domain.ActionClose); err != nil {
		return Accepted{}, err
	}
	if session.RuntimePhase != domain.RuntimeStarting &&
		session.RuntimePhase != domain.RuntimeIdle &&
		session.RuntimePhase != domain.RuntimeWaitingInput {
		return Accepted{}, domain.ErrInvalidState
	}
	if !c.sessionHasUserRequest(ctx, actor, session) {
		return c.discardEmptySession(ctx, actor, operationID, session)
	}
	archiveCommitID := "archive-" + operationID
	logInteractionOperation(
		ctx, ref, session.RuntimeGeneration, operationID, string(runtimehost.ActionClose),
	)
	closeCtx := application.WithOperationScope(ctx, operationID+"-archive-commit")
	if err := c.service.CloseSession(closeCtx, actor, session, archiveCommitID); err != nil {
		return Accepted{}, err
	}
	archived, err := c.service.Session(actor, ref)
	if err != nil {
		return Accepted{}, err
	}
	request := runtimehost.Request{
		OperationID: operationID, ActorID: int64(actor.UserID),
		NodeID: string(session.NodeID), SessionID: string(session.ID),
		ExpectedGeneration: session.RuntimeGeneration, Action: runtimehost.ActionClose,
		Backend: session.Backend, ArchiveCommitID: archiveCommitID,
		Archive: &runtimehost.ArchivePayload{
			ArchiveID: archiveCommitID, OwnerID: int64(session.OwnerID), Name: session.Name,
			Workdir: session.Workdir, ProviderSessionID: session.ProviderSessionID,
			CreatedAt: session.CreatedAt, ArchivedAt: archived.ArchivedAt,
		},
	}
	receipt, err := c.runtime.Submit(ctx, request)
	deferred := err != nil
	if err != nil {
		// The archive remains committed. A reconciler can safely retry runtime
		// deactivation with the same operation and archive IDs.
		c.retrySubmit(actor, request, false)
		receipt = retryReceipt(request)
	}
	c.observeArchive(actor, request)
	return Accepted{Session: ref, Receipt: receipt, Deferred: deferred}, nil
}

func (c *Controller) Restore(
	ctx context.Context,
	actor application.Principal,
	operationID string,
	ref domain.SessionRef,
) (Accepted, error) {
	return c.restore(ctx, actor, operationID, ref, "")
}

// RestoreWithProvider restores a legacy archive after the caller has resolved
// its missing provider identity unambiguously on the origin node.
func (c *Controller) RestoreWithProvider(
	ctx context.Context,
	actor application.Principal,
	operationID string,
	ref domain.SessionRef,
	providerID string,
) (Accepted, error) {
	return c.restore(ctx, actor, operationID, ref, providerID)
}

func (c *Controller) restore(
	ctx context.Context,
	actor application.Principal,
	operationID string,
	ref domain.SessionRef,
	providerID string,
) (Accepted, error) {
	startedAt := time.Now()
	lookupDuration := time.Duration(0)
	validateDuration := time.Duration(0)
	restoreDuration := time.Duration(0)
	selectDuration := time.Duration(0)
	generation := uint64(0)
	outcome := "lookup_failed"
	defer func() {
		logRestoreControlTiming(
			ref, generation, outcome, startedAt, lookupDuration, validateDuration,
			restoreDuration, selectDuration,
		)
	}()
	phaseStartedAt := time.Now()
	session, err := c.service.Session(actor, ref)
	lookupDuration = time.Since(phaseStartedAt)
	if err != nil {
		return Accepted{}, err
	}
	phaseStartedAt = time.Now()
	if err := c.service.RequireSessionAction(actor, ref, domain.ActionRestore); err != nil {
		validateDuration = time.Since(phaseStartedAt)
		outcome = "validation_failed"
		return Accepted{}, err
	}
	validateDuration = time.Since(phaseStartedAt)
	generation = session.RuntimeGeneration + 1
	logInteractionOperation(ctx, ref, generation, operationID, "restore")
	restoreCtx := application.WithOperationScope(ctx, operationID+"-restore")
	phaseStartedAt = time.Now()
	if providerID != "" {
		err = c.service.RecoverArchivedSession(restoreCtx, actor, session, providerID)
	} else {
		err = c.service.RestoreSession(restoreCtx, actor, session)
	}
	if err != nil {
		restoreDuration = time.Since(phaseStartedAt)
		outcome = "restore_apply_failed"
		return Accepted{}, err
	}
	restoreDuration = time.Since(phaseStartedAt)
	selectCtx := application.WithOperationScope(ctx, operationID+"-select")
	phaseStartedAt = time.Now()
	if err := c.service.SelectSession(selectCtx, actor, ref); err != nil {
		selectDuration = time.Since(phaseStartedAt)
		outcome = "select_apply_failed"
		return Accepted{}, err
	}
	selectDuration = time.Since(phaseStartedAt)
	outcome = "ok"
	return Accepted{Session: ref, Receipt: runtimehost.Receipt{
		OperationID: operationID, Accepted: true, Detail: "restore queued on origin node",
	}}, nil
}

func logRestoreControlTiming(
	ref domain.SessionRef,
	generation uint64,
	outcome string,
	startedAt time.Time,
	lookup time.Duration,
	validate time.Duration,
	restoreApply time.Duration,
	selectApply time.Duration,
) {
	total := time.Since(startedAt)
	format := "bria restore_timing: stage=control ref=%q generation=%d outcome=%s total_ms=%d lookup_ms=%d validate_ms=%d raft_restore_ms=%d raft_select_ms=%d slow_restore=%t"
	arguments := []any{
		ref.Key(), generation, outcome, total.Milliseconds(), lookup.Milliseconds(),
		validate.Milliseconds(), restoreApply.Milliseconds(), selectApply.Milliseconds(),
		total > time.Second,
	}
	processlog.Outcomef(processlog.Detail, outcome, format, arguments...)
	if total > time.Second {
		processlog.Outcomef(processlog.Service, outcome, format, arguments...)
	}
}
