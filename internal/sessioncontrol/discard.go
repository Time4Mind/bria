package sessioncontrol

import (
	"context"
	"errors"
	"time"

	"github.com/Time4Mind/bria/internal/application"
	"github.com/Time4Mind/bria/internal/domain"
	"github.com/Time4Mind/bria/internal/nodecontrol"
	"github.com/Time4Mind/bria/internal/runtimehost"
	"github.com/Time4Mind/bria/internal/transcript"
)

func (c *Controller) sessionHasUserRequest(
	ctx context.Context,
	actor application.Principal,
	session domain.Session,
) bool {
	if session.UserRequestTracked {
		return session.UserRequestSeen
	}
	// Replicated input intent is authoritative while a first prompt is queued
	// or its transcript has not appeared yet.
	if session.LastOperation != nil && session.LastOperation.Action == domain.ActionSendInput {
		return true
	}
	if session.ProviderSessionID == "" {
		return false
	}
	if c.transcripts == nil {
		// Fail closed: callers without transcript access retain the established
		// archive behavior instead of risking deletion of user work.
		return true
	}
	events, err := c.transcripts.ReadTranscript(ctx, nodecontrol.TranscriptQuery{
		ActorID: int64(actor.UserID), NodeID: string(session.NodeID),
		SessionID: string(session.ID), ExpectedGeneration: session.RuntimeGeneration,
		FirstUserPrompt: true,
	})
	if err != nil {
		return !errors.Is(err, transcript.ErrTranscriptNotFound)
	}
	for _, event := range events {
		if event.Kind == transcript.EventUserText {
			return true
		}
	}
	return false
}

func (c *Controller) discardEmptySession(
	ctx context.Context,
	actor application.Principal,
	operationID string,
	session domain.Session,
) (Accepted, error) {
	logInteractionOperation(
		ctx, session.Ref(), session.RuntimeGeneration, operationID, string(runtimehost.ActionDiscard),
	)
	discardCtx := application.WithOperationScope(ctx, operationID+"-discard")
	if err := c.service.DiscardSession(discardCtx, actor, session); err != nil {
		return Accepted{}, err
	}
	request := runtimehost.Request{
		OperationID: operationID, ActorID: int64(actor.UserID),
		NodeID: string(session.NodeID), SessionID: string(session.ID),
		ExpectedGeneration: session.RuntimeGeneration,
		Action:             runtimehost.ActionDiscard, Backend: session.Backend,
	}
	receipt, err := c.runtime.Submit(ctx, request)
	deferred := err != nil
	if err != nil {
		c.retrySubmit(actor, request, false)
		receipt = retryReceipt(request)
	}
	c.observeDiscard(actor, request)
	return Accepted{Session: session.Ref(), Receipt: receipt, Deferred: deferred}, nil
}

func (c *Controller) observeDiscard(
	actor application.Principal,
	request runtimehost.Request,
) {
	c.workers.Add(1)
	go func() {
		defer c.workers.Done()
		deadline := time.NewTimer(c.resultWait)
		defer deadline.Stop()
		ticker := time.NewTicker(c.pollInterval)
		defer ticker.Stop()
		for {
			select {
			case <-c.ctx.Done():
				return
			case <-deadline.C:
				return
			case <-ticker.C:
				result, found, _ := c.runtime.LookupResult(c.ctx, request)
				if !found {
					continue
				}
				if result.Delivered {
					c.completeDiscard(actor, request)
				}
				return
			}
		}
	}()
}

func (c *Controller) completeDiscard(
	actor application.Principal,
	request runtimehost.Request,
) {
	ref := domain.SessionRef{
		NodeID: domain.NodeID(request.NodeID), SessionID: domain.SessionID(request.SessionID),
	}
	session, err := c.service.Session(actor, ref)
	if err != nil || session.State != domain.SessionDiscarding ||
		session.RuntimeGeneration != request.ExpectedGeneration {
		return
	}
	ctx, cancel := context.WithTimeout(c.ctx, 3*time.Second)
	defer cancel()
	ctx = application.WithOperationScope(ctx, request.OperationID+"-discard-complete")
	_ = c.service.CompleteSessionDiscard(ctx, actor, session)
}
