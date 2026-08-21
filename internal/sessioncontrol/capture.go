package sessioncontrol

import (
	"context"
	"errors"
	"time"

	"github.com/Time4Mind/bria/internal/application"
	"github.com/Time4Mind/bria/internal/domain"
	"github.com/Time4Mind/bria/internal/processlog"
	"github.com/Time4Mind/bria/internal/runtimehost"
)

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
		processlog.Outcomef(
			processlog.Detail, outcome,
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
	logInteractionOperation(
		ctx, ref, request.ExpectedGeneration, request.OperationID, string(request.Action),
	)
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
