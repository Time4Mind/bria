package promptpreprocesssession

import (
	"context"
	"errors"
	"testing"

	"bria/internal/promptpreprocess"
)

type lifecycleObserverFunc func(context.Context, LifecycleObservation) error

func (function lifecycleObserverFunc) ObservePreprocessingSession(ctx context.Context, observation LifecycleObservation) error {
	return function(ctx, observation)
}

type completionFunc func(context.Context) error

func (function completionFunc) Accept(ctx context.Context) error { return function(ctx) }

func TestObservedCompletionReportsCloseFailureWithoutLosingAcceptance(t *testing.T) {
	closeErr := errors.New("close failed")
	var states []LifecycleObservation
	processor := &Processor{observer: lifecycleObserverFunc(func(_ context.Context, observation LifecycleObservation) error {
		states = append(states, observation)
		return nil
	})}
	completion := &observedCompletion{
		processor: processor, provider: "codex", model: "gpt-5.6-luna",
		inner: completionFunc(func(context.Context) error { return closeErr }),
	}

	if err := completion.Accept(context.Background()); !errors.Is(err, closeErr) {
		t.Fatalf("accept error = %v, want %v", err, closeErr)
	}
	if len(states) != 2 || states[0].State != "accepted" || states[1].State != "close_failed" || states[1].ErrorCategory != "provider" {
		t.Fatalf("lifecycle states = %#v", states)
	}
}

var _ promptpreprocess.Completion = completionFunc(nil)
