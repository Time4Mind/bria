package supervisioncomposition_test

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"bria/internal/app"
	"bria/internal/domain"
	"bria/internal/sessionsupervisor"
	"bria/internal/supervisioncomposition"
)

type stableBarrierError struct{ revision string }

func (err stableBarrierError) Error() string                         { return "accepted turn provider history is unverifiable" }
func (err stableBarrierError) StableRecoveryBarrierRevision() string { return err.revision }

type mutableStableBarrierReconciler struct {
	mu       sync.Mutex
	revision string
	calls    int
}

func (reconciler *mutableStableBarrierReconciler) ReconcileAcceptedTurns(context.Context, domain.SessionID, domain.ProviderBinding) (sessionsupervisor.AcceptedTurnReconciliation, error) {
	reconciler.mu.Lock()
	defer reconciler.mu.Unlock()
	reconciler.calls++
	return sessionsupervisor.AcceptedTurnReconciliation{}, stableBarrierError{revision: reconciler.revision}
}

func (reconciler *mutableStableBarrierReconciler) RecoveryEvidenceRevision(context.Context, domain.SessionID, domain.ProviderBinding) (string, error) {
	reconciler.mu.Lock()
	defer reconciler.mu.Unlock()
	return reconciler.revision, nil
}

func (reconciler *mutableStableBarrierReconciler) setRevision(revision string) {
	reconciler.mu.Lock()
	reconciler.revision = revision
	reconciler.mu.Unlock()
}

func (reconciler *mutableStableBarrierReconciler) attempts() int {
	reconciler.mu.Lock()
	defer reconciler.mu.Unlock()
	return reconciler.calls
}

