package sessioncontrol

import (
	"context"
	"strings"

	"github.com/Time4Mind/bria/internal/application"
	"github.com/Time4Mind/bria/internal/runtimehost"
)

// SendExternalInput pins the actor's active session before any node-local
// download or transcription. The runtime executor serializes this descriptor
// with text and control commands in that session's durable FIFO.
func (c *Controller) SendExternalInput(
	ctx context.Context,
	actor application.Principal,
	operationID string,
	input runtimehost.InputPayload,
) (Accepted, error) {
	session, err := c.service.ActiveSession(actor)
	if err != nil {
		return Accepted{}, err
	}
	accepted, err := c.submit(
		ctx, actor, operationID, session, runtimehost.ActionSendInput, "", &input,
	)
	if err == nil && !accepted.Deferred && session.Name == "" && session.OwnerID == actor.UserID &&
		strings.TrimSpace(input.Caption) != "" {
		accepted.NamingQueued = c.queueNaming(
			actor, operationID+"-name", session, input.Caption,
		)
	}
	return accepted, err
}
