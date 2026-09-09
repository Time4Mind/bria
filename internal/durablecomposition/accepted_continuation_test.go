package durablecomposition_test

import (
	"context"
	"path/filepath"
	"reflect"
	"sync/atomic"
	"testing"
	"time"

	"bria/internal/domain"
	"bria/internal/durablecomposition"
	"bria/internal/messagejournal"
	"bria/internal/sessionsupervisor"
	"bria/internal/turnprocessing"
)

type continuedInput struct {
	input     turnprocessing.DurableLeasedInput
	binding   domain.ProviderBinding
	callbacks turnprocessing.DurableInputCallbacks
}
type continuationController struct{ calls []continuedInput }

type acceptancePreservingJournal struct {
	*messagejournal.Journal
	resolutions atomic.Int32
}

func (j *acceptancePreservingJournal) ResolveAcceptedInput(ctx context.Context, id, message string, sequence uint64, phase messagejournal.InputPhase) (messagejournal.Input, error) {
	j.resolutions.Add(1)
	return j.Journal.ResolveAcceptedInput(ctx, id, message, sequence, phase)
}

func (c *continuationController) ContinueAcceptedInput(_ context.Context, binding domain.ProviderBinding, input turnprocessing.DurableLeasedInput, callbacks turnprocessing.DurableInputCallbacks) error {
	c.calls = append(c.calls, continuedInput{input, binding, callbacks})
	return nil
}

func TestAcceptedContinuationCommitsExactJournalTupleWithoutLeasingOrRewriting(t *testing.T) {
	for _, outcome := range []turnprocessing.DurableInputCompletion{turnprocessing.DurableInputSucceeded, turnprocessing.DurableInputTerminalFailed, turnprocessing.DurableInputAwaitingRecovery} {
		t.Run(string(outcome), func(t *testing.T) {
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
			original, _, err := journal.EnqueueInput(ctx, "s", "accepted", []byte("encoded-existing-payload"))
			if err != nil {
				t.Fatal(err)
			}
			if _, err := journal.LeaseNextInput(ctx, "s", "old-owner", time.Now(), time.Minute); err != nil {
				t.Fatal(err)
			}
			if _, err := journal.MarkInputAccepted(ctx, "s", "accepted", "old-owner"); err != nil {
				t.Fatal(err)
			}
			if _, _, err := journal.EnqueueInput(ctx, "s", "next", []byte("queued")); err != nil {
				t.Fatal(err)
			}
			before, err := journal.Inputs(ctx, "s")
			if err != nil {
				t.Fatal(err)
			}
			controller := &continuationController{}
			wakes := 0
			preserving := &acceptancePreservingJournal{Journal: journal}
			c := durablecomposition.AcceptedContinuation{Journal: preserving, Controller: controller, Wake: func(id domain.SessionID) {
				if id != "s" {
					t.Error("wrong wake")
				}
				wakes++
			}}
			reconciliation := sessionsupervisor.AcceptedTurnReconciliation{Turns: []sessionsupervisor.ReconciledAcceptedTurn{{MessageID: "accepted", Outcome: sessionsupervisor.AcceptedTurnUnknown}}}
			invalid := sessionsupervisor.AcceptedTurnReconciliation{Turns: append(append([]sessionsupervisor.ReconciledAcceptedTurn(nil), reconciliation.Turns...), sessionsupervisor.ReconciledAcceptedTurn{MessageID: "missing", Outcome: sessionsupervisor.AcceptedTurnUnknown})}
			if err := c.ContinueAcceptedTurns(ctx, session, prior, invalid); err == nil || len(controller.calls) != 0 {
				t.Fatal("invalid batch partially started observation")
			}
			if err := c.ContinueAcceptedTurns(ctx, session, prior, reconciliation); err != nil {
				t.Fatal(err)
			}
			if len(controller.calls) != 1 {
				t.Fatal("accepted input not continued")
			}
			call := controller.calls[0]
			if call.binding != binding || call.input.SessionID != "s" || call.input.MessageID != "accepted" || call.input.Sequence != original.Sequence || string(call.input.Payload) != string(original.Payload) {
				t.Fatal("continuation changed exact input")
			}
			after, err := journal.Inputs(ctx, "s")
			if err != nil || !reflect.DeepEqual(after, before) {
				t.Fatal("observation acquired a lease or changed custody")
			}
			receipt := turnprocessing.DurableInputProcessReceipt{SessionID: "s", MessageID: "wrong", Sequence: original.Sequence, Accepted: true, Completion: outcome}
			if err := call.callbacks.OnCompleted(ctx, receipt); err == nil {
				t.Fatal("wrong tuple completion accepted")
			}
			receipt.MessageID = "accepted"
			for i := 0; i < 2; i++ {
				if err := call.callbacks.OnCompleted(ctx, receipt); err != nil {
					t.Fatal(err)
				}
			}
			journal, err = messagejournal.Open(path, messagejournal.DefaultLimits())
			if err != nil {
				t.Fatal(err)
			}
			after, err = journal.Inputs(ctx, "s")
			if err != nil {
				t.Fatal(err)
			}
			want := messagejournal.InputAccepted
			if outcome == turnprocessing.DurableInputSucceeded {
				want = messagejournal.InputCompleted
			}
			if outcome == turnprocessing.DurableInputTerminalFailed {
				want = messagejournal.InputTerminalFailed
			}
			if after[0].Phase != want || after[0].Sequence != original.Sequence || string(after[0].Payload) != string(original.Payload) || !reflect.DeepEqual(after[1], before[1]) {
				t.Fatal("completion lost custody or touched queued successor")
			}
			wantWakes := 1
			if want == messagejournal.InputAccepted {
				wantWakes = 0
			}
			if wakes != wantWakes {
				t.Fatalf("wakes=%d want=%d", wakes, wantWakes)
			}
			if outcome == turnprocessing.DurableInputAwaitingRecovery && preserving.resolutions.Load() != 0 {
				t.Fatal("observation gap attempted to rewrite accepted custody")
			}
			if want != messagejournal.InputAccepted {
				c.Journal = journal
				if err := c.ContinueAcceptedTurns(ctx, session, prior, reconciliation); err != nil || len(controller.calls) != 1 {
					t.Fatal("completed input observed again after reopen")
				}
			}
		})
	}
}
