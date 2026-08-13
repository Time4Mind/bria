package sessioncontrol

import (
	"context"

	"github.com/Time4Mind/bria/internal/application"
	"github.com/Time4Mind/bria/internal/domain"
	"github.com/Time4Mind/bria/internal/nodecontrol"
	"github.com/Time4Mind/bria/internal/transcript"
)

func (c *Controller) Transcript(
	ctx context.Context,
	actor application.Principal,
	ref domain.SessionRef,
) ([]transcript.Event, error) {
	if c.transcripts == nil {
		return nil, ErrRuntimeUnavailable
	}
	session, err := c.service.Session(actor, ref)
	if err != nil {
		return nil, err
	}
	if session.State == domain.SessionArchived {
		if err := c.service.RequireSessionAction(actor, ref, domain.ActionView); err != nil {
			return nil, err
		}
	} else {
		if err := c.service.RequireSessionAction(actor, ref, domain.ActionCapture); err != nil {
			return nil, err
		}
	}
	return c.transcripts.ReadTranscript(ctx, nodecontrol.TranscriptQuery{
		ActorID: int64(actor.UserID), NodeID: string(session.NodeID),
		SessionID: string(session.ID), ExpectedGeneration: session.RuntimeGeneration,
	})
}
