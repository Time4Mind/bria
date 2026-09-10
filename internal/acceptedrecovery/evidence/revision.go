// Package evidence fingerprints only metadata which can unblock accepted-turn recovery.
package evidence

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"hash"
	"sort"
	"strings"

	"bria/internal/domain"
	"bria/internal/durableflow"
	"bria/internal/sessionruntime"
	"bria/internal/sessionsupervisor"
)

var ErrUnavailable = errors.New("recovery evidence is unavailable")

type FinalLookup interface {
	LookupFinal(context.Context, domain.SessionID, domain.ProviderBinding, string) (sessionruntime.ReconciledAcceptedTurn, bool, error)
}

type Barrier struct {
	Cause    error
	Revision string
}

func (err Barrier) Error() string                         { return err.Cause.Error() }
func (err Barrier) Unwrap() error                         { return err.Cause }
func (err Barrier) StableRecoveryBarrierRevision() string { return err.Revision }

func Revision(ctx context.Context, flow *durableflow.Flow, history sessionsupervisor.AcceptedTurnReconciler, sessionID domain.SessionID, binding domain.ProviderBinding) (string, error) {
	if ctx == nil || flow == nil || history == nil || strings.TrimSpace(string(sessionID)) == "" {
		return "", ErrUnavailable
	}
	inputs, err := flow.RecoveryInputs(ctx, string(sessionID))
	if err != nil {
		return "", err
	}
	native, err := history.ReconcileAcceptedTurns(ctx, sessionID, binding)
	if err != nil {
		return "", err
	}
	turns := append([]sessionsupervisor.ReconciledAcceptedTurn(nil), native.Turns...)
	sort.Slice(turns, func(i, j int) bool {
		if turns[i].MessageID != turns[j].MessageID {
			return turns[i].MessageID < turns[j].MessageID
		}
		if turns[i].TurnID != turns[j].TurnID {
			return turns[i].TurnID < turns[j].TurnID
		}
		return turns[i].Outcome < turns[j].Outcome
	})
	digest := encoder{Hash: sha256.New()}
	digest.field("journal-v1")
	digest.number(uint64(len(inputs)))
	for _, input := range inputs {
		digest.field(input.MessageID, string(input.Phase), input.LeaseOwner)
		digest.number(input.Sequence)
		digest.boolean(!input.LeaseUntil.IsZero())
		if !input.LeaseUntil.IsZero() {
			digest.number(uint64(input.LeaseUntil.UnixNano()))
		}
	}
	digest.field("native-v1")
	digest.number(uint64(len(turns)))
	for _, turn := range turns {
		digest.field(turn.MessageID, turn.TurnID, string(turn.Outcome))
	}
	if lookup, ok := history.(FinalLookup); ok {
		digest.field("terminal-proof-v1")
		for _, input := range inputs {
			if !recoveryRelevant(input) {
				continue
			}
			turn, final, lookupErr := lookup.LookupFinal(ctx, sessionID, binding, input.MessageID)
			if lookupErr != nil {
				return "", lookupErr
			}
			digest.field(input.MessageID, turn.MessageID, turn.TurnID, string(turn.Outcome))
			digest.boolean(final, turn.Final != "", turn.TerminalFailureProven)
		}
	}
	return hex.EncodeToString(digest.Sum(nil)), nil
}

func recoveryRelevant(input durableflow.RecoveryInput) bool {
	return input.Phase == "accepted" || input.Phase == "unknown" || input.Phase == "failed" || input.Phase == "pending" && input.LeaseOwner != ""
}

type encoder struct{ hash.Hash }

func (digest encoder) field(values ...string) {
	for _, value := range values {
		digest.number(uint64(len(value)))
		_, _ = digest.Write([]byte(value))
	}
}

func (digest encoder) number(value uint64) {
	var data [8]byte
	binary.BigEndian.PutUint64(data[:], value)
	_, _ = digest.Write(data[:])
}

func (digest encoder) boolean(values ...bool) {
	for _, value := range values {
		if value {
			_, _ = digest.Write([]byte{1})
		} else {
			_, _ = digest.Write([]byte{0})
		}
	}
}
