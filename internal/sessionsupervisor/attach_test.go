package sessionsupervisor_test

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"bria/internal/app"
	"bria/internal/domain"
	"bria/internal/messagejournal"
	"bria/internal/providerattachport"
	"bria/internal/sessionsupervisor"
)

type attachRuntime struct {
	fakeRestarter
	attached bool
	binding  domain.ProviderBinding
	err      error
	detached bool
	onAttach func()
}

type orderedInputRecovery struct {
	events    *[]string
	active    bool
	beginErr  error
	commitErr error
}

func (recovery orderedInputRecovery) BeginInputRecovery(context.Context, string, string, bool) (bool, error) {
	*recovery.events = append(*recovery.events, "begin")
	return recovery.active, recovery.beginErr
}

func (recovery orderedInputRecovery) CommitInputRecoverySkip(context.Context, string, string) error {
	*recovery.events = append(*recovery.events, "commit")
	return recovery.commitErr
}

type orderedRecoveryStore struct {
	*memoryStore
	events *[]string
}

func (store orderedRecoveryStore) Replace(ctx context.Context, expected, next domain.Session) error {
	*store.events = append(*store.events, "replace:"+string(next.Status()))
	return store.memoryStore.Replace(ctx, expected, next)
}

type orderedRecoveryReconciler struct{ events *[]string }

func (reconciler orderedRecoveryReconciler) ReconcileAcceptedTurns(context.Context, domain.SessionID, domain.ProviderBinding) (sessionsupervisor.AcceptedTurnReconciliation, error) {
	*reconciler.events = append(*reconciler.events, "reconcile")
	return sessionsupervisor.AcceptedTurnReconciliation{}, nil
}

func TestAttachedRecoveryCommitsCapturedInputsBeforePublishingReady(t *testing.T) {
	for _, commitErr := range []error{nil, errors.New("synthetic recovery commit failure")} {
		t.Run(fmt.Sprint(commitErr), func(t *testing.T) {
			ready := readySession(t, "input-recovery-order")
			prior, _ := ready.Binding()
			next := prior
			next.Generation++
			var events []string
			baseStore := &memoryStore{session: ready}
			store := orderedRecoveryStore{memoryStore: baseStore, events: &events}
			runtime := &attachRuntime{binding: next, onAttach: func() { events = append(events, "attach") }}
			supervisor, err := sessionsupervisor.New(store, waitFunc(func(context.Context, domain.SessionID, domain.ProviderBinding) error { return nil }), runtime, sessionsupervisor.Options{
				MaxRestartAttempts: 1,
				WaitBeforeRetry:    func(context.Context, int) error { return nil },
				Now:                time.Now,
				AcceptedTurns:      orderedRecoveryReconciler{events: &events},
				InputRecovery:      orderedInputRecovery{events: &events, active: true, commitErr: commitErr},
			})
			if err != nil {
				t.Fatal(err)
			}
			result, err := supervisor.Watch(context.Background(), ready.ID(), prior)
			wantPrefix := []string{"replace:awaiting_recovery", "begin", "attach", "reconcile", "commit"}
			if len(events) < len(wantPrefix) {
				t.Fatalf("recovery events = %v", events)
			}
			for index := range wantPrefix {
				if events[index] != wantPrefix[index] {
					t.Fatalf("recovery events = %v, want prefix %v", events, wantPrefix)
				}
			}
			if commitErr == nil {
				if err != nil || !result.Recovered || len(events) != 6 || events[5] != "replace:ready" || store.session.Status() != domain.SessionReady || runtime.detached {
					t.Fatalf("successful recovery = %+v, %v, events=%v status=%s detached=%t", result, err, events, store.session.Status(), runtime.detached)
				}
				return
			}
			if !errors.Is(err, commitErr) || result.Recovered || store.session.Status() != domain.SessionAwaitingRecovery || !runtime.detached {
				t.Fatalf("failed recovery commit escaped: %+v, %v, events=%v status=%s detached=%t", result, err, events, store.session.Status(), runtime.detached)
			}
		})
	}
}

