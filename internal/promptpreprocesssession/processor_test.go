package promptpreprocesssession

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"bria/internal/promptpreprocess"
	"bria/internal/promptpreprocessbinding"
	"bria/internal/promptpreprocesscommand"
	"bria/internal/promptpreprocesscore"
)

type lifecycleObserverFunc func(context.Context, LifecycleObservation) error

func (function lifecycleObserverFunc) ObservePreprocessingSession(ctx context.Context, observation LifecycleObservation) error {
	return function(ctx, observation)
}

type completionFunc func(context.Context) error

func (function completionFunc) Accept(ctx context.Context) error { return function(ctx) }

func TestObservedCompletionReportsReleaseFailureWithoutLosingAcceptance(t *testing.T) {
	releaseErr := errors.New("release failed")
	var states []LifecycleObservation
	processor := &Processor{observer: lifecycleObserverFunc(func(_ context.Context, observation LifecycleObservation) error {
		states = append(states, observation)
		return nil
	})}
	completion := &observedCompletion{
		processor: processor, provider: "codex", model: "gpt-5.6-luna",
		inner: completionFunc(func(context.Context) error { return releaseErr }),
	}

	if err := completion.Accept(context.Background()); !errors.Is(err, releaseErr) {
		t.Fatalf("accept error = %v, want %v", err, releaseErr)
	}
	if len(states) != 2 || states[0].State != "accepted" || states[1].State != "release_failed" || states[1].ErrorCategory != "provider" {
		t.Fatalf("lifecycle states = %#v", states)
	}
}

var _ promptpreprocess.Completion = completionFunc(nil)

func TestProcessorProcessUsesDurablyCapturedRequestMode(t *testing.T) {
	commands, err := promptpreprocesscommand.New(testConfigStore(os.Args[0]), append(os.Environ(), "BRIA_TEST_TELEGRAM_TOKEN=redacted"), "local")
	if err != nil {
		t.Fatal(err)
	}
	starts := make(chan promptpreprocesscore.StartRequest, 2)
	turns := make(chan string, 2)
	bindings, err := promptpreprocessbinding.OpenFileBindingStore(filepath.Join(t.TempDir(), "bindings.json"))
	if err != nil {
		t.Fatal(err)
	}
	processor := &Processor{commands: commands}
	processor.manager = promptpreprocesscore.New(func(_ context.Context, start promptpreprocesscore.StartRequest) (promptpreprocesscore.Session, error) {
		starts <- start
		return &processorFixtureSession{threadID: "captured", started: turns}, nil
	}, bindings)
	defer processor.Close(context.Background())
	if err := processor.SetMode(context.Background(), promptpreprocess.ModePerSession); err != nil {
		t.Fatal(err)
	}
	result, err := processor.Process(context.Background(), promptpreprocess.Request{
		ComputerID: "local", SessionID: "main", MessageID: "accepted", Sequence: 1,
		Mode: promptpreprocess.ModeShared, Instruction: "clean", Text: "raw",
	})
	if err != nil || result.Completion == nil {
		t.Fatalf("result = %#v, %v", result, err)
	}
	if start := receiveProcessorValue(t, starts); start.Key.Mode != promptpreprocess.ModeShared {
		t.Fatalf("Processor replaced request mode: %#v", start.Key)
	}
	if turn := receiveProcessorValue(t, turns); turn != "captured:accepted:1" {
		t.Fatalf("turn = %q", turn)
	}
	if err := result.Completion.Accept(context.Background()); err != nil {
		t.Fatal(err)
	}
}

type processorFixtureSession struct {
	threadID string
	started  chan<- string
}

func (session *processorFixtureSession) Process(_ context.Context, request promptpreprocess.Request) (promptpreprocess.Result, error) {
	session.started <- session.threadID + ":" + request.MessageID + ":1"
	return promptpreprocess.Result{Text: "cleaned"}, nil
}

func (session *processorFixtureSession) Binding() string             { return session.threadID }
func (session *processorFixtureSession) Close(context.Context) error { return nil }

func receiveProcessorValue[T any](t *testing.T, values <-chan T) T {
	t.Helper()
	select {
	case value := <-values:
		return value
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for processor fixture")
		var zero T
		return zero
	}
}
