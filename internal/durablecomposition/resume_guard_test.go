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

func TestResumeGuardWaitsForAcceptedTerminalAndRejectsUnknown(t *testing.T) {
	for _, outcome := range []sessionruntime.AcceptedTurnOutcome{sessionruntime.AcceptedTurnUnknown, sessionruntime.AcceptedTurnCompleted, sessionruntime.AcceptedTurnFailed} {
		t.Run(string(outcome), func(t *testing.T) {
			ctx := context.Background()
			path := filepath.Join(t.TempDir(), "journal.json")
			journal, err := messagejournal.Open(path, messagejournal.DefaultLimits())
			if err != nil {
				t.Fatal(err)
			}
			if _, _, err = journal.EnqueueInput(ctx, "logical", "a", []byte("synthetic")); err != nil {
				t.Fatal(err)
			}
			if _, err = journal.LeaseNextInput(ctx, "logical", "worker", time.Unix(200, 0), time.Minute); err != nil {
				t.Fatal(err)
			}
			if _, err = journal.MarkInputAccepted(ctx, "logical", "a", "worker"); err != nil {
				t.Fatal(err)
			}
			journal, err = messagejournal.Open(path, messagejournal.DefaultLimits())
			if err != nil {
				t.Fatal(err)
			}
			flow, err := durableflow.New(journal, nil, nil, durableflow.Options{Owner: "worker", LeaseDuration: time.Minute, Now: func() time.Time { return time.Unix(210, 0) }})
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
			turn := sessionruntime.ReconciledAcceptedTurn{MessageID: "a", Outcome: outcome, TurnID: "exact-turn"}
			if outcome == sessionruntime.AcceptedTurnCompleted {
				turn.Final = "final"
			}
			if outcome == sessionruntime.AcceptedTurnFailed {
				turn.TerminalFailureProven = true
			}
			r := durablecomposition.AcceptedTurnReconciler{Flow: flow, Histories: map[domain.Provider]sessionsupervisor.AcceptedTurnReconciler{domain.ProviderCodex: finalHistory{turn: turn, lookup: turn}}, FinalRestorer: restoreFinal(func(context.Context, domain.SessionID, string, string) error { return nil })}
			// Before the optional app guard exists, resume has no check at all.
			var guardErr error
			if guard, ok := any(r).(interface {
				CheckResume(context.Context, domain.Session) error
			}); ok {
				guardErr = guard.CheckResume(ctx, session)
			}
			if outcome == sessionruntime.AcceptedTurnUnknown && !errors.Is(guardErr, sessionsupervisor.ErrReconciliationRequired) || outcome != sessionruntime.AcceptedTurnUnknown && guardErr != nil {
				t.Fatalf("resume guard outcome=%s error=%v", outcome, guardErr)
			}
		})
	}
}