func TestAttachedRecoveryRetrySkipsOnlyOriginalBoundaryAndDispatchesSuccessorOnce(t *testing.T) {
	ctx := context.Background()
	journal, err := messagejournal.Open(filepath.Join(t.TempDir(), "journal.json"), messagejournal.DefaultLimits())
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err = journal.EnqueueInput(ctx, "recovery-cutoff", "unknown-old", []byte("old")); err != nil {
		t.Fatal(err)
	}
	if _, err = journal.LeaseNextInput(ctx, "recovery-cutoff", "crashed", time.Unix(1, 0), time.Minute); err != nil {
		t.Fatal(err)
	}
	if _, err = journal.MarkInputDeliveryUnknown(ctx, "recovery-cutoff", "unknown-old", "crashed"); err != nil {
		t.Fatal(err)
	}
	if _, _, err = journal.EnqueueInput(ctx, "recovery-cutoff", "pending-old", []byte("old pending")); err != nil {
		t.Fatal(err)
	}

	ready := readySession(t, "recovery-cutoff")
	prior, _ := ready.Binding()
	next := prior
	next.Generation++
	store := &memoryStore{session: ready}
	transient := errors.New("synthetic attach interruption")
	runtime := &attachRuntime{binding: next, err: transient}
	options := sessionsupervisor.Options{
		MaxRestartAttempts: 1,
		WaitBeforeRetry:    func(context.Context, int) error { return nil },
		Now:                time.Now,
		AcceptedTurns:      &fakeReconciler{},
		InputRecovery:      journal,
	}
	supervisor, err := sessionsupervisor.New(store, waitFunc(func(context.Context, domain.SessionID, domain.ProviderBinding) error { return nil }), runtime, options)
	if err != nil {
		t.Fatal(err)
	}
	first, err := supervisor.Watch(ctx, ready.ID(), prior)
	if !errors.Is(err, transient) || !first.AwaitingRecovery || store.session.Status() != domain.SessionAwaitingRecovery {
		t.Fatalf("first recovery = %+v, %v, status=%s", first, err, store.session.Status())
	}
	before, err := journal.Inputs(ctx, string(ready.ID()))
	if err != nil || len(before) != 2 || before[0].Phase != messagejournal.InputUnknown || before[1].Phase != messagejournal.InputPending {
		t.Fatalf("failed attach changed captured inputs: %+v, %v", before, err)
	}
	if _, _, err = journal.EnqueueInput(ctx, string(ready.ID()), "after-boundary", []byte("new")); err != nil {
		t.Fatal(err)
	}
	runtime.err = nil
	second, err := supervisor.RecoverPersisted(ctx, ready.ID(), prior)
	if err != nil || !second.Recovered || store.session.Status() != domain.SessionReady {
		t.Fatalf("retried recovery = %+v, %v, status=%s", second, err, store.session.Status())
	}
	after, err := journal.Inputs(ctx, string(ready.ID()))
	if err != nil || len(after) != 3 || after[0].Phase != messagejournal.InputSkipped || after[1].Phase != messagejournal.InputSkipped || after[2].Phase != messagejournal.InputPending {
		t.Fatalf("recovery cutoff result = %+v, %v", after, err)
	}
	nextInput, err := journal.LeaseNextInput(ctx, string(ready.ID()), "recovered", time.Unix(100, 0), time.Minute)
	if err != nil || nextInput.MessageID != "after-boundary" || nextInput.Sequence != 3 {
		t.Fatalf("successor lease = %+v, %v", nextInput, err)
	}
	if _, err := journal.LeaseNextInput(ctx, string(ready.ID()), "duplicate", time.Unix(101, 0), time.Minute); !errors.Is(err, messagejournal.ErrNoAvailable) {
		t.Fatalf("successor leased twice: %v", err)
	}
}

