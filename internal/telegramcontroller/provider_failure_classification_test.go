package telegramcontroller_test

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"bria/internal/app"
	"bria/internal/controllertelemetry"
	"bria/internal/domain"
	"bria/internal/sessionruntime"
	"bria/internal/telegramcontroller"
)

type controllerEventCollector struct {
	mu     sync.Mutex
	events []controllertelemetry.Event
}

func (collector *controllerEventCollector) ObserveControllerEvent(_ context.Context, event controllertelemetry.Event) {
	collector.mu.Lock()
	collector.events = append(collector.events, event)
	collector.mu.Unlock()
}

func (collector *controllerEventCollector) providerFailures() int {
	collector.mu.Lock()
	defer collector.mu.Unlock()
	count := 0
	for _, event := range collector.events {
		if event.Stage == controllertelemetry.ProviderFailure {
			count++
		}
	}
	return count
}

func (collector *controllerEventCollector) providerFailureReasons() []controllertelemetry.Reason {
	collector.mu.Lock()
	defer collector.mu.Unlock()
	reasons := make([]controllertelemetry.Reason, 0, len(collector.events))
	for _, event := range collector.events {
		if event.Stage == controllertelemetry.ProviderFailure {
			reasons = append(reasons, event.Reason)
		}
	}
	return reasons
}

func TestProviderFailureTelemetryExcludesIntentionalInterruptionAndCancellation(t *testing.T) {
	for _, test := range []struct {
		name       string
		result     sessionruntime.TurnResult
		err        error
		wantEvents int
	}{
		{name: "interrupted", result: sessionruntime.TurnResult{TerminalStatus: sessionruntime.StatusInterrupted, ErrorCode: sessionruntime.ErrorInterrupted}, err: sessionruntime.ErrTurnFailed},
		{name: "unclaimed cancellation", err: context.Canceled, wantEvents: 1},
		{name: "provider failure joined with cancellation", err: errors.Join(context.Canceled, sessionruntime.ErrTurnFailed), wantEvents: 1},
		{name: "deadline", err: context.DeadlineExceeded, wantEvents: 1},
		{name: "provider failure", result: sessionruntime.TurnResult{TerminalStatus: sessionruntime.StatusFailed}, err: sessionruntime.ErrTurnFailed, wantEvents: 1},
	} {
		t.Run(test.name, func(t *testing.T) {
			ready := readySession(t, "11111111-1111-4111-9111-111111111111", domain.ProviderCodex, t.TempDir(), "provider-a", 1)
			observer := &controllerEventCollector{}
			provider := &interactiveSubmitter{submitWithCallbacks: func(_ context.Context, _ domain.SessionID, _ string, callbacks sessionruntime.TurnCallbacks) (sessionruntime.TurnResult, error) {
				if callbacks.OnAccepted != nil {
					if err := callbacks.OnAccepted(callbacks.MessageID); err != nil {
						return sessionruntime.TurnResult{}, err
					}
				}
				return test.result, test.err
			}}
			controller := newController(t, nil, newLockedSessions(ready), provider, nil, telegramcontroller.Options{
				Recovered: []domain.Session{ready}, ControllerObserver: observer,
			})
			t.Cleanup(func() { _ = controller.Close(context.Background()) })

			_, _ = controller.ProcessDurableInput(context.Background(), telegramcontroller.DurableLeasedInput{
				SessionID: ready.ID(), MessageID: "root", Sequence: 1, Payload: []byte("synthetic"),
			}, telegramcontroller.DurableInputCallbacks{OnAccepted: func(context.Context, telegramcontroller.DurableInputAcceptance) error { return nil }})
			waitProviderFailureCount(t, observer, test.wantEvents)
			if got := observer.providerFailures(); got != test.wantEvents {
				t.Fatalf("provider failures = %d, want %d", got, test.wantEvents)
			}
			if test.name == "deadline" {
				reasons := observer.providerFailureReasons()
				if len(reasons) != 1 || reasons[0] != controllertelemetry.DeadlineExceeded {
					t.Fatalf("provider failure reasons = %#v, want deadline_exceeded", reasons)
				}
			}
		})
	}
}

func TestIntentionalStopDoesNotHideJoinedProviderFailure(t *testing.T) {
	for _, test := range []struct {
		name   string
		result sessionruntime.TurnResult
		err    error
	}{
		{name: "joined provider marker", err: errors.Join(context.Canceled, sessionruntime.ErrTurnFailed)},
		{name: "failed terminal", result: sessionruntime.TurnResult{TerminalStatus: sessionruntime.StatusFailed}, err: context.Canceled},
	} {
		t.Run(test.name, func(t *testing.T) {
			ready := readySession(t, "33333333-3333-4333-9333-333333333333", domain.ProviderCodex, t.TempDir(), "provider-cancel", 1)
			accepted := make(chan struct{})
			stopRelease := make(chan struct{})
			observer := &controllerEventCollector{}
			provider := &interactiveSubmitter{submitWithCallbacks: func(_ context.Context, _ domain.SessionID, _ string, callbacks sessionruntime.TurnCallbacks) (sessionruntime.TurnResult, error) {
				if err := callbacks.OnAccepted(callbacks.MessageID); err != nil {
					return sessionruntime.TurnResult{}, err
				}
				close(accepted)
				<-stopRelease
				return test.result, test.err
			}}
			stopper := stopperFunc(func(context.Context, domain.SessionID) error {
				close(stopRelease)
				return nil
			})
			controller := newController(t, nil, newLockedSessions(ready), provider, nil, telegramcontroller.Options{
				Recovered: []domain.Session{ready}, ControllerObserver: observer, Stopper: stopper,
			})
			t.Cleanup(func() { _ = controller.Close(context.Background()) })

			if _, err := controller.Handle(context.Background(), message(9020, "provider request")); err != nil {
				t.Fatal(err)
			}
			waitClosed(t, accepted, "provider request was not accepted")
			if _, err := controller.Handle(context.Background(), message(9021, "/stop")); err != nil {
				t.Fatal(err)
			}
			waitProviderFailureCount(t, observer, 1)
			if got := observer.providerFailures(); got != 1 {
				t.Fatalf("provider failures=%d, want 1", got)
			}
		})
	}
}

