package acceptedcontinuation_test

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"bria/internal/acceptedcontinuation"
	"bria/internal/domain"
	"bria/internal/messagejournal"
	"bria/internal/sessionsupervisor"
)

func TestSelectorYieldsAcceptedHistoryToNewerPendingInput(t *testing.T) {
	ctx, selector, journal, session, prior := selectorFixture(t, "accepted")
	if _, _, err := journal.EnqueueInput(ctx, "s", "pending", []byte("new")); err != nil {
		t.Fatal(err)
	}
	reconciliation := sessionsupervisor.AcceptedTurnReconciliation{Turns: []sessionsupervisor.ReconciledAcceptedTurn{{MessageID: "accepted", Outcome: sessionsupervisor.AcceptedTurnUnknown}}}
	required, err := selector.Required(ctx, session, prior, reconciliation)
	if err != nil {
		t.Fatal(err)
	}
	if required {
		t.Fatal("older accepted observation blocked a newer pending input")
	}
}

func TestSelectorKeepsAcceptedObservationWithoutPendingSuccessor(t *testing.T) {
	ctx, selector, _, session, prior := selectorFixture(t, "accepted")
	reconciliation := sessionsupervisor.AcceptedTurnReconciliation{Turns: []sessionsupervisor.ReconciledAcceptedTurn{{MessageID: "accepted", Outcome: sessionsupervisor.AcceptedTurnUnknown}}}
	required, err := selector.Required(ctx, session, prior, reconciliation)
	if err != nil {
		t.Fatal(err)
	}
	if !required {
		t.Fatal("accepted turn without a successor lost its observer")
	}
}

func TestSelectorKeepsUnknownHandoffBarrierBeforePendingInput(t *testing.T) {
	ctx, selector, journal, session, prior := selectorFixture(t, "unknown")
	if _, _, err := journal.EnqueueInput(ctx, "s", "pending", []byte("new")); err != nil {
		t.Fatal(err)
	}
	reconciliation := sessionsupervisor.AcceptedTurnReconciliation{Turns: []sessionsupervisor.ReconciledAcceptedTurn{{MessageID: "unknown", Outcome: sessionsupervisor.AcceptedTurnUnknown}}}
	required, err := selector.Required(ctx, session, prior, reconciliation)
	if err != nil {
		t.Fatal(err)
	}
	if !required {
		t.Fatal("unknown provider hand-off lost its recovery barrier")
	}
}

func selectorFixture(t *testing.T, phase messagejournal.InputPhase) (context.Context, acceptedcontinuation.Selector, *messagejournal.Journal, domain.Session, domain.ProviderBinding) {
	t.Helper()
	ctx := context.Background()
	journal, err := messagejournal.Open(filepath.Join(t.TempDir(), "journal.json"), messagejournal.DefaultLimits())
	if err != nil {
		t.Fatal(err)
	}
	session, err := domain.NewStartingSession("s", "intent", "local", domain.ProviderCodex, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	prior := domain.ProviderBinding{Provider: domain.ProviderCodex, SessionID: "native", Generation: 1}
	binding := prior
	binding.Generation++
	session, err = session.Ready(binding)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err = journal.EnqueueInput(ctx, "s", string(phase), []byte("old")); err != nil {
		t.Fatal(err)
	}
	if _, err = journal.LeaseNextInput(ctx, "s", "old-owner", time.Now(), time.Minute); err != nil {
		t.Fatal(err)
	}
	if phase == messagejournal.InputAccepted {
		_, err = journal.MarkInputAccepted(ctx, "s", string(phase), "old-owner")
	} else {
		_, err = journal.MarkInputDeliveryUnknown(ctx, "s", string(phase), "old-owner")
	}
	if err != nil {
		t.Fatal(err)
	}
	return ctx, acceptedcontinuation.Selector{Journal: journal}, journal, session, prior
}
