package sessioncontrol

import (
	"context"
	"errors"
	"time"

	"github.com/Time4Mind/bria/internal/application"
	"github.com/Time4Mind/bria/internal/domain"
	"github.com/Time4Mind/bria/internal/runtimehost"
)

func (c *Controller) retrySubmit(
	actor application.Principal,
	request runtimehost.Request,
	reportFailure bool,
) {
	c.workers.Add(1)
	go func() {
		defer c.workers.Done()
		deadline := time.NewTimer(c.resultWait)
		defer deadline.Stop()
		ticker := time.NewTicker(c.retryInterval)
		defer ticker.Stop()
		for {
			select {
			case <-c.ctx.Done():
				return
			case <-deadline.C:
				if reportFailure {
					c.applyResult(
						actor, request,
						runtimehost.Result{Accepted: true, Detail: "origin node unavailable"},
						errors.New("origin node did not acknowledge operation"),
					)
				}
				return
			case <-ticker.C:
				ctx, cancel := context.WithTimeout(c.ctx, c.retryInterval)
				_, err := c.runtime.Submit(ctx, request)
				cancel()
				if err == nil {
					return
				}
			}
		}
	}()
}

func (c *Controller) observe(actor application.Principal, request runtimehost.Request) {
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
				result, found, err := c.runtime.LookupResult(c.ctx, request)
				if !found {
					continue
				}
				c.applyResult(actor, request, result, err)
				return
			}
		}
	}()
}

func (c *Controller) observeArchive(actor application.Principal, request runtimehost.Request) {
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
				if result.ArchiveCommitted && result.Delivered {
					c.completeArchive(actor, request)
				}
				return
			}
		}
	}()
}

func (c *Controller) completeArchive(
	actor application.Principal,
	request runtimehost.Request,
) {
	ref := domain.SessionRef{
		NodeID: domain.NodeID(request.NodeID), SessionID: domain.SessionID(request.SessionID),
	}
	session, err := c.service.Session(actor, ref)
	if err != nil || session.ArchiveID != request.ArchiveCommitID || session.ArchiveReady {
		return
	}
	ctx, cancel := context.WithTimeout(c.ctx, 3*time.Second)
	defer cancel()
	ctx = application.WithOperationScope(ctx, request.OperationID+"-archive-ready")
	_ = c.service.CompleteSessionArchive(ctx, actor, session)
}

func (c *Controller) observeNaming(
	actor application.Principal,
	request runtimehost.Request,
	namingKey string,
) {
	c.workers.Add(1)
	go func() {
		defer func() {
			c.namingMu.Lock()
			delete(c.naming, namingKey)
			c.namingMu.Unlock()
			c.workers.Done()
		}()
		deadline := time.NewTimer(c.namingWait)
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
				result, found, err := c.runtime.LookupResult(c.ctx, request)
				if !found {
					continue
				}
				if err == nil && result.Delivered && result.GeneratedName != "" {
					c.commitGeneratedName(actor, request, result.GeneratedName)
				}
				return
			}
		}
	}()
}

func (c *Controller) commitGeneratedName(
	actor application.Principal,
	request runtimehost.Request,
	name string,
) {
	ref := domain.SessionRef{
		NodeID: domain.NodeID(request.NodeID), SessionID: domain.SessionID(request.SessionID),
	}
	session, err := c.service.Session(actor, ref)
	if err != nil || session.Name != "" || session.RuntimeGeneration != request.ExpectedGeneration {
		return
	}
	ctx, cancel := context.WithTimeout(c.ctx, 3*time.Second)
	defer cancel()
	ctx = application.WithOperationScope(ctx, request.OperationID+"-commit")
	_ = c.service.RenameSession(ctx, actor, session, name)
}

func (c *Controller) applyResult(
	actor application.Principal,
	request runtimehost.Request,
	result runtimehost.Result,
	executionErr error,
) {
	ref := domain.SessionRef{
		NodeID: domain.NodeID(request.NodeID), SessionID: domain.SessionID(request.SessionID),
	}
	session, err := c.service.Session(actor, ref)
	if err != nil || session.RuntimeGeneration != request.ExpectedGeneration ||
		session.LastOperation == nil || session.LastOperation.OperationID != request.OperationID {
		return
	}
	if executionErr == nil && result.Delivered && request.Input != nil &&
		session.Name == "" && session.OwnerID == actor.UserID && result.ResolvedText != "" {
		c.queueNaming(actor, request.OperationID+"-name", session, result.ResolvedText)
	}
	ctx, cancel := context.WithTimeout(c.ctx, 3*time.Second)
	defer cancel()
	if executionErr == nil && result.Delivered && request.Action == runtimehost.ActionClear {
		clearCtx := application.WithOperationScope(ctx, request.OperationID+"-clear")
		_ = c.service.ClearSession(clearCtx, actor, session)
		return
	}
	status := domain.OperationSucceeded
	phase := domain.RuntimeIdle
	if request.Action == runtimehost.ActionSendInput {
		phase = session.RuntimePhase
	}
	if executionErr != nil || !result.Delivered {
		status = domain.OperationFailed
		phase = domain.RuntimeDegraded
	}
	domainAction, err := mapAction(request.Action)
	if err != nil {
		return
	}
	operation := &domain.SessionOperationResult{
		OperationID: request.OperationID, Action: domainAction,
		Status: status, Detail: result.Detail,
	}
	resultCtx := application.WithOperationScope(ctx, request.OperationID+"-result")
	_ = c.service.PublishSessionRuntime(resultCtx, session, phase, operation)
}
