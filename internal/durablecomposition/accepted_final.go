package durablecomposition

import (
	"context"
	"strings"

	"bria/internal/domain"
	"bria/internal/durableflow"
	"bria/internal/sessionruntime"
)

// AcceptedFinalRestorer persists an exact accepted request's final idempotently
// before its durable input is committed completed. It must reject conflicts.
type AcceptedFinalRestorer interface {
	RestoreAcceptedFinal(context.Context, domain.SessionID, string, string) error
}

type acceptedFinalLookup interface {
	LookupFinal(context.Context, domain.SessionID, domain.ProviderBinding, string) (sessionruntime.ReconciledAcceptedTurn, bool, error)
}

func (resolver *acceptedInputHistoryResolver) restoreFinal(ctx context.Context, messageID string, required bool) error {
	lookup, ok := resolver.history.(acceptedFinalLookup)
	if !ok {
		if required {
			return errAcceptedTurnHistoryUnverifiable
		}
		return nil
	}
	turn, proven, err := lookup.LookupFinal(ctx, resolver.sessionID, resolver.binding, messageID)
	if err != nil {
		return err
	}
	if (required || turn.TurnID != "") && (!proven || resolver.finals == nil) || turn.MessageID != messageID || turn.Outcome != sessionruntime.AcceptedTurnCompleted || proven && (turn.TurnID == "" || turn.Final == "" || len(turn.Final) > 32<<10) {
		return errAcceptedTurnHistoryUnverifiable
	}
	if !proven || resolver.finals == nil {
		return nil
	}
	return resolver.finals.RestoreAcceptedFinal(ctx, resolver.sessionID, messageID, turn.Final)
}

func (resolver *acceptedInputHistoryResolver) resolveFailure(ctx context.Context, messageID string, wasUnknown bool) (durableflow.AcceptedResolution, error) {
	resolution := durableflow.AcceptedFailed
	if wasUnknown {
		resolution = durableflow.AcceptedUnknown
	}
	lookup, ok := resolver.history.(acceptedFinalLookup)
	if !ok {
		return resolution, nil
	}
	turn, finalProven, err := lookup.LookupFinal(ctx, resolver.sessionID, resolver.binding, messageID)
	if err != nil {
		return resolution, err
	}
	if turn.MessageID != messageID || turn.Outcome != sessionruntime.AcceptedTurnFailed || finalProven || turn.Final != "" || turn.TerminalFailureProven && strings.TrimSpace(turn.TurnID) == "" {
		return resolution, errAcceptedTurnHistoryUnverifiable
	}
	if turn.TerminalFailureProven {
		resolution = durableflow.AcceptedTerminalFailed
	}
	return resolution, nil
}
