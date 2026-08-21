package telegramapp

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/Time4Mind/bria/internal/application"
	"github.com/Time4Mind/bria/internal/domain"
	"github.com/Time4Mind/bria/internal/interaction"
)

type coalescingCaptureControls struct {
	SessionControls
	mu       sync.Mutex
	calls    int
	started  chan struct{}
	release  chan struct{}
	contexts chan context.Context
}

type joinedCaptureContext struct {
	context.Context
	once   sync.Once
	joined chan struct{}
}

func (ctx *joinedCaptureContext) Done() <-chan struct{} {
	ctx.once.Do(func() { close(ctx.joined) })
	return ctx.Context.Done()
}

func (c *coalescingCaptureControls) CapturePane(
	ctx context.Context,
	_ application.Principal,
	_ string,
	_ domain.SessionRef,
) ([]byte, error) {
	c.mu.Lock()
	c.calls++
	if c.calls == 1 {
		close(c.started)
	}
	c.mu.Unlock()
	if c.contexts != nil {
		c.contexts <- ctx
	}
	select {
	case <-c.release:
		return []byte("pane"), nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func TestPaneCaptureRetainsIngressAfterForegroundCancellation(t *testing.T) {
	release := make(chan struct{})
	close(release)
	controls := &coalescingCaptureControls{
		started: make(chan struct{}), release: release, contexts: make(chan context.Context, 1),
	}
	handler := &Handler{controls: controls, paneRefreshState: newPaneRefreshState()}
	ingress, err := interaction.NewIngress("test-ui", "event-42", "callback")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(interaction.WithIngress(context.Background(), ingress))
	cancel()
	_, _ = handler.capturePaneCoalesced(
		ctx, application.Principal{UserID: 7},
		domain.SessionRef{NodeID: "node", SessionID: "session"}, "capture-42",
	)
	select {
	case captureCtx := <-controls.contexts:
		captured, ok := interaction.IngressFromContext(captureCtx)
		if !ok || captured.ID() != ingress.ID() {
			t.Fatalf("capture ingress=%+v ok=%t", captured, ok)
		}
	case <-time.After(time.Second):
		t.Fatal("detached pane capture did not run")
	}
}

func TestPaneCaptureContinuesAfterForegroundTimeoutAndCoalesces(t *testing.T) {
	controls := &coalescingCaptureControls{
		started: make(chan struct{}), release: make(chan struct{}),
	}
	handler := &Handler{controls: controls, paneRefreshState: newPaneRefreshState()}
	actor := application.Principal{UserID: 7}
	ref := domain.SessionRef{NodeID: "node", SessionID: "session"}

	short, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()
	first := make(chan error, 1)
	go func() {
		_, err := handler.capturePaneCoalesced(short, actor, ref, "first")
		first <- err
	}()
	<-controls.started
	if err := <-first; err != context.DeadlineExceeded {
		t.Fatalf("foreground error=%v", err)
	}

	second := make(chan struct {
		pane string
		err  error
	}, 1)
	joined := &joinedCaptureContext{
		Context: context.Background(), joined: make(chan struct{}),
	}
	go func() {
		pane, err := handler.capturePaneCoalesced(joined, actor, ref, "second")
		second <- struct {
			pane string
			err  error
		}{string(pane), err}
	}()
	<-joined.joined
	close(controls.release)
	result := <-second
	if result.err != nil || result.pane != "pane" {
		t.Fatalf("joined capture pane=%q err=%v", result.pane, result.err)
	}
	controls.mu.Lock()
	calls := controls.calls
	controls.mu.Unlock()
	if calls != 1 {
		t.Fatalf("capture calls=%d, want one", calls)
	}
}