func TestPersistedTotalFailureSkipsOldAcceptedTurnWithoutContinuation(t *testing.T) {
	ctx := context.Background()
	journal, err := messagejournal.Open(filepath.Join(t.TempDir(), "journal.json"), messagejournal.DefaultLimits())
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err = journal.EnqueueInput(ctx, "accepted-recovery-skip", "old-accepted", []byte("private")); err != nil {
		t.Fatal(err)
	}
	if _, err = journal.LeaseNextInput(ctx, "accepted-recovery-skip", "old-worker", time.Unix(1, 0), time.Minute); err != nil {
		t.Fatal(err)
	}
	if _, err = journal.MarkInputAccepted(ctx, "accepted-recovery-skip", "old-accepted", "old-worker"); err != nil {
		t.Fatal(err)
	}
	ready := readySession(t, "accepted-recovery-skip")
	running, err := ready.StartWork(ready.StateChangedAt().Add(time.Second))
	if err != nil {
		t.Fatal(err)
	}
	awaiting, err := running.AwaitRecoveryAt(running.StateChangedAt().Add(time.Second))
	if err != nil {
		t.Fatal(err)
	}
	prior, _ := awaiting.Binding()
	next := prior
	next.Generation++
	store := &memoryStore{session: awaiting}
	runtime := &attachRuntime{binding: next}
	continued := false
	supervisor, err := sessionsupervisor.New(store, waitFunc(func(context.Context, domain.SessionID, domain.ProviderBinding) error { return nil }), runtime, sessionsupervisor.Options{
		MaxRestartAttempts: 1, WaitBeforeRetry: func(context.Context, int) error { return nil }, Now: time.Now,
		AcceptedTurns: &fakeReconciler{reconciliation: sessionsupervisor.AcceptedTurnReconciliation{Turns: []sessionsupervisor.ReconciledAcceptedTurn{{MessageID: "old-accepted", TurnID: "old-turn", Outcome: sessionsupervisor.AcceptedTurnUnknown}}}},
		ContinueAcceptedTurns: func(context.Context, domain.Session, domain.ProviderBinding, sessionsupervisor.AcceptedTurnReconciliation) error {
			continued = true
			return nil
		},
		InputRecovery: journal,
	})
	if err != nil {
		t.Fatal(err)
	}
	result, err := supervisor.RecoverPersisted(ctx, awaiting.ID(), prior)
	if err != nil || !result.Recovered || result.AwaitingRecovery || continued || store.session.Status() != domain.SessionReady {
		t.Fatalf("accepted recovery skip = %+v err=%v continued=%t status=%s", result, err, continued, store.session.Status())
	}
	inputs, err := journal.Inputs(ctx, string(awaiting.ID()))
	if err != nil || len(inputs) != 1 || inputs[0].Phase != messagejournal.InputSkipped {
		t.Fatalf("old accepted input was not skipped: %+v, %v", inputs, err)
	}
}

func (*attachRuntime) SupportsAttach(domain.Provider) bool { return true }
func (runtime *attachRuntime) Attach(_ context.Context, request app.StartSessionRequest) (domain.ProviderBinding, error) {
	if request.PriorBinding == nil || request.PriorBinding.SessionID != runtime.binding.SessionID {
		return domain.ProviderBinding{}, errors.New("incorrect attach identity")
	}
	runtime.attached = runtime.err == nil
	if runtime.onAttach != nil {
		runtime.onAttach()
	}
	return runtime.binding, runtime.err
}

