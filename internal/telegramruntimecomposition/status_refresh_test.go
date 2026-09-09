package telegramruntimecomposition_test

import (
	"context"
	"sync"
	"testing"
	"time"

	"bria/internal/telegrambridge"
	"bria/internal/telegramcontroller"
	"bria/internal/telegramflow"
	"bria/internal/telegramruntimecomposition"
	"bria/internal/telegramstate"
	"bria/internal/telegramtrace"
	"bria/internal/telegramui"
)

type blockingStatusRenderer struct {
	started chan struct{}
	release chan struct{}
	mu      sync.Mutex
	calls   int
}

func (renderer *blockingStatusRenderer) RefreshStatus(ctx context.Context) (telegramcontroller.SemanticActionResult, error) {
	renderer.mu.Lock()
	renderer.calls++
	if renderer.calls == 1 {
		close(renderer.started)
	}
	renderer.mu.Unlock()
	select {
	case <-renderer.release:
	case <-ctx.Done():
		return telegramcontroller.SemanticActionResult{}, ctx.Err()
	}
	return telegramcontroller.SemanticActionResult{Surface: &telegramcontroller.SemanticSurface{
		Text: "fresh quota", RichMarkdown: true,
		Rows: [][]telegramcontroller.SemanticButton{{{Action: telegramcontroller.SemanticRefreshStatus}, {Action: telegramcontroller.SemanticMenuBack}}},
	}}, nil
}

type statusRefreshPublisher struct {
	published chan telegramflow.CallbackCommit
	mu        sync.Mutex
	surface   telegramflow.SurfaceOutput
	edited    bool
}

func (publisher *statusRefreshPublisher) EditCurrentGlobalSurface(_ context.Context, _ string, _ telegramstate.Carrier, expected telegrambridge.KeyboardPresentation, surface telegramflow.SurfaceOutput) (bool, error) {
	publisher.mu.Lock()
	publisher.surface = surface
	publisher.mu.Unlock()
	publisher.published <- telegramflow.CallbackCommit{Presentation: expected}
	return publisher.edited, nil
}

type refreshTraceObserver struct {
	mu     sync.Mutex
	events []telegramtrace.Event
}

func (observer *refreshTraceObserver) ObserveTelegramFlow(_ context.Context, event telegramtrace.Event) {
	observer.mu.Lock()
	observer.events = append(observer.events, event)
	observer.mu.Unlock()
}

func TestStatusRefreshIsSingleFlightAndPublishesFreshSnapshotToLatestStatus(t *testing.T) {
	root, cancel := context.WithCancel(context.Background())
	defer cancel()
	renderer := &blockingStatusRenderer{started: make(chan struct{}), release: make(chan struct{})}
	publisher := &statusRefreshPublisher{published: make(chan telegramflow.CallbackCommit, 2), edited: true}
	observer := &refreshTraceObserver{}
	refresh, err := telegramruntimecomposition.NewStatusRefreshCoordinator(root, renderer, publisher, observer)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = refresh.Close(context.Background()) })
	first := refreshCommit(t, 1)
	second := refreshCommit(t, 2)
	refresh.OnCallbackCommitted(first)
	select {
	case <-renderer.started:
	case <-time.After(time.Second):
		t.Fatal("quota refresh did not start")
	}
	refresh.OnCallbackCommitted(second)
	close(renderer.release)
	select {
	case published := <-publisher.published:
		if published.Presentation.TokenIDs[0] != second.Presentation.TokenIDs[0] {
			t.Fatalf("published target=%+v want latest=%+v", published.Presentation, second.Presentation)
		}
	case <-time.After(time.Second):
		t.Fatal("fresh status was not published")
	}
	renderer.mu.Lock()
	calls := renderer.calls
	renderer.mu.Unlock()
	if calls != 1 {
		t.Fatalf("RefreshStatus calls=%d want 1", calls)
	}
	publisher.mu.Lock()
	text := publisher.surface.Text
	publisher.mu.Unlock()
	if text != "fresh quota" {
		t.Fatalf("published text=%q", text)
	}
	waitForRefreshTrace(t, observer, "completed")
}

func TestStatusRefreshCloseCancelsProviderAndRejectsLaterCommits(t *testing.T) {
	renderer := &blockingStatusRenderer{started: make(chan struct{}), release: make(chan struct{})}
	publisher := &statusRefreshPublisher{published: make(chan telegramflow.CallbackCommit, 1), edited: true}
	observer := &refreshTraceObserver{}
	refresh, err := telegramruntimecomposition.NewStatusRefreshCoordinator(context.Background(), renderer, publisher, observer)
	if err != nil {
		t.Fatal(err)
	}
	refresh.OnCallbackCommitted(refreshCommit(t, 1))
	select {
	case <-renderer.started:
	case <-time.After(time.Second):
		t.Fatal("quota refresh did not start")
	}
	closeContext, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := refresh.Close(closeContext); err != nil {
		t.Fatal(err)
	}
	waitForRefreshTraceResult(t, observer, "failed", "cancelled")
	refresh.OnCallbackCommitted(refreshCommit(t, 2))
	renderer.mu.Lock()
	calls := renderer.calls
	renderer.mu.Unlock()
	if calls != 1 {
		t.Fatalf("RefreshStatus calls after close=%d want 1", calls)
	}
}

func refreshCommit(t *testing.T, updateID int64) telegramflow.CallbackCommit {
	t.Helper()
	now := time.Unix(1_800_000_000, 0).UTC()
	presentation := telegrambridge.KeyboardPresentation{
		SessionID: telegramui.GlobalSurfaceID, TokenIDs: []string{string(rune('a' + updateID))}, ExpiresAt: now.Add(time.Minute),
	}
	return telegramflow.CallbackCommit{
		OperationID: "status:" + string(rune('0'+updateID)), UpdateID: updateID,
		Action: telegramui.ActionRefreshStatus, Carrier: telegramstate.Carrier{ChatID: 42, MessageID: 99}, Presentation: presentation,
	}
}

func waitForRefreshTrace(t *testing.T, observer *refreshTraceObserver, result string) {
	waitForRefreshTraceResult(t, observer, result, "")
}

func waitForRefreshTraceResult(t *testing.T, observer *refreshTraceObserver, result, reason string) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		observer.mu.Lock()
		for _, event := range observer.events {
			if event.Stage == "quota.refresh" && event.Result == result && (reason == "" || event.Reason == reason) {
				observer.mu.Unlock()
				return
			}
		}
		observer.mu.Unlock()
		time.Sleep(time.Millisecond)
	}
	t.Fatal("refresh trace not observed")
}
