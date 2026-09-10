package acceptedrecovery_test

import (
	"context"
	"errors"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"bria/internal/acceptedrecovery"
	"bria/internal/domain"
	"bria/internal/durableflow"
	"bria/internal/messagejournal"
	"bria/internal/sessionsupervisor"
)

type recoveryEvidenceRevision interface {
	RecoveryEvidenceRevision(context.Context, domain.SessionID, domain.ProviderBinding) (string, error)
}

type stableRecoveryBarrier interface {
	StableRecoveryBarrierRevision() string
}

type mutableAcceptedHistory struct {
	mu    sync.Mutex
	turns []sessionsupervisor.ReconciledAcceptedTurn
}

func (history *mutableAcceptedHistory) ReconcileAcceptedTurns(context.Context, domain.SessionID, domain.ProviderBinding) (sessionsupervisor.AcceptedTurnReconciliation, error) {
	history.mu.Lock()
	defer history.mu.Unlock()
	return sessionsupervisor.AcceptedTurnReconciliation{Turns: append([]sessionsupervisor.ReconciledAcceptedTurn(nil), history.turns...)}, nil
}

func (history *mutableAcceptedHistory) replace(turns ...sessionsupervisor.ReconciledAcceptedTurn) {
	history.mu.Lock()
	history.turns = append([]sessionsupervisor.ReconciledAcceptedTurn(nil), turns...)
	history.mu.Unlock()
}

func TestHistoryUnverifiableCarriesSafeStableEvidenceRevision(t *testing.T) {
	ctx := context.Background()
	journal, err := messagejournal.Open(filepath.Join(t.TempDir(), "journal.json"), messagejournal.DefaultLimits())
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"accepted", "unknown", "pending"} {
		if _, _, err := journal.EnqueueInput(ctx, "logical", id, []byte("private payload")); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := journal.LeaseNextInput(ctx, "logical", "worker", time.Unix(10, 0), time.Minute); err != nil {
		t.Fatal(err)
	}
	if _, err := journal.MarkInputAccepted(ctx, "logical", "accepted", "worker"); err != nil {
		t.Fatal(err)
	}
	if _, err := journal.LeaseNextInput(ctx, "logical", "worker", time.Unix(10, 0), time.Minute); err != nil {
		t.Fatal(err)
	}
	if _, err := journal.MarkInputDeliveryUnknown(ctx, "logical", "unknown", "worker"); err != nil {
		t.Fatal(err)
	}
	flow, err := durableflow.New(journal, nil, nil, durableflow.Options{Owner: "worker", LeaseDuration: time.Minute, Now: time.Now})
	if err != nil {
		t.Fatal(err)
	}
	binding := domain.ProviderBinding{Provider: domain.ProviderCodex, SessionID: "native", Generation: 1}
	history := &mutableAcceptedHistory{turns: []sessionsupervisor.ReconciledAcceptedTurn{{MessageID: "accepted", Outcome: sessionsupervisor.AcceptedTurnUnknown}}}
	reconciler := acceptedrecovery.AcceptedTurnReconciler{Flow: flow, Histories: map[domain.Provider]sessionsupervisor.AcceptedTurnReconciler{domain.ProviderCodex: history}}

	if _, err = reconciler.ReconcileAcceptedTurns(ctx, "logical", binding); err == nil {
		t.Fatal("missing native receipt did not block recovery")
	}
	var barrier stableRecoveryBarrier
	if !errors.As(err, &barrier) || barrier.StableRecoveryBarrierRevision() == "" {
		t.Fatalf("history barrier has no stable revision: %v", err)
	}
	revisioner, ok := any(reconciler).(recoveryEvidenceRevision)
	if !ok {
		t.Fatal("accepted recovery does not expose a read-only evidence revision")
	}
	first, err := revisioner.RecoveryEvidenceRevision(ctx, "logical", binding)
	if err != nil || first != barrier.StableRecoveryBarrierRevision() {
		t.Fatalf("current revision = %q, %v; barrier = %q", first, err, barrier.StableRecoveryBarrierRevision())
	}
	second, err := revisioner.RecoveryEvidenceRevision(ctx, "logical", binding)
	if err != nil || second != first {
		t.Fatalf("unchanged evidence revision = %q, %v; want %q", second, err, first)
	}
	inputs, err := journal.Inputs(ctx, "logical")
	if err != nil || len(inputs) != 3 || inputs[0].Phase != messagejournal.InputAccepted || inputs[1].Phase != messagejournal.InputUnknown || inputs[2].Phase != messagejournal.InputPending {
		t.Fatalf("stable barrier changed custody: %+v, %v", inputs, err)
	}

	if _, err := journal.RetryInput(ctx, "logical", "unknown"); err != nil {
		t.Fatal(err)
	}
	journalChanged, err := revisioner.RecoveryEvidenceRevision(ctx, "logical", binding)
	if err != nil || journalChanged == first {
		t.Fatalf("journal evidence change was invisible: %q, %v", journalChanged, err)
	}
	history.replace(
		sessionsupervisor.ReconciledAcceptedTurn{MessageID: "accepted", Outcome: sessionsupervisor.AcceptedTurnUnknown},
		sessionsupervisor.ReconciledAcceptedTurn{MessageID: "unknown", Outcome: sessionsupervisor.AcceptedTurnCompleted, TurnID: "turn-2"},
	)
	nativeChanged, err := revisioner.RecoveryEvidenceRevision(ctx, "logical", binding)
	if err != nil || nativeChanged == journalChanged {
		t.Fatalf("native evidence change was invisible: %q, %v", nativeChanged, err)
	}
}
