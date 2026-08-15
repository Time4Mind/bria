package sessioncontrol

import (
	"context"
	"crypto/sha256"
	"fmt"
	"strings"
	"time"

	"github.com/Time4Mind/bria/internal/application"
	"github.com/Time4Mind/bria/internal/domain"
	"github.com/Time4Mind/bria/internal/nodecontrol"
	"github.com/Time4Mind/bria/internal/runtimehost"
	"github.com/Time4Mind/bria/internal/transcript"
)

const maxNamingAttempts = 3

func (c *Controller) queueNaming(
	actor application.Principal,
	operationID string,
	session domain.Session,
	seed string,
) bool {
	key := namingKey(session)
	c.namingMu.Lock()
	if c.naming[key] {
		c.namingMu.Unlock()
		return false
	}
	c.naming[key] = true
	c.namingMu.Unlock()
	c.startNaming(actor, operationID, session, seed, key)
	return true
}

func (c *Controller) startNaming(
	actor application.Principal,
	operationID string,
	session domain.Session,
	seed string,
	key string,
) {
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
}

// EnsureName recovers a missing display name from the first user event already
// stored in the origin node's transcript. It never resends that event to the
// interactive provider session and never blocks Telegram rendering.
func (c *Controller) EnsureName(actor application.Principal, ref domain.SessionRef) bool {
	session, err := c.service.Session(actor, ref)
	if err != nil || c.transcripts == nil || !session.IsLive() || session.Name != "" ||
		session.OwnerID != actor.UserID {
		return false
	}
	key := namingKey(session)
	c.namingMu.Lock()
	if c.naming[key] {
		c.namingMu.Unlock()
		return false
	}
	c.naming[key] = true
	c.namingMu.Unlock()
	c.workers.Add(1)
	go func() {
		defer c.workers.Done()
		ctx, cancel := context.WithTimeout(c.ctx, 5*time.Second)
		events, readErr := c.transcripts.ReadTranscript(ctx, nodecontrol.TranscriptQuery{
			ActorID: int64(actor.UserID), NodeID: string(session.NodeID),
			SessionID: string(session.ID), ExpectedGeneration: session.RuntimeGeneration,
		})
		cancel()
		seed := firstUserText(events)
		if readErr != nil || seed == "" {
			c.releaseNaming(key)
			return
		}
		operationID := recoveryNamingOperationID(session, seed)
		c.startNaming(actor, operationID, session, seed, key)
	}()
	return true
}

func namingKey(session domain.Session) string {
	return fmt.Sprintf("%s:%d", session.Ref().Key(), session.RuntimeGeneration)
}

func firstUserText(events []transcript.Event) string {
	for _, event := range events {
		if event.Kind == transcript.EventUserText && strings.TrimSpace(event.Text) != "" {
			return event.Text
		}
	}
	return ""
}

func recoveryNamingOperationID(session domain.Session, seed string) string {
	digest := sha256.Sum256([]byte(fmt.Sprintf(
		"%s\x00%d\x00%s", session.Ref().Key(), session.RuntimeGeneration, seed,
	)))
	return fmt.Sprintf("name-recover-%x-%d", digest[:8], time.Now().UnixNano())
}

func (c *Controller) releaseNaming(key string) {
	c.namingMu.Lock()
	delete(c.naming, key)
	c.namingMu.Unlock()
}