func waitProviderFailureCount(t *testing.T, observer *controllerEventCollector, want int) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for observer.providerFailures() != want && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
}

func TestStopCommandRecordsInterruptedWithoutProviderFailure(t *testing.T) {
	workdir := t.TempDir()
	ready := readySession(t, "11111111-1111-4111-9111-111111111111", domain.ProviderCodex, workdir, "provider-1", 1)
	started := make(chan struct{})
	continued := make(chan struct{})
	terminal := make(chan struct{})
	accepted := make(chan struct{})
	calls := 0
	provider := &interactiveSubmitter{submitWithCallbacks: func(_ context.Context, _ domain.SessionID, _ string, callbacks sessionruntime.TurnCallbacks) (sessionruntime.TurnResult, error) {
		calls++
		if callbacks.OnAccepted != nil {
			if err := callbacks.OnAccepted(callbacks.MessageID); err != nil {
				return sessionruntime.TurnResult{}, err
			}
		}
		if calls > 1 {
			close(continued)
			return sessionruntime.TurnResult{TerminalStatus: sessionruntime.StatusCompleted}, nil
		}
		close(accepted)
		close(started)
		<-terminal
		return sessionruntime.TurnResult{TerminalStatus: sessionruntime.StatusInterrupted, ErrorCode: sessionruntime.ErrorInterrupted}, sessionruntime.ErrTurnFailed
	}}
	observer := &controllerEventCollector{}
	creator := creatorFunc(func(_ context.Context, intent app.ConfirmedSessionIntent) (app.CreateSessionResult, error) {
		return app.CreateSessionResult{Session: sessionWithIntent(t, ready, intent.IntentID)}, nil
	})
	stopper := stopperFunc(func(context.Context, domain.SessionID) error {
		close(terminal)
		return nil
	})
	controller := newController(t, creator, &memorySessions{byID: map[domain.SessionID]domain.Session{ready.ID(): ready}}, provider, nil, telegramcontroller.Options{ControllerObserver: observer, Stopper: stopper})
	t.Cleanup(func() { _ = controller.Close(context.Background()) })

	mustStatus(t, controller, message(9001, "/new codex "+workdir))
	mustStatus(t, controller, message(9002, "accepted request"))
	waitClosed(t, started, "request did not start")
	waitClosed(t, accepted, "request was not accepted")
	mustStatus(t, controller, message(9003, "/stop"))
	mustStatus(t, controller, message(9004, "request after stop"))
	waitClosed(t, continued, "worker did not continue after interrupted terminal")

	if failures := observer.providerFailures(); failures != 0 {
		t.Fatalf("provider failures after /stop = %d, want 0", failures)
	}
}

func TestControllerCloseRecordsCancelledWithoutProviderFailure(t *testing.T) {
	workdir := t.TempDir()
	ready := readySession(t, "22222222-2222-4222-9222-222222222222", domain.ProviderCodex, workdir, "provider-2", 1)
	accepted := make(chan struct{})
	provider := &interactiveSubmitter{submitWithCallbacks: func(ctx context.Context, _ domain.SessionID, _ string, callbacks sessionruntime.TurnCallbacks) (sessionruntime.TurnResult, error) {
		if callbacks.OnAccepted != nil {
			if err := callbacks.OnAccepted(callbacks.MessageID); err != nil {
				return sessionruntime.TurnResult{}, err
			}
		}
		close(accepted)
		<-ctx.Done()
		return sessionruntime.TurnResult{}, ctx.Err()
	}}
	observer := &controllerEventCollector{}
	creator := creatorFunc(func(_ context.Context, intent app.ConfirmedSessionIntent) (app.CreateSessionResult, error) {
		return app.CreateSessionResult{Session: sessionWithIntent(t, ready, intent.IntentID)}, nil
	})
	controller := newController(t, creator, &memorySessions{byID: map[domain.SessionID]domain.Session{ready.ID(): ready}}, provider, nil, telegramcontroller.Options{ControllerObserver: observer})

	mustStatus(t, controller, message(9011, "/new codex "+workdir))
	mustStatus(t, controller, message(9012, "accepted request"))
	waitClosed(t, accepted, "request was not accepted")
	closeContext, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := controller.Close(closeContext); err != nil {
		t.Fatalf("Controller.Close() error = %v", err)
	}

	if failures := observer.providerFailures(); failures != 0 {
		t.Fatalf("provider failures after Controller.Close = %d, want 0", failures)
	}
}

func waitClosed(t *testing.T, signal <-chan struct{}, message string) {
	t.Helper()
	select {
	case <-signal:
	case <-time.After(time.Second):
		t.Fatal(message)
	}
}
