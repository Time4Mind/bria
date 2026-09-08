package observabilitycomposition

import (
	"context"
	"errors"
	"sync"
	"time"

	"bria/internal/domain"
	"bria/internal/observability"
	"bria/internal/sessionruntime"
	"bria/internal/turnprocessing"
)

var ErrInvalidConfiguration = errors.New("observability composition is invalid")

type Runtime interface {
	sessionruntime.Submitter
	sessionruntime.InteractiveSubmitter
}

type ProviderResolver interface {
	ProviderForSession(context.Context, domain.SessionID) (domain.Provider, error)
}

type Submitter struct {
	runtime   Runtime
	recorder  *observability.Recorder
	providers ProviderResolver
}

type CurrentRuntime interface {
	Runtime
	sessionruntime.CurrentTurnSubmitter
}

type CurrentSubmitter struct {
	*Submitter
	runtime CurrentRuntime
}

func NewCurrent(runtime CurrentRuntime, recorder *observability.Recorder, providers ProviderResolver) (*CurrentSubmitter, error) {
	base, err := New(runtime, recorder, providers)
	if err != nil {
		return nil, err
	}
	return &CurrentSubmitter{Submitter: base, runtime: runtime}, nil
}

func (submitter *CurrentSubmitter) SubmitCurrentWithCallbacks(ctx context.Context, sessionID domain.SessionID, input sessionruntime.StructuredInput, callbacks sessionruntime.TurnCallbacks) error {
	if submitter == nil || submitter.runtime == nil || submitter.Submitter == nil {
		return ErrInvalidConfiguration
	}
	span, timer := submitter.start(ctx, sessionID, callbacks.MessageID)
	if span == nil {
		return submitter.runtime.SubmitCurrentWithCallbacks(ctx, sessionID, input, callbacks)
	}
	err := submitter.runtime.SubmitCurrentWithCallbacks(ctx, sessionID, input, timer.callbacks(callbacks))
	if err == nil {
		_ = span.Success(timer.measurements())
	} else {
		_ = span.Failure(errorCategory(err), timer.measurements())
	}
	return err
}

func New(runtime Runtime, recorder *observability.Recorder, providers ProviderResolver) (*Submitter, error) {
	if runtime == nil || recorder == nil || providers == nil {
		return nil, ErrInvalidConfiguration
	}
	return &Submitter{runtime: runtime, recorder: recorder, providers: providers}, nil
}

func (submitter *Submitter) Submit(ctx context.Context, sessionID domain.SessionID, text string) (sessionruntime.TurnResult, error) {
	if submitter == nil || submitter.runtime == nil {
		return sessionruntime.TurnResult{}, ErrInvalidConfiguration
	}
	return submitter.runtime.Submit(ctx, sessionID, text)
}

func (submitter *Submitter) SubmitWithCallbacks(ctx context.Context, sessionID domain.SessionID, text string, callbacks sessionruntime.TurnCallbacks) (sessionruntime.TurnResult, error) {
	if submitter == nil || submitter.runtime == nil {
		return sessionruntime.TurnResult{}, ErrInvalidConfiguration
	}
	return submitter.submit(ctx, sessionID, callbacks, func(wrapped sessionruntime.TurnCallbacks) (sessionruntime.TurnResult, error) {
		return submitter.runtime.SubmitWithCallbacks(ctx, sessionID, text, wrapped)
	})
}

type PreparedRuntime interface {
	Runtime
	turnprocessing.PreparedTurnSubmitter
}

type PreparedSubmitter struct {
	*Submitter
	runtime PreparedRuntime
}

type PreparedCurrentRuntime interface {
	PreparedRuntime
	sessionruntime.CurrentTurnSubmitter
}

type PreparedCurrentSubmitter struct {
	*PreparedSubmitter
	runtime PreparedCurrentRuntime
}

func NewPreparedCurrent(runtime PreparedCurrentRuntime, recorder *observability.Recorder, providers ProviderResolver) (*PreparedCurrentSubmitter, error) {
	prepared, err := NewPrepared(runtime, recorder, providers)
	if err != nil {
		return nil, err
	}
	return &PreparedCurrentSubmitter{PreparedSubmitter: prepared, runtime: runtime}, nil
}

func (submitter *PreparedCurrentSubmitter) SubmitCurrentWithCallbacks(ctx context.Context, sessionID domain.SessionID, input sessionruntime.StructuredInput, callbacks sessionruntime.TurnCallbacks) error {
	current := &CurrentSubmitter{Submitter: submitter.Submitter, runtime: submitter.runtime}
	return current.SubmitCurrentWithCallbacks(ctx, sessionID, input, callbacks)
}