func TestStaleAttachCannotOverwriteNewGenerationOrAbortLiveCLI(t *testing.T) {
	ready := readySession(t, "stale-attach")
	prior, _ := ready.Binding()
	store := &memoryStore{session: ready}
	next := prior
	next.Generation++
	runtime := &attachRuntime{binding: next}
	var winner domain.Session
	runtime.onAttach = func() {
		newer := next
		newer.Generation++
		var err error
		winner, err = store.session.Recovered(newer, store.session.StateChangedAt())
		if err != nil {
			t.Fatal(err)
		}
		store.session = winner
	}
	supervisor := newSupervisorWithReconciler(t, store, waitFunc(func(context.Context, domain.SessionID, domain.ProviderBinding) error { return nil }), runtime, 1, nil, &fakeReconciler{})
	result, err := supervisor.Watch(context.Background(), ready.ID(), prior)
	if err != nil || !result.Stale || !store.session.Equal(winner) || !runtime.detached || len(runtime.aborted) != 0 || len(runtime.requests) != 0 {
		t.Fatalf("stale attachment mutated new owner: result=%+v err=%v detach=%t starts=%d aborts=%d", result, err, runtime.detached, len(runtime.requests), len(runtime.aborted))
	}
}

func TestCancelledAttachDoesNotPublishAnUnobservedRecovery(t *testing.T) {
	ready := readySession(t, "cancelled-attach")
	prior, _ := ready.Binding()
	next := prior
	next.Generation++
	store := &memoryStore{session: ready}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	runtime := &attachRuntime{binding: next, onAttach: cancel}
	supervisor := newSupervisorWithReconciler(t, store, waitFunc(func(context.Context, domain.SessionID, domain.ProviderBinding) error { return nil }), runtime, 1, nil, &fakeReconciler{})
	result, err := supervisor.Watch(ctx, ready.ID(), prior)
	if !errors.Is(err, context.Canceled) || result.Recovered || store.session.Status() != domain.SessionAwaitingRecovery || !runtime.detached || len(runtime.aborted) != 0 {
		t.Fatalf("cancelled observer was published: %+v err=%v detached=%t", result, err, runtime.detached)
	}
}

type detachedCloseRuntime struct{ *attachRuntime }

func (r detachedCloseRuntime) Abort(ctx context.Context, request app.StartSessionRequest, binding domain.ProviderBinding) error {
	if !r.attached {
		return errors.New("session process is not tracked")
	}
	return r.fakeRestarter.Abort(ctx, request, binding)
}

