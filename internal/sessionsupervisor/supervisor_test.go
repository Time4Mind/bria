package sessionsupervisor_test

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
)

func TestUnexpectedExitRecoversExactProviderSessionAtHigherGeneration(t *testing.T) {
	ready := readySession(t, "s-1")
	prior, _ := ready.Binding()
	store := &memoryStore{session: ready}
	restarter := &fakeRestarter{bindings: []domain.ProviderBinding{{
		Provider: prior.Provider, SessionID: prior.SessionID, Generation: prior.Generation + 1,
	}}}
	supervisor := newSupervisor(t, store, waitFunc(func(context.Context, domain.SessionID, domain.ProviderBinding) error {
		return nil
	}), restarter, 3, nil)

	result, err := supervisor.Watch(context.Background(), ready.ID(), prior)
	if err != nil {
		t.Fatalf("Watch() error = %v", err)
	}
	if !result.Recovered || result.AwaitingRecovery || result.Stale || result.RestartAttempts != 1 {
		t.Fatalf("result = %#v, want one successful recovery", result)
	}
	if got := store.session.Status(); got != domain.SessionReady {
		t.Fatalf("persisted status = %q, want ready", got)
	}
	gotBinding, ok := store.session.Binding()
	if !ok || gotBinding != restarter.bindings[0] {
		t.Fatalf("persisted binding = %#v, %v", gotBinding, ok)
	}
	if len(restarter.requests) != 1 {
		t.Fatalf("restart requests = %d, want 1", len(restarter.requests))
	}
	request := restarter.requests[0]
	if request.Mode != app.SessionStartResume || request.PriorBinding == nil || *request.PriorBinding != prior {
		t.Fatalf("restart request = %#v, want exact resume from %#v", request, prior)
	}
	if len(store.replacements) != 2 || store.replacements[0].Status() != domain.SessionAwaitingRecovery || store.replacements[1].Status() != domain.SessionReady {
		t.Fatalf("durable replacements = %#v, want awaiting then ready", statuses(store.replacements))
	}
}

func TestUnexpectedExitDoesNotClaimRecoveryWhenAwaitingStateWasNotPersisted(t *testing.T) {
	ready := readySession(t, "s-persist-failure")
	prior, _ := ready.Binding()
	persistErr := errors.New("disk unavailable")
	store := &memoryStore{session: ready, replaceErrors: []error{persistErr}}
	restarter := &fakeRestarter{}
	supervisor := newSupervisor(t, store, waitFunc(func(context.Context, domain.SessionID, domain.ProviderBinding) error {
		return nil
	}), restarter, 3, nil)

	result, err := supervisor.Watch(context.Background(), ready.ID(), prior)
	if !errors.Is(err, persistErr) {
		t.Fatalf("Watch() error = %v, want persistence error", err)
	}
	if result.Stale || result.AwaitingRecovery || result.Recovered {
		t.Fatalf("result = %#v, must not claim a durable transition", result)
	}
	if len(restarter.requests) != 0 {
		t.Fatalf("restart requests = %d, want none before durable awaiting state", len(restarter.requests))
	}
}

func TestInterruptedWorkRecoversAsReadyInsteadOfInventingRunningWork(t *testing.T) {
	for _, status := range []domain.SessionStatus{domain.SessionRunning, domain.SessionStopping} {
		t.Run(string(status), func(t *testing.T) {
			current := readySession(t, domain.SessionID("s-"+status))
			var err error
			current, err = current.StartWork(current.StateChangedAt().Add(time.Minute))
			if err != nil {
				t.Fatal(err)
			}
			if status == domain.SessionStopping {
				current, err = current.BeginStop(current.StateChangedAt().Add(time.Minute))
				if err != nil {
					t.Fatal(err)
				}
			}
			prior, _ := current.Binding()
			store := &memoryStore{session: current}
			restarter := &fakeRestarter{bindings: []domain.ProviderBinding{{Provider: prior.Provider, SessionID: prior.SessionID, Generation: 2}}}
			supervisor := newSupervisorWithReconciler(t, store, waitFunc(func(context.Context, domain.SessionID, domain.ProviderBinding) error { return nil }), restarter, 1, nil, &fakeReconciler{})

			result, err := supervisor.Watch(context.Background(), current.ID(), prior)
			if err != nil {
				t.Fatalf("Watch() error = %v", err)
			}
			if !result.Recovered || store.session.Status() != domain.SessionReady {
				t.Fatalf("result = %#v, persisted status = %q, want recovered ready", result, store.session.Status())
			}
		})
	}
}