func TestStableRecoveryBarrierRetriesOnlyAfterEvidenceRevisionChanges(t *testing.T) {
	ready, _ := readySession(t)
	awaiting, err := ready.AwaitRecoveryAt(ready.StateChangedAt())
	if err != nil {
		t.Fatal(err)
	}
	store := &memoryStore{session: awaiting}
	runtime := &attachRuntime{runtimeStub: &runtimeStub{}}
	reconciler := &mutableStableBarrierReconciler{revision: "evidence-v1"}
	var reports atomic.Int32
	manager, err := supervisioncomposition.New(supervisioncomposition.Options{
		LocalComputerID: "computer", Store: store, Restarter: runtime, Waiter: runtime,
		AcceptedTurns: reconciler, MaxRestartAttempts: 1, SweepInterval: 5 * time.Millisecond,
		Now: time.Now, WaitBeforeRetry: func(context.Context, int) error { return nil },
		Report: func(error) {}, ReportSession: func(domain.SessionID, error) { reports.Add(1) },
	})
	if err != nil {
		t.Fatal(err)
	}
	startup, err := manager.RecoverStartup(context.Background())
	if err != nil || startup.Awaiting != 1 || reconciler.attempts() != 1 || reports.Load() != 1 {
		t.Fatalf("startup = %+v, %v attempts=%d reports=%d", startup, err, reconciler.attempts(), reports.Load())
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- manager.Run(ctx) }()
	defer stopRecoveryManager(t, cancel, done)
	time.Sleep(30 * time.Millisecond)
	if reconciler.attempts() != 1 || reports.Load() != 1 {
		t.Fatalf("unchanged barrier retried immediately: attempts=%d reports=%d", reconciler.attempts(), reports.Load())
	}
	reconciler.setRevision("evidence-v2")
	deadline := time.Now().Add(time.Second)
	for (reconciler.attempts() < 2 || reports.Load() < 2) && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if reconciler.attempts() != 2 || reports.Load() != 2 {
		t.Fatalf("changed evidence attempts=%d reports=%d, want 2/2", reconciler.attempts(), reports.Load())
	}
	time.Sleep(30 * time.Millisecond)
	if reconciler.attempts() != 2 || reports.Load() != 2 {
		t.Fatalf("new stable barrier repeated: attempts=%d reports=%d", reconciler.attempts(), reports.Load())
	}
	current, err := store.Load(context.Background(), awaiting.ID())
	if err != nil || !current.Equal(awaiting) {
		t.Fatalf("stable barrier changed custody session: %+v, %v", current, err)
	}
	prior, bound := current.Binding()
	if !bound {
		t.Fatal("awaiting session lost binding")
	}
	nextBinding := prior
	nextBinding.Generation++
	changedReady, err := current.Recovered(nextBinding, time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	changedIdentity, err := changedReady.AwaitRecoveryAt(time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Replace(context.Background(), current, changedIdentity); err != nil {
		t.Fatal(err)
	}
	deadline = time.Now().Add(time.Second)
	for (reconciler.attempts() < 3 || reports.Load() < 3) && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if reconciler.attempts() != 3 || reports.Load() != 3 {
		t.Fatalf("changed identity attempts=%d reports=%d, want 3/3", reconciler.attempts(), reports.Load())
	}
	time.Sleep(30 * time.Millisecond)
	if reconciler.attempts() != 3 || reports.Load() != 3 {
		t.Fatalf("changed identity retried more than once: attempts=%d reports=%d", reconciler.attempts(), reports.Load())
	}
}

type failingAttachRuntime struct {
	*runtimeStub
	attempts atomic.Int32
	reports  atomic.Int32
	started  chan struct{}
	err      error
}

type recoveryClock struct{ unixNano atomic.Int64 }

func newRecoveryClock(at time.Time) *recoveryClock {
	clock := &recoveryClock{}
	clock.unixNano.Store(at.UnixNano())
	return clock
}

func (clock *recoveryClock) Now() time.Time {
	return time.Unix(0, clock.unixNano.Load()).UTC()
}

func (clock *recoveryClock) Advance(delta time.Duration) {
	clock.unixNano.Add(int64(delta))
}

func (*failingAttachRuntime) SupportsAttach(domain.Provider) bool { return true }

func (*failingAttachRuntime) Wait(context.Context, domain.SessionID, domain.ProviderBinding) error {
	return errors.New("provider process exited")
}

func (runtime *failingAttachRuntime) Attach(context.Context, app.StartSessionRequest) (domain.ProviderBinding, error) {
	runtime.attempts.Add(1)
	select {
	case runtime.started <- struct{}{}:
	default:
	}
	if runtime.err != nil {
		return domain.ProviderBinding{}, runtime.err
	}
	return domain.ProviderBinding{}, errors.New("provider attach failed")
}

func (*failingAttachRuntime) Detach(context.Context, app.StartSessionRequest, domain.ProviderBinding) error {
	return nil
}

func TestLiveRecoveryFailureDoesNotRelaunchEverySweep(t *testing.T) {
	manager, runtime, _, _ := newFailingRecoveryManager(t, nil)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- manager.Run(ctx) }()
	defer stopRecoveryManager(t, cancel, done)
	waitForRecoveryAttempts(t, runtime, 1)
	assertRecoveryAttemptsStay(t, runtime, 1)
}

func TestLiveRecoveryRetriesAfterBaseCooldown(t *testing.T) {
	manager, runtime, clock, _ := newFailingRecoveryManager(t, nil)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- manager.Run(ctx) }()
	defer stopRecoveryManager(t, cancel, done)
	waitForRecoveryAttempts(t, runtime, 1)
	clock.Advance(time.Minute - time.Second)
	assertRecoveryAttemptsStay(t, runtime, 1)
	clock.Advance(time.Second)
	waitForRecoveryAttempts(t, runtime, 2)
}

func TestLiveRecoveryBackoffGrowsAndCaps(t *testing.T) {
	manager, runtime, clock, _ := newFailingRecoveryManager(t, nil)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- manager.Run(ctx) }()
	defer stopRecoveryManager(t, cancel, done)
	waitForRecoveryAttempts(t, runtime, 1)

	delays := []time.Duration{
		time.Minute, 2 * time.Minute, 4 * time.Minute, 8 * time.Minute,
		16 * time.Minute, 20 * time.Minute, 20 * time.Minute,
	}
	for index, delay := range delays {
		clock.Advance(delay - time.Second)
		assertRecoveryAttemptsStay(t, runtime, int32(index+1))
		clock.Advance(time.Second)
		waitForRecoveryAttempts(t, runtime, int32(index+2))
	}
}

func TestLiveRecoveryContinuesAfterFormerFailureBudget(t *testing.T) {
	var reports atomic.Int32
	manager, runtime, clock, _ := newFailingRecoveryManager(t, func(error) { reports.Add(1) })
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- manager.Run(ctx) }()
	defer stopRecoveryManager(t, cancel, done)
	waitForRecoveryAttempts(t, runtime, 1)
	for want := int32(2); want <= 4; want++ {
		clock.Advance(20 * time.Minute)
		waitForRecoveryAttempts(t, runtime, want)
	}
	waitForReports(t, &reports, 4)
	if got := reports.Load(); got != runtime.attempts.Load() {
		t.Fatalf("reports = %d, attempts = %d; want one report per failed attempt", got, runtime.attempts.Load())
	}
}

