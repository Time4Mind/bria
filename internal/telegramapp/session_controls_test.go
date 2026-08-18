package telegramapp_test

import (
	"context"
	"sync"

	"github.com/Time4Mind/bria/internal/application"
	"github.com/Time4Mind/bria/internal/domain"
	"github.com/Time4Mind/bria/internal/nodecontrol"
	"github.com/Time4Mind/bria/internal/runtimehost"
	"github.com/Time4Mind/bria/internal/sessioncontrol"
	"github.com/Time4Mind/bria/internal/transcript"
)

type blockingControls struct {
	mu        sync.RWMutex
	started   chan struct{}
	release   chan struct{}
	ref       domain.SessionRef
	events    []transcript.Event
	afterSend []transcript.Event
	external  *runtimehost.InputPayload
	pane      []byte
	key       runtimehost.InteractiveKey
	keyHash   string
	text      string
}

type closingControls struct {
	*blockingControls
	service *application.Service
}

type delayedTranscriptControls struct {
	*blockingControls
	started chan struct{}
	release chan struct{}
	once    sync.Once
}

type transcriptErrorControls struct {
	*blockingControls
	err error
}

func (c *transcriptErrorControls) Transcript(context.Context, application.Principal, domain.SessionRef) ([]transcript.Event, error) {
	return nil, c.err
}

func (c *delayedTranscriptControls) Transcript(ctx context.Context, actor application.Principal, ref domain.SessionRef) ([]transcript.Event, error) {
	if ref != c.ref {
		return nil, nil
	}
	c.once.Do(func() { close(c.started) })
	select {
	case <-c.release:
		return c.blockingControls.Transcript(ctx, actor, ref)
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func (c *blockingControls) SendInput(_ context.Context, _ application.Principal, _ string, text string) (sessioncontrol.Accepted, error) {
	c.mu.Lock()
	c.text = text
	if c.afterSend != nil {
		c.events = append([]transcript.Event(nil), c.afterSend...)
	}
	c.mu.Unlock()
	return sessioncontrol.Accepted{Session: c.ref}, nil
}

func (c *blockingControls) SendExternalInput(_ context.Context, _ application.Principal, _ string, input runtimehost.InputPayload) (sessioncontrol.Accepted, error) {
	c.external = &input
	return sessioncontrol.Accepted{Session: c.ref}, nil
}

func (c *blockingControls) Stop(context.Context, application.Principal, string, domain.SessionRef) (sessioncontrol.Accepted, error) {
	close(c.started)
	<-c.release
	return sessioncontrol.Accepted{Session: c.ref, Receipt: runtimehost.Receipt{OperationID: "stop", Accepted: true}}, nil
}

func (c *blockingControls) Clear(context.Context, application.Principal, string, domain.SessionRef) (sessioncontrol.Accepted, error) {
	return sessioncontrol.Accepted{}, nil
}

func (c *blockingControls) Close(context.Context, application.Principal, string, domain.SessionRef) (sessioncontrol.Accepted, error) {
	return sessioncontrol.Accepted{}, nil
}

func (c *blockingControls) Restore(context.Context, application.Principal, string, domain.SessionRef) (sessioncontrol.Accepted, error) {
	return sessioncontrol.Accepted{}, nil
}

func (c *blockingControls) OpenTerminal(context.Context, application.Principal, string, domain.SessionRef) (sessioncontrol.Accepted, error) {
	return sessioncontrol.Accepted{}, nil
}

func (c *blockingControls) CapturePane(context.Context, application.Principal, string, domain.SessionRef) ([]byte, error) {
	return append([]byte(nil), c.pane...), nil
}

func (c *blockingControls) Transcript(context.Context, application.Principal, domain.SessionRef) ([]transcript.Event, error) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return append([]transcript.Event(nil), c.events...), nil
}

func (c *blockingControls) OpenSessionFile(context.Context, application.Principal, domain.SessionRef, string) (nodecontrol.SessionFile, error) {
	return nodecontrol.SessionFile{}, domain.ErrNotFound
}
