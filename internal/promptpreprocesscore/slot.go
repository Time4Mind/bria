package promptpreprocesscore

import (
	"context"
	"crypto/sha256"
	"errors"
	"sync"
	"time"

	"bria/internal/promptpreprocess"
)

type requestKey struct {
	computerID string
	sessionID  string
	messageID  string
	sequence   uint64
}

type invocation struct {
	key         requestKey
	fingerprint [32]byte
	request     promptpreprocess.Request
	done        chan struct{}
	result      promptpreprocess.Result
	err         error
	completion  *completion
}

type satelliteSlot struct {
	owner       *Manager
	factory     Factory
	store       BindingStore
	bindingMu   *sync.Mutex
	key         BindingKey
	generation  uint64
	ctx         context.Context
	cancel      context.CancelFunc
	predecessor <-chan struct{}
	retired     chan struct{}
	retiredOnce sync.Once

	mu       sync.Mutex
	current  *managedSession
	starting bool
	started  chan struct{}
	active   *invocation
	queue    []*invocation
	running  bool
	retiring bool
	closed   bool
	workers  sync.WaitGroup
}

func newSatelliteSlot(owner *Manager, parent context.Context, factory Factory, store BindingStore, bindingMu *sync.Mutex, key BindingKey, generation uint64, predecessor <-chan struct{}) *satelliteSlot {
	ctx, cancel := context.WithCancel(parent)
	return &satelliteSlot{
		owner: owner, factory: factory, store: store, bindingMu: bindingMu, key: key, generation: generation,
		ctx: ctx, cancel: cancel, predecessor: predecessor, retired: make(chan struct{}),
	}
}

func (slot *satelliteSlot) startForTopology(ctx context.Context) error {
	if slot.predecessorPending() {
		go func() { _ = slot.ensureStarted(slot.ctx) }()
		return nil
	}
	return slot.ensureStarted(ctx)
}

func (slot *satelliteSlot) predecessorPending() bool {
	if slot.predecessor == nil {
		return false
	}
	select {
	case <-slot.predecessor:
		return false
	default:
		return true
	}
}

func (slot *satelliteSlot) markRetiring() bool {
	slot.mu.Lock()
	defer slot.mu.Unlock()
	slot.retiring = true
	return slot.active == nil && len(slot.queue) == 0 && !slot.running
}

func (slot *satelliteSlot) ensureStarted(ctx context.Context) error {
	if slot.predecessor != nil {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-slot.predecessor:
		}
	}
	for {
		slot.mu.Lock()
		if slot.closed {
			slot.mu.Unlock()
			return ErrUnavailable
		}
		if slot.current != nil {
			slot.mu.Unlock()
			return nil
		}
		if slot.starting {
			started := slot.started
			slot.mu.Unlock()
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-started:
				continue
			}
		}
		slot.starting = true
		slot.started = make(chan struct{})
		started := slot.started
		slot.workers.Add(1)
		slot.mu.Unlock()

		slot.bindingMu.Lock()
		binding, found, err := slot.store.Load(ctx, slot.key)
		slot.bindingMu.Unlock()
		resumeID := ""
		if err == nil && found {
			resumeID = binding.ProviderSessionID
		}
		var prepared Session
		if err == nil {
			startContext, cancel := context.WithTimeout(slot.ctx, 30*time.Second)
			prepared, err = slot.factory(startContext, StartRequest{Key: slot.key, ResumeProviderSessionID: resumeID})
			cancel()
		}
		if err == nil {
			slot.mu.Lock()
			closed := slot.closed
			slot.mu.Unlock()
			slot.bindingMu.Lock()
			latest, latestFound, loadErr := slot.store.Load(context.WithoutCancel(ctx), slot.key)
			if loadErr != nil {
				err = loadErr
			} else if latestFound {
				latest.ProviderSessionID = prepared.Binding()
				err = slot.store.Save(context.WithoutCancel(ctx), latest)
			} else if !closed {
				err = slot.store.Save(context.WithoutCancel(ctx), Binding{Key: slot.key, Desired: DesiredActive, ProviderSessionID: prepared.Binding()})
			}
			slot.bindingMu.Unlock()
		}

		slot.mu.Lock()
		stale := slot.closed
		if err == nil && !stale {
			slot.current = &managedSession{session: prepared}
		}
		slot.starting = false
		close(started)
		slot.mu.Unlock()
		slot.workers.Done()
		if prepared != nil && (err != nil || stale) {
			closeCtx, cancel := closeContext()
			_ = prepared.Close(closeCtx)
			cancel()
		}
		if stale && err == nil {
			return ErrUnavailable
		}
		return err
	}
}

func (slot *satelliteSlot) Process(ctx context.Context, request promptpreprocess.Request) (promptpreprocess.Result, error) {
	key, fingerprint := invocationIdentity(request)
	slot.mu.Lock()
	if slot.closed {
		slot.mu.Unlock()
		return promptpreprocess.Result{}, ErrUnavailable
	}
	current, err := slot.findInvocationLocked(key, fingerprint)
	if err != nil {
		slot.mu.Unlock()
		return promptpreprocess.Result{}, err
	}
	if current == nil {
		current = &invocation{key: key, fingerprint: fingerprint, request: request, done: make(chan struct{})}
		current.completion = &completion{slot: slot, invocation: current}
		slot.queue = append(slot.queue, current)
		slot.runNextLocked()
	}
	slot.mu.Unlock()
	select {
	case <-ctx.Done():
		return promptpreprocess.Result{}, ctx.Err()
	case <-current.done:
		return current.result, current.err
	}
}

