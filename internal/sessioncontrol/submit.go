package sessioncontrol

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/Time4Mind/bria/internal/application"
	"github.com/Time4Mind/bria/internal/domain"
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
		return nil, err
	}
	ticker := time.NewTicker(c.pollInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-ticker.C:
			result, found, err := c.runtime.LookupResult(ctx, request)
			if err != nil {
				return nil, err
			}
			if found {
				if !result.Delivered || len(result.Pane) == 0 {
					return nil, runtimehost.ErrRuntimeUnavailable
				}
				return result.Pane, nil
			}
		}
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
	if session.RuntimePhase != domain.RuntimeIdle &&
		session.RuntimePhase != domain.RuntimeWaitingInput {
		return Accepted{}, domain.ErrInvalidState
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
	session, err := c.service.Session(actor, ref)
	if err != nil {
		return Accepted{}, err
	}
	if err := c.service.RequireSessionAction(actor, ref, domain.ActionRestore); err != nil {
		return Accepted{}, err
	}
	restoreCtx := application.WithOperationScope(ctx, operationID+"-restore")
	if err := c.service.RestoreSession(restoreCtx, actor, session); err != nil {
		return Accepted{}, err
	}
	selectCtx := application.WithOperationScope(ctx, operationID+"-select")
	if err := c.service.SelectSession(selectCtx, actor, ref); err != nil {
		return Accepted{}, err
	}
	return Accepted{Session: ref, Receipt: runtimehost.Receipt{
		OperationID: operationID, Accepted: true, Detail: "restore queued on origin node",
	}}, nil
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
	if action == runtimehost.ActionSendInput {
		activityCtx := application.WithOperationScope(ctx, operationID+"-activity")
		if err := c.service.RecordSessionActivity(activityCtx, actor, session.Ref()); err != nil {
			return Accepted{}, err
		}
	}
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
