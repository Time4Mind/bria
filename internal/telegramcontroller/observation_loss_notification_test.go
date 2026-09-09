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

func TestObservationLossNotificationPolicy(t *testing.T) {
	for _, persistent := range []bool{false, true} {
		for _, tc := range []struct {
			name   string
			result sessionruntime.TurnResult
			want   string
		}{
			{name: "unknown"},
			{name: "auth", result: sessionruntime.TurnResult{ErrorCode: sessionruntime.ErrorAuthenticationFailed}, want: "Ошибка авторизации Claude: требуется выполнить вход (/login)."},
			{name: "failed", result: sessionruntime.TurnResult{TerminalStatus: sessionruntime.StatusFailed}, want: "Ошибка CLI: запрос не выполнен."},
		} {
			t.Run(tc.name+map[bool]string{false: "/without-attach", true: "/with-attach"}[persistent], func(t *testing.T) {
				ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
				defer cancel()
				ready := readySession(t, "aaaaaaaa-aaaa-4aaa-9aaa-aaaaaaaaaaaa", domain.ProviderCodex, t.TempDir(), "native", 2)
				running, err := ready.StartWork(time.Now())
				if err != nil {
					t.Fatal(err)
				}
				binding, _ := running.Binding()
				provider := &acceptedObserver{observe: func(context.Context, domain.SessionID, domain.ProviderBinding, sessionruntime.TurnCallbacks) (sessionruntime.TurnResult, error) {
					return tc.result, errors.New("synthetic observation failure")
				}}
				state := &projectionUIState{}
				opts := telegramcontroller.Options{UIState: state, AcceptedObserver: provider}
				if persistent {
					opts.AcceptedObserver = &persistentAcceptedObserver{provider}
					opts.Recoverer = deferredRecoverer(func(context.Context, domain.SessionID) (domain.Session, error) { return running, nil })
				}
				var mu sync.Mutex
				var notices []string
				c := newController(t, nil, newLockedSessions(running), provider, notifierFunc(func(_ context.Context, n telegramcontroller.Notification) error {
					if n.Kind == telegramcontroller.NotificationError {
						mu.Lock()
						notices = append(notices, n.Text)
						mu.Unlock()
					}
					return nil
				}), opts)
				defer c.Close(context.Background())
				done := make(chan telegramcontroller.DurableInputProcessReceipt, 1)
				if err := c.ContinueAcceptedInput(ctx, binding, telegramcontroller.DurableLeasedInput{SessionID: running.ID(), MessageID: "accepted", Sequence: 1}, telegramcontroller.DurableInputCallbacks{OnCompleted: func(_ context.Context, r telegramcontroller.DurableInputProcessReceipt) error { done <- r; return nil }}); err != nil {
					t.Fatal(err)
				}
				select {
				case r := <-done:
					if !r.Accepted {
						t.Fatal("lost durable acceptance")
					}
					if tc.result.TerminalStatus == "" && r.Completion != telegramcontroller.DurableInputAwaitingRecovery {
						t.Fatalf("unknown outcome resolved: %s", r.Completion)
					}
				case <-ctx.Done():
					t.Fatal("continuation did not settle")
				}
				if err := c.Close(ctx); err != nil {
					t.Fatal(err)
				}
				if provider.submits.Load() != 0 {
					t.Fatal("accepted input replayed")
				}
				mu.Lock()
				defer mu.Unlock()
				history := state.history[running.ID()]
				if tc.want == "" {
					if len(notices) != 0 || len(history) != 0 {
						t.Fatalf("observation loss published notifications=%q history=%q", notices, history)
					}
				} else if len(notices) != 1 || notices[0] != tc.want || len(history) != 1 || history[0] != tc.want {
					t.Fatalf("genuine error missing: notifications=%q history=%q, want %q", notices, history, tc.want)
				}
			})
		}
	}
}
