package runtimehost

import (
	"context"
	"errors"
	"strings"
	"sync"
	"time"
)

type LocalExecutor struct {
	nodeID string
	driver RuntimeDriver
	store  OperationStore
	ctx    context.Context
	cancel context.CancelFunc

	mu             sync.RWMutex
	submitMu       sync.Mutex
	sessions       map[string]*localSession
	active         map[string]struct{}
	accepting      bool
	workers        sync.WaitGroup
	shutdownOnce   sync.Once
	shutdownDone   chan struct{}
	shutdownErr    error
	completionMu   sync.Mutex
	completionErr  error
	namer          NameGenerator
	archiver       ArchiveWriter
	inputs         InputResolver
	now            func() time.Time
	inputTiming    func(inputDeliveryTiming)
	queueLimits    InputQueueLimitResolver
	inputConfirmer InputConfirmer
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
		accepting: true, shutdownDone: make(chan struct{}),
		now: time.Now, inputTiming: logInputDeliveryTiming,
		queueLimits: defaultInputQueueLimitResolver{},
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

func (e *LocalExecutor) SetInputQueueLimitResolver(resolver InputQueueLimitResolver) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if resolver == nil {
		resolver = defaultInputQueueLimitResolver{}
	}
	e.queueLimits = resolver
}

func (e *LocalExecutor) SetInputConfirmer(confirmer InputConfirmer) {
	e.mu.Lock()
	e.inputConfirmer = confirmer
	e.mu.Unlock()
}

// ActiveOperationIDs returns a point-in-time set used by store maintenance to
// avoid expiring an operation that can still commit a durable result.
func (e *LocalExecutor) ActiveOperationIDs() map[string]struct{} {
	e.submitMu.Lock()
	defer e.submitMu.Unlock()
	active := make(map[string]struct{}, len(e.active))
	for operationID := range e.active {
		active[operationID] = struct{}{}
	}
	return active
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
	if e.ctx.Err() != nil {
		return ErrRuntimeShuttingDown
	}
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
	if e.ctx.Err() != nil {
		return ErrRuntimeShuttingDown
	}
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
	if e.ctx.Err() != nil {
		return ErrRuntimeShuttingDown
	}
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

func (e *LocalExecutor) completeAbandoned(requests []pendingRequest, failure error) {
	for _, queued := range requests {
		request := queued.request
		fingerprint := requestFingerprint(request)
		result := Result{Accepted: true, Detail: failure.Error()}
		e.recordCompletionError(
			e.store.Complete(request.OperationID, fingerprint, result, failure),
		)
		e.submitMu.Lock()
		delete(e.active, request.OperationID)
		e.submitMu.Unlock()
		if request.Action == ActionSendInput {
			completedAt := e.now()
			queue := nonNegativeDuration(completedAt.Sub(queued.enqueuedAt))
			attachmentWait := queued.attachmentWait
			if attachmentWait > queue {
				attachmentWait = queue
			}
			e.emitInputTiming(newInputDeliveryTiming(
				request,
				RuntimeBinding{NodeID: request.NodeID, SessionID: request.SessionID},
				inputQueueTiming{
					enqueuedAt: queued.enqueuedAt, queue: queue,
					attachmentWait: attachmentWait, fifoWait: queue - attachmentWait,
				},
				inputExecutionTiming{}, result, failure, completedAt,
			))
		}
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
	if e.ctx.Err() != nil {
		return Receipt{}, ErrRuntimeShuttingDown
	}
	submittedAt := e.now()
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
	if !e.accepting {
		return Receipt{}, ErrRuntimeShuttingDown
	}
	if receipt, existing, err := e.existingOperationReceiptLocked(
		request.OperationID, fingerprint,
	); err != nil || existing {
		return receipt, err
	}
	if request.Action != ActionCapture {
		admission, admitted := session.queueAdmission(
			request.Action, e.inputQueueLimit(request.ActorID),
		)
		if !admitted {
			if !admission.available {
				return Receipt{}, ErrRuntimeUnavailable
			}
			if session.snapshot().Generation != request.ExpectedGeneration {
				return Receipt{}, ErrStaleRuntime
			}
			logRuntimeQueueBackpressure(request, admission)
			return Receipt{}, ErrQueueFull
		}
	}
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
	if !session.enqueue(request, submittedAt) {
		result := Result{Accepted: true, Detail: "runtime target became unavailable"}
		completionErr := e.store.Complete(request.OperationID, fingerprint, result, ErrRuntimeUnavailable)
		delete(e.active, request.OperationID)
		if request.Action == ActionSendInput {
			completedAt := e.now()
			e.emitInputTiming(newInputDeliveryTiming(
				request, session.snapshot(),
				inputQueueTiming{
					enqueuedAt: submittedAt,
					queue:      nonNegativeDuration(completedAt.Sub(submittedAt)),
					fifoWait:   nonNegativeDuration(completedAt.Sub(submittedAt)),
				},
				inputExecutionTiming{}, result, ErrRuntimeUnavailable, completedAt,
			))
		}
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

func (e *LocalExecutor) inputQueueLimit(actorID int64) int {
	e.mu.RLock()
	resolver := e.queueLimits
	e.mu.RUnlock()
	if resolver == nil {
		return 5
	}
	return normalizeInputQueueLimit(resolver.InputQueueLimit(actorID))
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
		ErrRuntimeUnavailable, ErrRuntimeShuttingDown, ErrStaleRuntime, ErrOperationIDConflict,
		ErrOperationOutcomeUnknown, ErrInputUnconfirmed, ErrTerminalUnavailable,
		ErrUnsupportedBackendAction,
	} {
		if detail == known.Error() {
			return known
		}
	}
	return errors.New(detail)
}

// BeginShutdown atomically closes admission and starts draining operations that
// were durably accepted but never began execution. It returns immediately so
// the control server can finish its own HTTP shutdown in parallel.
func (e *LocalExecutor) BeginShutdown() {
	e.shutdownOnce.Do(func() {
		e.submitMu.Lock()
		e.accepting = false
		e.cancel()
		e.submitMu.Unlock()

		pending := make([]pendingRequest, 0)
		e.mu.Lock()
		for key, session := range e.sessions {
			delete(e.sessions, key)
			pending = append(pending, session.stopAndDrain()...)
		}
		e.mu.Unlock()

		go func() {
			e.completeAbandoned(pending, ErrRuntimeShuttingDown)
			e.workers.Wait()
			e.completionMu.Lock()
			e.shutdownErr = e.completionErr
			e.completionMu.Unlock()
			close(e.shutdownDone)
		}()
	})
}

func (e *LocalExecutor) Shutdown(ctx context.Context) error {
	e.BeginShutdown()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-e.shutdownDone:
		return e.shutdownErr
	}
}

// ShutdownComplete reports whether all runtime workers and terminal queue
// writes have finished. The Bolt store must not be closed before it returns
// true.
func (e *LocalExecutor) ShutdownComplete() bool {
	select {
	case <-e.shutdownDone:
		return true
	default:
		return false
	}
}

func (e *LocalExecutor) recordCompletionError(err error) {
	if err == nil {
		return
	}
	e.completionMu.Lock()
	if e.completionErr == nil {
		e.completionErr = err
	}
	e.completionMu.Unlock()
}

func (e *LocalExecutor) resolveSession(request Request) (*localSession, error) {
	if e.ctx.Err() != nil {
		return nil, ErrRuntimeShuttingDown
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
