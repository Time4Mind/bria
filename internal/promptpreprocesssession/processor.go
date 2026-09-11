// Package promptpreprocesssession owns hidden persistent provider-session
// satellites used for durable prompt preprocessing.
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
	"bria/internal/promptpreprocesscore"
)

var (
	ErrUnavailable = promptpreprocesscore.ErrUnavailable
	ErrInvocation  = promptpreprocesscore.ErrInvocation
	errProtocol    = errors.New("prompt preprocessing session protocol failed")
)

type Processor struct {
	commands          *promptpreprocesscommand.Processor
	adapterExecutable string
	manager           *promptpreprocesscore.Manager
	observer          LifecycleObserver
}

var _ promptpreprocess.Processor = (*Processor)(nil)
var _ promptpreprocess.Invalidator = (*Processor)(nil)
var _ promptpreprocess.ModeController = (*Processor)(nil)

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
	return NewWithBindingStore(commands, adapterExecutable, nil, observers...)
}

func NewWithBindingStore(commands *promptpreprocesscommand.Processor, adapterExecutable string, store BindingStore, observers ...LifecycleObserver) (*Processor, error) {
	if commands == nil || !strings.HasPrefix(adapterExecutable, "/") {
		return nil, ErrUnavailable
	}
	processor := &Processor{commands: commands, adapterExecutable: adapterExecutable}
	if len(observers) > 0 {
		processor.observer = observers[0]
	}
	processor.manager = promptpreprocesscore.New(processor.startSession, store)
	return processor, nil
}

func (processor *Processor) Warmup(ctx context.Context) {
	if processor != nil && processor.manager != nil {
		go func() { _ = processor.manager.Warmup(ctx) }()
	}
}

func (processor *Processor) Process(ctx context.Context, request promptpreprocess.Request) (promptpreprocess.Result, error) {
	if processor == nil || processor.manager == nil || ctx == nil || ctx.Err() != nil ||
		request.ComputerID != processor.commands.ComputerID() || strings.TrimSpace(request.MessageID) == "" ||
		strings.TrimSpace(request.Instruction) == "" || strings.TrimSpace(request.Text) == "" {
		return promptpreprocess.Result{}, ErrUnavailable
	}
	started := time.Now()
	result, err := processor.manager.Process(ctx, request)
	state, category := "responded", ""
	if err != nil {
		state, category = "failed", "provider"
	}
	processor.observe(context.WithoutCancel(ctx), LifecycleObservation{
		State: state, Provider: result.Provider, Model: result.Model,
		Duration: time.Since(started), ErrorCategory: category,
	})
	if result.Completion != nil {
		result.Completion = &observedCompletion{
			inner: result.Completion, processor: processor, provider: result.Provider, model: result.Model,
		}
	}
	return result, err
}

func (processor *Processor) Invalidate(computerID domain.ComputerID) {
	if processor == nil || processor.manager == nil || computerID != processor.commands.ComputerID() {
		return
	}
	processor.commands.Invalidate(computerID)
	processor.manager.Invalidate()
}

func (processor *Processor) Close(ctx context.Context) error {
	if processor == nil || processor.manager == nil {
		return nil
	}
	return processor.manager.Close(ctx)
}

func (processor *Processor) SetMode(ctx context.Context, mode promptpreprocess.Mode) error {
	if processor == nil || processor.manager == nil {
		return ErrUnavailable
	}
	return processor.manager.SetMode(ctx, mode)
}

func (processor *Processor) Activate(ctx context.Context, id domain.SessionID) error {
	if processor == nil || processor.manager == nil {
		return ErrUnavailable
	}
	return processor.manager.Activate(ctx, id)
}

func (processor *Processor) Archive(ctx context.Context, id domain.SessionID) error {
	if processor == nil || processor.manager == nil {
		return ErrUnavailable
	}
	return processor.manager.Archive(ctx, id)
}

func (processor *Processor) Restore(ctx context.Context, id domain.SessionID) error {
	if processor == nil || processor.manager == nil {
		return ErrUnavailable
	}
	return processor.manager.Restore(ctx, id)
}