func TestDetachedCloseObservesAcceptedWorkThenClosesExactAttachedGeneration(t *testing.T) {
	ctx := context.Background()
	current := readySession(t, "detached-close-work")
	current, err := current.StartWork(current.StateChangedAt().Add(time.Second))
	if err == nil {
		current, err = current.AwaitRecoveryAt(current.StateChangedAt().Add(time.Second))
	}
	if err != nil {
		t.Fatal(err)
	}
	prior, _ := current.Binding()
	next := prior
	next.Generation++
	store := &memoryStore{session: current}
	runtime := detachedCloseRuntime{&attachRuntime{binding: next}}
	closer, err := app.NewSessionCloser(store, runtime, time.Now)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = closer.Close(ctx, current.ID()); err == nil {
		t.Fatal("detached observer incorrectly proved close")
	}
	if target, _ := store.session.RecoveryTarget(); store.session.Status() != domain.SessionAwaitingRecovery || target != domain.SessionClosing {
		t.Fatal("close intent was not preserved")
	}
	observing := false
	supervisor, err := sessionsupervisor.New(store, waitFunc(func(context.Context, domain.SessionID, domain.ProviderBinding) error { return nil }), runtime, sessionsupervisor.Options{
		MaxRestartAttempts: 1, Now: time.Now, WaitBeforeRetry: func(context.Context, int) error { return nil },
		AcceptedTurns: &fakeReconciler{reconciliation: sessionsupervisor.AcceptedTurnReconciliation{Turns: []sessionsupervisor.ReconciledAcceptedTurn{{MessageID: "exact-accepted", Outcome: sessionsupervisor.AcceptedTurnUnknown}}}},
		ContinueAcceptedTurns: func(_ context.Context, attached domain.Session, old domain.ProviderBinding, receipt sessionsupervisor.AcceptedTurnReconciliation) error {
			if attached.Status() != domain.SessionClosingAfterWork || !store.session.Equal(attached) || old != prior || receipt.Turns[0].MessageID != "exact-accepted" {
				t.Fatal("continuation lost exact close/input identity")
			}
			observing = true
			return nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	result, err := supervisor.RecoverPersisted(ctx, current.ID(), prior)
	if err != nil || !result.Recovered || !observing || store.session.Status() != domain.SessionClosingAfterWork || len(runtime.aborted) != 0 || runtime.detached {
		t.Fatalf("unknown accepted close failed to observe: %+v err=%v", result, err)
	}
	// The continuation's terminal-proof branch uses the existing public closer.
	closed, err := closer.Close(ctx, current.ID())
	if err != nil || closed.Session.Status() != domain.SessionArchived || len(runtime.aborted) != 1 || runtime.aborted[0] != next || len(runtime.requests) != 0 {
		t.Fatalf("terminal close changed CLI identity or failed: %+v err=%v aborted=%v", closed, err, runtime.aborted)
	}
}

func TestUnavailableOrClosedNativeTerminalNeverFallsBackToStart(t *testing.T) {
	for _, cause := range []string{"closed", "identity mismatch", "temporary unavailable"} {
		t.Run(cause, func(t *testing.T) {
			ready := readySession(t, "unavailable-attach")
			prior, _ := ready.Binding()
			next := prior
			next.Generation++
			store := &memoryStore{session: ready}
			runtime := &attachRuntime{binding: next, err: errors.New(cause)}
			supervisor := newSupervisorWithReconciler(t, store, waitFunc(func(context.Context, domain.SessionID, domain.ProviderBinding) error { return nil }), runtime, 1, nil, &fakeReconciler{})
			result, err := supervisor.Watch(context.Background(), ready.ID(), prior)
			if !errors.Is(err, runtime.err) || !result.AwaitingRecovery || result.Recovered || store.session.Status() != domain.SessionAwaitingRecovery || len(runtime.requests) != 0 {
				t.Fatalf("unavailable terminal replaced or reported alive: result=%+v err=%v starts=%d", result, err, len(runtime.requests))
			}
		})
	}
}

func TestProvenUnavailableNativeTerminalArchivesInsteadOfAwaitingForever(t *testing.T) {
	ready := readySession(t, "proven-unavailable-attach")
	prior, _ := ready.Binding()
	next := prior
	next.Generation++
	store := &memoryStore{session: ready}
	runtime := &attachRuntime{binding: next, err: providerattachport.ErrTerminalUnavailable}
	supervisor := newSupervisorWithReconciler(t, store, waitFunc(func(context.Context, domain.SessionID, domain.ProviderBinding) error { return nil }), runtime, 1, nil, &fakeReconciler{})
	result, err := supervisor.Watch(context.Background(), ready.ID(), prior)
	if err != nil || !result.Archived || result.AwaitingRecovery || result.Recovered || store.session.Status() != domain.SessionArchived || len(runtime.requests) != 0 {
		t.Fatalf("proven unavailable terminal remained recoverable: result=%+v status=%s err=%v starts=%d", result, store.session.Status(), err, len(runtime.requests))
	}
}

func TestNativeSavedCloseIntentArchivesOnlyAfterExactAttachedClose(t *testing.T) {
	for _, afterWork := range []bool{false, true} {
		t.Run(fmt.Sprint(afterWork), func(t *testing.T) {
			current := readySession(t, "attached-close")
			var err error
			if afterWork {
				current, err = current.StartWork(current.StateChangedAt().Add(time.Second))
				if err == nil {
					current, err = current.CloseAfterWork(current.StateChangedAt())
				}
			} else {
				current, err = current.BeginClose(current.StateChangedAt().Add(time.Second))
			}
			if err != nil {
				t.Fatal(err)
			}
			prior, _ := current.Binding()
			next := prior
			next.Generation++
			store := &memoryStore{session: current}
			runtime := &attachRuntime{binding: next}
			supervisor := newSupervisorWithReconciler(t, store, waitFunc(func(context.Context, domain.SessionID, domain.ProviderBinding) error { return nil }), runtime, 1, nil, attachOrderedReconciler{runtime})
			result, err := supervisor.Watch(context.Background(), current.ID(), prior)
			if err != nil || !result.Archived || store.session.Status() != domain.SessionArchived || !runtime.attached || len(runtime.aborted) != 1 || runtime.aborted[0] != next || len(runtime.requests) != 0 {
				t.Fatalf("saved close intent lacked exact physical close: %+v err=%v attached=%t aborted=%v", result, err, runtime.attached, runtime.aborted)
			}
		})
	}
}

func (r *attachRuntime) Detach(context.Context, app.StartSessionRequest, domain.ProviderBinding) error {
	r.detached = true
	return nil
}

type attachOrderedReconciler struct{ runtime *attachRuntime }

func (r attachOrderedReconciler) ReconcileAcceptedTurns(context.Context, domain.SessionID, domain.ProviderBinding) (sessionsupervisor.AcceptedTurnReconciliation, error) {
	if !r.runtime.attached {
		return sessionsupervisor.AcceptedTurnReconciliation{}, errors.New("native observation requires attachment first")
	}
	return sessionsupervisor.AcceptedTurnReconciliation{}, nil
}

func TestAttachedUnknownRequiresContinuationAndNeverBecomesDurablyReady(t *testing.T) {
	for _, mode := range []string{"observe", "missing", "registration-error", "stopping", "closing-after-work"} {
		t.Run(mode, func(t *testing.T) {
			current := readySession(t, domain.SessionID("attached-"+mode))
			current, err := current.StartWork(current.StateChangedAt().Add(time.Second))
			if err != nil {
				t.Fatal(err)
			}
			if mode == "stopping" {
				current, err = current.BeginStop(current.StateChangedAt().Add(time.Second))
			}
			if mode == "closing-after-work" {
				current, err = current.CloseAfterWork(current.StateChangedAt().Add(time.Second))
			}
			if err != nil {
				t.Fatal(err)
			}
			prior, _ := current.Binding()
			next := prior
			next.Generation++
			store := &memoryStore{session: current}
			runtime := &attachRuntime{binding: next}
			receipt := sessionsupervisor.AcceptedTurnReconciliation{Turns: []sessionsupervisor.ReconciledAcceptedTurn{{MessageID: "accepted-exact", Outcome: sessionsupervisor.AcceptedTurnUnknown}}}
			observed := false
			options := sessionsupervisor.Options{MaxRestartAttempts: 1, Now: time.Now, WaitBeforeRetry: func(context.Context, int) error { return nil }, AcceptedTurns: &fakeReconciler{reconciliation: receipt}}
			if mode != "missing" {
				options.ContinueAcceptedTurns = func(_ context.Context, attached domain.Session, old domain.ProviderBinding, got sessionsupervisor.AcceptedTurnReconciliation) error {
					if !store.session.Equal(attached) || old != prior || len(got.Turns) != 1 || got.Turns[0] != receipt.Turns[0] {
						t.Fatal("continuation lacks exact committed session and accepted message")
					}
					observed = true
					if mode == "registration-error" {
						return errors.New("observer registration failed")
					}
					return nil
				}
			}
			supervisor, err := sessionsupervisor.New(store, waitFunc(func(context.Context, domain.SessionID, domain.ProviderBinding) error { return nil }), runtime, options)
			if err != nil {
				t.Fatal(err)
			}
			result, err := supervisor.Watch(context.Background(), current.ID(), prior)
			blocked := mode == "missing" || mode == "registration-error"
			if blocked {
				if !errors.Is(err, sessionsupervisor.ErrReconciliationRequired) || !result.AwaitingRecovery || store.session.Status() != domain.SessionAwaitingRecovery || !runtime.detached {
					t.Fatalf("unobserved accepted work escaped recovery: %+v err=%v detached=%t", result, err, runtime.detached)
				}
			} else if err != nil || !result.Recovered || !observed || store.session.Status() != current.Status() {
				t.Fatalf("live accepted state not retained: %+v err=%v want=%s", result, err, current.Status())
			}
			for _, committed := range store.replacements {
				if committed.Status() == domain.SessionReady {
					t.Fatal("unknown accepted input became durably Ready")
				}
			}
			if len(runtime.requests) != 0 || len(runtime.aborted) != 0 {
				t.Fatal("accepted input recovery replaced or closed live CLI")
			}
		})
	}
}

func TestAttachedAcceptedHistoryYieldsReadyStateToQueuedSuccessor(t *testing.T) {
	current := readySession(t, "attached-successor")
	prior, _ := current.Binding()
	next := prior
	next.Generation++
	store := &memoryStore{session: current}
	runtime := &attachRuntime{binding: next}
	continued := false
	supervisor, err := sessionsupervisor.New(store, waitFunc(func(context.Context, domain.SessionID, domain.ProviderBinding) error { return nil }), runtime, sessionsupervisor.Options{
		MaxRestartAttempts: 1,
		Now:                time.Now,
		WaitBeforeRetry:    func(context.Context, int) error { return nil },
		AcceptedTurns: &fakeReconciler{reconciliation: sessionsupervisor.AcceptedTurnReconciliation{Turns: []sessionsupervisor.ReconciledAcceptedTurn{{
			MessageID: "accepted-old", Outcome: sessionsupervisor.AcceptedTurnUnknown,
		}}}},
		ShouldContinueAcceptedTurns: func(context.Context, domain.Session, domain.ProviderBinding, sessionsupervisor.AcceptedTurnReconciliation) (bool, error) {
			return false, nil
		},
		ContinueAcceptedTurns: func(context.Context, domain.Session, domain.ProviderBinding, sessionsupervisor.AcceptedTurnReconciliation) error {
			continued = true
			return nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	result, err := supervisor.Watch(context.Background(), current.ID(), prior)
	if err != nil || !result.Recovered || result.Session.Status() != domain.SessionReady || store.session.Status() != domain.SessionReady {
		t.Fatalf("queued successor did not receive ready recovery: result=%+v status=%s err=%v", result, store.session.Status(), err)
	}
	if continued || runtime.detached || len(runtime.requests) != 0 || len(runtime.aborted) != 0 {
		t.Fatalf("historical observer or replacement started: continued=%t detached=%t starts=%d aborts=%d", continued, runtime.detached, len(runtime.requests), len(runtime.aborted))
	}
}

func TestNativeRecoveryAttachesBeforeAcceptedReconciliationWithoutStartingCLI(t *testing.T) {
	for _, failure := range []error{nil, errors.New("observer disconnected")} {
		t.Run(fmt.Sprint(failure), func(t *testing.T) { testAttachedIdle(t, failure) })
	}
}

func testAttachedIdle(t *testing.T, waitError error) {
	ready := readySession(t, "attach-idle")
	prior, _ := ready.Binding()
	store := &memoryStore{session: ready}
	next := prior
	next.Generation++
	runtime := &attachRuntime{binding: next}
	supervisor := newSupervisorWithReconciler(t, store,
		waitFunc(func(context.Context, domain.SessionID, domain.ProviderBinding) error { return waitError }),
		runtime, 1, nil, attachOrderedReconciler{runtime})
	result, err := supervisor.Watch(context.Background(), ready.ID(), prior)
	if err != nil || !result.Recovered || store.session.Status() != domain.SessionReady {
		t.Fatalf("exact attached idle recovery = %+v status=%s err=%v", result, store.session.Status(), err)
	}
	if binding, _ := store.session.Binding(); binding != next || len(runtime.requests) != 0 || len(runtime.aborted) != 0 {
		t.Fatalf("live CLI replaced or closed: binding=%+v starts=%d aborts=%d", binding, len(runtime.requests), len(runtime.aborted))
	}
}