func TestLiveRecoveryBackoffResetsForChangedBinding(t *testing.T) {
	manager, runtime, clock, store := newFailingRecoveryManager(t, nil)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- manager.Run(ctx) }()
	defer stopRecoveryManager(t, cancel, done)
	waitForRecoveryAttempts(t, runtime, 1)

	current, err := store.Load(ctx, "123e4567-e89b-12d3-a456-426614174000")
	if err != nil {
		t.Fatal(err)
	}
	prior, bound := current.Binding()
	if !bound {
		t.Fatal("awaiting session lost binding")
	}
	clock.Advance(time.Second)
	nextBinding := prior
	nextBinding.Generation++
	recovered, err := current.Recovered(nextBinding, clock.Now())
	if err != nil {
		t.Fatal(err)
	}
	clock.Advance(time.Second)
	nextAwaiting, err := recovered.AwaitRecoveryAt(clock.Now())
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Replace(ctx, current, nextAwaiting); err != nil {
		t.Fatal(err)
	}
	waitForRecoveryAttempts(t, runtime, 2)
}

func TestLiveRecoveryBackoffResetsForChangedLifecycle(t *testing.T) {
	manager, runtime, clock, store := newFailingRecoveryManager(t, nil)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- manager.Run(ctx) }()
	defer stopRecoveryManager(t, cancel, done)
	waitForRecoveryAttempts(t, runtime, 1)

	current, err := store.Load(ctx, "123e4567-e89b-12d3-a456-426614174000")
	if err != nil {
		t.Fatal(err)
	}
	if _, bound := current.Binding(); !bound {
		t.Fatal("awaiting session lost binding")
	}
	clock.Advance(time.Second)
	snapshot := current.Snapshot()
	snapshot.StateChangedAt = clock.Now()
	changed, err := domain.RestoreSession(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Replace(ctx, current, changed); err != nil {
		t.Fatal(err)
	}
	waitForRecoveryAttempts(t, runtime, 2)
}

