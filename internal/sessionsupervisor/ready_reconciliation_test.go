package sessionsupervisor_test

import (
	"context"
	"errors"
	"testing"

	"bria/internal/domain"
	"bria/internal/sessionsupervisor"
)

func TestReadyRecoveryCannotDiscardAcceptedPendingTurn(t *testing.T) {
	for _, persisted := range []bool{false, true} {
		name := "exited_ready"
		if persisted {
			name = "persisted_target_ready"
		}
		t.Run(name, func(t *testing.T) {
			ready := readySession(t, "ready-pending")
			prior, _ := ready.Binding()
			current := ready
			if persisted {
				var err error
				current, err = ready.AwaitRecoveryAt(ready.StateChangedAt())
				if err != nil {
					t.Fatal(err)
				}
			}
			store := &memoryStore{session: current}
			next := prior
			next.Generation++
			restarter := &fakeRestarter{bindings: []domain.ProviderBinding{next}}
			reconciler := &fakeReconciler{reconciliation: sessionsupervisor.AcceptedTurnReconciliation{Turns: []sessionsupervisor.ReconciledAcceptedTurn{{MessageID: "accepted-A", Outcome: sessionsupervisor.AcceptedTurnUnknown}}}}
			supervisor := newSupervisorWithReconciler(t, store, waitFunc(func(context.Context, domain.SessionID, domain.ProviderBinding) error { return nil }), restarter, 1, nil, reconciler)
			var result sessionsupervisor.Result
			var err error
			if persisted {
				result, err = supervisor.RecoverPersisted(context.Background(), ready.ID(), prior)
			} else {
				result, err = supervisor.Watch(context.Background(), ready.ID(), prior)
			}
			if !errors.Is(err, sessionsupervisor.ErrReconciliationRequired) || !result.AwaitingRecovery || result.Recovered || store.session.Status() != domain.SessionAwaitingRecovery || len(restarter.requests) != 0 {
				t.Fatalf("accepted pending escaped Ready recovery: result=%+v err=%v starts=%d", result, err, len(restarter.requests))
			}
			reconciler.reconciliation.Turns[0].Outcome = sessionsupervisor.AcceptedTurnCompleted
			result, err = supervisor.RecoverPersisted(context.Background(), ready.ID(), prior)
			if err != nil || !result.Recovered || result.Session.Status() != domain.SessionReady || len(restarter.requests) != 1 {
				t.Fatalf("late terminal did not resume exact session once: %+v %v", result, err)
			}
			result, err = supervisor.RecoverPersisted(context.Background(), ready.ID(), prior)
			if err != nil || !result.Stale || len(restarter.requests) != 1 {
				t.Fatalf("repeat recovery was not stale: %+v %v", result, err)
			}
		})
	}
}
