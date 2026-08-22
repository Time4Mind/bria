package sessioncontrol

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/Time4Mind/bria/internal/application"
	"github.com/Time4Mind/bria/internal/domain"
	"github.com/Time4Mind/bria/internal/processlog"
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
				if errors.Is(err, runtimehost.ErrQueueFull) {
					if reportFailure {
						c.applyResult(
							actor, request,
							runtimehost.Result{Accepted: true, Detail: "runtime queue is full"},
							err,
						)
					}
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
		deadline := time.NewTimer(c.operationResultWait(request))
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

func (c *Controller) operationResultWait(request runtimehost.Request) time.Duration {
	if request.Action == runtimehost.ActionSendInput && request.Input != nil {
		return c.mediaResultWait
	}
	return c.resultWait
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
			c.releaseNaming(namingKey)
			c.workers.Done()
		}()
		baseOperationID := request.OperationID
		for attempt := 0; attempt < maxNamingAttempts; attempt++ {
			if attempt > 0 {
				if !c.namingStillNeeded(actor, request) {
					return
				}
				request.OperationID = namingRetryOperationID(baseOperationID, attempt)
				ctx, cancel := context.WithTimeout(c.ctx, c.retryInterval)
				_, err := c.runtime.Submit(ctx, request)
				cancel()
				if err != nil {
					c.retrySubmit(actor, request, false)
				}
			}
			result, found, err := c.waitForNamingResult(request)
			if found && err == nil && result.Delivered && result.GeneratedName != "" {
				c.commitGeneratedName(actor, request, result.GeneratedName)
				return
			}
		}
	}()
}

func (c *Controller) waitForNamingResult(
	request runtimehost.Request,
) (runtimehost.Result, bool, error) {
	deadline := time.NewTimer(c.namingWait)
	defer deadline.Stop()
	ticker := time.NewTicker(c.pollInterval)
	defer ticker.Stop()
	for {
		select {
		case <-c.ctx.Done():
			return runtimehost.Result{}, false, c.ctx.Err()
		case <-deadline.C:
			return runtimehost.Result{}, false, context.DeadlineExceeded
		case <-ticker.C:
			result, found, err := c.runtime.LookupResult(c.ctx, request)
			if found {
				return result, true, err
			}
		}
	}
}

func (c *Controller) namingStillNeeded(
	actor application.Principal,
	request runtimehost.Request,
) bool {
	session, err := c.service.Session(actor, domain.SessionRef{
		NodeID: domain.NodeID(request.NodeID), SessionID: domain.SessionID(request.SessionID),
	})
	return err == nil && session.IsLive() && sessionNeedsGeneratedName(session) &&
		session.RuntimeGeneration == request.ExpectedGeneration
}

func namingRetryOperationID(base string, attempt int) string {
	suffix := fmt.Sprintf("-retry-%d", attempt)
	if len(base)+len(suffix) > 128 {
		base = base[:128-len(suffix)]
	}
	return base + suffix
}

func (c *Controller) commitGeneratedName(
	actor application.Principal,
	request runtimehost.Request,
	name string,
) {
	ref := domain.SessionRef{
		NodeID: domain.NodeID(request.NodeID), SessionID: domain.SessionID(request.SessionID),
	}
	for attempt := 0; attempt < 3; attempt++ {
		session, err := c.service.Session(actor, ref)
		if err != nil || !sessionNeedsGeneratedName(session) ||
			session.RuntimeGeneration != request.ExpectedGeneration {
			return
		}
		candidate, err := c.service.AvailableSessionName(actor, ref, name)
		if err != nil {
			return
		}
		ctx, cancel := context.WithTimeout(c.ctx, 3*time.Second)
		ctx = application.WithOperationScope(
			ctx, fmt.Sprintf("%s-commit-%d-%s", request.OperationID, attempt, candidate),
		)
		err = c.service.RenameSession(ctx, actor, session, candidate)
		cancel()
		if err == nil {
			processlog.Servicef("bria session naming: renamed ref=%q", ref.Key())
			return
		}
	}
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
	if request.Action == runtimehost.ActionSendInput && request.Input == nil &&
		executionErr == nil && result.Delivered &&
		(result.ProviderAccepted == nil || *result.ProviderAccepted) {
		// The transcript acknowledgement proves submission, not turn completion.
		// Keep the durable operation queued until the canonical final settles it.
		return
	}
	providerRejected := result.ProviderAccepted != nil && !*result.ProviderAccepted
	if executionErr == nil && result.Delivered && !providerRejected && request.Input != nil &&
		session.Name == "" && session.OwnerID == actor.UserID && result.ResolvedText != "" {
		c.queueNaming(actor, request.OperationID+"-name", session, result.ResolvedText)
	}
	ctx, cancel := context.WithTimeout(c.ctx, 3*time.Second)
	defer cancel()
	if executionErr == nil && result.Delivered && request.Action == runtimehost.ActionClear {
		clearCtx := application.WithOperationScope(ctx, request.OperationID+"-clear")
		clearErr := c.service.ClearSession(clearCtx, actor, session)
		if clearErr != nil {
			c.retryClearCommit(actor, request, session, 1, clearErr)
		}
		return
	}
	status := domain.OperationSucceeded
	phase := domain.RuntimeIdle
	if request.Action == runtimehost.ActionSendInput {
		phase = session.RuntimePhase
	}
	if executionErr != nil || !result.Delivered || providerRejected {
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
	if request.Input != nil && request.Input.Kind == runtimehost.InputVoice {
		operation.InputKind = "voice"
		operation.TranscriptBaselineCount = request.Input.TranscriptBaselineCount
		operation.TranscriptBaselineKnown = request.Input.TranscriptBaselineKnown
		operation.TranscriptOrdinal = request.Input.TranscriptOrdinal
		outcome := "delivered"
		if status == domain.OperationFailed {
			outcome = "failed"
		}
		processlog.Outcomef(
			processlog.Detail, outcome,
			"bria voice_input: stage=delivery ref=%q generation=%d operation=%q outcome=%s",
			ref.Key(), request.ExpectedGeneration, request.OperationID, outcome,
		)
	}
	resultCtx := application.WithOperationScope(ctx, request.OperationID+"-result")
	_ = c.service.PublishSessionRuntime(resultCtx, session, phase, operation)
}
