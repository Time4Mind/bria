package telegramapp

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/Time4Mind/bria/internal/application"
	"github.com/Time4Mind/bria/internal/domain"
)

type coalescingCaptureControls struct {
	SessionControls
	mu      sync.Mutex
	calls   int
	started chan struct{}
	release chan struct{}
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
	select {
	case <-c.release:
		return []byte("pane"), nil
	case <-ctx.Done():
		return nil, ctx.Err()
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