func TestLiveRecoveryRestartsUnboundInitialSession(t *testing.T) {
	starting, err := domain.NewStartingSession("77777777-7777-4777-8777-777777777777", "intent-unbound", "computer", domain.ProviderCodex, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	awaiting, err := starting.AwaitRecovery()
	if err != nil {
		t.Fatal(err)
	}
	store := &memoryStore{session: awaiting}
	runtime := &unboundRecoveryRuntime{runtimeStub: &runtimeStub{}, started: make(chan struct{}, 1)}
	manager, err := supervisioncomposition.New(supervisioncomposition.Options{
		LocalComputerID: "computer", Store: store, Restarter: &runtimeStub{}, InitialRestarter: runtime, Waiter: runtime,
		AcceptedTurns: acceptedStub{}, MaxRestartAttempts: 3, SweepInterval: 5 * time.Millisecond,
		Now: time.Now, WaitBeforeRetry: func(context.Context, int) error { return nil }, Report: func(error) {},
	})
	if err != nil {
		t.Fatal(err)
	}
	startup, err := manager.RecoverStartup(context.Background())
	if err != nil || startup.Awaiting != 1 {
		t.Fatalf("RecoverStartup() = (%#v, %v), want one deferred awaiting session", startup, err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- manager.Run(ctx) }()
	select {
	case <-runtime.started:
	case <-time.After(time.Second):
		cancel()
		t.Fatal("unbound initial recovery did not start")
	}
	deadline := time.Now().Add(time.Second)
	for {
		current, loadErr := store.Load(context.Background(), awaiting.ID())
		if loadErr == nil && current.Status() == domain.SessionReady {
			binding, bound := current.Binding()
			if !bound || binding.SessionID != "provider-unbound-recovered" {
				t.Fatalf("recovered binding = (%#v, %t)", binding, bound)
			}
			break
		}
		if time.Now().After(deadline) {
			cancel()
			t.Fatalf("unbound session remained %q: %v", current.Status(), loadErr)
		}
		time.Sleep(time.Millisecond)
	}
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("Run() error = %v", err)
	}
}

type unboundRecoveryRuntime struct {
	*runtimeStub
	started chan struct{}
}

func (runtime *unboundRecoveryRuntime) Start(_ context.Context, request app.StartSessionRequest) (domain.ProviderBinding, error) {
	if request.Mode != app.SessionStartNew || request.PriorBinding != nil {
		return domain.ProviderBinding{}, errors.New("unbound recovery did not use a new start")
	}
	runtime.started <- struct{}{}
	return domain.ProviderBinding{Provider: request.Provider, SessionID: "provider-unbound-recovered", Generation: 1}, nil
}

type failingUnboundRecoveryRuntime struct {
	*runtimeStub
	attempts atomic.Int32
	started  chan struct{}
}

type forbiddenRawInitialRuntime struct {
	*runtimeStub
	starts atomic.Int32
}

func (runtime *forbiddenRawInitialRuntime) Start(context.Context, app.StartSessionRequest) (domain.ProviderBinding, error) {
	runtime.starts.Add(1)
	return domain.ProviderBinding{}, errors.New("raw initial runtime must not start")
}

func TestPersistedStartingDefersToConfigAwareLiveRecovery(t *testing.T) {
	starting, err := domain.NewStartingSession("aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa", "intent-persisted-starting", "computer", domain.ProviderCodex, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	store := &memoryStore{session: starting}
	clock := newRecoveryClock(starting.StateChangedAt())
	rawRuntime := &forbiddenRawInitialRuntime{runtimeStub: &runtimeStub{}}
	initialRuntime := &failingUnboundRecoveryRuntime{runtimeStub: &runtimeStub{}, started: make(chan struct{}, 4)}
	reported := make(chan domain.SessionID, 4)
	manager, err := supervisioncomposition.New(supervisioncomposition.Options{
		LocalComputerID: "computer", Store: store, Restarter: rawRuntime, InitialRestarter: initialRuntime, Waiter: rawRuntime,
		AcceptedTurns: acceptedStub{}, MaxRestartAttempts: 3, SweepInterval: 5 * time.Millisecond,
		Now: clock.Now, WaitBeforeRetry: func(context.Context, int) error { return nil }, Report: func(error) {},
		ReportSession: func(id domain.SessionID, _ error) { reported <- id },
	})
	if err != nil {
		t.Fatal(err)
	}
	startup, err := manager.RecoverStartup(context.Background())
	if err != nil || startup.Awaiting != 1 || rawRuntime.starts.Load() != 0 || initialRuntime.attempts.Load() != 0 {
		t.Fatalf("RecoverStartup() = result %#v error %v raw %d initial %d", startup, err, rawRuntime.starts.Load(), initialRuntime.attempts.Load())
	}
	persisted, err := store.Load(context.Background(), starting.ID())
	target, recovering := persisted.RecoveryTarget()
	if err != nil || persisted.Status() != domain.SessionAwaitingRecovery || !recovering || target != domain.SessionStarting {
		t.Fatalf("normalized session = status %q target (%q,%t) error %v", persisted.Status(), target, recovering, err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- manager.Run(ctx) }()
	defer stopRecoveryManager(t, cancel, done)
	select {
	case <-initialRuntime.started:
	case <-time.After(time.Second):
		t.Fatal("config-aware live recovery did not start")
	}
	select {
	case id := <-reported:
		if id != starting.ID() {
			t.Fatalf("reported session = %q, want %q", id, starting.ID())
		}
	case <-time.After(time.Second):
		t.Fatal("persisted starting failure was not reported")
	}
	assertUnboundAttemptsStay(t, initialRuntime, 1)
}

func (runtime *failingUnboundRecoveryRuntime) Start(_ context.Context, request app.StartSessionRequest) (domain.ProviderBinding, error) {
	if request.Mode != app.SessionStartNew || request.PriorBinding != nil {
		return domain.ProviderBinding{}, errors.New("unbound recovery did not use a new start")
	}
	runtime.attempts.Add(1)
	runtime.started <- struct{}{}
	return domain.ProviderBinding{}, errors.New("unbound provider start failed")
}

func TestFailedUnboundInitialRecoveryUsesCooldownAndSessionReport(t *testing.T) {
	starting, err := domain.NewStartingSession("88888888-8888-4888-8888-888888888888", "intent-unbound-failure", "computer", domain.ProviderCodex, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	awaiting, err := starting.AwaitRecovery()
	if err != nil {
		t.Fatal(err)
	}
	store := &memoryStore{session: awaiting}
	clock := newRecoveryClock(awaiting.StateChangedAt())
	runtime := &failingUnboundRecoveryRuntime{runtimeStub: &runtimeStub{}, started: make(chan struct{}, 4)}
	reported := make(chan domain.SessionID, 4)
	manager, err := supervisioncomposition.New(supervisioncomposition.Options{
		LocalComputerID: "computer", Store: store, Restarter: &runtimeStub{}, InitialRestarter: runtime, Waiter: runtime,
		AcceptedTurns: acceptedStub{}, MaxRestartAttempts: 3, SweepInterval: 5 * time.Millisecond,
		Now: clock.Now, WaitBeforeRetry: func(context.Context, int) error { return nil }, Report: func(error) {},
		ReportSession: func(id domain.SessionID, _ error) { reported <- id },
	})
	if err != nil {
		t.Fatal(err)
	}
	startup, err := manager.RecoverStartup(context.Background())
	if err != nil || startup.Awaiting != 1 {
		t.Fatalf("RecoverStartup() = (%#v, %v), want one deferred awaiting session", startup, err)
	}
	if got := runtime.attempts.Load(); got != 0 {
		t.Fatalf("startup recovery attempts = %d, want deferred live retry", got)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- manager.Run(ctx) }()
	defer stopRecoveryManager(t, cancel, done)
	select {
	case <-runtime.started:
	case <-time.After(time.Second):
		t.Fatal("first unbound retry did not start")
	}
	select {
	case id := <-reported:
		if id != awaiting.ID() {
			t.Fatalf("reported session = %q, want %q", id, awaiting.ID())
		}
	case <-time.After(time.Second):
		t.Fatal("unbound failure was not reported")
	}
	assertUnboundAttemptsStay(t, runtime, 1)
	clock.Advance(time.Minute)
	select {
	case <-runtime.started:
	case <-time.After(time.Second):
		t.Fatal("unbound retry did not resume after cooldown")
	}
}

func assertUnboundAttemptsStay(t *testing.T, runtime *failingUnboundRecoveryRuntime, want int32) {
	t.Helper()
	time.Sleep(30 * time.Millisecond)
	if got := runtime.attempts.Load(); got != want {
		t.Fatalf("unbound recovery attempts = %d, want %d", got, want)
	}
}

func newFailingRecoveryManager(t *testing.T, report func(error)) (*supervisioncomposition.Manager, *failingAttachRuntime, *recoveryClock, *memoryStore) {
	t.Helper()
	ready, _ := readySession(t)
	clock := newRecoveryClock(time.Now().UTC())
	awaiting, err := ready.AwaitRecoveryAt(clock.Now())
	if err != nil {
		t.Fatal(err)
	}
	store := &memoryStore{session: awaiting}
	runtime := &failingAttachRuntime{runtimeStub: &runtimeStub{}, started: make(chan struct{}, 32)}
	if report == nil {
		report = func(error) {}
	}
	manager, err := supervisioncomposition.New(supervisioncomposition.Options{
		LocalComputerID: "computer", Store: store, Restarter: runtime, Waiter: runtime,
		AcceptedTurns: acceptedStub{}, MaxRestartAttempts: 1, SweepInterval: 5 * time.Millisecond,
		Now: clock.Now, WaitBeforeRetry: func(context.Context, int) error { return nil }, Report: func(err error) {
			runtime.reports.Add(1)
			report(err)
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	return manager, runtime, clock, store
}

func waitForRecoveryAttempts(t *testing.T, runtime *failingAttachRuntime, want int32) {
	t.Helper()
	deadline := time.After(time.Second)
	for runtime.attempts.Load() < want {
		select {
		case <-runtime.started:
		case <-deadline:
			t.Fatalf("recovery attempts = %d, want at least %d", runtime.attempts.Load(), want)
		}
	}
	waitForReports(t, &runtime.reports, want)
}

func assertRecoveryAttemptsStay(t *testing.T, runtime *failingAttachRuntime, want int32) {
	t.Helper()
	time.Sleep(30 * time.Millisecond)
	if got := runtime.attempts.Load(); got != want {
		t.Fatalf("recovery attempts = %d, want %d before cooldown expires", got, want)
	}
}

func waitForReports(t *testing.T, reports *atomic.Int32, want int32) {
	t.Helper()
	deadline := time.After(time.Second)
	for reports.Load() < want {
		select {
		case <-deadline:
			t.Fatalf("reported recovery errors = %d, want %d", reports.Load(), want)
		default:
			time.Sleep(time.Millisecond)
		}
	}
}

func stopRecoveryManager(t *testing.T, cancel context.CancelFunc, done <-chan error) {
	t.Helper()
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("Run() error = %v", err)
	}
}
