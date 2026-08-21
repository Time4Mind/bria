package sessioncontrol

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/Time4Mind/bria/internal/application"
	"github.com/Time4Mind/bria/internal/domain"
	"github.com/Time4Mind/bria/internal/processlog"
	"github.com/Time4Mind/bria/internal/runtimehost"
)

func (c *Controller) SendInput(
	ctx context.Context,
	actor application.Principal,
	operationID string,
	text string,
) (Accepted, error) {
	if strings.TrimSpace(text) == "" {
		return Accepted{}, fmt.Errorf("input text is required")
	}
	session, err := c.service.ActiveSession(actor)
	if err != nil {
		return Accepted{}, err
	}
	accepted, err := c.submit(ctx, actor, operationID, session, runtimehost.ActionSendInput, text, nil)
	if err == nil && !accepted.Deferred && session.Name == "" && session.OwnerID == actor.UserID {
		accepted.NamingQueued = c.queueNaming(actor, operationID+"-name", session, text)
	}
	return accepted, err
}

func (c *Controller) Stop(
	ctx context.Context,
	actor application.Principal,
	operationID string,
	ref domain.SessionRef,
) (Accepted, error) {
	session, err := c.service.Session(actor, ref)
	if err != nil {
		return Accepted{}, err
	}
	return c.submit(ctx, actor, operationID, session, runtimehost.ActionStop, "", nil)
}

func (c *Controller) Clear(
	ctx context.Context,
	actor application.Principal,
	operationID string,
	ref domain.SessionRef,
) (Accepted, error) {
	session, err := c.service.Session(actor, ref)
	if err != nil {
		return Accepted{}, err
	}
	return c.submit(ctx, actor, operationID, session, runtimehost.ActionClear, "", nil)
}

func (c *Controller) OpenTerminal(
	ctx context.Context,
	actor application.Principal,
	operationID string,
	ref domain.SessionRef,
) (Accepted, error) {
	session, err := c.service.Session(actor, ref)
	if err != nil {
		return Accepted{}, err
	}
	return c.submit(ctx, actor, operationID, session, runtimehost.ActionOpenTerminal, "", nil)
}

func (c *Controller) CapturePane(
	ctx context.Context,
	actor application.Principal,
	operationID string,
	ref domain.SessionRef,
) ([]byte, error) {
	startedAt := time.Now()
	queueDuration := time.Duration(0)
	outcome := "queue_error"
	defer func() {
		processlog.Detailf(
			"bria sessioncontrol: capture_phase ref=%q queue_ms=%d execution_ms=%d outcome=%s",
			ref.Key(), queueDuration.Milliseconds(),
			time.Since(startedAt).Milliseconds()-queueDuration.Milliseconds(), outcome,
		)
	}()
	session, err := c.service.Session(actor, ref)
	if err != nil {
		return nil, err
	}
	if err := c.service.RequireSessionAction(actor, ref, domain.ActionCapture); err != nil {
		return nil, err
	}
	request := runtimehost.Request{
		OperationID: operationID, ActorID: int64(actor.UserID),
		NodeID: string(session.NodeID), SessionID: string(session.ID),
		ExpectedGeneration: session.RuntimeGeneration,
		Action:             runtimehost.ActionCapture, Backend: session.Backend,
	}
	if _, err := c.runtime.Submit(ctx, request); err != nil {
		queueDuration = time.Since(startedAt)
		outcome = captureOutcome(err, true)
		return nil, err
	}
	queueDuration = time.Since(startedAt)
	outcome = "tmux_wait"
	ticker := time.NewTicker(c.pollInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			outcome = captureOutcome(ctx.Err(), false)
			return nil, ctx.Err()
		case <-ticker.C:
			result, found, err := c.runtime.LookupResult(ctx, request)
			if err != nil {
				outcome = captureOutcome(err, false)
				return nil, err
			}
			if found {
				if !result.Delivered || len(result.Pane) == 0 {
					outcome = "target_missing"
					return nil, runtimehost.ErrRuntimeUnavailable
				}
				outcome = "ok"
				return result.Pane, nil
			}
		}
	}
}

func captureOutcome(err error, queue bool) string {
	switch {
	case errors.Is(err, context.DeadlineExceeded) && queue:
		return "queue_timeout"
	case errors.Is(err, context.DeadlineExceeded):
		return "tmux_timeout"
	case errors.Is(err, runtimehost.ErrStaleRuntime):
		return "stale_generation"
	case errors.Is(err, runtimehost.ErrRuntimeUnavailable):
		return "target_missing"
	default:
		return "error"
	}
}

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
	restoreCtx := application.WithOperationScope(ctx, operationID+"-restore")
	phaseStartedAt = time.Now()
	if err := c.service.RestoreSession(restoreCtx, actor, session); err != nil {
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
	processlog.Detailf(format, arguments...)
	if total > time.Second {
		processlog.Servicef(format, arguments...)
	}
}

