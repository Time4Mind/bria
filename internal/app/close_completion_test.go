package app_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"bria/internal/app"
	"bria/internal/domain"
)

type completionCloser interface {
	BeginCloseWithCompletion(context.Context, domain.SessionID, func(context.Context, app.CloseSessionResult, error)) (app.CloseSessionResult, error)
}

type completionStarter struct {
	lifecycleStarter
	release <-chan struct{}
	failure error
}

type repeatedBusyStarter struct {
	lifecycleStarter
	aborted chan struct{}
}

func (s *repeatedBusyStarter) Abort(context.Context, app.StartSessionRequest, domain.ProviderBinding) error {
	s.aborted <- struct{}{}
	return nil
}

func TestRepeatedInteractiveBusyCloseDoesNotInterruptRunningWork(t *testing.T) {
	running, err := readySession(t, time.Now().Add(-time.Hour)).StartWork(time.Now())
	if err != nil {
		t.Fatal(err)
	}
	s := &lifecycleStore{session: running}
	starter := &repeatedBusyStarter{aborted: make(chan struct{}, 2)}
	closer, _ := app.NewSessionCloser(s, starter, time.Now)
	for i := 0; i < 2; i++ {
		result, err := closer.BeginClose(context.Background(), running.ID())
		if err != nil || !result.Scheduled || result.Session.Status() != domain.SessionClosingAfterWork {
			t.Fatalf("busy close=%+v err=%v", result, err)
		}
	}
	select {
	case <-starter.aborted:
		t.Fatal("repeated archive interrupted running provider")
	case <-time.After(100 * time.Millisecond):
	}
}

func (s *completionStarter) Abort(context.Context, app.StartSessionRequest, domain.ProviderBinding) error {
	<-s.release
	return s.failure
}

func TestInteractiveCloseReportsDurableCompletionAfterCallbackCancellation(t *testing.T) {
	for _, fail := range []bool{false, true} {
		t.Run(map[bool]string{false: "archive", true: "provider_failure"}[fail], func(t *testing.T) {
			store := &lifecycleStore{session: readySession(t, time.Now().Add(-time.Hour))}
			release := make(chan struct{})
			starter := &completionStarter{release: release}
			if fail {
				starter.failure = errors.New("provider failure")
			}
			closer, err := app.NewSessionCloser(store, starter, time.Now)
			if err != nil {
				t.Fatal(err)
			}
			interactive, ok := any(closer).(completionCloser)
			if !ok {
				t.Fatal("interactive close discards asynchronous completion")
			}
			type key struct{}
			ctx, cancel := context.WithCancel(context.WithValue(context.Background(), key{}, "close-operation"))
			defer cancel()
			done := make(chan struct{}, 1)
			result, err := interactive.BeginCloseWithCompletion(ctx, store.session.ID(), func(completedCtx context.Context, result app.CloseSessionResult, err error) {
				defer func() { done <- struct{}{} }()
				want := domain.SessionArchived
				if fail {
					want = domain.SessionAwaitingRecovery
				}
				if result.Session.Status() != want || !store.session.Equal(result.Session) || result.Scheduled || (err != nil) != fail || (fail && !errors.Is(err, starter.failure)) {
					t.Errorf("completion status=%s durable=%s scheduled=%v err=%v", result.Session.Status(), store.session.Status(), result.Scheduled, err)
				}
				if completedCtx.Err() != nil || completedCtx.Value(key{}) != "close-operation" {
					t.Error("completion lost operation context or inherited cancellation")
				}
			})
			if err != nil || !result.Scheduled || result.Session.Status() != domain.SessionClosing {
				t.Fatalf("begin=%+v err=%v", result, err)
			}
			cancel()
			close(release)
			select {
			case <-done:
			case <-time.After(time.Second):
				t.Fatal("durable result not reported")
			}
		})
	}
}
