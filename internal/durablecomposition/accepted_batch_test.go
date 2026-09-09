package durablecomposition_test

import (
	"bria/internal/domain"
	"bria/internal/durablecomposition"
	"bria/internal/messagejournal"
	"bria/internal/sessionsupervisor"
	"bria/internal/turncontinuation"
	"bria/internal/turnprocessing"
	"context"
	"errors"
	"path/filepath"
	"reflect"
	"testing"
	"time"
)

type batchContinuationController struct {
	continuationController
	members []turncontinuation.Member
	settled func()
}

func (c *batchContinuationController) ContinueAcceptedBatch(_ context.Context, _ domain.ProviderBinding, members []turncontinuation.Member, settled func()) error {
	c.members = members
	c.settled = settled
	return nil
}

func TestAcceptedContinuationBatchPreservesJournalAndWakesAfterSettlement(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "journal.json")
	journal, err := messagejournal.Open(path, messagejournal.DefaultLimits())
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
	session, err = session.StartWork(time.Now())
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"root", "steer", "next"} {
		if _, _, err := journal.EnqueueInput(ctx, "s", id, []byte("existing")); err != nil {
			t.Fatal(err)
		}
		if _, err := journal.LeaseNextInput(ctx, "s", "old-owner", time.Now(), time.Minute); err != nil {
			t.Fatal(err)
		}
		if _, err := journal.MarkInputAccepted(ctx, "s", id, "old-owner"); err != nil {
			t.Fatal(err)
		}
	}
	if _, _, err := journal.EnqueueInput(ctx, "s", "pending", []byte("unsent")); err != nil {
		t.Fatal(err)
	}
	before, _ := journal.Inputs(ctx, "s")
	controller := &batchContinuationController{}
	wakes := 0
	composition := durablecomposition.AcceptedContinuation{Journal: journal, Controller: controller, Wake: func(domain.SessionID) { wakes++ }}
	reconciliation := sessionsupervisor.AcceptedTurnReconciliation{Turns: []sessionsupervisor.ReconciledAcceptedTurn{{MessageID: "next", TurnID: "b", Outcome: sessionsupervisor.AcceptedTurnUnknown}, {MessageID: "steer", TurnID: "a", Outcome: sessionsupervisor.AcceptedTurnUnknown}, {MessageID: "root", TurnID: "a", Outcome: sessionsupervisor.AcceptedTurnUnknown}}}
	if err := composition.ContinueAcceptedTurns(ctx, session, prior, reconciliation); err != nil {
		t.Fatal(err)
	}
	after, _ := journal.Inputs(ctx, "s")
	if !reflect.DeepEqual(before, after) || len(controller.calls) != 0 {
		t.Fatal("batch leased, rewrote custody, or started individual observers")
	}
	groups, err := turncontinuation.Plan(controller.members)
	if err != nil {
		t.Fatal(err)
	}
	if len(groups) != 2 || groups[0].Members[0].Input.MessageID != "root" || groups[1].Members[0].Input.MessageID != "next" {
		t.Fatal("lost root or sequential grouping")
	}
	var observations []string
	err = turncontinuation.Execute(ctx, groups, func(m turncontinuation.Member) turnprocessing.DurableInputCompletion {
		observations = append(observations, m.Input.MessageID)
		return turnprocessing.DurableInputSucceeded
	}, func(m turncontinuation.Member, outcome turnprocessing.DurableInputCompletion) error {
		return m.Callbacks.OnCompleted(ctx, turnprocessing.DurableInputProcessReceipt{SessionID: m.Input.SessionID, MessageID: m.Input.MessageID, Sequence: m.Input.Sequence, Accepted: true, Completion: outcome})
	}, func() error {
		if wakes != 0 {
			return errors.New("wake preceded lifecycle settlement")
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	controller.settled()
	journal, err = messagejournal.Open(path, messagejournal.DefaultLimits())
	if err != nil {
		t.Fatal(err)
	}
	after, _ = journal.Inputs(ctx, "s")
	if wakes != 1 || !reflect.DeepEqual(observations, []string{"root", "next"}) || !reflect.DeepEqual(before[3], after[3]) {
		t.Fatal("lost exact observation, pending preservation, or settled wake")
	}
	for _, in := range after[:3] {
		if in.Phase != messagejournal.InputCompleted {
			t.Fatal("member not completed after reopen")
		}
	}
}
