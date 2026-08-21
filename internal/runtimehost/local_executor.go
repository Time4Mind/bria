package runtimehost

import (
	"context"
	"errors"
	"strings"
	"sync"
)

type LocalExecutor struct {
	nodeID string
	driver RuntimeDriver
	store  OperationStore
	ctx    context.Context
	cancel context.CancelFunc

	mu       sync.RWMutex
	submitMu sync.Mutex
	sessions map[string]*localSession
	active   map[string]struct{}
	workers  sync.WaitGroup
	namer    NameGenerator
	archiver ArchiveWriter
	inputs   InputResolver
}

type NameGenerator interface {
	Generate(context.Context, string, string) (string, error)
}

type ArchiveWriter interface {
	Commit(context.Context, Request) error
	Finalize(context.Context, Request) error
}

func NewLocalExecutor(
	nodeID string,
	driver RuntimeDriver,
	store OperationStore,
) (*LocalExecutor, error) {
	if strings.TrimSpace(nodeID) == "" {
		return nil, errors.New("node id is required")
	}
	if driver == nil || store == nil {
		return nil, errors.New("runtime driver and operation store are required")
	}
	ctx, cancel := context.WithCancel(context.Background())
	return &LocalExecutor{
		nodeID: nodeID, driver: driver, store: store, ctx: ctx, cancel: cancel,
		sessions: make(map[string]*localSession), active: make(map[string]struct{}),
	}, nil
}

func (e *LocalExecutor) SetNameGenerator(generator NameGenerator) {
	e.mu.Lock()
	e.namer = generator
	e.mu.Unlock()
}

func (e *LocalExecutor) SetArchiveWriter(writer ArchiveWriter) {
	e.mu.Lock()
	e.archiver = writer
	e.mu.Unlock()
}

func (e *LocalExecutor) SetInputResolver(resolver InputResolver) {
	e.mu.Lock()
	e.inputs = resolver
	e.mu.Unlock()
}

// PrepareRecovery replaces an older archived runtime incarnation with an
// unattached binding. Commands for the restored generation can then queue
// before the provider session has finished resuming.
func (e *LocalExecutor) PrepareRecovery(binding RuntimeBinding) error {
	if err := validatePreparedBinding(binding, e.nodeID); err != nil {
		return err
	}
	key := runtimeKey(binding.NodeID, binding.SessionID)
	e.mu.Lock()
	defer e.mu.Unlock()
	current := e.sessions[key]
	if current != nil {
		currentBinding := current.snapshot()
		if current.matchesIdentity(binding) {
			return nil
		}
		if currentBinding.Generation >= binding.Generation {
			return ErrStaleRuntime
		}
		delete(e.sessions, key)
		e.completeAbandoned(current.stopAndDrain(), ErrStaleRuntime)
	}
	session := &localSession{binding: binding, executor: e}
	session.wake = sync.NewCond(&session.mu)
	e.sessions[key] = session
	e.workers.Add(1)
	go session.run()
	return nil
}

func (e *LocalExecutor) Register(binding RuntimeBinding) error {
	if err := validateBinding(binding, e.nodeID); err != nil {
		return err
	}
	key := runtimeKey(binding.NodeID, binding.SessionID)
	e.mu.Lock()
	defer e.mu.Unlock()
	current := e.sessions[key]
	if current != nil {
		if current.attach(binding) {
			return nil
		}
		return ErrStaleRuntime
	}
	session := &localSession{binding: binding, executor: e}
	session.wake = sync.NewCond(&session.mu)
	e.sessions[key] = session
	e.workers.Add(1)
	go session.run()
	return nil
}

// Prepare registers an exact runtime incarnation before its tmux target is
// restored. Commands are durably accepted into the same per-session FIFO and
// remain there until Register attaches the recovered target.
func (e *LocalExecutor) Prepare(binding RuntimeBinding) error {
	if err := validatePreparedBinding(binding, e.nodeID); err != nil {
		return err
	}
	key := runtimeKey(binding.NodeID, binding.SessionID)
	e.mu.Lock()
	defer e.mu.Unlock()
	current := e.sessions[key]
	if current != nil {
		if current.matchesIdentity(binding) {
			return nil
		}
		return ErrStaleRuntime
	}
	session := &localSession{binding: binding, executor: e}
	session.wake = sync.NewCond(&session.mu)
	e.sessions[key] = session
	e.workers.Add(1)
	go session.run()
	return nil
}

func (e *LocalExecutor) Unregister(nodeID, sessionID string, generation uint64) error {
	key := runtimeKey(nodeID, sessionID)
	e.mu.Lock()
	defer e.mu.Unlock()
	current := e.sessions[key]
	if current == nil {
		return ErrRuntimeUnavailable
	}
	if current.snapshot().Generation != generation {
		return ErrStaleRuntime
	}
	delete(e.sessions, key)
	e.completeAbandoned(current.stopAndDrain(), ErrRuntimeUnavailable)
	return nil
}

