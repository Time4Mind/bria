package promptpreprocesssession

import (
	"context"
	"crypto/sha256"
	"errors"
	"sync"
	"time"

	"bria/internal/promptpreprocess"
)

type session interface {
	Process(context.Context, promptpreprocess.Request) (promptpreprocess.Result, error)
	Close(context.Context) error
}

type sessionFactory func(context.Context) (session, error)

type sessionPool struct {
	factory sessionFactory

	rootContext context.Context
	cancelRoot  context.CancelFunc

	mu         sync.Mutex
	ready      *managedSession
	active     *invocation
	starting   bool
	startErr   error
	changed    chan struct{}
	closed     bool
	generation uint64
	starts     sync.WaitGroup
}

func newSessionPool(factory sessionFactory) *sessionPool {
	ctx, cancel := context.WithCancel(context.Background())
	return &sessionPool{factory: factory, rootContext: ctx, cancelRoot: cancel, changed: make(chan struct{})}
}

func (pool *sessionPool) Warmup(context.Context) {
	pool.mu.Lock()
	pool.startLocked()
	pool.mu.Unlock()
}

func (pool *sessionPool) Process(ctx context.Context, request promptpreprocess.Request) (promptpreprocess.Result, promptpreprocess.Completion, error) {
	current, owner, err := pool.acquire(ctx, request)
	if err != nil {
		return promptpreprocess.Result{}, nil, err
	}
	if !owner {
		select {
		case <-ctx.Done():
			return promptpreprocess.Result{}, nil, ctx.Err()
		case <-current.done:
			return current.result, current.completion, current.err
		}
	}
	result, processErr := current.session.Process(ctx, request)
	pool.mu.Lock()
	current.result = result
	current.err = processErr
	close(current.done)
	pool.signalLocked()
	pool.mu.Unlock()
	return result, current.completion, processErr
}

func (pool *sessionPool) acquire(ctx context.Context, request promptpreprocess.Request) (*invocation, bool, error) {
	key := requestKey{computerID: string(request.ComputerID), sessionID: string(request.SessionID), messageID: request.MessageID, sequence: request.Sequence}
	fingerprint := sha256.Sum256([]byte(request.Instruction + "\x00" + request.Text))
	for {
		pool.mu.Lock()
		if pool.closed {
			pool.mu.Unlock()
			return nil, false, ErrUnavailable
		}
		if pool.active != nil {
			active := pool.active
			if active.key == key {
				if active.fingerprint != fingerprint {
					pool.mu.Unlock()
					return nil, false, ErrInvocation
				}
				pool.mu.Unlock()
				return active, false, nil
			}
			changed := pool.changed
			pool.mu.Unlock()
			select {
			case <-ctx.Done():
				return nil, false, ctx.Err()
			case <-changed:
			}
			continue
		}
		if pool.ready != nil {
			prepared := pool.ready
			pool.ready = nil
			current := &invocation{key: key, fingerprint: fingerprint, session: prepared, done: make(chan struct{})}
			current.completion = &completion{pool: pool, invocation: current}
			pool.active = current
			pool.startLocked()
			pool.mu.Unlock()
			return current, true, nil
		}
		if pool.startErr != nil {
			err := pool.startErr
			pool.startErr = nil
			pool.startLocked()
			pool.mu.Unlock()
			return nil, false, err
		}
		pool.startLocked()
		changed := pool.changed
		pool.mu.Unlock()
		select {
		case <-ctx.Done():
			return nil, false, ctx.Err()
		case <-changed:
		}
	}
}

