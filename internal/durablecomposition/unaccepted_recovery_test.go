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
)

func TestLeasedPendingRecoveryRequiresExactTerminalProof(t *testing.T) {
	for _, tc := range []struct {
		name string
		turn sessionruntime.ReconciledAcceptedTurn
		want messagejournal.InputPhase
	}{
		{"unknown", sessionruntime.ReconciledAcceptedTurn{MessageID: "a", Outcome: sessionruntime.AcceptedTurnUnknown, TurnID: "turn"}, messagejournal.InputPending},
		{"legacy completed", sessionruntime.ReconciledAcceptedTurn{MessageID: "a", Outcome: sessionruntime.AcceptedTurnCompleted}, messagejournal.InputPending},
		{"unproven failed", sessionruntime.ReconciledAcceptedTurn{MessageID: "a", Outcome: sessionruntime.AcceptedTurnFailed, TurnID: "turn"}, messagejournal.InputPending},
		{"exact final", sessionruntime.ReconciledAcceptedTurn{MessageID: "a", Outcome: sessionruntime.AcceptedTurnCompleted, TurnID: "turn", Final: "exact final"}, messagejournal.InputCompleted},
		{"exact failed", sessionruntime.ReconciledAcceptedTurn{MessageID: "a", Outcome: sessionruntime.AcceptedTurnFailed, TurnID: "turn", TerminalFailureProven: true}, messagejournal.InputTerminalFailed},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			path := filepath.Join(t.TempDir(), "journal.json")
			journal, err := messagejournal.Open(path, messagejournal.DefaultLimits())
			if err != nil {
				t.Fatal(err)
			}
			for _, id := range []string{"a", "b"} {
				if _, _, err = journal.EnqueueInput(ctx, "logical", id, []byte("synthetic")); err != nil {
					t.Fatal(err)
				}
			}
			lease, err := journal.LeaseNextInput(ctx, "logical", "old-owner", time.Unix(10, 0), time.Minute)
			if err != nil {
				t.Fatal(err)
			}
			// Process loss after ACK but before MarkInputAccepted persisted.
			journal, err = messagejournal.Open(path, messagejournal.DefaultLimits())
			if err != nil {
				t.Fatal(err)
			}
			flow, err := durableflow.New(journal, nil, nil, durableflow.Options{Owner: "new-owner", LeaseDuration: time.Minute, Now: func() time.Time { return time.Unix(999, 0) }})
			if err != nil {
				t.Fatal(err)
			}
			session, err := domain.NewStartingSession("logical", "intent", "computer", domain.ProviderCodex, "/work")
			if err != nil {
				t.Fatal(err)
			}
			session, err = session.Ready(domain.ProviderBinding{Provider: domain.ProviderCodex, SessionID: "thread", Generation: 1})
			if err != nil {
				t.Fatal(err)
			}
			saved := ""
			r := durablecomposition.AcceptedTurnReconciler{Flow: flow, Histories: map[domain.Provider]sessionsupervisor.AcceptedTurnReconciler{domain.ProviderCodex: finalHistory{turn: tc.turn, lookup: tc.turn}}, FinalRestorer: restoreFinal(func(_ context.Context, _ domain.SessionID, id, final string) error {
				if id != "a" {
					t.Fatalf("wrong final identity %q", id)
				}
				saved = final
				return nil
			})}
			guardErr := r.CheckResume(ctx, session)
			if tc.want == messagejournal.InputPending && !errors.Is(guardErr, sessionsupervisor.ErrReconciliationRequired) || tc.want != messagejournal.InputPending && guardErr != nil {
				t.Fatalf("unsafe resume: %v", guardErr)
			}
			opened, err := messagejournal.Open(path, messagejournal.DefaultLimits())
			if err != nil {
				t.Fatal(err)
			}
			inputs, err := opened.Inputs(ctx, "logical")
			if err != nil || len(inputs) != 2 || inputs[0].Phase != tc.want || inputs[1].Phase != messagejournal.InputPending {
				t.Fatalf("recovered custody: %+v %v", inputs, err)
			}
			if tc.want == messagejournal.InputPending && (inputs[0].Lease != lease.Lease || saved != "") {
				t.Fatalf("uncertain ACK lease/final changed: %+v", inputs[0])
			}
			if tc.want == messagejournal.InputCompleted && saved != "exact final" {
				t.Fatal("completed without saving exact final")
			}
		})
	}
}