func TestInterruptedActiveTurnWithoutHistoryReconcilerStaysAwaitingRecovery(t *testing.T) {
	running := readySession(t, "s-needs-reconciliation")
	var err error
	running, err = running.StartWork(running.StateChangedAt().Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	prior, _ := running.Binding()
	store := &memoryStore{session: running}
	restarter := &fakeRestarter{bindings: []domain.ProviderBinding{{Provider: prior.Provider, SessionID: prior.SessionID, Generation: 2}}}
	supervisor := newSupervisor(t, store, waitFunc(func(context.Context, domain.SessionID, domain.ProviderBinding) error { return nil }), restarter, 1, nil)

	result, err := supervisor.Watch(context.Background(), running.ID(), prior)
	if !errors.Is(err, sessionsupervisor.ErrReconciliationRequired) {
		t.Fatalf("Watch() error = %v, want ErrReconciliationRequired", err)
	}
	if !result.AwaitingRecovery || result.Recovered || store.session.Status() != domain.SessionAwaitingRecovery {
		t.Fatalf("result=%#v status=%q, want durable awaiting recovery", result, store.session.Status())
	}
	if len(restarter.requests) != 0 {
		t.Fatalf("restart requests = %d, want none before accepted turn reconciliation", len(restarter.requests))
	}
}

func TestReconciledUnknownTurnKeepsSessionAwaitingRecoveryWithoutRestart(t *testing.T) {
	running := readySession(t, "s-reconciled-unknown")
	var err error
	running, err = running.StartWork(running.StateChangedAt().Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	prior, _ := running.Binding()
	store := &memoryStore{session: running}
	restarter := &fakeRestarter{bindings: []domain.ProviderBinding{{Provider: prior.Provider, SessionID: prior.SessionID, Generation: 2}}}
	reconciler := &fakeReconciler{reconciliation: sessionsupervisor.AcceptedTurnReconciliation{Turns: []sessionsupervisor.ReconciledAcceptedTurn{{
		MessageID: "telegram:42", Outcome: sessionsupervisor.AcceptedTurnUnknown,
	}}}}
	supervisor := newSupervisorWithReconciler(t, store, waitFunc(func(context.Context, domain.SessionID, domain.ProviderBinding) error { return nil }), restarter, 1, nil, reconciler)

	result, err := supervisor.Watch(context.Background(), running.ID(), prior)
	if !errors.Is(err, sessionsupervisor.ErrReconciliationRequired) {
		t.Fatalf("Watch() error = %v, want ErrReconciliationRequired", err)
	}
	if !result.AwaitingRecovery || result.Recovered || store.session.Status() != domain.SessionAwaitingRecovery ||
		len(result.Reconciliation.Turns) != 1 || result.Reconciliation.Turns[0].Outcome != sessionsupervisor.AcceptedTurnUnknown {
		t.Fatalf("result = %#v status=%q, want exact unknown receipt and blocked recovery", result, store.session.Status())
	}
	if len(reconciler.sessionIDs) != 1 || reconciler.sessionIDs[0] != running.ID() || reconciler.bindings[0] != prior {
		t.Fatalf("reconciliation target = %v %#v, want %q %#v", reconciler.sessionIDs, reconciler.bindings, running.ID(), prior)
	}
	if len(restarter.requests) != 0 {
		t.Fatalf("restart requests = %d, want none while an accepted turn is unknown", len(restarter.requests))
	}
}

func TestReconciliationFailureOrInvalidReceiptLeavesSessionAwaitingAndDoesNotRestart(t *testing.T) {
	for _, tc := range []struct {
		name       string
		reconciler *fakeReconciler
	}{
		{name: "history unavailable", reconciler: &fakeReconciler{err: errors.New("provider history unavailable")}},
		{name: "invalid durable receipt", reconciler: &fakeReconciler{reconciliation: sessionsupervisor.AcceptedTurnReconciliation{Turns: []sessionsupervisor.ReconciledAcceptedTurn{{MessageID: "m1", Outcome: "replayed"}}}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			running := readySession(t, domain.SessionID("s-"+tc.name))
			var err error
			running, err = running.StartWork(running.StateChangedAt().Add(time.Minute))
			if err != nil {
				t.Fatal(err)
			}
			prior, _ := running.Binding()
			store := &memoryStore{session: running}
			restarter := &fakeRestarter{bindings: []domain.ProviderBinding{{Provider: prior.Provider, SessionID: prior.SessionID, Generation: 2}}}
			supervisor := newSupervisorWithReconciler(t, store, waitFunc(func(context.Context, domain.SessionID, domain.ProviderBinding) error { return nil }), restarter, 1, nil, tc.reconciler)

			result, err := supervisor.Watch(context.Background(), running.ID(), prior)
			if !errors.Is(err, sessionsupervisor.ErrReconciliationRequired) {
				t.Fatalf("Watch() error = %v, want ErrReconciliationRequired", err)
			}
			if !result.AwaitingRecovery || store.session.Status() != domain.SessionAwaitingRecovery || len(restarter.requests) != 0 {
				t.Fatalf("result=%#v status=%q restarts=%d, want blocked awaiting", result, store.session.Status(), len(restarter.requests))
			}
		})
	}
}

func TestClosingAfterWorkAlsoRequiresAcceptedTurnReconciliationBeforeArchive(t *testing.T) {
	closing := readySession(t, "s-closing-reconcile")
	var err error
	closing, err = closing.StartWork(closing.StateChangedAt().Add(time.Minute))
	if err == nil {
		closing, err = closing.CloseAfterWork(closing.StateChangedAt().Add(time.Minute))
	}
	if err != nil {
		t.Fatal(err)
	}
	prior, _ := closing.Binding()
	store := &memoryStore{session: closing}
	restarter := &fakeRestarter{}
	supervisor := newSupervisor(t, store, waitFunc(func(context.Context, domain.SessionID, domain.ProviderBinding) error { return nil }), restarter, 1, nil)

	result, err := supervisor.Watch(context.Background(), closing.ID(), prior)
	if !errors.Is(err, sessionsupervisor.ErrReconciliationRequired) {
		t.Fatalf("Watch() error = %v, want ErrReconciliationRequired", err)
	}
	if !result.AwaitingRecovery || result.Archived || store.session.Status() != domain.SessionAwaitingRecovery {
		t.Fatalf("result=%#v status=%q, want awaiting until accepted turn is resolved", result, store.session.Status())
	}
}

func TestClosingAfterWorkWithUnknownAcceptedTurnDoesNotArchive(t *testing.T) {
	closing := readySession(t, "s-closing-unknown")
	var err error
	closing, err = closing.StartWork(closing.StateChangedAt().Add(time.Minute))
	if err == nil {
		closing, err = closing.CloseAfterWork(closing.StateChangedAt().Add(time.Minute))
	}
	if err != nil {
		t.Fatal(err)
	}
	prior, _ := closing.Binding()
	store := &memoryStore{session: closing}
	restarter := &fakeRestarter{}
	reconciler := &fakeReconciler{reconciliation: sessionsupervisor.AcceptedTurnReconciliation{Turns: []sessionsupervisor.ReconciledAcceptedTurn{{
		MessageID: "telegram:closing", Outcome: sessionsupervisor.AcceptedTurnUnknown,
	}}}}
	supervisor := newSupervisorWithReconciler(t, store, waitFunc(func(context.Context, domain.SessionID, domain.ProviderBinding) error { return nil }), restarter, 1, nil, reconciler)

	result, err := supervisor.Watch(context.Background(), closing.ID(), prior)
	if !errors.Is(err, sessionsupervisor.ErrReconciliationRequired) {
		t.Fatalf("Watch() error = %v, want ErrReconciliationRequired", err)
	}
	if !result.AwaitingRecovery || result.Archived || result.Recovered || store.session.Status() != domain.SessionAwaitingRecovery || len(restarter.requests) != 0 {
		t.Fatalf("result=%#v status=%q restarts=%d, want blocked without archive", result, store.session.Status(), len(restarter.requests))
	}
}

func TestRecoverPersistedReconcilesInterruptedTurnThenResumesExactSessionIdempotently(t *testing.T) {
	running := readySession(t, "s-persisted-running")
	var err error
	running, err = running.StartWork(running.StateChangedAt().Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	prior, _ := running.Binding()
	awaiting, err := running.AwaitRecoveryAt(running.StateChangedAt().Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	store := &memoryStore{session: awaiting}
	next := domain.ProviderBinding{Provider: prior.Provider, SessionID: prior.SessionID, Generation: prior.Generation + 1}
	restarter := &fakeRestarter{bindings: []domain.ProviderBinding{next}}
	reconciler := &fakeReconciler{reconciliation: sessionsupervisor.AcceptedTurnReconciliation{Turns: []sessionsupervisor.ReconciledAcceptedTurn{{
		MessageID: "telegram:persisted", Outcome: sessionsupervisor.AcceptedTurnCompleted,
	}}}}
	supervisor := newSupervisorWithReconciler(t, store, waitFunc(func(context.Context, domain.SessionID, domain.ProviderBinding) error {
		t.Fatal("RecoverPersisted must not wait on a watcher from the prior process")
		return nil
	}), restarter, 1, nil, reconciler)

	result, err := supervisor.RecoverPersisted(context.Background(), awaiting.ID(), prior)
	if err != nil || !result.Recovered || result.AwaitingRecovery || result.Session.Status() != domain.SessionReady {
		t.Fatalf("RecoverPersisted() = %#v, %v", result, err)
	}
	if len(reconciler.bindings) != 1 || reconciler.bindings[0] != prior || len(restarter.requests) != 1 ||
		restarter.requests[0].PriorBinding == nil || *restarter.requests[0].PriorBinding != prior {
		t.Fatalf("reconcile/restart = %#v / %#v, want exact prior binding", reconciler.bindings, restarter.requests)
	}
	if binding, ok := result.Session.Binding(); !ok || binding != next {
		t.Fatalf("recovered binding = %#v, %v", binding, ok)
	}

	replayed, err := supervisor.RecoverPersisted(context.Background(), awaiting.ID(), prior)
	if err != nil || !replayed.Stale || len(reconciler.bindings) != 1 || len(restarter.requests) != 1 {
		t.Fatalf("replayed RecoverPersisted() = %#v, %v, reconciles=%d restarts=%d", replayed, err, len(reconciler.bindings), len(restarter.requests))
	}
}

func TestRecoverPersistedUnknownTurnRemainsBlockedWithoutRestartOrArchive(t *testing.T) {
	closing := readySession(t, "s-persisted-unknown")
	var err error
	closing, err = closing.StartWork(closing.StateChangedAt().Add(time.Minute))
	if err == nil {
		closing, err = closing.CloseAfterWork(closing.StateChangedAt().Add(time.Minute))
	}
	if err != nil {
		t.Fatal(err)
	}
	prior, _ := closing.Binding()
	awaiting, err := closing.AwaitRecoveryAt(closing.StateChangedAt().Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	store := &memoryStore{session: awaiting}
	restarter := &fakeRestarter{}
	reconciler := &fakeReconciler{reconciliation: sessionsupervisor.AcceptedTurnReconciliation{Turns: []sessionsupervisor.ReconciledAcceptedTurn{{
		MessageID: "telegram:unknown", Outcome: sessionsupervisor.AcceptedTurnUnknown,
	}}}}
	supervisor := newSupervisorWithReconciler(t, store, waitFunc(func(context.Context, domain.SessionID, domain.ProviderBinding) error { return nil }), restarter, 1, nil, reconciler)

	result, err := supervisor.RecoverPersisted(context.Background(), awaiting.ID(), prior)
	if !errors.Is(err, sessionsupervisor.ErrReconciliationRequired) || !result.AwaitingRecovery || result.Recovered || result.Archived ||
		store.session.Status() != domain.SessionAwaitingRecovery || len(restarter.requests) != 0 {
		t.Fatalf("RecoverPersisted() = %#v, %v, status=%q restarts=%d", result, err, store.session.Status(), len(restarter.requests))
	}
}

func TestRecoverPersistedFinalizesClosingTargetsWithoutResurrection(t *testing.T) {
	for _, target := range []domain.SessionStatus{domain.SessionClosingAfterWork, domain.SessionClosing} {
		t.Run(string(target), func(t *testing.T) {
			current := readySession(t, domain.SessionID("s-persisted-"+target))
			var err error
			if target == domain.SessionClosingAfterWork {
				current, err = current.StartWork(current.StateChangedAt().Add(time.Minute))
				if err == nil {
					current, err = current.CloseAfterWork(current.StateChangedAt().Add(time.Minute))
				}
			} else {
				current, err = current.BeginClose(current.StateChangedAt().Add(time.Minute))
			}
			if err != nil {
				t.Fatal(err)
			}
			prior, _ := current.Binding()
			awaiting, err := current.AwaitRecoveryAt(current.StateChangedAt().Add(time.Minute))
			if err != nil {
				t.Fatal(err)
			}
			store := &memoryStore{session: awaiting}
			restarter := &fakeRestarter{}
			var reconciler sessionsupervisor.AcceptedTurnReconciler
			if target == domain.SessionClosingAfterWork {
				reconciler = &fakeReconciler{reconciliation: sessionsupervisor.AcceptedTurnReconciliation{Turns: []sessionsupervisor.ReconciledAcceptedTurn{{
					MessageID: "telegram:closed", Outcome: sessionsupervisor.AcceptedTurnFailed,
				}}}}
			}
			supervisor := newSupervisorWithReconciler(t, store, waitFunc(func(context.Context, domain.SessionID, domain.ProviderBinding) error { return nil }), restarter, 1, nil, reconciler)

			result, err := supervisor.RecoverPersisted(context.Background(), awaiting.ID(), prior)
			if err != nil || !result.Archived || result.Recovered || result.AwaitingRecovery || store.session.Status() != domain.SessionArchived || len(restarter.requests) != 0 {
				t.Fatalf("RecoverPersisted(%s) = %#v, %v, status=%q restarts=%d", target, result, err, store.session.Status(), len(restarter.requests))
			}
		})
	}
}

func TestRecoverPersistedRejectsMismatchedPriorBindingWithoutSideEffects(t *testing.T) {
	ready := readySession(t, "s-persisted-mismatch")
	prior, _ := ready.Binding()
	awaiting, err := ready.AwaitRecoveryAt(ready.StateChangedAt().Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	wrong := prior
	wrong.Generation++
	store := &memoryStore{session: awaiting}
	restarter := &fakeRestarter{}
	reconciler := &fakeReconciler{}
	supervisor := newSupervisorWithReconciler(t, store, waitFunc(func(context.Context, domain.SessionID, domain.ProviderBinding) error { return nil }), restarter, 1, nil, reconciler)

	result, err := supervisor.RecoverPersisted(context.Background(), awaiting.ID(), wrong)
	if err != nil || !result.Stale || !result.AwaitingRecovery || len(reconciler.bindings) != 0 || len(restarter.requests) != 0 || len(store.replacements) != 0 {
		t.Fatalf("RecoverPersisted(mismatch) = %#v, %v", result, err)
	}
}

func TestExitedClosingSessionArchivesWithoutStartingReplacement(t *testing.T) {
	for _, status := range []domain.SessionStatus{domain.SessionClosingAfterWork, domain.SessionClosing} {
		t.Run(string(status), func(t *testing.T) {
			current := readySession(t, domain.SessionID("s-"+status))
			var err error
			if status == domain.SessionClosingAfterWork {
				current, err = current.StartWork(current.StateChangedAt().Add(time.Minute))
				if err == nil {
					current, err = current.CloseAfterWork(current.StateChangedAt().Add(time.Minute))
				}
			} else {
				current, err = current.BeginClose(current.StateChangedAt().Add(time.Minute))
			}
			if err != nil {
				t.Fatal(err)
			}
			prior, _ := current.Binding()
			store := &memoryStore{session: current}
			restarter := &fakeRestarter{}
			var reconciler sessionsupervisor.AcceptedTurnReconciler
			if status == domain.SessionClosingAfterWork {
				reconciler = &fakeReconciler{}
			}
			supervisor := newSupervisorWithReconciler(t, store, waitFunc(func(context.Context, domain.SessionID, domain.ProviderBinding) error { return nil }), restarter, 1, nil, reconciler)

			result, err := supervisor.Watch(context.Background(), current.ID(), prior)
			if err != nil {
				t.Fatalf("Watch() error = %v", err)
			}
			if !result.Archived || store.session.Status() != domain.SessionArchived {
				t.Fatalf("result = %#v, persisted status = %q, want archived", result, store.session.Status())
			}
			if len(restarter.requests) != 0 {
				t.Fatalf("restart requests = %d, want none", len(restarter.requests))
			}
		})
	}
}

func TestStaleGenerationWatcherCannotChangeOrRestartCurrentGeneration(t *testing.T) {
	old := readySession(t, "s-stale")
	oldBinding, _ := old.Binding()
	awaiting, err := old.AwaitRecoveryAt(old.StateChangedAt().Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	currentBinding := oldBinding
	currentBinding.Generation++
	current, err := awaiting.Recovered(currentBinding, awaiting.StateChangedAt().Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	store := &memoryStore{session: current}
	restarter := &fakeRestarter{}
	supervisor := newSupervisor(t, store, waitFunc(func(context.Context, domain.SessionID, domain.ProviderBinding) error { return nil }), restarter, 3, nil)

	result, err := supervisor.Watch(context.Background(), current.ID(), oldBinding)
	if err != nil {
		t.Fatalf("Watch() error = %v", err)
	}
	if !result.Stale || !store.session.Equal(current) || len(store.replacements) != 0 || len(restarter.requests) != 0 {
		t.Fatalf("result=%#v replacements=%d restarts=%d, want stale no-op", result, len(store.replacements), len(restarter.requests))
	}
}

func TestWaitFailureFromWatcherForAlreadyArchivedSessionIsStaleNoOp(t *testing.T) {
	ready := readySession(t, "s-archived-stale")
	observed, _ := ready.Binding()
	archived, err := ready.BeginClose(ready.StateChangedAt().Add(time.Minute))
	if err == nil {
		archived, err = archived.Archive(archived.StateChangedAt().Add(time.Minute))
	}
	if err != nil {
		t.Fatal(err)
	}
	waitErr := errors.New("binding no longer tracked")
	store := &memoryStore{session: archived}
	supervisor := newSupervisor(t, store, waitFunc(func(context.Context, domain.SessionID, domain.ProviderBinding) error { return waitErr }), &fakeRestarter{}, 1, nil)

	result, err := supervisor.Watch(context.Background(), archived.ID(), observed)
	if err != nil {
		t.Fatalf("Watch() error = %v, want stale no-op", err)
	}
	if !result.Stale || !result.Session.Equal(archived) || len(store.replacements) != 0 {
		t.Fatalf("result=%#v replacements=%d, want archived stale no-op", result, len(store.replacements))
	}
}

func TestRecoveryRetriesAreBoundedByInjectedWaits(t *testing.T) {
	ready := readySession(t, "s-retry")
	prior, _ := ready.Binding()
	transient := errors.New("temporary start failure")
	restarter := &fakeRestarter{
		bindings: []domain.ProviderBinding{{}, {}, {Provider: prior.Provider, SessionID: prior.SessionID, Generation: 2}},
		errors:   []error{transient, transient, nil},
	}
	var waits []int
	supervisor := newSupervisor(t, &memoryStore{session: ready}, waitFunc(func(context.Context, domain.SessionID, domain.ProviderBinding) error { return nil }), restarter, 3,
		func(_ context.Context, completedAttempt int) error {
			waits = append(waits, completedAttempt)
			return nil
		})

	result, err := supervisor.Watch(context.Background(), ready.ID(), prior)
	if err != nil {
		t.Fatalf("Watch() error = %v", err)
	}
	if !result.Recovered || result.RestartAttempts != 3 || len(waits) != 2 || waits[0] != 1 || waits[1] != 2 {
		t.Fatalf("result=%#v waits=%v, want success on bounded third attempt", result, waits)
	}
}

func TestReplacementProviderSessionIsAbortedAndNeverPersisted(t *testing.T) {
	ready := readySession(t, "s-replacement")
	prior, _ := ready.Binding()
	replacement := domain.ProviderBinding{Provider: prior.Provider, SessionID: "different-thread", Generation: 2}
	restarter := &fakeRestarter{bindings: []domain.ProviderBinding{replacement}}
	store := &memoryStore{session: ready}
	supervisor := newSupervisor(t, store, waitFunc(func(context.Context, domain.SessionID, domain.ProviderBinding) error { return nil }), restarter, 1, nil)

	result, err := supervisor.Watch(context.Background(), ready.ID(), prior)
	if !errors.Is(err, sessionsupervisor.ErrRecoveryExhausted) {
		t.Fatalf("Watch() error = %v, want recovery exhausted", err)
	}
	if !result.AwaitingRecovery || result.Recovered || len(restarter.aborted) != 1 || restarter.aborted[0] != replacement {
		t.Fatalf("result=%#v aborted=%#v, want rejected replacement aborted", result, restarter.aborted)
	}
	if binding, _ := store.session.Binding(); binding != prior || store.session.Status() != domain.SessionAwaitingRecovery {
		t.Fatalf("persisted session binding=%#v status=%q, want original awaiting recovery", binding, store.session.Status())
	}
}

func TestConcurrentWatchersDoNotElectOrStartASecondReplacement(t *testing.T) {
	ready := readySession(t, "s-concurrent")
	prior, _ := ready.Binding()
	store := &lockedStore{session: ready}
	arrived := make(chan struct{}, 2)
	release := make(chan struct{})
	waiter := waitFunc(func(context.Context, domain.SessionID, domain.ProviderBinding) error {
		arrived <- struct{}{}
		<-release
		return nil
	})
	restarter := &countingRestarter{binding: domain.ProviderBinding{Provider: prior.Provider, SessionID: prior.SessionID, Generation: 2}}
	var ticks atomic.Int64
	supervisor, err := sessionsupervisor.New(store, waiter, restarter, sessionsupervisor.Options{
		MaxRestartAttempts: 1,
		WaitBeforeRetry:    func(context.Context, int) error { return nil },
		Now:                func() time.Time { return time.Unix(ticks.Add(1)+2_000_000_000, 0).UTC() },
	})
	if err != nil {
		t.Fatal(err)
	}
	type outcome struct {
		result sessionsupervisor.Result
		err    error
	}
	outcomes := make(chan outcome, 2)
	for index := 0; index < 2; index++ {
		go func() {
			result, watchErr := supervisor.Watch(context.Background(), ready.ID(), prior)
			outcomes <- outcome{result: result, err: watchErr}
		}()
	}
	<-arrived
	<-arrived
	close(release)
	first, second := <-outcomes, <-outcomes
	if first.err != nil || second.err != nil {
		t.Fatalf("Watch errors = %v, %v", first.err, second.err)
	}
	recovered, stale := 0, 0
	for _, result := range []sessionsupervisor.Result{first.result, second.result} {
		if result.Recovered {
			recovered++
		}
		if result.Stale {
			stale++
		}
	}
	if recovered != 1 || stale != 1 || restarter.starts.Load() != 1 {
		t.Fatalf("recovered=%d stale=%d starts=%d, want one owner and one stale watcher", recovered, stale, restarter.starts.Load())
	}
	if binding, _ := store.current().Binding(); binding != restarter.binding {
		t.Fatalf("persisted binding = %#v, want %#v", binding, restarter.binding)
	}
}

func newSupervisor(t *testing.T, store sessionsupervisor.Store, waiter sessionsupervisor.ProcessWaiter, restarter sessionsupervisor.Restarter, attempts int, retry sessionsupervisor.RetryWaiter) *sessionsupervisor.Supervisor {
	return newSupervisorWithReconciler(t, store, waiter, restarter, attempts, retry, nil)
}

func newSupervisorWithReconciler(t *testing.T, store sessionsupervisor.Store, waiter sessionsupervisor.ProcessWaiter, restarter sessionsupervisor.Restarter, attempts int, retry sessionsupervisor.RetryWaiter, reconciler sessionsupervisor.AcceptedTurnReconciler) *sessionsupervisor.Supervisor {
	t.Helper()
	next := time.Date(2026, 9, 3, 12, 0, 0, 0, time.UTC)
	now := func() time.Time { next = next.Add(time.Second); return next }
	if retry == nil {
		retry = func(context.Context, int) error { return nil }
	}
	value, err := sessionsupervisor.New(store, waiter, restarter, sessionsupervisor.Options{
		MaxRestartAttempts: attempts, WaitBeforeRetry: retry, Now: now, AcceptedTurns: reconciler,
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	return value
}

func readySession(t *testing.T, id domain.SessionID) domain.Session {
	t.Helper()
	at := time.Date(2026, 9, 3, 10, 0, 0, 0, time.UTC)
	value, err := domain.NewStartingSessionAt(id, domain.IntentID("intent-"+id), "local", domain.ProviderCodex, "/work", at, domain.SessionLifetimeNever)
	if err != nil {
		t.Fatal(err)
	}
	value, err = value.ReadyAt(domain.ProviderBinding{Provider: domain.ProviderCodex, SessionID: "thread-" + string(id), Generation: 1}, at.Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	return value
}

type memoryStore struct {
	session       domain.Session
	replacements  []domain.Session
	loadErr       error
	replaceErrors []error
}

func (store *memoryStore) Load(context.Context, domain.SessionID) (domain.Session, error) {
	if store.loadErr != nil {
		return domain.Session{}, store.loadErr
	}
	return store.session, nil
}

func (store *memoryStore) Replace(_ context.Context, expected, next domain.Session) error {
	if len(store.replaceErrors) > 0 {
		err := store.replaceErrors[0]
		store.replaceErrors = store.replaceErrors[1:]
		if err != nil {
			return err
		}
	}
	if !store.session.Equal(expected) {
		return errors.New("compare conflict")
	}
	store.session = next
	store.replacements = append(store.replacements, next)
	return nil
}

type waitFunc func(context.Context, domain.SessionID, domain.ProviderBinding) error

func (function waitFunc) Wait(ctx context.Context, id domain.SessionID, binding domain.ProviderBinding) error {
	return function(ctx, id, binding)
}

type fakeRestarter struct {
	bindings []domain.ProviderBinding
	errors   []error
	requests []app.StartSessionRequest
	aborted  []domain.ProviderBinding
}

type fakeReconciler struct {
	reconciliation sessionsupervisor.AcceptedTurnReconciliation
	err            error
	sessionIDs     []domain.SessionID
	bindings       []domain.ProviderBinding
}

func (reconciler *fakeReconciler) ReconcileAcceptedTurns(_ context.Context, sessionID domain.SessionID, binding domain.ProviderBinding) (sessionsupervisor.AcceptedTurnReconciliation, error) {
	reconciler.sessionIDs = append(reconciler.sessionIDs, sessionID)
	reconciler.bindings = append(reconciler.bindings, binding)
	return reconciler.reconciliation, reconciler.err
}

func (restarter *fakeRestarter) Start(_ context.Context, request app.StartSessionRequest) (domain.ProviderBinding, error) {
	restarter.requests = append(restarter.requests, request)
	index := len(restarter.requests) - 1
	if index < len(restarter.errors) && restarter.errors[index] != nil {
		return domain.ProviderBinding{}, restarter.errors[index]
	}
	if index >= len(restarter.bindings) {
		return domain.ProviderBinding{}, errors.New("missing fake binding")
	}
	return restarter.bindings[index], nil
}

func (restarter *fakeRestarter) Abort(_ context.Context, _ app.StartSessionRequest, binding domain.ProviderBinding) error {
	restarter.aborted = append(restarter.aborted, binding)
	return nil
}

func statuses(values []domain.Session) []domain.SessionStatus {
	result := make([]domain.SessionStatus, len(values))
	for index := range values {
		result[index] = values[index].Status()
	}
	return result
}

type lockedStore struct {
	mu      sync.Mutex
	session domain.Session
}

func (store *lockedStore) Load(context.Context, domain.SessionID) (domain.Session, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	return store.session, nil
}

func (store *lockedStore) Replace(_ context.Context, expected, next domain.Session) error {
	store.mu.Lock()
	defer store.mu.Unlock()
	if !store.session.Equal(expected) {
		return errors.New("compare conflict")
	}
	store.session = next
	return nil
}

func (store *lockedStore) current() domain.Session {
	store.mu.Lock()
	defer store.mu.Unlock()
	return store.session
}

type countingRestarter struct {
	binding domain.ProviderBinding
	starts  atomic.Int32
}

func (restarter *countingRestarter) Start(context.Context, app.StartSessionRequest) (domain.ProviderBinding, error) {
	restarter.starts.Add(1)
	return restarter.binding, nil
}

func (*countingRestarter) Abort(context.Context, app.StartSessionRequest, domain.ProviderBinding) error {
	return nil
}
