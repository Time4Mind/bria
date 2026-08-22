package sessioncontrol

import (
	"context"
	"errors"
	"fmt"
	"strings"

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
	request := runtimehost.Request{
		OperationID: operationID, ActorID: int64(actor.UserID),
		NodeID: string(session.NodeID), SessionID: string(session.ID),
		ExpectedGeneration: session.RuntimeGeneration, Action: action,
		Text: text, Input: input, Backend: session.Backend,
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
	logInteractionOperation(
		ctx, session.Ref(), request.ExpectedGeneration, request.OperationID, string(request.Action),
	)
	if action == runtimehost.ActionSendInput {
		// Persist the user's request before exposing it to the runtime. A crash
		// between these steps must never let boot recovery classify the session
		// as empty and delete accepted user work.
		activityCtx := application.WithOperationScope(ctx, operationID+"-activity")
		if err := c.service.RecordSessionActivity(activityCtx, actor, session.Ref()); err != nil {
			return Accepted{}, err
		}
	}
	receipt, err := c.runtime.Submit(ctx, request)
	if err != nil {
		if errors.Is(err, runtimehost.ErrQueueFull) {
			if action == runtimehost.ActionSendInput {
				return Accepted{Session: session.Ref()}, domain.ErrQueueFull
			}
			return Accepted{Session: session.Ref()}, ErrRuntimeUnavailable
		}
		c.retrySubmit(actor, request, action == runtimehost.ActionSendInput)
		receipt = retryReceipt(request)
	}
	phase := phaseAfterAccept(action, session.RuntimePhase)
	result := &domain.SessionOperationResult{
		OperationID: operationID, Action: domainAction, Status: domain.OperationQueued,
	}
	if input != nil && input.Kind == runtimehost.InputVoice {
		result.InputKind = "voice"
		result.TranscriptBaselineCount = input.TranscriptBaselineCount
		result.TranscriptBaselineKnown = input.TranscriptBaselineKnown
		result.TranscriptOrdinal = input.TranscriptOrdinal
	}
	stateCtx := application.WithOperationScope(ctx, operationID+"-queued")
	if err := c.service.PublishSessionRuntime(stateCtx, session, phase, result); err != nil {
		return Accepted{}, err
	}
	if action == runtimehost.ActionStop || action == runtimehost.ActionClear ||
		action == runtimehost.ActionSendInput {
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
