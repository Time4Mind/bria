// Package recoverycomposition adapts neutral runtime recovery reads to supervision.
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
	read, err := reconciler.read(ctx, sessionID, binding)
	if err != nil {
		return sessionsupervisor.AcceptedTurnReconciliation{}, err
	}
	result := sessionsupervisor.AcceptedTurnReconciliation{Turns: make([]sessionsupervisor.ReconciledAcceptedTurn, len(read.Turns))}
	for index, turn := range read.Turns {
		result.Turns[index] = sessionsupervisor.ReconciledAcceptedTurn{MessageID: turn.MessageID, Outcome: sessionsupervisor.AcceptedTurnOutcome(turn.Outcome), TurnID: turn.TurnID}
	}
	return result, nil
}

// LookupFinal returns exact idempotency identity and a proven publishable final.
// The bool proves only a final; exact failure proof is TerminalFailureProven.
func (reconciler *Reconciler) LookupFinal(ctx context.Context, sessionID domain.SessionID, binding domain.ProviderBinding, messageID string) (sessionruntime.ReconciledAcceptedTurn, bool, error) {
	read, err := reconciler.read(ctx, sessionID, binding)
	if err != nil {
		return sessionruntime.ReconciledAcceptedTurn{}, false, err
	}
	for _, turn := range read.Turns {
		if turn.MessageID == messageID {
			return turn, turn.Final != "", nil
		}
	}
	return sessionruntime.ReconciledAcceptedTurn{}, false, nil
}

func (reconciler *Reconciler) read(ctx context.Context, sessionID domain.SessionID, binding domain.ProviderBinding) (sessionruntime.AcceptedTurnReconciliation, error) {
	if reconciler == nil || reconciler.reader == nil || reconciler.sessions == nil || ctx == nil {
		return sessionruntime.AcceptedTurnReconciliation{}, ErrInvalidReconciliation
	}
	session, err := reconciler.sessions.Load(ctx, sessionID)
	if err != nil {
		return sessionruntime.AcceptedTurnReconciliation{}, err
	}
	currentBinding, bound := session.Binding()
	if !bound || session.ID() != sessionID || session.Provider() != binding.Provider || currentBinding != binding {
		return sessionruntime.AcceptedTurnReconciliation{}, ErrInvalidReconciliation
	}
	read, err := reconciler.reader.ReadAcceptedTurns(ctx, sessionruntime.AcceptedTurnReadRequest{
		SessionID: session.ID(),
		Provider:  session.Provider(),
		Workdir:   session.Workdir(),
		Binding:   currentBinding,
	})
	if err != nil {
		return sessionruntime.AcceptedTurnReconciliation{}, err
	}
	seen := map[string]bool{}
	for _, turn := range read.Turns {
		if turn.MessageID == "" || seen[turn.MessageID] || turn.Final != "" && (turn.TurnID == "" || turn.Outcome != sessionruntime.AcceptedTurnCompleted || len(turn.Final) > 32<<10) {
			return sessionruntime.AcceptedTurnReconciliation{}, ErrInvalidReconciliation
		}
		seen[turn.MessageID] = true
		if turn.TerminalFailureProven && (turn.Outcome != sessionruntime.AcceptedTurnFailed || turn.TurnID == "") ||
			turn.Outcome != sessionruntime.AcceptedTurnCompleted && turn.Outcome != sessionruntime.AcceptedTurnFailed && turn.Outcome != sessionruntime.AcceptedTurnUnknown {
			return sessionruntime.AcceptedTurnReconciliation{}, ErrInvalidReconciliation
		}
	}
	current, err := reconciler.sessions.Load(ctx, sessionID)
	if err != nil {
		return sessionruntime.AcceptedTurnReconciliation{}, err
	}
	if currentBinding, bound := current.Binding(); !bound || currentBinding != binding || current.ID() != session.ID() || current.Workdir() != session.Workdir() {
		return sessionruntime.AcceptedTurnReconciliation{}, ErrInvalidReconciliation
	}
	return read, nil
}

var _ sessionsupervisor.AcceptedTurnReconciler = (*Reconciler)(nil)