func (pool *sessionPool) startLocked() {
	if pool.closed || pool.starting || pool.ready != nil {
		return
	}
	pool.starting = true
	generation := pool.generation
	pool.starts.Add(1)
	go func() {
		defer pool.starts.Done()
		startContext, cancel := context.WithTimeout(pool.rootContext, 30*time.Second)
		defer cancel()
		prepared, err := pool.factory(startContext)
		pool.mu.Lock()
		pool.starting = false
		stale := generation != pool.generation
		discard := false
		if err != nil {
			if !stale {
				pool.startErr = err
				pool.scheduleRetryLocked(generation)
			}
		} else if pool.closed || stale {
			discard = true
		} else {
			pool.ready = &managedSession{session: prepared}
			pool.startErr = nil
		}
		if stale && !discard {
			pool.startLocked()
		}
		pool.signalLocked()
		pool.mu.Unlock()
		if !discard {
			return
		}
		closeCtx, closeCancel := closeContext()
		_ = (&managedSession{session: prepared}).Close(closeCtx)
		closeCancel()
		pool.mu.Lock()
		if stale {
			pool.startLocked()
		}
		pool.signalLocked()
		pool.mu.Unlock()
	}()
}

func (pool *sessionPool) scheduleRetryLocked(generation uint64) {
	pool.starts.Add(1)
	go func() {
		defer pool.starts.Done()
		timer := time.NewTimer(2 * time.Second)
		defer timer.Stop()
		select {
		case <-pool.rootContext.Done():
			return
		case <-timer.C:
		}
		pool.mu.Lock()
		if !pool.closed && generation == pool.generation {
			pool.startLocked()
		}
		pool.mu.Unlock()
	}()
}

func (pool *sessionPool) Invalidate() {
	pool.mu.Lock()
	if pool.closed {
		pool.mu.Unlock()
		return
	}
	pool.generation++
	ready := pool.ready
	pool.ready = nil
	pool.startErr = nil
	pool.signalLocked()
	pool.startLocked()
	pool.mu.Unlock()
	if ready != nil {
		ctx, cancel := closeContext()
		defer cancel()
		_ = ready.Close(ctx)
	}
}

func (pool *sessionPool) signalLocked() {
	close(pool.changed)
	pool.changed = make(chan struct{})
}

func (pool *sessionPool) Close(ctx context.Context) error {
	pool.mu.Lock()
	if pool.closed {
		pool.mu.Unlock()
		return nil
	}
	pool.closed = true
	pool.cancelRoot()
	ready, active := pool.ready, pool.active
	pool.ready, pool.active = nil, nil
	pool.signalLocked()
	pool.mu.Unlock()

	done := make(chan struct{})
	go func() { pool.starts.Wait(); close(done) }()
	var closeErr error
	select {
	case <-done:
	case <-ctx.Done():
		closeErr = ctx.Err()
	}
	if ready != nil {
		closeErr = errors.Join(closeErr, ready.Close(ctx))
	}
	if active != nil {
		closeErr = errors.Join(closeErr, active.session.Close(ctx))
	}
	return closeErr
}

type requestKey struct {
	computerID string
	sessionID  string
	messageID  string
	sequence   uint64
}

type invocation struct {
	key         requestKey
	fingerprint [32]byte
	session     *managedSession
	completion  *completion
	done        chan struct{}
	result      promptpreprocess.Result
	err         error
}

type completion struct {
	once       sync.Once
	pool       *sessionPool
	invocation *invocation
	err        error
}

func (completed *completion) Accept(context.Context) error {
	completed.once.Do(func() {
		closeCtx, cancel := closeContext()
		completed.err = completed.invocation.session.Close(closeCtx)
		cancel()
		completed.pool.mu.Lock()
		if completed.pool.active == completed.invocation {
			completed.pool.active = nil
			completed.pool.signalLocked()
		}
		completed.pool.mu.Unlock()
	})
	return completed.err
}

type managedSession struct {
	session session
	once    sync.Once
	err     error
}

func (current *managedSession) Process(ctx context.Context, request promptpreprocess.Request) (promptpreprocess.Result, error) {
	return current.session.Process(ctx, request)
}

func (current *managedSession) Close(ctx context.Context) error {
	current.once.Do(func() { current.err = current.session.Close(ctx) })
	return current.err
}
