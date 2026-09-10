// Package promptpreprocesssession owns the hidden one-shot provider-session
// pool used for durable prompt preprocessing.
package promptpreprocesssession

import (
	"context"
	"errors"
	"strings"
	"sync"
	"time"

	"bria/internal/domain"
	"bria/internal/promptpreprocess"
	"bria/internal/promptpreprocesscommand"
)

var (
	ErrUnavailable = errors.New("prompt preprocessing session is unavailable")
	ErrInvocation  = errors.New("prompt preprocessing session invocation failed")
	errProtocol    = errors.New("prompt preprocessing session protocol failed")
)

type Processor struct {
	commands          *promptpreprocesscommand.Processor
	adapterExecutable string
	pool              *sessionPool
	observer          LifecycleObserver
}

var _ promptpreprocess.Processor = (*Processor)(nil)
var _ promptpreprocess.Invalidator = (*Processor)(nil)

type LifecycleObservation struct {
	State         string
	Provider      domain.Provider
	Model         string
	Duration      time.Duration
	ErrorCategory string
}

type LifecycleObserver interface {
	ObservePreprocessingSession(context.Context, LifecycleObservation) error
}

func New(commands *promptpreprocesscommand.Processor, adapterExecutable string, observers ...LifecycleObserver) (*Processor, error) {
	if commands == nil || !strings.HasPrefix(adapterExecutable, "/") {
		return nil, ErrUnavailable
	}
	processor := &Processor{commands: commands, adapterExecutable: adapterExecutable}
	if len(observers) > 0 {
		processor.observer = observers[0]
	}
	processor.pool = newSessionPool(processor.startSession)
	return processor, nil
}

func (processor *Processor) Warmup(ctx context.Context) {
	if processor != nil && processor.pool != nil {
		processor.pool.Warmup(ctx)
	}
}

func (processor *Processor) Process(ctx context.Context, request promptpreprocess.Request) (promptpreprocess.Result, error) {
	if processor == nil || processor.pool == nil || ctx == nil || ctx.Err() != nil ||
		request.ComputerID != processor.commands.ComputerID() || strings.TrimSpace(request.MessageID) == "" ||
		strings.TrimSpace(request.Instruction) == "" || strings.TrimSpace(request.Text) == "" {
		return promptpreprocess.Result{}, ErrUnavailable
	}
	started := time.Now()
	result, completion, err := processor.pool.Process(ctx, request)
	state, category := "responded", ""
	if err != nil {
		state, category = "failed", "provider"
		processor.pool.Invalidate()
	}
	processor.observe(context.WithoutCancel(ctx), LifecycleObservation{
		State: state, Provider: result.Provider, Model: result.Model,
		Duration: time.Since(started), ErrorCategory: category,
	})
	if completion != nil {
		result.Completion = &observedCompletion{
			inner: completion, processor: processor, provider: result.Provider, model: result.Model,
		}
	}
	return result, err
}

func (processor *Processor) Invalidate(computerID domain.ComputerID) {
	if processor == nil || processor.pool == nil || computerID != processor.commands.ComputerID() {
		return
	}
	processor.commands.Invalidate(computerID)
	processor.pool.Invalidate()
}

func (processor *Processor) Close(ctx context.Context) error {
	if processor == nil || processor.pool == nil {
		return nil
	}
	return processor.pool.Close(ctx)
}

func (processor *Processor) startSession(ctx context.Context) (session, error) {
	var lastErr error
	for attempts := 0; attempts < 2; attempts++ {
		selection, err := processor.commands.Select(ctx)
		if err != nil {
			return nil, err
		}
		started := time.Now()
		processor.observe(context.WithoutCancel(ctx), LifecycleObservation{
			State: "starting", Provider: selection.Provider(), Model: selection.Model(),
		})
		var prepared session
		readyState := "ready"
		switch selection.Provider() {
		case domain.ProviderCodex:
			prepared, err = startCodexSession(ctx, selection, processor.adapterExecutable)
		case domain.ProviderClaude:
			prepared = &statelessFallbackSession{processor: processor.commands}
			readyState = "degraded"
		default:
			err = ErrUnavailable
		}
		if err == nil {
			processor.observe(context.WithoutCancel(ctx), LifecycleObservation{
				State: readyState, Provider: selection.Provider(), Model: selection.Model(), Duration: time.Since(started),
			})
			return &observedSession{session: prepared, selection: selection, processor: processor.commands}, nil
		}
		processor.observe(context.WithoutCancel(ctx), LifecycleObservation{
			State: "start_failed", Provider: selection.Provider(), Model: selection.Model(),
			Duration: time.Since(started), ErrorCategory: "provider",
		})
		lastErr = err
		processor.commands.Report(selection, false)
	}
	if lastErr == nil {
		lastErr = ErrUnavailable
	}
	return nil, lastErr
}

func (processor *Processor) observe(ctx context.Context, observation LifecycleObservation) {
	if processor.observer != nil {
		_ = processor.observer.ObservePreprocessingSession(ctx, observation)
	}
}

type observedCompletion struct {
	once      sync.Once
	inner     promptpreprocess.Completion
	processor *Processor
	provider  domain.Provider
	model     string
	err       error
}

func (completion *observedCompletion) Accept(ctx context.Context) error {
	completion.once.Do(func() {
		completion.processor.observe(context.WithoutCancel(ctx), LifecycleObservation{
			State: "accepted", Provider: completion.provider, Model: completion.model,
		})
		started := time.Now()
		completion.err = completion.inner.Accept(ctx)
		state, category := "closed", ""
		if completion.err != nil {
			state, category = "close_failed", "provider"
		}
		completion.processor.observe(context.WithoutCancel(ctx), LifecycleObservation{
			State: state, Provider: completion.provider, Model: completion.model,
			Duration: time.Since(started), ErrorCategory: category,
		})
	})
	return completion.err
}

type observedSession struct {
	session   session
	selection promptpreprocesscommand.Selection
	processor *promptpreprocesscommand.Processor
}

func (current *observedSession) Process(ctx context.Context, request promptpreprocess.Request) (promptpreprocess.Result, error) {
	result, err := current.session.Process(ctx, request)
	result.Provider = current.selection.Provider()
	result.Model = current.selection.Model()
	current.processor.Report(current.selection, err == nil)
	return result, err
}

func (current *observedSession) Close(ctx context.Context) error { return current.session.Close(ctx) }

type statelessFallbackSession struct {
	processor *promptpreprocesscommand.Processor
}

func (fallback *statelessFallbackSession) Process(ctx context.Context, request promptpreprocess.Request) (promptpreprocess.Result, error) {
	return fallback.processor.Process(ctx, request)
}

func (*statelessFallbackSession) Close(context.Context) error { return nil }

func closeContext() (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.Background(), 10*time.Second)
}
