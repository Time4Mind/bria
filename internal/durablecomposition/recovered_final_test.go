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
	"bria/internal/sessionruntime"
	"bria/internal/sessionsupervisor"
	"bria/internal/storage"
	"bria/internal/telegramcontroller"
)

type recoveredEnqueueFault struct {
	durableflow.Journal
	afterWrite bool
	check      func()
	fault      error
}

func (j recoveredEnqueueFault) EnqueueOutput(ctx context.Context, id, op, kind string, payload []byte) (messagejournal.Output, bool, error) {
	j.check()
	if !j.afterWrite {
		return messagejournal.Output{}, false, j.fault
	}
	output, inserted, err := j.Journal.EnqueueOutput(ctx, id, op, kind, payload)
	if err != nil {
		return output, inserted, err
	}
	return output, inserted, j.fault // Real atomic journal write, ambiguous return.
}

func TestRecoveredFinalEnqueueFailurePreservesAcceptedAndReopenIsIdempotent(t *testing.T) {
	for _, afterWrite := range []bool{false, true} {
		t.Run(map[bool]string{false: "before-write", true: "after-write"}[afterWrite], func(t *testing.T) {
			ctx := context.Background()
			dir := t.TempDir()
			statePath, journalPath := filepath.Join(dir, "state.json"), filepath.Join(dir, "journal.json")
			store, err := storage.OpenSessionStore(statePath)
			if err != nil {
				t.Fatal(err)
			}
			session, err := domain.NewStartingSession("logical", "intent", "local", domain.ProviderCodex, "/synthetic")
			if err != nil {
				t.Fatal(err)
			}
			if _, _, err := store.PutStartingIfAbsent(ctx, session); err != nil {
				t.Fatal(err)
			}
			if err := store.SetCardPrompt(ctx, "logical", "m", "synthetic"); err != nil {
				t.Fatal(err)
			}
			journal, err := messagejournal.Open(journalPath, messagejournal.DefaultLimits())
			if err != nil {
				t.Fatal(err)
			}
			if _, _, err := journal.EnqueueInput(ctx, "logical", "m", []byte("synthetic")); err != nil {
				t.Fatal(err)
			}
			if _, err := journal.LeaseNextInput(ctx, "logical", "worker", time.Now(), time.Minute); err != nil {
				t.Fatal(err)
			}
			if _, err := journal.MarkInputAccepted(ctx, "logical", "m", "worker"); err != nil {
				t.Fatal(err)
			}
			const final = "  Final exact 界\n"
			turn := sessionruntime.ReconciledAcceptedTurn{MessageID: "m", TurnID: "native-turn", Outcome: sessionruntime.AcceptedTurnCompleted, Final: final}
			fault := errors.New("injected output persistence failure")
			flow, err := durableflow.New(recoveredEnqueueFault{Journal: journal, afterWrite: afterWrite, fault: fault, check: func() {
				inputs, err := journal.Inputs(ctx, "logical")
				if err != nil || len(inputs) != 1 || inputs[0].Phase != messagejournal.InputAccepted {
					t.Fatal("input completed before output enqueue")
				}
				state, err := store.LoadTelegramUI(ctx)
				if err != nil {
					t.Fatal(err)
				}
				card, _ := state.Card("logical")
				if len(card.PendingFinalOperations) != 1 || card.PendingFinalOperations[0] != "m:final" || card.History[len(card.History)-1] != final {
					t.Fatal("output enqueue preceded exact final/fence persistence")
				}
			}}, nil, nil, durableflow.Options{Owner: "worker", LeaseDuration: time.Minute, Now: time.Now})
			if err != nil {
				t.Fatal(err)
			}
			wake := make(chan domain.SessionID, 1)
			reconciler := durablecomposition.AcceptedTurnReconciler{Flow: flow, Histories: map[domain.Provider]sessionsupervisor.AcceptedTurnReconciler{domain.ProviderCodex: finalHistory{turn: turn, lookup: turn}}, FinalRestorer: durablecomposition.RecoveredFinalRestorer{History: store, Output: durablecomposition.OutputCustody{Flow: flow, Wake: wake, OwnerPrivateChatID: 42}}}
			binding := domain.ProviderBinding{Provider: domain.ProviderCodex, SessionID: "native-thread", Generation: 1}
			if _, err := reconciler.ReconcileAcceptedTurns(ctx, "logical", binding); !errors.Is(err, fault) {
				t.Fatalf("enqueue failure swallowed: %v", err)
			}
			journal, err = messagejournal.Open(journalPath, messagejournal.DefaultLimits())
			if err != nil {
				t.Fatal(err)
			}
			inputs, err := journal.Inputs(ctx, "logical")
			if err != nil || len(inputs) != 1 || inputs[0].Phase != messagejournal.InputAccepted {
				t.Fatal("enqueue failure lost accepted custody")
			}
			outputs, err := journal.Outputs(ctx, "logical")
			if err != nil || len(outputs) != map[bool]int{false: 0, true: 1}[afterWrite] {
				t.Fatal("fault boundary did not preserve actual write outcome")
			}
			select {
			case <-wake:
				t.Fatal("failed receipt emitted success wake")
			default:
			}
			store, err = storage.OpenSessionStore(statePath)
			if err != nil {
				t.Fatal(err)
			}
			flow, err = durableflow.New(journal, nil, nil, durableflow.Options{Owner: "restarted", LeaseDuration: time.Minute, Now: time.Now})
			if err != nil {
				t.Fatal(err)
			}
			restored := durablecomposition.RecoveredFinalRestorer{History: store, Output: durablecomposition.OutputCustody{Flow: flow, Wake: wake, OwnerPrivateChatID: 42}}
			reconciler.Flow, reconciler.FinalRestorer = flow, restored
			if _, err := reconciler.ReconcileAcceptedTurns(ctx, "logical", binding); err != nil {
				t.Fatal(err)
			}
			select {
			case id := <-wake:
				if id != "logical" {
					t.Fatal("wake lost session identity")
				}
			default:
				t.Fatal("successful recovery omitted existing dispatcher wake")
			}
			outputs, err = journal.Outputs(ctx, "logical")
			if err != nil || len(outputs) != 1 || outputs[0].OperationID != "m:final" || outputs[0].Kind != string(telegramcontroller.NotificationFinal) || string(outputs[0].Payload) != final || outputs[0].Phase != messagejournal.OutputPending {
				t.Fatal("ambiguous retry changed payload/kind/operation or duplicated output")
			}
			inputs, err = journal.Inputs(ctx, "logical")
			if err != nil || inputs[0].Phase != messagejournal.InputCompleted {
				t.Fatal("input did not complete after durable output custody")
			}
			state, err := store.LoadTelegramUI(ctx)
			if err != nil {
				t.Fatal(err)
			}
			card, _ := state.Card("logical")
			if len(card.History) != 2 || len(card.PendingFinalOperations) != 1 {
				t.Fatal("recovery retry duplicated final/fence")
			}
		})
	}
}

func TestRecoveredFinalHistoryFailureNeverEnqueues(t *testing.T) {
	journal, err := messagejournal.Open(filepath.Join(t.TempDir(), "journal.json"), messagejournal.DefaultLimits())
	if err != nil {
		t.Fatal(err)
	}
	flow, err := durableflow.New(journal, nil, nil, durableflow.Options{Owner: "worker", LeaseDuration: time.Minute, Now: time.Now})
	if err != nil {
		t.Fatal(err)
	}
	fault := errors.New("history not durable")
	r := durablecomposition.RecoveredFinalRestorer{History: restoreFinal(func(context.Context, domain.SessionID, string, string) error { return fault }), Output: durablecomposition.OutputCustody{Flow: flow, OwnerPrivateChatID: 42}}
	if err := r.RestoreAcceptedFinal(context.Background(), "logical", "m", "answer"); !errors.Is(err, fault) {
		t.Fatal(err)
	}
	outputs, err := journal.Outputs(context.Background(), "logical")
	if err != nil || len(outputs) != 0 {
		t.Fatal("history failure enqueued output")
	}
}
