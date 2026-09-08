package durablecomposition_test

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
	"bria/internal/recoverycomposition"
	"bria/internal/sessionruntime"
	"bria/internal/sessionsupervisor"
	"bria/internal/storage"
)

type recoveryRead func(context.Context, sessionruntime.AcceptedTurnReadRequest) (sessionruntime.AcceptedTurnReconciliation, error)

func (f recoveryRead) ReadAcceptedTurns(ctx context.Context, r sessionruntime.AcceptedTurnReadRequest) (sessionruntime.AcceptedTurnReconciliation, error) {
	return f(ctx, r)
}

type recoveryLoad struct{ session domain.Session }

func (l recoveryLoad) Load(context.Context, domain.SessionID) (domain.Session, error) {
	return l.session, nil
}

type restoreFinal func(context.Context, domain.SessionID, string, string) error

func (f restoreFinal) RestoreAcceptedFinal(ctx context.Context, id domain.SessionID, message, final string) error {
	return f(ctx, id, message, final)
}

func TestRecoveryRestoresFinalBeforeCommittingAcceptedOutcome(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	journalPath, statePath := filepath.Join(dir, "journal.json"), filepath.Join(dir, "state.json")
	journal, err := messagejournal.Open(journalPath, messagejournal.DefaultLimits())
	if err != nil {
		t.Fatal(err)
	}
	flow, err := durableflow.New(journal, nil, nil, durableflow.Options{Owner: "worker", LeaseDuration: time.Minute, Now: func() time.Time { return time.Unix(210, 0) }})
	if err != nil {
		t.Fatal(err)
	}
	store, err := storage.OpenSessionStore(statePath)
	if err != nil {
		t.Fatal(err)
	}
	session, err := domain.NewStartingSessionAt("logical", "intent", "computer", domain.ProviderCodex, "/work", time.Unix(1, 0), domain.SessionLifetimeNever)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err = store.PutStartingIfAbsent(ctx, session); err != nil {
		t.Fatal(err)
	}
	if err = store.SetCardPrompt(ctx, session.ID(), "m", "Synthetic prompt"); err != nil {
		t.Fatal(err)
	}
	binding := domain.ProviderBinding{Provider: domain.ProviderCodex, SessionID: "thread", Generation: 1}
	session, err = session.ReadyAt(binding, time.Unix(2, 0))
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err = journal.EnqueueInput(ctx, "logical", "m", []byte("Synthetic prompt")); err != nil {
		t.Fatal(err)
	}
	if _, err = journal.LeaseNextInput(ctx, "logical", "worker", time.Unix(200, 0), time.Minute); err != nil {
		t.Fatal(err)
	}
	if _, err = journal.MarkInputAccepted(ctx, "logical", "m", "worker"); err != nil {
		t.Fatal(err)
	}
	history, err := recoverycomposition.NewReconciler(recoveryRead(func(context.Context, sessionruntime.AcceptedTurnReadRequest) (sessionruntime.AcceptedTurnReconciliation, error) {
		return sessionruntime.AcceptedTurnReconciliation{Turns: []sessionruntime.ReconciledAcceptedTurn{{MessageID: "m", Outcome: sessionruntime.AcceptedTurnCompleted, TurnID: "turn", Final: "Recovered final"}}}, nil
	}), recoveryLoad{session})
	if err != nil {
		t.Fatal(err)
	}
	reconciler := durablecomposition.AcceptedTurnReconciler{Flow: flow, Histories: map[domain.Provider]sessionsupervisor.AcceptedTurnReconciler{domain.ProviderCodex: history}, FinalRestorer: restoreFinal(func(ctx context.Context, id domain.SessionID, message, final string) error {
		opened, err := messagejournal.Open(journalPath, messagejournal.DefaultLimits())
		if err != nil {
			return err
		}
		inputs, err := opened.Inputs(ctx, "logical")
		if err != nil {
			return err
		}
		if len(inputs) != 1 || inputs[0].Phase != messagejournal.InputAccepted {
			return errors.New("completed before final persistence")
		}
		return store.RestoreAcceptedFinal(ctx, id, message, final)
	})}
	for i := 0; i < 2; i++ {
		if _, err = reconciler.ReconcileAcceptedTurns(ctx, "logical", binding); err != nil {
			t.Fatal(err)
		}
	}
	reopened, err := storage.OpenSessionStore(statePath)
	if err != nil {
		t.Fatal(err)
	}
	blocks, err := reopened.LoadCardTranscript(ctx, "logical", true)
	if err != nil || len(blocks) != 2 || blocks[1].Kind != "final" || blocks[1].Text != "Recovered final" {
		t.Fatalf("completed receipt lost final: %#v %v", blocks, err)
	}
	inputs, err := journal.Inputs(ctx, "logical")
	if err != nil || inputs[0].Phase != messagejournal.InputCompleted {
		t.Fatalf("terminal not committed: %#v %v", inputs, err)
	}
}
