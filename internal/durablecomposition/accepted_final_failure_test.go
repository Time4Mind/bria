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

type finalHistory struct {
	turn   sessionruntime.ReconciledAcceptedTurn
	lookup sessionruntime.ReconciledAcceptedTurn
}

func (h finalHistory) ReconcileAcceptedTurns(context.Context, domain.SessionID, domain.ProviderBinding) (sessionsupervisor.AcceptedTurnReconciliation, error) {
	return sessionsupervisor.AcceptedTurnReconciliation{Turns: []sessionsupervisor.ReconciledAcceptedTurn{{MessageID: h.turn.MessageID, Outcome: sessionsupervisor.AcceptedTurnOutcome(h.turn.Outcome)}}}, nil
}

func (h finalHistory) LookupFinal(context.Context, domain.SessionID, domain.ProviderBinding, string) (sessionruntime.ReconciledAcceptedTurn, bool, error) {
	return h.lookup, h.lookup.Final != "", nil
}

func TestRecoveryFinalSinkFailureAndUnknownNeverCompleteOrReplay(t *testing.T) {
	for _, tc := range []struct {
		name              string
		outcome           sessionruntime.AcceptedTurnOutcome
		final             string
		stale             bool
		want              messagejournal.InputPhase
		previouslyUnknown bool
		missingSink       bool
	}{
		{"sink failure", sessionruntime.AcceptedTurnCompleted, "proven final", false, messagejournal.InputAccepted, false, false},
		{"stale proof", sessionruntime.AcceptedTurnCompleted, "", true, messagejournal.InputAccepted, false, false},
		{"unknown", sessionruntime.AcceptedTurnUnknown, "", false, messagejournal.InputAccepted, false, false},
		{"completed without final", sessionruntime.AcceptedTurnCompleted, "", false, messagejournal.InputCompleted, false, false},
		{"failed without final", sessionruntime.AcceptedTurnFailed, "", false, messagejournal.InputFailed, false, false},
		{"unknown needs final proof", sessionruntime.AcceptedTurnCompleted, "", false, messagejournal.InputUnknown, true, false},
		{"correlated requires sink", sessionruntime.AcceptedTurnCompleted, "proven final", false, messagejournal.InputAccepted, false, true},
		{"legacy without sink", sessionruntime.AcceptedTurnCompleted, "", false, messagejournal.InputCompleted, false, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			path := filepath.Join(t.TempDir(), "journal.json")
			journal, err := messagejournal.Open(path, messagejournal.DefaultLimits())
			if err != nil {
				t.Fatal(err)
			}
			if _, _, err = journal.EnqueueInput(ctx, "logical", "m", []byte("synthetic")); err != nil {
				t.Fatal(err)
			}
			if _, err = journal.LeaseNextInput(ctx, "logical", "worker", time.Unix(200, 0), time.Minute); err != nil {
				t.Fatal(err)
			}
			if tc.previouslyUnknown {
				_, err = journal.MarkInputDeliveryUnknown(ctx, "logical", "m", "worker")
			} else {
				_, err = journal.MarkInputAccepted(ctx, "logical", "m", "worker")
			}
			if err != nil {
				t.Fatal(err)
			}
			flow, err := durableflow.New(journal, nil, nil, durableflow.Options{Owner: "worker", LeaseDuration: time.Minute, Now: func() time.Time { return time.Unix(210, 0) }})
			if err != nil {
				t.Fatal(err)
			}
			turn := sessionruntime.ReconciledAcceptedTurn{MessageID: "m", Outcome: tc.outcome, Final: tc.final}
			if tc.final != "" {
				turn.TurnID = "turn"
			}
			lookup := turn
			if tc.stale {
				lookup.Outcome = sessionruntime.AcceptedTurnUnknown
			}
			r := durablecomposition.AcceptedTurnReconciler{Flow: flow, Histories: map[domain.Provider]sessionsupervisor.AcceptedTurnReconciler{domain.ProviderCodex: finalHistory{turn: turn, lookup: lookup}}, FinalRestorer: restoreFinal(func(context.Context, domain.SessionID, string, string) error {
				return errors.New("synthetic persistence failure")
			})}
			if tc.missingSink {
				r.FinalRestorer = nil
			}
			_, err = r.ReconcileAcceptedTurns(ctx, "logical", domain.ProviderBinding{Provider: domain.ProviderCodex, SessionID: "thread", Generation: 1})
			if (err != nil) != (tc.final != "" || tc.stale || tc.previouslyUnknown) {
				t.Fatalf("unexpected recovery result: %v", err)
			}
			reopened, err := messagejournal.Open(path, messagejournal.DefaultLimits())
			if err != nil {
				t.Fatal(err)
			}
			inputs, err := reopened.Inputs(ctx, "logical")
			if err != nil || len(inputs) != 1 || inputs[0].Phase != tc.want {
				t.Fatalf("unsafe journal phase: %#v %v", inputs, err)
			}
			if _, err = reopened.LeaseNextInput(ctx, "logical", "other", time.Unix(999, 0), time.Minute); !errors.Is(err, messagejournal.ErrNoAvailable) {
				t.Fatalf("accepted input replayable: %v", err)
			}
		})
	}
}
