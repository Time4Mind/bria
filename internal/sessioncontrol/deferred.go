package sessioncontrol

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/Time4Mind/bria/internal/application"
	"github.com/Time4Mind/bria/internal/domain"
	"github.com/Time4Mind/bria/internal/runtimehost"
)

type Leadership interface {
	IsLeader() bool
}

func (c *Controller) queueDeferredInput(
	ctx context.Context,
	actor application.Principal,
	operationID string,
	session domain.Session,
	text string,
	input *runtimehost.InputPayload,
) (Accepted, error) {
	deferred, err := deferredInput(operationID, actor, session, text, input)
	if err != nil {
		return Accepted{}, err
	}
	logInteractionOperation(
		ctx, session.Ref(), deferred.ExpectedGeneration, deferred.OperationID,
		string(runtimehost.ActionSendInput),
	)
	queueCtx := application.WithOperationScope(ctx, operationID+"-offline-queue")
	if err := c.service.QueueDeferredInput(queueCtx, deferred); err != nil {
		return Accepted{Session: session.Ref()}, err
	}
	return Accepted{
		Session: session.Ref(), Deferred: true,
		Receipt: runtimehost.Receipt{OperationID: operationID, Accepted: true, Detail: "waiting for node recovery"},
	}, nil
}

func deferredInput(
	operationID string,
	actor application.Principal,
	session domain.Session,
	text string,
	input *runtimehost.InputPayload,
) (domain.DeferredSessionInput, error) {
	result := domain.DeferredSessionInput{
		OperationID: operationID, ActorID: actor.UserID, Session: session.Ref(),
		ExpectedGeneration: session.RuntimeGeneration, Kind: domain.DeferredInputText,
		Text: text,
	}
	if input == nil {
		return result, result.Validate()
	}
	result.Text = ""
	result.Kind = domain.DeferredInputKind(input.Kind)
	result.Caption = input.Caption
	result.VoiceBackend = input.VoiceBackend
	result.VoiceLanguage = input.VoiceLanguage
	result.TranscriptBaselineCount = input.TranscriptBaselineCount
	result.TranscriptBaselineKnown = input.TranscriptBaselineKnown
	result.TranscriptOrdinal = input.TranscriptOrdinal
	result.File = domain.DeferredInputFile{
		Provider: input.File.Provider, ID: input.File.ID, UniqueID: input.File.UniqueID,
		Name: input.File.Name, MIMEType: input.File.MIMEType, Size: input.File.Size,
	}
	return result, result.Validate()
}

// RunDeferredInputs drains one replicated head per session at a time. A head
// remains in Raft state until the owning runtime reports a terminal outcome;
// retries therefore reuse the operation ID and cannot overtake it.
func (c *Controller) RunDeferredInputs(ctx context.Context, leadership Leadership, interval time.Duration) error {
	if leadership == nil {
		return errors.New("leadership is required")
	}
	if interval <= 0 {
		interval = 500 * time.Millisecond
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	inflight := make(map[string]bool)
	var mu sync.Mutex
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
			if !leadership.IsLeader() {
				continue
			}
			for _, head := range c.service.DeferredInputHeads() {
				key := head.Session.Ref().Key()
				mu.Lock()
				busy := inflight[key]
				if !busy {
					inflight[key] = true
				}
				mu.Unlock()
				if busy {
					continue
				}
				c.workers.Add(1)
				go func(item application.DeferredInputHead) {
					defer c.workers.Done()
					defer func() {
						mu.Lock()
						delete(inflight, item.Session.Ref().Key())
						mu.Unlock()
					}()
					c.deliverDeferred(ctx, leadership, item)
				}(head)
			}
		}
	}
}

func (c *Controller) deliverDeferred(ctx context.Context, leadership Leadership, head application.DeferredInputHead) {
	request := deferredRequest(head.Session, head.Input)
	if _, err := c.runtime.Submit(ctx, request); err != nil {
		return
	}
	deadline := time.NewTimer(c.operationResultWait(request))
	defer deadline.Stop()
	ticker := time.NewTicker(c.pollInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-deadline.C:
			return
		case <-ticker.C:
			if !leadership.IsLeader() {
				return
			}
			result, found, err := c.runtime.LookupResult(ctx, request)
			if err != nil || !found {
				continue
			}
			resolveCtx := application.WithOperationScope(ctx, head.Input.OperationID+"-offline-resolve")
			failed := !result.Delivered
			if err := c.service.ResolveDeferredInput(
				resolveCtx, head.Session.Ref(), head.Input.OperationID, failed, result.Detail,
			); err != nil && !errors.Is(err, domain.ErrStaleOperation) {
				return
			}
			if !failed && head.Session.Name == "" && head.Session.OwnerID == head.Input.ActorID {
				nameText := head.Input.Text
				if nameText == "" {
					nameText = head.Input.Caption
				}
				if nameText != "" {
					c.queueNaming(application.Principal{UserID: head.Input.ActorID}, head.Input.OperationID+"-name", head.Session, nameText)
				}
			}
			return
		}
	}
}

func deferredRequest(session domain.Session, input domain.DeferredSessionInput) runtimehost.Request {
	request := runtimehost.Request{
		OperationID: input.OperationID, ActorID: int64(input.ActorID),
		NodeID: string(session.NodeID), SessionID: string(session.ID),
		ExpectedGeneration: input.ExpectedGeneration, Action: runtimehost.ActionSendInput,
		Text: input.Text, Backend: session.Backend,
	}
	if input.Kind != domain.DeferredInputText {
		request.Input = &runtimehost.InputPayload{
			Kind: runtimehost.InputKind(input.Kind), Caption: input.Caption,
			VoiceBackend: input.VoiceBackend, VoiceLanguage: input.VoiceLanguage,
			TranscriptBaselineCount: input.TranscriptBaselineCount,
			TranscriptBaselineKnown: input.TranscriptBaselineKnown,
			TranscriptOrdinal:       input.TranscriptOrdinal,
			File: runtimehost.InputFile{
				Provider: input.File.Provider, ID: input.File.ID, UniqueID: input.File.UniqueID,
				Name: input.File.Name, MIMEType: input.File.MIMEType, Size: input.File.Size,
			},
		}
	}
	return request
}
