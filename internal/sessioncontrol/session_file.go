package sessioncontrol

import (
	"context"

	"github.com/Time4Mind/bria/internal/application"
	"github.com/Time4Mind/bria/internal/domain"
	"github.com/Time4Mind/bria/internal/nodecontrol"
)

func (c *Controller) OpenSessionFile(
	ctx context.Context,
	actor application.Principal,
	ref domain.SessionRef,
	path string,
) (nodecontrol.SessionFile, error) {
	if c.files == nil {
		return nodecontrol.SessionFile{}, ErrRuntimeUnavailable
	}
	session, err := c.service.Session(actor, ref)
	if err != nil {
		return nodecontrol.SessionFile{}, err
	}
	if err := c.service.RequireSessionAction(actor, ref, domain.ActionCapture); err != nil {
		return nodecontrol.SessionFile{}, err
	}
	return c.files.OpenSessionFile(ctx, nodecontrol.SessionFileQuery{
		ActorID: int64(actor.UserID), NodeID: string(session.NodeID),
		SessionID: string(session.ID), ExpectedGeneration: session.RuntimeGeneration,
		Path: path,
	})
}
