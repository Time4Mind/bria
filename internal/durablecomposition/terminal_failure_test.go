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

func TestLateExactFailureProofUnblocksLegacyFailedAndUnknownWithoutReplay(t *testing.T) {
	for _, prior := range []messagejournal.InputPhase{messagejournal.InputAccepted, messagejournal.InputUnknown, messagejournal.InputFailed} {
		t.Run(string(prior), func(t *testing.T) {
			ctx := context.Background()
			path := filepath.Join(t.TempDir(), "journal.json")
			journal, err := messagejournal.Open(path, messagejournal.DefaultLimits())
			if err != nil {
				t.Fatal(err)
			}
			for _, id := range []string{"a", "b"} {
				if _, _, err := journal.EnqueueInput(ctx, "s", id, []byte("synthetic")); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := journal.LeaseNextInput(ctx, "s", "worker", time.Unix(10, 0), time.Minute); err != nil {
				t.Fatal(err)
			}
			if _, err := journal.MarkInputAccepted(ctx, "s", "a", "worker"); err != nil {
				t.Fatal(err)
			}
			if prior == messagejournal.InputFailed {
				_, err = journal.FailInput(ctx, "s", "a")
			}
			if prior == messagejournal.InputUnknown {
				_, err = journal.MarkInputUnknown(ctx, "s", "a")
			}
			if err != nil {
				t.Fatal(err)
			}
			for pass := 0; pass < 3; pass++ {
				journal, err = messagejournal.Open(path, messagejournal.DefaultLimits())
				if err != nil {
					t.Fatal(err)
				}
				flow, err := durableflow.New(journal, nil, nil, durableflow.Options{Owner: "worker", LeaseDuration: time.Minute, Now: time.Now})
				if err != nil {
					t.Fatal(err)
				}
				turn := sessionruntime.ReconciledAcceptedTurn{MessageID: "a", Outcome: sessionruntime.AcceptedTurnFailed, TurnID: "exact-turn", TerminalFailureProven: pass > 0}
				r := durablecomposition.AcceptedTurnReconciler{Flow: flow, Histories: map[domain.Provider]sessionsupervisor.AcceptedTurnReconciler{domain.ProviderCodex: finalHistory{turn: turn, lookup: turn}}}
				if _, err := r.ReconcileAcceptedTurns(ctx, "s", domain.ProviderBinding{Provider: domain.ProviderCodex, SessionID: "thread", Generation: 1}); err != nil {
					t.Fatal(err)
				}
				inputs, err := journal.Inputs(ctx, "s")
				want := messagejournal.InputFailed
				if pass == 0 && prior == messagejournal.InputUnknown {
					want = messagejournal.InputUnknown
				}
				if pass > 0 {
					want = "terminal_failed"
				}
				if err != nil || len(inputs) != 2 || inputs[0].Phase != want {
					t.Fatalf("pass %d phases=%#v %v want=%s", pass, inputs, err, want)
				}
				if pass == 0 {
					if _, err := journal.LeaseNextInput(ctx, "s", "other", time.Unix(100, 0), time.Minute); !errors.Is(err, messagejournal.ErrNoAvailable) {
						t.Fatalf("unproven failure unblocked: %v", err)
					}
				}
			}
			next, err := journal.LeaseNextInput(ctx, "s", "other", time.Unix(100, 0), time.Minute)
			if err != nil || next.MessageID != "b" {
				t.Fatalf("late proof did not unblock exactly b: %#v %v", next, err)
			}
			if _, err := journal.RetryInput(ctx, "s", "a"); !errors.Is(err, messagejournal.ErrInvalidTransition) {
				t.Fatalf("old failure replayable: %v", err)
			}
		})
	}
}

type outcomeOnlyHistory struct {
	sessionsupervisor.AcceptedTurnReconciler
}

func TestUnprovenOrMismatchedRecoveryFailureCannotUnblockLane(t *testing.T) {
	for _, tc := range []struct {
		name     string
		lookup   sessionruntime.ReconciledAcceptedTurn
		noLookup bool
	}{
		{"legacy receipt", sessionruntime.ReconciledAcceptedTurn{MessageID: "a", Outcome: sessionruntime.AcceptedTurnFailed}, false},
		{"correlated without proof", sessionruntime.ReconciledAcceptedTurn{MessageID: "a", Outcome: sessionruntime.AcceptedTurnFailed, TurnID: "turn"}, false},
		{"missing turn", sessionruntime.ReconciledAcceptedTurn{MessageID: "a", Outcome: sessionruntime.AcceptedTurnFailed, TerminalFailureProven: true}, false},
		{"blank turn", sessionruntime.ReconciledAcceptedTurn{MessageID: "a", Outcome: sessionruntime.AcceptedTurnFailed, TurnID: " ", TerminalFailureProven: true}, false},
		{"wrong message", sessionruntime.ReconciledAcceptedTurn{MessageID: "b", Outcome: sessionruntime.AcceptedTurnFailed, TurnID: "turn", TerminalFailureProven: true}, false},
		{"wrong outcome", sessionruntime.ReconciledAcceptedTurn{MessageID: "a", Outcome: sessionruntime.AcceptedTurnCompleted, TurnID: "turn", TerminalFailureProven: true}, false},
		{"conflicting final", sessionruntime.ReconciledAcceptedTurn{MessageID: "a", Outcome: sessionruntime.AcceptedTurnFailed, TurnID: "turn", Final: "untrusted final", TerminalFailureProven: true}, false},
		{"no lookup", sessionruntime.ReconciledAcceptedTurn{}, true},
	} {
		for _, prior := range []messagejournal.InputPhase{messagejournal.InputUnknown, messagejournal.InputFailed} {
			t.Run(tc.name+"/"+string(prior), func(t *testing.T) {
				ctx := context.Background()
				path := filepath.Join(t.TempDir(), "journal.json")
				journal, err := messagejournal.Open(path, messagejournal.DefaultLimits())
				if err != nil {
					t.Fatal(err)
				}
				for _, id := range []string{"a", "b"} {
					if _, _, err := journal.EnqueueInput(ctx, "s", id, []byte("synthetic")); err != nil {
						t.Fatal(err)
					}
				}
				if _, err := journal.LeaseNextInput(ctx, "s", "worker", time.Unix(10, 0), time.Minute); err != nil {
					t.Fatal(err)
				}
				// This is an unaccepted delivery failure, not provider acceptance.
				if prior == messagejournal.InputFailed {
					_, err = journal.MarkInputDeliveryFailed(ctx, "s", "a", "worker")
				} else {
					_, err = journal.MarkInputDeliveryUnknown(ctx, "s", "a", "worker")
				}
				if err != nil {
					t.Fatal(err)
				}
				flow, err := durableflow.New(journal, nil, nil, durableflow.Options{Owner: "worker", LeaseDuration: time.Minute, Now: time.Now})
				if err != nil {
					t.Fatal(err)
				}
				var history sessionsupervisor.AcceptedTurnReconciler = finalHistory{turn: sessionruntime.ReconciledAcceptedTurn{MessageID: "a", Outcome: sessionruntime.AcceptedTurnFailed}, lookup: tc.lookup}
				if tc.noLookup {
					history = outcomeOnlyHistory{history}
				}
				r := durablecomposition.AcceptedTurnReconciler{Flow: flow, Histories: map[domain.Provider]sessionsupervisor.AcceptedTurnReconciler{domain.ProviderCodex: history}, FinalRestorer: restoreFinal(func(context.Context, domain.SessionID, string, string) error {
					t.Error("failure restored a final")
					return nil
				})}
				_, _ = r.ReconcileAcceptedTurns(ctx, "s", domain.ProviderBinding{Provider: domain.ProviderCodex, SessionID: "thread", Generation: 1})
				journal, err = messagejournal.Open(path, messagejournal.DefaultLimits())
				if err != nil {
					t.Fatal(err)
				}
				inputs, err := journal.Inputs(ctx, "s")
				if err != nil || len(inputs) != 2 || inputs[0].Phase != prior || inputs[1].Phase != messagejournal.InputPending {
					t.Fatalf("unproven failure changed phases: %#v %v", inputs, err)
				}
				if _, err := journal.LeaseNextInput(ctx, "s", "other", time.Unix(100, 0), time.Minute); !errors.Is(err, messagejournal.ErrNoAvailable) {
					t.Fatalf("unproven failure unblocked: %v", err)
				}
			})
		}
	}
}
