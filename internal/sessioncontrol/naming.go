package sessioncontrol

import (
	"context"
	"fmt"

	"github.com/Time4Mind/bria/internal/application"
	"github.com/Time4Mind/bria/internal/domain"
	"github.com/Time4Mind/bria/internal/runtimehost"
)

func (c *Controller) queueNaming(
	actor application.Principal,
	operationID string,
	session domain.Session,
	seed string,
) bool {
	key := fmt.Sprintf("%s:%d", session.Ref().Key(), session.RuntimeGeneration)
	c.namingMu.Lock()
	if c.naming[key] {
		c.namingMu.Unlock()
		return false
	}
	c.naming[key] = true
	c.namingMu.Unlock()
	request := runtimehost.Request{
		OperationID: operationID, ActorID: int64(actor.UserID),
		NodeID: string(session.NodeID), SessionID: string(session.ID),
		ExpectedGeneration: session.RuntimeGeneration,
		Action:             runtimehost.ActionGenerateName, Text: seed, Backend: session.Backend,
	}
	c.workers.Add(1)
	go func() {
		defer c.workers.Done()
		ctx, cancel := context.WithTimeout(c.ctx, c.retryInterval)
		_, err := c.runtime.Submit(ctx, request)
		cancel()
		if err != nil {
			c.retrySubmit(actor, request, false)
		}
		c.observeNaming(actor, request, key)
	}()
	return true
}
