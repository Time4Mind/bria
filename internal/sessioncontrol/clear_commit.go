package sessioncontrol

import (
	"context"
	"time"

	"github.com/Time4Mind/bria/internal/application"
	"github.com/Time4Mind/bria/internal/domain"
	"github.com/Time4Mind/bria/internal/processlog"
	"github.com/Time4Mind/bria/internal/runtimehost"
)

const clearCommitMaxAttempts = 5

func (c *Controller) retryClearCommit(
	actor application.Principal,
	request runtimehost.Request,
	before domain.Session,
	attempts int,
	lastErr error,
) {
	c.workers.Add(1)
	go func() {
		defer c.workers.Done()
		ref := before.Ref()
		scope := request.OperationID + "-clear"

		for {
			current, err := c.service.Session(actor, ref)
			if err != nil {
				processlog.Criticalf(
					"bria session clear: cannot reread ref=%q operation=%q: %v",
					ref.Key(), request.OperationID, err,
				)
				return
			}
			if clearCommitMatches(current, before) {
				processlog.Servicef(
					"bria session clear: committed ref=%q operation=%q generation=%d",
					ref.Key(), request.OperationID, current.RuntimeGeneration,
				)
				return
			}
			if current.RuntimeGeneration != before.RuntimeGeneration ||
				current.Revision != before.Revision {
				processlog.Servicef(
					"bria session clear: stopped on newer state ref=%q operation=%q generation=%d revision=%d",
					ref.Key(), request.OperationID, current.RuntimeGeneration, current.Revision,
				)
				return
			}
			if attempts >= clearCommitMaxAttempts {
				break
			}
			if !c.waitForClearRetry() {
				return
			}
			attempts++
			attemptCtx, cancel := context.WithTimeout(c.ctx, 3*time.Second)
			clearCtx := application.WithOperationScope(attemptCtx, scope)
			err = c.service.ClearSession(clearCtx, actor, before)
			cancel()
			if err == nil {
				continue
			}
			lastErr = err
		}

		current, err := c.service.Session(actor, ref)
		if err == nil && clearCommitMatches(current, before) {
			processlog.Servicef(
				"bria session clear: committed after final reread ref=%q operation=%q generation=%d",
				ref.Key(), request.OperationID, current.RuntimeGeneration,
			)
			return
		}
		if err != nil {
			lastErr = err
		}
		processlog.Criticalf(
			"bria session clear: commit retry exhausted ref=%q operation=%q attempts=%d: %v",
			ref.Key(), request.OperationID, clearCommitMaxAttempts, lastErr,
		)
	}()
}

func (c *Controller) waitForClearRetry() bool {
	timer := time.NewTimer(c.retryInterval)
	defer timer.Stop()
	select {
	case <-c.ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}

func clearCommitMatches(current, before domain.Session) bool {
	// ClearSession's replicated LastOperation ID is the deterministic scoped
	// command ID, not the runtime request ID. The exact one-step generation and
	// revision transition, plus the clear result, identifies this commit while
	// allowing the same scoped command to be replayed safely.
	return current.IsLive() &&
		current.RuntimeGeneration == before.RuntimeGeneration+1 &&
		current.Revision == before.Revision+1 &&
		current.ProviderSessionID == "" && !current.ProviderResume &&
		current.LastOperation != nil &&
		current.LastOperation.Action == domain.ActionClear &&
		current.LastOperation.Status == domain.OperationSucceeded
}
