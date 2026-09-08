// Package acceptedrecovery reconciles accepted input custody before exact session recovery.
package acceptedrecovery

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"bria/internal/domain"
	"bria/internal/durableflow"
	"bria/internal/sessionsupervisor"
)

var errAcceptedTurnHistoryUnverifiable = errors.New("accepted turn provider history is unverifiable")

type AcceptedTurnReconciler struct {
	Flow          *durableflow.Flow
	Histories     map[domain.Provider]sessionsupervisor.AcceptedTurnReconciler
	FinalRestorer AcceptedFinalRestorer
}

func (reconciler AcceptedTurnReconciler) ReconcileAcceptedTurns(ctx context.Context, sessionID domain.SessionID, binding domain.ProviderBinding) (sessionsupervisor.AcceptedTurnReconciliation, error) {
	if reconciler.Flow == nil || strings.TrimSpace(string(sessionID)) == "" {
		return sessionsupervisor.AcceptedTurnReconciliation{}, errAcceptedTurnHistoryUnverifiable
	}
	resolver := &acceptedInputHistoryResolver{history: reconciler.Histories[binding.Provider], sessionID: sessionID, binding: binding, finals: reconciler.FinalRestorer}
	results, err := reconciler.Flow.ReconcileAcceptedInputs(ctx, string(sessionID), resolver)
	receipt := sessionsupervisor.AcceptedTurnReconciliation{Turns: make([]sessionsupervisor.ReconciledAcceptedTurn, 0, len(results))}
	for _, result := range results {
		outcome := sessionsupervisor.AcceptedTurnUnknown
		switch result.Resolution {
		case durableflow.AcceptedCompleted:
			outcome = sessionsupervisor.AcceptedTurnCompleted
		case durableflow.AcceptedFailed, durableflow.AcceptedTerminalFailed:
			outcome = sessionsupervisor.AcceptedTurnFailed
		case durableflow.AcceptedUnknown, durableflow.AcceptedPending:
		default:
			err = errors.Join(err, errAcceptedTurnHistoryUnverifiable)
		}
		receipt.Turns = append(receipt.Turns, sessionsupervisor.ReconciledAcceptedTurn{MessageID: result.MessageID, Outcome: outcome})
	}
	return receipt, err
}

type acceptedInputHistoryResolver struct {
	history   sessionsupervisor.AcceptedTurnReconciler
	sessionID domain.SessionID
	binding   domain.ProviderBinding
	loaded    bool
	outcomes  map[string]sessionsupervisor.AcceptedTurnOutcome
	err       error
	finals    AcceptedFinalRestorer
}

func (resolver *acceptedInputHistoryResolver) ResolveAccepted(ctx context.Context, input durableflow.AcceptedInput) (durableflow.AcceptedResolutionResult, error) {
	result := durableflow.AcceptedResolutionResult{SessionID: input.SessionID, MessageID: input.MessageID, Sequence: input.Sequence, Resolution: durableflow.AcceptedUnknown}
	if !input.PreviouslyUnknown && !input.PreviouslyFailed && !input.PreviouslyUnaccepted {
		result.Resolution = durableflow.AcceptedPending
	}
	if resolver == nil || input.SessionID != string(resolver.sessionID) || strings.TrimSpace(input.MessageID) == "" {
		return result, errAcceptedTurnHistoryUnverifiable
	}
	resolver.load(ctx)
	if resolver.err != nil {
		return result, resolver.err
	}
	outcome, found := resolver.outcomes[input.MessageID]
	if !found {
		return result, fmt.Errorf("%w: message %q is absent", errAcceptedTurnHistoryUnverifiable, input.MessageID)
	}
	if input.PreviouslyFailed && outcome != sessionsupervisor.AcceptedTurnFailed {
		result.Resolution = durableflow.AcceptedFailed
		return result, nil
	}
	switch outcome {
	case sessionsupervisor.AcceptedTurnCompleted:
		if err := resolver.restoreFinal(ctx, input.MessageID, input.PreviouslyUnknown || input.PreviouslyUnaccepted); err != nil {
			return result, err
		}
		result.Resolution = durableflow.AcceptedCompleted
		result.AcceptanceProven = input.PreviouslyUnaccepted || input.PreviouslyUnknown
	case sessionsupervisor.AcceptedTurnFailed:
		resolution, err := resolver.resolveFailure(ctx, input.MessageID, input.PreviouslyUnknown || input.PreviouslyUnaccepted)
		result.Resolution = resolution
		result.AcceptanceProven = resolution == durableflow.AcceptedTerminalFailed
		return result, err
	case sessionsupervisor.AcceptedTurnUnknown:
	default:
		return result, errAcceptedTurnHistoryUnverifiable
	}
	return result, nil
}

func (resolver *acceptedInputHistoryResolver) load(ctx context.Context) {
	if resolver.loaded {
		return
	}
	resolver.loaded = true
	if resolver.history == nil {
		resolver.err = errAcceptedTurnHistoryUnverifiable
		return
	}
	receipt, err := resolver.history.ReconcileAcceptedTurns(ctx, resolver.sessionID, resolver.binding)
	if err != nil {
		resolver.err = err
		return
	}
	resolver.outcomes = make(map[string]sessionsupervisor.AcceptedTurnOutcome, len(receipt.Turns))
	for _, turn := range receipt.Turns {
		if strings.TrimSpace(turn.MessageID) == "" || strings.TrimSpace(turn.MessageID) != turn.MessageID || resolver.outcomes[turn.MessageID] != "" {
			resolver.err = errAcceptedTurnHistoryUnverifiable
			return
		}
		switch turn.Outcome {
		case sessionsupervisor.AcceptedTurnCompleted, sessionsupervisor.AcceptedTurnFailed, sessionsupervisor.AcceptedTurnUnknown:
			resolver.outcomes[turn.MessageID] = turn.Outcome
		default:
			resolver.err = errAcceptedTurnHistoryUnverifiable
			return
		}
	}
}

var _ sessionsupervisor.AcceptedTurnReconciler = AcceptedTurnReconciler{}
var _ durableflow.AcceptedInputResolver = (*acceptedInputHistoryResolver)(nil)
