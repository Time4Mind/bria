package main

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"bria/internal/domain"
	"bria/internal/durablecomposition"
	"bria/internal/durableflow"
	"bria/internal/messagejournal"
	"bria/internal/sessionsupervisor"
)

type acceptedHistoryStub struct {
	receipt sessionsupervisor.AcceptedTurnReconciliation
	err     error
	calls   int
	binding domain.ProviderBinding
}

func (stub *acceptedHistoryStub) ReconcileAcceptedTurns(
	_ context.Context,
	_ domain.SessionID,
	binding domain.ProviderBinding,
) (sessionsupervisor.AcceptedTurnReconciliation, error) {
	stub.calls++
	stub.binding = binding
	return stub.receipt, stub.err
}

func TestDurableAcceptedTurnReconcilerCommitsExactProviderHistoryOutcome(t *testing.T) {
	flow, journal := acceptedRecoveryFlow(t)
	const (
		sessionID = domain.SessionID("123e4567-e89b-12d3-a456-426614174000")
		messageID = "telegram-update:77"
	)
	markAcceptedInput(t, flow, journal, sessionID, messageID)
	binding := domain.ProviderBinding{Provider: domain.ProviderCodex, SessionID: "thread-1", Generation: 3}
	history := &acceptedHistoryStub{receipt: sessionsupervisor.AcceptedTurnReconciliation{Turns: []sessionsupervisor.ReconciledAcceptedTurn{
		{MessageID: messageID, Outcome: sessionsupervisor.AcceptedTurnCompleted},
	}}}
	reconciler := durablecomposition.AcceptedTurnReconciler{
		Flow:      flow,
		Histories: map[domain.Provider]sessionsupervisor.AcceptedTurnReconciler{domain.ProviderCodex: history},
	}

	receipt, err := reconciler.ReconcileAcceptedTurns(context.Background(), sessionID, binding)
	if err != nil {
		t.Fatal(err)
	}
	if len(receipt.Turns) != 1 || receipt.Turns[0].MessageID != messageID || receipt.Turns[0].Outcome != sessionsupervisor.AcceptedTurnCompleted {
		t.Fatalf("reconciliation = %#v", receipt)
	}
	inputs, err := journal.Inputs(context.Background(), string(sessionID))
	if err != nil || len(inputs) != 1 || inputs[0].Phase != messagejournal.InputCompleted {
		t.Fatalf("journal inputs = %#v, %v", inputs, err)
	}
	if history.calls != 1 || history.binding != binding {
		t.Fatalf("history calls/binding = %d/%#v", history.calls, history.binding)
	}
}

func TestDurableAcceptedTurnReconcilerSealsMissingCorrelationUnknown(t *testing.T) {
	flow, journal := acceptedRecoveryFlow(t)
	const (
		sessionID = domain.SessionID("123e4567-e89b-12d3-a456-426614174000")
		messageID = "telegram-update:78"
	)
	markAcceptedInput(t, flow, journal, sessionID, messageID)
	history := &acceptedHistoryStub{receipt: sessionsupervisor.AcceptedTurnReconciliation{Turns: []sessionsupervisor.ReconciledAcceptedTurn{
		{MessageID: "different-message", Outcome: sessionsupervisor.AcceptedTurnCompleted},
	}}}
	reconciler := durablecomposition.AcceptedTurnReconciler{
		Flow:      flow,
		Histories: map[domain.Provider]sessionsupervisor.AcceptedTurnReconciler{domain.ProviderCodex: history},
	}

	receipt, err := reconciler.ReconcileAcceptedTurns(context.Background(), sessionID, domain.ProviderBinding{
		Provider: domain.ProviderCodex, SessionID: "thread-1", Generation: 3,
	})
	if err == nil || len(receipt.Turns) != 1 || receipt.Turns[0].Outcome != sessionsupervisor.AcceptedTurnUnknown {
		t.Fatalf("reconciliation = %#v, %v", receipt, err)
	}
	inputs, loadErr := journal.Inputs(context.Background(), string(sessionID))
	if loadErr != nil || len(inputs) != 1 || inputs[0].Phase != messagejournal.InputUnknown {
		t.Fatalf("journal inputs = %#v, %v", inputs, loadErr)
	}
}

func TestDurableAcceptedTurnReconcilerDoesNotStartProviderWhenNoAcceptedInputExists(t *testing.T) {
	flow, _ := acceptedRecoveryFlow(t)
	history := &acceptedHistoryStub{err: errors.New("must not be called")}
	reconciler := durablecomposition.AcceptedTurnReconciler{
		Flow:      flow,
		Histories: map[domain.Provider]sessionsupervisor.AcceptedTurnReconciler{domain.ProviderCodex: history},
	}
	receipt, err := reconciler.ReconcileAcceptedTurns(context.Background(), "123e4567-e89b-12d3-a456-426614174000", domain.ProviderBinding{
		Provider: domain.ProviderCodex, SessionID: "thread-1", Generation: 3,
	})
	if err != nil || len(receipt.Turns) != 0 || history.calls != 0 {
		t.Fatalf("reconciliation/history calls = %#v/%d, %v", receipt, history.calls, err)
	}
}

func acceptedRecoveryFlow(t *testing.T) (*durableflow.Flow, *messagejournal.Journal) {
	t.Helper()
	journal, err := messagejournal.Open(filepath.Join(t.TempDir(), "journal.json"), messagejournal.DefaultLimits())
	if err != nil {
		t.Fatal(err)
	}
	flow, err := durableflow.New(journal, nil, nil, durableflow.Options{
		Owner: "recovery", LeaseDuration: time.Minute, Now: time.Now,
	})
	if err != nil {
		t.Fatal(err)
	}
	return flow, journal
}

func markAcceptedInput(
	t *testing.T,
	flow *durableflow.Flow,
	journal *messagejournal.Journal,
	sessionID domain.SessionID,
	messageID string,
) {
	t.Helper()
	receipt, err := flow.EnqueueInput(context.Background(), string(sessionID), messageID, []byte("prompt"))
	if err != nil {
		t.Fatal(err)
	}
	leased, err := journal.LeaseNextInput(context.Background(), string(sessionID), "recovery", time.Now(), time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if leased.Sequence != receipt.Sequence {
		t.Fatalf("leased sequence = %d, want %d", leased.Sequence, receipt.Sequence)
	}
	if _, err := flow.RecordLeasedInputAccepted(context.Background(), string(sessionID), messageID, receipt.Sequence); err != nil {
		t.Fatal(err)
	}
}