func (c *Controller) submit(
	ctx context.Context,
	actor application.Principal,
	operationID string,
	session domain.Session,
	action runtimehost.Action,
	text string,
	input *runtimehost.InputPayload,
) (Accepted, error) {
	domainAction, err := mapAction(action)
	if err != nil {
		return Accepted{}, err
	}
	if action == runtimehost.ActionSendInput {
		queue, queueErr := c.service.ShouldQueueInput(actor, session.Ref())
		if queueErr == nil && queue {
			return c.queueDeferredInput(ctx, actor, operationID, session, text, input)
		}
	}
	if err := c.service.RequireSessionAction(actor, session.Ref(), domainAction); err != nil {
		return Accepted{}, err
	}
	if err := requireRuntimePhase(session, action); err != nil {
		return Accepted{}, err
	}
	if action == runtimehost.ActionSendInput {
		// Persist the user's request before exposing it to the runtime. A crash
		// between these steps must never let boot recovery classify the session
		// as empty and delete accepted user work.
		activityCtx := application.WithOperationScope(ctx, operationID+"-activity")
		if err := c.service.RecordSessionActivity(activityCtx, actor, session.Ref()); err != nil {
			return Accepted{}, err
		}
	}
	request := runtimehost.Request{
		OperationID: operationID, ActorID: int64(actor.UserID),
		NodeID: string(session.NodeID), SessionID: string(session.ID),
		ExpectedGeneration: session.RuntimeGeneration, Action: action,
		Text: text, Input: input, Backend: session.Backend,
	}
	receipt, err := c.runtime.Submit(ctx, request)
	if err != nil {
		c.retrySubmit(actor, request, action == runtimehost.ActionSendInput)
		receipt = retryReceipt(request)
	}
	phase := phaseAfterAccept(action, session.RuntimePhase)
	result := &domain.SessionOperationResult{
		OperationID: operationID, Action: domainAction, Status: domain.OperationQueued,
	}
	stateCtx := application.WithOperationScope(ctx, operationID+"-queued")
	if err := c.service.PublishSessionRuntime(stateCtx, session, phase, result); err != nil {
		return Accepted{}, err
	}
	if action == runtimehost.ActionStop || action == runtimehost.ActionClear || input != nil {
		c.observe(actor, request)
	}
	return Accepted{Session: session.Ref(), Receipt: receipt}, nil
}

func retryReceipt(request runtimehost.Request) runtimehost.Receipt {
	return runtimehost.Receipt{
		OperationID: request.OperationID,
		Accepted:    true,
		Detail:      "operation queued for node retry",
	}
}

func requireRuntimePhase(session domain.Session, action runtimehost.Action) error {
	switch action {
	case runtimehost.ActionSendInput:
		if session.RuntimePhase == domain.RuntimeStopping {
			return domain.ErrInvalidState
		}
	case runtimehost.ActionStop:
		if session.RuntimePhase != domain.RuntimeRunning &&
			session.RuntimePhase != domain.RuntimeDegraded {
			return domain.ErrInvalidState
		}
	case runtimehost.ActionClear:
		if session.RuntimePhase == domain.RuntimeStopping {
			return domain.ErrInvalidState
		}
	}
	return nil
}

func mapAction(action runtimehost.Action) (domain.SessionAction, error) {
	switch action {
	case runtimehost.ActionSendInput:
		return domain.ActionSendInput, nil
	case runtimehost.ActionStop:
		return domain.ActionStop, nil
	case runtimehost.ActionClear:
		return domain.ActionClear, nil
	case runtimehost.ActionOpenTerminal:
		return domain.ActionOpenTerminal, nil
	default:
		return "", fmt.Errorf("unsupported session control action %q", action)
	}
}

func phaseAfterAccept(action runtimehost.Action, current domain.RuntimePhase) domain.RuntimePhase {
	// Input accepted while the provider is being provisioned stays queued in the
	// local executor. Keeping the phase makes the startup reconciler responsible
	// for the only transition out of RuntimeStarting.
	if current == domain.RuntimeStarting {
		return current
	}
	switch action {
	case runtimehost.ActionSendInput:
		return domain.RuntimeRunning
	case runtimehost.ActionStop, runtimehost.ActionClear:
		return domain.RuntimeStopping
	default:
		return current
	}
}
