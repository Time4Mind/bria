package telegramcontroller

import (
	"bria/internal/app"
	"bria/internal/controllertelemetry"
	"bria/internal/coordinator"
	"bria/internal/domain"
	"bria/internal/sessioncloseflow"
	"context"
)

func (c *Controller) beginInteractiveClose(ctx context.Context, id domain.SessionID, interactive InteractiveSessionCloser) (coordinator.Decision, error) {
	closer, ok := c.sessionCloser.(sessioncloseflow.CompletingCloser)
	if !ok {
		return c.closeSessionResult(ctx, id, interactive.BeginClose)
	}
	var decision coordinator.Decision
	err := c.closeFlow.Begin(ctx, id, closer, func(closeFn sessioncloseflow.CloseFunc) (err error) {
		decision, err = c.closeSessionResult(ctx, id, closeFn)
		return err
	}, func(completedContext context.Context, result app.CloseSessionResult) error {
		c.mu.Lock()
		closed := c.closed
		c.mu.Unlock()
		if closed {
			return nil
		}
		return c.applyClosedSession(completedContext, result.Session)
	})
	return decision, err
}

func (c *Controller) projectedEvent(ctx context.Context, source domain.SessionID, result SemanticActionResult, err error) {
	e := controllertelemetry.Event{Stage: controllertelemetry.ProjectedTarget, SessionID: string(source), Reason: controllertelemetry.SessionList, Outcome: controllertelemetry.Projected}
	switch {
	case err != nil:
		e.Outcome, e.Reason = controllertelemetry.Failed, controllertelemetry.ProjectionFailed
	case result.Card != nil:
		e.TargetSessionID, e.Reason = string(result.Card.SessionID), controllertelemetry.SessionCard
	case result.Surface != nil && result.Surface.NativeSessionID != "":
		e.TargetSessionID, e.Reason = string(result.Surface.NativeSessionID), controllertelemetry.NativeSurface
	case result.Surface == nil || result.Surface.Text != "Сессии":
		return
	}
	c.closeFlow.Observe(ctx, e)
}

func (c *Controller) selectAfterClose(ctx context.Context, session domain.Session) (domain.SessionID, bool, error) {
	c.selectionMu.Lock()
	defer c.selectionMu.Unlock()
	c.mu.Lock()
	delete(c.live, session.ID())
	previous := c.active
	c.mu.Unlock()
	r, err := c.closeFlow.RemoveClosed(ctx, c.nodes, session)
	if err != nil {
		return "", false, err
	}
	foreground := r.Selected == session.ComputerID()
	if foreground {
		c.mu.Lock()
		c.active = r.Active
		c.mu.Unlock()
	}
	return r.Active, foreground && previous == session.ID(), nil
}
