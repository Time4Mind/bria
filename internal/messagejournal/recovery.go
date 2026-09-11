package messagejournal

import (
	"context"
	"errors"
)

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
		if record.Phase == InputSkipped {
			return ErrInputSkipped
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

// BeginInputRecovery persists the exact sequence boundary of one proven
// provider recovery before the replacement process may publish Ready. While
// the boundary is open, no input from this session can be leased. Enqueue stays
// available so input accepted concurrently after the cutoff is retained.
//
// legacyBlockedOnly is used only while upgrading an already-Ready session from
// an older Bria version. It opens a boundary only when an old failed/unknown
// head has a newer pending successor, which proves that the user continued
// after the blocked request.
func (journal *Journal) BeginInputRecovery(ctx context.Context, sessionID, token string, legacyBlockedOnly bool) (bool, error) {
	if err := ctx.Err(); err != nil {
		return false, err
	}
	if err := validateOpaqueID(sessionID, journal.limits.MaxIDBytes, "session id"); err != nil {
		return false, err
	}
	if err := validateOpaqueID(token, journal.limits.MaxIDBytes, "input recovery token"); err != nil {
		return false, err
	}
	active := false
	err := journal.mutate(func(loaded *document) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		session, err := sessionAt(loaded, sessionID, false, journal.limits)
		if err != nil {
			if errors.Is(err, ErrNotFound) {
				return errNoMutation
			}
			return err
		}
		if session.Recovery != nil {
			if session.Recovery.Token == token {
				active = true
				return errNoMutation
			}
			if session.Recovery.Phase == inputRecoveryOpen {
				// A later recovery owner may take over the same durable cutoff
				// after the session state transition changed its deterministic
				// token. The cutoff is never widened; the old owner is fenced.
				session.Recovery.Token = token
				active = true
				return nil
			}
		}
		if !hasUnresolvedInputs(session.Inputs) || legacyBlockedOnly && !hasBlockedSuccessor(session.Inputs) {
			return errNoMutation
		}
		session.Recovery = &inputRecoveryRecord{
			Token: token, ThroughSequence: session.NextSequence, Phase: inputRecoveryOpen,
		}
		active = true
		return nil
	})
	if errors.Is(err, errNoMutation) {
		return active, nil
	}
	return active, err
}

// InputRecoveryOpen reports whether leasing and backup export are fenced by an
// uncommitted recovery boundary for this session.
func (journal *Journal) InputRecoveryOpen(ctx context.Context, sessionID string) (bool, error) {
	if err := ctx.Err(); err != nil {
		return false, err
	}
	if err := validateOpaqueID(sessionID, journal.limits.MaxIDBytes, "session id"); err != nil {
		return false, err
	}
	open := false
	err := journal.inspect(func(loaded document) error {
		session, err := sessionAt(&loaded, sessionID, false, journal.limits)
		if errors.Is(err, ErrNotFound) {
			return nil
		}
		if err != nil {
			return err
		}
		open = session.Recovery != nil && session.Recovery.Phase == inputRecoveryOpen
		return nil
	})
	return open, err
}

// BackupRecords returns one atomic, recovery-fenced view of both journal
// directions. A caller can never observe unresolved inputs without also
// observing an already-open recovery boundary.
func (journal *Journal) BackupRecords(ctx context.Context, sessionID string) ([]Input, []Output, error) {
	if err := ctx.Err(); err != nil {
		return nil, nil, err
	}
	if err := validateOpaqueID(sessionID, journal.limits.MaxIDBytes, "session id"); err != nil {
		return nil, nil, err
	}
	var inputs []Input
	var outputs []Output
	err := journal.inspect(func(loaded document) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		session, err := sessionAt(&loaded, sessionID, false, journal.limits)
		if errors.Is(err, ErrNotFound) {
			inputs, outputs = []Input{}, []Output{}
			return nil
		}
		if err != nil {
			return err
		}
		if session.Recovery != nil && session.Recovery.Phase == inputRecoveryOpen {
			return ErrRecoveryInProgress
		}
		inputs = make([]Input, len(session.Inputs))
		for index, record := range session.Inputs {
			inputs[index] = inputFromRecord(sessionID, record)
		}
		outputs = make([]Output, len(session.Outputs))
		for index, record := range session.Outputs {
			outputs[index] = outputFromRecord(sessionID, record)
		}
		return nil
	})
	return inputs, outputs, err
}

// CommitInputRecoverySkip atomically tombstones every unresolved input captured
// by BeginInputRecovery. Completed and terminal-failed outcomes are preserved;
// inputs enqueued after the cutoff are untouched. The committed marker remains
// durable so a crash before the session Ready write cannot capture a wider
// boundary on replay.
func (journal *Journal) CommitInputRecoverySkip(ctx context.Context, sessionID, token string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := validateOpaqueID(sessionID, journal.limits.MaxIDBytes, "session id"); err != nil {
		return err
	}
	if err := validateOpaqueID(token, journal.limits.MaxIDBytes, "input recovery token"); err != nil {
		return err
	}
	err := journal.mutate(func(loaded *document) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		session, err := sessionAt(loaded, sessionID, false, journal.limits)
		if err != nil || session.Recovery == nil {
			return ErrNotFound
		}
		if session.Recovery.Token != token {
			return ErrConflict
		}
		if session.Recovery.Phase == inputRecoveryCommitted {
			return errNoMutation
		}
		if session.Recovery.Phase != inputRecoveryOpen {
			return ErrInvalidFormat
		}
		for index := range session.Inputs {
			record := &session.Inputs[index]
			if record.Sequence > session.Recovery.ThroughSequence {
				break
			}
			switch record.Phase {
			case InputPending, InputAccepted, InputFailed, InputUnknown:
				record.Phase = InputSkipped
				record.Lease = leaseRecord{}
			case InputCompleted, InputTerminalFailed, InputSkipped:
			default:
				return ErrInvalidFormat
			}
		}
		session.Recovery.Phase = inputRecoveryCommitted
		return nil
	})
	if errors.Is(err, errNoMutation) {
		return nil
	}
	return err
}

func hasUnresolvedInputs(inputs []inputRecord) bool {
	for _, input := range inputs {
		switch input.Phase {
		case InputPending, InputAccepted, InputFailed, InputUnknown:
			return true
		}
	}
	return false
}

func hasBlockedSuccessor(inputs []inputRecord) bool {
	var blockedSequence uint64
	for _, input := range inputs {
		if blockedSequence == 0 && (input.Phase == InputFailed || input.Phase == InputUnknown) {
			blockedSequence = input.Sequence
			continue
		}
		if blockedSequence != 0 && input.Sequence > blockedSequence && input.Phase == InputPending {
			return true
		}
	}
	return false
}
