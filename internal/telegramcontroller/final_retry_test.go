package telegramcontroller_test

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"bria/internal/domain"
	"bria/internal/sessionruntime"
	"bria/internal/telegramcontroller"
)

type retryFinalState struct {
	mu       sync.Mutex
	failures int
	final    string
	attempt  chan struct{}
}

func (*retryFinalState) SetActiveSession(context.Context, domain.SessionID) error { return nil }
func (s *retryFinalState) RestoreAcceptedFinal(_ context.Context, _ domain.SessionID, _ string, text string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	select {
	case s.attempt <- struct{}{}:
	default:
	}
	if s.failures != 0 {
		s.failures--
		return errors.New("synthetic storage unavailable")
	}
	s.final = text
	return nil
}

func TestFinalSaveRecoversAutomaticallyWithoutProviderReplay(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	ready := readySession(t, "aaaaaaaa-aaaa-4aaa-9aaa-aaaaaaaaaaaa", domain.ProviderCodex, t.TempDir(), "provider-a", 1)
	state := &retryFinalState{failures: 1, attempt: make(chan struct{}, 8)}
	sent := make(chan string, 4)
	provider := &interactiveSubmitter{submitWithCallbacks: func(_ context.Context, _ domain.SessionID, text string, cb sessionruntime.TurnCallbacks) (sessionruntime.TurnResult, error) {
		sent <- text
		if err := cb.OnAccepted(cb.MessageID); err != nil {
			return sessionruntime.TurnResult{}, err
		}
		return sessionruntime.TurnResult{TerminalStatus: sessionruntime.StatusCompleted, Final: "retained final"}, nil
	}}
	c := newController(t, nil, newLockedSessions(ready), provider, nil, telegramcontroller.Options{Recovered: []domain.Session{ready}, UIState: state})
	defer c.Close(context.Background())
	completed := make(chan telegramcontroller.DurableInputProcessReceipt, 4)
	callbacks := telegramcontroller.DurableInputCallbacks{
		OnAccepted: func(context.Context, telegramcontroller.DurableInputAcceptance) error { return nil },
		OnCompleted: func(_ context.Context, receipt telegramcontroller.DurableInputProcessReceipt) error {
			completed <- receipt
			return nil
		},
	}
	input := telegramcontroller.DurableLeasedInput{SessionID: ready.ID(), MessageID: "root", Sequence: 1, Payload: []byte("one request")}
	if receipt, err := c.ProcessDurableInput(ctx, input, callbacks); err != nil || !receipt.Accepted {
		t.Fatalf("acceptance=%+v err=%v", receipt, err)
	}
	select {
	case got := <-completed:
		if got.Completion != telegramcontroller.DurableInputSucceeded {
			t.Fatalf("temporary final-write failure prematurely settled request: %s", got.Completion)
		}
	case <-ctx.Done():
		t.Fatal("final save did not recover automatically")
	}
	state.mu.Lock()
	final := state.final
	state.mu.Unlock()
	if final != "retained final" {
		t.Fatalf("saved final=%q", final)
	}
	if first := <-sent; first != "one request" {
		t.Fatalf("provider input=%q", first)
	}
	select {
	case replay := <-sent:
		t.Fatalf("provider replay=%q", replay)
	default:
	}
}

func TestPendingFinalSaveStopsOnShutdownOrReplacedBinding(t *testing.T) {
	for _, mode := range []string{"shutdown", "replaced-binding"} {
		t.Run(mode, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			ready := readySession(t, "aaaaaaaa-aaaa-4aaa-9aaa-aaaaaaaaaaaa", domain.ProviderCodex, t.TempDir(), "provider-a", 1)
			sessions := newLockedSessions(ready)
			state := &retryFinalState{failures: 100, attempt: make(chan struct{}, 8)}
			provider := &interactiveSubmitter{submitWithCallbacks: func(_ context.Context, _ domain.SessionID, _ string, cb sessionruntime.TurnCallbacks) (sessionruntime.TurnResult, error) {
				if err := cb.OnAccepted(cb.MessageID); err != nil {
					return sessionruntime.TurnResult{}, err
				}
				return sessionruntime.TurnResult{TerminalStatus: sessionruntime.StatusCompleted, Final: "old final"}, nil
			}}
			c := newController(t, nil, sessions, provider, nil, telegramcontroller.Options{Recovered: []domain.Session{ready}, UIState: state})
			defer c.Close(context.Background())
			completed := make(chan telegramcontroller.DurableInputProcessReceipt, 1)
			callbacks := telegramcontroller.DurableInputCallbacks{
				OnAccepted: func(context.Context, telegramcontroller.DurableInputAcceptance) error { return nil },
				OnCompleted: func(_ context.Context, receipt telegramcontroller.DurableInputProcessReceipt) error {
					completed <- receipt
					return nil
				},
			}
			if receipt, err := c.ProcessDurableInput(ctx, telegramcontroller.DurableLeasedInput{SessionID: ready.ID(), MessageID: "old", Sequence: 1, Payload: []byte("request")}, callbacks); err != nil || !receipt.Accepted {
				t.Fatalf("accepted=%+v err=%v", receipt, err)
			}
			select {
			case <-state.attempt:
			case <-ctx.Done():
				t.Fatal("first save not attempted")
			}
			select {
			case got := <-completed:
				t.Fatalf("failed save must remain pending while live: %+v", got)
			case <-time.After(20 * time.Millisecond):
			}
			if mode == "shutdown" {
				closeCtx, stop := context.WithTimeout(context.Background(), time.Second)
				defer stop()
				if err := c.Close(closeCtx); err != nil {
					t.Fatalf("retry delayed shutdown: %v", err)
				}
			} else {
				awaiting, err := ready.AwaitRecoveryAt(time.Now())
				if err != nil {
					t.Fatal(err)
				}
				binding, _ := ready.Binding()
				binding.Generation++
				recovered, err := awaiting.Recovered(binding, time.Now())
				if err != nil {
					t.Fatal(err)
				}
				sessions.Set(recovered)
				state.mu.Lock()
				state.failures = 0
				state.mu.Unlock()
			}
			select {
			case got := <-completed:
				if got.Completion != telegramcontroller.DurableInputUnknown {
					t.Fatalf("old request falsely completed: %+v", got)
				}
			case <-ctx.Done():
				t.Fatal("retry did not settle")
			}
			state.mu.Lock()
			final := state.final
			state.mu.Unlock()
			if final != "" {
				t.Fatalf("old final persisted after %s: %q", mode, final)
			}
		})
	}
}