func (processor *Processor) Forget(ctx context.Context, id domain.SessionID) error {
	if processor == nil || processor.manager == nil {
		return ErrUnavailable
	}
	return processor.manager.Forget(ctx, id)
}

func (processor *Processor) Reconcile(ctx context.Context, states []PrimaryState) error {
	if processor == nil || processor.manager == nil {
		return ErrUnavailable
	}
	return processor.manager.Reconcile(ctx, states)
}

func (processor *Processor) startSession(ctx context.Context, start StartRequest) (promptpreprocesscore.Session, error) {
	replacement := start.Replacement
	replacementStarted := time.Now()
	var lastErr error
	var lastProvider domain.Provider
	var lastModel string
	for attempts := 0; attempts < 2; attempts++ {
		selection, err := processor.commands.Select(ctx)
		if err != nil {
			if replacement {
				processor.observe(context.WithoutCancel(ctx), LifecycleObservation{
					State: "replacement_failed", Duration: time.Since(replacementStarted), ErrorCategory: startupErrorCategory(err),
				})
			}
			return nil, err
		}
		lastProvider, lastModel = selection.Provider(), selection.Model()
		started := time.Now()
		processor.observe(context.WithoutCancel(ctx), LifecycleObservation{
			State: "starting", Provider: selection.Provider(), Model: selection.Model(),
		})
		var prepared promptpreprocesscore.Session
		unsupportedProvider := false
		switch selection.Provider() {
		case domain.ProviderCodex:
			var workdir string
			workdir, err = satelliteWorkdir(processor.commands.ComputerID(), start.Key)
			if err == nil {
				prepared, err = startCodexSessionIn(ctx, selection, processor.adapterExecutable, start.ResumeProviderSessionID, workdir)
			}
		default:
			// A stateless command is not a satellite: it cannot preserve context
			// or resume an archived binding. Fail closed until that provider has
			// a persistent read-only adapter contract.
			unsupportedProvider = true
			err = ErrUnavailable
		}
		if err == nil {
			processor.observe(context.WithoutCancel(ctx), LifecycleObservation{
				State: "ready", Provider: selection.Provider(), Model: selection.Model(), Duration: time.Since(started),
			})
			if replacement {
				processor.observe(context.WithoutCancel(ctx), LifecycleObservation{
					State: "replacement_ready", Provider: selection.Provider(), Model: selection.Model(), Duration: time.Since(started),
				})
			}
			return &observedSession{session: prepared, selection: selection, processor: processor.commands}, nil
		}
		processor.observe(context.WithoutCancel(ctx), LifecycleObservation{
			State: "start_failed", Provider: selection.Provider(), Model: selection.Model(),
			Duration: time.Since(started), ErrorCategory: startupErrorCategory(err),
		})
		if !unsupportedProvider || !errors.Is(lastErr, promptpreprocesscore.ErrResumeUnavailable) {
			lastErr = err
		}
		processor.commands.Report(selection, false)
	}
	if lastErr == nil {
		lastErr = ErrUnavailable
	}
	if replacement {
		processor.observe(context.WithoutCancel(ctx), LifecycleObservation{
			State: "replacement_failed", Provider: lastProvider, Model: lastModel,
			Duration: time.Since(replacementStarted), ErrorCategory: startupErrorCategory(lastErr),
		})
	}
	return nil, lastErr
}

func startupErrorCategory(err error) string {
	if errors.Is(err, promptpreprocesscore.ErrResumeUnavailable) {
		return "thread_not_found"
	}
	return "provider"
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
		state, category := "released", ""
		if completion.err != nil {
			state, category = "release_failed", "provider"
		}
		completion.processor.observe(context.WithoutCancel(ctx), LifecycleObservation{
			State: state, Provider: completion.provider, Model: completion.model,
			Duration: time.Since(started), ErrorCategory: category,
		})
	})
	return completion.err
}

type observedSession struct {
	session   promptpreprocesscore.Session
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

func (current *observedSession) Binding() string { return current.session.Binding() }

func closeContext() (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.Background(), 10*time.Second)
}
