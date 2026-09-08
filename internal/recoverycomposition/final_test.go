package recoverycomposition_test

import (
	"context"
	"errors"
	"testing"

	"bria/internal/domain"
	"bria/internal/recoverycomposition"
	"bria/internal/sessionruntime"
)

func TestLookupFinalRetainsExactOutcomeAndRejectsAmbiguousProof(t *testing.T) {
	binding := domain.ProviderBinding{Provider: domain.ProviderCodex, SessionID: "thread", Generation: 1}
	session := readySession(t, "logical", "/work", binding)
	for _, tc := range []struct {
		name            string
		turns           []sessionruntime.ReconciledAcceptedTurn
		proven, invalid bool
	}{
		{"exact", []sessionruntime.ReconciledAcceptedTurn{{MessageID: "m", Outcome: sessionruntime.AcceptedTurnCompleted, TurnID: "turn", Final: "Final"}}, true, false},
		{"old completed", []sessionruntime.ReconciledAcceptedTurn{{MessageID: "m", Outcome: sessionruntime.AcceptedTurnCompleted}}, false, false},
		{"old failed", []sessionruntime.ReconciledAcceptedTurn{{MessageID: "m", Outcome: sessionruntime.AcceptedTurnFailed}}, false, false},
		{"old unknown", []sessionruntime.ReconciledAcceptedTurn{{MessageID: "m", Outcome: sessionruntime.AcceptedTurnUnknown}}, false, false},
		{"proven failure", []sessionruntime.ReconciledAcceptedTurn{{MessageID: "m", Outcome: sessionruntime.AcceptedTurnFailed, TurnID: "turn", TerminalFailureProven: true}}, false, false},
		{"failure proof without turn", []sessionruntime.ReconciledAcceptedTurn{{MessageID: "m", Outcome: sessionruntime.AcceptedTurnFailed, TerminalFailureProven: true}}, false, true},
		{"unknown failure proof", []sessionruntime.ReconciledAcceptedTurn{{MessageID: "m", Outcome: sessionruntime.AcceptedTurnUnknown, TurnID: "turn", TerminalFailureProven: true}}, false, true},
		{"completed failure proof", []sessionruntime.ReconciledAcceptedTurn{{MessageID: "m", Outcome: sessionruntime.AcceptedTurnCompleted, TurnID: "turn", TerminalFailureProven: true}}, false, true},
		{"missing correlation", []sessionruntime.ReconciledAcceptedTurn{{MessageID: "m", Outcome: sessionruntime.AcceptedTurnCompleted, Final: "Final"}}, false, true},
		{"failed final", []sessionruntime.ReconciledAcceptedTurn{{MessageID: "m", Outcome: sessionruntime.AcceptedTurnFailed, TurnID: "turn", Final: "Final"}}, false, true},
		{"unknown final", []sessionruntime.ReconciledAcceptedTurn{{MessageID: "m", Outcome: sessionruntime.AcceptedTurnUnknown, TurnID: "turn", Final: "Final"}}, false, true},
		{"duplicate identity", []sessionruntime.ReconciledAcceptedTurn{{MessageID: "m", Outcome: sessionruntime.AcceptedTurnUnknown}, {MessageID: "m", Outcome: sessionruntime.AcceptedTurnCompleted, TurnID: "turn", Final: "Final"}}, false, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r, err := recoverycomposition.NewReconciler(acceptedTurnReaderFunc(func(context.Context, sessionruntime.AcceptedTurnReadRequest) (sessionruntime.AcceptedTurnReconciliation, error) {
				return sessionruntime.AcceptedTurnReconciliation{Turns: tc.turns}, nil
			}), sessionLoaderFunc(func(context.Context, domain.SessionID) (domain.Session, error) { return session, nil }))
			if err != nil {
				t.Fatal(err)
			}
			supervised, err := r.ReconcileAcceptedTurns(context.Background(), "logical", binding)
			if tc.invalid {
				if !errors.Is(err, recoverycomposition.ErrInvalidReconciliation) {
					t.Fatal("invalid proof passed supervisor projection validation")
				}
			} else if err != nil || len(supervised.Turns) != 1 || string(supervised.Turns[0].Outcome) != string(tc.turns[0].Outcome) {
				t.Fatal("supervisor projection changed the legacy outcome enum")
			}
			for i := 0; i < 2; i++ {
				got, proven, err := r.LookupFinal(context.Background(), "logical", binding, "m")
				if tc.invalid {
					if !errors.Is(err, recoverycomposition.ErrInvalidReconciliation) {
						t.Fatalf("ambiguous proof accepted: %#v %v", got, err)
					}
					continue
				}
				if err != nil || proven != tc.proven || got != tc.turns[0] {
					t.Fatalf("lookup=%#v %v %v", got, proven, err)
				}
				missing, proven, err := r.LookupFinal(context.Background(), "logical", binding, "absent")
				if err != nil || proven || missing.MessageID != "" {
					t.Fatal("substituted another request")
				}
			}
		})
	}
}

func TestLookupFinalRejectsBindingChangedDuringRead(t *testing.T) {
	binding := domain.ProviderBinding{Provider: domain.ProviderCodex, SessionID: "thread", Generation: 1}
	current := readySession(t, "logical", "/work", binding)
	nextBinding := binding
	nextBinding.Generation = 2
	next := readySession(t, "logical", "/work", nextBinding)
	r, err := recoverycomposition.NewReconciler(acceptedTurnReaderFunc(func(context.Context, sessionruntime.AcceptedTurnReadRequest) (sessionruntime.AcceptedTurnReconciliation, error) {
		current = next
		return sessionruntime.AcceptedTurnReconciliation{Turns: []sessionruntime.ReconciledAcceptedTurn{{MessageID: "m", Outcome: sessionruntime.AcceptedTurnCompleted, TurnID: "turn", Final: "stale"}}}, nil
	}), sessionLoaderFunc(func(context.Context, domain.SessionID) (domain.Session, error) { return current, nil }))
	if err != nil {
		t.Fatal(err)
	}
	if _, proven, err := r.LookupFinal(context.Background(), "logical", binding, "m"); proven || !errors.Is(err, recoverycomposition.ErrInvalidReconciliation) {
		t.Fatalf("stale read published: %v %v", proven, err)
	}
}
