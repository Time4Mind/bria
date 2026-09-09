// Package sessionattachment owns exact live-terminal recovery and its acceptance proof.
package sessionattachment

import (
	"bria/internal/domain"
	"bria/internal/providerattachport"
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"
)

var ErrReconciliationRequired = errors.New("accepted turn reconciliation required")
var ErrInvalidReconciliation = errors.New("invalid accepted turn reconciliation")

// AcceptedTurnOutcome is the durable terminal disposition of one input that
// the provider had accepted before its exact process generation exited.
type AcceptedTurnOutcome string

const (
	AcceptedTurnCompleted AcceptedTurnOutcome = "completed"
	AcceptedTurnFailed    AcceptedTurnOutcome = "failed"
	AcceptedTurnUnknown   AcceptedTurnOutcome = "unknown"
)

// ReconciledAcceptedTurn is an observation, not a journal phase. Unknown means
// terminal proof is pending; a durable acceptance must remain accepted.
type ReconciledAcceptedTurn struct {
	MessageID string
	TurnID    string
	Outcome   AcceptedTurnOutcome
}

type AcceptedTurnReconciliation struct {
	Turns []ReconciledAcceptedTurn
}

// AcceptedTurnReconciler inspects provider-native history for the exact prior
// binding and returns only after every accepted input it found has been
// durably moved to completed, failed, or blocking unknown state. An empty
// receipt proves there were no accepted inputs for this logical session.
type AcceptedTurnReconciler interface {
	ReconcileAcceptedTurns(context.Context, domain.SessionID, domain.ProviderBinding) (AcceptedTurnReconciliation, error)
}

type Result struct {
	Stale            bool
	AwaitingRecovery bool
	Recovered        bool
	Archived         bool
	Deleted          bool
	RestartAttempts  int
	Session          domain.Session
	Reconciliation   AcceptedTurnReconciliation
}

type Options struct {
	Store interface {
		Replace(context.Context, domain.Session, domain.Session) error
	}
	Attacher                    providerattachport.SessionAttacher
	Abort                       func(context.Context, providerattachport.StartSessionRequest, domain.ProviderBinding) error
	Now                         func() time.Time
	AcceptedTurns               AcceptedTurnReconciler
	ShouldContinueAcceptedTurns func(context.Context, domain.Session, domain.ProviderBinding, AcceptedTurnReconciliation) (bool, error)
	ContinueAcceptedTurns       func(context.Context, domain.Session, domain.ProviderBinding, AcceptedTurnReconciliation) error
	Conflict                    func(context.Context, domain.Session, error) (Result, error)
	Archive                     func(domain.Session, time.Time) (domain.Session, error)
}

func ValidateReconciliation(reconciliation AcceptedTurnReconciliation) error {
	seen := make(map[string]struct{}, len(reconciliation.Turns))
	for _, turn := range reconciliation.Turns {
		if len(turn.TurnID) > 512 || !utf8.ValidString(turn.TurnID) || strings.TrimSpace(turn.TurnID) != turn.TurnID || strings.ContainsAny(turn.TurnID, "\x00\r\n") {
			return ErrInvalidReconciliation
		}
		if strings.TrimSpace(turn.MessageID) == "" || strings.TrimSpace(turn.MessageID) != turn.MessageID {
			return fmt.Errorf("%w: message id is invalid", ErrInvalidReconciliation)
		}
		if _, exists := seen[turn.MessageID]; exists {
			return fmt.Errorf("%w: duplicate message id %q", ErrInvalidReconciliation, turn.MessageID)
		}
		seen[turn.MessageID] = struct{}{}
		switch turn.Outcome {
		case AcceptedTurnCompleted, AcceptedTurnFailed, AcceptedTurnUnknown:
		default:
			return fmt.Errorf("%w: unsupported outcome %q", ErrInvalidReconciliation, turn.Outcome)
		}
	}
	return nil
}