func NewPrepared(runtime PreparedRuntime, recorder *observability.Recorder, providers ProviderResolver) (*PreparedSubmitter, error) {
	base, err := New(runtime, recorder, providers)
	if err != nil {
		return nil, err
	}
	return &PreparedSubmitter{Submitter: base, runtime: runtime}, nil
}

func (submitter *PreparedSubmitter) SubmitPreparedWithCallbacks(ctx context.Context, sessionID domain.SessionID, input turnprocessing.PreparedInput, callbacks sessionruntime.TurnCallbacks) (sessionruntime.TurnResult, error) {
	if submitter == nil || submitter.runtime == nil || submitter.Submitter == nil {
		return sessionruntime.TurnResult{}, ErrInvalidConfiguration
	}
	return submitter.submit(ctx, sessionID, callbacks, func(wrapped sessionruntime.TurnCallbacks) (sessionruntime.TurnResult, error) {
		return submitter.runtime.SubmitPreparedWithCallbacks(ctx, sessionID, input, wrapped)
	})
}

func (submitter *Submitter) submit(ctx context.Context, sessionID domain.SessionID, callbacks sessionruntime.TurnCallbacks, call func(sessionruntime.TurnCallbacks) (sessionruntime.TurnResult, error)) (sessionruntime.TurnResult, error) {
	span, timer := submitter.start(ctx, sessionID, callbacks.MessageID)
	if span == nil {
		return call(callbacks)
	}
	result, err := call(timer.callbacks(callbacks))
	measurements := timer.measurements()
	if err == nil {
		_ = span.Success(measurements)
	} else {
		_ = span.Failure(errorCategory(err), measurements)
	}
	return result, err
}

func (submitter *Submitter) start(ctx context.Context, sessionID domain.SessionID, occurrence string) (*observability.Span, *turnTimer) {
	if submitter.recorder == nil || submitter.providers == nil {
		return nil, nil
	}
	provider, err := submitter.providers.ProviderForSession(ctx, sessionID)
	stage := providerStage(provider)
	if err != nil || stage == "" {
		return nil, nil
	}
	scope, err := observability.NewScope(string(sessionID))
	if err != nil {
		return nil, nil
	}
	span, err := submitter.recorder.Start(scope, stage, occurrence)
	if err != nil {
		return nil, nil
	}
	return span, &turnTimer{started: time.Now()}
}

func providerStage(provider domain.Provider) string {
	switch provider {
	case domain.ProviderCodex:
		return "provider.codex.submit"
	case domain.ProviderClaude:
		return "provider.claude.submit"
	default:
		return ""
	}
}

type turnTimer struct {
	started         time.Time
	mu              sync.Mutex
	accepted, event *time.Duration
}

func (timer *turnTimer) callbacks(callbacks sessionruntime.TurnCallbacks) sessionruntime.TurnCallbacks {
	if callbacks.OnAccepted != nil {
		original := callbacks.OnAccepted
		callbacks.OnAccepted = func(value string) error { timer.first(&timer.accepted); return original(value) }
	}
	if callbacks.OnEvent != nil {
		original := callbacks.OnEvent
		callbacks.OnEvent = func(event sessionruntime.TurnEvent) error { timer.first(&timer.event); return original(event) }
	}
	return callbacks
}

func (timer *turnTimer) first(destination **time.Duration) {
	timer.mu.Lock()
	defer timer.mu.Unlock()
	if *destination == nil {
		elapsed := time.Since(timer.started)
		*destination = observability.Duration(elapsed)
	}
}

func (timer *turnTimer) measurements() observability.Measurements {
	timer.mu.Lock()
	defer timer.mu.Unlock()
	return observability.Measurements{ProviderAccept: timer.accepted, FirstEvent: timer.event, Total: observability.Duration(time.Since(timer.started))}
}

func errorCategory(err error) string {
	if class := sessionruntime.RuntimeFailureClass(err); class != "" {
		return class
	}
	switch {
	case errors.Is(err, context.Canceled):
		return "cancelled"
	case errors.Is(err, context.DeadlineExceeded):
		return "deadline_exceeded"
	default:
		return "provider_error"
	}
}

var _ sessionruntime.Submitter = (*Submitter)(nil)
var _ sessionruntime.InteractiveSubmitter = (*Submitter)(nil)
var _ sessionruntime.CurrentTurnSubmitter = (*CurrentSubmitter)(nil)
var _ turnprocessing.PreparedTurnSubmitter = (*PreparedSubmitter)(nil)
var _ sessionruntime.CurrentTurnSubmitter = (*PreparedCurrentSubmitter)(nil)
var _ turnprocessing.PreparedTurnSubmitter = (*PreparedCurrentSubmitter)(nil)
