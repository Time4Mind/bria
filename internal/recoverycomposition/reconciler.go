// Package recoverycomposition adapts provider-neutral runtime recovery reads
// to the lifecycle supervisor without introducing a reverse package edge.
package recoverycomposition

import (
	"context"
	"errors"

	"bria/internal/domain"
	"bria/internal/sessionruntime"
	"bria/internal/sessionsupervisor"
)

var ErrInvalidReconciliation = errors.New("invalid provider accepted-turn reconciliation")

type Reconciler struct {
	reader   sessionruntime.AcceptedTurnReader
	sessions SessionLoader
}

type SessionLoader interface {
	Load(context.Context, domain.SessionID) (domain.Session, error)
}

func NewReconciler(reader sessionruntime.AcceptedTurnReader, sessions SessionLoader) (*Reconciler, error) {
	if reader == nil || sessions == nil {
		return nil, ErrInvalidReconciliation
	}
	return &Reconciler{reader: reader, sessions: sessions}, nil
}

func (reconciler *Reconciler) ReconcileAcceptedTurns(ctx context.Context, sessionID domain.SessionID, binding domain.ProviderBinding) (sessionsupervisor.AcceptedTurnReconciliation, error) {
	if reconciler == nil || reconciler.reader == nil || reconciler.sessions == nil || ctx == nil {
		return sessionsupervisor.AcceptedTurnReconciliation{}, ErrInvalidReconciliation
	}
	session, err := reconciler.sessions.Load(ctx, sessionID)
	if err != nil {
		return sessionsupervisor.AcceptedTurnReconciliation{}, err
	}
	currentBinding, bound := session.Binding()
	if !bound || session.ID() != sessionID || session.Provider() != binding.Provider || currentBinding != binding {
		return sessionsupervisor.AcceptedTurnReconciliation{}, ErrInvalidReconciliation
	}
	read, err := reconciler.reader.ReadAcceptedTurns(ctx, sessionruntime.AcceptedTurnReadRequest{
		SessionID: session.ID(),
		Provider:  session.Provider(),
		Workdir:   session.Workdir(),
		Binding:   currentBinding,
	})
	if err != nil {
		return sessionsupervisor.AcceptedTurnReconciliation{}, err
	}
	result := sessionsupervisor.AcceptedTurnReconciliation{Turns: make([]sessionsupervisor.ReconciledAcceptedTurn, len(read.Turns))}
	for index, turn := range read.Turns {
		outcome := sessionsupervisor.AcceptedTurnOutcome(turn.Outcome)
		switch outcome {
		case sessionsupervisor.AcceptedTurnCompleted, sessionsupervisor.AcceptedTurnFailed, sessionsupervisor.AcceptedTurnUnknown:
		default:
			return sessionsupervisor.AcceptedTurnReconciliation{}, ErrInvalidReconciliation
		}
		result.Turns[index] = sessionsupervisor.ReconciledAcceptedTurn{MessageID: turn.MessageID, Outcome: outcome}
	}
	return result, nil
}

var _ sessionsupervisor.AcceptedTurnReconciler = (*Reconciler)(nil)
