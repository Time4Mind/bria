package supervisioncomposition_test

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"bria/internal/domain"
	"bria/internal/sessionsupervisor"
	"bria/internal/storage"
	"bria/internal/supervisioncomposition"
)

type startupReadyHistory func(context.Context, domain.SessionID, domain.ProviderBinding) (sessionsupervisor.AcceptedTurnReconciliation, error)

func (f startupReadyHistory) ReconcileAcceptedTurns(ctx context.Context, id domain.SessionID, binding domain.ProviderBinding) (sessionsupervisor.AcceptedTurnReconciliation, error) {
	return f(ctx, id, binding)
}

func TestManagerStartupReadyReconcilesBeforeGenericResume(t *testing.T) {
	for _, awaiting := range []bool{false, true} {
		name := "ready"
		if awaiting {
			name = "awaiting_target_ready"
		}
		for _, proof := range []string{"pending", "unavailable", "empty", "completed"} {
			t.Run(name+"/"+proof, func(t *testing.T) {
				ctx := context.Background()
				ready, prior := readySession(t)
				current := ready
				if awaiting {
					var err error
					current, err = ready.AwaitRecoveryAt(ready.StateChangedAt())
					if err != nil {
						t.Fatal(err)
					}
				}
				store, err := storage.OpenSessionStore(filepath.Join(t.TempDir(), "state.json"))
				if err != nil {
					t.Fatal(err)
				}
				snapshot := ready.Snapshot()
				snapshot.Status, snapshot.Binding = domain.SessionStarting, nil
				snapshot.StateChangedAt = snapshot.CreatedAt
				starting, err := domain.RestoreSession(snapshot)
				if err != nil {
					t.Fatal(err)
				}
				if _, _, err = store.PutStartingIfAbsent(ctx, starting); err != nil {
					t.Fatal(err)
				}
				if err = store.Replace(ctx, starting, current); err != nil {
					t.Fatal(err)
				}
				// There is deliberately no card: absent displayed history must
				// never erase independent accepted input custody during recovery.
				runtime := &runtimeStub{}
				reconciled := false
				late := false
				history := startupReadyHistory(func(_ context.Context, id domain.SessionID, binding domain.ProviderBinding) (sessionsupervisor.AcceptedTurnReconciliation, error) {
					if id != ready.ID() || binding != prior {
						t.Fatalf("reconciliation crossed exact prior: %s %+v", id, binding)
					}
					reconciled = true
					if proof == "unavailable" && !late {
						return sessionsupervisor.AcceptedTurnReconciliation{}, errors.New("synthetic unreadable history")
					}
					if proof == "empty" {
						return sessionsupervisor.AcceptedTurnReconciliation{}, nil
					}
					outcome := sessionsupervisor.AcceptedTurnUnknown
					if proof == "completed" || late {
						outcome = sessionsupervisor.AcceptedTurnCompleted
					}
					return sessionsupervisor.AcceptedTurnReconciliation{Turns: []sessionsupervisor.ReconciledAcceptedTurn{{MessageID: "accepted-A", Outcome: outcome}}}, nil
				})
				manager, err := supervisioncomposition.New(supervisioncomposition.Options{LocalComputerID: "computer", Store: store, Restarter: runtime, Waiter: runtime, AcceptedTurns: history, MaxRestartAttempts: 1, SweepInterval: time.Hour, Now: time.Now, WaitBeforeRetry: func(context.Context, int) error { return nil }, Report: func(error) {}})
				if err != nil {
					t.Fatal(err)
				}
				result, err := manager.RecoverStartup(ctx)
				current, loadErr := store.Load(ctx, ready.ID())
				starts, _ := runtime.counts()
				blocked := proof == "pending" || proof == "unavailable"
				if err != nil || loadErr != nil || !reconciled {
					t.Fatalf("startup bypassed reconciliation: result=%+v err=%v load=%v reconciled=%t", result, err, loadErr, reconciled)
				}
				if blocked {
					if starts != 0 || result.Recovered != 0 || result.Awaiting != 1 || len(result.Sessions) != 0 || current.Status() != domain.SessionAwaitingRecovery {
						t.Fatalf("unproven turn admitted generic resume: %+v starts=%d status=%s", result, starts, current.Status())
					}
					retained, _ := current.Binding()
					if retained != prior {
						t.Fatalf("blocked recovery replaced binding: %+v", retained)
					}
					late = true
					result, err = manager.RecoverStartup(ctx)
					if err != nil {
						t.Fatal(err)
					}
					current, loadErr = store.Load(ctx, ready.ID())
					starts, _ = runtime.counts()
				}
				binding, bound := current.Binding()
				if loadErr != nil || result.Recovered != 1 || len(result.Sessions) != 1 || result.Awaiting != 0 || current.Status() != domain.SessionReady || starts != 1 || !bound || binding.SessionID != prior.SessionID || binding.Generation != prior.Generation+1 {
					t.Fatalf("terminal/empty proof did not resume once: %+v starts=%d session=%+v", result, starts, current)
				}
			})
		}
	}
}
