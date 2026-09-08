package messagejournal

import "context"

// ResolveUnknownInput commits newly proven history for the exact durable tuple.
// It never leases or requeues input; ordinary completion remains accepted-only.
func (journal *Journal) ResolveUnknownInput(ctx context.Context, sessionID, messageID string, sequence uint64, outcome InputPhase) (Input, error) {
	if sequence == 0 || outcome != InputCompleted && outcome != InputFailed && outcome != InputTerminalFailed {
		return Input{}, ErrInvalidTransition
	}
	return journal.transitionInputOutcome(ctx, sessionID, messageID, sequence, outcome, InputUnknown)
}

// ResolveAcceptedInput commits an exact asynchronous result, including one that
// arrives after recovery sealed the acceptance unknown. It never requeues input.
func (journal *Journal) ResolveAcceptedInput(ctx context.Context, sessionID, messageID string, sequence uint64, outcome InputPhase) (Input, error) {
	if sequence == 0 || outcome != InputCompleted && outcome != InputFailed && outcome != InputUnknown && outcome != InputTerminalFailed {
		return Input{}, ErrInvalidTransition
	}
	return journal.transitionInputOutcome(ctx, sessionID, messageID, sequence, outcome, InputAccepted)
}

func (journal *Journal) transitionInputOutcome(ctx context.Context, sessionID, messageID string, sequence uint64, outcome, from InputPhase) (Input, error) {
	return journal.transitionInput(ctx, sessionID, messageID, func(record *inputRecord) error {
		if sequence != 0 && record.Sequence != sequence {
			return ErrInvalidTransition
		}
		if record.Phase == outcome || record.Phase == InputAccepted && outcome == InputUnknown {
			return errNoMutation
		}
		if record.Phase != from && !(sequence != 0 && from == InputAccepted && (record.Phase == InputUnknown || record.Phase == InputFailed && outcome == InputTerminalFailed)) {
			return ErrInvalidTransition
		}
		record.Phase = outcome
		return nil
	})
}
