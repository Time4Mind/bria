package acceptedrecovery_test

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"bria/internal/acceptedrecovery"
	"bria/internal/domain"
	"bria/internal/durablecomposition"
	"bria/internal/durableflow"
	"bria/internal/messagejournal"
	"bria/internal/recoverycomposition"
	"bria/internal/sessionruntime"
	"bria/internal/sessionsupervisor"
	"bria/internal/storage"
)

func TestCompletedNativeGroupKeepsOldestFinalRootAcrossJournalReopen(t *testing.T) {
	for _, rootCompleted := range []bool{false, true} {
		t.Run(map[bool]string{false: "both-accepted", true: "root-already-completed"}[rootCompleted], func(t *testing.T) {
			ctx := context.Background()
			dir := t.TempDir()
			statePath, journalPath := filepath.Join(dir, "state.json"), filepath.Join(dir, "journal.json")
			state, err := storage.OpenSessionStore(statePath)
			if err != nil {
				t.Fatal(err)
			}
			starting, err := domain.NewStartingSession("logical", "intent", "local", domain.ProviderCodex, dir)
			if err != nil {
				t.Fatal(err)
			}
			if _, _, err := state.PutStartingIfAbsent(ctx, starting); err != nil {
				t.Fatal(err)
			}
			binding := domain.ProviderBinding{Provider: domain.ProviderCodex, SessionID: "native", Generation: 1}
			ready, err := starting.Ready(binding)
			if err != nil {
				t.Fatal(err)
			}
			if err := state.Replace(ctx, starting, ready); err != nil {
				t.Fatal(err)
			}
			journal, err := messagejournal.Open(journalPath, messagejournal.DefaultLimits())
			if err != nil {
				t.Fatal(err)
			}
			for _, id := range []string{"root", "steer", "different-turn"} {
				if err := state.SetCardPrompt(ctx, "logical", id, id+" prompt"); err != nil {
					t.Fatal(err)
				}
				if _, _, err := journal.EnqueueInput(ctx, "logical", id, []byte("existing payload")); err != nil {
					t.Fatal(err)
				}
				if _, err := journal.LeaseNextInput(ctx, "logical", "worker", time.Now(), time.Minute); err != nil {
					t.Fatal(err)
				}
				if _, err := journal.MarkInputAccepted(ctx, "logical", id, "worker"); err != nil {
					t.Fatal(err)
				}
			}
			flow, err := durableflow.New(journal, nil, nil, durableflow.Options{Owner: "worker", LeaseDuration: time.Minute, Now: time.Now})
			if err != nil {
				t.Fatal(err)
			}
			restorer := durablecomposition.RecoveredFinalRestorer{History: state, Output: durablecomposition.OutputCustody{Flow: flow, OwnerPrivateChatID: 42}}
			if rootCompleted {
				if err := restorer.RestoreAcceptedFinal(ctx, "logical", "root", "same final text"); err != nil {
					t.Fatal(err)
				}
				if _, err := journal.ResolveAcceptedInput(ctx, "logical", "root", 1, messagejournal.InputCompleted); err != nil {
					t.Fatal(err)
				}
			}
			journal, err = messagejournal.Open(journalPath, messagejournal.DefaultLimits())
			if err != nil {
				t.Fatal(err)
			}
			flow, err = durableflow.New(journal, nil, nil, durableflow.Options{Owner: "reopened", LeaseDuration: time.Minute, Now: time.Now})
			if err != nil {
				t.Fatal(err)
			}
			restorer.Output.Flow = flow
			history, err := recoverycomposition.NewReconciler(recoveryRead(func(context.Context, sessionruntime.AcceptedTurnReadRequest) (sessionruntime.AcceptedTurnReconciliation, error) {
				return sessionruntime.AcceptedTurnReconciliation{Turns: []sessionruntime.ReconciledAcceptedTurn{{MessageID: "steer", TurnID: "shared", Outcome: sessionruntime.AcceptedTurnCompleted, Final: "same final text"}, {MessageID: "different-turn", TurnID: "separate", Outcome: sessionruntime.AcceptedTurnCompleted, Final: "same final text"}, {MessageID: "root", TurnID: "shared", Outcome: sessionruntime.AcceptedTurnCompleted, Final: "same final text"}}}, nil
			}), state)
			if err != nil {
				t.Fatal(err)
			}
			reconciler := acceptedrecovery.AcceptedTurnReconciler{Flow: flow, Histories: map[domain.Provider]sessionsupervisor.AcceptedTurnReconciler{domain.ProviderCodex: history}, FinalRestorer: restorer}
			for i := 0; i < 2; i++ {
				if _, err := reconciler.ReconcileAcceptedTurns(ctx, "logical", binding); err != nil {
					t.Fatal(err)
				}
			}
			outputs, err := journal.Outputs(ctx, "logical")
			if err != nil {
				t.Fatal(err)
			}
			if len(outputs) != 2 || outputs[0].OperationID != "root:final" || outputs[1].OperationID != "different-turn:final" {
				t.Fatalf("native group created extra final identities: %d outputs", len(outputs))
			}
			state, err = storage.OpenSessionStore(statePath)
			if err != nil {
				t.Fatal(err)
			}
			blocks, err := state.LoadCardTranscript(ctx, "logical", true)
			if err != nil {
				t.Fatal(err)
			}
			finals := 0
			for _, block := range blocks {
				if block.Kind == "final" {
					finals++
					if block.FinalOperationID == "steer:final" {
						t.Fatal("steer became a duplicate final carrier")
					}
				}
			}
			if finals != 2 {
				t.Fatalf("persisted final blocks=%d want=2", finals)
			}
			inputs, err := journal.Inputs(ctx, "logical")
			if err != nil {
				t.Fatal(err)
			}
			for _, input := range inputs {
				if input.Phase != messagejournal.InputCompleted {
					t.Fatal("group member did not complete")
				}
			}
		})
	}
}