func (slot *satelliteSlot) matches(request promptpreprocess.Request) bool {
	key, fingerprint := invocationIdentity(request)
	slot.mu.Lock()
	defer slot.mu.Unlock()
	current, err := slot.findInvocationLocked(key, fingerprint)
	return err == nil && current != nil
}

func invocationIdentity(request promptpreprocess.Request) (requestKey, [32]byte) {
	key := requestKey{computerID: string(request.ComputerID), sessionID: string(request.SessionID), messageID: request.MessageID, sequence: request.Sequence}
	return key, sha256.Sum256([]byte(request.Instruction + "\x00" + request.Text))
}

func (slot *satelliteSlot) findInvocationLocked(key requestKey, fingerprint [32]byte) (*invocation, error) {
	if slot.active != nil && slot.active.key == key {
		if slot.active.fingerprint != fingerprint {
			return nil, ErrInvocation
		}
		return slot.active, nil
	}
	for _, queued := range slot.queue {
		if queued.key != key {
			continue
		}
		if queued.fingerprint != fingerprint {
			return nil, ErrInvocation
		}
		return queued, nil
	}
	return nil, nil
}

func (slot *satelliteSlot) runNextLocked() {
	if slot.running || slot.active != nil || slot.closed || len(slot.queue) == 0 {
		return
	}
	current := slot.queue[0]
	slot.queue = slot.queue[1:]
	slot.active = current
	slot.running = true
	slot.workers.Add(1)
	go slot.run(current)
}

func (slot *satelliteSlot) run(current *invocation) {
	defer slot.workers.Done()
	err := slot.ensureStarted(slot.ctx)
	var result promptpreprocess.Result
	if err == nil {
		slot.mu.Lock()
		provider := slot.current
		slot.mu.Unlock()
		if provider == nil {
			err = ErrUnavailable
		} else {
			result, err = provider.Process(slot.ctx, current.request)
		}
	}
	if err == nil {
		result.Completion = current.completion
	}
	slot.mu.Lock()
	current.result, current.err = result, err
	close(current.done)
	slot.running = false
	if err != nil {
		if slot.active == current {
			slot.active = nil
		}
		provider := slot.current
		slot.current = nil
		slot.runNextLocked()
		retire := slot.retiring && slot.active == nil && len(slot.queue) == 0 && !slot.running
		slot.mu.Unlock()
		if provider != nil {
			closeCtx, cancel := closeContext()
			_ = provider.Close(closeCtx)
			cancel()
		}
		if retire {
			go func() {
				closeCtx, cancel := closeContext()
				_ = slot.owner.retireSlot(closeCtx, slot)
				cancel()
			}()
		}
		return
	}
	slot.mu.Unlock()
}

func (slot *satelliteSlot) accept(ctx context.Context, current *invocation) error {
	slot.mu.Lock()
	if slot.active == current {
		slot.active = nil
		slot.runNextLocked()
	}
	retire := slot.retiring && slot.active == nil && len(slot.queue) == 0 && !slot.running
	slot.mu.Unlock()
	if retire {
		return slot.owner.retireSlot(ctx, slot)
	}
	return nil
}

func (slot *satelliteSlot) Close(ctx context.Context) error {
	if slot == nil {
		return nil
	}
	slot.mu.Lock()
	if slot.closed {
		slot.mu.Unlock()
		return nil
	}
	slot.closed = true
	slot.cancel()
	provider := slot.current
	slot.current = nil
	queued := slot.queue
	slot.queue = nil
	for _, current := range queued {
		current.err = ErrUnavailable
		close(current.done)
	}
	slot.mu.Unlock()
	var result error
	if provider != nil {
		result = provider.Close(ctx)
	}
	done := make(chan struct{})
	go func() { slot.workers.Wait(); close(done) }()
	select {
	case <-done:
		slot.signalRetired()
	case <-ctx.Done():
		result = errors.Join(result, ctx.Err())
		go func() {
			<-done
			slot.signalRetired()
		}()
	}
	return result
}

func (slot *satelliteSlot) signalRetired() {
	slot.retiredOnce.Do(func() { close(slot.retired) })
}

type completion struct {
	once       sync.Once
	slot       *satelliteSlot
	invocation *invocation
	err        error
}

func (completed *completion) Accept(ctx context.Context) error {
	completed.once.Do(func() { completed.err = completed.slot.accept(ctx, completed.invocation) })
	return completed.err
}

type managedSession struct {
	session Session
	once    sync.Once
	err     error
}

func (current *managedSession) Process(ctx context.Context, request promptpreprocess.Request) (promptpreprocess.Result, error) {
	return current.session.Process(ctx, request)
}

func (current *managedSession) Binding() string { return current.session.Binding() }

func (current *managedSession) Close(ctx context.Context) error {
	current.once.Do(func() { current.err = current.session.Close(ctx) })
	return current.err
}

func closeContext() (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.Background(), 10*time.Second)
}
