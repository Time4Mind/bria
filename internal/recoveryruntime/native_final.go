package recoveryruntime

import (
	"context"

	"bria/internal/nativetranscript"
	"bria/internal/sessionruntime"
)

type nativeFinalProof struct {
	final                            string
	complete, interrupted, ambiguous bool
}

// enrichNativeTurns only consumes the public exactly bound transcript reader.
// A bounded fresh read makes repeat lookups independent of cursor state. Missing,
// malformed or inaccessible evidence leaves the saved receipt outcome intact.
func (reader *NativeReader) enrichNativeTurns(ctx context.Context, request sessionruntime.AcceptedTurnReadRequest, turns []sessionruntime.ReconciledAcceptedTurn) error {
	if reader.transcriptRoot == "" {
		return nil
	}
	proofs := map[string]*nativeFinalProof{}
	for _, turn := range turns {
		if turn.TurnID != "" {
			proofs[turn.TurnID] = &nativeFinalProof{}
		}
	}
	if len(proofs) == 0 {
		return nil
	}
	transcript, err := nativetranscript.Open(ctx, nativetranscript.Options{Provider: string(request.Provider), SessionID: request.Binding.SessionID, Workdir: request.Workdir, Root: reader.transcriptRoot})
	if err != nil {
		return ctx.Err()
	}
	defer transcript.Close()
	// Scan proves exhaustion of its captured prefix, including ignored batches.
	// Partial tails, malformed records and budget exhaustion discard all proof.
	err = transcript.Scan(ctx, func(events []nativetranscript.Event) error {
		for _, event := range events {
			proof := proofs[event.TurnID]
			if proof == nil {
				continue
			}
			switch event.Kind {
			case nativetranscript.KindFinal:
				if proof.complete || len(event.Text) > defaultMaxTextBytes || proof.final != "" && proof.final != event.Text {
					proof.ambiguous = true
				}
				if !proof.ambiguous {
					proof.final = event.Text
				}
			case nativetranscript.KindComplete:
				proof.complete = true
			case nativetranscript.KindInterrupted:
				proof.interrupted = true
			}
		}
		return nil
	})
	if err != nil {
		return ctx.Err()
	}
	for index := range turns {
		turn := &turns[index]
		proof := proofs[turn.TurnID]
		if proof == nil {
			continue
		}
		proven := proof.complete && proof.final != "" && !proof.ambiguous && !proof.interrupted
		failureProven := proof.interrupted && !proof.complete && !proof.ambiguous
		if turn.Outcome == sessionruntime.AcceptedTurnUnknown {
			if failureProven {
				turn.Outcome = sessionruntime.AcceptedTurnFailed
			} else if proven {
				turn.Outcome = sessionruntime.AcceptedTurnCompleted
			}
		}
		if turn.Outcome == sessionruntime.AcceptedTurnCompleted && proven {
			turn.Final = proof.final
		}
		turn.TerminalFailureProven = turn.Outcome == sessionruntime.AcceptedTurnFailed && failureProven
	}
	return nil
}
