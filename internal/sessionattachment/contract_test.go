package sessionattachment_test

import (
	"bria/internal/sessionattachment"
	"errors"
	"testing"
)

func TestReconciliationProofRemainsExactAndFailClosed(t *testing.T) {
	for _, outcome := range []sessionattachment.AcceptedTurnOutcome{sessionattachment.AcceptedTurnCompleted, sessionattachment.AcceptedTurnFailed, sessionattachment.AcceptedTurnUnknown} {
		if err := sessionattachment.ValidateReconciliation(sessionattachment.AcceptedTurnReconciliation{Turns: []sessionattachment.ReconciledAcceptedTurn{{MessageID: "exact-input", Outcome: outcome}}}); err != nil {
			t.Fatal(err)
		}
	}
	for _, turns := range [][]sessionattachment.ReconciledAcceptedTurn{
		{{MessageID: "", Outcome: sessionattachment.AcceptedTurnCompleted}},
		{{MessageID: " exact-input", Outcome: sessionattachment.AcceptedTurnCompleted}},
		{{MessageID: "exact-input", Outcome: "assumed_completed"}},
		{{MessageID: "same", Outcome: sessionattachment.AcceptedTurnCompleted}, {MessageID: "same", Outcome: sessionattachment.AcceptedTurnUnknown}},
	} {
		if err := sessionattachment.ValidateReconciliation(sessionattachment.AcceptedTurnReconciliation{Turns: turns}); !errors.Is(err, sessionattachment.ErrInvalidReconciliation) {
			t.Fatalf("invalid proof accepted: %v", err)
		}
	}
}
