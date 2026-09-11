// Package acceptedcontinuation selects whether retained accepted provider work
// must be observed before a newer durable input can run.
package acceptedcontinuation

import (
	"context"
	"errors"

	"bria/internal/domain"
	"bria/internal/messagejournal"
	"bria/internal/sessionsupervisor"
)

type Journal interface {
	Inputs(context.Context, string) ([]messagejournal.Input, error)
}

type Selector struct {
	Journal Journal
}

// Required keeps an unresolved accepted turn observable unless a newer pending
// root is already waiting. The accepted receipt remains replay-fenced, while
// the attached session may return Ready for that successor.
func (s Selector) Required(ctx context.Context, session domain.Session, prior domain.ProviderBinding, reconciliation sessionsupervisor.AcceptedTurnReconciliation) (bool, error) {
	binding, bound := session.Binding()
	if ctx == nil || s.Journal == nil || !bound || session.ID() == "" || binding.Provider != prior.Provider || binding.SessionID != prior.SessionID || binding.Generation <= prior.Generation {
		return false, errors.New("accepted continuation binding is invalid")
	}
	if session.Status() != domain.SessionReady {
		return true, nil
	}
	inputs, err := s.Journal.Inputs(ctx, string(session.ID()))
	if err != nil {
		return false, err
	}
	if len(reconciliation.Turns) > 512 {
		return false, errors.New("accepted continuation batch exceeds limit")
	}
	byMessage := make(map[string]messagejournal.Input, len(inputs))
	var latestAccepted uint64
	for _, input := range inputs {
		byMessage[input.MessageID] = input
		if input.Phase == messagejournal.InputAccepted && input.Sequence > latestAccepted {
			latestAccepted = input.Sequence
		}
	}
	seen := make(map[string]bool, len(reconciliation.Turns))
	for _, turn := range reconciliation.Turns {
		if turn.MessageID == "" || seen[turn.MessageID] {
			return false, errors.New("accepted continuation receipt is invalid")
		}
		seen[turn.MessageID] = true
		if turn.Outcome != sessionsupervisor.AcceptedTurnUnknown {
			continue
		}
		input, ok := byMessage[turn.MessageID]
		if !ok || input.SessionID != string(session.ID()) || input.Sequence == 0 {
			return false, errors.New("accepted continuation input is missing")
		}
		switch input.Phase {
		case messagejournal.InputCompleted, messagejournal.InputTerminalFailed, messagejournal.InputSkipped:
			continue
		case messagejournal.InputUnknown:
			if input.Sequence > latestAccepted {
				latestAccepted = input.Sequence
			}
		case messagejournal.InputAccepted:
		default:
			return false, errors.New("accepted continuation input has no acceptance custody")
		}
	}
	if latestAccepted == 0 {
		return false, nil
	}
	for _, input := range inputs {
		if input.Phase == messagejournal.InputPending && input.Sequence > latestAccepted {
			return false, nil
		}
	}
	return true, nil
}
