package durablecomposition

import (
	"context"
	"errors"

	"bria/internal/domain"
	"bria/internal/messagejournal"
	"bria/internal/sessionsupervisor"
	"bria/internal/turncontinuation"
	"bria/internal/turnprocessing"
)

type AcceptedContinuationController interface {
	ContinueAcceptedInput(context.Context, domain.ProviderBinding, turnprocessing.DurableLeasedInput, turnprocessing.DurableInputCallbacks) error
}

type AcceptedContinuationJournal interface {
	Inputs(context.Context, string) ([]messagejournal.Input, error)
	ResolveAcceptedInput(context.Context, string, string, uint64, messagejournal.InputPhase) (messagejournal.Input, error)
}

// AcceptedContinuation connects attached observation to existing exact custody.
// It owns neither provider submission nor a new input lease.
type AcceptedContinuation struct {
	Journal    AcceptedContinuationJournal
	Controller AcceptedContinuationController
	Wake       func(domain.SessionID)
}

func (c AcceptedContinuation) ContinueAcceptedTurns(ctx context.Context, session domain.Session, prior domain.ProviderBinding, reconciliation sessionsupervisor.AcceptedTurnReconciliation) error {
	binding, bound := session.Binding()
	if ctx == nil || c.Journal == nil || c.Controller == nil || !bound || session.ID() == "" || binding.Provider != prior.Provider || binding.SessionID != prior.SessionID || binding.Generation <= prior.Generation {
		return errors.New("accepted continuation binding is invalid")
	}
	inputs, err := c.Journal.Inputs(ctx, string(session.ID()))
	if err != nil {
		return err
	}
	seen := make(map[string]bool)
	var requests []turncontinuation.Member
	if len(reconciliation.Turns) > 512 {
		return errors.New("accepted continuation batch exceeds limit")
	}
	for _, turn := range reconciliation.Turns {
		if turn.MessageID == "" || seen[turn.MessageID] {
			return errors.New("accepted continuation receipt is invalid")
		}
		seen[turn.MessageID] = true
		if turn.Outcome != sessionsupervisor.AcceptedTurnUnknown {
			continue
		}
		var input messagejournal.Input
		for _, candidate := range inputs {
			if candidate.MessageID == turn.MessageID {
				input = candidate
				break
			}
		}
		if input.SessionID != string(session.ID()) || input.Sequence == 0 {
			return errors.New("accepted continuation input is missing")
		}
		if input.Phase == messagejournal.InputCompleted || input.Phase == messagejournal.InputTerminalFailed {
			continue
		}
		if input.Phase != messagejournal.InputAccepted && input.Phase != messagejournal.InputUnknown {
			return errors.New("accepted continuation input has no acceptance custody")
		}
		request := turnprocessing.DurableLeasedInput{SessionID: session.ID(), MessageID: input.MessageID, Sequence: input.Sequence, Payload: append([]byte(nil), input.Payload...)}
		for _, ref := range input.Attachments {
			request.Attachments = append(request.Attachments, turnprocessing.AttachmentRef{Reference: ref.Reference, Size: ref.Size, SHA256: ref.SHA256})
		}
		requests = append(requests, turncontinuation.Member{Input: request, TurnID: turn.TurnID, Accepted: input.Phase == messagejournal.InputAccepted, Callbacks: c.callbacks(request)})
	}
	if len(requests) == 0 {
		return nil
	}
	if _, err := turncontinuation.Plan(requests); err != nil {
		return err
	}
	if controller, ok := c.Controller.(interface {
		ContinueAcceptedBatch(context.Context, domain.ProviderBinding, []turncontinuation.Member, func()) error
	}); ok {
		quiet := c
		quiet.Wake = nil
		for i := range requests {
			requests[i].Callbacks = quiet.callbacks(requests[i].Input)
		}
		return controller.ContinueAcceptedBatch(ctx, binding, requests, func() {
			if c.Wake != nil {
				c.Wake(session.ID())
			}
		})
	}
	if len(requests) != 1 {
		return errors.New("accepted batch observation is unavailable")
	}
	return c.Controller.ContinueAcceptedInput(ctx, binding, requests[0].Input, requests[0].Callbacks)
}

func (c AcceptedContinuation) callbacks(input turnprocessing.DurableLeasedInput) turnprocessing.DurableInputCallbacks {
	return turncontinuation.Callbacks(input, func(ctx context.Context, completion turnprocessing.DurableInputCompletion) error {
		var outcome messagejournal.InputPhase
		switch completion {
		case turnprocessing.DurableInputSucceeded:
			outcome = messagejournal.InputCompleted
		case turnprocessing.DurableInputTerminalFailed:
			outcome = messagejournal.InputTerminalFailed
		case turnprocessing.DurableInputFailed:
			outcome = messagejournal.InputFailed
		}
		committed, err := c.Journal.ResolveAcceptedInput(context.WithoutCancel(ctx), string(input.SessionID), input.MessageID, input.Sequence, outcome)
		if err != nil {
			return err
		}
		if committed.SessionID != string(input.SessionID) || committed.MessageID != input.MessageID || committed.Sequence != input.Sequence {
			return errors.New("accepted continuation commit tuple changed")
		}
		return nil
	}, func() {
		if c.Wake != nil {
			c.Wake(input.SessionID)
		}
	})
}
