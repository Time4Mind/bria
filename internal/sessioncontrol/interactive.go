package sessioncontrol

import (
	"context"
	"time"

	"github.com/Time4Mind/bria/internal/application"
	"github.com/Time4Mind/bria/internal/domain"
	"github.com/Time4Mind/bria/internal/runtimehost"
)

// SendKey addresses one exact prompt incarnation. The origin executor verifies
// the pane hash again immediately before touching tmux, closing the stale-click
// race between Telegram and a changing CLI.
func (c *Controller) SendKey(
	ctx context.Context,
	actor application.Principal,
	operationID string,
	ref domain.SessionRef,
	key runtimehost.InteractiveKey,
	promptHash string,
) ([]byte, error) {
	session, err := c.service.Session(actor, ref)
	if err != nil {
		return nil, err
	}
	if err := c.service.RequireSessionAction(actor, ref, domain.ActionSendKey); err != nil {
		return nil, err
	}
	if session.RuntimePhase != domain.RuntimeWaitingInput || session.InteractivePrompt == nil ||
		session.InteractivePrompt.Hash != promptHash {
		return nil, domain.ErrStaleOperation
	}
	request := runtimehost.Request{
		OperationID: operationID, ActorID: int64(actor.UserID),
		NodeID: string(session.NodeID), SessionID: string(session.ID),
		ExpectedGeneration: session.RuntimeGeneration, Backend: session.Backend,
		Action: runtimehost.ActionSendKey, Key: key, ExpectedPromptHash: promptHash,
	}
	logInteractionOperation(ctx, ref, request.ExpectedGeneration, request.OperationID, string(request.Action))
	if _, err := c.runtime.Submit(ctx, request); err != nil {
		return nil, err
	}
	return c.waitForPane(ctx, request)
}

func (c *Controller) waitForPane(
	ctx context.Context,
	request runtimehost.Request,
) ([]byte, error) {
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
