package telegramruntimecomposition

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"bria/internal/telegrambridge"
	"bria/internal/telegramcontroller"
	"bria/internal/telegramflow"
	"bria/internal/telegramstate"
	"bria/internal/telegramtrace"
	"bria/internal/telegramui"
)

type StatusRefreshController interface {
	RefreshStatus(context.Context) (telegramcontroller.SemanticActionResult, error)
}

type CurrentGlobalSurfaceEditor interface {
	EditCurrentGlobalSurface(context.Context, string, telegramstate.Carrier, telegrambridge.KeyboardPresentation, telegramflow.SurfaceOutput) (bool, error)
}

// StatusRefreshCoordinator starts quota collection only after the cached
// Status edit has a confirmed receipt. Repeated taps replace the target but
// share one provider collection.
type StatusRefreshCoordinator struct {
	controller StatusRefreshController
	editor     CurrentGlobalSurfaceEditor
	observer   telegramflow.TraceObserver
	root       context.Context
	cancel     context.CancelFunc

	mu         sync.Mutex
	running    bool
	closed     bool
	generation uint64
	target     telegramflow.CallbackCommit
	workers    sync.WaitGroup
}

// StatusRefreshCommitRelay breaks the construction cycle between telegramflow
// and its post-commit status refresh consumer.
type StatusRefreshCommitRelay struct {
	mu     sync.RWMutex
	target *StatusRefreshCoordinator
}

func (relay *StatusRefreshCommitRelay) Bind(target *StatusRefreshCoordinator) error {
	if relay == nil || target == nil {
		return errors.New("status refresh commit target is required")
	}
	relay.mu.Lock()
	defer relay.mu.Unlock()
	if relay.target != nil {
		return errors.New("status refresh commit target is already bound")
	}
	relay.target = target
	return nil
}

func (relay *StatusRefreshCommitRelay) OnCallbackCommitted(commit telegramflow.CallbackCommit) {
	if relay == nil {
		return
	}
	relay.mu.RLock()
	target := relay.target
	relay.mu.RUnlock()
	if target != nil {
		target.OnCallbackCommitted(commit)
	}
}

func NewStatusRefreshCoordinator(
	root context.Context,
	controller StatusRefreshController,
	editor CurrentGlobalSurfaceEditor,
	observer telegramflow.TraceObserver,
) (*StatusRefreshCoordinator, error) {
	if root == nil || controller == nil || editor == nil {
		return nil, errors.New("status refresh dependencies are required")
	}
	lifetime, cancel := context.WithCancel(root)
	return &StatusRefreshCoordinator{controller: controller, editor: editor, observer: observer, root: lifetime, cancel: cancel}, nil
}

// OnCallbackCommitted is intentionally non-blocking: telegramflow invokes it
// while settling the callback receipt.
func (coordinator *StatusRefreshCoordinator) OnCallbackCommitted(commit telegramflow.CallbackCommit) {
	if coordinator == nil || commit.Action != telegramui.ActionRefreshStatus || commit.OperationID == "" ||
		commit.UpdateID <= 0 || commit.Carrier.ChatID <= 0 || commit.Carrier.MessageID <= 0 ||
		commit.Presentation.SessionID != telegramui.GlobalSurfaceID {
		return
	}
	coordinator.mu.Lock()
	if coordinator.closed {
		coordinator.mu.Unlock()
		return
	}
	coordinator.generation++
	coordinator.target = commit
	if coordinator.running {
		coordinator.mu.Unlock()
		return
	}
	coordinator.running = true
	coordinator.workers.Add(1)
	coordinator.mu.Unlock()
	go coordinator.run(commit.OperationID)
}

func (coordinator *StatusRefreshCoordinator) run(operationID string) {
	defer coordinator.workers.Done()
	started := time.Now()
	coordinator.trace(telegramtrace.Event{Stage: "quota.refresh", OperationID: operationID, Result: "started"})
	result, err := coordinator.controller.RefreshStatus(coordinator.root)
	if err != nil {
		coordinator.finish(operationID, started, "failed", refreshFailureReason(err), true)
		return
	}
	if result.Card != nil || result.Surface == nil {
		coordinator.finish(operationID, started, "failed", "invalid_projection", true)
		return
	}
	surface, err := projectSemanticSurface(*result.Surface)
	if err != nil {
		coordinator.finish(operationID, started, "failed", "invalid_projection", true)
		return
	}
	for {
		coordinator.mu.Lock()
		generation, target := coordinator.generation, coordinator.target
		coordinator.mu.Unlock()
		edited, editErr := coordinator.editor.EditCurrentGlobalSurface(
			coordinator.root,
			fmt.Sprintf("quota-refresh:%d", target.UpdateID),
			target.Carrier,
			target.Presentation,
			*surface,
		)
		if editErr != nil {
			coordinator.finish(operationID, started, "failed", refreshFailureReason(editErr), true)
			return
		}
		coordinator.mu.Lock()
		if generation != coordinator.generation && !coordinator.closed {
			coordinator.mu.Unlock()
			continue
		}
		coordinator.running = false
		coordinator.mu.Unlock()
		result, reason := "completed", ""
		if !edited {
			result, reason = "skipped", "stale_presentation"
		}
		coordinator.trace(telegramtrace.Event{Stage: "quota.refresh", OperationID: operationID, Result: result, Reason: reason, Duration: time.Since(started)})
		return
	}
}

func (coordinator *StatusRefreshCoordinator) finish(operationID string, started time.Time, result, reason string, failed bool) {
	coordinator.mu.Lock()
	coordinator.running = false
	coordinator.mu.Unlock()
	event := telegramtrace.Event{Stage: "quota.refresh", OperationID: operationID, Result: result, Reason: reason, Duration: time.Since(started)}
	if failed {
		event.Error = "status refresh failed"
	}
	coordinator.trace(event)
}

func refreshFailureReason(err error) string {
	switch {
	case errors.Is(err, context.Canceled):
		return "cancelled"
	case errors.Is(err, context.DeadlineExceeded):
		return "deadline_exceeded"
	default:
		return "provider_failure"
	}
}

func (coordinator *StatusRefreshCoordinator) trace(event telegramtrace.Event) {
	if coordinator.observer != nil {
		coordinator.observer.ObserveTelegramFlow(context.WithoutCancel(coordinator.root), event)
	}
}

func (coordinator *StatusRefreshCoordinator) Close(ctx context.Context) error {
	if coordinator == nil {
		return nil
	}
	coordinator.mu.Lock()
	if !coordinator.closed {
		coordinator.closed = true
		coordinator.cancel()
	}
	coordinator.mu.Unlock()
	done := make(chan struct{})
	go func() {
		coordinator.workers.Wait()
		close(done)
	}()
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