func (e *LocalExecutor) completeAbandoned(requests []Request, failure error) {
	for _, request := range requests {
		fingerprint := requestFingerprint(request)
		result := Result{Accepted: true, Detail: failure.Error()}
		_ = e.store.Complete(request.OperationID, fingerprint, result, failure)
		e.submitMu.Lock()
		delete(e.active, request.OperationID)
		e.submitMu.Unlock()
	}
}

func (e *LocalExecutor) retireClosedRuntime(binding RuntimeBinding) {
	key := runtimeKey(binding.NodeID, binding.SessionID)
	e.mu.Lock()
	session := e.sessions[key]
	if session == nil || session.snapshot().Generation != binding.Generation {
		e.mu.Unlock()
		return
	}
	delete(e.sessions, key)
	pending := session.stopAndDrain()
	e.mu.Unlock()
	e.completeAbandoned(pending, ErrRuntimeUnavailable)
}

// Submit persists the operation intent and appends it to the host-local FIFO.
// It never waits for tmux or provider processing.
func (e *LocalExecutor) Submit(ctx context.Context, request Request) (Receipt, error) {
	if err := ctx.Err(); err != nil {
		return Receipt{}, err
	}
	if err := request.validate(); err != nil {
		return Receipt{}, err
	}
	fingerprint := requestFingerprint(request)
	if receipt, existing, err := e.existingOperationReceipt(request.OperationID, fingerprint); err != nil || existing {
		return receipt, err
	}
	session, err := e.resolveSession(request)
	if err != nil {
		return Receipt{}, err
	}

	e.submitMu.Lock()
	defer e.submitMu.Unlock()
	record, created, err := e.store.CreatePending(request.OperationID, fingerprint, request.Action)
	if err != nil {
		return Receipt{}, err
	}
	if !created {
		if record.State == OperationCompleted {
			return Receipt{
				OperationID: request.OperationID, Accepted: true, Duplicate: true,
				Detail: "operation already completed",
			}, nil
		}
		if _, active := e.active[request.OperationID]; !active {
			return Receipt{}, ErrOperationOutcomeUnknown
		}
		return Receipt{
			OperationID: request.OperationID, Accepted: true, Duplicate: true,
			Detail: "operation already queued",
		}, nil
	}
	e.active[request.OperationID] = struct{}{}
	if request.Action == ActionCapture {
		// Pane capture is read-only and must not wait behind a provider input or
		// archive operation. Execute it on a bounded independent lane; identity is
		// revalidated by executeOnce immediately before tmux is touched.
		binding := session.snapshot()
		e.workers.Add(1)
		go e.runReadOnlyCapture(request, binding)
		return Receipt{
			OperationID: request.OperationID, Accepted: true,
			Detail: "read-only capture accepted",
		}, nil
	}
	if !session.enqueue(request) {
		result := Result{Accepted: true, Detail: "runtime target became unavailable"}
		completionErr := e.store.Complete(request.OperationID, fingerprint, result, ErrRuntimeUnavailable)
		delete(e.active, request.OperationID)
		if completionErr != nil {
			return Receipt{}, errors.Join(ErrRuntimeUnavailable, completionErr)
		}
		return Receipt{}, ErrRuntimeUnavailable
	}
	return Receipt{
		OperationID: request.OperationID, Accepted: true,
		Detail: "operation queued",
	}, nil
}

func (e *LocalExecutor) LookupResult(
	ctx context.Context,
	operationID string,
) (Result, bool, error) {
	if err := ctx.Err(); err != nil {
		return Result{}, false, err
	}
	record, found, err := e.store.Lookup(operationID)
	if err != nil || !found || record.State != OperationCompleted {
		return Result{}, false, err
	}
	result := record.Result
	if record.Error != "" {
		return result, true, storedOperationError(record.Error)
	}
	return result, true, nil
}

func storedOperationError(detail string) error {
	for _, known := range []error{
		ErrRuntimeUnavailable, ErrStaleRuntime, ErrOperationIDConflict,
		ErrOperationOutcomeUnknown, ErrTerminalUnavailable, ErrUnsupportedBackendAction,
	} {
		if detail == known.Error() {
			return known
		}
	}
	return errors.New(detail)
}

func (e *LocalExecutor) Shutdown(ctx context.Context) error {
	e.cancel()
	e.mu.RLock()
	for _, session := range e.sessions {
		session.mu.Lock()
		session.wake.Broadcast()
		session.mu.Unlock()
	}
	e.mu.RUnlock()
	done := make(chan struct{})
	go func() {
		e.workers.Wait()
		close(done)
	}()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-done:
		return nil
	}
}

func (e *LocalExecutor) resolveSession(request Request) (*localSession, error) {
	if e.ctx.Err() != nil {
		return nil, ErrRuntimeUnavailable
	}
	if request.NodeID != e.nodeID {
		return nil, ErrRuntimeUnavailable
	}
	e.mu.RLock()
	session := e.sessions[runtimeKey(request.NodeID, request.SessionID)]
	e.mu.RUnlock()
	if session == nil {
		return nil, ErrRuntimeUnavailable
	}
	binding := session.snapshot()
	if binding.Generation != request.ExpectedGeneration ||
		!strings.EqualFold(binding.Backend, request.Backend) {
		return nil, ErrStaleRuntime
	}
	return session, nil
}
